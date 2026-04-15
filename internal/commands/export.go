package commands

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	gmhtml "github.com/yuin/goldmark/renderer/html"

	"github.com/ugurcan-aytar/anvil/internal/engine"
	"github.com/ugurcan-aytar/anvil/internal/wiki"
)

// exportOptions are the `anvil export` flags.
type exportOptions struct {
	Output string
	Title  string
}

var exportOpts exportOptions

var exportCmd = &cobra.Command{
	Use:   "export",
	Short: "Export wiki as a standalone static HTML site",
	Long: `anvil export converts every wiki/ page to HTML, resolves
[[wikilink]] references to intra-site <a href="slug.html"> links,
and drops a minimal stylesheet alongside. No external dependencies —
open index.html in a browser and everything works.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runExport(cmd.Context(), exportOpts)
	},
}

func init() {
	exportCmd.Flags().StringVarP(&exportOpts.Output, "output", "o", "./anvil-export",
		"output directory")
	exportCmd.Flags().StringVar(&exportOpts.Title, "title", "",
		"site title (default: project directory name)")
}

// mdRenderer is the goldmark instance shared across every page.
// GFM enables tables, strikethrough, task lists, and autolinks.
// WithUnsafe lets our pre-substituted <a> / <span class="missing">
// tags survive — the body is anvil's own markdown, never arbitrary
// user input, so HTML injection isn't a concern.
var mdRenderer = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithRendererOptions(gmhtml.WithUnsafe()),
)

func runExport(ctx context.Context, opts exportOptions) error {
	_ = ctx
	eng, err := engine.Open(projectDir)
	if err != nil {
		return err
	}
	defer eng.Close()

	pages, err := wiki.ListPages(eng.WikiDir())
	if err != nil {
		return fmt.Errorf("list pages: %w", err)
	}

	outDir := opts.Output
	if outDir == "" {
		outDir = "./anvil-export"
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", outDir, err)
	}

	title := opts.Title
	if title == "" {
		title = filepath.Base(eng.ProjectRoot())
	}

	// Build the slug set so [[wikilinks]] can point at files that
	// actually exist. Missing targets render as span.missing so the
	// reader sees the dangling ref without a broken <a>.
	known := map[string]struct{}{}
	for _, p := range pages {
		known[strings.TrimSuffix(p.Filename, ".md")] = struct{}{}
	}

	// Write each page.
	written := 0
	for _, p := range pages {
		stem := strings.TrimSuffix(p.Filename, ".md")
		outPath := filepath.Join(outDir, stem+".html")
		if err := writeExportPage(outPath, p, title, known); err != nil {
			return fmt.Errorf("write %s: %w", outPath, err)
		}
		written++
	}

	// Index + stylesheet.
	if err := writeExportIndex(filepath.Join(outDir, "index.html"), title, pages); err != nil {
		return fmt.Errorf("write index.html: %w", err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "style.css"), []byte(exportCSS), 0o644); err != nil {
		return fmt.Errorf("write style.css: %w", err)
	}

	fmt.Printf("Exported %d page(s) → %s/\n", written, outDir)
	return nil
}

// writeExportPage renders one wiki.Page to an HTML file. The body
// is resolved for wikilinks BEFORE it hits goldmark so the markdown
// renderer sees a plain <a> instead of "[[...]]".
func writeExportPage(outPath string, p *wiki.Page, siteTitle string, known map[string]struct{}) error {
	bodyHTML, err := renderBodyHTML(p.Body, known)
	if err != nil {
		return err
	}
	meta := renderPageMeta(p)
	htmlBody := pageTemplate(siteTitle, p.Title, meta, bodyHTML)
	return os.WriteFile(outPath, []byte(htmlBody), 0o644)
}

// renderBodyHTML swaps every [[target]] with an <a href="target.html">
// (or a span.missing for dangling refs), then runs the result through
// goldmark.
func renderBodyHTML(body string, known map[string]struct{}) (string, error) {
	resolved := wiki.WikilinkRegexp().ReplaceAllStringFunc(body, func(match string) string {
		inner := strings.TrimPrefix(match, "[[")
		inner = strings.TrimSuffix(inner, "]]")
		// Pipe form "target|display" — split, keep both.
		target, display := inner, inner
		if i := strings.Index(inner, "|"); i >= 0 {
			target = strings.TrimSpace(inner[:i])
			display = strings.TrimSpace(inner[i+1:])
		}
		stem := strings.TrimSuffix(target, ".md")
		if _, ok := known[stem]; !ok {
			return fmt.Sprintf(`<span class="missing">%s</span>`, html.EscapeString(display))
		}
		return fmt.Sprintf(`<a href="%s.html">%s</a>`, html.EscapeString(stem), html.EscapeString(display))
	})

	var buf bytes.Buffer
	if err := mdRenderer.Convert([]byte(resolved), &buf); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// renderPageMeta produces the small metadata block shown below the
// page title (type, sources, related, updated). Empty fields are
// skipped so the block stays tidy for minimal pages.
func renderPageMeta(p *wiki.Page) string {
	var b strings.Builder
	b.WriteString(`<dl class="meta">`)
	if p.Type != "" {
		fmt.Fprintf(&b, `<dt>type</dt><dd>%s</dd>`, html.EscapeString(p.Type))
	}
	if p.Updated != "" {
		fmt.Fprintf(&b, `<dt>updated</dt><dd>%s</dd>`, html.EscapeString(p.Updated))
	}
	if len(p.Sources) > 0 {
		fmt.Fprintf(&b, `<dt>sources</dt><dd>%s</dd>`,
			html.EscapeString(strings.Join(p.Sources, ", ")))
	}
	if len(p.Related) > 0 {
		var links []string
		for _, r := range p.Related {
			stem := strings.TrimSuffix(r, ".md")
			links = append(links,
				fmt.Sprintf(`<a href="%s.html">%s</a>`,
					html.EscapeString(stem), html.EscapeString(stem)))
		}
		fmt.Fprintf(&b, `<dt>related</dt><dd>%s</dd>`, strings.Join(links, ", "))
	}
	b.WriteString(`</dl>`)
	return b.String()
}

// writeExportIndex produces index.html — a table of every page with
// title, type, and TLDR. Uses the wiki package's TLDR heuristic so
// the HTML index matches what wiki/index.md shows.
func writeExportIndex(outPath, siteTitle string, pages []*wiki.Page) error {
	sort.Slice(pages, func(i, j int) bool { return pages[i].Filename < pages[j].Filename })

	var body strings.Builder
	body.WriteString(`<h1>` + html.EscapeString(siteTitle) + `</h1>`)
	body.WriteString(`<p class="meta">` + fmt.Sprintf("%d pages", len(pages)) + `</p>`)
	body.WriteString(`<table class="index"><thead><tr><th>Page</th><th>Type</th><th>TLDR</th></tr></thead><tbody>`)
	for _, p := range pages {
		stem := strings.TrimSuffix(p.Filename, ".md")
		title := p.Title
		if title == "" {
			title = stem
		}
		tldr := firstSentenceForIndex(p.Body)
		fmt.Fprintf(&body,
			`<tr><td><a href="%s.html">%s</a></td><td>%s</td><td>%s</td></tr>`,
			html.EscapeString(stem),
			html.EscapeString(title),
			html.EscapeString(p.Type),
			html.EscapeString(tldr))
	}
	body.WriteString(`</tbody></table>`)

	htmlOut := pageTemplate(siteTitle, siteTitle, "", body.String())
	return os.WriteFile(outPath, []byte(htmlOut), 0o644)
}

// firstSentenceForIndex is the TLDR heuristic for index.html —
// trims leading headings + returns the first sentence up to
// ~160 chars. Intentionally simpler than the wiki package's
// version; we don't care about a markdown-perfect preview.
func firstSentenceForIndex(body string) string {
	body = strings.TrimSpace(body)
	for strings.HasPrefix(body, "#") {
		if nl := strings.IndexByte(body, '\n'); nl >= 0 {
			body = strings.TrimSpace(body[nl+1:])
		} else {
			return ""
		}
	}
	end := len(body)
	for i := 0; i < len(body); i++ {
		c := body[i]
		if (c == '.' || c == '!' || c == '?') && (i+1 >= len(body) || body[i+1] == ' ' || body[i+1] == '\n') {
			end = i + 1
			break
		}
	}
	out := strings.TrimSpace(body[:end])
	if n := 160; len([]rune(out)) > n {
		out = string([]rune(out)[:n-1]) + "…"
	}
	return out
}

// pageTemplate wraps rendered HTML in the shared page chrome.
// Inline link to the stylesheet so `file://` opens work without a
// base-href fuss.
func pageTemplate(siteTitle, pageTitle, metaBlock, body string) string {
	return `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>` + html.EscapeString(pageTitle) + ` · ` + html.EscapeString(siteTitle) + `</title>
<link rel="stylesheet" href="style.css">
</head>
<body>
<nav><a href="index.html">` + html.EscapeString(siteTitle) + `</a></nav>
<main>
<h1>` + html.EscapeString(pageTitle) + `</h1>
` + metaBlock + `
<section class="body">
` + body + `
</section>
</main>
</body>
</html>
`
}

// exportCSS is the minimal stylesheet shipped alongside every
// export. Light theme, readable typography, 720px max width.
const exportCSS = `body {
  font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial, sans-serif;
  max-width: 720px;
  margin: 2rem auto;
  padding: 0 1rem;
  color: #1a1a1a;
  line-height: 1.6;
  background: #fefefe;
}
nav { font-size: 0.9em; margin-bottom: 2rem; }
nav a { color: #555; text-decoration: none; }
nav a:hover { text-decoration: underline; }
h1 { border-bottom: 2px solid #1a1a1a; padding-bottom: 0.3em; }
h2 { margin-top: 2em; }
a { color: #0366d6; }
a.missing, span.missing { color: #cb2431; text-decoration: line-through; }
dl.meta { font-size: 0.85em; color: #666; margin: 0 0 1.5em 0; padding: 0.5em 1em; background: #f5f5f5; border-left: 3px solid #0366d6; }
dl.meta dt { float: left; clear: left; width: 5em; font-weight: 600; }
dl.meta dd { margin-left: 6em; }
table.index { border-collapse: collapse; width: 100%; }
table.index th, table.index td { border-bottom: 1px solid #e1e1e1; padding: 0.5em; text-align: left; }
table.index th { background: #f5f5f5; }
code { background: #f0f0f0; padding: 0.1em 0.3em; border-radius: 3px; }
pre { background: #f5f5f5; padding: 1em; overflow-x: auto; border-radius: 4px; }
pre code { background: none; padding: 0; }
blockquote { border-left: 4px solid #ddd; margin: 0; padding: 0 1em; color: #555; }
`
