package lint

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ugurcan-aytar/anvil/internal/llm"
	"github.com/ugurcan-aytar/anvil/internal/wiki"
)

// scriptedClient mirrors the one in commands/ask_integration_test.go
// so the lint package can stay self-contained (no test-package
// cross-imports).
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

// writePageWithSources is the contradiction/stale fixture builder —
// same shape as writePage but also stamps sources so overlap
// detection has something to latch onto.
func writePageWithSources(t *testing.T, wikiDir, filename, body string, sources ...string) {
	t.Helper()
	p := &wiki.Page{
		Filename: filename,
		Title:    stemOf(filename),
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
// Happy path: two pages with a shared source, one contradiction found.
// ============================================================

func TestDetectContradictionsHappyPath(t *testing.T) {
	w := seedWiki(t)
	writePageWithSources(t, w, "caching-strategies.md",
		"Redis TTL default is 300s per the team's production settings.",
		"raw/runbook.md")
	writePageWithSources(t, w, "meeting-2026-04-01.md",
		"Team set Redis TTL to 600s during the 04-01 meeting.",
		"raw/runbook.md")

	reply := `Contradiction:
  Claim A: Redis TTL default is 300s
  Claim B: Team set Redis TTL to 600s
  Detail: Pages disagree on the actual configured TTL value.
`
	client := &scriptedClient{Responses: []string{reply}}

	cs, err := DetectContradictions(context.Background(), client, w)
	if err != nil {
		t.Fatalf("DetectContradictions: %v", err)
	}
	if len(cs) != 1 {
		t.Fatalf("want 1 contradiction, got %d (%+v)", len(cs), cs)
	}
	c := cs[0]
	if c.PageA != "caching-strategies" || c.PageB != "meeting-2026-04-01" {
		t.Errorf("page stems = %q / %q", c.PageA, c.PageB)
	}
	if !strings.Contains(c.ClaimA, "300s") {
		t.Errorf("ClaimA = %q", c.ClaimA)
	}
	if !strings.Contains(c.ClaimB, "600s") {
		t.Errorf("ClaimB = %q", c.ClaimB)
	}
	if !strings.Contains(c.Detail, "disagree") {
		t.Errorf("Detail = %q", c.Detail)
	}
	if len(client.Calls) != 1 {
		t.Errorf("expected 1 LLM call, got %d", len(client.Calls))
	}
}

// ============================================================
// NO_CONTRADICTIONS reply yields zero hits.
// ============================================================

func TestDetectContradictionsNegative(t *testing.T) {
	w := seedWiki(t)
	writePageWithSources(t, w, "a.md", "A says X.", "raw/r.md")
	writePageWithSources(t, w, "b.md", "B says Y.", "raw/r.md")

	client := &scriptedClient{Responses: []string{"NO_CONTRADICTIONS"}}
	cs, err := DetectContradictions(context.Background(), client, w)
	if err != nil {
		t.Fatalf("DetectContradictions: %v", err)
	}
	if len(cs) != 0 {
		t.Errorf("NO_CONTRADICTIONS should yield zero hits; got %+v", cs)
	}
}

// ============================================================
// Pages without overlap aren't paired → no LLM call.
// ============================================================

func TestDetectContradictionsSkipsUnrelatedPairs(t *testing.T) {
	w := seedWiki(t)
	writePageWithSources(t, w, "a.md", "A", "raw/a.md")
	writePageWithSources(t, w, "b.md", "B", "raw/b.md") // disjoint sources

	client := &scriptedClient{}
	cs, err := DetectContradictions(context.Background(), client, w)
	if err != nil {
		t.Fatalf("DetectContradictions: %v", err)
	}
	if len(cs) != 0 {
		t.Errorf("disjoint pages should yield zero contradictions; got %+v", cs)
	}
	if len(client.Calls) != 0 {
		t.Errorf("no overlap → no LLM call; got %d calls", len(client.Calls))
	}
}

// ============================================================
// Overlap can be `related` OR `sources` — either triggers pairing.
// ============================================================

func TestDetectContradictionsMatchesRelatedOverlap(t *testing.T) {
	w := seedWiki(t)
	// Shared `related` entry, not sources.
	writePage(t, w, "a.md", "body", "shared-topic")
	writePage(t, w, "b.md", "body", "shared-topic")

	client := &scriptedClient{Responses: []string{"NO_CONTRADICTIONS"}}
	if _, err := DetectContradictions(context.Background(), client, w); err != nil {
		t.Fatalf("DetectContradictions: %v", err)
	}
	if len(client.Calls) != 1 {
		t.Errorf("related overlap should trigger 1 LLM call; got %d", len(client.Calls))
	}
}

// ============================================================
// Multi-block reply: two contradictions on one pair.
// ============================================================

func TestDetectContradictionsParsesMultipleBlocks(t *testing.T) {
	w := seedWiki(t)
	writePageWithSources(t, w, "a.md", "A", "raw/r.md")
	writePageWithSources(t, w, "b.md", "B", "raw/r.md")

	reply := `Contradiction:
  Claim A: First A claim
  Claim B: First B claim
  Detail: First conflict.

Contradiction:
  Claim A: Second A claim
  Claim B: Second B claim
  Detail: Second conflict.
`
	client := &scriptedClient{Responses: []string{reply}}
	cs, err := DetectContradictions(context.Background(), client, w)
	if err != nil {
		t.Fatalf("DetectContradictions: %v", err)
	}
	if len(cs) != 2 {
		t.Fatalf("want 2 contradictions, got %d (%+v)", len(cs), cs)
	}
}

// ============================================================
// Nil client rejected.
// ============================================================

func TestDetectContradictionsRejectsNilClient(t *testing.T) {
	w := seedWiki(t)
	if _, err := DetectContradictions(context.Background(), nil, w); err == nil {
		t.Error("nil client should error")
	}
}

// ============================================================
// Cap enforced: more than MaxContradictionPairs overlap-pairs → cap triggers.
// ============================================================

func TestDetectContradictionsCapsPairs(t *testing.T) {
	w := seedWiki(t)
	// Create enough overlap-sharing pages that the pair-count
	// exceeds MaxContradictionPairs. Six pages all sharing one source
	// produce C(6,2) = 15 pairs > cap of 10.
	for _, n := range []string{"a", "b", "c", "d", "e", "f"} {
		writePageWithSources(t, w, n+".md", "body", "raw/shared.md")
	}
	// Queue enough "NO" replies to cover the cap.
	replies := make([]string, MaxContradictionPairs)
	for i := range replies {
		replies[i] = "NO_CONTRADICTIONS"
	}
	client := &scriptedClient{Responses: replies}

	if _, err := DetectContradictions(context.Background(), client, w); err != nil {
		t.Fatalf("DetectContradictions: %v", err)
	}
	if len(client.Calls) != MaxContradictionPairs {
		t.Errorf("want exactly %d calls (cap), got %d", MaxContradictionPairs, len(client.Calls))
	}
}
