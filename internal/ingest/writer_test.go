package ingest

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ugurcan-aytar/anvil/internal/wiki"
)

// fixedTime is the timestamp Write stamps into frontmatter for tests.
// Deterministic output makes assertions stable.
var fixedTime = time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)

const fixedDateStr = "2026-04-15"

// ============================================================
// Create path: LLM writes a fully-formed page, Write persists + indexes.
// ============================================================

func TestWriteCreatesNewPage(t *testing.T) {
	wikiDir := emptyWiki(t)

	llmOutput := `---
title: Circuit Breaker
type: concept
sources:
  - raw/cb-paper.md
related:
  - retry-pattern
created: 2026-04-15
updated: 2026-04-15
---

Circuit breakers stop cascading failures in distributed systems. See [[retry-pattern]] for a complementary technique.
`
	client := &scriptedClient{Responses: []string{llmOutput}}
	result := &ReconcileResult{
		Create: []PageDraft{{
			Slug:        "circuit-breaker.md",
			Name:        "Circuit Breaker",
			Type:        "concept",
			Description: "Stops cascades.",
			Claims:      []Claim{{Claim: "Trips after N failures.", Related: []string{"Circuit Breaker"}}},
			Connections: []Connection{{From: "Circuit Breaker", To: "Retry Pattern", Relationship: "complements"}},
			SourcePath:  "raw/cb-paper.md",
		}},
	}
	report := Write(context.Background(), client, result, wikiDir, fixedTime)
	if len(report.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", report.Errors)
	}
	if len(report.Created) != 1 || report.Created[0] != "circuit-breaker.md" {
		t.Errorf("Created = %v", report.Created)
	}

	// Page persisted with correct frontmatter + body.
	written, err := wiki.ReadPage(wikiDir, "circuit-breaker.md")
	if err != nil {
		t.Fatalf("ReadPage: %v", err)
	}
	if written.Title != "Circuit Breaker" {
		t.Errorf("Title = %q", written.Title)
	}
	if written.Type != "concept" {
		t.Errorf("Type = %q", written.Type)
	}
	if !containsStr(written.Sources, "raw/cb-paper.md") {
		t.Errorf("Sources = %v", written.Sources)
	}
	if !strings.Contains(written.Body, "[[retry-pattern]]") {
		t.Errorf("body should carry the wikilink; body was:\n%s", written.Body)
	}

	// Index was updated.
	idx, err := os.ReadFile(filepath.Join(wikiDir, "index.md"))
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if !strings.Contains(string(idx), "[[circuit-breaker]]") {
		t.Errorf("index missing new page; body:\n%s", string(idx))
	}
}

// ============================================================
// Create path: LLM forgets frontmatter fields, Write fills defaults.
// ============================================================

func TestWriteFillsMissingFrontmatter(t *testing.T) {
	wikiDir := emptyWiki(t)

	// LLM returned a page with no type, no sources, no dates.
	llmOutput := `---
title: Sparse Page
---

Body only.
`
	client := &scriptedClient{Responses: []string{llmOutput}}
	result := &ReconcileResult{
		Create: []PageDraft{{
			Slug:       "sparse-page.md",
			Name:       "Sparse Page",
			Type:       "entity",
			SourcePath: "raw/source.md",
		}},
	}
	report := Write(context.Background(), client, result, wikiDir, fixedTime)
	if len(report.Errors) > 0 {
		t.Fatalf("errors: %v", report.Errors)
	}
	page, err := wiki.ReadPage(wikiDir, "sparse-page.md")
	if err != nil {
		t.Fatalf("ReadPage: %v", err)
	}
	if page.Type != "entity" {
		t.Errorf("Type default fill = %q", page.Type)
	}
	if !containsStr(page.Sources, "raw/source.md") {
		t.Errorf("Source default fill = %v", page.Sources)
	}
	if page.Created != fixedDateStr {
		t.Errorf("Created default = %q", page.Created)
	}
	if page.Updated != fixedDateStr {
		t.Errorf("Updated default = %q", page.Updated)
	}
}

// ============================================================
// Create path: LLM wraps output in a ``` fence — still parses.
// ============================================================

func TestWriteStripsFencedResponse(t *testing.T) {
	wikiDir := emptyWiki(t)
	llmOutput := "```markdown\n" + `---
title: Fenced Page
type: concept
---

Hello.
` + "\n```"
	client := &scriptedClient{Responses: []string{llmOutput}}
	result := &ReconcileResult{
		Create: []PageDraft{{
			Slug: "fenced-page.md", Name: "Fenced Page", Type: "concept", SourcePath: "raw/x.md",
		}},
	}
	report := Write(context.Background(), client, result, wikiDir, fixedTime)
	if len(report.Errors) > 0 {
		t.Fatalf("errors: %v", report.Errors)
	}
	if _, err := wiki.ReadPage(wikiDir, "fenced-page.md"); err != nil {
		t.Errorf("fenced response did not round-trip: %v", err)
	}
}

// ============================================================
// Update path: existing page + LLM merge, Updated bumps, Created preserved.
// ============================================================

func TestWriteUpdatesExistingPage(t *testing.T) {
	wikiDir := emptyWiki(t)
	writePageFile(t, wikiDir, "circuit-breaker.md", "Circuit Breaker", "concept")
	// The helper writes Created=Updated=2026-01-01 and one old source.
	existing, err := wiki.ReadPage(wikiDir, "circuit-breaker.md")
	if err != nil {
		t.Fatal(err)
	}

	llmOutput := `---
title: Circuit Breaker
type: concept
sources:
  - raw/new.md
created: 2026-04-15
updated: 2026-04-15
---

Updated body with [[retry-pattern]] mention.
`
	client := &scriptedClient{Responses: []string{llmOutput}}
	result := &ReconcileResult{
		Update: []PageUpdate{{
			Slug:       "circuit-breaker.md",
			Name:       "Circuit Breaker",
			Type:       "concept",
			Existing:   existing,
			NewInfo:    "Half-open probe.",
			SourcePath: "raw/new.md",
		}},
	}
	report := Write(context.Background(), client, result, wikiDir, fixedTime)
	if len(report.Errors) > 0 {
		t.Fatalf("errors: %v", report.Errors)
	}
	if len(report.Updated) != 1 || report.Updated[0] != "circuit-breaker.md" {
		t.Errorf("Updated = %v", report.Updated)
	}
	updated, err := wiki.ReadPage(wikiDir, "circuit-breaker.md")
	if err != nil {
		t.Fatalf("ReadPage: %v", err)
	}
	// Original source must survive even though the LLM only listed
	// the new one.
	if !containsStr(updated.Sources, "raw/old.md") {
		t.Errorf("original source dropped; Sources = %v", updated.Sources)
	}
	if !containsStr(updated.Sources, "raw/new.md") {
		t.Errorf("new source missing; Sources = %v", updated.Sources)
	}
	// Created preserved.
	if updated.Created != "2026-01-01" {
		t.Errorf("Created should stay at original date; got %q", updated.Created)
	}
	// Updated bumped.
	if updated.Updated != fixedDateStr {
		t.Errorf("Updated = %q, want %q", updated.Updated, fixedDateStr)
	}
	// Body reflects LLM output.
	if !strings.Contains(updated.Body, "[[retry-pattern]]") {
		t.Errorf("body missing wikilink; body:\n%s", updated.Body)
	}
}

// ============================================================
// Failure path: LLM returns garbage → error captured, other pages proceed.
// ============================================================

func TestWriteCollectsErrorsAndContinues(t *testing.T) {
	wikiDir := emptyWiki(t)
	good := `---
title: Good
type: concept
sources:
  - raw/x.md
---

Body.
`
	// First response opens a frontmatter block but never closes it —
	// parsePage flags that; the writer should capture the error and
	// proceed to the next draft.
	malformed := "---\ntitle: Bad\ntype: concept\n\nbody without closing delimiter\n"
	client := &scriptedClient{Responses: []string{malformed, good}}
	result := &ReconcileResult{
		Create: []PageDraft{
			{Slug: "bad.md", Name: "Bad", Type: "concept", SourcePath: "raw/x.md"},
			{Slug: "good.md", Name: "Good", Type: "concept", SourcePath: "raw/x.md"},
		},
	}
	report := Write(context.Background(), client, result, wikiDir, fixedTime)
	if len(report.Errors) != 1 {
		t.Errorf("expected 1 error, got %d (errors: %v)", len(report.Errors), report.Errors)
	}
	if len(report.Created) != 1 || report.Created[0] != "good.md" {
		t.Errorf("Created = %v", report.Created)
	}
}

// ============================================================
// renderClaimsBullets / renderConnectionsBullets empty-list sentinel.
// ============================================================

func TestRenderBulletsEmptyYieldsNone(t *testing.T) {
	if got := renderClaimsBullets(nil); got != "(none)" {
		t.Errorf("empty claims = %q", got)
	}
	if got := renderConnectionsBullets(nil); got != "(none)" {
		t.Errorf("empty connections = %q", got)
	}
}
