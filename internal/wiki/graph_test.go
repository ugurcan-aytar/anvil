package wiki

import (
	"reflect"
	"testing"
)

func seedGraph(t *testing.T) (dir string) {
	t.Helper()
	dir = t.TempDir()
	writePageTest(t, dir, "alpha.md", "Alpha", "concept",
		"Alpha links to [[beta]] and [[gamma]]. And [[does-not-exist]].")
	writePageTest(t, dir, "beta.md", "Beta", "entity",
		"Beta links back to [[alpha]].")
	writePageTest(t, dir, "gamma.md", "Gamma", "concept",
		"Gamma has no links.")
	writePageTest(t, dir, "delta.md", "Delta", "concept",
		"Delta also has no links but nothing points here either.")
	return dir
}

func TestBuildGraphBasic(t *testing.T) {
	dir := seedGraph(t)
	g, err := BuildGraph(dir)
	if err != nil {
		t.Fatal(err)
	}
	if g.PageCount() != 4 {
		t.Errorf("PageCount = %d, want 4", g.PageCount())
	}
}

func TestGraphOrphans(t *testing.T) {
	dir := seedGraph(t)
	g, _ := BuildGraph(dir)
	got := g.Orphans()
	// alpha is linked-to by beta (not orphan).
	// beta is linked-to by alpha (not orphan).
	// gamma is linked-to by alpha (not orphan).
	// delta has no inbound links → orphan.
	want := []string{"delta"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Orphans = %v, want %v", got, want)
	}
}

func TestGraphBacklinks(t *testing.T) {
	dir := seedGraph(t)
	g, _ := BuildGraph(dir)

	cases := []struct {
		name, page string
		want       []string
	}{
		{"beta from alpha", "beta", []string{"alpha"}},
		{"alpha from beta", "alpha", []string{"beta"}},
		{"gamma from alpha", "gamma", []string{"alpha"}},
		{"delta has none", "delta", []string{}},
		{"filename works too", "beta.md", []string{"alpha"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := g.Backlinks(tc.page)
			if len(got) != len(tc.want) {
				t.Fatalf("Backlinks(%q) = %v, want %v", tc.page, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("Backlinks(%q)[%d] = %q, want %q", tc.page, i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestGraphMissingPages(t *testing.T) {
	dir := seedGraph(t)
	g, _ := BuildGraph(dir)
	got := g.MissingPages()
	want := []string{"does-not-exist"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("MissingPages = %v, want %v", got, want)
	}
}

func TestGraphSelfLinkDoesntBlockOrphan(t *testing.T) {
	dir := t.TempDir()
	// Only one page, self-linking. Should count as orphan.
	writePageTest(t, dir, "solo.md", "Solo", "concept", "Self-reference [[solo]].")
	g, _ := BuildGraph(dir)
	if orphans := g.Orphans(); len(orphans) != 1 || orphans[0] != "solo" {
		t.Errorf("self-link shouldn't rescue orphan status: got %v", orphans)
	}
}

func TestGraphEmptyDir(t *testing.T) {
	g, err := BuildGraph(t.TempDir())
	if err != nil {
		t.Fatalf("empty dir should not error: %v", err)
	}
	if g.PageCount() != 0 {
		t.Errorf("PageCount = %d, want 0", g.PageCount())
	}
	if len(g.Orphans()) != 0 {
		t.Error("empty dir should have no orphans")
	}
}
