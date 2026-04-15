package ingest

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ugurcan-aytar/anvil/internal/llm"
	"github.com/ugurcan-aytar/anvil/internal/wiki"
)

// WriteReport summarises what Write did. Created + Updated carry
// slugs (e.g. "circuit-breaker.md"). Errors is a slice because Write
// keeps going after a per-page failure — ingest of a batch of 10
// sources shouldn't abort because page 3's LLM reply was malformed.
type WriteReport struct {
	Created []string
	Updated []string
	Errors  []error
}

// Write materialises result into wiki pages. Each PageDraft → LLM
// write call → wiki.WritePage → wiki.AddToIndex. Each PageUpdate →
// LLM update call → wiki.WritePage (same slug, overwrites) →
// wiki.AddToIndex (the duplicate-row guard in AddToIndex triggers a
// full rebuild so the updated row refreshes).
//
// now is the timestamp stamped into frontmatter when the LLM's own
// reply doesn't supply one. Exported as an argument so tests can
// freeze time.
func Write(ctx context.Context, client llm.Client, result *ReconcileResult, wikiDir string, now time.Time) *WriteReport {
	report := &WriteReport{}
	if result == nil {
		report.Errors = append(report.Errors, fmt.Errorf("write: nil result"))
		return report
	}
	if client == nil {
		report.Errors = append(report.Errors, fmt.Errorf("write: nil client"))
		return report
	}
	dateStr := now.Format("2006-01-02")
	if now.IsZero() {
		dateStr = time.Now().Format("2006-01-02")
	}

	// Build the slug catalog once. We merge in every slug this
	// batch is ABOUT to create so same-source siblings can reference
	// each other via canonical slugs even when they haven't been
	// written to disk yet. Without this, the first batch's pages
	// would all see an empty catalog and drift into variant forms
	// (e.g. [[no-code-devs]] pointing at nocodedevs.md).
	cat, err := LoadSlugCatalog(wikiDir)
	if err != nil {
		report.Errors = append(report.Errors, fmt.Errorf("slug catalog: %w", err))
		// Non-fatal — fall through with a nil catalog; the prompt
		// block renders empty, matching the fresh-wiki behaviour.
		cat = nil
	}
	existingSlugs := cat.Slugs()
	for _, d := range result.Create {
		stem := strings.TrimSuffix(d.Slug, ".md")
		if stem != "" && !containsStr(existingSlugs, stem) {
			existingSlugs = append(existingSlugs, stem)
		}
	}
	for _, u := range result.Update {
		stem := strings.TrimSuffix(u.Slug, ".md")
		if stem != "" && !containsStr(existingSlugs, stem) {
			existingSlugs = append(existingSlugs, stem)
		}
	}

	for _, draft := range result.Create {
		slug, err := writeCreate(ctx, client, draft, wikiDir, dateStr, existingSlugs)
		if err != nil {
			report.Errors = append(report.Errors, fmt.Errorf("create %s: %w", draft.Slug, err))
			continue
		}
		report.Created = append(report.Created, slug)
	}
	for _, upd := range result.Update {
		slug, err := writeUpdate(ctx, client, upd, wikiDir, dateStr, existingSlugs)
		if err != nil {
			report.Errors = append(report.Errors, fmt.Errorf("update %s: %w", upd.Slug, err))
			continue
		}
		report.Updated = append(report.Updated, slug)
	}
	return report
}

// writeCreate handles one PageDraft end-to-end. Renders the write
// prompt, sends it to the LLM, parses the returned markdown, fills
// in any frontmatter the LLM forgot, persists, and appends a row to
// wiki/index.md.
func writeCreate(ctx context.Context, client llm.Client, draft PageDraft, wikiDir, dateStr string, existingSlugs []string) (string, error) {
	prompt, err := RenderWritePrompt(WriteContext{
		Slug:          draft.Slug,
		Name:          draft.Name,
		Type:          draft.Type,
		Description:   draft.Description,
		Claims:        renderClaimsBullets(draft.Claims),
		Connections:   renderConnectionsBullets(draft.Connections),
		SourcePath:    draft.SourcePath,
		ExistingSlugs: existingSlugs,
	})
	if err != nil {
		return "", fmt.Errorf("render write prompt: %w", err)
	}
	raw, err := client.Complete(ctx, SystemPrompt, prompt)
	if err != nil {
		return "", fmt.Errorf("llm write call: %w", err)
	}
	page, err := wiki.ParsePage([]byte(stripPreamble(stripFence(raw))))
	if err != nil {
		return "", fmt.Errorf("parse llm output: %w", err)
	}
	fillCreateDefaults(page, draft, dateStr)
	if err := wiki.WritePage(wikiDir, page); err != nil {
		return "", fmt.Errorf("write page: %w", err)
	}
	if err := wiki.AddToIndex(wikiDir, page, ""); err != nil {
		return "", fmt.Errorf("add to index: %w", err)
	}
	return draft.Slug, nil
}

// writeUpdate handles one PageUpdate end-to-end. Serialises the
// existing page back to markdown for the prompt (so the LLM can see
// frontmatter + body), runs the update prompt, overlays the LLM's
// returned page onto the existing one (so any LLM-dropped fields
// survive), and persists.
func writeUpdate(ctx context.Context, client llm.Client, upd PageUpdate, wikiDir, dateStr string, existingSlugs []string) (string, error) {
	existingMD, err := serialisePageForPrompt(upd.Existing)
	if err != nil {
		return "", fmt.Errorf("serialise existing page: %w", err)
	}
	prompt, err := RenderUpdatePrompt(UpdateContext{
		ExistingPage:  existingMD,
		NewInfo:       upd.NewInfo,
		SourcePath:    upd.SourcePath,
		ExistingSlugs: existingSlugs,
	})
	if err != nil {
		return "", fmt.Errorf("render update prompt: %w", err)
	}
	raw, err := client.Complete(ctx, SystemPrompt, prompt)
	if err != nil {
		return "", fmt.Errorf("llm update call: %w", err)
	}
	page, err := wiki.ParsePage([]byte(stripPreamble(stripFence(raw))))
	if err != nil {
		return "", fmt.Errorf("parse llm output: %w", err)
	}
	fillUpdateDefaults(page, upd, dateStr)
	if err := wiki.WritePage(wikiDir, page); err != nil {
		return "", fmt.Errorf("write page: %w", err)
	}
	if err := wiki.AddToIndex(wikiDir, page, ""); err != nil {
		return "", fmt.Errorf("add to index: %w", err)
	}
	return upd.Slug, nil
}

// fillCreateDefaults patches a freshly-parsed Page so mandatory
// frontmatter fields are populated even when the LLM forgets one of
// them. The draft is the source of truth for identity (title, type,
// slug); date fields fall back to now; sources always include the
// ingest's current source path.
func fillCreateDefaults(p *wiki.Page, draft PageDraft, dateStr string) {
	p.Filename = draft.Slug
	if strings.TrimSpace(p.Title) == "" {
		p.Title = draft.Name
	}
	if strings.TrimSpace(p.Type) == "" {
		p.Type = draft.Type
	}
	if !containsStr(p.Sources, draft.SourcePath) {
		p.Sources = append(p.Sources, draft.SourcePath)
	}
	if strings.TrimSpace(p.Created) == "" {
		p.Created = dateStr
	}
	if strings.TrimSpace(p.Updated) == "" {
		p.Updated = dateStr
	}
}

// fillUpdateDefaults merges the LLM's returned page with what the
// existing page already carried. Sources + Related grow (never
// shrink). Title / Type fall back to the existing or the draft.
// Created stays pinned to the existing page's original creation
// date; Updated bumps to today.
func fillUpdateDefaults(p *wiki.Page, upd PageUpdate, dateStr string) {
	p.Filename = upd.Slug
	if strings.TrimSpace(p.Title) == "" {
		p.Title = upd.Existing.Title
		if p.Title == "" {
			p.Title = upd.Name
		}
	}
	if strings.TrimSpace(p.Type) == "" {
		p.Type = upd.Existing.Type
		if p.Type == "" {
			p.Type = upd.Type
		}
	}
	// Union the sources — LLM-dropped entries come back. Append new
	// ingest source if missing.
	existingSources := upd.Existing.Sources
	for _, s := range existingSources {
		if !containsStr(p.Sources, s) {
			p.Sources = append(p.Sources, s)
		}
	}
	if !containsStr(p.Sources, upd.SourcePath) {
		p.Sources = append(p.Sources, upd.SourcePath)
	}
	// Union Related links.
	for _, r := range upd.Existing.Related {
		if !containsStr(p.Related, r) {
			p.Related = append(p.Related, r)
		}
	}
	// Preserve original creation date.
	if strings.TrimSpace(upd.Existing.Created) != "" {
		p.Created = upd.Existing.Created
	} else if strings.TrimSpace(p.Created) == "" {
		p.Created = dateStr
	}
	// Always bump the updated field on an update.
	p.Updated = dateStr
}

// renderClaimsBullets formats a claim slice as bullet lines for the
// write prompt. Empty slice returns "(none)" — the LLM deals with
// that gracefully; an empty "Claims:" section invites it to invent.
func renderClaimsBullets(claims []Claim) string {
	if len(claims) == 0 {
		return "(none)"
	}
	var b strings.Builder
	for _, c := range claims {
		fmt.Fprintf(&b, "- %s\n", c.Claim)
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderConnectionsBullets formats a connection slice the same way.
func renderConnectionsBullets(conns []Connection) string {
	if len(conns) == 0 {
		return "(none)"
	}
	var b strings.Builder
	for _, c := range conns {
		rel := c.Relationship
		if rel == "" {
			rel = "related to"
		}
		fmt.Fprintf(&b, "- %s %s %s\n", c.From, rel, c.To)
	}
	return strings.TrimRight(b.String(), "\n")
}

// stripPreamble drops any chatter the LLM wrote BEFORE the opening
// frontmatter delimiter. The prompt asks for raw markdown starting
// with "---", but some backends (especially Claude CLI) occasionally
// lead with "Here is the wiki page for X:" or a permission-error
// message before the real page. Without this, ParsePage would miss
// the frontmatter and the whole response would land in Page.Body,
// producing a duplicate-frontmatter page after fillCreateDefaults
// re-adds its own block.
//
// Strategy: find the first line that's exactly "---" (frontmatter
// opener) and slice from there. If no such line exists, return raw
// unchanged — the response genuinely has no frontmatter, the
// body-only branch of ParsePage is the right outcome.
func stripPreamble(raw string) string {
	lines := strings.Split(raw, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) == "---" {
			return strings.Join(lines[i:], "\n")
		}
	}
	return raw
}

// stripFence pulls the markdown payload out of a ``` fenced block
// when the LLM wrapped its response in one despite the prompt
// forbidding it. No-op when there's no fence.
func stripFence(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if !strings.HasPrefix(trimmed, "```") {
		return raw
	}
	// Drop first line of the fence (```markdown, ```md, ```).
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

// serialisePageForPrompt reassembles a *wiki.Page into its on-disk
// markdown form. Only used to feed the update prompt — the actual
// persist path goes through wiki.WritePage, which does its own
// serialisation.
func serialisePageForPrompt(p *wiki.Page) (string, error) {
	if p == nil {
		return "", fmt.Errorf("nil page")
	}
	// Round-trip through wiki.ParsePage would be circular. Instead
	// emit a minimal but valid frontmatter+body by mirroring the
	// serialisation contract wiki.WritePage uses internally.
	var b strings.Builder
	b.WriteString("---\n")
	if p.Title != "" {
		fmt.Fprintf(&b, "title: %s\n", p.Title)
	}
	if p.Type != "" {
		fmt.Fprintf(&b, "type: %s\n", p.Type)
	}
	if len(p.Sources) > 0 {
		b.WriteString("sources:\n")
		for _, s := range p.Sources {
			fmt.Fprintf(&b, "  - %s\n", s)
		}
	}
	if len(p.Related) > 0 {
		b.WriteString("related:\n")
		for _, r := range p.Related {
			fmt.Fprintf(&b, "  - %s\n", r)
		}
	}
	if p.Created != "" {
		fmt.Fprintf(&b, "created: %s\n", p.Created)
	}
	if p.Updated != "" {
		fmt.Fprintf(&b, "updated: %s\n", p.Updated)
	}
	b.WriteString("---\n\n")
	b.WriteString(p.Body)
	if !strings.HasSuffix(p.Body, "\n") {
		b.WriteByte('\n')
	}
	return b.String(), nil
}

func containsStr(xs []string, target string) bool {
	for _, x := range xs {
		if x == target {
			return true
		}
	}
	return false
}
