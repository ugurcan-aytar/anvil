// Package llm is anvil's minimal batch LLM client.
//
// Three transports are supported, picked by priority at Select time:
//
//  1. Anthropic REST        -- ANTHROPIC_API_KEY set
//  2. OpenAI-compatible     -- OPENAI_API_KEY set (covers OpenAI proper,
//                              Ollama, OpenRouter, LM Studio, Groq, Together…
//                              via OPENAI_BASE_URL)
//  3. Claude CLI fallback   -- `claude` (or $ANVIL_CLAUDE_BIN) on PATH
//
// Unlike brain's llm package there is no streaming, no markdown renderer,
// no caching, no thinking phases. anvil's ingest / reconcile / write pipeline
// only needs a single request → full response. Every backend implements the
// same Client interface so callers swap transports transparently.
//
// Tests must never hit a real API. Use MockClient (in llm_test.go) or feed
// anthropicClient / openAIClient a custom BaseURL pointing at httptest.
package llm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Client is the batch completion contract every backend satisfies.
// Complete blocks until the model returns a full response (or ctx is
// cancelled) and returns the assistant text verbatim. No chunked
// delivery, no partial results — callers that need streaming should
// import brain's llm package instead.
type Client interface {
	// Complete sends system + user and returns the model's text reply.
	// An empty system prompt is permitted (some providers treat it as
	// no system instruction). ctx cancellation unwinds the in-flight
	// request and returns its error.
	Complete(ctx context.Context, system, user string) (string, error)

	// Describe returns a one-line human-readable backend summary for
	// anvil status / doctor output ("Anthropic API (claude-sonnet-4-6)").
	Describe() string
}

// Backend enumerates the transports Select can hand back. BackendNone
// indicates no credential / binary was found — Select returns
// ErrNoBackend in that case.
type Backend int

const (
	BackendNone Backend = iota
	BackendAnthropic
	BackendOpenAI
	BackendCLI
)

// DefaultMaxTokens is the max_tokens value both HTTP backends send on
// every request. High enough to fit an entire wiki page + frontmatter,
// low enough to keep ingest latency bounded when a source triggers
// verbose extractions.
const DefaultMaxTokens = 4096

// ErrNoBackend is returned from Select when no backend is configured.
// Callers are expected to print install / env-var guidance rather than
// letting the raw error bubble up.
var ErrNoBackend = errors.New(
	"no LLM backend configured — set ANTHROPIC_API_KEY, OPENAI_API_KEY, " +
		"or install the `claude` CLI (override path via ANVIL_CLAUDE_BIN)")

// lookPath is a seam for tests that want to stub PATH resolution. The
// production code path never reassigns this; tests restore it via
// t.Cleanup so no cross-test leakage.
var lookPath = exec.LookPath

// DetectBackend reports which transport Select would pick right now.
// Exported for `anvil doctor` and the setup-guidance renderer.
func DetectBackend() Backend {
	if strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")) != "" {
		return BackendAnthropic
	}
	if strings.TrimSpace(os.Getenv("OPENAI_API_KEY")) != "" {
		return BackendOpenAI
	}
	if _, err := lookPath(claudeBinary()); err == nil {
		return BackendCLI
	}
	return BackendNone
}

// Select returns the Client anvil will use for this invocation. Picks
// the first available backend in the documented priority order; returns
// ErrNoBackend when none of the three options is configured.
func Select() (Client, error) {
	switch DetectBackend() {
	case BackendAnthropic:
		return newAnthropicClient(), nil
	case BackendOpenAI:
		return newOpenAIClient(), nil
	case BackendCLI:
		return newCLIClient(), nil
	default:
		return nil, ErrNoBackend
	}
}

// claudeBinary returns the executable name the CLI fallback will exec.
// Defaults to `claude`; override via ANVIL_CLAUDE_BIN (for opencode and
// other drop-in replacements that speak the same -p / -o contract).
func claudeBinary() string {
	if name := strings.TrimSpace(os.Getenv("ANVIL_CLAUDE_BIN")); name != "" {
		return name
	}
	return "claude"
}

// resolveModel applies the model override precedence documented in
// CLAUDE.md: an env var beats the backend's default. `envName` is the
// variable consulted ("ANVIL_MODEL" for Anthropic, "OPENAI_MODEL" for
// the OpenAI-compatible path).
func resolveModel(envName, def string) string {
	if v := strings.TrimSpace(os.Getenv(envName)); v != "" {
		return v
	}
	return def
}

// BackendName is a short lowercase label used in Describe output and
// occasional log lines. Kept as a separate function so tests can assert
// on the string without pattern-matching Describe()'s longer form.
func (b Backend) BackendName() string {
	switch b {
	case BackendAnthropic:
		return "anthropic"
	case BackendOpenAI:
		return "openai"
	case BackendCLI:
		return "cli"
	default:
		return "none"
	}
}

// SetupGuidance returns a multi-line message that tells the user how
// to make a backend available. Rendered by `anvil doctor` and by the
// ingest command when Select errors.
func SetupGuidance() string {
	return strings.TrimSpace(`
No LLM backend is configured. anvil needs one of:

  1. Anthropic API         export ANTHROPIC_API_KEY=sk-ant-...
     (model override)      export ANVIL_MODEL=claude-sonnet-4-6

  2. OpenAI-compatible     export OPENAI_API_KEY=sk-...
                           export OPENAI_BASE_URL=http://localhost:11434/v1   # Ollama
                           export OPENAI_MODEL=gpt-4o

  3. Claude CLI fallback   install https://docs.claude.com/en/docs/claude-code
                           (override path: ANVIL_CLAUDE_BIN=/opt/bin/claude)
`)
}

// assertEnv is a tiny helper for clients that want to double-check a
// required env var right before the network call. Returns a formatted
// error instead of an empty API key if the variable is unset — easier
// to debug than a 401 from the remote.
func assertEnv(name string) (string, error) {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return "", fmt.Errorf("%s is not set", name)
	}
	return v, nil
}
