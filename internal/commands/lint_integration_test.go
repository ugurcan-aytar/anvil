// End-to-end tests for `anvil lint`. Each test seeds a real
// project layout, drives runLint through its Cobra entry point, and
// asserts on the stdout report + any side effects of --fix.

package commands

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ugurcan-aytar/anvil/internal/llm"
	"github.com/ugurcan-aytar/anvil/internal/wiki"
)

// prepareLintProject builds a project with a deliberate mix of
// issues so the lint output has something to flag.
func prepareLintProject(t *testing.T) string {
	t.Helper()
	root := bootstrapProject(t)
	wikiDir := filepath.Join(root, "wiki")

	writePage := func(filename, title, body string, sources ...string) {
		p := &wiki.Page{
			Filename: filename,
			Title:    title,
			Type:     "concept",
			Sources:  sources,
			Created:  "2026-04-15",
			Updated:  "2026-04-15",
			Body:     body,
		}
		if err := wiki.WritePage(wikiDir, p); err != nil {
			t.Fatal(err)
		}
	}
	writePage("linked.md", "Linked", "This page is referenced by others.")
	writePage("referrer.md", "Referrer",
		"Points to [[linked]] and [[missing-page]].",
		"raw/source-a.md")
	writePage("orphan.md", "Orphan",
		"Nobody links to this page. It sits alone.")
	// Rebuild index so "orphan.md" ends up listed; later we
	// deliberately rewrite index.md in the stale-index test.
	if err := wiki.RebuildIndex(wikiDir); err != nil {
		t.Fatal(err)
	}
	return root
}

// ============================================================
// Structural-only: orphan + missing-page + stale-index findings
// land in the rendered report; no LLM calls happen.
// ============================================================

func TestLintStructuralOnlyReportsFindings(t *testing.T) {
	root := prepareLintProject(t)
	// Introduce a stale-index entry: drop a page on disk but don't
	// rebuild the index.
	p := &wiki.Page{
		Filename: "recent-addition.md",
		Title:    "Recent Addition",
		Type:     "concept",
		Created:  "2026-04-15",
		Updated:  "2026-04-15",
		Body:     "Fresh page the index hasn't caught up on.",
	}
	if err := wiki.WritePage(filepath.Join(root, "wiki"), p); err != nil {
		t.Fatal(err)
	}

	client := swapLLMClient(t, nil) // unused in structural-only mode

	var out string
	withProjectDir(t, root, func() {
		var err error
		out, err = captureStdout(t, func() error {
			return runLint(context.Background(), lintOptions{StructuralOnly: true})
		})
		if err != nil {
			t.Fatalf("runLint: %v", err)
		}
	})
	if len(client.Calls) != 0 {
		t.Errorf("--structural-only must not call LLM; got %d calls", len(client.Calls))
	}
	for _, want := range []string{
		"Structural checks:",
		"pages scanned",
		"orphan pages: orphan",
		"missing page(s): missing-page",
		"page(s) not in index: recent-addition",
		"LLM checks: skipped",
		"Health:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("lint output missing %q; full output:\n%s", want, out)
		}
	}
}

// ============================================================
// Full mode: contradiction detection fires, finding is rendered.
// ============================================================

func TestLintReportsContradictions(t *testing.T) {
	root := prepareLintProject(t)
	wikiDir := filepath.Join(root, "wiki")
	// Two pages sharing a source — overlap triggers pairing.
	writePageWithSources(t, wikiDir, "caching-strategies.md",
		"Redis TTL default is 300s.", "raw/runbook.md")
	writePageWithSources(t, wikiDir, "meeting-2026-04-01.md",
		"Team set Redis TTL to 600s.", "raw/runbook.md")

	// Script: 1 contradiction reply + (stale check runs but both
	// pages share updated=2026-04-15 so no LLM call) + 1 suggest
	// reply.
	contraReply := `Contradiction:
  Claim A: Redis TTL default is 300s
  Claim B: Team set Redis TTL to 600s
  Detail: Pages disagree on the configured TTL.
`
	suggestReply := `1. Merge [[caching-strategies]] and [[meeting-2026-04-01]] after reconciling TTL.`
	_ = swapLLMClient(t, []string{contraReply, suggestReply})

	var out string
	withProjectDir(t, root, func() {
		var err error
		out, err = captureStdout(t, func() error {
			return runLint(context.Background(), lintOptions{})
		})
		if err != nil {
			t.Fatalf("runLint: %v", err)
		}
	})
	for _, want := range []string{
		"Contradiction check (LLM):",
		"1 contradiction(s) found",
		"[[caching-strategies]]",
		"[[meeting-2026-04-01]]",
		"Redis TTL default is 300s",
		"Suggestions:",
		"Merge",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q; full:\n%s", want, out)
		}
	}
}

// writePageWithSources duplicates the lint-package helper so this
// file doesn't depend on an internal helper moving.
func writePageWithSources(t *testing.T, wikiDir, filename, body string, sources ...string) {
	t.Helper()
	p := &wiki.Page{
		Filename: filename,
		Title:    strings.TrimSuffix(filename, ".md"),
		Type:     "concept",
		Sources:  sources,
		Created:  "2026-04-15",
		Updated:  "2026-04-15",
		Body:     body,
	}
	if err := wiki.WritePage(wikiDir, p); err != nil {
		t.Fatal(err)
	}
}

// ============================================================
// --fix rebuilds the index and clears stale-index findings on rerun.
// ============================================================

func TestLintFixRebuildsIndex(t *testing.T) {
	root := prepareLintProject(t)
	wikiDir := filepath.Join(root, "wiki")
	// Introduce the same stale-index situation as above.
	p := &wiki.Page{
		Filename: "fresh.md",
		Title:    "Fresh",
		Type:     "concept",
		Created:  "2026-04-15",
		Updated:  "2026-04-15",
		Body:     "Latest page, not yet in index.",
	}
	if err := wiki.WritePage(wikiDir, p); err != nil {
		t.Fatal(err)
	}
	_ = swapLLMClient(t, nil)

	// Run with --fix + --structural-only so the LLM path doesn't run.
	var out string
	withProjectDir(t, root, func() {
		var err error
		out, err = captureStdout(t, func() error {
			return runLint(context.Background(), lintOptions{StructuralOnly: true, Fix: true})
		})
		if err != nil {
			t.Fatalf("runLint: %v", err)
		}
	})
	if !strings.Contains(out, "Fix: rebuilt wiki/index.md") {
		t.Errorf("fix summary missing; output:\n%s", out)
	}

	// Second pass: no stale-index findings survive.
	withProjectDir(t, root, func() {
		var err error
		out, err = captureStdout(t, func() error {
			return runLint(context.Background(), lintOptions{StructuralOnly: true})
		})
		if err != nil {
			t.Fatalf("second runLint: %v", err)
		}
	})
	if !strings.Contains(out, "✓ 0 page(s) not in index") {
		t.Errorf("after --fix, stale-index should be zero; got:\n%s", out)
	}
}

// ============================================================
// Missing LLM backend → downgrade to structural-only, don't error.
// ============================================================

func TestLintMissingBackendFallsBackToStructural(t *testing.T) {
	root := prepareLintProject(t)

	prev := newLLMClient
	t.Cleanup(func() { newLLMClient = prev })
	newLLMClient = func() (llm.Client, error) { return nil, llm.ErrNoBackend }

	var out string
	withProjectDir(t, root, func() {
		var err error
		out, err = captureStdout(t, func() error {
			return runLint(context.Background(), lintOptions{})
		})
		if err != nil {
			t.Fatalf("runLint should not error when backend missing: %v", err)
		}
	})
	if !strings.Contains(out, "Structural checks") {
		t.Errorf("structural output missing; got:\n%s", out)
	}
	if !strings.Contains(out, "LLM checks: skipped") {
		t.Errorf("missing-backend path should report LLM skip; got:\n%s", out)
	}
}
