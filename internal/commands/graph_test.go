package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGraphProducesHTMLAndData: seeds a small wiki, runs graph
// with --no-open, and verifies the HTML file carries the expected
// JSON data blob.
func TestGraphProducesHTMLAndData(t *testing.T) {
	root := bootstrapProject(t)
	w := filepath.Join(root, "wiki")
	writePageWithSources(t, w, "hub.md", "Everything links here.", "raw/s.md")
	writePageWithSources(t, w, "leaf.md", "Points to [[hub]] and [[phantom]].", "raw/s.md")

	outPath := filepath.Join(root, "graph.html")
	withProjectDir(t, root, func() {
		if _, err := captureStdout(t, func() error {
			return runGraph(context.Background(), graphOptions{Output: outPath, NoOpen: true})
		}); err != nil {
			t.Fatalf("runGraph: %v", err)
		}
	})
	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("graph HTML missing: %v", err)
	}
	body := string(raw)
	for _, want := range []string{
		"d3@7",
		`"nodes":`,
		`"links":`,
		`"hub"`,
		`"leaf"`,
		`"phantom"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("graph HTML missing %q", want)
		}
	}
}

// TestCollectGraphDataShapes: asserts the node / link counts and
// that a missing-page target produces a missing node, not a real
// one.
func TestCollectGraphDataShapes(t *testing.T) {
	root := bootstrapProject(t)
	w := filepath.Join(root, "wiki")
	writePageWithSources(t, w, "a.md", "Goes to [[b]] and [[phantom]].", "raw/s.md")
	writePageWithSources(t, w, "b.md", "Body.", "raw/s.md")

	var raw []byte
	var outPath = filepath.Join(root, "graph.html")
	withProjectDir(t, root, func() {
		if _, err := captureStdout(t, func() error {
			return runGraph(context.Background(), graphOptions{Output: outPath, NoOpen: true})
		}); err != nil {
			t.Fatal(err)
		}
	})
	raw, _ = os.ReadFile(outPath)

	// Pull out the data blob: it sits on a `const data = {...};` line.
	idx := strings.Index(string(raw), "const data = ")
	if idx < 0 {
		t.Fatalf("data blob missing")
	}
	blob := string(raw[idx+len("const data = "):])
	end := strings.Index(blob, ";")
	if end < 0 {
		t.Fatalf("data blob unterminated")
	}
	var data graphData
	if err := json.Unmarshal([]byte(blob[:end]), &data); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	// 2 real + 1 missing = 3 nodes.
	if len(data.Nodes) != 3 {
		t.Errorf("node count = %d, want 3 (%+v)", len(data.Nodes), data.Nodes)
	}
	var missingFound bool
	for _, n := range data.Nodes {
		if n.ID == "phantom" && n.Missing {
			missingFound = true
		}
	}
	if !missingFound {
		t.Errorf("phantom should appear as a missing node; nodes=%+v", data.Nodes)
	}
	// 2 links: a→b, a→phantom.
	if len(data.Links) != 2 {
		t.Errorf("link count = %d, want 2 (%+v)", len(data.Links), data.Links)
	}
}
