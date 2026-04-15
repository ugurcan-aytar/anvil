package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ugurcan-aytar/anvil/internal/engine"
	"github.com/ugurcan-aytar/anvil/internal/lint"
	"github.com/ugurcan-aytar/anvil/internal/llm"
	"github.com/ugurcan-aytar/anvil/internal/wiki"
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Pipeline health check — project layout, DB, embedder, LLM backend, ANVIL.md",
	Long: `anvil doctor runs every cheap health check anvil has and reports
them as a single dashboard. Every check is independent — one
failure doesn't short-circuit the rest, so the report stays useful
on a partly-broken setup.

Fast (no LLM calls, no network). Safe to wire into a pre-commit hook
or CI if you want a "project is still anvil-shaped" guard.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runDoctor(cmd.Context())
	},
}

// runDoctor is the exported-to-tests entry point. Returns an error
// only when the overall check result is failure; individual line
// issues go through `report` and are tallied.
func runDoctor(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	fmt.Println("anvil doctor")
	fmt.Println()

	r := &doctorReport{}
	checkProject(r)
	checkWiki(r)
	checkRaw(r)
	checkRecallDB(r)
	checkEmbedder(r)
	checkLLMBackend(r)
	checkANVILmd(r)
	checkIndex(r)
	checkLog(r)

	fmt.Println()
	if r.fail == 0 {
		fmt.Println("All checks passed.")
		return nil
	}
	fmt.Printf("%d issue(s) found.\n", r.fail)
	return fmt.Errorf("doctor: %d issue(s) found", r.fail)
}

// doctorReport tallies per-check outcomes so the final summary can
// say "3 issues found" without every check tracking its own state.
type doctorReport struct {
	pass int
	fail int
}

// ok renders a green-ish "Label: …detail… ✓" line and bumps pass.
func (r *doctorReport) ok(label, detail string) {
	fmt.Printf("  %-12s %s ✓\n", label+":", detail)
	r.pass++
}

// warn emits a ⚠ line without counting toward the fail tally —
// for informational cases that don't mean the project is broken
// (e.g. "no embedder available, BM25 only").
func (r *doctorReport) warn(label, detail string) {
	fmt.Printf("  %-12s %s ⚠\n", label+":", detail)
}

// bad renders a ✗ line and bumps the fail counter so the final
// summary can show it.
func (r *doctorReport) bad(label, detail string) {
	fmt.Printf("  %-12s %s ✗\n", label+":", detail)
	r.fail++
}

// ------ checks -----------------------------------------------------

// checkProject confirms projectDir exists + resolves to an absolute
// path. A missing directory is a hard fail — every later check needs
// it and would produce confusing errors otherwise.
func checkProject(r *doctorReport) {
	abs, err := filepath.Abs(projectDir)
	if err != nil {
		r.bad("Project", fmt.Sprintf("cannot resolve %q: %v", projectDir, err))
		return
	}
	info, err := os.Stat(abs)
	if err != nil {
		r.bad("Project", fmt.Sprintf("%s — %v", abs, err))
		return
	}
	if !info.IsDir() {
		r.bad("Project", fmt.Sprintf("%s is not a directory", abs))
		return
	}
	// DB existence is informational — status also stats it.
	dbPath := filepath.Join(abs, engine.DBSubdir, engine.DBFilename)
	if _, err := os.Stat(dbPath); err == nil {
		r.ok("Project", fmt.Sprintf("%s (.anvil/index.db exists)", abs))
	} else {
		r.warn("Project", fmt.Sprintf("%s (.anvil/index.db not yet created — run `anvil init`)", abs))
	}
}

// checkANVILmd verifies the schema file, the root marker every
// `engine.Open` call checks.
func checkANVILmd(r *doctorReport) {
	path := filepath.Join(absProject(), "ANVIL.md")
	info, err := os.Stat(path)
	if err != nil {
		r.bad("ANVIL.md", fmt.Sprintf("missing (%v) — run `anvil init`", err))
		return
	}
	r.ok("ANVIL.md", fmt.Sprintf("present (%d bytes)", info.Size()))
}

// checkWiki confirms wiki/ is present and writable, plus reports
// current page count as confirmation that ListPages can parse the
// existing files.
func checkWiki(r *doctorReport) {
	wikiDir := filepath.Join(absProject(), "wiki")
	info, err := os.Stat(wikiDir)
	if err != nil {
		r.bad("Wiki", fmt.Sprintf("missing (%v)", err))
		return
	}
	if !info.IsDir() {
		r.bad("Wiki", fmt.Sprintf("%s is not a directory", wikiDir))
		return
	}
	if !isWritable(wikiDir) {
		r.bad("Wiki", fmt.Sprintf("%s not writable", wikiDir))
		return
	}
	pages, err := wiki.ListPages(wikiDir)
	if err != nil {
		r.bad("Wiki", fmt.Sprintf("list pages: %v", err))
		return
	}
	r.ok("Wiki", fmt.Sprintf("writable (%d pages)", len(pages)))
}

// checkRaw confirms raw/ exists + is readable. Missing raw/ isn't
// strictly fatal — `anvil init` creates it — but it indicates the
// project scaffolding is incomplete.
func checkRaw(r *doctorReport) {
	rawDir := filepath.Join(absProject(), "raw")
	info, err := os.Stat(rawDir)
	if err != nil {
		r.bad("Raw", fmt.Sprintf("missing (%v)", err))
		return
	}
	if !info.IsDir() {
		r.bad("Raw", fmt.Sprintf("%s is not a directory", rawDir))
		return
	}
	entries, err := os.ReadDir(rawDir)
	if err != nil {
		r.bad("Raw", fmt.Sprintf("read dir: %v", err))
		return
	}
	count := 0
	for _, e := range entries {
		if !e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			count++
		}
	}
	r.ok("Raw", fmt.Sprintf("readable (%d files)", count))
}

// checkRecallDB opens the engine once — a clean Open proves the
// SQLite DB + FTS5 extension + recall collections are all in place.
// Any error is a hard fail.
func checkRecallDB(r *doctorReport) {
	eng, err := engine.Open(projectDir)
	if err != nil {
		r.bad("Recall DB", fmt.Sprintf("open: %v", err))
		return
	}
	defer eng.Close()
	dbPath := eng.DBPath()
	info, err := os.Stat(dbPath)
	if err != nil {
		r.bad("Recall DB", fmt.Sprintf("stat: %v", err))
		return
	}
	r.ok("Recall DB", fmt.Sprintf("healthy (FTS5 loaded, %s)", humanBytesDoctor(info.Size())))
}

// checkEmbedder tells the user whether hybrid search is live. The
// engine's Embedder() returns (nil, nil) when no backend is
// compiled / configured — that's a warn, not a fail, because BM25
// still works.
func checkEmbedder(r *doctorReport) {
	eng, err := engine.Open(projectDir)
	if err != nil {
		// Project-level error already reported by checkRecallDB.
		return
	}
	defer eng.Close()

	emb, err := eng.Embedder()
	if err != nil {
		r.bad("Embedding", fmt.Sprintf("misconfigured: %v", err))
		return
	}
	if emb == nil {
		r.warn("Embedding", "no embedder available — BM25 only (set RECALL_EMBED_PROVIDER or install the local model)")
		return
	}
	// Reach into recall to count embedded chunks — gives the user a
	// real signal that `anvil ingest` produced vector data.
	r.ok("Embedding", fmt.Sprintf("%s", emb.ModelName()))
}

// checkLLMBackend picks the active backend via llm.Select without
// making a network call. Missing backend is a warn — not every
// anvil use case needs one (`lint --structural-only`, `search`).
func checkLLMBackend(r *doctorReport) {
	client, err := newLLMClient()
	if err != nil {
		if err == llm.ErrNoBackend {
			r.warn("LLM backend", "not configured — ingest / ask / save disabled")
			return
		}
		r.bad("LLM backend", err.Error())
		return
	}
	r.ok("LLM backend", client.Describe())
}

// checkIndex tallies pages against wiki/index.md — out-of-sync is a
// warn because `anvil lint --fix` rebuilds it in one call.
func checkIndex(r *doctorReport) {
	wikiDir := filepath.Join(absProject(), "wiki")
	stale, err := lint.CheckStaleIndex(wikiDir)
	if err != nil {
		r.bad("Index", err.Error())
		return
	}
	pages, _ := wiki.ListPages(wikiDir)
	if len(stale) == 0 {
		r.ok("Index", fmt.Sprintf("%d entries (synced)", len(pages)))
		return
	}
	r.warn("Index", fmt.Sprintf("%d entries (%d pages missing — run `anvil lint --fix`)", len(pages), len(stale)))
}

// checkLog counts wiki/log.md heading lines. The file doubles as
// the human-readable audit trail for ingest / ask / save / lint.
func checkLog(r *doctorReport) {
	logPath := filepath.Join(absProject(), "wiki", wiki.LogFilename)
	raw, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			r.warn("Log", "wiki/log.md missing — `anvil init` seeds it")
			return
		}
		r.bad("Log", err.Error())
		return
	}
	n := 0
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "## [") {
			n++
		}
	}
	r.ok("Log", fmt.Sprintf("%d entries", n))
}

// ------ helpers ----------------------------------------------------

// absProject lifts the package-level projectDir into an absolute
// path without re-implementing the fallback logic in every check.
func absProject() string {
	abs, err := filepath.Abs(projectDir)
	if err != nil {
		return projectDir
	}
	return abs
}

// isWritable does a cheap probe — creates a temp file, writes to
// it, removes it. Avoids parsing unix permission bits.
func isWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".anvil-doctor-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

// humanBytesDoctor mirrors the helper in status.go but stays local
// so doctor.go doesn't depend on cross-file internals.
func humanBytesDoctor(n int64) string {
	const kib = 1024
	const mib = 1024 * kib
	switch {
	case n >= mib:
		return fmt.Sprintf("%.2f MB", float64(n)/float64(mib))
	case n >= kib:
		return fmt.Sprintf("%.2f KB", float64(n)/float64(kib))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
