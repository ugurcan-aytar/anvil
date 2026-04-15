package llm

// Anthropic Messages API transport — non-streaming.
//
// We deliberately skip the official SDK and speak HTTP directly: the
// payload shape below is stable, and a single extra dependency would
// pull in streaming / content-block / cache-control code anvil never
// uses. Requests and responses are JSON; on non-2xx we try to surface
// the server's error message before falling back to a raw status dump.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const (
	defaultAnthropicEndpoint = "https://api.anthropic.com/v1/messages"
	defaultAnthropicModel    = "claude-sonnet-4-6"
	anthropicAPIVersion      = "2023-06-01"
)

// anthropicClient is the Client implementation for the Messages API.
// Fields are exported for httptest-based tests in the same package
// (they construct the struct directly to point at a fake server).
type anthropicClient struct {
	// BaseURL is the full endpoint URL, including /v1/messages. When
	// empty, falls back to the Anthropic production endpoint. Tests
	// override with an httptest.Server URL.
	BaseURL string
	// Model is the resolved model string (sonnet-4-6 by default,
	// ANVIL_MODEL overrides). Exposed so Describe can render it.
	Model string
	// HTTPClient lets tests inject a client with a short timeout or
	// a custom RoundTripper. Defaults to http.DefaultClient.
	HTTPClient *http.Client
}

func newAnthropicClient() *anthropicClient {
	return &anthropicClient{
		BaseURL: defaultAnthropicEndpoint,
		Model:   resolveModel("ANVIL_MODEL", defaultAnthropicModel),
		HTTPClient: &http.Client{
			// No timeout by default — ingest prompts can be long and
			// callers are expected to pass a ctx with deadline when
			// they care. Tests override via WithHTTPClient.
			Timeout: 0,
		},
	}
}

// anthropicRequest mirrors the shape the Messages endpoint expects for
// a plain text prompt. We skip caching, tool-use, and multi-part
// content blocks — strings are enough for ingest.
type anthropicRequest struct {
	Model     string                `json:"model"`
	MaxTokens int                   `json:"max_tokens"`
	System    string                `json:"system,omitempty"`
	Messages  []anthropicReqMessage `json:"messages"`
}

type anthropicReqMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// anthropicResponse is the non-streaming response shape. Content is
// always a list of blocks; we join the text of every text-typed block
// into the returned string (models that emit multiple blocks for one
// response — e.g. some tool-use paths — still concatenate cleanly).
type anthropicResponse struct {
	Content []anthropicContentBlock `json:"content"`
	Model   string                  `json:"model"`
}

type anthropicContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type anthropicError struct {
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// Complete posts system + user to the Messages API and returns the
// concatenated text content. Any non-2xx status is surfaced with the
// server's error message when the body parses as the documented error
// envelope; otherwise the raw body is included for debuggability.
func (c *anthropicClient) Complete(ctx context.Context, system, user string) (string, error) {
	apiKey, err := assertEnv("ANTHROPIC_API_KEY")
	if err != nil {
		return "", err
	}

	payload := anthropicRequest{
		Model:     c.Model,
		MaxTokens: DefaultMaxTokens,
		System:    system,
		Messages: []anthropicReqMessage{
			{Role: "user", Content: user},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode request: %w", err)
	}

	url := c.BaseURL
	if url == "" {
		url = defaultAnthropicEndpoint
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", anthropicAPIVersion)

	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("anthropic request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr anthropicError
		if err := json.Unmarshal(raw, &apiErr); err == nil && apiErr.Error.Message != "" {
			return "", fmt.Errorf("anthropic API error (%d): %s", resp.StatusCode, apiErr.Error.Message)
		}
		return "", fmt.Errorf("anthropic API returned %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var parsed anthropicResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	var out strings.Builder
	for _, block := range parsed.Content {
		if block.Type == "text" {
			out.WriteString(block.Text)
		}
	}
	return out.String(), nil
}

// Describe returns the "Anthropic API (model)" summary used by
// `anvil doctor` and the ingest setup banner.
func (c *anthropicClient) Describe() string {
	return fmt.Sprintf("Anthropic API (%s)", c.Model)
}
