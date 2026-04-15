package query

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ugurcan-aytar/anvil/internal/engine"
	"github.com/ugurcan-aytar/anvil/internal/wiki"
)

// seedProject writes the minimum skeleton engine.Open expects plus a
// few wiki + raw files so Query has something to hit.
func seedProject(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "project")
	for _, sub := range []string{"wiki", "raw", ".anvil"} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "ANVIL.md"), []byte("# schema\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func writeWikiPage(t *testing.T, root, filename, title, body string) {
	t.Helper()
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

func writeRawFile(t *testing.T, root, filename, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "raw", filename), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// openAndIndex opens the engine and runs a recall Index so the BM25
// tables are populated. All downstream query tests depend on this.
func openAndIndex(t *testing.T, root string) *engine.Engine {
	t.Helper()
	eng, err := engine.Open(root)
	if err != nil {
		t.Fatalf("engine.Open: %v", err)
	}
	t.Cleanup(func() { eng.Close() })
	if _, err := eng.Recall().Index(); err != nil {
		t.Fatalf("index: %v", err)
	}
	return eng
}

// ============================================================
// Wiki-only hit: a matching wiki page, no raw sources.
// ============================================================

func TestQuerySplitsWikiAndRawHits(t *testing.T) {
	root := seedProject(t)
	writeWikiPage(t, root, "circuit-breaker.md", "Circuit Breaker",
		"Circuit breakers stop cascading failures in distributed systems.")
	writeWikiPage(t, root, "retry-pattern.md", "Retry Pattern",
		"Retry pattern re-attempts failed operations.")
	writeRawFile(t, root, "system-design.md",
		"The circuit breaker pattern is discussed in chapter 4 of the system design book.")
	eng := openAndIndex(t, root)

	result, err := Query(context.Background(), eng, "circuit breaker", Options{TopK: 10})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(result.WikiHits) == 0 {
		t.Errorf("expected wiki hits; got none")
	}
	if len(result.RawHits) == 0 {
		t.Errorf("expected raw hits; got none")
	}
	// Each wiki hit should have a matching full page.
	if len(result.WikiPages) != len(result.WikiHits) {
		t.Errorf("WikiPages (%d) must match WikiHits (%d)",
			len(result.WikiPages), len(result.WikiHits))
	}
	// The loaded page should carry the full body + frontmatter fields.
	found := false
	for _, p := range result.WikiPages {
		if p.Filename == "circuit-breaker.md" {
			if p.Title != "Circuit Breaker" {
				t.Errorf("loaded wiki page Title = %q", p.Title)
			}
			if p.Type != "concept" {
				t.Errorf("loaded wiki page Type = %q", p.Type)
			}
			found = true
		}
	}
	if !found {
		t.Errorf("circuit-breaker page missing from WikiPages")
	}
}

// ============================================================
// Collection flag narrows the retrieval scope.
// ============================================================

func TestQueryCollectionFlag(t *testing.T) {
	root := seedProject(t)
	writeWikiPage(t, root, "widget.md", "Widget", "WIKIUNIQUETERM appears here.")
	writeRawFile(t, root, "paper.md", "RAWUNIQUETERM lives in the raw source.")
	eng := openAndIndex(t, root)

	cases := []struct {
		name            string
		query           string
		collection      string
		wantWikiHits    int
		wantRawHits     int
		wantWikiMinimum int // ≥ this count
	}{
		{"wiki-only finds wiki term", "WIKIUNIQUETERM", "wiki", 1, 0, 1},
		{"wiki-only misses raw term", "RAWUNIQUETERM", "wiki", 0, 0, 0},
		{"raw-only finds raw term", "RAWUNIQUETERM", "raw", 0, 1, 0},
		{"raw-only misses wiki term", "WIKIUNIQUETERM", "raw", 0, 0, 0},
		{"both-default finds wiki term", "WIKIUNIQUETERM", "", 1, 0, 1},
		{"both-default finds raw term", "RAWUNIQUETERM", "", 0, 1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := Query(context.Background(), eng, tc.query, Options{Collection: tc.collection, TopK: 10})
			if err != nil {
				t.Fatalf("Query: %v", err)
			}
			if len(r.WikiHits) != tc.wantWikiHits {
				t.Errorf("wiki hits = %d, want %d", len(r.WikiHits), tc.wantWikiHits)
			}
			if len(r.RawHits) != tc.wantRawHits {
				t.Errorf("raw hits = %d, want %d", len(r.RawHits), tc.wantRawHits)
			}
			if len(r.WikiPages) < tc.wantWikiMinimum {
				t.Errorf("WikiPages = %d, want >= %d",
					len(r.WikiPages), tc.wantWikiMinimum)
			}
		})
	}
}

// ============================================================
// Empty question is rejected before hitting recall.
// ============================================================

func TestQueryRejectsEmptyQuestion(t *testing.T) {
	root := seedProject(t)
	eng := openAndIndex(t, root)
	if _, err := Query(context.Background(), eng, "   ", Options{}); err == nil {
		t.Error("blank question should error")
	}
}

// ============================================================
// Zero TopK defaults to DefaultTopK rather than "return nothing".
// ============================================================

func TestQueryDefaultsTopKWhenZero(t *testing.T) {
	root := seedProject(t)
	// Seed 15 wiki pages so 0/default TopK behaviour is observable.
	for i := 0; i < 15; i++ {
		writeWikiPage(t, root, fpName(i), fpName(i), "circuit breaker appears here")
	}
	eng := openAndIndex(t, root)

	r, err := Query(context.Background(), eng, "circuit breaker", Options{TopK: 0})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	// DefaultTopK is 10 — we should see exactly 10 wiki hits, not 15.
	if len(r.WikiHits) != DefaultTopK {
		t.Errorf("TopK=0 default should cap at %d; got %d hits",
			DefaultTopK, len(r.WikiHits))
	}
}

// ============================================================
// Nil engine returns an error.
// ============================================================

func TestQueryRejectsNilEngine(t *testing.T) {
	if _, err := Query(context.Background(), nil, "q", Options{}); err == nil {
		t.Error("nil engine should error")
	}
}

// fpName returns a stable filename for the page-count test.
func fpName(i int) string {
	letters := []rune{'a', 'b', 'c', 'd', 'e', 'f', 'g', 'h', 'i', 'j', 'k', 'l', 'm', 'n', 'o'}
	return string(letters[i]) + ".md"
}
