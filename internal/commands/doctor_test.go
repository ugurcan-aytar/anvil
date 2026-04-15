package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ugurcan-aytar/anvil/internal/llm"
)

// TestDoctorHappyPath: fresh project → all checks green.
func TestDoctorHappyPath(t *testing.T) {
	root := bootstrapProject(t)
	// Backend present — doctor should list it.
	_ = swapLLMClient(t, nil)

	var out string
	var err error
	withProjectDir(t, root, func() {
		out, err = captureStdout(t, func() error { return runDoctor(context.Background()) })
	})
	if err != nil {
		t.Fatalf("runDoctor: %v", err)
	}
	for _, want := range []string{
		"anvil doctor",
		"Project:",
		"Wiki:",
		"Raw:",
		"Recall DB:",
		"LLM backend:",
		"ANVIL.md:",
		"Index:",
		"Log:",
		"All checks passed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output missing %q; body:\n%s", want, out)
		}
	}
}

// TestDoctorReportsMissingBackend: when newLLMClient returns
// ErrNoBackend, the LLM line should ⚠ (warn, not fail) — ingest /
// ask / save are disabled but lint --structural-only and search
// still work.
func TestDoctorReportsMissingBackend(t *testing.T) {
	root := bootstrapProject(t)
	prev := newLLMClient
	t.Cleanup(func() { newLLMClient = prev })
	newLLMClient = func() (llm.Client, error) { return nil, llm.ErrNoBackend }

	var out string
	withProjectDir(t, root, func() {
		out, _ = captureStdout(t, func() error { return runDoctor(context.Background()) })
	})
	if !strings.Contains(out, "LLM backend:") {
		t.Fatalf("LLM backend row missing; body:\n%s", out)
	}
	// Warn markers ship as ⚠ — specific enough to assert on.
	if !strings.Contains(out, "not configured") {
		t.Errorf("missing-backend should surface 'not configured'; body:\n%s", out)
	}
}

// TestDoctorDetectsMissingANVILmd: removing ANVIL.md should surface
// a ✗ row (the file is the engine.Open gate and the project marker).
func TestDoctorDetectsMissingANVILmd(t *testing.T) {
	root := bootstrapProject(t)
	if err := os.Remove(filepath.Join(root, "ANVIL.md")); err != nil {
		t.Fatal(err)
	}
	_ = swapLLMClient(t, nil)

	var out string
	withProjectDir(t, root, func() {
		out, _ = captureStdout(t, func() error { return runDoctor(context.Background()) })
	})
	if !strings.Contains(out, "ANVIL.md") {
		t.Fatalf("output missing ANVIL.md row; body:\n%s", out)
	}
	if !strings.Contains(out, "✗") {
		t.Errorf("expected ✗ on broken ANVIL.md row; body:\n%s", out)
	}
	if !strings.Contains(out, "issue(s) found") {
		t.Errorf("broken state should trip the summary counter; body:\n%s", out)
	}
}

// TestDoctorStaleIndex: writing a page without rebuilding the index
// → doctor warns about it.
func TestDoctorWarnsOnStaleIndex(t *testing.T) {
	root := bootstrapProject(t)
	if err := os.WriteFile(filepath.Join(root, "wiki", "dangling.md"),
		[]byte("---\ntitle: Dangling\ntype: concept\ncreated: \"2026-04-16\"\nupdated: \"2026-04-16\"\n---\n\nBody.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = swapLLMClient(t, nil)

	var out string
	withProjectDir(t, root, func() {
		out, _ = captureStdout(t, func() error { return runDoctor(context.Background()) })
	})
	if !strings.Contains(out, "Index:") {
		t.Fatalf("output missing Index row; body:\n%s", out)
	}
	if !strings.Contains(out, "missing") {
		t.Errorf("expected stale-index warning; body:\n%s", out)
	}
}
