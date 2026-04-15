package ingest

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ugurcan-aytar/anvil/internal/wiki"
)

// SlugCatalog is the set of slugs currently backed by a wiki file,
// along with cheap helpers for mapping a candidate slug onto an
// established canonical form. Used to suppress slug drift ("nocode-devs"
// drifting away from "nocodedevs") both in the prompt layer (we feed
// Slugs() to the LLM) and in the reconcile layer (Canonicalise maps
// a fresh candidate onto an existing slug before the create/update
// decision).
type SlugCatalog struct {
	// slugs is the authoritative set — every value is a stem (no .md
	// suffix). Sorted when returned via Slugs() so prompt rendering
	// is deterministic.
	slugs []string
	// byNorm maps a normalized form (see normSlug) to the
	// canonical slug. Empty-string normalizations are filtered out
	// so a degenerate page name can't shadow a real one.
	byNorm map[string]string
}

// LoadSlugCatalog walks wikiDir (via wiki.ListPages, which already
// honours the reserved-filename skip list) and builds the catalog.
// A missing wiki dir produces an empty catalog — the caller should
// not treat that as an error because a fresh `anvil init` run has
// nothing to canonicalise.
func LoadSlugCatalog(wikiDir string) (*SlugCatalog, error) {
	pages, err := wiki.ListPages(wikiDir)
	if err != nil {
		return nil, fmt.Errorf("slug catalog: %w", err)
	}
	cat := &SlugCatalog{
		slugs:  make([]string, 0, len(pages)),
		byNorm: make(map[string]string, len(pages)),
	}
	for _, p := range pages {
		stem := strings.TrimSuffix(p.Filename, ".md")
		if stem == "" {
			continue
		}
		cat.slugs = append(cat.slugs, stem)
		norm := normSlug(stem)
		if norm != "" {
			cat.byNorm[norm] = stem
		}
	}
	sort.Strings(cat.slugs)
	return cat, nil
}

// Slugs returns the sorted stem list. Suitable for dropping into
// prompts via the slugCatalogBlock template.
func (c *SlugCatalog) Slugs() []string {
	if c == nil {
		return nil
	}
	out := make([]string, len(c.slugs))
	copy(out, c.slugs)
	return out
}

// Canonicalise maps a candidate slug onto an existing one when
// they're close enough to be considered the same concept. Returns
// (canonical, true) on a hit, ("", false) when the candidate is
// genuinely new.
//
// Matching rules, in order:
//
//  1. Exact: the candidate slug already exists.
//  2. Normalized exact: stripping "-" and "_" produces a match
//     (catches "nocodedevs" vs "nocode-devs" vs "nocode_devs").
//  3. Levenshtein ≤ 2: the candidate is within two single-character
//     edits of an existing slug (catches typos like "circut-breaker"
//     vs "circuit-breaker"). Only considered when the candidate is
//     at least 5 chars so short slugs don't all collapse together.
func (c *SlugCatalog) Canonicalise(candidate string) (string, bool) {
	if c == nil || len(c.slugs) == 0 {
		return "", false
	}
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return "", false
	}
	// Rule 1 — exact match.
	for _, s := range c.slugs {
		if s == candidate {
			return s, true
		}
	}
	// Rule 2 — normalized match.
	norm := normSlug(candidate)
	if norm != "" {
		if canon, ok := c.byNorm[norm]; ok && canon != candidate {
			return canon, true
		}
	}
	// Rule 3 — Levenshtein ≤ 2, guarded by length floor so short
	// slugs (e.g. "ai" vs "ui") don't false-match.
	if len(candidate) < 5 {
		return "", false
	}
	best := ""
	bestDist := 3 // strictly less-than below
	for _, s := range c.slugs {
		// Skip pairs where the length gap alone exceeds the
		// threshold — levenshtein is ≥ |len(a)-len(b)|.
		if abs(len(s)-len(candidate)) > 2 {
			continue
		}
		d := levenshtein(s, candidate)
		if d < bestDist {
			bestDist = d
			best = s
		}
	}
	if best != "" && best != candidate {
		return best, true
	}
	return "", false
}

// normSlug lowercases and strips separator characters so slug
// spellings with / without dashes collapse. Non-alnum runes beyond
// the standard separators are kept — they shouldn't appear in a
// valid slug anyway, so preserving them avoids hiding bad inputs.
func normSlug(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(s) {
		if r == '-' || r == '_' || r == ' ' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// levenshtein is the classic edit-distance DP, tuned for slugs
// (short strings, no allocations in the hot path beyond the two
// rolling rows).
func levenshtein(a, b string) int {
	if a == b {
		return 0
	}
	la, lb := len(a), len(b)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}
	// Two rolling rows — prev + cur.
	prev := make([]int, lb+1)
	cur := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		cur[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min3(
				cur[j-1]+1,       // insertion
				prev[j]+1,        // deletion
				prev[j-1]+cost,   // substitution
			)
		}
		prev, cur = cur, prev
	}
	return prev[lb]
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
