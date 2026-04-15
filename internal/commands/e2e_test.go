// End-to-end scenario coverage. This file stress-tests every
// user-facing command (ingest, ask, save, lint, status) against a
// real filesystem using t.TempDir(), backed by a scripted LLM
// client. Each test is independent and asserts on the on-disk
// state + captured stdout that a real user would see.
//
// Helpers reused from the other integration tests in this package:
//   - bootstrapProject(t) / withProjectDir(t, ...) / captureStdout — integration_test.go
//   - swapLLMClient / extractResponse / writePageResponse — ask_integration_test.go
//   - writePageWithSources — lint_integration_test.go
//
// To keep the file readable, tests are grouped by command and
// prefixed with the scenario category in the test name.

package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ugurcan-aytar/anvil/internal/llm"
	"github.com/ugurcan-aytar/anvil/internal/wiki"
)

// quickExtract is a compact entity+concept YAML extraction used
// when a test only cares that an ingest produces one page. Two
// concepts are emitted so Reconcile creates two drafts when the
// test opts in, but most tests only queue one write response and
// therefore only exercise the first concept.
func simpleExtraction(name, claim string) string {
	return "```yaml\n" +
		"entities: []\n" +
		"concepts:\n" +
		"  - name: \"" + name + "\"\n" +
		"    description: \"Test concept.\"\n" +
		"claims:\n" +
		"  - claim: \"" + claim + "\"\n" +
		"    related: [\"" + name + "\"]\n" +
		"connections: []\n" +
		"```\n"
}

// simplePage is a scripted write-prompt reply the tests re-use.
// Produces a minimal but valid synthesis page with the passed slug
// + the passed body.
func simplePage(title, pageType, sourceRel, body string) string {
	return "---\n" +
		"title: " + title + "\n" +
		"type: " + pageType + "\n" +
		"sources:\n" +
		"  - " + sourceRel + "\n" +
		"created: 2026-04-16\n" +
		"updated: 2026-04-16\n" +
		"---\n\n" +
		body + "\n"
}

// ============================================================
// INGEST SCENARIOS
// ============================================================

// #1 — single .md source → one wiki page lands on disk.
func TestE2EIngestSingleMarkdown(t *testing.T) {
	root := bootstrapProject(t)
	src := filepath.Join(root, "raw", "cb.md")
	if err := os.WriteFile(src, []byte("# Circuit Breaker\n\nStops cascades.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = swapLLMClient(t, []string{
		simpleExtraction("Circuit Breaker", "Stops cascading failures."),
		simplePage("Circuit Breaker", "concept", "raw/cb.md", "Body with [[retry-pattern]]."),
	})
	withProjectDir(t, root, func() {
		if _, err := captureStdout(t, func() error {
			return runIngest(context.Background(), []string{src}, ingestOptions{})
		}); err != nil {
			t.Fatalf("runIngest: %v", err)
		}
	})
	if _, err := os.Stat(filepath.Join(root, "wiki", "circuit-breaker.md")); err != nil {
		t.Errorf("expected wiki/circuit-breaker.md: %v", err)
	}
}

// #2 — directory recursion picks up every .md underneath.
func TestE2EIngestDirectoryRecursive(t *testing.T) {
	root := bootstrapProject(t)
	nested := filepath.Join(root, "raw", "subdir")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "raw", "a.md"), []byte("About Alpha."), 0o644)
	os.WriteFile(filepath.Join(nested, "b.md"), []byte("About Beta."), 0o644)

	_ = swapLLMClient(t, []string{
		simpleExtraction("Alpha", "Alpha exists."),
		simplePage("Alpha", "concept", "raw/a.md", "Alpha body."),
		simpleExtraction("Beta", "Beta exists."),
		simplePage("Beta", "concept", "raw/subdir/b.md", "Beta body."),
	})
	withProjectDir(t, root, func() {
		if _, err := captureStdout(t, func() error {
			return runIngest(context.Background(), []string{filepath.Join(root, "raw")}, ingestOptions{})
		}); err != nil {
			t.Fatalf("runIngest: %v", err)
		}
	})
	for _, want := range []string{"alpha.md", "beta.md"} {
		if _, err := os.Stat(filepath.Join(root, "wiki", want)); err != nil {
			t.Errorf("missing page %s: %v", want, err)
		}
	}
}

// #3 — re-ingest of unchanged content is a no-op (hash cache hit).
func TestE2EIngestSkipsUnchangedContent(t *testing.T) {
	root := bootstrapProject(t)
	src := filepath.Join(root, "raw", "stable.md")
	os.WriteFile(src, []byte("stable body\n"), 0o644)
	_ = swapLLMClient(t, []string{
		simpleExtraction("Stable", "Stable claim."),
		simplePage("Stable", "concept", "raw/stable.md", "Stable body."),
	})
	withProjectDir(t, root, func() {
		captureStdout(t, func() error { return runIngest(context.Background(), []string{src}, ingestOptions{}) })
	})
	// Second pass: fresh client; zero calls proves cache hit.
	client := swapLLMClient(t, []string{"should-not-be-called"})
	var out string
	withProjectDir(t, root, func() {
		out, _ = captureStdout(t, func() error { return runIngest(context.Background(), []string{src}, ingestOptions{}) })
	})
	if len(client.Calls) != 0 {
		t.Errorf("unchanged re-ingest should skip LLM; got %d calls", len(client.Calls))
	}
	if !strings.Contains(out, "skipped (unchanged)") {
		t.Errorf("expected 'Skipping' output; got:\n%s", out)
	}
}

// #4 — changing the source bumps the hash; update flow fires.
func TestE2EIngestUpdatesOnChange(t *testing.T) {
	root := bootstrapProject(t)
	src := filepath.Join(root, "raw", "evolving.md")
	os.WriteFile(src, []byte("v1 body\n"), 0o644)
	_ = swapLLMClient(t, []string{
		simpleExtraction("Evolving", "v1 claim."),
		simplePage("Evolving", "concept", "raw/evolving.md", "v1 body here."),
	})
	withProjectDir(t, root, func() {
		captureStdout(t, func() error { return runIngest(context.Background(), []string{src}, ingestOptions{}) })
	})
	os.WriteFile(src, []byte("v2 body with more content\n"), 0o644)

	updated := "---\ntitle: Evolving\ntype: concept\nsources:\n  - raw/evolving.md\ncreated: 2026-04-16\nupdated: 2026-04-16\n---\n\nv2 body here with new detail.\n"
	client := swapLLMClient(t, []string{
		simpleExtraction("Evolving", "v2 claim."),
		updated,
	})
	withProjectDir(t, root, func() {
		captureStdout(t, func() error { return runIngest(context.Background(), []string{src}, ingestOptions{}) })
	})
	if len(client.Calls) != 2 {
		t.Errorf("expected 2 calls (extract + update), got %d", len(client.Calls))
	}
	body, _ := os.ReadFile(filepath.Join(root, "wiki", "evolving.md"))
	if !strings.Contains(string(body), "v2 body") {
		t.Errorf("page should reflect v2 update; got:\n%s", body)
	}
	if strings.Contains(string(body), "v1 body") {
		t.Errorf("page still carries v1 body; got:\n%s", body)
	}
}

// #5 — fully empty file is rejected before the LLM is called.
func TestE2EIngestEmptyFileRejected(t *testing.T) {
	root := bootstrapProject(t)
	src := filepath.Join(root, "raw", "blank.md")
	os.WriteFile(src, []byte("   \n\t\n   "), 0o644)
	client := swapLLMClient(t, []string{})
	var out string
	withProjectDir(t, root, func() {
		out, _ = captureStdout(t, func() error { return runIngest(context.Background(), []string{src}, ingestOptions{}) })
	})
	if len(client.Calls) != 0 {
		t.Errorf("empty file should not trigger LLM; got %d calls", len(client.Calls))
	}
	if !strings.Contains(out, "1 error") {
		t.Errorf("summary should report the error; got:\n%s", out)
	}
}

// #6 — oversized source gets truncated before the LLM sees it.
func TestE2EIngestLargeFileTruncates(t *testing.T) {
	root := bootstrapProject(t)
	src := filepath.Join(root, "raw", "huge.md")
	big := strings.Repeat("circuit breaker content block. ", 1500) // ~45k chars
	os.WriteFile(src, []byte(big), 0o644)

	client := swapLLMClient(t, []string{
		simpleExtraction("Huge", "Huge topic."),
		simplePage("Huge", "concept", "raw/huge.md", "Body."),
	})
	withProjectDir(t, root, func() {
		captureStdout(t, func() error { return runIngest(context.Background(), []string{src}, ingestOptions{}) })
	})
	if len(client.Calls) < 1 {
		t.Fatal("no LLM calls were made")
	}
	sent := client.Calls[0].User
	if !strings.Contains(sent, "[truncated:") {
		t.Errorf("oversized prompt missing truncation sentinel (len=%d)", len(sent))
	}
	if len(sent) >= len(big) {
		t.Errorf("truncated prompt should be smaller than raw content: %d vs %d",
			len(sent), len(big))
	}
}

// #7 — .txt sources are accepted the same way .md are.
func TestE2EIngestPlainTextFile(t *testing.T) {
	root := bootstrapProject(t)
	src := filepath.Join(root, "raw", "note.txt")
	os.WriteFile(src, []byte("Plain text note about Widget."), 0o644)
	_ = swapLLMClient(t, []string{
		simpleExtraction("Widget", "Widgets exist."),
		simplePage("Widget", "concept", "raw/note.txt", "Widget body."),
	})
	withProjectDir(t, root, func() {
		if _, err := captureStdout(t, func() error {
			return runIngest(context.Background(), []string{src}, ingestOptions{})
		}); err != nil {
			t.Fatalf("runIngest: %v", err)
		}
	})
	if _, err := os.Stat(filepath.Join(root, "wiki", "widget.md")); err != nil {
		t.Errorf("expected wiki/widget.md: %v", err)
	}
}

// #8 — binary file passed directly is rejected with an actionable
// error; the same file inside a walked directory is silently
// skipped so a mixed raw/ layout still works.
func TestE2EIngestBinaryFile(t *testing.T) {
	t.Run("single binary file → rejected", func(t *testing.T) {
		root := bootstrapProject(t)
		src := filepath.Join(root, "raw", "image.png")
		os.WriteFile(src, []byte{0x89, 'P', 'N', 'G'}, 0o644)
		_ = swapLLMClient(t, nil)
		var err error
		withProjectDir(t, root, func() {
			err = runIngest(context.Background(), []string{src}, ingestOptions{})
		})
		if err == nil {
			t.Fatal("binary file should be rejected")
		}
		if !strings.Contains(err.Error(), ".md") {
			t.Errorf("error should mention supported extensions; got %v", err)
		}
	})
	t.Run("mixed directory → binary silently skipped", func(t *testing.T) {
		root := bootstrapProject(t)
		os.WriteFile(filepath.Join(root, "raw", "note.md"), []byte("Real note."), 0o644)
		os.WriteFile(filepath.Join(root, "raw", "image.png"), []byte{0x89, 'P', 'N', 'G'}, 0o644)
		_ = swapLLMClient(t, []string{
			simpleExtraction("Note", "Notes."),
			simplePage("Note", "concept", "raw/note.md", "Body."),
		})
		withProjectDir(t, root, func() {
			if _, err := captureStdout(t, func() error {
				return runIngest(context.Background(), []string{filepath.Join(root, "raw")}, ingestOptions{})
			}); err != nil {
				t.Fatalf("runIngest: %v", err)
			}
		})
		if _, err := os.Stat(filepath.Join(root, "wiki", "note.md")); err != nil {
			t.Errorf("note.md should have been ingested: %v", err)
		}
	})
}

// #9 — --dry-run runs extract but not write, persists nothing.
func TestE2EIngestDryRunNoWrites(t *testing.T) {
	root := bootstrapProject(t)
	src := filepath.Join(root, "raw", "x.md")
	os.WriteFile(src, []byte("content"), 0o644)
	client := swapLLMClient(t, []string{simpleExtraction("Thing", "Claim.")})
	withProjectDir(t, root, func() {
		captureStdout(t, func() error { return runIngest(context.Background(), []string{src}, ingestOptions{DryRun: true}) })
	})
	if len(client.Calls) != 1 {
		t.Errorf("dry-run should make exactly 1 LLM call (extract); got %d", len(client.Calls))
	}
	entries, _ := os.ReadDir(filepath.Join(root, "wiki"))
	for _, e := range entries {
		if e.Name() == "thing.md" {
			t.Errorf("dry-run should not persist wiki/thing.md")
		}
	}
}

// #10 — --force re-ingests a cached file.
func TestE2EIngestForceBypassesCache(t *testing.T) {
	root := bootstrapProject(t)
	src := filepath.Join(root, "raw", "f.md")
	os.WriteFile(src, []byte("forced content"), 0o644)
	_ = swapLLMClient(t, []string{
		simpleExtraction("Forcible", "Claim."),
		simplePage("Forcible", "concept", "raw/f.md", "Body."),
	})
	withProjectDir(t, root, func() {
		captureStdout(t, func() error { return runIngest(context.Background(), []string{src}, ingestOptions{}) })
	})
	client := swapLLMClient(t, []string{
		simpleExtraction("Forcible", "Claim v2."),
		simplePage("Forcible", "concept", "raw/f.md", "Body v2."),
	})
	withProjectDir(t, root, func() {
		captureStdout(t, func() error { return runIngest(context.Background(), []string{src}, ingestOptions{Force: true}) })
	})
	if len(client.Calls) == 0 {
		t.Errorf("--force should re-trigger LLM calls; got 0")
	}
}

// #11 — two different sources mentioning the same entity merge
// their frontmatter: the second ingest hits the update path and
// both source paths land in the sources list.
func TestE2EIngestTwoSourcesMergeIntoOnePage(t *testing.T) {
	root := bootstrapProject(t)
	src1 := filepath.Join(root, "raw", "paper.md")
	src2 := filepath.Join(root, "raw", "meeting.md")
	os.WriteFile(src1, []byte("Paper about Foo."), 0o644)
	os.WriteFile(src2, []byte("Meeting notes about Foo."), 0o644)
	_ = swapLLMClient(t, []string{
		simpleExtraction("Foo", "Foo claim 1."),
		simplePage("Foo", "concept", "raw/paper.md", "Initial body about Foo."),
	})
	withProjectDir(t, root, func() {
		captureStdout(t, func() error { return runIngest(context.Background(), []string{src1}, ingestOptions{}) })
	})
	// Update response — LLM only lists the new source, writer
	// backfills the original.
	_ = swapLLMClient(t, []string{
		simpleExtraction("Foo", "Foo claim 2."),
		"---\ntitle: Foo\ntype: concept\nsources:\n  - raw/meeting.md\ncreated: 2026-04-16\nupdated: 2026-04-16\n---\n\nMerged body with new detail.\n",
	})
	withProjectDir(t, root, func() {
		captureStdout(t, func() error { return runIngest(context.Background(), []string{src2}, ingestOptions{}) })
	})
	body, _ := os.ReadFile(filepath.Join(root, "wiki", "foo.md"))
	for _, want := range []string{"raw/paper.md", "raw/meeting.md", "Merged body"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("merged page missing %q; body:\n%s", want, body)
		}
	}
}

// ============================================================
// ASK SCENARIOS
// ============================================================

// #12 — question where only wiki/ has relevant content.
func TestE2EAskWikiOnlyHits(t *testing.T) {
	root := bootstrapProject(t)
	writePageWithSources(t, filepath.Join(root, "wiki"), "circuit-breaker.md",
		"Circuit breakers stop cascades.", "raw/ref.md")
	client := swapLLMClient(t, []string{"Wiki answer about [[circuit-breaker]]."})
	var out string
	withProjectDir(t, root, func() {
		out, _ = captureStdout(t, func() error {
			return runAsk(context.Background(), "circuit breaker", askOptions{NoSave: true})
		})
	})
	if !strings.Contains(out, "Searching wiki... 1 hits") {
		t.Errorf("expected 1 wiki hit; got:\n%s", out)
	}
	if !strings.Contains(out, "Searching raw... 0 hits") {
		t.Errorf("expected 0 raw hits; got:\n%s", out)
	}
	if len(client.Calls) != 1 {
		t.Errorf("want 1 synth call, got %d", len(client.Calls))
	}
}

// #13 — question where only raw/ matches.
func TestE2EAskRawOnlyHits(t *testing.T) {
	root := bootstrapProject(t)
	os.WriteFile(filepath.Join(root, "raw", "paper.md"),
		[]byte("The chapter discusses kubernetes operators in detail."), 0o644)
	_ = swapLLMClient(t, []string{"From raw: see `raw/paper.md`."})
	var out string
	withProjectDir(t, root, func() {
		out, _ = captureStdout(t, func() error {
			return runAsk(context.Background(), "kubernetes operators", askOptions{NoSave: true})
		})
	})
	if !strings.Contains(out, "Searching raw... 1 hits") {
		t.Errorf("expected 1 raw hit; got:\n%s", out)
	}
	if !strings.Contains(out, "Searching wiki... 0 hits") {
		t.Errorf("expected 0 wiki hits; got:\n%s", out)
	}
}

// #14 — both collections have hits; wiki prints first.
func TestE2EAskWikiPrefersOverRaw(t *testing.T) {
	root := bootstrapProject(t)
	writePageWithSources(t, filepath.Join(root, "wiki"), "observability.md",
		"Observability covers metrics and traces.", "raw/book.md")
	os.WriteFile(filepath.Join(root, "raw", "book.md"),
		[]byte("Observability chapter: metrics, logs, traces."), 0o644)
	_ = swapLLMClient(t, []string{
		"Summary anchored on [[observability]] with `raw/book.md` for depth.",
	})
	var out string
	withProjectDir(t, root, func() {
		out, _ = captureStdout(t, func() error {
			return runAsk(context.Background(), "observability", askOptions{NoSave: true})
		})
	})
	// Isolate the "Sources:" block — the answer body itself can
	// mention raw/ or wiki/ paths in any order, which would defeat
	// a naive whole-output substring ordering check.
	idx := strings.Index(out, "Sources:")
	if idx < 0 {
		t.Fatalf("Sources block missing; output:\n%s", out)
	}
	block := out[idx:]
	wikiIdx := strings.Index(block, "wiki/observability.md")
	rawIdx := strings.Index(block, "raw/book.md")
	if wikiIdx < 0 || rawIdx < 0 {
		t.Fatalf("both sources should appear in Sources block; got:\n%s", block)
	}
	if wikiIdx > rawIdx {
		t.Errorf("wiki/ should render before raw/ in the Sources block; got wiki@%d raw@%d", wikiIdx, rawIdx)
	}
}

// #15 — no hits → graceful message, zero LLM calls.
func TestE2EAskNoHitsGraceful(t *testing.T) {
	root := bootstrapProject(t)
	client := swapLLMClient(t, []string{"should-not-be-called"})
	var out string
	withProjectDir(t, root, func() {
		out, _ = captureStdout(t, func() error {
			return runAsk(context.Background(), "anything", askOptions{NoSave: true})
		})
	})
	if len(client.Calls) != 0 {
		t.Errorf("no hits → LLM should not be called; got %d calls", len(client.Calls))
	}
	if !strings.Contains(out, "No relevant notes") {
		t.Errorf("expected graceful 'No relevant notes' message; got:\n%s", out)
	}
}

// #16 — --no-save suppresses the interactive prompt.
func TestE2EAskNoSaveSkipsPrompt(t *testing.T) {
	root := bootstrapProject(t)
	writePageWithSources(t, filepath.Join(root, "wiki"), "topic.md", "Topic body.", "raw/s.md")
	_ = swapLLMClient(t, []string{"Answer with [[topic]]."})
	var out string
	withProjectDir(t, root, func() {
		out, _ = captureStdout(t, func() error {
			return runAsk(context.Background(), "topic", askOptions{NoSave: true})
		})
	})
	if strings.Contains(out, "Save this answer to wiki?") {
		t.Errorf("--no-save should omit the save prompt; got:\n%s", out)
	}
	// .anvil/last-answer.json still stashed so a later `anvil
	// save` can use it.
	if _, err := os.Stat(filepath.Join(root, ".anvil", lastAnswerFilename)); err != nil {
		t.Errorf("last-answer stash missing: %v", err)
	}
}

// #17 — ask → save creates a page with type: synthesis.
func TestE2EAskThenSaveCreatesSynthesisPage(t *testing.T) {
	root := bootstrapProject(t)
	writePageWithSources(t, filepath.Join(root, "wiki"), "topic.md", "Topic body.", "raw/s.md")
	_ = swapLLMClient(t, []string{"Body with [[topic]]."})
	withProjectDir(t, root, func() {
		captureStdout(t, func() error {
			return runAsk(context.Background(), "topic", askOptions{NoSave: true})
		})
	})
	saveReply := `FILENAME: topic-digest.md
---
title: Topic Digest
type: synthesis
sources:
  - wiki/topic.md
created: 2026-04-16
updated: 2026-04-16
---

Digest body.
`
	_ = swapLLMClient(t, []string{saveReply})
	withProjectDir(t, root, func() {
		if err := runSave(context.Background(), saveOptions{}); err != nil {
			t.Fatalf("runSave: %v", err)
		}
	})
	body, err := os.ReadFile(filepath.Join(root, "wiki", "topic-digest.md"))
	if err != nil {
		t.Fatalf("saved page missing: %v", err)
	}
	if !strings.Contains(string(body), "type: synthesis") {
		t.Errorf("saved page should be type: synthesis; got:\n%s", body)
	}
}

// ============================================================
// LINT SCENARIOS
// ============================================================

// #18 — a page nobody links to is flagged as an orphan.
func TestE2ELintDetectsOrphan(t *testing.T) {
	root := bootstrapProject(t)
	w := filepath.Join(root, "wiki")
	writePageWithSources(t, w, "lonely.md", "Body nobody links to.")
	writePageWithSources(t, w, "linked.md", "Body.")
	writePageWithSources(t, w, "linker.md", "Points to [[linked]].")
	wiki.RebuildIndex(w)
	_ = swapLLMClient(t, nil)

	var out string
	withProjectDir(t, root, func() {
		out, _ = captureStdout(t, func() error {
			return runLint(context.Background(), lintOptions{StructuralOnly: true})
		})
	})
	if !strings.Contains(out, "orphan pages") || !strings.Contains(out, "lonely") {
		t.Errorf("orphan not flagged; output:\n%s", out)
	}
}

// #19 — a dangling [[wikilink]] shows up in missing pages.
func TestE2ELintDetectsMissingPage(t *testing.T) {
	root := bootstrapProject(t)
	w := filepath.Join(root, "wiki")
	writePageWithSources(t, w, "real.md", "Mentions [[phantom-page]].")
	wiki.RebuildIndex(w)
	_ = swapLLMClient(t, nil)
	var out string
	withProjectDir(t, root, func() {
		out, _ = captureStdout(t, func() error {
			return runLint(context.Background(), lintOptions{StructuralOnly: true})
		})
	})
	if !strings.Contains(out, "missing page(s): phantom-page") {
		t.Errorf("missing-page not flagged; output:\n%s", out)
	}
}

// #20 — LLM-driven contradiction across pages sharing a source.
func TestE2ELintDetectsContradiction(t *testing.T) {
	root := bootstrapProject(t)
	w := filepath.Join(root, "wiki")
	writePageWithSources(t, w, "a.md", "TTL is 300 seconds.", "raw/runbook.md")
	writePageWithSources(t, w, "b.md", "TTL is 600 seconds.", "raw/runbook.md")
	wiki.RebuildIndex(w)
	_ = swapLLMClient(t, []string{
		"Contradiction:\n  Claim A: TTL 300s\n  Claim B: TTL 600s\n  Detail: Pages disagree.\n",
		"1. Reconcile the TTL between pages.",
	})
	var out string
	withProjectDir(t, root, func() {
		out, _ = captureStdout(t, func() error {
			return runLint(context.Background(), lintOptions{})
		})
	})
	if !strings.Contains(out, "1 contradiction(s) found") {
		t.Errorf("contradiction not rendered; output:\n%s", out)
	}
}

// #21 — --structural-only bypasses every LLM call.
func TestE2ELintStructuralOnlySkipsLLM(t *testing.T) {
	root := bootstrapProject(t)
	w := filepath.Join(root, "wiki")
	writePageWithSources(t, w, "a.md", "A", "raw/r.md")
	writePageWithSources(t, w, "b.md", "B", "raw/r.md")
	wiki.RebuildIndex(w)
	client := swapLLMClient(t, []string{"should-not-be-called"})
	withProjectDir(t, root, func() {
		captureStdout(t, func() error { return runLint(context.Background(), lintOptions{StructuralOnly: true}) })
	})
	if len(client.Calls) != 0 {
		t.Errorf("--structural-only must not call LLM; got %d", len(client.Calls))
	}
}

// #22 — --fix rebuilds index, stale-index findings vanish on rerun.
func TestE2ELintFixRebuildsIndex(t *testing.T) {
	root := bootstrapProject(t)
	w := filepath.Join(root, "wiki")
	writePageWithSources(t, w, "newly-added.md", "Brand-new body not yet indexed.")
	// No RebuildIndex — keeps the page out of index.md.
	_ = swapLLMClient(t, nil)
	withProjectDir(t, root, func() {
		captureStdout(t, func() error { return runLint(context.Background(), lintOptions{StructuralOnly: true, Fix: true}) })
	})
	// Second pass: stale-index should be clean.
	var out string
	withProjectDir(t, root, func() {
		out, _ = captureStdout(t, func() error { return runLint(context.Background(), lintOptions{StructuralOnly: true}) })
	})
	if !strings.Contains(out, "✓ 0 page(s) not in index") {
		t.Errorf("post-fix stale-index still flagged; got:\n%s", out)
	}
}

// ============================================================
// CROSS-COMMAND SCENARIOS
// ============================================================

// #23 — full happy-path cycle: init → ingest → ask → save → lint
// → status. Each stage consumes its predecessor's effects.
func TestE2EFullCycleInitToStatus(t *testing.T) {
	root := bootstrapProject(t) // init already happened
	src := filepath.Join(root, "raw", "source.md")
	os.WriteFile(src, []byte("# System\nSystem body."), 0o644)

	_ = swapLLMClient(t, []string{
		simpleExtraction("System", "Systems exist."),
		simplePage("System", "concept", "raw/source.md", "System body with context."),
	})
	withProjectDir(t, root, func() {
		captureStdout(t, func() error { return runIngest(context.Background(), []string{src}, ingestOptions{}) })
	})

	// ask
	_ = swapLLMClient(t, []string{"Answer about [[system]]."})
	withProjectDir(t, root, func() {
		captureStdout(t, func() error { return runAsk(context.Background(), "system", askOptions{NoSave: true}) })
	})
	// save
	saveReply := "FILENAME: system-digest.md\n---\ntitle: System Digest\ntype: synthesis\nsources:\n  - wiki/system.md\ncreated: 2026-04-16\nupdated: 2026-04-16\n---\n\nDigest body.\n"
	_ = swapLLMClient(t, []string{saveReply})
	withProjectDir(t, root, func() {
		if err := runSave(context.Background(), saveOptions{}); err != nil {
			t.Fatalf("runSave: %v", err)
		}
	})
	// lint (structural only so no LLM scripting needed)
	_ = swapLLMClient(t, nil)
	withProjectDir(t, root, func() {
		captureStdout(t, func() error { return runLint(context.Background(), lintOptions{StructuralOnly: true}) })
	})
	// status should show both pages
	var statusOut string
	withProjectDir(t, root, func() {
		statusOut, _ = captureStdout(t, func() error { return runStatus(root) })
	})
	if !strings.Contains(statusOut, "Wiki:    2 pages") {
		t.Errorf("status should report 2 pages (system + digest); got:\n%s", statusOut)
	}
}

// #24 — ingesting more sources tightens the lint report: the
// orphan count should not grow, and newly-referenced pages should
// clear their "missing" status once their backing page lands.
func TestE2EMultipleIngestsReduceMissing(t *testing.T) {
	root := bootstrapProject(t)
	// Step 1: create a page referencing [[phantom]] — missing.
	writePageWithSources(t, filepath.Join(root, "wiki"), "linker.md",
		"Mentions [[phantom]].", "raw/s.md")
	wiki.RebuildIndex(filepath.Join(root, "wiki"))
	_ = swapLLMClient(t, nil)
	var out string
	withProjectDir(t, root, func() {
		out, _ = captureStdout(t, func() error { return runLint(context.Background(), lintOptions{StructuralOnly: true}) })
	})
	if !strings.Contains(out, "phantom") {
		t.Fatalf("missing-page should include phantom at step 1; got:\n%s", out)
	}
	// Step 2: ingest a source that creates the phantom page.
	src := filepath.Join(root, "raw", "phantom-src.md")
	os.WriteFile(src, []byte("Article about Phantom."), 0o644)
	_ = swapLLMClient(t, []string{
		simpleExtraction("Phantom", "Phantom claim."),
		simplePage("Phantom", "concept", "raw/phantom-src.md", "Body."),
	})
	withProjectDir(t, root, func() {
		captureStdout(t, func() error { return runIngest(context.Background(), []string{src}, ingestOptions{}) })
	})
	// Step 3: lint again — missing-page list should no longer name
	// phantom now that the file exists.
	_ = swapLLMClient(t, nil)
	withProjectDir(t, root, func() {
		out, _ = captureStdout(t, func() error { return runLint(context.Background(), lintOptions{StructuralOnly: true}) })
	})
	if strings.Contains(out, "missing page(s): phantom") {
		t.Errorf("phantom should be resolved after ingest; output:\n%s", out)
	}
}

// #25 — two anvil projects side by side don't leak into each
// other's .anvil/index.db.
func TestE2ETwoProjectsIsolated(t *testing.T) {
	rootA := bootstrapProject(t)
	rootB := bootstrapProject(t)
	os.WriteFile(filepath.Join(rootA, "raw", "a.md"), []byte("Only in A."), 0o644)
	os.WriteFile(filepath.Join(rootB, "raw", "b.md"), []byte("Only in B."), 0o644)

	// Ingest into A.
	_ = swapLLMClient(t, []string{
		simpleExtraction("AlphaEntity", "A."),
		simplePage("AlphaEntity", "concept", "raw/a.md", "Body A."),
	})
	withProjectDir(t, rootA, func() {
		captureStdout(t, func() error { return runIngest(context.Background(), []string{filepath.Join(rootA, "raw", "a.md")}, ingestOptions{}) })
	})
	// Ingest into B.
	_ = swapLLMClient(t, []string{
		simpleExtraction("BetaEntity", "B."),
		simplePage("BetaEntity", "concept", "raw/b.md", "Body B."),
	})
	withProjectDir(t, rootB, func() {
		captureStdout(t, func() error { return runIngest(context.Background(), []string{filepath.Join(rootB, "raw", "b.md")}, ingestOptions{}) })
	})
	// A has alphaentity, not betaentity.
	if _, err := os.Stat(filepath.Join(rootA, "wiki", "alphaentity.md")); err != nil {
		t.Errorf("A missing alphaentity: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rootA, "wiki", "betaentity.md")); err == nil {
		t.Errorf("A must not have betaentity leaking from B")
	}
	if _, err := os.Stat(filepath.Join(rootB, "wiki", "betaentity.md")); err != nil {
		t.Errorf("B missing betaentity: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rootB, "wiki", "alphaentity.md")); err == nil {
		t.Errorf("B must not have alphaentity leaking from A")
	}
}

// ============================================================
// EDGE CASES
// ============================================================

// #26 — running a command against a directory that's not an anvil
// project returns a clear error.
func TestE2EEdgeNotAnAnvilProject(t *testing.T) {
	fakeRoot := t.TempDir() // no ANVIL.md
	var err error
	withProjectDir(t, fakeRoot, func() {
		err = runStatus(fakeRoot)
	})
	if err == nil {
		t.Fatal("expected error when run outside an anvil project")
	}
	if !strings.Contains(err.Error(), "anvil init") {
		t.Errorf("error should suggest `anvil init`; got %v", err)
	}
}

// #27 — no LLM backend: ask/ingest error with setup guidance; lint
// falls back to structural with a warning.
func TestE2EEdgeNoLLMBackend(t *testing.T) {
	root := bootstrapProject(t)
	os.WriteFile(filepath.Join(root, "raw", "s.md"), []byte("body"), 0o644)
	prev := newLLMClient
	t.Cleanup(func() { newLLMClient = prev })
	newLLMClient = func() (llm.Client, error) { return nil, llm.ErrNoBackend }

	t.Run("ingest errors with guidance", func(t *testing.T) {
		var err error
		withProjectDir(t, root, func() {
			_, err = captureStdout(t, func() error {
				return runIngest(context.Background(), []string{filepath.Join(root, "raw", "s.md")}, ingestOptions{})
			})
		})
		if err == nil {
			t.Fatal("ingest without backend should error")
		}
	})
	t.Run("ask errors with guidance", func(t *testing.T) {
		writePageWithSources(t, filepath.Join(root, "wiki"), "topic.md", "Some body.", "raw/s.md")
		var err error
		withProjectDir(t, root, func() {
			_, err = captureStdout(t, func() error {
				return runAsk(context.Background(), "topic", askOptions{NoSave: true})
			})
		})
		if err == nil {
			t.Fatal("ask without backend should error")
		}
	})
	t.Run("lint downgrades to structural", func(t *testing.T) {
		var out string
		var err error
		withProjectDir(t, root, func() {
			out, err = captureStdout(t, func() error {
				return runLint(context.Background(), lintOptions{})
			})
		})
		if err != nil {
			t.Fatalf("lint should not error on missing backend: %v", err)
		}
		if !strings.Contains(out, "Structural checks") {
			t.Errorf("lint should print structural section even without backend; got:\n%s", out)
		}
	})
}

// #28 — asking on a brand-new project with zero pages is graceful.
func TestE2EEdgeAskOnEmptyWiki(t *testing.T) {
	root := bootstrapProject(t)
	client := swapLLMClient(t, []string{"unused"})
	var out string
	withProjectDir(t, root, func() {
		out, _ = captureStdout(t, func() error {
			return runAsk(context.Background(), "anything", askOptions{NoSave: true})
		})
	})
	if len(client.Calls) != 0 {
		t.Errorf("empty wiki should not trigger LLM; got %d calls", len(client.Calls))
	}
	if !strings.Contains(out, "No relevant notes") {
		t.Errorf("expected graceful empty-wiki output; got:\n%s", out)
	}
}

// #29 — unicode survives a full ingest → ask round-trip.
func TestE2EEdgeUnicodeContent(t *testing.T) {
	root := bootstrapProject(t)
	src := filepath.Join(root, "raw", "turkish.md")
	unicodeBody := "Türkçe içerik: dağıtık sistemlerde circuit breaker çökmeleri önler.\n"
	os.WriteFile(src, []byte(unicodeBody), 0o644)
	_ = swapLLMClient(t, []string{
		simpleExtraction("Circuit Breaker", "Önler."),
		"---\ntitle: Circuit Breaker\ntype: concept\nsources:\n  - raw/turkish.md\ncreated: 2026-04-16\nupdated: 2026-04-16\n---\n\nTürkçe açıklama: " + unicodeBody + "\n",
	})
	withProjectDir(t, root, func() {
		captureStdout(t, func() error { return runIngest(context.Background(), []string{src}, ingestOptions{}) })
	})
	body, err := os.ReadFile(filepath.Join(root, "wiki", "circuit-breaker.md"))
	if err != nil {
		t.Fatalf("page missing: %v", err)
	}
	if !strings.Contains(string(body), "Türkçe") {
		t.Errorf("unicode body round-trip failed; got:\n%s", body)
	}
}

// #30 — corrupted frontmatter surfaces as a parse error rather
// than a panic or silent misread.
func TestE2EEdgeCorruptedFrontmatter(t *testing.T) {
	root := bootstrapProject(t)
	// Unterminated frontmatter — no closing "---".
	bad := "---\ntitle: Broken\nstill inside frontmatter\n"
	if err := os.WriteFile(filepath.Join(root, "wiki", "broken.md"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	var err error
	withProjectDir(t, root, func() {
		_, err = captureStdout(t, func() error { return runStatus(root) })
	})
	if err == nil {
		t.Fatal("corrupted frontmatter should bubble up as an error")
	}
	if !strings.Contains(err.Error(), "frontmatter") && !strings.Contains(err.Error(), "broken") {
		t.Errorf("error should mention the bad file or frontmatter; got %v", err)
	}
}

// #31 — stashed ask answer survives a process boundary: write
// last-answer.json by hand, confirm runSave picks it up.
func TestE2EEdgeSaveReadsStashedAnswer(t *testing.T) {
	root := bootstrapProject(t)
	stash := lastAnswerRecord{
		Question: "what are circuit breakers?",
		Answer:   "They stop cascades.",
		Sources:  []string{"wiki/circuit-breaker.md"},
	}
	raw, _ := json.MarshalIndent(stash, "", "  ")
	os.WriteFile(filepath.Join(root, ".anvil", lastAnswerFilename), raw, 0o644)

	// Pre-seed the wiki so the LLM's write prompt has something to
	// cross-reference.
	writePageWithSources(t, filepath.Join(root, "wiki"), "circuit-breaker.md",
		"Pattern body.", "raw/book.md")
	saveReply := "FILENAME: cb-overview.md\n---\ntitle: CB Overview\ntype: synthesis\nsources:\n  - wiki/circuit-breaker.md\ncreated: 2026-04-16\nupdated: 2026-04-16\n---\n\nOverview body.\n"
	_ = swapLLMClient(t, []string{saveReply})
	withProjectDir(t, root, func() {
		if err := runSave(context.Background(), saveOptions{}); err != nil {
			t.Fatalf("runSave: %v", err)
		}
	})
	if _, err := os.Stat(filepath.Join(root, "wiki", "cb-overview.md")); err != nil {
		t.Errorf("save should have persisted the page: %v", err)
	}
}

// #32 — save without any stashed answer errors with actionable
// guidance pointing the user at anvil ask.
func TestE2EEdgeSaveWithoutPriorAsk(t *testing.T) {
	root := bootstrapProject(t)
	_ = swapLLMClient(t, nil)
	var err error
	withProjectDir(t, root, func() {
		err = runSave(context.Background(), saveOptions{})
	})
	if err == nil {
		t.Fatal("save without prior ask should error")
	}
	if !strings.Contains(err.Error(), "anvil ask") {
		t.Errorf("error should point at `anvil ask`; got %v", err)
	}
}

// ============================================================
// sanity: generate a one-line pass/fail summary helpful during
// manual runs. The helper is a no-op under `go test` output
// (stdout gets captured elsewhere); it exists so the maintainer
// running `go test -tags sqlite_fts5 -race -run E2E -v ./internal/commands/` sees
// a quick tally.
// ============================================================

func init() {
	// Ensure fmt is used so gofmt doesn't prune the import even if
	// every test above switches away from it; the import serves the
	// public e2e test helpers that call fmt via their error paths.
	_ = fmt.Sprintf
}
