package wiki

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writePageTest(t *testing.T, dir, filename, title, pageType, body string) {
	t.Helper()
	p := &Page{Title: title, Type: pageType, Body: body, Filename: filename}
	if err := WritePage(dir, p); err != nil {
		t.Fatalf("WritePage %s: %v", filename, err)
	}
}

func TestRebuildIndexFromEmptyDir(t *testing.T) {
	dir := t.TempDir()
	if err := RebuildIndex(dir); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, IndexFilename))
	if string(got) != IndexEmptyBody {
		t.Errorf("empty index = %q, want %q", got, IndexEmptyBody)
	}
}

func TestRebuildIndexTableShape(t *testing.T) {
	dir := t.TempDir()
	writePageTest(t, dir, "alpha.md", "Alpha", "concept", "First. Second.")
	writePageTest(t, dir, "beta.md", "Beta", "entity", "Just one sentence with no period")
	if err := RebuildIndex(dir); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, IndexFilename))
	s := string(got)
	if !strings.Contains(s, "# Index") {
		t.Errorf("missing header:\n%s", s)
	}
	if !strings.Contains(s, "| Page | Type | TLDR |") {
		t.Errorf("missing table header:\n%s", s)
	}
	if !strings.Contains(s, "| [[alpha]] | concept | First. |") {
		t.Errorf("alpha row missing / wrong:\n%s", s)
	}
	if !strings.Contains(s, "| [[beta]] | entity | Just one sentence with no period |") {
		t.Errorf("beta row missing / wrong:\n%s", s)
	}
}

func TestAddToIndexAppendsNewRow(t *testing.T) {
	dir := t.TempDir()
	writePageTest(t, dir, "alpha.md", "Alpha", "concept", "First.")
	if err := RebuildIndex(dir); err != nil {
		t.Fatal(err)
	}
	// New page + AddToIndex.
	newPage := &Page{Title: "Beta", Type: "entity", Body: "Second.", Filename: "beta.md"}
	writePageTest(t, dir, newPage.Filename, newPage.Title, newPage.Type, newPage.Body)
	if err := AddToIndex(dir, newPage, ""); err != nil {
		t.Fatalf("AddToIndex: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, IndexFilename))
	s := string(got)
	if !strings.Contains(s, "[[alpha]]") || !strings.Contains(s, "[[beta]]") {
		t.Errorf("both rows should be present:\n%s", s)
	}
	// Duplicate alpha — AddToIndex should rebuild, not double.
	if err := AddToIndex(dir, &Page{Title: "Alpha", Type: "concept", Body: "First.", Filename: "alpha.md"}, ""); err != nil {
		t.Fatalf("AddToIndex dup: %v", err)
	}
	got2, _ := os.ReadFile(filepath.Join(dir, IndexFilename))
	if strings.Count(string(got2), "[[alpha]]") != 1 {
		t.Errorf("duplicate add should produce exactly one alpha row:\n%s", got2)
	}
}

func TestAddToIndexOnEmptyIndexRebuilds(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, IndexFilename), []byte(IndexEmptyBody), 0o644)
	writePageTest(t, dir, "alpha.md", "Alpha", "concept", "First.")
	if err := AddToIndex(dir, &Page{Title: "Alpha", Type: "concept", Body: "First.", Filename: "alpha.md"}, ""); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, IndexFilename))
	if !strings.Contains(string(got), "| [[alpha]] | concept |") {
		t.Errorf("add-to-empty should seed the table:\n%s", got)
	}
}

func TestFirstSentenceHeuristic(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"sentence ends on period + space", "First sentence. Second sentence.", "First sentence."}, // period retained — sentence terminator is part of the summary
		{"no period", "Just one line no period", "Just one line no period"},
		{"h1 stripped", "# Title\n\nActual content here.", "Actual content here."},
		{"question mark", "Is this a question? And another.", "Is this a question?"},
		{"exclamation", "Wow! What happened?", "Wow!"},
		{"empty", "", ""},
		{"truncation", strings.Repeat("a", MaxTLDRRunes+50), strings.Repeat("a", MaxTLDRRunes-1) + "…"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := firstSentenceHeuristic(tc.in)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestEscapeTableCell(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain", "plain"},
		{"with | pipe", `with \| pipe`},
		{"with\nnewline", "with newline"},
	}
	for _, tc := range cases {
		got := escapeTableCell(tc.in)
		if got != tc.want {
			t.Errorf("escapeTableCell(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
