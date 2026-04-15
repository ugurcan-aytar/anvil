package query

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strings"
	"text/template"

	"github.com/ugurcan-aytar/anvil/internal/llm"
)

// Answer is what Synthesize returns: the LLM's reply plus the list
// of sources that were actually cited in the reply. Unverified is
// non-empty when the LLM referenced a source that wasn't in the
// retrieval context — callers should surface that as a warning.
type Answer struct {
	// Text is the LLM's answer verbatim.
	Text string
	// Sources lists the unique citations the reply actually used,
	// in the order they first appeared. Wiki citations look like
	// "circuit-breaker.md"; raw citations keep their relative path
	// (e.g. "raw/system-design.md").
	Sources []string
	// Unverified lists citations the LLM emitted that weren't in the
	// retrieval context. Non-empty Unverified means the caller
	// should render a "⚠️ unverified citation" warning next to the
	// answer; it does NOT imply the answer is wrong, just that the
	// grounding is weaker for those claims.
	Unverified []string
}

// synthesisSystem is the fixed system prompt every Synthesize call
// uses. Pinned to a constant (not a template) because it carries no
// per-query data — the context lives in the user prompt so prompt
// caching (when it lands) gets max mileage out of the system slot.
const synthesisSystem = `You are anvil, a knowledge-base assistant. Answer the user's question using ONLY the context provided below.

Rules:
- Cite every wiki source with [[filename]] (without the .md suffix) and every raw source with its relative path in backticks, e.g. ` + "`raw/system-design.md`" + `.
- Prefer information from wiki pages (compiled knowledge) over raw chunks when they overlap.
- If wiki and raw sources disagree, say so explicitly and cite both.
- If the context does not contain enough information, say so. Do NOT fabricate details or invent citations.
- Be concise — one or two short paragraphs is usually right. Bullet points are fine when the answer has structure.`

// synthesisUserTemplate is the per-query user prompt. Contains the
// wiki-then-raw context in the order the synthesizer wants the LLM
// to prefer it. Uses text/template so callers don't hand-concatenate
// and miss delimiters between chunks.
var synthesisUserTemplate = template.Must(template.New("synth").Parse(`Question: {{.Question}}

=== WIKI CONTEXT (compiled knowledge) ===
{{- if .WikiPages}}
{{range .WikiPages}}
--- [[{{.Stem}}]] — {{.Title}} ---
{{.Body}}
{{end}}
{{- else}}
(no wiki pages matched)
{{- end}}

=== RAW CONTEXT (primary sources) ===
{{- if .RawHits}}
{{range .RawHits}}
--- ` + "`raw/{{.Path}}`" + ` (score {{printf "%.2f" .Score}}) ---
{{.Snippet}}
{{end}}
{{- else}}
(no raw sources matched)
{{- end}}

Answer the question above.`))

// synthesisUserData is the shape synthesisUserTemplate expects.
// Trimmed-down versions of Result fields so the template doesn't need
// to reach into wiki.Page / Hit internals.
type synthesisUserData struct {
	Question  string
	WikiPages []synthesisWikiEntry
	RawHits   []Hit
}

type synthesisWikiEntry struct {
	Stem  string // filename without .md
	Title string
	Body  string
}

// Synthesize runs the LLM call that turns a retrieval Result into
// an answer. Returns an error from the LLM verbatim; citation
// verification never fails the call (it flags via Answer.Unverified
// instead).
func Synthesize(ctx context.Context, client llm.Client, question string, result *Result) (*Answer, error) {
	if client == nil {
		return nil, fmt.Errorf("synthesize: llm client is nil")
	}
	if result == nil {
		return nil, fmt.Errorf("synthesize: result is nil")
	}
	if strings.TrimSpace(question) == "" {
		return nil, fmt.Errorf("synthesize: question is empty")
	}

	user, err := renderSynthesisUser(question, result)
	if err != nil {
		return nil, fmt.Errorf("render synthesis prompt: %w", err)
	}

	text, err := client.Complete(ctx, synthesisSystem, user)
	if err != nil {
		return nil, fmt.Errorf("llm synthesize call: %w", err)
	}
	text = strings.TrimSpace(text)

	cited, unverified := extractAndVerifyCitations(text, result)
	return &Answer{
		Text:       text,
		Sources:    cited,
		Unverified: unverified,
	}, nil
}

// renderSynthesisUser executes the user-prompt template against the
// retrieval Result. Kept package-private so callers go through
// Synthesize.
func renderSynthesisUser(question string, result *Result) (string, error) {
	data := synthesisUserData{
		Question: question,
		RawHits:  result.RawHits,
	}
	for _, p := range result.WikiPages {
		data.WikiPages = append(data.WikiPages, synthesisWikiEntry{
			Stem:  strings.TrimSuffix(p.Filename, ".md"),
			Title: p.Title,
			Body:  strings.TrimSpace(p.Body),
		})
	}
	var buf bytes.Buffer
	if err := synthesisUserTemplate.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// wikiCitationRE matches [[stem]] or [[stem|display]] — the raw
// wikilink form anvil uses. Reused from wiki.ExtractWikilinks by
// copying the pattern so the query package doesn't have to import
// wiki just for a regex.
var wikiCitationRE = regexp.MustCompile(`\[\[([^\[\]|]+)(?:\|[^\[\]]*)?\]\]`)

// rawCitationRE matches backticked paths that look like raw sources.
// Scoped to paths starting with "raw/" so generic backticked code
// like `func` or `foo()` isn't mis-classified as a citation.
var rawCitationRE = regexp.MustCompile("`(raw/[^`\\s]+?\\.[a-zA-Z0-9]+)`")

// extractAndVerifyCitations parses the LLM's answer for wiki + raw
// citations, then partitions them into (verified, unverified) based
// on what was actually in the retrieval context. Wiki citations
// reference a wiki stem ("circuit-breaker"); we check it against
// every WikiPage's filename stem. Raw citations quote a relative
// path; we check it against every RawHit's Path (which recall
// already prefixes with "raw/" via CollectionName).
func extractAndVerifyCitations(text string, result *Result) (verified, unverified []string) {
	wikiStems := map[string]struct{}{}
	for _, p := range result.WikiPages {
		wikiStems[strings.TrimSuffix(p.Filename, ".md")] = struct{}{}
	}
	rawPaths := map[string]struct{}{}
	for _, h := range result.RawHits {
		// recall's RawHit.Path is collection-relative (e.g. "paper.md");
		// the LLM writes the user-visible "raw/paper.md" form.
		rawPaths["raw/"+h.Path] = struct{}{}
	}

	seen := map[string]struct{}{}
	add := func(dst *[]string, cite string) {
		if _, dup := seen[cite]; dup {
			return
		}
		seen[cite] = struct{}{}
		*dst = append(*dst, cite)
	}

	for _, m := range wikiCitationRE.FindAllStringSubmatch(text, -1) {
		stem := strings.TrimSpace(m[1])
		if stem == "" {
			continue
		}
		cite := "wiki/" + stem + ".md"
		if _, ok := wikiStems[stem]; ok {
			add(&verified, cite)
		} else {
			add(&unverified, cite)
		}
	}
	for _, m := range rawCitationRE.FindAllStringSubmatch(text, -1) {
		path := strings.TrimSpace(m[1])
		if path == "" {
			continue
		}
		if _, ok := rawPaths[path]; ok {
			add(&verified, path)
		} else {
			add(&unverified, path)
		}
	}
	return verified, unverified
}
