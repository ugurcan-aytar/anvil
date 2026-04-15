// Package lint walks the wiki and reports on its health. Two kinds
// of check run: structural (pure-Go graph analysis — orphans,
// missing pages, broken links, empty pages, stale index entries) and
// LLM-driven (contradictions across pages, stale claims, wiki-wide
// improvement suggestions).
//
// Run(...) composes both into a single LintReport. The `anvil lint`
// command renders that report; `anvil lint --structural-only` skips
// every LLM call.
package lint

// BrokenLink is one reference in a page whose target doesn't exist.
// Location distinguishes body wikilinks from `related:` frontmatter
// entries so the user sees exactly where to edit.
type BrokenLink struct {
	// SourcePage is the stem ("circuit-breaker") of the page that
	// contains the broken link.
	SourcePage string
	// Target is the stem the link points at ("rate-limiting").
	Target string
	// Location is "body" for a [[wikilink]] in prose or "frontmatter"
	// for an entry in the page's `related:` list.
	Location string
}

// Contradiction captures two pages making conflicting claims about
// the same topic. Populated by DetectContradictions via the LLM.
type Contradiction struct {
	PageA  string // stem of the first page
	PageB  string // stem of the second page
	ClaimA string // the specific claim on PageA
	ClaimB string // the conflicting claim on PageB
	Detail string // LLM's explanation of the conflict
}

// StaleClaim is a claim on an older page that a newer source
// supersedes. Populated by DetectStaleClaims.
type StaleClaim struct {
	Page        string // page carrying the stale claim
	Claim       string // the specific claim
	OlderSource string // source that originally grounded the claim
	NewerSource string // newer source that contradicts / updates it
	Detail      string // LLM's explanation
}

// LintReport is the full health dashboard returned by Run. Every
// field is optional — an empty wiki produces an empty report with
// HealthScore == 100.
type LintReport struct {
	Orphans        []string
	MissingPages   []string
	BrokenLinks    []BrokenLink
	EmptyPages     []string
	StaleIndex     []string // pages on disk but not listed in wiki/index.md
	Contradictions []Contradiction
	StaleClaims    []StaleClaim
	Suggestions    []string
	PageCount      int
	HealthScore    float64 // 0-100, filled by Run
}
