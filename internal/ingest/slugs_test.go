package ingest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ugurcan-aytar/anvil/internal/wiki"
)

// seedCatalogWiki writes a minimal set of pages whose stems become
// the slug catalog under test.
func seedCatalogWiki(t *testing.T, stems ...string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "wiki")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, stem := range stems {
		p := &wiki.Page{
			Filename: stem + ".md",
			Title:    stem,
			Type:     "concept",
			Body:     "Body.",
		}
		if err := wiki.WritePage(dir, p); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// ============================================================
// Slugs(): returns sorted stems from the wiki.
// ============================================================

func TestSlugCatalogLists(t *testing.T) {
	dir := seedCatalogWiki(t, "zebra", "alpha", "mu")
	cat, err := LoadSlugCatalog(dir)
	if err != nil {
		t.Fatalf("LoadSlugCatalog: %v", err)
	}
	got := cat.Slugs()
	want := []string{"alpha", "mu", "zebra"}
	if len(got) != len(want) {
		t.Fatalf("Slugs = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("Slugs[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// ============================================================
// Canonicalise: exact match passes through (true).
// ============================================================

func TestCanonicaliseExactMatch(t *testing.T) {
	dir := seedCatalogWiki(t, "nocodedevs")
	cat, _ := LoadSlugCatalog(dir)
	got, ok := cat.Canonicalise("nocodedevs")
	if !ok {
		t.Fatal("exact match should return true")
	}
	if got != "nocodedevs" {
		t.Errorf("got %q, want nocodedevs", got)
	}
}

// ============================================================
// Canonicalise: normalized match ("nocode-devs" → "nocodedevs").
// ============================================================

func TestCanonicaliseNormalizedMatch(t *testing.T) {
	dir := seedCatalogWiki(t, "nocodedevs")
	cat, _ := LoadSlugCatalog(dir)
	got, ok := cat.Canonicalise("nocode-devs")
	if !ok {
		t.Fatalf("normalized drift should match; got ok=false")
	}
	if got != "nocodedevs" {
		t.Errorf("canonical = %q, want nocodedevs", got)
	}
	// Bi-directional — "nocodedevs" seed but candidate "no_code_devs" also catches.
	got, ok = cat.Canonicalise("no_code_devs")
	if !ok || got != "nocodedevs" {
		t.Errorf("underscore variant: got %q / %v", got, ok)
	}
}

// ============================================================
// Canonicalise: Levenshtein ≤ 2 catches typos.
// ============================================================

func TestCanonicaliseLevenshteinMatch(t *testing.T) {
	dir := seedCatalogWiki(t, "circuit-breaker")
	cat, _ := LoadSlugCatalog(dir)
	// One-char typo → match.
	got, ok := cat.Canonicalise("circut-breaker")
	if !ok || got != "circuit-breaker" {
		t.Errorf("single-edit typo: got %q / %v", got, ok)
	}
	// Two-char typo → match.
	got, ok = cat.Canonicalise("circuitbraker")
	if !ok || got != "circuit-breaker" {
		// circuitbraker normalized = circuitbraker, seed normalized = circuitbreaker → lev=1
		t.Errorf("two-edit typo: got %q / %v", got, ok)
	}
}

// ============================================================
// Canonicalise: genuine new slug → no false positive.
// ============================================================

func TestCanonicaliseDoesNotFalseMatch(t *testing.T) {
	dir := seedCatalogWiki(t, "circuit-breaker")
	cat, _ := LoadSlugCatalog(dir)
	// Different concept entirely — should not collapse.
	if canon, ok := cat.Canonicalise("retry-pattern"); ok {
		t.Errorf("retry-pattern should be fresh, got canon=%q", canon)
	}
}

// ============================================================
// Canonicalise: short slugs bypass Levenshtein (length floor).
// ============================================================

func TestCanonicaliseShortSlugsSkipLevenshtein(t *testing.T) {
	dir := seedCatalogWiki(t, "ai")
	cat, _ := LoadSlugCatalog(dir)
	// Different 2-char slug — must NOT match via edit distance.
	if _, ok := cat.Canonicalise("ui"); ok {
		t.Errorf("short-slug levenshtein false match triggered")
	}
}

// ============================================================
// Canonicalise on empty catalog returns ("", false) without panicking.
// ============================================================

func TestCanonicaliseEmptyCatalog(t *testing.T) {
	dir := seedCatalogWiki(t) // no pages
	cat, _ := LoadSlugCatalog(dir)
	if _, ok := cat.Canonicalise("anything"); ok {
		t.Error("empty catalog should never claim canonical match")
	}
}

// ============================================================
// Reconcile route: drifted LLM name collapses onto existing slug.
// ============================================================

func TestReconcileCollapsesDriftedSlugs(t *testing.T) {
	dir := seedCatalogWiki(t, "nocodedevs")

	ext := &Extraction{
		Entities: []Entity{
			{Name: "Nocode Devs", Description: "drifted variant"},
		},
	}
	result, err := Reconcile(ext, dir, "raw/x.md")
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	// Should route to Update (existing page), not Create (new page).
	if len(result.Create) != 0 {
		t.Errorf("Create should be empty; got %+v", result.Create)
	}
	if len(result.Update) != 1 {
		t.Fatalf("Update should have one entry; got %+v", result.Update)
	}
	if result.Update[0].Slug != "nocodedevs.md" {
		t.Errorf("update slug = %q, want nocodedevs.md", result.Update[0].Slug)
	}
}

// ============================================================
// Prompt rendering: ExistingSlugs appears in the extract prompt.
// ============================================================

func TestExtractPromptIncludesSlugs(t *testing.T) {
	out, err := RenderExtractPrompt(ExtractContext{
		Title:         "Paper",
		Path:          "raw/paper.md",
		Content:       "body",
		ExistingSlugs: []string{"circuit-breaker", "retry-pattern"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Existing wiki pages",
		"- circuit-breaker",
		"- retry-pattern",
		"canonical slug",
	} {
		if !contains(out, want) {
			t.Errorf("extract prompt missing %q; body:\n%s", want, out)
		}
	}
}

// ============================================================
// Prompt rendering: empty slug list → section skipped.
// ============================================================

func TestExtractPromptSkipsWhenSlugsEmpty(t *testing.T) {
	out, err := RenderExtractPrompt(ExtractContext{
		Title:   "Fresh",
		Path:    "raw/x.md",
		Content: "body",
	})
	if err != nil {
		t.Fatal(err)
	}
	if contains(out, "Existing wiki pages") {
		t.Errorf("empty catalog should skip the section; body:\n%s", out)
	}
}
