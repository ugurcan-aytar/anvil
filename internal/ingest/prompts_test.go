package ingest

import (
	"strings"
	"testing"
)

func TestRenderExtractPromptIncludesSource(t *testing.T) {
	out, err := RenderExtractPrompt(ExtractContext{
		Title:   "Circuit Breaker Paper",
		Path:    "raw/cb-paper.md",
		Content: "Paragraph describing the circuit breaker pattern.",
	})
	if err != nil {
		t.Fatalf("RenderExtractPrompt: %v", err)
	}
	for _, want := range []string{
		"Circuit Breaker Paper",
		"raw/cb-paper.md",
		"Paragraph describing the circuit breaker pattern.",
		"```yaml",
		"entities:",
		"concepts:",
		"claims:",
		"connections:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("extract prompt missing %q; full text:\n%s", want, out)
		}
	}
}

func TestRenderWritePromptIncludesInputs(t *testing.T) {
	out, err := RenderWritePrompt(WriteContext{
		Slug:        "circuit-breaker.md",
		Name:        "Circuit Breaker",
		Type:        "concept",
		Description: "A pattern for stopping cascading failures.",
		Claims:      "- Trips after N failures\n- Half-open probe",
		Connections: "- relates to: [[retry-pattern]]",
		SourcePath:  "raw/cb-paper.md",
	})
	if err != nil {
		t.Fatalf("RenderWritePrompt: %v", err)
	}
	for _, want := range []string{
		"circuit-breaker.md",
		"Circuit Breaker",
		"concept",
		"A pattern for stopping cascading failures.",
		"Trips after N failures",
		"[[retry-pattern]]",
		"raw/cb-paper.md",
		"YAML frontmatter",
		"[[wikilink]]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("write prompt missing %q; full text:\n%s", want, out)
		}
	}
}

func TestRenderUpdatePromptIncludesExistingPage(t *testing.T) {
	existing := `---
title: Circuit Breaker
type: concept
sources:
  - raw/cb-paper.md
created: 2026-04-10
updated: 2026-04-10
---

Existing page body.
`
	out, err := RenderUpdatePrompt(UpdateContext{
		ExistingPage: existing,
		NewInfo:      "Additional detail from the new source.",
		SourcePath:   "raw/new-source.md",
	})
	if err != nil {
		t.Fatalf("RenderUpdatePrompt: %v", err)
	}
	for _, want := range []string{
		"Existing page body.",
		"Additional detail from the new source.",
		"raw/new-source.md",
		"⚠️ Contradiction",
		"sources",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("update prompt missing %q", want)
		}
	}
}

func TestSystemPromptMentionsAnvil(t *testing.T) {
	if !strings.Contains(SystemPrompt, "anvil") {
		t.Errorf("system prompt should self-identify; got %q", SystemPrompt)
	}
	if !strings.Contains(strings.ToLower(SystemPrompt), "fabricate") {
		t.Errorf("system prompt should include the no-fabrication directive")
	}
}
