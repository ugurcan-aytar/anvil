package commands

import (
	"reflect"
	"testing"
)

// TestNormaliseSourcesListDedupesMixedForms covers the real-data
// bug: LLM mixes "[[slug]]" wikilinks and "wiki/slug.md" paths in
// the same sources list, producing duplicate entries after save.
func TestNormaliseSourcesListDedupesMixedForms(t *testing.T) {
	input := []string{
		"[[circuit-breaker]]",
		"wiki/circuit-breaker.md",   // same entity as [[circuit-breaker]]
		"raw/paper.md",
		"raw/paper.md",              // exact dup
		" wiki/CIRCUIT-BREAKER.md ", // case drift — still a dup
	}
	got := normaliseSourcesList(input)
	// [[circuit-breaker]] normalises to wiki/circuit-breaker.md which
	// case-insensitively dedupes against both the explicit path and
	// the CIRCUIT-BREAKER variant. raw/paper.md dedupes against itself.
	want := []string{
		"wiki/circuit-breaker.md",
		"raw/paper.md",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestNormaliseOneSourceConvertsWikilink(t *testing.T) {
	cases := map[string]string{
		"[[circuit-breaker]]":         "wiki/circuit-breaker.md",
		"[[circuit-breaker|display]]": "wiki/circuit-breaker.md",
		"wiki/x.md":                   "wiki/x.md",
		"raw/paper.md":                "raw/paper.md",
		"":                            "",
		"   ":                         "",
		"[[ x ]]":                     "wiki/x.md",
	}
	for input, want := range cases {
		if got := normaliseOneSource(input); got != want {
			t.Errorf("normaliseOneSource(%q) = %q, want %q", input, got, want)
		}
	}
}
