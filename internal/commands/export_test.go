package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ugurcan-aytar/anvil/internal/wiki"
)

// TestExportProducesHTMLFiles: run export against a seeded wiki,
// verify one .html per page plus index.html + style.css.
func TestExportProducesHTMLFiles(t *testing.T) {
	root := bootstrapProject(t)
	w := filepath.Join(root, "wiki")
	writePageWithSources(t, w, "circuit-breaker.md",
		"A circuit breaker stops cascading failures. See [[retry-pattern]].", "raw/book.md")
	writePageWithSources(t, w, "retry-pattern.md",
		"Retries fail safely when the remote is flaky.", "raw/book.md")

	outDir := filepath.Join(root, "site")
	var err error
	withProjectDir(t, root, func() {
		_, err = captureStdout(t, func() error {
			return runExport(context.Background(), exportOptions{Output: outDir, Title: "Test Wiki"})
		})
	})
	if err != nil {
		t.Fatalf("runExport: %v", err)
	}

	// Every page + index + css.
	for _, f := range []string{
		"circuit-breaker.html",
		"retry-pattern.html",
		"index.html",
		"style.css",
	} {
		if _, err := os.Stat(filepath.Join(outDir, f)); err != nil {
			t.Errorf("missing %s: %v", f, err)
		}
	}

	// Wikilink resolution: retry-pattern reference in circuit-breaker
	// should render as <a href="retry-pattern.html">retry-pattern</a>.
	raw, _ := os.ReadFile(filepath.Join(outDir, "circuit-breaker.html"))
	if !strings.Contains(string(raw), `<a href="retry-pattern.html">retry-pattern</a>`) {
		t.Errorf("wikilink not resolved to <a>; body:\n%s", raw)
	}
	if !strings.Contains(string(raw), "Test Wiki") {
		t.Errorf("site title missing; body:\n%s", raw)
	}
}

// TestExportMissingWikilinkRendersSpanMissing: references to pages
// that don't exist become <span class="missing">...</span> so the
// reader can see the dangling ref visually.
func TestExportMissingWikilinkRendersSpanMissing(t *testing.T) {
	root := bootstrapProject(t)
	w := filepath.Join(root, "wiki")
	writePageWithSources(t, w, "linker.md",
		"This mentions [[no-such-page]].", "raw/s.md")

	outDir := filepath.Join(root, "site")
	withProjectDir(t, root, func() {
		captureStdout(t, func() error {
			return runExport(context.Background(), exportOptions{Output: outDir})
		})
	})
	raw, _ := os.ReadFile(filepath.Join(outDir, "linker.html"))
	if !strings.Contains(string(raw), `<span class="missing">no-such-page</span>`) {
		t.Errorf("missing-page reference should render span.missing; body:\n%s", raw)
	}
}

// TestExportIndexListsEveryPage: index.html carries a row per page,
// each linked to its .html file.
func TestExportIndexListsEveryPage(t *testing.T) {
	root := bootstrapProject(t)
	w := filepath.Join(root, "wiki")
	for _, name := range []string{"alpha.md", "beta.md", "gamma.md"} {
		p := &wiki.Page{
			Filename: name,
			Title:    strings.TrimSuffix(name, ".md"),
			Type:     "concept",
			Body:     "Body for " + name + ".",
			Created:  "2026-04-16",
			Updated:  "2026-04-16",
		}
		if err := wiki.WritePage(w, p); err != nil {
			t.Fatal(err)
		}
	}
	outDir := filepath.Join(root, "site")
	withProjectDir(t, root, func() {
		captureStdout(t, func() error {
			return runExport(context.Background(), exportOptions{Output: outDir})
		})
	})
	raw, _ := os.ReadFile(filepath.Join(outDir, "index.html"))
	for _, want := range []string{
		`<a href="alpha.html">alpha</a>`,
		`<a href="beta.html">beta</a>`,
		`<a href="gamma.html">gamma</a>`,
		"3 pages",
	} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("index missing %q; body:\n%s", want, raw)
		}
	}
}
