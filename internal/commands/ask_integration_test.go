// End-to-end tests for `anvil ask` + `anvil save`. Each test seeds a
// real tempdir project, runs the commands through their runAsk /
// runSave entry points (the same ones Cobra calls), and asserts on
// the on-disk state. Mock LLM responses are scripted per test so the
// ask → save pipeline is deterministic.

package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ugurcan-aytar/anvil/internal/wiki"
)

// prepareAskProject creates a project + writes two wiki pages and one
// raw source, then returns the project root. Shared by most ask tests
// because the retrieval surface is cheap to build but clunky to
// repeat inline.
func prepareAskProject(t *testing.T) string {
	t.Helper()
	root := bootstrapProject(t)

	writePage := func(filename, title, body string) {
		p := &wiki.Page{
			Filename: filename,
			Title:    title,
			Type:     "concept",
			Sources:  []string{"raw/source.md"},
			Created:  "2026-04-15",
			Updated:  "2026-04-15",
			Body:     body,
		}
		if err := wiki.WritePage(filepath.Join(root, "wiki"), p); err != nil {
			t.Fatal(err)
		}
	}
	writePage("circuit-breaker.md", "Circuit Breaker",
		"Circuit breakers stop cascading failures in distributed systems.")
	writePage("retry-pattern.md", "Retry Pattern",
		"Retry pattern re-attempts failed operations with backoff.")

	if err := os.WriteFile(filepath.Join(root, "raw", "system-design.md"),
		[]byte("Chapter 4 discusses the circuit breaker at length."), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// ============================================================
// Ask happy path: hits found, answer + sources printed, last-answer.json stashed.
// ============================================================

func TestAskRetrievesAndSynthesises(t *testing.T) {
	root := prepareAskProject(t)

	answer := "Circuit breakers stop cascading failures. See [[circuit-breaker]] for the pattern, with `raw/system-design.md` offering a deeper treatment."
	client := swapLLMClient(t, []string{answer})

	var out string
	withProjectDir(t, root, func() {
		var err error
		out, err = captureStdout(t, func() error {
			return runAsk(context.Background(), "what is a circuit breaker?",
				askOptions{NoSave: true})
		})
		if err != nil {
			t.Fatalf("runAsk: %v", err)
		}
	})
	if len(client.Calls) != 1 {
		t.Errorf("expected 1 LLM call, got %d", len(client.Calls))
	}
	// Output contains the retrieval summary and the answer body.
	for _, want := range []string{
		"Searching wiki...",
		"Searching raw...",
		"[[circuit-breaker]]",
		"raw/system-design.md",
		"Sources:",
		"wiki/circuit-breaker.md (compiled)",
		"raw/system-design.md (primary)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("ask output missing %q; got:\n%s", want, out)
		}
	}

	// last-answer.json exists + has the expected shape.
	lastPath := filepath.Join(root, ".anvil", lastAnswerFilename)
	raw, err := os.ReadFile(lastPath)
	if err != nil {
		t.Fatalf("last-answer.json not stashed: %v", err)
	}
	var rec lastAnswerRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatalf("parse last-answer.json: %v", err)
	}
	if rec.Question != "what is a circuit breaker?" {
		t.Errorf("stashed Question = %q", rec.Question)
	}
	if rec.Answer != answer {
		t.Errorf("stashed Answer mismatch")
	}
	if len(rec.Sources) == 0 {
		t.Errorf("stashed Sources should be populated; got empty")
	}
}

// ============================================================
// Fabricated citation ends up in Unverified, rendered under a warning.
// ============================================================

func TestAskFlagsUnverifiedCitations(t *testing.T) {
	root := prepareAskProject(t)
	answer := "The pattern lives in [[made-up-name]] according to my notes."
	_ = swapLLMClient(t, []string{answer})

	var out string
	withProjectDir(t, root, func() {
		var err error
		out, err = captureStdout(t, func() error {
			return runAsk(context.Background(), "what is a circuit breaker?",
				askOptions{NoSave: true})
		})
		if err != nil {
			t.Fatalf("runAsk: %v", err)
		}
	})
	if !strings.Contains(out, "Unverified citations") {
		t.Errorf("expected 'Unverified citations' block; got:\n%s", out)
	}
	if !strings.Contains(out, "made-up-name") {
		t.Errorf("fabricated citation should be listed; got:\n%s", out)
	}
}

// ============================================================
// No hits at all — ask reports gracefully without calling the LLM.
// ============================================================

func TestAskReportsNoHitsGracefully(t *testing.T) {
	root := bootstrapProject(t)
	client := swapLLMClient(t, []string{"should-not-be-called"})

	var out string
	withProjectDir(t, root, func() {
		var err error
		out, err = captureStdout(t, func() error {
			return runAsk(context.Background(), "anything at all",
				askOptions{NoSave: true})
		})
		if err != nil {
			t.Fatalf("runAsk: %v", err)
		}
	})
	if len(client.Calls) != 0 {
		t.Errorf("empty retrieval should skip LLM; got %d calls", len(client.Calls))
	}
	if !strings.Contains(out, "No relevant notes") {
		t.Errorf("expected no-hits message; got:\n%s", out)
	}
}

// ============================================================
// Save flow: run ask + save via interactive "y" → page persists.
// ============================================================

func TestSaveWritesWikiPageFromLastAnswer(t *testing.T) {
	root := prepareAskProject(t)

	// Two scripted responses: one for ask (the synth reply), one for
	// save (the wiki-page materialisation).
	askReply := "Circuit breakers stop cascading failures. See [[circuit-breaker]]."
	saveReply := `FILENAME: circuit-breaker-overview.md
---
title: Circuit Breaker Overview
type: synthesis
sources:
  - wiki/circuit-breaker.md
created: 2026-04-16
updated: 2026-04-16
---

Circuit breakers stop cascading failures in distributed systems.
Related pattern: [[retry-pattern]].
`
	_ = swapLLMClient(t, []string{askReply, saveReply})
	// Drive "y" into the interactive prompt.
	prevStdin := askStdin
	askStdin = bytes.NewBufferString("y\n")
	t.Cleanup(func() { askStdin = prevStdin })

	withProjectDir(t, root, func() {
		if _, err := captureStdout(t, func() error {
			return runAsk(context.Background(), "circuit breaker", askOptions{})
		}); err != nil {
			t.Fatalf("runAsk: %v", err)
		}
	})

	// Page landed under wiki/ with correct frontmatter + body.
	savedPath := filepath.Join(root, "wiki", "circuit-breaker-overview.md")
	body, err := os.ReadFile(savedPath)
	if err != nil {
		t.Fatalf("save page missing: %v", err)
	}
	for _, want := range []string{
		"title: Circuit Breaker Overview",
		"type: synthesis",
		"wiki/circuit-breaker.md",
		"[[retry-pattern]]",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("saved page missing %q; body:\n%s", want, body)
		}
	}

	// index.md updated.
	idx, _ := os.ReadFile(filepath.Join(root, "wiki", "index.md"))
	if !strings.Contains(string(idx), "[[circuit-breaker-overview]]") {
		t.Errorf("index missing saved synthesis row; body:\n%s", idx)
	}

	// log.md carries a save entry.
	logBody, _ := os.ReadFile(filepath.Join(root, "wiki", "log.md"))
	if !strings.Contains(string(logBody), "save | circuit breaker") {
		t.Errorf("log missing save entry; body:\n%s", logBody)
	}
	if !strings.Contains(string(logBody), "Created: circuit-breaker-overview.md") {
		t.Errorf("log save entry missing Created line; body:\n%s", logBody)
	}
}

// ============================================================
// `anvil save` without a prior ask → actionable error.
// ============================================================

func TestSaveRejectsWithoutPriorAsk(t *testing.T) {
	root := bootstrapProject(t)
	_ = swapLLMClient(t, []string{"should-not-be-called"})

	var err error
	withProjectDir(t, root, func() {
		err = runSave(context.Background(), saveOptions{})
	})
	if err == nil {
		t.Fatal("runSave without prior ask should error")
	}
	if !strings.Contains(err.Error(), "anvil ask") {
		t.Errorf("error should point user at `anvil ask`; got %v", err)
	}
}

// ============================================================
// --name flag overrides the LLM's suggested filename.
// ============================================================

func TestSaveRespectsNameOverride(t *testing.T) {
	root := prepareAskProject(t)

	askReply := "See [[circuit-breaker]]."
	saveReply := `FILENAME: llm-suggested-name.md
---
title: From LLM
type: synthesis
sources:
  - wiki/circuit-breaker.md
created: 2026-04-16
updated: 2026-04-16
---

Body.
`
	_ = swapLLMClient(t, []string{askReply, saveReply})
	withProjectDir(t, root, func() {
		if _, err := captureStdout(t, func() error {
			return runAsk(context.Background(), "circuit breaker", askOptions{NoSave: true})
		}); err != nil {
			t.Fatalf("runAsk: %v", err)
		}
	})
	_ = swapLLMClient(t, []string{saveReply})
	withProjectDir(t, root, func() {
		if err := runSave(context.Background(), saveOptions{Name: "user-override"}); err != nil {
			t.Fatalf("runSave: %v", err)
		}
	})
	if _, err := os.Stat(filepath.Join(root, "wiki", "user-override.md")); err != nil {
		t.Errorf("override filename not used: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "wiki", "llm-suggested-name.md")); err == nil {
		t.Errorf("LLM's suggested filename should NOT have been used")
	}
}

// ============================================================
// --no-save skips the prompt and stashes the answer only.
// ============================================================

func TestAskNoSaveSkipsSavePrompt(t *testing.T) {
	root := prepareAskProject(t)
	client := swapLLMClient(t, []string{"Plain answer with [[circuit-breaker]]."})

	// askStdin is deliberately left empty — if the prompt fires, it'll
	// hit EOF and the save path won't be invoked.
	withProjectDir(t, root, func() {
		if _, err := captureStdout(t, func() error {
			return runAsk(context.Background(), "circuit breaker", askOptions{NoSave: true})
		}); err != nil {
			t.Fatalf("runAsk: %v", err)
		}
	})
	// Only the ask LLM call should fire — no save call.
	if len(client.Calls) != 1 {
		t.Errorf("want 1 LLM call with --no-save; got %d", len(client.Calls))
	}
	// last-answer.json still written so a later `anvil save` works.
	if _, err := os.Stat(filepath.Join(root, ".anvil", lastAnswerFilename)); err != nil {
		t.Errorf("--no-save should still stash last answer: %v", err)
	}
}
