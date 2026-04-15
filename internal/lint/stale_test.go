package lint

import (
	"context"
	"testing"

	"github.com/ugurcan-aytar/anvil/internal/wiki"
)

// writePageDated is a stale-test helper that stamps an arbitrary
// `updated` date on the page so age-gap logic has something to
// compute against.
func writePageDated(t *testing.T, wikiDir, filename, updated, body string, sources ...string) {
	t.Helper()
	p := &wiki.Page{
		Filename: filename,
		Title:    stemOf(filename),
		Type:     "concept",
		Sources:  sources,
		Created:  "2026-01-01",
		Updated:  updated,
		Body:     body,
	}
	if err := wiki.WritePage(wikiDir, p); err != nil {
		t.Fatal(err)
	}
}

// ============================================================
// Happy path: 60-day gap + overlap → LLM called, claims parsed.
// ============================================================

func TestDetectStaleClaimsHappyPath(t *testing.T) {
	w := seedWiki(t)
	writePageDated(t, w, "old-page.md", "2026-01-01",
		"Old claim: Redis TTL default is 300s.", "raw/shared.md")
	writePageDated(t, w, "new-page.md", "2026-04-01",
		"New team decision: Redis TTL is now 600s.", "raw/shared.md")

	reply := `StaleClaim:
  Claim: Redis TTL default is 300s
  Detail: New-page supersedes this with a 600s TTL decision.
`
	client := &scriptedClient{Responses: []string{reply}}
	claims, err := DetectStaleClaims(context.Background(), client, w)
	if err != nil {
		t.Fatalf("DetectStaleClaims: %v", err)
	}
	if len(claims) != 1 {
		t.Fatalf("want 1 stale claim, got %d (%+v)", len(claims), claims)
	}
	sc := claims[0]
	if sc.Page != "old-page" {
		t.Errorf("Page = %q", sc.Page)
	}
	if sc.OlderSource != "raw/shared.md" || sc.NewerSource != "raw/shared.md" {
		t.Errorf("sources mismatch: older=%q newer=%q", sc.OlderSource, sc.NewerSource)
	}
	if sc.Claim == "" {
		t.Errorf("Claim is empty")
	}
}

// ============================================================
// NO_STALE_CLAIMS reply → zero hits.
// ============================================================

func TestDetectStaleClaimsNegative(t *testing.T) {
	w := seedWiki(t)
	writePageDated(t, w, "a.md", "2026-01-01", "Old body", "raw/shared.md")
	writePageDated(t, w, "b.md", "2026-04-01", "Newer body", "raw/shared.md")

	client := &scriptedClient{Responses: []string{"NO_STALE_CLAIMS"}}
	claims, err := DetectStaleClaims(context.Background(), client, w)
	if err != nil {
		t.Fatalf("DetectStaleClaims: %v", err)
	}
	if len(claims) != 0 {
		t.Errorf("expected zero; got %+v", claims)
	}
}

// ============================================================
// Age gap under threshold → no LLM call.
// ============================================================

func TestDetectStaleClaimsSkipsSmallGaps(t *testing.T) {
	w := seedWiki(t)
	writePageDated(t, w, "a.md", "2026-04-01", "A", "raw/shared.md")
	writePageDated(t, w, "b.md", "2026-04-15", "B", "raw/shared.md")

	client := &scriptedClient{}
	claims, err := DetectStaleClaims(context.Background(), client, w)
	if err != nil {
		t.Fatalf("DetectStaleClaims: %v", err)
	}
	if len(claims) != 0 {
		t.Errorf("small gap should yield zero; got %+v", claims)
	}
	if len(client.Calls) != 0 {
		t.Errorf("small gap → no LLM call; got %d", len(client.Calls))
	}
}

// ============================================================
// No overlap → no LLM call even if gap is huge.
// ============================================================

func TestDetectStaleClaimsSkipsNoOverlap(t *testing.T) {
	w := seedWiki(t)
	writePageDated(t, w, "a.md", "2026-01-01", "A", "raw/a.md")
	writePageDated(t, w, "b.md", "2026-04-01", "B", "raw/b.md")

	client := &scriptedClient{}
	_, err := DetectStaleClaims(context.Background(), client, w)
	if err != nil {
		t.Fatalf("DetectStaleClaims: %v", err)
	}
	if len(client.Calls) != 0 {
		t.Errorf("no overlap → no LLM call; got %d", len(client.Calls))
	}
}

// ============================================================
// Nil client rejected.
// ============================================================

func TestDetectStaleClaimsRejectsNilClient(t *testing.T) {
	w := seedWiki(t)
	if _, err := DetectStaleClaims(context.Background(), nil, w); err == nil {
		t.Error("nil client should error")
	}
}
