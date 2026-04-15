package commands

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"text/template"
	"time"

	"github.com/spf13/cobra"

	"github.com/ugurcan-aytar/anvil/internal/engine"
	"github.com/ugurcan-aytar/anvil/internal/llm"
	"github.com/ugurcan-aytar/anvil/internal/wiki"
)

// saveOptions carries the `anvil save` flags.
type saveOptions struct {
	// Name overrides the LLM's suggested filename. Passed verbatim
	// (minus a trailing ".md" if the user wrote one) into
	// wiki.SlugFromTitle semantics so trailing punctuation doesn't
	// leak through.
	Name string
}

var saveOpts saveOptions

var saveCmd = &cobra.Command{
	Use:   "save",
	Short: "Persist the last `anvil ask` answer as a synthesis wiki page",
	Long: `anvil save takes the last answer produced by ` + "`anvil ask`" + `,
runs it through the LLM once more to produce a self-contained wiki
page, and writes that page under wiki/ as a synthesis entry. The
index + log are refreshed so the saved page is immediately
searchable.

Requires a prior ` + "`anvil ask`" + ` in the same project — the answer is
stashed in .anvil/last-answer.json.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSave(cmd.Context(), saveOpts)
	},
}

func init() {
	saveCmd.Flags().StringVar(&saveOpts.Name, "name", "",
		"override the suggested filename (e.g. --name circuit-breaker-overview)")
}

// savePromptTemplate tells the LLM to convert a Q&A into a full wiki
// page. The FILENAME: header is a machine-readable one-liner the
// parser strips out of the body before writing to disk.
var savePromptTemplate = template.Must(template.New("save").Parse(`Convert this Q&A into a self-contained wiki synthesis page.

Rules:
- On the FIRST line of your response, emit:  FILENAME: <kebab-case>.md
  (no bold, no backticks — just the literal "FILENAME:" prefix and the slug).
- After the FILENAME line, write the full page: YAML frontmatter delimited by ` + "`---`" + ` lines (title, type: synthesis, sources list, created, updated — use today's date {{.Today}}), then the markdown body.
- Cross-reference related wiki pages using [[wikilink]] syntax, pulling from the sources list below.
- Keep the body concise (a few paragraphs or a short section list). It should make sense to someone reading it cold, without the original question.
- Do NOT wrap the output in a code fence.

Question: {{.Question}}

Answer:
{{.Answer}}

Sources:
{{- range .Sources}}
- {{.}}
{{- end}}
`))

// savePromptData is the shape savePromptTemplate expects.
type savePromptData struct {
	Question string
	Answer   string
	Sources  []string
	Today    string
}

// runSave is the entry point both Cobra and the ask-prompt flow use.
func runSave(ctx context.Context, opts saveOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	eng, err := engine.Open(projectDir)
	if err != nil {
		return err
	}
	defer eng.Close()

	record, err := readLastAnswer(eng.ProjectRoot())
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no previous answer found — run `anvil ask \"...\"` first")
		}
		return fmt.Errorf("read last answer: %w", err)
	}

	client, err := newLLMClient()
	if err != nil {
		if err == llm.ErrNoBackend {
			fmt.Fprintln(os.Stderr, llm.SetupGuidance())
		}
		return err
	}

	prompt, err := renderSavePrompt(record)
	if err != nil {
		return fmt.Errorf("render save prompt: %w", err)
	}
	raw, err := client.Complete(ctx, saveSystemPrompt, prompt)
	if err != nil {
		return fmt.Errorf("llm save call: %w", err)
	}

	filename, body, err := parseSaveResponse(raw)
	if err != nil {
		return fmt.Errorf("parse llm output: %w", err)
	}
	if opts.Name != "" {
		filename = normaliseSaveFilename(opts.Name)
	}

	page, err := wiki.ParsePage([]byte(body))
	if err != nil {
		return fmt.Errorf("parse saved page: %w", err)
	}
	page.Filename = filename
	fillSaveDefaults(page, record)

	if err := wiki.WritePage(eng.WikiDir(), page); err != nil {
		return fmt.Errorf("write page: %w", err)
	}
	if err := wiki.AddToIndex(eng.WikiDir(), page, ""); err != nil {
		return fmt.Errorf("add to index: %w", err)
	}
	if err := wiki.AppendLog(eng.WikiDir(), wiki.LogEntry{
		Timestamp: time.Now(),
		Type:      wiki.LogTypeSave,
		Title:     record.Question,
		Created:   []string{filename},
		Sources:   record.Sources,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "warning: append log failed: %v\n", err)
	}
	if _, err := eng.Recall().Index(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: reindex failed: %v\n", err)
	}

	fmt.Printf("Saved as wiki/%s\n", filename)
	fmt.Println("Index updated.")
	return nil
}

// saveSystemPrompt is the system message for the save call — shorter
// than the ingest system message because the user prompt already
// carries every format rule the LLM needs.
const saveSystemPrompt = "You are anvil, converting a question-and-answer pair into a durable wiki synthesis page. Follow the format rules exactly and ground every claim in the provided sources."

// renderSavePrompt fills savePromptTemplate against a lastAnswerRecord.
func renderSavePrompt(rec *lastAnswerRecord) (string, error) {
	data := savePromptData{
		Question: rec.Question,
		Answer:   rec.Answer,
		Sources:  rec.Sources,
		Today:    time.Now().Format("2006-01-02"),
	}
	var buf bytes.Buffer
	if err := savePromptTemplate.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// parseSaveResponse splits an LLM reply into (filename, body).
// Expected shape:
//
//	FILENAME: some-slug.md
//	---
//	title: ...
//	...
//	---
//
//	Body markdown.
//
// Tolerates optional whitespace / blank lines before and after the
// FILENAME marker.
func parseSaveResponse(raw string) (string, string, error) {
	text := strings.TrimSpace(stripCodeFence(raw))
	lines := strings.SplitN(text, "\n", 2)
	if len(lines) == 0 {
		return "", "", fmt.Errorf("empty response")
	}
	head := strings.TrimSpace(lines[0])
	if !strings.HasPrefix(strings.ToUpper(head), "FILENAME:") {
		return "", "", fmt.Errorf("response missing leading FILENAME: line; got %q", head)
	}
	filename := normaliseSaveFilename(strings.TrimSpace(head[len("FILENAME:"):]))
	if filename == "" {
		return "", "", fmt.Errorf("FILENAME: header was empty")
	}
	body := ""
	if len(lines) > 1 {
		body = strings.TrimLeft(lines[1], "\n")
	}
	return filename, body, nil
}

// normaliseSaveFilename lowercases, strips path separators, and
// guarantees a .md suffix. Handles both LLM-suggested slugs and
// user-provided --name overrides the same way.
func normaliseSaveFilename(name string) string {
	name = strings.TrimSpace(name)
	name = strings.Trim(name, "`\"'")
	// Strip any directory prefix — saved pages always land directly
	// under wiki/, never in a subfolder.
	if i := strings.LastIndexAny(name, "/\\"); i >= 0 {
		name = name[i+1:]
	}
	if name == "" {
		return ""
	}
	if !strings.HasSuffix(strings.ToLower(name), ".md") {
		name += ".md"
	}
	// Strip any characters the writer would reject.
	name = strings.ReplaceAll(name, "..", ".")
	return name
}

// stripCodeFence removes a surrounding ```markdown / ```md / ```
// fence if present. Reuses the same logic as the ingest writer
// but local so the commands package doesn't reach into ingest just
// for this helper.
func stripCodeFence(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if !strings.HasPrefix(trimmed, "```") {
		return raw
	}
	nl := strings.IndexByte(trimmed, '\n')
	if nl < 0 {
		return raw
	}
	inner := trimmed[nl+1:]
	if end := strings.LastIndex(inner, "```"); end >= 0 {
		inner = inner[:end]
	}
	return inner
}

// fillSaveDefaults patches any frontmatter field the LLM forgot so
// every saved page looks like every other synthesis page on disk.
func fillSaveDefaults(p *wiki.Page, rec *lastAnswerRecord) {
	dateStr := time.Now().Format("2006-01-02")
	if strings.TrimSpace(p.Type) == "" {
		p.Type = "synthesis"
	}
	if strings.TrimSpace(p.Title) == "" {
		p.Title = strings.TrimSpace(rec.Question)
	}
	// Merge the LLM-written sources with the ask record's citations.
	// Normalise everything to path form (wiki/slug.md / raw/...)
	// before the dedup pass — the LLM sometimes emits the raw
	// "[[slug]]" wikilink form inside the sources list, which would
	// otherwise produce a duplicate entry alongside the "wiki/slug.md"
	// path the writer wants.
	merged := append([]string{}, p.Sources...)
	merged = append(merged, rec.Sources...)
	p.Sources = normaliseSourcesList(merged)

	if strings.TrimSpace(p.Created) == "" {
		p.Created = dateStr
	}
	if strings.TrimSpace(p.Updated) == "" {
		p.Updated = dateStr
	}
}

// wikilinkInSourceRE matches "[[slug]]" entries so normaliseSourcesList
// can turn them into the canonical "wiki/slug.md" path form.
var wikilinkInSourceRE = regexp.MustCompile(`^\[\[([^\[\]|]+?)(?:\|[^\[\]]*)?\]\]$`)

// normaliseSourcesList turns a mixed list (wiki path, raw path, stray
// [[wikilink]] entries) into a clean, deduped list of path-shaped
// entries. Preserves first-occurrence order so the frontmatter
// stays readable.
func normaliseSourcesList(xs []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(xs))
	for _, s := range xs {
		norm := normaliseOneSource(s)
		if norm == "" {
			continue
		}
		key := strings.ToLower(norm)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, norm)
	}
	return out
}

// normaliseOneSource converts a single entry into path form.
// Returns "" for blank / useless entries so the caller can drop them.
func normaliseOneSource(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "`\"'")
	if s == "" {
		return ""
	}
	// "[[slug]]" → "wiki/slug.md"
	if m := wikilinkInSourceRE.FindStringSubmatch(s); len(m) >= 2 {
		stem := strings.TrimSpace(m[1])
		if stem == "" {
			return ""
		}
		stem = strings.TrimSuffix(stem, ".md")
		return "wiki/" + stem + ".md"
	}
	return s
}

