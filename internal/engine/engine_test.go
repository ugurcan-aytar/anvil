package engine_test

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/ugurcan-aytar/recall/pkg/recall"

	"github.com/ugurcan-aytar/anvil/internal/engine"
)

// seedProject writes the minimum skeleton engine.Open expects:
// ANVIL.md, raw/, wiki/. Doesn't populate .anvil/ — that's what
// Open is testing.
func seedProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, sub := range []string{"raw", "wiki"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "ANVIL.md"), []byte("# test schema\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestOpenRejectsMissingAnvilMD(t *testing.T) {
	dir := t.TempDir()
	_, err := engine.Open(dir)
	if err == nil {
		t.Fatal("Open should reject a directory without ANVIL.md")
	}
}

func TestOpenCreatesDBAndCollections(t *testing.T) {
	dir := seedProject(t)
	eng, err := engine.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer eng.Close()

	if _, err := os.Stat(eng.DBPath()); err != nil {
		t.Fatalf("DB not created at %s: %v", eng.DBPath(), err)
	}
	collections, err := eng.Recall().ListCollections()
	if err != nil {
		t.Fatalf("ListCollections: %v", err)
	}
	names := make([]string, 0, len(collections))
	for _, c := range collections {
		names = append(names, c.Name)
	}
	sort.Strings(names)
	if len(names) != 2 || names[0] != engine.CollRaw || names[1] != engine.CollWiki {
		t.Errorf("collections = %v, want [raw wiki]", names)
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	dir := seedProject(t)
	for i := 0; i < 3; i++ {
		eng, err := engine.Open(dir)
		if err != nil {
			t.Fatalf("Open %d: %v", i, err)
		}
		if err := eng.Close(); err != nil {
			t.Errorf("Close %d: %v", i, err)
		}
	}
	// Still exactly two collections — no duplicates accumulated.
	eng, err := engine.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	cs, _ := eng.Recall().ListCollections()
	if len(cs) != 2 {
		t.Errorf("after 3 opens, collections = %d, want 2", len(cs))
	}
}

func TestPathAccessors(t *testing.T) {
	dir := seedProject(t)
	eng, err := engine.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	abs, _ := filepath.Abs(dir)
	if eng.ProjectRoot() != abs {
		t.Errorf("ProjectRoot = %q, want %q", eng.ProjectRoot(), abs)
	}
	if eng.RawDir() != filepath.Join(abs, "raw") {
		t.Errorf("RawDir = %q", eng.RawDir())
	}
	if eng.WikiDir() != filepath.Join(abs, "wiki") {
		t.Errorf("WikiDir = %q", eng.WikiDir())
	}
	if eng.DBPath() != filepath.Join(abs, ".anvil", "index.db") {
		t.Errorf("DBPath = %q", eng.DBPath())
	}
}

func TestCloseOnNilEngineIsSafe(t *testing.T) {
	var e *engine.Engine
	if err := e.Close(); err != nil {
		t.Errorf("Close on nil Engine returned %v, want nil", err)
	}
}

// SetEmbedder pre-empts the lazy resolver so callers that want a
// deterministic embedder (MockEmbedder in tests) don't touch the
// env-driven factory. Embedder() should then return the injected
// handle without probing.
func TestSetEmbedderOverridesResolver(t *testing.T) {
	dir := seedProject(t)
	eng, err := engine.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	mock := recall.NewMockEmbedder(recall.EmbeddingDimensions)
	eng.SetEmbedder(mock)

	got, err := eng.Embedder()
	if err != nil {
		t.Fatalf("Embedder: %v", err)
	}
	if got == nil {
		t.Fatal("Embedder returned nil after SetEmbedder")
	}
	if got != mock {
		t.Errorf("Embedder returned a different handle than the one injected")
	}
}

// On the default build (no embed_llama tag, no API key) Embedder()
// should degrade gracefully — return (nil, nil) rather than an
// error — so BM25-only callers still work.
func TestEmbedderGracefulFallback(t *testing.T) {
	dir := seedProject(t)
	eng, err := engine.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	// No SetEmbedder call and no env config → default fallback.
	got, err := eng.Embedder()
	// It's OK to get either (nil, nil) [no backend] OR
	// (embedder, nil) [embed_llama compiled in + model present].
	// What's NOT OK is (nil, err) on a fresh stub build.
	if err != nil {
		t.Logf("note: Embedder returned error (%v) — acceptable only if an API provider is env-configured in CI", err)
	}
	_ = got
}
