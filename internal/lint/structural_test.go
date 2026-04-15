package lint

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ugurcan-aytar/anvil/internal/wiki"
)

// seedWiki creates an empty wiki/ under t.TempDir() and returns its
// path. Tests add pages on top via writePage.
func seedWiki(t *testing.T) string {
	t.Helper()
	d := filepath.Join(t.TempDir(), "wiki")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	return d
}

// writePage is the shared test helper for stamping a page into a
// wiki/ directory. Frontmatter defaults make it easy to construct a
// valid page with just a filename + body for most tests.
func writePage(t *testing.T, wikiDir, filename, body string, related ...string) {
	t.Helper()
	p := &wiki.Page{
		Filename: filename,
		Title:    stemOf(filename),
		Type:     "concept",
		Related:  related,
		Created:  "2026-04-15",
		Updated:  "2026-04-15",
		Body:     body,
	}
	if err := wiki.WritePage(wikiDir, p); err != nil {
		t.Fatal(err)
	}
}

// ============================================================
// CheckOrphans: page with no backlinks → orphan.
// ============================================================

func TestCheckOrphans(t *testing.T) {
	w := seedWiki(t)
	writePage(t, w, "linked.md", "Referenced by other pages.")
	writePage(t, w, "orphan.md", "Nobody links to me.")
	writePage(t, w, "referrer.md", "I mention [[linked]] somewhere.")

	orphans, err := CheckOrphans(w)
	if err != nil {
		t.Fatalf("CheckOrphans: %v", err)
	}
	sort.Strings(orphans)
	// Both "orphan" AND "referrer" are orphans (nobody links to them
	// — referrer points out, not in). "linked" is not.
	want := []string{"orphan", "referrer"}
	if len(orphans) != len(want) {
		t.Fatalf("orphans = %v, want %v", orphans, want)
	}
	for i, w := range want {
		if orphans[i] != w {
			t.Errorf("orphans[%d] = %q, want %q", i, orphans[i], w)
		}
	}
}

// ============================================================
// CheckMissingPages: wikilink target not on disk → flagged.
// ============================================================

func TestCheckMissingPages(t *testing.T) {
	w := seedWiki(t)
	writePage(t, w, "page-a.md", "Links to [[page-b]] which exists and [[ghost]] which doesn't.")
	writePage(t, w, "page-b.md", "Real page.")

	missing, err := CheckMissingPages(w)
	if err != nil {
		t.Fatalf("CheckMissingPages: %v", err)
	}
	if len(missing) != 1 || missing[0] != "ghost" {
		t.Errorf("missing = %v, want [ghost]", missing)
	}
}

// ============================================================
// CheckBrokenLinks: distinguishes body vs frontmatter origins.
// ============================================================

func TestCheckBrokenLinksSplitsByLocation(t *testing.T) {
	w := seedWiki(t)
	writePage(t, w, "page-a.md", "Links to [[phantom]] in the body.", "also-missing")
	writePage(t, w, "real.md", "Exists.")

	broken, err := CheckBrokenLinks(w)
	if err != nil {
		t.Fatalf("CheckBrokenLinks: %v", err)
	}
	// Expect exactly two entries: one body, one frontmatter.
	if len(broken) != 2 {
		t.Fatalf("broken = %+v, want 2 entries", broken)
	}
	var sawBody, sawFM bool
	for _, bl := range broken {
		if bl.SourcePage != "page-a" {
			t.Errorf("source = %q, want page-a", bl.SourcePage)
		}
		switch bl.Location {
		case "body":
			if bl.Target != "phantom" {
				t.Errorf("body target = %q", bl.Target)
			}
			sawBody = true
		case "frontmatter":
			if bl.Target != "also-missing" {
				t.Errorf("frontmatter target = %q", bl.Target)
			}
			sawFM = true
		}
	}
	if !sawBody || !sawFM {
		t.Errorf("missing one of body/frontmatter: %+v", broken)
	}
}

// ============================================================
// CheckEmptyPages: body shorter than threshold → flagged.
// ============================================================

func TestCheckEmptyPages(t *testing.T) {
	w := seedWiki(t)
	writePage(t, w, "full.md",
		"This page has plenty of content, easily more than fifty characters.")
	writePage(t, w, "sparse.md", "short")
	writePage(t, w, "just-at-limit.md", strings.Repeat("x", EmptyBodyThreshold+10))

	empty, err := CheckEmptyPages(w)
	if err != nil {
		t.Fatalf("CheckEmptyPages: %v", err)
	}
	if len(empty) != 1 || empty[0] != "sparse" {
		t.Errorf("empty = %v, want [sparse]", empty)
	}
}

// ============================================================
// CheckStaleIndex: page on disk but not in index.md → flagged.
// ============================================================

func TestCheckStaleIndex(t *testing.T) {
	w := seedWiki(t)
	writePage(t, w, "indexed.md", "A")
	writePage(t, w, "unindexed.md", "B")
	// Partial index — mentions only "indexed".
	indexBody := "# Index\n\n| Page | Type | TLDR |\n|------|------|------|\n| [[indexed]] | concept | . |\n"
	if err := os.WriteFile(filepath.Join(w, wiki.IndexFilename), []byte(indexBody), 0o644); err != nil {
		t.Fatal(err)
	}

	stale, err := CheckStaleIndex(w)
	if err != nil {
		t.Fatalf("CheckStaleIndex: %v", err)
	}
	if len(stale) != 1 || stale[0] != "unindexed" {
		t.Errorf("stale = %v, want [unindexed]", stale)
	}
}

// ============================================================
// CheckStaleIndex: no index file → every page reported.
// ============================================================

func TestCheckStaleIndexNoFile(t *testing.T) {
	w := seedWiki(t)
	writePage(t, w, "a.md", "A")
	writePage(t, w, "b.md", "B")

	stale, err := CheckStaleIndex(w)
	if err != nil {
		t.Fatalf("CheckStaleIndex: %v", err)
	}
	if len(stale) != 2 {
		t.Errorf("no-index → want all pages stale; got %v", stale)
	}
}

// ============================================================
// Empty wiki dir: every check returns empty, no error.
// ============================================================

func TestChecksHandleEmptyWiki(t *testing.T) {
	w := seedWiki(t)
	if got, err := CheckOrphans(w); err != nil || len(got) != 0 {
		t.Errorf("orphans on empty wiki: %v / %v", got, err)
	}
	if got, err := CheckMissingPages(w); err != nil || len(got) != 0 {
		t.Errorf("missing on empty wiki: %v / %v", got, err)
	}
	if got, err := CheckBrokenLinks(w); err != nil || len(got) != 0 {
		t.Errorf("broken on empty wiki: %v / %v", got, err)
	}
	if got, err := CheckEmptyPages(w); err != nil || len(got) != 0 {
		t.Errorf("empty on empty wiki: %v / %v", got, err)
	}
	if got, err := CheckStaleIndex(w); err != nil || len(got) != 0 {
		t.Errorf("stale-index on empty wiki: %v / %v", got, err)
	}
}
