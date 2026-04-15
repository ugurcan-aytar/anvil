package ingest

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ugurcan-aytar/anvil/internal/llm"
)

// scriptedClient is a per-package copy of llm.MockClient so ingest
// tests don't depend on the llm package exposing it at test time.
// Identical contract — queued responses, optional error, per-call
// history.
type scriptedClient struct {
	Responses []string
	Err       error
	Calls     []struct{ System, User string }
}

func (s *scriptedClient) Complete(ctx context.Context, system, user string) (string, error) {
	s.Calls = append(s.Calls, struct{ System, User string }{system, user})
	idx := len(s.Calls) - 1
	if idx >= len(s.Responses) {
		if s.Err != nil {
			return "", s.Err
		}
		return "", fmt.Errorf("mock: no scripted response for call %d", idx+1)
	}
	return s.Responses[idx], nil
}

func (s *scriptedClient) Describe() string { return "Scripted" }

var _ llm.Client = (*scriptedClient)(nil)

// ============================================================
// Happy path: valid fenced YAML parses cleanly.
// ============================================================

func TestExtractHappyPath(t *testing.T) {
	resp := "Here is the extraction:\n\n" +
		"```yaml\n" +
		"entities:\n" +
		"  - name: \"Tobias Lütke\"\n" +
		"    description: \"Founder of Shopify; built qmd.\"\n" +
		"concepts:\n" +
		"  - name: \"BM25\"\n" +
		"    description: \"Probabilistic ranking function.\"\n" +
		"claims:\n" +
		"  - claim: \"qmd uses RRF for hybrid search.\"\n" +
		"    related: [\"qmd\", \"RRF\"]\n" +
		"connections:\n" +
		"  - from: \"qmd\"\n" +
		"    to: \"RRF\"\n" +
		"    relationship: \"uses\"\n" +
		"```\n"
	client := &scriptedClient{Responses: []string{resp}}

	ext, err := Extract(context.Background(), client, Source{
		Path:    "raw/qmd-paper.md",
		Title:   "qmd paper",
		Content: "qmd is a search engine written by Tobias Lütke using BM25 and RRF.",
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(ext.Entities) != 1 || ext.Entities[0].Name != "Tobias Lütke" {
		t.Errorf("entities = %+v", ext.Entities)
	}
	if len(ext.Concepts) != 1 || ext.Concepts[0].Name != "BM25" {
		t.Errorf("concepts = %+v", ext.Concepts)
	}
	if len(ext.Claims) != 1 || ext.Claims[0].Claim != "qmd uses RRF for hybrid search." {
		t.Errorf("claims = %+v", ext.Claims)
	}
	if len(ext.Claims[0].Related) != 2 {
		t.Errorf("claim.related = %v", ext.Claims[0].Related)
	}
	if len(ext.Connections) != 1 {
		t.Errorf("connections = %+v", ext.Connections)
	}
	if len(client.Calls) != 1 {
		t.Errorf("expected 1 call, got %d", len(client.Calls))
	}
}

// ============================================================
// Raw YAML (no code fence) still parses.
// ============================================================

func TestExtractAcceptsRawYAML(t *testing.T) {
	resp := `entities: []
concepts:
  - name: "Locality"
    description: "..."
claims: []
connections: []
`
	client := &scriptedClient{Responses: []string{resp}}
	ext, err := Extract(context.Background(), client, Source{
		Path: "raw/x.md", Title: "X", Content: "body",
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(ext.Concepts) != 1 || ext.Concepts[0].Name != "Locality" {
		t.Errorf("concepts = %+v", ext.Concepts)
	}
}

// ============================================================
// Retry path: first response malformed, second fenced+clean.
// ============================================================

func TestExtractRetriesOnMalformedYAML(t *testing.T) {
	bad := "The source talks about several things but I will not respond in YAML."
	good := "```yaml\nentities: []\nconcepts:\n  - name: \"Retry\"\n    description: \"Works.\"\nclaims: []\nconnections: []\n```\n"
	client := &scriptedClient{Responses: []string{bad, good}}
	ext, err := Extract(context.Background(), client, Source{
		Path: "raw/x.md", Title: "X", Content: "body",
	})
	if err != nil {
		t.Fatalf("Extract should have recovered via retry: %v", err)
	}
	if len(client.Calls) != 2 {
		t.Errorf("want 2 calls (initial + retry), got %d", len(client.Calls))
	}
	if len(ext.Concepts) != 1 || ext.Concepts[0].Name != "Retry" {
		t.Errorf("concepts = %+v", ext.Concepts)
	}
	// Second call should mention the retry directive.
	if !strings.Contains(client.Calls[1].User, "previous response could not be parsed") {
		t.Errorf("retry prompt did not include the reminder; got:\n%s", client.Calls[1].User)
	}
}

// ============================================================
// Both attempts malformed → error mentions both.
// ============================================================

func TestExtractFailsWhenRetryAlsoMalformed(t *testing.T) {
	client := &scriptedClient{Responses: []string{"nope", "still nope"}}
	_, err := Extract(context.Background(), client, Source{
		Path: "raw/x.md", Title: "X", Content: "body",
	})
	if err == nil {
		t.Fatal("expected error after two malformed responses")
	}
	if len(client.Calls) != 2 {
		t.Errorf("want 2 calls, got %d", len(client.Calls))
	}
}

// ============================================================
// Empty content is rejected before any LLM call.
// ============================================================

func TestExtractRejectsEmptyContent(t *testing.T) {
	client := &scriptedClient{}
	_, err := Extract(context.Background(), client, Source{
		Path: "raw/empty.md", Title: "Empty", Content: "   \n\t  ",
	})
	if err == nil {
		t.Fatal("expected error for empty content")
	}
	if len(client.Calls) != 0 {
		t.Errorf("should not call LLM on empty content; got %d calls", len(client.Calls))
	}
}

// ============================================================
// Truncation: oversized sources get trimmed before sending.
// ============================================================

func TestExtractTruncatesOversizedContent(t *testing.T) {
	// Craft content estimated at ~10k tokens (40k chars).
	big := strings.Repeat("word ", 12000) // 60k chars ≈ 15k tokens
	client := &scriptedClient{Responses: []string{"```yaml\nentities: []\nconcepts: []\nclaims: []\nconnections: []\n```"}}
	_, err := Extract(context.Background(), client, Source{
		Path: "raw/big.md", Title: "Big", Content: big,
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	sent := client.Calls[0].User
	if !strings.Contains(sent, "[truncated:") {
		t.Errorf("oversized source should include truncation sentinel; got tail: %q",
			tail(sent, 120))
	}
	// Prompt should be shorter than the original content (strong
	// guarantee given the sentinel + truncation window).
	if len(sent) > len(big) {
		t.Errorf("truncated prompt grew the content: %d vs %d", len(sent), len(big))
	}
}

// ============================================================
// Small content is NOT truncated.
// ============================================================

func TestExtractDoesNotTruncateSmallContent(t *testing.T) {
	small := "A short paragraph."
	client := &scriptedClient{Responses: []string{"```yaml\nentities: []\nconcepts: []\nclaims: []\nconnections: []\n```"}}
	if _, err := Extract(context.Background(), client, Source{
		Path: "raw/x.md", Title: "X", Content: small,
	}); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if strings.Contains(client.Calls[0].User, "[truncated:") {
		t.Errorf("small content should not carry truncation sentinel")
	}
	if !strings.Contains(client.Calls[0].User, small) {
		t.Errorf("small content should appear verbatim")
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
