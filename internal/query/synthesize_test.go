package query

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ugurcan-aytar/anvil/internal/llm"
	"github.com/ugurcan-aytar/anvil/internal/wiki"
)

// scriptedClient is a minimal llm.Client stand-in for synthesis tests.
// Only returns queued responses; the synth code doesn't retry so a
// 1-element script covers every test.
type scriptedClient struct {
	Responses []string
	Calls     []struct{ System, User string }
}

func (s *scriptedClient) Complete(_ context.Context, system, user string) (string, error) {
	s.Calls = append(s.Calls, struct{ System, User string }{system, user})
	idx := len(s.Calls) - 1
	if idx >= len(s.Responses) {
		return "", fmt.Errorf("mock: no scripted response for call %d", idx+1)
	}
	return s.Responses[idx], nil
}

func (s *scriptedClient) Describe() string { return "Scripted" }

var _ llm.Client = (*scriptedClient)(nil)

// simpleResult builds a Result with one wiki page + one raw hit —
// the common case for synthesis tests.
func simpleResult() *Result {
	return &Result{
		WikiHits: []Hit{{Path: "circuit-breaker.md", Title: "Circuit Breaker", Score: 1.0}},
		WikiPages: []*wiki.Page{{
			Filename: "circuit-breaker.md",
			Title:    "Circuit Breaker",
			Type:     "concept",
			Body:     "Circuit breakers stop cascading failures in distributed systems.",
		}},
		RawHits: []Hit{{Path: "system-design.md", Title: "System Design", Score: 0.5, Snippet: "Chapter 4 covers the pattern."}},
	}
}

// ============================================================
// Happy path: valid citations verify; Sources + no Unverified.
// ============================================================

func TestSynthesizeVerifiesCitations(t *testing.T) {
	answer := "Circuit breakers stop cascades. See [[circuit-breaker]] for the pattern and `raw/system-design.md` for chapter 4."
	client := &scriptedClient{Responses: []string{answer}}
	ans, err := Synthesize(context.Background(), client, "what is a circuit breaker", simpleResult())
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if ans.Text != answer {
		t.Errorf("Text mismatch")
	}
	if len(ans.Sources) != 2 {
		t.Errorf("Sources = %v, want 2 items", ans.Sources)
	}
	if len(ans.Unverified) != 0 {
		t.Errorf("Unverified should be empty; got %v", ans.Unverified)
	}
	// Explicit expected sources.
	wants := []string{"wiki/circuit-breaker.md", "raw/system-design.md"}
	for _, w := range wants {
		found := false
		for _, s := range ans.Sources {
			if s == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Sources missing %q; got %v", w, ans.Sources)
		}
	}
}

// ============================================================
// Fabricated citation lands in Unverified.
// ============================================================

func TestSynthesizeFlagsFabricatedCitations(t *testing.T) {
	answer := "The answer involves [[made-up-page]] and `raw/not-real.md`, plus [[circuit-breaker]]."
	client := &scriptedClient{Responses: []string{answer}}
	ans, err := Synthesize(context.Background(), client, "q", simpleResult())
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	// circuit-breaker is in the result → verified.
	if len(ans.Sources) != 1 || ans.Sources[0] != "wiki/circuit-breaker.md" {
		t.Errorf("Sources = %v", ans.Sources)
	}
	// made-up-page and raw/not-real.md → unverified.
	if len(ans.Unverified) != 2 {
		t.Errorf("want 2 unverified citations, got %v", ans.Unverified)
	}
	got := strings.Join(ans.Unverified, ",")
	if !strings.Contains(got, "made-up-page") {
		t.Errorf("unverified missing made-up-page: %v", ans.Unverified)
	}
	if !strings.Contains(got, "raw/not-real.md") {
		t.Errorf("unverified missing raw/not-real.md: %v", ans.Unverified)
	}
}

// ============================================================
// Dedup: same citation cited three times → appears once.
// ============================================================

func TestSynthesizeDedupesCitations(t *testing.T) {
	answer := "[[circuit-breaker]] [[circuit-breaker]] [[circuit-breaker]]"
	client := &scriptedClient{Responses: []string{answer}}
	ans, err := Synthesize(context.Background(), client, "q", simpleResult())
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if len(ans.Sources) != 1 {
		t.Errorf("Sources = %v, want 1 (dedup)", ans.Sources)
	}
}

// ============================================================
// System prompt mentions the no-fabrication directive (regression guard).
// ============================================================

func TestSynthesizeSystemHasGroundingDirective(t *testing.T) {
	client := &scriptedClient{Responses: []string{"irrelevant"}}
	_, _ = Synthesize(context.Background(), client, "q", simpleResult())
	if len(client.Calls) != 1 {
		t.Fatalf("expected 1 call")
	}
	sys := client.Calls[0].System
	for _, want := range []string{
		"ONLY the context",
		"fabricate",
	} {
		if !strings.Contains(sys, want) {
			t.Errorf("system prompt missing %q; got:\n%s", want, sys)
		}
	}
}

// ============================================================
// User prompt includes the wiki page body AND raw snippets.
// ============================================================

func TestSynthesizeUserPromptIncludesContext(t *testing.T) {
	client := &scriptedClient{Responses: []string{""}}
	_, _ = Synthesize(context.Background(), client, "what is the pattern?", simpleResult())
	user := client.Calls[0].User
	for _, want := range []string{
		"what is the pattern?",
		"WIKI CONTEXT",
		"[[circuit-breaker]]",
		"stop cascading failures",
		"RAW CONTEXT",
		"raw/system-design.md",
		"Chapter 4 covers the pattern.",
	} {
		if !strings.Contains(user, want) {
			t.Errorf("user prompt missing %q; got:\n%s", want, user)
		}
	}
}

// ============================================================
// Empty context still renders — "(no wiki pages matched)" etc.
// ============================================================

func TestSynthesizeHandlesEmptyContext(t *testing.T) {
	client := &scriptedClient{Responses: []string{"I don't have enough context to answer."}}
	ans, err := Synthesize(context.Background(), client, "q", &Result{})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if len(ans.Sources) != 0 || len(ans.Unverified) != 0 {
		t.Errorf("empty context should yield no citations; got %v / %v", ans.Sources, ans.Unverified)
	}
	user := client.Calls[0].User
	if !strings.Contains(user, "(no wiki pages matched)") {
		t.Errorf("missing wiki placeholder; user:\n%s", user)
	}
	if !strings.Contains(user, "(no raw sources matched)") {
		t.Errorf("missing raw placeholder")
	}
}

// ============================================================
// Nil / empty inputs are rejected before the LLM call.
// ============================================================

func TestSynthesizeGuardsInputs(t *testing.T) {
	client := &scriptedClient{}
	if _, err := Synthesize(context.Background(), nil, "q", simpleResult()); err == nil {
		t.Error("nil client should error")
	}
	if _, err := Synthesize(context.Background(), client, "", simpleResult()); err == nil {
		t.Error("empty question should error")
	}
	if _, err := Synthesize(context.Background(), client, "q", nil); err == nil {
		t.Error("nil result should error")
	}
}

// ============================================================
// Generic backticks ("`foo()`") aren't mis-classified as citations.
// ============================================================

func TestSynthesizeIgnoresGenericBackticks(t *testing.T) {
	answer := "Use the `foo()` function and see [[circuit-breaker]]."
	client := &scriptedClient{Responses: []string{answer}}
	ans, err := Synthesize(context.Background(), client, "q", simpleResult())
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if len(ans.Sources) != 1 {
		t.Errorf("Sources = %v — expected only circuit-breaker", ans.Sources)
	}
	if len(ans.Unverified) != 0 {
		t.Errorf("generic `foo()` must not be treated as citation; got %v", ans.Unverified)
	}
}
