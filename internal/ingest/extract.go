package ingest

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ugurcan-aytar/anvil/internal/llm"
)

// Token-budget heuristic. Real BERT-style tokenization would tell the
// truth but the LLM backends don't expose a pre-tokenizer and the
// decision here is coarse: pass it through or truncate. 4 chars per
// token is the standard thumbnail for English / mixed text, close
// enough for "is this source too big for one ingest call".
const (
	approxCharsPerToken = 4
	maxSourceTokens     = 8000
	truncatedTokens     = 6000
)

// Extract calls the LLM with the extract prompt and returns the
// parsed YAML extraction. On malformed YAML, retries once with a
// reminder appended to the prompt — LLMs occasionally bury the YAML
// block inside explanatory prose, and the nudge usually fixes it.
// Larger sources are truncated to truncatedTokens' worth of chars
// before being sent, with a sentinel appended so the LLM knows.
func Extract(ctx context.Context, client llm.Client, source Source) (*Extraction, error) {
	if client == nil {
		return nil, fmt.Errorf("extract: llm client is nil")
	}
	if strings.TrimSpace(source.Content) == "" {
		return nil, fmt.Errorf("extract: source %q has empty content", source.Path)
	}

	prompt, err := RenderExtractPrompt(ExtractContext{
		Title:   source.Title,
		Path:    source.Path,
		Content: maybeTruncate(source.Content),
	})
	if err != nil {
		return nil, fmt.Errorf("render extract prompt: %w", err)
	}

	raw, err := client.Complete(ctx, SystemPrompt, prompt)
	if err != nil {
		return nil, fmt.Errorf("llm extract call: %w", err)
	}
	ext, parseErr := parseExtraction(raw)
	if parseErr == nil {
		return ext, nil
	}

	// One retry with an explicit reminder. The retry prompt trails
	// the original so the model has full context about what it's
	// correcting — not a cold re-ask.
	retry := prompt + "\n\nYour previous response could not be parsed as YAML. Respond again with ONLY the YAML block inside a fenced ```yaml``` code block, nothing else."
	raw2, err := client.Complete(ctx, SystemPrompt, retry)
	if err != nil {
		return nil, fmt.Errorf("llm extract retry: %w", err)
	}
	ext, err = parseExtraction(raw2)
	if err != nil {
		return nil, fmt.Errorf("extract: yaml parse failed twice (last error: %v); first attempt: %w", err, parseErr)
	}
	return ext, nil
}

// maybeTruncate shortens content when its token estimate exceeds the
// soft cap, appending a sentinel so the LLM knows the tail is missing.
// Preserves the full text when it fits.
func maybeTruncate(content string) string {
	tokens := estimateTokens(content)
	if tokens <= maxSourceTokens {
		return content
	}
	maxChars := truncatedTokens * approxCharsPerToken
	if maxChars >= len(content) {
		return content
	}
	return content[:maxChars] + "\n\n... [truncated: source was longer than the ingest window]"
}

// estimateTokens is the cheap heuristic Extract uses to decide when
// to truncate. Not a real tokenizer — tests just need it to be
// monotonic in length.
func estimateTokens(s string) int {
	if s == "" {
		return 0
	}
	return len(s) / approxCharsPerToken
}

// yamlFenceRE matches the ```yaml ... ``` block the prompt asks for.
// MustCompile here because the regex is literal; compile errors at
// package load are preferable to silent parse fails.
var yamlFenceRE = regexp.MustCompile("(?s)```(?:yaml|yml)?\\s*\\n(.*?)```")

// parseExtraction pulls the YAML body out of an LLM response and
// unmarshals it into Extraction. Three progressively-looser
// strategies cover the shapes the LLM is likely to produce:
//
//  1. Fenced ```yaml / ```yml block (what the prompt asks for)
//  2. Fenced unlabelled ``` block
//  3. Raw YAML — the entire response unmarshals cleanly.
//
// An error from every strategy bubbles up so the caller can trigger
// the retry.
func parseExtraction(raw string) (*Extraction, error) {
	attempts := extractionCandidates(raw)
	if len(attempts) == 0 {
		return nil, fmt.Errorf("no YAML content in response")
	}
	var lastErr error
	for _, body := range attempts {
		var ext Extraction
		if err := yaml.Unmarshal([]byte(body), &ext); err != nil {
			lastErr = err
			continue
		}
		return &ext, nil
	}
	return nil, fmt.Errorf("yaml unmarshal: %w", lastErr)
}

// extractionCandidates returns the YAML bodies worth trying, in
// preference order. Uses a deduped slice so the raw-body fallback
// doesn't retry the same text the fence regex already matched.
func extractionCandidates(raw string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, 3)
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		if _, ok := seen[s]; ok {
			return
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	for _, m := range yamlFenceRE.FindAllStringSubmatch(raw, -1) {
		if len(m) >= 2 {
			add(m[1])
		}
	}
	add(raw)
	return out
}
