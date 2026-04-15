package lint

import (
	"context"
	"testing"
)

// ============================================================
// Structural-only mode: no LLM calls, LLM fields stay empty.
// ============================================================

func TestRunStructuralOnlySkipsLLM(t *testing.T) {
	w := seedWiki(t)
	writePage(t, w, "orphan.md", "Body.")
	writePage(t, w, "referrer.md", "Points to [[ghost]].")

	client := &scriptedClient{}
	report, err := Run(context.Background(), client, w, RunOptions{StructuralOnly: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(client.Calls) != 0 {
		t.Errorf("StructuralOnly should bypass every LLM call; got %d", len(client.Calls))
	}
	// Structural findings landed.
	if len(report.Orphans) == 0 {
		t.Errorf("orphans should be populated")
	}
	if len(report.MissingPages) == 0 {
		t.Errorf("missing pages should be populated")
	}
	// LLM-backed fields stay empty.
	if len(report.Contradictions) != 0 || len(report.StaleClaims) != 0 || len(report.Suggestions) != 0 {
		t.Errorf("LLM fields should be empty under --structural-only; report=%+v", report)
	}
	if report.HealthScore <= 0 {
		t.Errorf("health score should be non-zero; got %v", report.HealthScore)
	}
}

// ============================================================
// Full mode with nil client skips LLM (defense against CI misuse).
// ============================================================

func TestRunNilClientSkipsLLM(t *testing.T) {
	w := seedWiki(t)
	writePage(t, w, "a.md", "Body.")
	report, err := Run(context.Background(), nil, w, RunOptions{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(report.Contradictions) != 0 {
		t.Errorf("nil client → no contradictions; got %+v", report.Contradictions)
	}
}

// ============================================================
// Empty wiki → every field empty, score pegged at 100.
// ============================================================

func TestRunOnEmptyWiki(t *testing.T) {
	w := seedWiki(t)
	report, err := Run(context.Background(), nil, w, RunOptions{StructuralOnly: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.PageCount != 0 {
		t.Errorf("PageCount = %d on empty wiki", report.PageCount)
	}
	if report.HealthScore != MaxHealthScore {
		t.Errorf("empty wiki health = %v, want %v", report.HealthScore, MaxHealthScore)
	}
}

// ============================================================
// Full mode with overlap pages → contradictions + stale claims land.
// ============================================================

func TestRunFullModePopulatesLLMFields(t *testing.T) {
	w := seedWiki(t)
	writePageDated(t, w, "old.md", "2026-01-01", "Old claim here.", "raw/shared.md")
	writePageDated(t, w, "new.md", "2026-04-01", "Newer correction.", "raw/shared.md")

	// Script: 1 contradiction call + 1 stale call + 1 suggest call.
	// First pair returns a contradiction, same pair returns a stale
	// claim, then suggestions.
	contradictionReply := `Contradiction:
  Claim A: Old claim
  Claim B: New correction
  Detail: Newer page supersedes.
`
	staleReply := `StaleClaim:
  Claim: Old claim here.
  Detail: Newer page corrects it.
`
	suggestReply := `1. Merge [[old]] and [[new]] into one page.`
	client := &scriptedClient{Responses: []string{contradictionReply, staleReply, suggestReply}}

	report, err := Run(context.Background(), client, w, RunOptions{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(report.Contradictions) == 0 {
		t.Errorf("expected contradictions to be populated")
	}
	if len(report.StaleClaims) == 0 {
		t.Errorf("expected stale claims to be populated")
	}
	if len(report.Suggestions) == 0 {
		t.Errorf("expected suggestions to be populated")
	}
	// 3 LLM calls — 1 contradiction, 1 stale, 1 suggest.
	if len(client.Calls) != 3 {
		t.Errorf("want 3 LLM calls, got %d", len(client.Calls))
	}
}
