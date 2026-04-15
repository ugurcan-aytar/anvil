package lint

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strings"
	"text/template"
	"time"

	"github.com/ugurcan-aytar/anvil/internal/llm"
	"github.com/ugurcan-aytar/anvil/internal/wiki"
)

// StaleAgeGap is the minimum gap (days) between two pages' `updated`
// fields before they get sent to the LLM for stale-claim analysis.
// Shorter gaps are dominated by normal editing activity; 30 days
// gives a signal that the newer page likely carries information the
// older one couldn't have.
const StaleAgeGap = 30 * 24 * time.Hour

// MaxStalePairs caps per-run LLM calls, same way contradictions do.
const MaxStalePairs = 10

// staleNegative is the literal reply the LLM returns when the newer
// page doesn't supersede anything on the older one.
const staleNegative = "NO_STALE_CLAIMS"

// DetectStaleClaims finds claims on older pages that a newer page
// (same topic) supersedes. Skips pairs that don't share `related`
// or `sources` entries — random-pair comparison would dominate LLM
// costs without finding real staleness.
func DetectStaleClaims(ctx context.Context, client llm.Client, wikiDir string) ([]StaleClaim, error) {
	if client == nil {
		return nil, fmt.Errorf("stale: llm client is nil")
	}
	pages, err := wiki.ListPages(wikiDir)
	if err != nil {
		return nil, fmt.Errorf("list pages: %w", err)
	}

	pairs := overlappingPairs(pages)
	staleCandidates := make([]stalePair, 0, len(pairs))
	for _, p := range pairs {
		oldest, newest, gap := ageGap(p.A, p.B)
		if oldest == nil || gap < StaleAgeGap {
			continue
		}
		staleCandidates = append(staleCandidates, stalePair{
			Older: oldest,
			Newer: newest,
		})
	}
	if len(staleCandidates) > MaxStalePairs {
		staleCandidates = staleCandidates[:MaxStalePairs]
	}

	var out []StaleClaim
	for _, sp := range staleCandidates {
		found, err := askStale(ctx, client, sp.Older, sp.Newer)
		if err != nil {
			continue
		}
		out = append(out, found...)
	}
	return out, nil
}

// stalePair is a directional pair: Older is the source of claims
// that may be stale; Newer is the reference whose facts trump.
type stalePair struct {
	Older *wiki.Page
	Newer *wiki.Page
}

// ageGap returns (older, newer, gap). When either page's `updated`
// field is unparseable or missing, returns (nil, nil, 0) so the
// caller can skip the pair.
func ageGap(a, b *wiki.Page) (*wiki.Page, *wiki.Page, time.Duration) {
	ta, okA := parseDate(a.Updated)
	tb, okB := parseDate(b.Updated)
	if !okA || !okB {
		return nil, nil, 0
	}
	if ta.Before(tb) {
		return a, b, tb.Sub(ta)
	}
	if tb.Before(ta) {
		return b, a, ta.Sub(tb)
	}
	return nil, nil, 0
}

// parseDate accepts the YYYY-MM-DD form frontmatter uses. Returns
// (zero, false) on failure so the caller can skip quietly.
func parseDate(s string) (time.Time, bool) {
	t, err := time.Parse("2006-01-02", strings.TrimSpace(s))
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

var staleTemplate = template.Must(template.New("stale").Parse(`This older wiki page was last updated on {{.OlderDate}}. A newer page was updated on {{.NewerDate}}.

Does the newer page contain information that supersedes, corrects, or updates claims in the older page?

If it does NOT, respond with exactly this single word: NO_STALE_CLAIMS

If it does, respond with one block per stale claim in this exact format (and nothing else):

StaleClaim:
  Claim: <short description of the claim on the older page>
  Detail: <one-sentence explanation of how the newer page supersedes it>

Older page: [[{{.OlderStem}}]] (sources: {{.OlderSources}})
{{.OlderBody}}

Newer page: [[{{.NewerStem}}]] (sources: {{.NewerSources}})
{{.NewerBody}}
`))

type staleData struct {
	OlderStem    string
	OlderDate    string
	OlderSources string
	OlderBody    string
	NewerStem    string
	NewerDate    string
	NewerSources string
	NewerBody    string
}

const staleSystem = "You are anvil, a wiki health auditor. Analyse whether a newer page supersedes claims on an older one. Follow the output format exactly."

func askStale(ctx context.Context, client llm.Client, older, newer *wiki.Page) ([]StaleClaim, error) {
	data := staleData{
		OlderStem:    stemOf(older.Filename),
		OlderDate:    older.Updated,
		OlderSources: strings.Join(older.Sources, ", "),
		OlderBody:    strings.TrimSpace(older.Body),
		NewerStem:    stemOf(newer.Filename),
		NewerDate:    newer.Updated,
		NewerSources: strings.Join(newer.Sources, ", "),
		NewerBody:    strings.TrimSpace(newer.Body),
	}
	var buf bytes.Buffer
	if err := staleTemplate.Execute(&buf, data); err != nil {
		return nil, err
	}
	reply, err := client.Complete(ctx, staleSystem, buf.String())
	if err != nil {
		return nil, err
	}
	reply = strings.TrimSpace(reply)
	if reply == "" {
		return nil, nil
	}
	if strings.HasPrefix(strings.ToUpper(reply), staleNegative) ||
		strings.Contains(strings.ToUpper(reply), "NO STALE CLAIMS") {
		return nil, nil
	}
	return parseStaleClaims(reply, older, newer), nil
}

// staleSplitRE splits a reply into StaleClaim: blocks. Same
// technique as contradictions — RE2 lacks a lookahead, so we split
// on the header and iterate the resulting segments.
var staleSplitRE = regexp.MustCompile(`(?mi)^[ \t]*StaleClaim:\s*`)

var (
	claimRE       = regexp.MustCompile(`(?i)Claim:\s*(.+)`)
	staleDetailRE = regexp.MustCompile(`(?i)Detail:\s*(.+)`)
)

func parseStaleClaims(reply string, older, newer *wiki.Page) []StaleClaim {
	var out []StaleClaim
	segments := staleSplitRE.Split(reply, -1)
	olderSrc := firstNonEmpty(older.Sources...)
	newerSrc := firstNonEmpty(newer.Sources...)
	for _, block := range segments[1:] {
		claim := firstSubmatch(claimRE, block)
		detail := firstSubmatch(staleDetailRE, block)
		if claim == "" {
			continue
		}
		out = append(out, StaleClaim{
			Page:        stemOf(older.Filename),
			Claim:       claim,
			OlderSource: olderSrc,
			NewerSource: newerSrc,
			Detail:      detail,
		})
	}
	return out
}

// firstNonEmpty returns the first non-blank string in xs, or "" if
// all are blank. Keeps callers from doing the TrimSpace + length
// check inline.
func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if strings.TrimSpace(x) != "" {
			return x
		}
	}
	return ""
}
