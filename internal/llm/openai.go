package llm

// OpenAI-compatible transport — non-streaming.
//
// Anything that speaks /v1/chat/completions works: OpenAI, Ollama
// (http://localhost:11434/v1), OpenRouter, LM Studio, LiteLLM, Groq,
// Together, Fireworks. Override the endpoint with OPENAI_BASE_URL;
// override the model name with OPENAI_MODEL. ANVIL_MODEL is ignored
// here on purpose — its value (e.g. `claude-sonnet-4-6`) is
// meaningful only to the Anthropic backend, and silently sending it
// to an OpenAI-compat server would produce confusing 400s.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

const (
	defaultOpenAIBaseURL = "https://api.openai.com/v1"
	defaultOpenAIModel   = "gpt-4o"
)

// openAIClient is the Client implementation for any OpenAI-compatible
// /v1/chat/completions endpoint.
type openAIClient struct {
	// BaseURL is the API root (everything up to but not including
	// "/chat/completions"). Empty falls back to the OpenAI production
	// endpoint. OPENAI_BASE_URL picks this up; tests set it directly
	// to the httptest.Server.URL.
	BaseURL string
	// Model is the resolved chat completions model. OPENAI_MODEL
	// wins over the package default; ANVIL_MODEL is ignored.
	Model string
	// HTTPClient is overridable for tests. nil → http.DefaultClient.
	HTTPClient *http.Client
}

func newOpenAIClient() *openAIClient {
	return &openAIClient{
		BaseURL:    openAIBaseURL(),
		Model:      resolveModel("OPENAI_MODEL", defaultOpenAIModel),
		HTTPClient: &http.Client{},
	}
}

// openAIBaseURL returns the user's OPENAI_BASE_URL with any trailing
// slash stripped, or the production default when unset.
func openAIBaseURL() string {
	if base := strings.TrimSpace(os.Getenv("OPENAI_BASE_URL")); base != "" {
		return strings.TrimRight(base, "/")
	}
	return defaultOpenAIBaseURL
}

type openAIRequest struct {
	Model     string          `json:"model"`
	Messages  []openAIMessage `json:"messages"`
	MaxTokens int             `json:"max_tokens,omitempty"`
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// openAIResponse captures the subset of the chat.completion shape
// anvil uses. Providers vary on optional fields (usage, id, created)
// but choices[0].message.content is universal.
type openAIResponse struct {
	Choices []struct {
		Message openAIMessage `json:"message"`
	} `json:"choices"`
}

type openAIError struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

// Complete posts system + user as a two-message array and returns the
// assistant message content. An empty system prompt is skipped rather
// than sent as `{"role": "system", "content": ""}`, which some
// providers interpret as "no system message" and others as an empty
// user-facing directive.
func (c *openAIClient) Complete(ctx context.Context, system, user string) (string, error) {
	apiKey, err := assertEnv("OPENAI_API_KEY")
	if err != nil {
		return "", err
	}

	payload := openAIRequest{
		Model:     c.Model,
		MaxTokens: DefaultMaxTokens,
		Messages:  make([]openAIMessage, 0, 2),
	}
	if strings.TrimSpace(system) != "" {
		payload.Messages = append(payload.Messages, openAIMessage{Role: "system", Content: system})
	}
	payload.Messages = append(payload.Messages, openAIMessage{Role: "user", Content: user})

	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode request: %w", err)
	}

	base := c.BaseURL
	if base == "" {
		base = defaultOpenAIBaseURL
	}
	endpoint := strings.TrimRight(base, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("openai request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr openAIError
		if err := json.Unmarshal(raw, &apiErr); err == nil && apiErr.Error.Message != "" {
			return "", fmt.Errorf("openai API error (%d): %s", resp.StatusCode, apiErr.Error.Message)
		}
		return "", fmt.Errorf("openai API returned %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var parsed openAIResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("openai response had no choices")
	}
	return parsed.Choices[0].Message.Content, nil
}

func (c *openAIClient) Describe() string {
	base := c.BaseURL
	if base == "" {
		base = defaultOpenAIBaseURL
	}
	return fmt.Sprintf("OpenAI-compatible %s (%s)", base, c.Model)
}
