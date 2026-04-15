package ingest

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/ugurcan-aytar/anvil/internal/wiki"
)

// emptyWiki creates an empty wiki directory under t.TempDir().
func emptyWiki(t *testing.T) string {
	t.Helper()
	d := filepath.Join(t.TempDir(), "wiki")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	return d
}

// writePageFile drops a minimal markdown page into wikiDir.
func writePageFile(t *testing.T, wikiDir, filename, title, pageType string) {
	t.Helper()
	p := &wiki.Page{
		Filename: filename,
		Title:    title,
		Type:     pageType,
		Sources:  []string{"raw/old.md"},
		Created:  "2026-01-01",
		Updated:  "2026-01-01",
		Body:     "Existing body.\n",
	}
	if err := wiki.WritePage(wikiDir, p); err != nil {
		t.Fatal(err)
	}
}

// ============================================================
// Empty wiki: everything becomes a Create.
// ============================================================

func TestReconcileEmptyWikiAllCreates(t *testing.T) {
	wikiDir := emptyWiki(t)
	ext := &Extraction{
		Entities: []Entity{
			{Name: "Shopify", Description: "E-commerce platform."},
		},
		Concepts: []Concept{
			{Name: "Circuit Breaker", Description: "Fault-isolation pattern."},
			{Name: "Retry Pattern", Description: "Re-attempt on failure."},
		},
		Claims: []Claim{
			{Claim: "Prevents cascades.", Related: []string{"Circuit Breaker"}},
		},
		Connections: []Connection{
			{From: "Circuit Breaker", To: "Retry Pattern", Relationship: "complements"},
		},
	}

	result, err := Reconcile(ext, wikiDir, "raw/source.md")
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(result.Update) != 0 {
		t.Errorf("empty wiki should yield zero updates; got %+v", result.Update)
	}
	if len(result.Create) != 3 {
		t.Fatalf("expected 3 creates, got %d: %+v", len(result.Create), result.Create)
	}
	slugs := make([]string, len(result.Create))
	for i, d := range result.Create {
		slugs[i] = d.Slug
	}
	sort.Strings(slugs)
	want := []string{"circuit-breaker.md", "retry-pattern.md", "shopify.md"}
	for i, w := range want {
		if slugs[i] != w {
			t.Errorf("slugs[%d] = %q, want %q", i, slugs[i], w)
		}
	}

	// Claim + connection filtering: the CB draft carries the claim
	// and the connection; the founder's draft carries neither.
	var cbDraft *PageDraft
	for i := range result.Create {
		if result.Create[i].Slug == "circuit-breaker.md" {
			cbDraft = &result.Create[i]
		}
	}
	if cbDraft == nil {
		t.Fatal("CB draft missing")
	}
	if len(cbDraft.Claims) != 1 {
		t.Errorf("CB draft claims = %d, want 1", len(cbDraft.Claims))
	}
	if len(cbDraft.Connections) != 1 {
		t.Errorf("CB draft connections = %d, want 1", len(cbDraft.Connections))
	}
	if cbDraft.Type != "concept" {
		t.Errorf("CB draft Type = %q", cbDraft.Type)
	}
	if cbDraft.SourcePath != "raw/source.md" {
		t.Errorf("source path = %q", cbDraft.SourcePath)
	}
}

// ============================================================
// Existing page: entity maps to Update.
// ============================================================

func TestReconcileExistingPageBecomesUpdate(t *testing.T) {
	wikiDir := emptyWiki(t)
	writePageFile(t, wikiDir, "circuit-breaker.md", "Circuit Breaker", "concept")

	ext := &Extraction{
		Concepts: []Concept{
			{Name: "Circuit Breaker", Description: "Updated summary."},
		},
		Claims: []Claim{
			{Claim: "Half-open probe policy.", Related: []string{"Circuit Breaker"}},
		},
	}
	result, err := Reconcile(ext, wikiDir, "raw/new.md")
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(result.Create) != 0 {
		t.Errorf("existing page should not be Create'd; got %+v", result.Create)
	}
	if len(result.Update) != 1 {
		t.Fatalf("want 1 update, got %d", len(result.Update))
	}
	u := result.Update[0]
	if u.Slug != "circuit-breaker.md" {
		t.Errorf("slug = %q", u.Slug)
	}
	if u.Existing == nil {
		t.Fatal("Update should carry the existing page")
	}
	if u.Existing.Title != "Circuit Breaker" {
		t.Errorf("existing title = %q", u.Existing.Title)
	}
	if u.NewInfo == "" {
		t.Error("NewInfo should summarise what this ingest adds")
	}
	if !contains(u.NewInfo, "Half-open probe policy.") {
		t.Errorf("NewInfo missing the claim:\n%s", u.NewInfo)
	}
}

// ============================================================
// Deduplication: same name in both entities and concepts → one draft.
// ============================================================

func TestReconcileDedupesSameSlug(t *testing.T) {
	wikiDir := emptyWiki(t)
	ext := &Extraction{
		Entities: []Entity{{Name: "Rust"}},
		Concepts: []Concept{{Name: "Rust"}}, // same slug
	}
	result, err := Reconcile(ext, wikiDir, "raw/rust.md")
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	total := len(result.Create) + len(result.Update)
	if total != 1 {
		t.Errorf("want 1 page total, got %d (%+v)", total, result)
	}
}

// ============================================================
// Blank names are ignored (LLM occasionally emits empty entries).
// ============================================================

func TestReconcileSkipsBlankNames(t *testing.T) {
	wikiDir := emptyWiki(t)
	ext := &Extraction{
		Entities: []Entity{{Name: "   "}, {Name: ""}, {Name: "Alice"}},
	}
	result, _ := Reconcile(ext, wikiDir, "raw/x.md")
	if len(result.Create) != 1 || result.Create[0].Name != "Alice" {
		t.Errorf("should keep only Alice; got %+v", result.Create)
	}
}

// ============================================================
// Case-insensitive related-matching: "circuit breaker" in claim.Related
// still pairs with "Circuit Breaker" concept.
// ============================================================

func TestReconcileRelatedIsCaseInsensitive(t *testing.T) {
	wikiDir := emptyWiki(t)
	ext := &Extraction{
		Concepts: []Concept{{Name: "Circuit Breaker"}},
		Claims: []Claim{
			{Claim: "Trips after 5 failures.", Related: []string{"circuit breaker"}},
		},
	}
	result, _ := Reconcile(ext, wikiDir, "raw/x.md")
	if len(result.Create) != 1 {
		t.Fatalf("want 1 create, got %d", len(result.Create))
	}
	if len(result.Create[0].Claims) != 1 {
		t.Errorf("claim pairing should be case-insensitive; got %+v", result.Create[0].Claims)
	}
}

// ============================================================
// Nil extraction returns an error.
// ============================================================

func TestReconcileRejectsNilExtraction(t *testing.T) {
	if _, err := Reconcile(nil, "/tmp", "x"); err == nil {
		t.Error("Reconcile(nil, ...) should error")
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) &&
		(haystack == needle ||
			len(needle) == 0 ||
			indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
