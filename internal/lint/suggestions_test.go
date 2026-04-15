package lint

import (
	"context"
	"strings"
	"testing"

	"github.com/ugurcan-aytar/anvil/internal/wiki"
)

// ============================================================
// Happy path: LLM returns a numbered list → parsed as strings.
// ============================================================

func TestSuggestParsesNumberedList(t *testing.T) {
	w := seedWiki(t)
	writePage(t, w, "hub.md", "Well-linked hub.")
	writePage(t, w, "leaf.md", "Links to [[hub]].")

	reply := `1. Create [[rate-limiting]] — referenced but missing.
2. Add a link from [[leaf]] to [[another-hub]].
3. Research: How do circuit breakers interact with retries?
`
	client := &scriptedClient{Responses: []string{reply}}
	g, err := wiki.BuildGraph(w)
	if err != nil {
		t.Fatal(err)
	}
	sugs, err := Suggest(context.Background(), client, w, g)
	if err != nil {
		t.Fatalf("Suggest: %v", err)
	}
	if len(sugs) != 3 {
		t.Errorf("want 3 suggestions, got %d (%+v)", len(sugs), sugs)
	}
	if !strings.Contains(sugs[0], "rate-limiting") {
		t.Errorf("first suggestion body missing expected text: %q", sugs[0])
	}
}

// ============================================================
// Cap at MaxSuggestions even when LLM returns more.
// ============================================================

func TestSuggestCapsAtMaxSuggestions(t *testing.T) {
	w := seedWiki(t)
	reply := `1. a
2. b
3. c
4. d
5. e
6. f
7. g`
	client := &scriptedClient{Responses: []string{reply}}
	g, err := wiki.BuildGraph(w)
	if err != nil {
		t.Fatal(err)
	}
	sugs, err := Suggest(context.Background(), client, w, g)
	if err != nil {
		t.Fatalf("Suggest: %v", err)
	}
	if len(sugs) != MaxSuggestions {
		t.Errorf("want %d suggestions (cap), got %d", MaxSuggestions, len(sugs))
	}
}

// ============================================================
// Prompt carries real stats — hub, orphan, missing counts.
// ============================================================

func TestSuggestPromptIncludesStats(t *testing.T) {
	w := seedWiki(t)
	writePage(t, w, "hub.md", "Hub page.")
	writePage(t, w, "linker.md", "Links to [[hub]] and [[ghost]].")
	writePage(t, w, "orphan-page.md", "No incoming links.")

	client := &scriptedClient{Responses: []string{"1. stub"}}
	g, err := wiki.BuildGraph(w)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Suggest(context.Background(), client, w, g); err != nil {
		t.Fatalf("Suggest: %v", err)
	}
	if len(client.Calls) != 1 {
		t.Fatalf("want 1 call, got %d", len(client.Calls))
	}
	user := client.Calls[0].User
	// Orphans: hub, orphan-page, linker all lack inbound — 3 orphans.
	// But hub has 1 incoming (linker). So orphans are linker + orphan-page.
	for _, want := range []string{
		"3 pages total",
		"Most connected: [[hub]]",
		"Wiki index:",
	} {
		if !strings.Contains(user, want) {
			t.Errorf("prompt missing %q; body:\n%s", want, user)
		}
	}
}

// ============================================================
// Nil graph is tolerated — stats just skip the graph-derived bits.
// ============================================================

func TestSuggestToleratesNilGraph(t *testing.T) {
	w := seedWiki(t)
	writePage(t, w, "only.md", "Single page, graph skipped.")
	client := &scriptedClient{Responses: []string{"1. x"}}

	if _, err := Suggest(context.Background(), client, w, nil); err != nil {
		t.Fatalf("Suggest: %v", err)
	}
	user := client.Calls[0].User
	// Page count still renders from ListPages, not the graph.
	if !strings.Contains(user, "1 pages total") {
		t.Errorf("prompt missing page count; body:\n%s", user)
	}
}

// ============================================================
// parseNumberedList tolerates prefix variation.
// ============================================================

func TestParseNumberedListAcceptsVariousPrefixes(t *testing.T) {
	reply := `1) first
2. second
3- third
4: fourth
- 5. fifth
some prose line that isn't a bullet
`
	got := parseNumberedList(reply, 10)
	if len(got) != 5 {
		t.Errorf("want 5 parsed items, got %d (%v)", len(got), got)
	}
}

// ============================================================
// Nil client rejected.
// ============================================================

func TestSuggestRejectsNilClient(t *testing.T) {
	w := seedWiki(t)
	if _, err := Suggest(context.Background(), nil, w, nil); err == nil {
		t.Error("nil client should error")
	}
}
