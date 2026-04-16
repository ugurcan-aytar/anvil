package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ugurcan-aytar/anvil/internal/llm"
)

// concurrentMockClient tracks the peak number of in-flight Complete
// calls so we can assert that --workers actually produces parallelism.
type concurrentMockClient struct {
	mu         sync.Mutex
	inFlight   int
	peak       int
	calls      int
	extractYAML string
	pageBody    string
}

func (m *concurrentMockClient) Complete(ctx context.Context, system, user string) (string, error) {
	m.mu.Lock()
	m.inFlight++
	if m.inFlight > m.peak {
		m.peak = m.inFlight
	}
	m.calls++
	call := m.calls
	m.mu.Unlock()

	// Simulate LLM latency so concurrent callers actually overlap.
	// 10ms is plenty — the race detector doesn't need longer.
	<-timeAfter(10)

	m.mu.Lock()
	m.inFlight--
	m.mu.Unlock()

	// Extract calls look different from write calls — the prompt
	// for extract asks for YAML. Cheap heuristic to route responses.
	if strings.Contains(user, "extract") || strings.Contains(user, "YAML") {
		return m.extractYAML, nil
	}
	_ = call
	return m.pageBody, nil
}

func (m *concurrentMockClient) Describe() string { return "Concurrent Mock" }

var _ llm.Client = (*concurrentMockClient)(nil)

// timeAfter returns a channel that fires after ms milliseconds. A
// tiny helper so tests don't need a time.After at the call site.
func timeAfter(ms int) <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		time.Sleep(time.Duration(ms) * time.Millisecond)
		close(ch)
	}()
	return ch
}

// TestIngestConcurrentProducesParallelExtracts: with --workers 3 and
// 5 files, the in-flight counter should peak at >1.
func TestIngestConcurrentProducesParallelExtracts(t *testing.T) {
	root := bootstrapProject(t)
	for i := 1; i <= 5; i++ {
		name := filepath.Join(root, "raw", "f"+string(rune('0'+i))+".md")
		os.WriteFile(name, []byte("Content block "+string(rune('0'+i))+"."), 0o644)
	}
	extractYAML := "```yaml\nentities: []\nconcepts:\n  - name: \"Thing" + "\"\n    description: \"x\"\nclaims: []\nconnections: []\n```"
	pageBody := "---\ntitle: Thing\ntype: concept\nsources:\n  - raw/f1.md\ncreated: 2026-04-16\nupdated: 2026-04-16\n---\n\nBody.\n"

	mock := &concurrentMockClient{extractYAML: extractYAML, pageBody: pageBody}
	prev := newLLMClient
	t.Cleanup(func() { newLLMClient = prev })
	newLLMClient = func() (llm.Client, error) { return mock, nil }

	args := []string{filepath.Join(root, "raw")}
	withProjectDir(t, root, func() {
		if _, err := captureStdout(t, func() error {
			return runIngest(context.Background(), args, ingestOptions{Workers: 3})
		}); err != nil {
			t.Fatalf("runIngest: %v", err)
		}
	})
	if mock.peak < 2 {
		t.Errorf("expected in-flight peak ≥ 2 with --workers 3; got %d (calls=%d)", mock.peak, mock.calls)
	}
}

// TestIngestConcurrentRaceClean: --workers with -race should produce
// no data races. Go test -race will catch any leaks at the end.
func TestIngestConcurrentRaceClean(t *testing.T) {
	root := bootstrapProject(t)
	for i := 1; i <= 4; i++ {
		name := filepath.Join(root, "raw", "r"+string(rune('0'+i))+".md")
		os.WriteFile(name, []byte("body "+string(rune('0'+i))), 0o644)
	}
	extractYAML := "```yaml\nentities: []\nconcepts: []\nclaims: []\nconnections: []\n```"

	mock := &concurrentMockClient{extractYAML: extractYAML}
	prev := newLLMClient
	t.Cleanup(func() { newLLMClient = prev })
	newLLMClient = func() (llm.Client, error) { return mock, nil }

	args := []string{filepath.Join(root, "raw")}
	withProjectDir(t, root, func() {
		if _, err := captureStdout(t, func() error {
			return runIngest(context.Background(), args, ingestOptions{Workers: 4})
		}); err != nil {
			t.Fatalf("runIngest: %v", err)
		}
	})
}
