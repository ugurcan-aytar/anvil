package lint

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/template"

	"github.com/ugurcan-aytar/anvil/internal/llm"
	"github.com/ugurcan-aytar/anvil/internal/wiki"
)

// MaxSuggestions caps what the LLM is asked to return — keeps the
// reply small and predictable. `anvil lint` renders every element
// verbatim, so five well-picked pointers beat fifteen noisy ones.
const MaxSuggestions = 5

// Suggest asks the LLM for wiki-wide improvement ideas based on the
// current index.md content + graph statistics. Returns up to
// MaxSuggestions pointers as plain strings (one per suggestion).
//
// graph may be nil — the function is resilient. When graph is nil,
// only page count + index content reach the prompt.
func Suggest(ctx context.Context, client llm.Client, wikiDir string, graph *wiki.Graph) ([]string, error) {
	if client == nil {
		return nil, fmt.Errorf("suggest: llm client is nil")
	}
	indexBody := readIndexBody(wikiDir)
	pages, err := wiki.ListPages(wikiDir)
	if err != nil {
		return nil, fmt.Errorf("list pages: %w", err)
	}

	data := suggestData{
		IndexContent: indexBody,
		PageCount:    len(pages),
	}
	if graph != nil {
		data.OrphanCount = len(graph.Orphans())
		data.MissingCount = len(graph.MissingPages())
		data.TopHubs = strings.Join(topHubs(pages, graph, 3), ", ")
		data.Isolated = strings.Join(isolatedPages(pages, graph, 3), ", ")
	}

	var buf bytes.Buffer
	if err := suggestTemplate.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("render suggest prompt: %w", err)
	}
	reply, err := client.Complete(ctx, suggestSystem, buf.String())
	if err != nil {
		return nil, err
	}
	return parseNumberedList(reply, MaxSuggestions), nil
}

const suggestSystem = "You are anvil, a wiki health advisor. Propose concrete improvements given the current wiki state. Be specific and actionable — reference real pages by their [[stem]] form."

var suggestTemplate = template.Must(template.New("suggest").Parse(`Given this wiki's state, propose up to {{.MaxSuggestions}} concrete improvements.

Wiki statistics:
- {{.PageCount}} pages total
- {{.OrphanCount}} orphan page(s) — no inbound links
- {{.MissingCount}} missing page(s) — referenced but don't exist
{{- if .TopHubs}}
- Most connected: {{.TopHubs}}
{{- end}}
{{- if .Isolated}}
- Least connected: {{.Isolated}}
{{- end}}

Wiki index:
{{.IndexContent}}

Suggest specific, actionable improvements. Focus on:
1. Missing pages that should exist
2. Connections between existing pages that should be drawn
3. Topics that need deeper research
4. Questions worth investigating

Respond as a numbered list, one suggestion per line. No preamble, no trailing commentary.`))

type suggestData struct {
	IndexContent   string
	PageCount      int
	OrphanCount    int
	MissingCount   int
	TopHubs        string
	Isolated       string
	MaxSuggestions int // unused directly but handy if the template grows
}

// numberedLineRE matches list lines the LLM returns. Tolerant of
// different prefix conventions: "1.", "1)", "- 1.", "1:".
var numberedLineRE = regexp.MustCompile(`(?m)^\s*(?:-\s*)?\d+[.)\-:]\s+(.+)$`)

// parseNumberedList pulls suggestion strings out of the LLM reply.
// Caps at max — surplus items are silently dropped.
func parseNumberedList(reply string, max int) []string {
	out := make([]string, 0, max)
	for _, m := range numberedLineRE.FindAllStringSubmatch(reply, -1) {
		if len(m) < 2 {
			continue
		}
		line := strings.TrimSpace(m[1])
		if line == "" {
			continue
		}
		out = append(out, line)
		if len(out) >= max {
			break
		}
	}
	return out
}

// readIndexBody returns wiki/index.md verbatim, or the empty string
// when no index exists. Used so the suggest prompt always has a
// body to quote even on freshly-initialised projects.
func readIndexBody(wikiDir string) string {
	raw, err := os.ReadFile(filepath.Join(wikiDir, wiki.IndexFilename))
	if err != nil {
		return ""
	}
	return string(raw)
}

// topHubs returns up to k page stems with the most inbound links.
// Ties are broken alphabetically so results are deterministic.
func topHubs(pages []*wiki.Page, graph *wiki.Graph, k int) []string {
	scored := make([]stemCount, 0, len(pages))
	for _, p := range pages {
		stem := stemOf(p.Filename)
		scored = append(scored, stemCount{
			stem:  stem,
			count: len(graph.Backlinks(stem)),
		})
	}
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].count != scored[j].count {
			return scored[i].count > scored[j].count
		}
		return scored[i].stem < scored[j].stem
	})
	out := make([]string, 0, k)
	for _, sc := range scored {
		if sc.count == 0 {
			break // drop zero-backlink pages from the "hub" list
		}
		out = append(out, fmt.Sprintf("[[%s]] (%d)", sc.stem, sc.count))
		if len(out) >= k {
			break
		}
	}
	return out
}

// isolatedPages returns up to k pages with zero inbound AND zero
// outbound links — the most disconnected members of the wiki.
// Ties broken alphabetically.
func isolatedPages(pages []*wiki.Page, graph *wiki.Graph, k int) []string {
	var out []string
	for _, p := range pages {
		stem := stemOf(p.Filename)
		backs := graph.Backlinks(stem)
		outgoing := wiki.ExtractWikilinks(p.Body)
		if len(backs) == 0 && len(outgoing) == 0 {
			out = append(out, "[[" + stem + "]]")
		}
	}
	sort.Strings(out)
	if len(out) > k {
		out = out[:k]
	}
	return out
}

// stemCount is used for the sort-by-backlinks-desc helper.
type stemCount struct {
	stem  string
	count int
}
