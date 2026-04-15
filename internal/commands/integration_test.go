// Integration tests covering the manual A1 smoke plan end-to-end.
//
// Each TestCase corresponds to a step in the local test plan the
// user runs before tagging. We deliberately call the same handler
// functions Cobra dispatches to (initProject, runStatus, runSearch)
// so a test failure and a CLI failure share one root cause.
//
// Shared plumbing: every test gets its own t.TempDir(), sets
// projectDir to point at it, and restores the default afterward.
// stdout capture lets the status / search assertions inspect the
// same bytes the user sees.

package commands

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withProjectDir swaps the package-level projectDir for the duration
// of fn so runStatus / runSearch (which read the global) target the
// test fixture, and restores it afterwards.
func withProjectDir(t *testing.T, dir string, fn func()) {
	t.Helper()
	prev := projectDir
	projectDir = dir
	t.Cleanup(func() { projectDir = prev })
	fn()
}

// captureStdout redirects os.Stdout into a pipe for the duration
// of fn and returns everything fn wrote. Errors from fn bubble up
// alongside the captured text so the caller can assert on both.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w

	done := make(chan struct{})
	var buf strings.Builder
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()

	fnErr := fn()
	_ = w.Close()
	<-done
	os.Stdout = old
	return buf.String(), fnErr
}

// bootstrapProject runs initProject against a fresh t.TempDir() and
// returns the project root. Asserts the init call itself succeeded
// so tests can assume the fixture is in a known-good state.
func bootstrapProject(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "project")
	if _, err := captureStdout(t, func() error { return initProject(root) }); err != nil {
		t.Fatalf("initProject: %v", err)
	}
	return root
}

// ============================================================
// Step 1 — anvil init creates the scaffolding.
// ============================================================

func TestInitCreatesFullLayout(t *testing.T) {
	root := bootstrapProject(t)

	// Directory tree.
	for _, sub := range []string{".anvil", "raw", "wiki"} {
		info, err := os.Stat(filepath.Join(root, sub))
		if err != nil {
			t.Errorf("missing %s/: %v", sub, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%s should be a directory", sub)
		}
	}

	// Files the seeder writes.
	wants := map[string]string{
		"ANVIL.md":       "# ANVIL.md — Wiki Schema",
		".gitignore":     ".anvil/",
		"wiki/index.md":  "# Index",
		"wiki/log.md":    "# Log",
		"raw/.gitkeep":   "",
	}
	for rel, wantSubstr := range wants {
		path := filepath.Join(root, rel)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("missing %s: %v", rel, err)
			continue
		}
		if wantSubstr != "" && !strings.Contains(string(raw), wantSubstr) {
			t.Errorf("%s missing marker %q; body was:\n%s", rel, wantSubstr, raw)
		}
	}

	// DB was created (engine.Open ran to completion).
	if _, err := os.Stat(filepath.Join(root, ".anvil", "index.db")); err != nil {
		t.Errorf("index.db not created: %v", err)
	}
}

// ============================================================
// Step 2 — anvil init refuses to overwrite an existing project.
// ============================================================

func TestInitRejectsExistingProject(t *testing.T) {
	root := bootstrapProject(t)

	// Second init on the same target must error. Must not wipe
	// any file — check an ANVIL.md marker survives.
	err := initProject(root)
	if err == nil {
		t.Fatal("initProject on existing path should error")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error message should mention 'already exists'; got %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "ANVIL.md")); err != nil {
		t.Errorf("existing project file was clobbered: %v", err)
	}
}

// ============================================================
// Step 3 — anvil status on an empty project.
// ============================================================

func TestStatusOnEmptyProject(t *testing.T) {
	root := bootstrapProject(t)

	var out string
	var err error
	withProjectDir(t, root, func() {
		out, err = captureStdout(t, func() error { return runStatus(root) })
	})
	if err != nil {
		t.Fatalf("runStatus: %v", err)
	}

	wants := []string{
		"Wiki:    0 pages",
		"Raw:     0 files",
		"Index:   0 entries",
		"Log:     0 entries",
		"0 orphan page(s), 0 missing page(s)",
	}
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("empty-project status missing %q; output:\n%s", w, out)
		}
	}
}

// ============================================================
// Step 4 — wiki page with wikilink → status shows orphan + missing.
// ============================================================

func TestStatusAfterPageWithWikilink(t *testing.T) {
	root := bootstrapProject(t)

	// Write a page referencing a non-existent target so both
	// orphan + missing counters flip to 1.
	page := `---
title: Circuit Breaker
type: concept
created: 2026-04-15
updated: 2026-04-15
---

Circuit breakers stop cascading failures in distributed systems.
See [[retry-pattern]] for a complementary technique.
`
	if err := os.WriteFile(filepath.Join(root, "wiki", "circuit-breaker.md"), []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}

	var out string
	var err error
	withProjectDir(t, root, func() {
		out, err = captureStdout(t, func() error { return runStatus(root) })
	})
	if err != nil {
		t.Fatalf("runStatus: %v", err)
	}

	wants := []string{
		"Wiki:    1 pages",
		"1 concept",
		"1 orphan page(s), 1 missing page(s)",
		"orphans: circuit-breaker",
		"missing: retry-pattern",
	}
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("status missing %q; output:\n%s", w, out)
		}
	}
}

// ============================================================
// Step 5 — anvil search "term" returns a BM25 hit.
// ============================================================

func TestSearchFindsBM25Hit(t *testing.T) {
	root := bootstrapProject(t)

	page := `---
title: Circuit Breaker
type: concept
---

Circuit breakers stop cascading failures in distributed systems.
`
	if err := os.WriteFile(filepath.Join(root, "wiki", "circuit-breaker.md"), []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}

	var out string
	var err error
	withProjectDir(t, root, func() {
		out, err = captureStdout(t, func() error {
			return runSearch("circuit breaker", searchOptions{Limit: 10})
		})
	})
	if err != nil {
		t.Fatalf("runSearch: %v", err)
	}
	if !strings.Contains(out, "circuit-breaker.md") {
		t.Errorf("search didn't return the written page; output:\n%s", out)
	}
	if !strings.Contains(out, "score") {
		t.Errorf("expected a score line in output; got:\n%s", out)
	}
}

// ============================================================
// Step 6 — --collection flag restricts the search scope.
// ============================================================

func TestSearchCollectionFlag(t *testing.T) {
	root := bootstrapProject(t)

	// A page in wiki/ and a raw source with different distinct
	// terms so we can tell which collection was searched.
	wikiPage := `---
title: Wiki Page
type: concept
---

This page talks about WIKIUNIQUETERM.
`
	rawFile := "Raw source talking about RAWUNIQUETERM.\n"
	if err := os.WriteFile(filepath.Join(root, "wiki", "wiki-page.md"), []byte(wikiPage), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "raw", "raw-source.md"), []byte(rawFile), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name       string
		query      string
		collection string
		wantFile   string
		unwantFile string
	}{
		{"wiki-only finds wiki page", "WIKIUNIQUETERM", "wiki", "wiki-page.md", "raw-source.md"},
		{"wiki-only misses raw source", "RAWUNIQUETERM", "wiki", "", "raw-source.md"},
		{"raw-only finds raw source", "RAWUNIQUETERM", "raw", "raw-source.md", "wiki-page.md"},
		{"raw-only misses wiki page", "WIKIUNIQUETERM", "raw", "", "wiki-page.md"},
		{"both-default finds wiki term", "WIKIUNIQUETERM", "", "wiki-page.md", ""},
		{"both-default finds raw term", "RAWUNIQUETERM", "", "raw-source.md", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out string
			var err error
			withProjectDir(t, root, func() {
				out, err = captureStdout(t, func() error {
					return runSearch(tc.query, searchOptions{
						Limit:      10,
						Collection: tc.collection,
					})
				})
			})
			if err != nil {
				t.Fatalf("runSearch: %v", err)
			}
			if tc.wantFile != "" && !strings.Contains(out, tc.wantFile) {
				t.Errorf("expected %s in output; got:\n%s", tc.wantFile, out)
			}
			if tc.unwantFile != "" && strings.Contains(out, tc.unwantFile) {
				t.Errorf("did not expect %s in output; got:\n%s", tc.unwantFile, out)
			}
		})
	}
}

// ============================================================
// Step 7 — -n flag caps result count.
// ============================================================

func TestSearchLimitFlag(t *testing.T) {
	root := bootstrapProject(t)

	// Three pages all matching the same query so the limit is
	// what gates the output length.
	for i, name := range []string{"alpha.md", "beta.md", "gamma.md"} {
		body := "---\ntitle: " + string(rune('A'+i)) + "\ntype: concept\n---\n\nThis page has the word SHAREDTERM.\n"
		if err := os.WriteFile(filepath.Join(root, "wiki", name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	countHits := func(out string) int {
		// Each hit starts with "<collection>/<path>" as its
		// first stanza line. Count lines containing ".md  #"
		// which appears exactly once per result in runSearch's
		// rendering.
		n := 0
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, ".md  #") {
				n++
			}
		}
		return n
	}

	for _, tc := range []struct {
		limit int
		want  int
	}{
		{1, 1},
		{2, 2},
		{10, 3},
	} {
		t.Run("limit="+string(rune('0'+tc.limit)), func(t *testing.T) {
			var out string
			var err error
			withProjectDir(t, root, func() {
				out, err = captureStdout(t, func() error {
					return runSearch("SHAREDTERM", searchOptions{Limit: tc.limit})
				})
			})
			if err != nil {
				t.Fatalf("runSearch: %v", err)
			}
			if got := countHits(out); got != tc.want {
				t.Errorf("limit=%d: got %d hits, want %d — output:\n%s",
					tc.limit, got, tc.want, out)
			}
		})
	}
}

// ============================================================
// Step 8 — adding a raw file updates the raw count in status.
// ============================================================

func TestStatusWithRawFiles(t *testing.T) {
	root := bootstrapProject(t)

	// Baseline: zero raw files.
	var before string
	withProjectDir(t, root, func() {
		before, _ = captureStdout(t, func() error { return runStatus(root) })
	})
	if !strings.Contains(before, "Raw:     0 files") {
		t.Fatalf("baseline should report 0 raw files; got:\n%s", before)
	}

	// Drop two source files (plus a hidden file to confirm the
	// counter skips hidden entries like .gitkeep).
	os.WriteFile(filepath.Join(root, "raw", "paper.md"), []byte("content"), 0o644)
	os.WriteFile(filepath.Join(root, "raw", "notes.md"), []byte("more"), 0o644)
	os.WriteFile(filepath.Join(root, "raw", ".hidden"), []byte("not counted"), 0o644)

	var after string
	withProjectDir(t, root, func() {
		after, _ = captureStdout(t, func() error { return runStatus(root) })
	})
	if !strings.Contains(after, "Raw:     2 files") {
		t.Errorf("expected 2 raw files; got:\n%s", after)
	}
}
