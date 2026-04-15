package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// MockClient is the in-package fake every higher-level test (extract,
// reconcile, writer, ingest integration) uses instead of a real
// backend. It replays scripted responses verbatim, records every call,
// and can simulate errors. Stored in the production file so packages
// outside llm/ can import it via the test binary too — go test allows
// this because the type lives in a _test.go file referenced only from
// the same package's tests, and we re-expose it in ingest's test files
// via copy-paste when needed. Within the same package the interface
// assertion below proves it satisfies Client.
type MockClient struct {
	// Responses is consumed in order: call N returns Responses[N].
	// When exhausted, Complete returns an error — tests should size
	// the slice to match the expected call count.
	Responses []string
	// Err, when set, is returned by every Complete call AFTER the
	// Responses slice is exhausted. Leave zero to assert on the
	// "script exhausted" message instead.
	Err error
	// Calls records every (system, user) pair for later assertions
	// on prompt content or call count.
	Calls []MockCall
}

type MockCall struct {
	System string
	User   string
}

func (m *MockClient) Complete(ctx context.Context, system, user string) (string, error) {
	m.Calls = append(m.Calls, MockCall{System: system, User: user})
	idx := len(m.Calls) - 1
	if idx >= len(m.Responses) {
		if m.Err != nil {
			return "", m.Err
		}
		return "", fmt.Errorf("mock: no scripted response for call %d (have %d)", idx+1, len(m.Responses))
	}
	return m.Responses[idx], nil
}

func (m *MockClient) Describe() string { return "Mock LLM" }

// Compile-time check: MockClient satisfies Client.
var _ Client = (*MockClient)(nil)

// withEnv sets envs for the duration of the test and restores them via
// t.Cleanup. Pass an empty string to unset.
func withEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	prev := map[string]string{}
	for k, v := range kv {
		if cur, ok := os.LookupEnv(k); ok {
			prev[k] = cur
		} else {
			prev[k] = "__unset__"
		}
		if v == "" {
			_ = os.Unsetenv(k)
		} else {
			_ = os.Setenv(k, v)
		}
	}
	t.Cleanup(func() {
		for k, v := range prev {
			if v == "__unset__" {
				_ = os.Unsetenv(k)
			} else {
				_ = os.Setenv(k, v)
			}
		}
	})
}

// ============================================================
// DetectBackend / Select priority
// ============================================================

func TestDetectBackendPriority(t *testing.T) {
	// Clear everything so lookPath-based checks use our stub.
	withEnv(t, map[string]string{
		"ANTHROPIC_API_KEY": "",
		"OPENAI_API_KEY":    "",
		"ANVIL_CLAUDE_BIN":  "",
	})
	// Pretend claude isn't on PATH unless a test opts in.
	prevLookPath := lookPath
	lookPath = func(string) (string, error) { return "", fmt.Errorf("not found") }
	t.Cleanup(func() { lookPath = prevLookPath })

	if got := DetectBackend(); got != BackendNone {
		t.Errorf("no creds, no CLI → want BackendNone, got %v", got)
	}

	_ = os.Setenv("ANTHROPIC_API_KEY", "sk-ant-fake")
	t.Cleanup(func() { _ = os.Unsetenv("ANTHROPIC_API_KEY") })
	if got := DetectBackend(); got != BackendAnthropic {
		t.Errorf("anthropic key set → want BackendAnthropic, got %v", got)
	}

	// Anthropic must win over OpenAI when both set.
	_ = os.Setenv("OPENAI_API_KEY", "sk-fake")
	t.Cleanup(func() { _ = os.Unsetenv("OPENAI_API_KEY") })
	if got := DetectBackend(); got != BackendAnthropic {
		t.Errorf("both keys set → anthropic should still win, got %v", got)
	}

	_ = os.Unsetenv("ANTHROPIC_API_KEY")
	if got := DetectBackend(); got != BackendOpenAI {
		t.Errorf("only openai key → want BackendOpenAI, got %v", got)
	}

	_ = os.Unsetenv("OPENAI_API_KEY")
	lookPath = func(string) (string, error) { return "/fake/claude", nil }
	if got := DetectBackend(); got != BackendCLI {
		t.Errorf("no keys + claude on PATH → want BackendCLI, got %v", got)
	}
}

func TestSelectReturnsErrNoBackend(t *testing.T) {
	withEnv(t, map[string]string{
		"ANTHROPIC_API_KEY": "",
		"OPENAI_API_KEY":    "",
	})
	prevLookPath := lookPath
	lookPath = func(string) (string, error) { return "", fmt.Errorf("not found") }
	t.Cleanup(func() { lookPath = prevLookPath })

	_, err := Select()
	if err == nil {
		t.Fatal("Select with no backend should error")
	}
	if err != ErrNoBackend {
		t.Errorf("want ErrNoBackend, got %v", err)
	}
}

// ============================================================
// Anthropic client
// ============================================================

func TestAnthropicClientSuccess(t *testing.T) {
	withEnv(t, map[string]string{"ANTHROPIC_API_KEY": "sk-ant-test"})

	var gotPayload anthropicRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Header sanity.
		if got := r.Header.Get("x-api-key"); got != "sk-ant-test" {
			t.Errorf("x-api-key = %q", got)
		}
		if got := r.Header.Get("anthropic-version"); got != anthropicAPIVersion {
			t.Errorf("anthropic-version = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("content-type = %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotPayload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		// Return the documented non-streaming shape.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": "hello "},
				{"type": "text", "text": "world"},
			},
			"model": "claude-sonnet-4-6",
		})
	}))
	defer server.Close()

	client := &anthropicClient{
		BaseURL:    server.URL,
		Model:      "claude-sonnet-4-6",
		HTTPClient: server.Client(),
	}
	got, err := client.Complete(context.Background(), "sys prompt", "user prompt")
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got != "hello world" {
		t.Errorf("Complete = %q, want %q", got, "hello world")
	}
	if gotPayload.Model != "claude-sonnet-4-6" {
		t.Errorf("request model = %q", gotPayload.Model)
	}
	if gotPayload.System != "sys prompt" {
		t.Errorf("request system = %q", gotPayload.System)
	}
	if len(gotPayload.Messages) != 1 || gotPayload.Messages[0].Content != "user prompt" {
		t.Errorf("request messages = %+v", gotPayload.Messages)
	}
	if gotPayload.MaxTokens != DefaultMaxTokens {
		t.Errorf("max_tokens = %d", gotPayload.MaxTokens)
	}
}

func TestAnthropicClientSurfacesAPIError(t *testing.T) {
	withEnv(t, map[string]string{"ANTHROPIC_API_KEY": "sk-ant-test"})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"bad prompt"}}`))
	}))
	defer server.Close()

	client := &anthropicClient{BaseURL: server.URL, Model: "m", HTTPClient: server.Client()}
	_, err := client.Complete(context.Background(), "", "u")
	if err == nil {
		t.Fatal("expected error on 400")
	}
	if !strings.Contains(err.Error(), "bad prompt") {
		t.Errorf("error should include server message, got %v", err)
	}
}

func TestAnthropicClientRejectsMissingAPIKey(t *testing.T) {
	withEnv(t, map[string]string{"ANTHROPIC_API_KEY": ""})
	client := &anthropicClient{Model: "m"}
	_, err := client.Complete(context.Background(), "", "u")
	if err == nil || !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") {
		t.Errorf("want missing-env error, got %v", err)
	}
}

// ============================================================
// OpenAI-compat client
// ============================================================

func TestOpenAIClientSuccess(t *testing.T) {
	withEnv(t, map[string]string{"OPENAI_API_KEY": "sk-openai-test"})

	var gotPayload openAIRequest
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %q, want /chat/completions", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotPayload); err != nil {
			t.Fatalf("decode: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": "pong"}},
			},
		})
	}))
	defer server.Close()

	client := &openAIClient{BaseURL: server.URL, Model: "gpt-4o", HTTPClient: server.Client()}
	got, err := client.Complete(context.Background(), "sys", "ping")
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got != "pong" {
		t.Errorf("Complete = %q, want pong", got)
	}
	if gotAuth != "Bearer sk-openai-test" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if len(gotPayload.Messages) != 2 {
		t.Fatalf("messages = %+v, want 2", gotPayload.Messages)
	}
	if gotPayload.Messages[0].Role != "system" || gotPayload.Messages[0].Content != "sys" {
		t.Errorf("system msg = %+v", gotPayload.Messages[0])
	}
	if gotPayload.Messages[1].Role != "user" || gotPayload.Messages[1].Content != "ping" {
		t.Errorf("user msg = %+v", gotPayload.Messages[1])
	}
}

func TestOpenAIClientOmitsEmptySystem(t *testing.T) {
	withEnv(t, map[string]string{"OPENAI_API_KEY": "sk-openai-test"})

	var gotPayload openAIRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotPayload)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": "ok"}}},
		})
	}))
	defer server.Close()

	client := &openAIClient{BaseURL: server.URL, Model: "m", HTTPClient: server.Client()}
	if _, err := client.Complete(context.Background(), "", "u"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	// Empty system should not produce a system-role message.
	for _, m := range gotPayload.Messages {
		if m.Role == "system" {
			t.Errorf("empty system should be omitted; got message %+v", m)
		}
	}
}

func TestOpenAIClientSurfacesAPIError(t *testing.T) {
	withEnv(t, map[string]string{"OPENAI_API_KEY": "sk-openai-test"})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid key","type":"auth_error"}}`))
	}))
	defer server.Close()

	client := &openAIClient{BaseURL: server.URL, Model: "m", HTTPClient: server.Client()}
	_, err := client.Complete(context.Background(), "", "u")
	if err == nil {
		t.Fatal("expected error on 401")
	}
	if !strings.Contains(err.Error(), "invalid key") {
		t.Errorf("error should include server message, got %v", err)
	}
}

func TestOpenAIBaseURLRespectsEnv(t *testing.T) {
	withEnv(t, map[string]string{"OPENAI_BASE_URL": "http://localhost:11434/v1/"})
	if got := openAIBaseURL(); got != "http://localhost:11434/v1" {
		t.Errorf("openAIBaseURL = %q (should trim trailing slash)", got)
	}
}

// ============================================================
// CLI fallback — uses a tempdir fake shell script.
// ============================================================

func TestCLIClientReadsFakeScript(t *testing.T) {
	// Windows would need a .cmd; anvil's supported platforms are
	// macOS + Linux, so gate accordingly.
	if runtime.GOOS == "windows" {
		t.Skip("cli fallback test skips on windows")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "fakeclaude")
	body := `#!/bin/sh
# Echo the last positional argument (the user prompt) so we can
# assert the contract.
echo "cli-echo: $@"
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	client := &cliClient{Binary: script}
	got, err := client.Complete(context.Background(), "system prompt here", "what is it")
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if !strings.Contains(got, "what is it") {
		t.Errorf("stdout should contain the user prompt; got %q", got)
	}
	if !strings.Contains(got, "--system-prompt") {
		t.Errorf("should pass --system-prompt; got %q", got)
	}
}

func TestCLIClientReportsMissingBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("cli fallback test skips on windows")
	}
	client := &cliClient{Binary: "/nonexistent/path/should/not/exist/claude-x"}
	_, err := client.Complete(context.Background(), "", "hi")
	if err == nil {
		t.Fatal("want error for missing binary")
	}
	if err != ErrCLIMissing {
		t.Errorf("want ErrCLIMissing, got %v", err)
	}
}

func TestCLIClientReportsNonZeroExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("cli fallback test skips on windows")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "failing")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'boom' >&2\nexit 7\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	client := &cliClient{Binary: script}
	_, err := client.Complete(context.Background(), "", "")
	if err == nil {
		t.Fatal("want error for exit 7")
	}
	if !strings.Contains(err.Error(), "7") {
		t.Errorf("err should mention exit code 7, got %v", err)
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("err should include stderr 'boom', got %v", err)
	}
}

// ============================================================
// Model resolution precedence
// ============================================================

func TestResolveModel(t *testing.T) {
	withEnv(t, map[string]string{"ANVIL_MODEL": "", "OPENAI_MODEL": ""})
	if got := resolveModel("ANVIL_MODEL", "claude-sonnet-4-6"); got != "claude-sonnet-4-6" {
		t.Errorf("default fallback = %q", got)
	}
	_ = os.Setenv("ANVIL_MODEL", "custom-opus")
	t.Cleanup(func() { _ = os.Unsetenv("ANVIL_MODEL") })
	if got := resolveModel("ANVIL_MODEL", "claude-sonnet-4-6"); got != "custom-opus" {
		t.Errorf("env override = %q", got)
	}
}

// ============================================================
// Describe outputs
// ============================================================

func TestDescribeSummaries(t *testing.T) {
	a := &anthropicClient{Model: "claude-sonnet-4-6"}
	if !strings.Contains(a.Describe(), "Anthropic") || !strings.Contains(a.Describe(), "sonnet") {
		t.Errorf("anthropic describe = %q", a.Describe())
	}
	o := &openAIClient{BaseURL: "http://x", Model: "gpt-4o"}
	if !strings.Contains(o.Describe(), "gpt-4o") {
		t.Errorf("openai describe = %q", o.Describe())
	}
	c := &cliClient{Binary: "claude", Model: ""}
	if !strings.Contains(c.Describe(), "Claude CLI") {
		t.Errorf("cli describe = %q", c.Describe())
	}
}
