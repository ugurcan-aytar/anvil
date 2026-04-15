package commands

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
)

// withVerbosity temporarily flips the package-level verbosity so
// callers can assert behaviour at each level. Restored in cleanup.
func withVerbosity(t *testing.T, v Verbosity) {
	t.Helper()
	prev := verbosity
	verbosity = v
	t.Cleanup(func() { verbosity = prev })
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	defer func() { os.Stderr = old }()

	done := make(chan struct{})
	var buf bytes.Buffer
	go func() { _, _ = buf.ReadFrom(r); close(done) }()

	fn()
	_ = w.Close()
	<-done
	return buf.String()
}

func TestPrintVerboseSilentInNormal(t *testing.T) {
	withVerbosity(t, VerbosityNormal)
	out := captureStderr(t, func() { printVerbose("should-not-show\n") })
	if out != "" {
		t.Errorf("normal mode should silence printVerbose; got %q", out)
	}
}

func TestPrintVerboseShowsInVerbose(t *testing.T) {
	withVerbosity(t, VerbosityVerbose)
	out := captureStderr(t, func() { printVerbose("snippet\n") })
	if !strings.Contains(out, "snippet") {
		t.Errorf("verbose mode should surface printVerbose; got %q", out)
	}
}

// wrapClient passes the client through unchanged under quiet / normal.
func TestWrapClientPassthrough(t *testing.T) {
	client := &scriptedClient{Responses: []string{"ok"}}
	withVerbosity(t, VerbosityNormal)
	if wrapped := wrapClient(client); wrapped != client {
		t.Errorf("normal mode should pass client through unchanged")
	}
	withVerbosity(t, VerbosityQuiet)
	if wrapped := wrapClient(client); wrapped != client {
		t.Errorf("quiet mode should pass client through unchanged")
	}
}

// --verbose wraps the client so every Complete call logs a snippet.
func TestWrapClientLogsInVerbose(t *testing.T) {
	withVerbosity(t, VerbosityVerbose)
	client := &scriptedClient{Responses: []string{"my reply body"}}
	wrapped := wrapClient(client)
	if wrapped == client {
		t.Fatalf("verbose mode must wrap the client, got same handle")
	}

	out := captureStderr(t, func() {
		_, _ = wrapped.Complete(context.Background(), "sys msg", "user msg")
	})
	if !strings.Contains(out, "system: sys msg") {
		t.Errorf("verbose log should include system prompt snippet; got:\n%s", out)
	}
	if !strings.Contains(out, "user:") {
		t.Errorf("verbose log should include user prompt; got:\n%s", out)
	}
	if !strings.Contains(out, "my reply body") {
		t.Errorf("verbose log should include reply; got:\n%s", out)
	}
}

// abbrev truncates and appends an ellipsis above the cap.
func TestAbbrev(t *testing.T) {
	if got := abbrev("short", 10); got != "short" {
		t.Errorf("abbrev = %q", got)
	}
	long := strings.Repeat("x", 50)
	got := abbrev(long, 10)
	if !strings.HasSuffix(got, "…") {
		t.Errorf("abbrev should append ellipsis; got %q", got)
	}
	if len([]rune(got)) != 11 {
		t.Errorf("abbrev rune count = %d, want 11", len([]rune(got)))
	}
}

// Ensure Describe is preserved through the wrapper.
func TestWrapClientDescribe(t *testing.T) {
	withVerbosity(t, VerbosityVerbose)
	client := &scriptedClient{}
	wrapped := wrapClient(client)
	d := wrapped.Describe()
	if !strings.Contains(d, "Scripted") {
		t.Errorf("describe should retain inner's label; got %q", d)
	}
}

func init() { _ = fmt.Sprintf }
