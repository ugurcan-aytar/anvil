// Package ingest turns raw source files into wiki pages.
//
// The ingest pipeline runs in four stages:
//
//  1. Extract  — LLM reads the source, emits entities/concepts/claims
//     as YAML (prompts.go, extract.go).
//  2. Reconcile — the extraction is diffed against the current wiki.
//     Each entity/concept becomes either a PageDraft (new) or a
//     PageUpdate (merge into existing; reconcile.go).
//  3. Write    — LLM authors each create/update page from the draft
//     or the existing page + new info (writer.go).
//  4. Record   — source hash cached in .anvil/index.db so the next
//     ingest on the same unchanged file is a no-op (cache.go).
//
// Nothing in this package talks to recall directly — the command
// layer orchestrates the four steps and then calls
// engine.Recall().Index() at the end so the new markdown is BM25able.
package ingest

import (
	"bytes"
	"text/template"
)

// ExtractContext feeds the extract prompt. Title is the source
// filename's stem (or first H1 if the source has one), Path is the
// path relative to the project root, Content is the source body with
// any frontmatter stripped.
type ExtractContext struct {
	Title   string
	Path    string
	Content string
}

// WriteContext feeds the "author this page" prompt used when a new
// entity/concept is being created. Claims and Connections are
// pre-rendered as bullet lists by the writer — passing structured
// slices here would invite template surgery later; strings keep the
// rendering boundary in Go where it belongs.
type WriteContext struct {
	Slug        string
	Name        string
	Type        string
	Description string
	Claims      string
	Connections string
	SourcePath  string
}

// UpdateContext feeds the "merge new info into existing page"
// prompt. ExistingPage is the full on-disk markdown (frontmatter
// included) so the LLM can preserve fields it doesn't need to edit.
type UpdateContext struct {
	ExistingPage string
	NewInfo      string
	SourcePath   string
}

// extractTemplate is the "extract structure from source" prompt. The
// YAML-only response format is strict on purpose — the parser in
// extract.go does one retry on malformed output, and a free-form
// fallback would invite drift.
var extractTemplate = template.Must(template.New("extract").Parse(`You are a wiki compiler. Read the following source document and extract:

1. ENTITIES: People, companies, tools, projects mentioned. For each: name, one-line description.
2. CONCEPTS: Ideas, frameworks, patterns, techniques discussed. For each: name, one-line description.
3. CLAIMS: Specific factual claims or decisions. For each: the claim, which entity/concept it relates to.
4. CONNECTIONS: How entities and concepts relate to each other.

Respond in this exact YAML format, wrapped in a single fenced code block:

` + "```yaml" + `
entities:
  - name: "..."
    description: "..."
concepts:
  - name: "..."
    description: "..."
claims:
  - claim: "..."
    related: ["entity-or-concept-name", ...]
connections:
  - from: "..."
    to: "..."
    relationship: "..."
` + "```" + `

Leave a section with an empty list (` + "`entities: []`" + `) when nothing fits. Do NOT invent content that is not in the source. Do NOT write prose outside the code block.

Source document title: {{.Title}}
Source document path: {{.Path}}

---
{{.Content}}
`))

// writeTemplate is the "author this page from scratch" prompt. Kept
// strict about the filename and the frontmatter shape so the parser
// in writer.go doesn't need to second-guess what the LLM produced.
var writeTemplate = template.Must(template.New("write").Parse(`You are a wiki page writer. Write a markdown wiki page for the following entity/concept.

Rules:
- Begin with YAML frontmatter delimited by ` + "`---`" + ` lines. Include: title, type, sources (list), related (list, may be empty), created, updated. Use today's date in YYYY-MM-DD form for both.
- After the frontmatter, write the page body in markdown. Start with a one-line TLDR that summarises the page in a single factual sentence.
- Cross-reference related pages using [[wikilink]] syntax. Target slug form — e.g. [[circuit-breaker]] not [[Circuit Breaker]].
- Be factual — only include information grounded in the provided source.
- Keep the page concise. One page per concept, not a book chapter.
- Do NOT wrap the output in a code fence. Return the raw markdown.
- Filename will be: {{.Slug}}

Entity/concept: {{.Name}}
Type: {{.Type}}
Description: {{.Description}}

Relevant claims from source:
{{.Claims}}

Relevant connections:
{{.Connections}}

Source: {{.SourcePath}}
`))

// updateTemplate is the "merge new facts into an existing page"
// prompt. The contradiction directive is non-optional — a silently
// overwritten claim is a wiki-health regression; an explicit
// contradiction is a lint surface the user can act on.
var updateTemplate = template.Must(template.New("update").Parse(`You are a wiki maintainer. A new source has been ingested that contains information relevant to an existing wiki page. Update the page to incorporate the new information.

Rules:
- Preserve existing content that is still accurate.
- Add new information from the new source.
- Append the new source path to the frontmatter's sources list. Do NOT remove existing sources.
- Add new [[wikilinks]] where relevant.
- If the new information CONTRADICTS existing content, do NOT silently overwrite. Flag it explicitly in the body: "⚠️ Contradiction: [old-source] says X; [new-source] says Y." The user will resolve it with ` + "`anvil lint`" + `.
- Update the frontmatter's ` + "`updated`" + ` field to today's date (YYYY-MM-DD).
- Return the full updated page, frontmatter + body, as raw markdown. Do NOT wrap in a code fence.

Existing page:
---
{{.ExistingPage}}
---

New information from {{.SourcePath}}:
{{.NewInfo}}
`))

// RenderExtractPrompt produces the prompt body for the extract call.
func RenderExtractPrompt(c ExtractContext) (string, error) {
	return render(extractTemplate, c)
}

// RenderWritePrompt produces the prompt body for a create page call.
func RenderWritePrompt(c WriteContext) (string, error) {
	return render(writeTemplate, c)
}

// RenderUpdatePrompt produces the prompt body for an update page
// call.
func RenderUpdatePrompt(c UpdateContext) (string, error) {
	return render(updateTemplate, c)
}

func render(t *template.Template, data any) (string, error) {
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// SystemPrompt is the fixed system message every ingest LLM call gets.
// Kept short — backend-specific system slots charge per token and the
// per-task guidance already lives in the user prompt templates.
const SystemPrompt = "You are anvil, an LLM that maintains a structured wiki from raw source documents. Follow the user's format instructions exactly and never fabricate information that is not in the provided source."
