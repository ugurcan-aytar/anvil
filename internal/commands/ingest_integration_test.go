// End-to-end ingest tests driven by a scripted LLM client. The mock
// replays fixed responses in order, so each test lays out the exact
// sequence of (extract, write, write, ...) calls the ingest pipeline
// will make and asserts on the resulting on-disk state.

package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ugurcan-aytar/anvil/internal/llm"
)

// scriptedClient is a per-package mock for the LLM. Identical contract
// to internal/llm.MockClient but kept local so commands/ tests don't
// need to import a test-only symbol from another package.
type scriptedClient struct {
	Responses []string
	Calls     []struct{ System, User string }
}

func (s *scriptedClient) Complete(_ context.Context, system, user string) (string, error) {
	s.Calls = append(s.Calls, struct{ System, User string }{system, user})
	idx := len(s.Calls) - 1
	if idx >= len(s.Responses) {
		return "", fmt.Errorf("mock: no scripted response for call %d (have %d)", idx+1, len(s.Responses))
	}
	return s.Responses[idx], nil
}

func (s *scriptedClient) Describe() string { return "Scripted LLM" }

var _ llm.Client = (*scriptedClient)(nil)

// swapLLMClient installs a scripted client for the duration of the
// test. Returns the client so the caller can inspect Calls.
func swapLLMClient(t *testing.T, responses []string) *scriptedClient {
	t.Helper()
	client := &scriptedClient{Responses: responses}
	prev := newLLMClient
	newLLMClient = func() (llm.Client, error) { return client, nil }
	t.Cleanup(func() { newLLMClient = prev })
	return client
}

// extractResponse builds the scripted YAML the extract prompt expects.
// Returns a single concept + one claim — enough to exercise the
// create-one-page path without drowning the assertions.
func extractResponse(concept, claim string) string {
	return "```yaml\n" +
		"entities: []\n" +
		"concepts:\n" +
		"  - name: \"" + concept + "\"\n" +
		"    description: \"Reusable description for tests.\"\n" +
		"claims:\n" +
		"  - claim: \"" + claim + "\"\n" +
		"    related: [\"" + concept + "\"]\n" +
		"connections: []\n" +
		"```\n"
}

// writePageResponse builds a scripted markdown page — what the LLM's
// write prompt is expected to return.
func writePageResponse(title, pageType, sourceRel, body string) string {
	return `---
title: ` + title + `
type: ` + pageType + `
sources:
  - ` + sourceRel + `
created: 2026-04-15
updated: 2026-04-15
---

` + body + `
`
}

// ============================================================
// End-to-end: init → ingest one source → create one page.
// Covers assertions 1-9 from the A2.8 spec.
// ============================================================

func TestIngestCreatesPageAndLogsIt(t *testing.T) {
	root := bootstrapProject(t)

	// Drop a source into raw/.
	rawRel := "raw/cb.md"
	rawBody := "# Circuit Breaker\n\nCircuit breakers stop cascading failures. Complements the retry pattern.\n"
	if err := os.WriteFile(filepath.Join(root, rawRel), []byte(rawBody), 0o644); err != nil {
		t.Fatal(err)
	}

	// Two responses: one for extract, one for write.
	client := swapLLMClient(t, []string{
		extractResponse("Circuit Breaker", "Circuit breakers stop cascading failures."),
		writePageResponse("Circuit Breaker", "concept", rawRel,
			"Circuit breakers stop cascading failures. See [[retry-pattern]] for the complementary technique."),
	})

	withProjectDir(t, root, func() {
		if _, err := captureStdout(t, func() error {
			return runIngest(context.Background(), []string{filepath.Join(root, rawRel)}, ingestOptions{})
		}); err != nil {
			t.Fatalf("runIngest: %v", err)
		}
	})

	// 1. wiki/circuit-breaker.md exists with correct frontmatter.
	pagePath := filepath.Join(root, "wiki", "circuit-breaker.md")
	page, err := os.ReadFile(pagePath)
	if err != nil {
		t.Fatalf("page not created: %v", err)
	}
	for _, want := range []string{
		"title: Circuit Breaker",
		"type: concept",
		rawRel, // appears in sources list
		"2026-04-15", // matches both created and updated lines regardless of quote shape
		"[[retry-pattern]]",
	} {
		if !strings.Contains(string(page), want) {
			t.Errorf("page missing %q; body:\n%s", want, page)
		}
	}

	// 2. wiki/index.md updated.
	idx, _ := os.ReadFile(filepath.Join(root, "wiki", "index.md"))
	if !strings.Contains(string(idx), "[[circuit-breaker]]") {
		t.Errorf("index.md missing new page; body:\n%s", idx)
	}

	// 3. wiki/log.md has an ingest entry.
	logBody, _ := os.ReadFile(filepath.Join(root, "wiki", "log.md"))
	if !strings.Contains(string(logBody), "ingest | "+rawRel) {
		t.Errorf("log.md missing ingest entry; body:\n%s", logBody)
	}
	if !strings.Contains(string(logBody), "Created: circuit-breaker.md") {
		t.Errorf("log.md missing Created line; body:\n%s", logBody)
	}

	// 4. Call count matches the sequence (1 extract + 1 write).
	if len(client.Calls) != 2 {
		t.Errorf("expected 2 LLM calls, got %d", len(client.Calls))
	}
}

// ============================================================
// Re-ingesting an unchanged file is a no-op — cache kicks in.
// ============================================================

func TestIngestSkipsUnchangedSource(t *testing.T) {
	root := bootstrapProject(t)
	rawRel := "raw/cb.md"
	if err := os.WriteFile(filepath.Join(root, rawRel), []byte("stable body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// First pass: normal ingest (extract + write).
	client := swapLLMClient(t, []string{
		extractResponse("Widget", "Widgets exist."),
		writePageResponse("Widget", "concept", rawRel, "Widgets exist."),
	})
	withProjectDir(t, root, func() {
		if _, err := captureStdout(t, func() error {
			return runIngest(context.Background(), []string{filepath.Join(root, rawRel)}, ingestOptions{})
		}); err != nil {
			t.Fatalf("first runIngest: %v", err)
		}
	})
	if len(client.Calls) != 2 {
		t.Fatalf("first pass: want 2 LLM calls, got %d", len(client.Calls))
	}

	// Second pass with the SAME scripted responses (should not be
	// consumed). Cache should short-circuit before any LLM call.
	client2 := swapLLMClient(t, []string{"should-not-be-called"})
	var out string
	withProjectDir(t, root, func() {
		var err error
		out, err = captureStdout(t, func() error {
			return runIngest(context.Background(), []string{filepath.Join(root, rawRel)}, ingestOptions{})
		})
		if err != nil {
			t.Fatalf("second runIngest: %v", err)
		}
	})
	if len(client2.Calls) != 0 {
		t.Errorf("re-ingest with unchanged file should make 0 LLM calls, got %d", len(client2.Calls))
	}
	if !strings.Contains(out, "Skipping") {
		t.Errorf("expected 'Skipping' in output; got:\n%s", out)
	}
}

// ============================================================
// Modify file → re-ingest → update existing page.
// ============================================================

func TestIngestUpdatesWhenSourceChanges(t *testing.T) {
	root := bootstrapProject(t)
	rawRel := "raw/cb.md"
	rawAbs := filepath.Join(root, rawRel)
	if err := os.WriteFile(rawAbs, []byte("v1: circuit breaker body\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// First ingest.
	_ = swapLLMClient(t, []string{
		extractResponse("Circuit Breaker", "v1 claim."),
		writePageResponse("Circuit Breaker", "concept", rawRel,
			"v1 body about circuit breakers."),
	})
	withProjectDir(t, root, func() {
		if _, err := captureStdout(t, func() error {
			return runIngest(context.Background(), []string{rawAbs}, ingestOptions{})
		}); err != nil {
			t.Fatalf("first runIngest: %v", err)
		}
	})

	// Change the source: cache hash changes, existing wiki page
	// should be updated (not duplicated).
	if err := os.WriteFile(rawAbs, []byte("v2: circuit breaker body, now with added retry discussion\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	updatedLLMBody := `---
title: Circuit Breaker
type: concept
sources:
  - ` + rawRel + `
created: 2026-04-15
updated: 2026-04-15
---

v2 body now mentions the [[retry-pattern]] explicitly.
`
	client := swapLLMClient(t, []string{
		extractResponse("Circuit Breaker", "v2 claim with retry mention."),
		updatedLLMBody,
	})
	withProjectDir(t, root, func() {
		if _, err := captureStdout(t, func() error {
			return runIngest(context.Background(), []string{rawAbs}, ingestOptions{})
		}); err != nil {
			t.Fatalf("second runIngest: %v", err)
		}
	})
	if len(client.Calls) != 2 {
		t.Errorf("second pass: want 2 calls (extract + update), got %d", len(client.Calls))
	}

	// The second call MUST be the update prompt — i.e. it should
	// contain the "wiki maintainer" directive, not the writer one.
	if !strings.Contains(client.Calls[1].User, "wiki maintainer") {
		t.Errorf("second call should use the update prompt; got:\n%s", client.Calls[1].User[:min(400, len(client.Calls[1].User))])
	}

	page, err := os.ReadFile(filepath.Join(root, "wiki", "circuit-breaker.md"))
	if err != nil {
		t.Fatalf("page missing: %v", err)
	}
	if !strings.Contains(string(page), "v2 body") {
		t.Errorf("page body should reflect v2 content; got:\n%s", page)
	}
	if strings.Contains(string(page), "v1 body") {
		t.Errorf("page still carries v1 body; got:\n%s", page)
	}
}

// ============================================================
// After ingest, `anvil status` reflects the new page count.
// ============================================================

func TestIngestReflectsInStatus(t *testing.T) {
	root := bootstrapProject(t)
	rawRel := "raw/thing.md"
	if err := os.WriteFile(filepath.Join(root, rawRel), []byte("content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = swapLLMClient(t, []string{
		extractResponse("Thing", "Things exist."),
		writePageResponse("Thing", "concept", rawRel, "Body."),
	})
	withProjectDir(t, root, func() {
		if _, err := captureStdout(t, func() error {
			return runIngest(context.Background(), []string{filepath.Join(root, rawRel)}, ingestOptions{})
		}); err != nil {
			t.Fatalf("runIngest: %v", err)
		}
	})
	var statusOut string
	withProjectDir(t, root, func() {
		var err error
		statusOut, err = captureStdout(t, func() error { return runStatus(root) })
		if err != nil {
			t.Fatalf("runStatus: %v", err)
		}
	})
	if !strings.Contains(statusOut, "Wiki:    1 pages") {
		t.Errorf("status should show 1 wiki page; got:\n%s", statusOut)
	}
	if !strings.Contains(statusOut, "Raw:     1 files") {
		t.Errorf("status should show 1 raw file; got:\n%s", statusOut)
	}
	if !strings.Contains(statusOut, "1 concept") {
		t.Errorf("status should report the type histogram; got:\n%s", statusOut)
	}
}

// ============================================================
// --dry-run: extract runs, write does NOT, cache NOT updated.
// ============================================================

func TestIngestDryRunSkipsWrites(t *testing.T) {
	root := bootstrapProject(t)
	rawRel := "raw/thing.md"
	if err := os.WriteFile(filepath.Join(root, rawRel), []byte("content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	client := swapLLMClient(t, []string{
		extractResponse("Thing", "Claim."),
	})
	withProjectDir(t, root, func() {
		if _, err := captureStdout(t, func() error {
			return runIngest(context.Background(), []string{filepath.Join(root, rawRel)}, ingestOptions{DryRun: true})
		}); err != nil {
			t.Fatalf("dry-run runIngest: %v", err)
		}
	})
	if len(client.Calls) != 1 {
		t.Errorf("dry-run should make 1 LLM call (extract only), got %d", len(client.Calls))
	}
	// No page created.
	if _, err := os.Stat(filepath.Join(root, "wiki", "thing.md")); err == nil {
		t.Error("dry-run should not have persisted a page")
	}
}

// ============================================================
// --force: re-ingests even when hash matches.
// ============================================================

func TestIngestForceBypassesCache(t *testing.T) {
	root := bootstrapProject(t)
	rawRel := "raw/thing.md"
	if err := os.WriteFile(filepath.Join(root, rawRel), []byte("content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = swapLLMClient(t, []string{
		extractResponse("Thing", "Claim."),
		writePageResponse("Thing", "concept", rawRel, "Body."),
	})
	withProjectDir(t, root, func() {
		if _, err := captureStdout(t, func() error {
			return runIngest(context.Background(), []string{filepath.Join(root, rawRel)}, ingestOptions{})
		}); err != nil {
			t.Fatalf("first runIngest: %v", err)
		}
	})
	// --force a second pass. Responses must be re-queued.
	client := swapLLMClient(t, []string{
		extractResponse("Thing", "Claim v2."),
		`---
title: Thing
type: concept
sources:
  - ` + rawRel + `
created: 2026-04-15
updated: 2026-04-15
---

Forced update body.
`,
	})
	withProjectDir(t, root, func() {
		if _, err := captureStdout(t, func() error {
			return runIngest(context.Background(), []string{filepath.Join(root, rawRel)}, ingestOptions{Force: true})
		}); err != nil {
			t.Fatalf("force runIngest: %v", err)
		}
	})
	if len(client.Calls) == 0 {
		t.Errorf("--force should have triggered LLM calls; got 0")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
