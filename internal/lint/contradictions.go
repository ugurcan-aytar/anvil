package lint

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strings"
	"text/template"

	"github.com/ugurcan-aytar/anvil/internal/llm"
	"github.com/ugurcan-aytar/anvil/internal/wiki"
)

// MaxContradictionPairs caps how many (pageA, pageB) pairs get sent
// to the LLM for contradiction analysis during a single lint run.
// Ten pairs ≈ ten LLM calls per `anvil lint` — expensive but
// bounded. Users who want exhaustive analysis can run lint multiple
// times after curating the wiki.
const MaxContradictionPairs = 10

// contradictionNegative is the literal string the LLM returns when
// two pages don't conflict. Matched case-insensitively so "no
// contradictions found", "NO_CONTRADICTIONS", etc. all count.
const contradictionNegative = "NO_CONTRADICTIONS"

// DetectContradictions pairs up overlap-sharing pages (same related
// stem OR same source path in frontmatter) and asks the LLM to
// flag contradictions between each pair. Pairs without overlap are
// skipped — random-pair N² contradiction hunting would cost more
// LLM calls than the feature is worth.
func DetectContradictions(ctx context.Context, client llm.Client, wikiDir string) ([]Contradiction, error) {
	if client == nil {
		return nil, fmt.Errorf("contradictions: llm client is nil")
	}
	pages, err := wiki.ListPages(wikiDir)
	if err != nil {
		return nil, fmt.Errorf("list pages: %w", err)
	}
	pairs := overlappingPairs(pages)
	if len(pairs) > MaxContradictionPairs {
		pairs = pairs[:MaxContradictionPairs]
	}

	var found []Contradiction
	for _, pair := range pairs {
		contradictions, err := askContradiction(ctx, client, pair.A, pair.B)
		if err != nil {
			// Per-pair failures log to stderr-via-nothing here;
			// the orchestrator is free to bubble errors up or
			// ignore them. A hard-fail would abort the whole
			// lint run for one bad prompt.
			continue
		}
		found = append(found, contradictions...)
	}
	return found, nil
}

// pairPages pairs two wiki pages for LLM comparison.
type pairPages struct {
	A *wiki.Page
	B *wiki.Page
}

// overlappingPairs returns page pairs that share at least one
// `related` entry or one `source` path. Same-page is excluded.
// Result order is stable (sorted by filename).
func overlappingPairs(pages []*wiki.Page) []pairPages {
	// Fold into a deterministic order so the MaxContradictionPairs
	// cap doesn't randomly pick different pairs on repeated runs.
	byName := make([]*wiki.Page, len(pages))
	copy(byName, pages)
	for i := 0; i < len(byName); i++ {
		for j := i + 1; j < len(byName); j++ {
			if byName[j].Filename < byName[i].Filename {
				byName[i], byName[j] = byName[j], byName[i]
			}
		}
	}

	var out []pairPages
	for i := 0; i < len(byName); i++ {
		for j := i + 1; j < len(byName); j++ {
			if sharesOverlap(byName[i], byName[j]) {
				out = append(out, pairPages{A: byName[i], B: byName[j]})
			}
		}
	}
	return out
}

// sharesOverlap returns true when a + b have at least one entry in
// common between their `related` lists OR their `sources` lists.
// Case-insensitive, trimmed.
func sharesOverlap(a, b *wiki.Page) bool {
	rel := make(map[string]struct{}, len(a.Related)+len(a.Sources))
	for _, r := range a.Related {
		rel[norm(r)] = struct{}{}
	}
	for _, s := range a.Sources {
		rel[norm(s)] = struct{}{}
	}
	for _, r := range b.Related {
		if _, ok := rel[norm(r)]; ok {
			return true
		}
	}
	for _, s := range b.Sources {
		if _, ok := rel[norm(s)]; ok {
			return true
		}
	}
	return false
}

func norm(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// contradictionTemplate is the prompt sent to the LLM for each page
// pair. The strict "Contradiction:" / "Claim A:" etc. format makes
// parsing trivial and lets the user mentally map each hit.
var contradictionTemplate = template.Must(template.New("contradiction").Parse(`Compare these two wiki pages. Do they contain any contradictions — claims that directly conflict with each other?

If they DO NOT conflict, respond with exactly this single word: NO_CONTRADICTIONS

If they DO, respond with one block per contradiction in this exact format (and nothing else):

Contradiction:
  Claim A: <short verbatim or paraphrased claim from page A>
  Claim B: <short verbatim or paraphrased claim from page B>
  Detail: <one-sentence explanation of why they conflict>

Page A: [[{{.StemA}}]]
{{.BodyA}}

Page B: [[{{.StemB}}]]
{{.BodyB}}
`))

// contradictionData is the shape contradictionTemplate expects.
type contradictionData struct {
	StemA string
	StemB string
	BodyA string
	BodyB string
}

// askContradiction runs one LLM call for a single pair. Returns zero
// contradictions when the reply starts with contradictionNegative.
func askContradiction(ctx context.Context, client llm.Client, a, b *wiki.Page) ([]Contradiction, error) {
	data := contradictionData{
		StemA: stemOf(a.Filename),
		StemB: stemOf(b.Filename),
		BodyA: strings.TrimSpace(a.Body),
		BodyB: strings.TrimSpace(b.Body),
	}
	var buf bytes.Buffer
	if err := contradictionTemplate.Execute(&buf, data); err != nil {
		return nil, err
	}
	reply, err := client.Complete(ctx, contradictionSystem, buf.String())
	if err != nil {
		return nil, err
	}
	reply = strings.TrimSpace(reply)
	if reply == "" {
		return nil, nil
	}
	upper := strings.ToUpper(reply)
	if strings.HasPrefix(upper, contradictionNegative) || strings.Contains(upper, "NO CONTRADICTIONS") {
		return nil, nil
	}

	return parseContradictions(reply, data.StemA, data.StemB), nil
}

// contradictionSystem is the fixed system prompt for the
// contradiction call. Separate from the synthesis/ingest system
// prompts because contradiction analysis has a stricter output
// format than prose generation.
const contradictionSystem = "You are anvil, a wiki health auditor. Analyse pairs of wiki pages for contradictions. Follow the user's output format exactly — the parser is mechanical."

// claimARE / claimBRE / detailRE extract individual fields from one
// block. Tolerant of bullet/space variation (the LLM sometimes
// inserts leading "-" or uses different indent).
var (
	claimARE = regexp.MustCompile(`(?i)Claim\s*A:\s*(.+)`)
	claimBRE = regexp.MustCompile(`(?i)Claim\s*B:\s*(.+)`)
	detailRE = regexp.MustCompile(`(?i)Detail:\s*(.+)`)
)

// contradictionSplitRE matches the "Contradiction:" block header
// (case-insensitive, optional leading whitespace). Used with Split
// instead of FindAll because Go's RE2 can't express a zero-width
// lookahead to separate sibling blocks.
var contradictionSplitRE = regexp.MustCompile(`(?mi)^[ \t]*Contradiction:\s*`)

// parseContradictions turns the LLM reply into structured output.
// Unparseable blocks are dropped rather than bubbled as errors —
// the call was already expensive and a partial result beats failing
// the whole lint run.
func parseContradictions(reply, stemA, stemB string) []Contradiction {
	var out []Contradiction
	// Splits on every "Contradiction:" header. The first element is
	// whatever prose the LLM wrote before the first block (usually
	// empty); subsequent elements are the field bodies.
	segments := contradictionSplitRE.Split(reply, -1)
	for _, block := range segments[1:] {
		claimA := firstSubmatch(claimARE, block)
		claimB := firstSubmatch(claimBRE, block)
		detail := firstSubmatch(detailRE, block)
		if claimA == "" && claimB == "" {
			continue
		}
		out = append(out, Contradiction{
			PageA:  stemA,
			PageB:  stemB,
			ClaimA: claimA,
			ClaimB: claimB,
			Detail: detail,
		})
	}
	return out
}

// firstSubmatch pulls the first capturing group of re out of s,
// trimmed to one line (the LLM sometimes writes multi-line claim
// bodies; we keep the first line for display tidiness).
func firstSubmatch(re *regexp.Regexp, s string) string {
	m := re.FindStringSubmatch(s)
	if len(m) < 2 {
		return ""
	}
	line := strings.TrimSpace(m[1])
	if nl := strings.IndexByte(line, '\n'); nl >= 0 {
		line = strings.TrimSpace(line[:nl])
	}
	return line
}
