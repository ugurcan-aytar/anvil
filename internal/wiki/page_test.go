package wiki

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestReadWriteRoundTrip(t *testing.T) {
	dir := t.TempDir()
	in := &Page{
		Title:    "Circuit Breaker Pattern",
		Type:     "concept",
		Sources:  []string{"raw/interesting-paper.md"},
		Related:  []string{"retry-pattern.md", "bulkhead-pattern.md"},
		Created:  "2026-04-15",
		Updated:  "2026-04-15",
		Body:     "Stops cascading failures in distributed systems.\n\nSee [[retry-pattern]] for a complementary technique.\n",
		Filename: "circuit-breaker-pattern.md",
	}
	if err := WritePage(dir, in); err != nil {
		t.Fatalf("WritePage: %v", err)
	}
	out, err := ReadPage(dir, in.Filename)
	if err != nil {
		t.Fatalf("ReadPage: %v", err)
	}
	if out.Title != in.Title || out.Type != in.Type {
		t.Errorf("identity drift: %+v", out)
	}
	if !reflect.DeepEqual(out.Sources, in.Sources) {
		t.Errorf("Sources = %v, want %v", out.Sources, in.Sources)
	}
	if !reflect.DeepEqual(out.Related, in.Related) {
		t.Errorf("Related = %v, want %v", out.Related, in.Related)
	}
	// Body should be byte-identical after a round trip.
	if out.Body != in.Body {
		t.Errorf("Body mismatch\nwant: %q\n got: %q", in.Body, out.Body)
	}
	if out.Filename != in.Filename {
		t.Errorf("Filename = %q, want %q", out.Filename, in.Filename)
	}
}

func TestReadPageRejectsReserved(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "index.md"), []byte("# Index"), 0o644)
	if _, err := ReadPage(dir, "index.md"); err == nil {
		t.Fatal("ReadPage(index.md) should error — index.md is not a page")
	}
}

func TestWritePageRejectsBadFilenames(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name, fn string
	}{
		{"empty", ""},
		{"reserved index", "index.md"},
		{"reserved log", "log.md"},
		{"path separator", "subdir/page.md"},
		{"parent traversal", "../escape.md"},
		{"wrong extension", "page.txt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := WritePage(dir, &Page{Title: "x", Filename: tc.fn})
			if err == nil {
				t.Fatalf("WritePage(%q) should error", tc.fn)
			}
		})
	}
}

func TestListPagesSkipsReserved(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644)
	}
	pageContent := "---\ntitle: X\ntype: concept\n---\n\nbody\n"
	write("index.md", "# Index\n\nNo pages yet.\n")
	write("log.md", "# Log\n")
	write("alpha.md", pageContent)
	write("beta.md", pageContent)
	write("notes.txt", "not markdown")

	pages, err := ListPages(dir)
	if err != nil {
		t.Fatalf("ListPages: %v", err)
	}
	if len(pages) != 2 {
		t.Fatalf("len = %d, want 2 (alpha + beta, reserved + non-md skipped)", len(pages))
	}
	if pages[0].Filename != "alpha.md" || pages[1].Filename != "beta.md" {
		t.Errorf("order: %q, %q", pages[0].Filename, pages[1].Filename)
	}
}

func TestListPagesHandlesMissingDir(t *testing.T) {
	pages, err := ListPages(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Errorf("missing wikiDir should return nil,nil — got err %v", err)
	}
	if len(pages) != 0 {
		t.Errorf("missing wikiDir should return empty slice — got %d", len(pages))
	}
}

func TestDeletePage(t *testing.T) {
	dir := t.TempDir()
	p := &Page{Title: "x", Type: "concept", Body: "b\n", Filename: "x.md"}
	if err := WritePage(dir, p); err != nil {
		t.Fatal(err)
	}
	if err := DeletePage(dir, "x.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "x.md")); err == nil {
		t.Fatal("DeletePage didn't actually delete")
	}
}

func TestSlugFromTitle(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"Circuit Breaker Pattern", "circuit-breaker-pattern.md"},
		{"  Leading / trailing  ", "leading-trailing.md"},
		{"What is REST?", "what-is-rest.md"},
		{"C++ vs Go", "c-vs-go.md"},
		{"123 Numbers", "123-numbers.md"},
		{"Multiple   Spaces", "multiple-spaces.md"},
		{"Snake_Case_Name", "snake-case-name.md"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := SlugFromTitle(tc.in)
			if got != tc.want {
				t.Errorf("SlugFromTitle(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSlugFromTitleEmptyFallsBack(t *testing.T) {
	got := SlugFromTitle("!!!")
	if !strings.HasPrefix(got, "page-") || !strings.HasSuffix(got, ".md") {
		t.Errorf("empty-slug fallback = %q, want page-<ts>.md", got)
	}
}

func TestExtractWikilinks(t *testing.T) {
	cases := []struct {
		name, in string
		want     []string
	}{
		{"none", "plain markdown", nil},
		{"single", "See [[target]]", []string{"target"}},
		{"multiple", "[[a]] and [[b]] and [[c]]", []string{"a", "b", "c"}},
		{"dedup", "[[a]] [[a]] [[b]]", []string{"a", "b"}},
		{"piped alias", "[[target|display]]", []string{"target"}},
		{"whitespace inside", "[[  padded  ]]", []string{"padded"}},
		{"empty target", "[[]]", nil},
		{"nested bracket rejected", "[[foo[bar]]]", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractWikilinks(tc.in)
			// reflect.DeepEqual(nil, []string{}) is false — treat
			// the empty-slice case by length so either representation
			// passes.
			if len(tc.want) == 0 && len(got) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ExtractWikilinks(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestParsePageUnterminatedFrontmatter(t *testing.T) {
	dir := t.TempDir()
	// Missing closing --- → error.
	os.WriteFile(filepath.Join(dir, "bad.md"),
		[]byte("---\ntitle: Bad\ntype: concept\n\nbody without closing delim\n"), 0o644)
	if _, err := ReadPage(dir, "bad.md"); err == nil {
		t.Fatal("unterminated frontmatter should error")
	}
}

func TestParsePageNoFrontmatterIsBodyOnly(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "plain.md"), []byte("just body, no frontmatter\n"), 0o644)
	p, err := ReadPage(dir, "plain.md")
	if err != nil {
		t.Fatal(err)
	}
	if p.Title != "" || p.Type != "" {
		t.Errorf("frontmatter-less page should zero identity: %+v", p)
	}
	if !strings.HasPrefix(p.Body, "just body") {
		t.Errorf("body lost: %q", p.Body)
	}
}

// Regression guard for the duplicate-frontmatter bug found during
// the ZeroToMarketing real-world test. Raw LLM replies sometimes
// carry a leading "\n" (stray blank line before the frontmatter).
// Without leading-whitespace trimming, parsePage fell through to
// the body-only branch and the whole reply got re-wrapped with a
// second frontmatter by the writer stage.
func TestParsePageToleratesLeadingWhitespace(t *testing.T) {
	cases := map[string]string{
		"leading \\n":   "\n---\ntitle: Trim\ntype: concept\n---\n\nBody.\n",
		"leading CRLF":  "\r\n---\ntitle: Trim\ntype: concept\n---\n\nBody.\n",
		"leading space": "   \n---\ntitle: Trim\ntype: concept\n---\n\nBody.\n",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			p, err := ParsePage([]byte(raw))
			if err != nil {
				t.Fatalf("ParsePage: %v", err)
			}
			if p.Title != "Trim" {
				t.Errorf("Title = %q, want Trim (leading whitespace should not send us to body-only branch)", p.Title)
			}
			if p.Type != "concept" {
				t.Errorf("Type = %q, want concept", p.Type)
			}
			if !strings.Contains(p.Body, "Body.") {
				t.Errorf("body = %q", p.Body)
			}
		})
	}
}
