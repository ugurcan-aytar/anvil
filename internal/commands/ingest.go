package commands

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ugurcan-aytar/anvil/internal/engine"
	"github.com/ugurcan-aytar/anvil/internal/ingest"
	"github.com/ugurcan-aytar/anvil/internal/llm"
	"github.com/ugurcan-aytar/anvil/internal/wiki"
)

// ingestOptions are the flags riding alongside `anvil ingest`.
type ingestOptions struct {
	// DryRun stops short of calling the LLM's write prompt and
	// persisting pages. Extract + reconcile still run so users can
	// preview the shape of an ingest before paying for the heavier
	// write calls.
	DryRun bool
	// Force ignores the content-hash cache: every file is re-ingested
	// even when its bytes match a prior ingest.
	Force bool
	// Workers is the maximum number of source files whose Extract
	// call can run concurrently. 1 (default) keeps the pre-v0.2.7
	// sequential behaviour. Reconcile + write stay sequential in
	// both modes because they mutate the wiki dir + DB.
	Workers int
}

var ingestOpts ingestOptions

// newLLMClient is the factory anvil uses to obtain an llm.Client.
// Package-level so integration tests can swap it for a MockClient.
// Production code path delegates straight to llm.Select().
var newLLMClient = llm.Select

var ingestCmd = &cobra.Command{
	Use:   "ingest <file|dir|glob> [...]",
	Short: "Ingest source documents into the wiki (LLM extracts + writes pages)",
	Long: `anvil ingest reads each source, asks the LLM to extract entities /
concepts / claims, and then creates or updates wiki pages to reflect
that information. Sources are hashed so re-running ingest on an
unchanged file is a no-op.

Accepts any number of files, directories (walked recursively for .md
and .txt), or shell-expanded globs.`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runIngest(cmd.Context(), args, ingestOpts)
	},
}

func init() {
	ingestCmd.Flags().BoolVar(&ingestOpts.DryRun, "dry-run", false,
		"extract + reconcile only; do not call the LLM writer or persist pages")
	ingestCmd.Flags().BoolVarP(&ingestOpts.Force, "force", "f", false,
		"ignore the content-hash cache and re-ingest every file")
	ingestCmd.Flags().IntVarP(&ingestOpts.Workers, "workers", "w", 1,
		"max concurrent Extract calls (reconcile + write stay sequential)")
}

// runIngest is the ingest entry point. Visible to the commands package
// only so both Cobra and the integration test can drive it.
func runIngest(ctx context.Context, args []string, opts ingestOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	eng, err := engine.Open(projectDir)
	if err != nil {
		return err
	}
	defer eng.Close()

	files, err := collectSourceFiles(eng.ProjectRoot(), args)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no source files matched %v", args)
	}

	// Select the LLM once up front so a missing-backend error aborts
	// before we burn time hashing + extracting. Dry-run still wants
	// the client because extract uses it.
	client, err := newLLMClient()
	if err != nil {
		if err == llm.ErrNoBackend {
			fmt.Fprintln(os.Stderr, llm.SetupGuidance())
		}
		return err
	}
	client = wrapClient(client) // --verbose decorator (no-op otherwise)
	if verbosity >= VerbosityNormal {
		fmt.Printf("LLM backend: %s\n", client.Describe())
	}

	dbPath := eng.DBPath()
	wikiDir := eng.WikiDir()

	// Batch banner — only useful when the user pointed ingest at
	// more than a single file.
	batchStart := time.Now()
	if verbosity >= VerbosityNormal && len(files) > 1 {
		fmt.Printf("Ingesting %d files from %v...\n\n", len(files), args)
	}
	// Padding width so "[  1/95]" lines up with "[ 95/95]".
	padWidth := len(fmt.Sprintf("%d", len(files)))
	counterFmt := fmt.Sprintf("[%%%dd/%%d] ", padWidth)

	summary := ingestSummary{}
	workers := opts.Workers
	if workers < 1 {
		workers = 1
	}
	if workers == 1 {
		// Sequential path — preserves the v0.2.6 behaviour byte-for-byte.
		for i, relPath := range files {
			absPath := filepath.Join(eng.ProjectRoot(), relPath)
			if verbosity >= VerbosityNormal {
				fmt.Printf(counterFmt, i+1, len(files))
			}
			if err := ingestOne(ctx, client, dbPath, wikiDir, relPath, absPath, opts, &summary); err != nil {
				fmt.Fprintf(os.Stderr, "  ! %s: %v\n", relPath, err)
				summary.Errors++
			}
		}
	} else {
		// Parallel path — extract runs concurrently, reconcile + write
		// + log + print stay serial behind ingestMutex. Per-file output
		// therefore stays coherent; only the hidden LLM I/O interleaves.
		summary = runIngestConcurrent(ctx, client, dbPath, wikiDir, files, opts, workers, counterFmt)
	}
	batchElapsed := time.Since(batchStart).Round(time.Second)

	// Refresh the on-disk index + BM25 tables so `anvil status` /
	// `anvil search` reflect the ingest. Cheap; no reason to skip.
	if !opts.DryRun {
		if err := wiki.RebuildIndex(wikiDir); err != nil {
			fmt.Fprintf(os.Stderr, "rebuild index: %v\n", err)
		}
		if _, err := eng.Recall().Index(); err != nil {
			fmt.Fprintf(os.Stderr, "reindex collections: %v\n", err)
		}
		// Vector embed pass — makes `anvil ask` hybrid search
		// pick up the newly-written chunks. Opt-in: we only
		// embed when the engine has a usable Embedder; missing
		// backends (default build + no API provider) silently
		// skip so BM25-only users aren't blocked.
		if emb, err := eng.Embedder(); err == nil && emb != nil {
			if res, err := eng.Recall().Embed(emb, false); err != nil {
				fmt.Fprintf(os.Stderr, "warning: embedding refresh failed: %v\n", err)
			} else if res != nil && res.Embedded > 0 {
				fmt.Printf("Embedded %d chunk(s) (of %d total).\n", res.Embedded, res.Total)
			}
		}
		// Snapshot the wiki so the next `anvil diff` has a baseline
		// to compare against. Non-fatal — a failed snapshot just
		// means the next diff will show every page as "Added".
		snap, snapErr := wiki.CaptureSnapshot(wikiDir)
		if snapErr != nil {
			fmt.Fprintf(os.Stderr, "warning: wiki snapshot capture: %v\n", snapErr)
		} else {
			snapPath := filepath.Join(eng.ProjectRoot(), engine.DBSubdir, wiki.SnapshotFilename)
			if err := wiki.SaveSnapshot(snapPath, snap); err != nil {
				fmt.Fprintf(os.Stderr, "warning: wiki snapshot save: %v\n", err)
			}
		}
	}

	fmt.Println()
	fmt.Printf("Summary: %d files, %d skipped, %d created, %d updated",
		summary.Processed, summary.Skipped, summary.Created, summary.Updated)
	if summary.Errors > 0 {
		fmt.Printf(", %d error(s)", summary.Errors)
	}
	if len(files) > 1 {
		fmt.Printf(" (%s total)", batchElapsed)
	}
	fmt.Println()
	return nil
}

// ingestSummary is the aggregate displayed at the end of a batch run.
type ingestSummary struct {
	Processed int
	Skipped   int
	Created   int
	Updated   int
	Errors    int
}

// ingestOne runs the full extract → reconcile → write pipeline for a
// single source file. It also hashes the file, checks the cache, and
// records a log entry on success.
func ingestOne(
	ctx context.Context,
	client llm.Client,
	dbPath, wikiDir, relPath, absPath string,
	opts ingestOptions,
	summary *ingestSummary,
) error {
	raw, err := os.ReadFile(absPath)
	if err != nil {
		return fmt.Errorf("read source: %w", err)
	}
	if strings.TrimSpace(string(raw)) == "" {
		return fmt.Errorf("source is empty")
	}

	hash := ingest.HashBytes(raw)
	if !opts.Force {
		already, err := ingest.IsAlreadyIngested(dbPath, relPath, hash)
		if err != nil {
			return fmt.Errorf("cache check: %w", err)
		}
		if already {
			if verbosity >= VerbosityNormal {
				fmt.Printf("%s ... skipped (unchanged)\n", relPath)
			}
			summary.Skipped++
			return nil
		}
	}
	if verbosity >= VerbosityNormal {
		fmt.Printf("%s ... ", relPath)
	}
	started := time.Now()

	// Feed the current slug catalog to the LLM so it re-uses
	// canonical forms ("nocodedevs" not "nocode-devs"). Failures
	// here degrade to nil — the prompt template skips the section
	// and the ingest still runs.
	var existingSlugs []string
	if cat, err := ingest.LoadSlugCatalog(wikiDir); err == nil {
		existingSlugs = cat.Slugs()
	}
	source := ingest.Source{
		Path:          relPath,
		Title:         sourceTitle(relPath, raw),
		Content:       stripLeadingFrontmatter(string(raw)),
		ExistingSlugs: existingSlugs,
	}

	extraction, err := ingest.Extract(ctx, client, source)
	if err != nil {
		return fmt.Errorf("extract: %w", err)
	}
	printVerbose("  extracted: %d entities, %d concepts, %d claims\n",
		len(extraction.Entities), len(extraction.Concepts), len(extraction.Claims))

	reconciled, err := ingest.Reconcile(extraction, wikiDir, relPath)
	if err != nil {
		return fmt.Errorf("reconcile: %w", err)
	}

	if opts.DryRun {
		if verbosity >= VerbosityNormal {
			fmt.Printf("[dry-run] %d create / %d update (%s)\n",
				len(reconciled.Create), len(reconciled.Update),
				truncDur(time.Since(started)))
		}
		summary.Processed++
		return nil
	}

	report := ingest.Write(ctx, client, reconciled, wikiDir, time.Now())
	for _, werr := range report.Errors {
		fmt.Fprintf(os.Stderr, "  ! write: %v\n", werr)
	}
	summary.Created += len(report.Created)
	summary.Updated += len(report.Updated)
	summary.Processed++

	if err := ingest.MarkIngested(dbPath, relPath, hash); err != nil {
		return fmt.Errorf("mark cache: %w", err)
	}

	if err := wiki.AppendLog(wikiDir, wiki.LogEntry{
		Timestamp: time.Now(),
		Type:      wiki.LogTypeIngest,
		Title:     relPath,
		Created:   report.Created,
		Updated:   report.Updated,
		Sources:   []string{relPath},
	}); err != nil {
		fmt.Fprintf(os.Stderr, "  ! append log: %v\n", err)
	}

	if verbosity >= VerbosityNormal {
		fmt.Printf("%d created, %d updated (%s)\n",
			len(report.Created), len(report.Updated),
			truncDur(time.Since(started)))
	}
	printVerbose("  created: %s\n", strings.Join(report.Created, ", "))
	printVerbose("  updated: %s\n", strings.Join(report.Updated, ", "))
	return nil
}

// collectSourceFiles expands every arg into a list of project-relative
// file paths. Each arg can be a file, a directory (walked recursively
// for .md + .txt), or a shell-expanded path. Results are deduped and
// sorted so batch runs are deterministic.
func collectSourceFiles(projectRoot string, args []string) ([]string, error) {
	seen := map[string]struct{}{}
	out := make([]string, 0)
	add := func(rel string) {
		if _, dup := seen[rel]; dup {
			return
		}
		seen[rel] = struct{}{}
		out = append(out, rel)
	}

	for _, arg := range args {
		abs, err := filepath.Abs(arg)
		if err != nil {
			return nil, fmt.Errorf("resolve %q: %w", arg, err)
		}
		info, err := os.Stat(abs)
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", arg, err)
		}
		if info.IsDir() {
			err := filepath.WalkDir(abs, func(p string, d fs.DirEntry, werr error) error {
				if werr != nil {
					return werr
				}
				if d.IsDir() {
					return nil
				}
				if !isIngestibleExt(p) {
					return nil
				}
				rel, err := filepath.Rel(projectRoot, p)
				if err != nil {
					return err
				}
				add(rel)
				return nil
			})
			if err != nil {
				return nil, err
			}
			continue
		}
		if !isIngestibleExt(abs) {
			return nil, fmt.Errorf("%s: only .md / .txt sources are supported", arg)
		}
		rel, err := filepath.Rel(projectRoot, abs)
		if err != nil {
			return nil, err
		}
		add(rel)
	}
	sort.Strings(out)
	return out, nil
}

// isIngestibleExt returns true for file extensions anvil knows how to
// ingest. Narrow on purpose — a .pdf or .docx needs a conversion step
// that isn't in anvil's scope yet.
func isIngestibleExt(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".md" || ext == ".txt"
}

// sourceTitle returns a reasonable page title for the source. Prefer
// the first H1 heading when the file has one; fall back to the
// filename stem.
func sourceTitle(relPath string, raw []byte) string {
	lines := strings.Split(string(raw), "\n")
	inFrontmatter := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Allow a leading frontmatter block — skip it before
		// looking for the first heading.
		if i == 0 && trimmed == "---" {
			inFrontmatter = true
			continue
		}
		if inFrontmatter {
			if trimmed == "---" {
				inFrontmatter = false
			}
			continue
		}
		if strings.HasPrefix(trimmed, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, "# "))
		}
		if trimmed != "" {
			// First non-heading, non-blank, non-frontmatter line.
			// No top-level H1 available — fall through to stem.
			break
		}
	}
	return strings.TrimSuffix(filepath.Base(relPath), filepath.Ext(relPath))
}

// stripLeadingFrontmatter drops a YAML frontmatter block when one
// opens the file. Extract prompts don't need the frontmatter bytes
// and keeping them bloats the prompt + invites the LLM to parrot
// them back.
func stripLeadingFrontmatter(raw string) string {
	if !strings.HasPrefix(raw, "---\n") && !strings.HasPrefix(raw, "---\r\n") {
		return raw
	}
	// Find the closing delim on its own line after the opening one.
	rest := raw[strings.Index(raw, "\n")+1:]
	if idx := strings.Index(rest, "\n---\n"); idx >= 0 {
		return strings.TrimLeft(rest[idx+5:], "\n")
	}
	if idx := strings.Index(rest, "\n---\r\n"); idx >= 0 {
		return strings.TrimLeft(rest[idx+6:], "\n")
	}
	return raw
}

// joinWithWikiPrefix renders "wiki/a.md, wiki/b.md" so the terminal
// output matches the spec's example where slugs appear under the
// wiki directory.
func joinWithWikiPrefix(slugs []string) string {
	out := make([]string, len(slugs))
	for i, s := range slugs {
		out[i] = "wiki/" + s
	}
	return strings.Join(out, ", ")
}

// truncDur rounds a duration to 0.1s granularity for terminal output.
func truncDur(d time.Duration) time.Duration {
	return d.Round(100 * time.Millisecond)
}
