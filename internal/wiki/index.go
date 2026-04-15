// Wiki index.md generator.
//
// index.md is a one-stop table of every page in wiki/: filename
// (as a wikilink), type, and a one-line TLDR. Rebuilt from the
// current page set, not append-only — the full rebuild is cheap
// (a few k pages max per real project) and keeps the index a pure
// derivative of the page files.
//
// The TLDR is a simple heuristic: first sentence of the body,
// capped at MaxTLDRRunes. Good enough for Phase A1; Phase A2's
// ingest pipeline can overwrite it with an LLM-written summary
// later.

package wiki

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// IndexFilename is the reserved name for the generated index.
const IndexFilename = "index.md"

// MaxTLDRRunes caps the first-sentence heuristic. Long enough for a
// useful preview, short enough to keep the table scannable.
const MaxTLDRRunes = 100

// IndexHeader prefaces the generated index. Kept ASCII so grep +
// piped tooling doesn't need UTF-8 handling.
const IndexHeader = "# Index\n\n"

// IndexEmptyBody is what RebuildIndex writes when there are no pages
// — anvil init seeds this so the file exists from the start.
const IndexEmptyBody = IndexHeader + "No pages yet.\n"

// RebuildIndex scans wikiDir for pages (ListPages skips reserved
// names) and overwrites wiki/index.md with a fresh table. Returns
// early with the empty body when the wiki has no pages — makes
// `anvil init` seeding a one-call no-branch.
func RebuildIndex(wikiDir string) error {
	pages, err := ListPages(wikiDir)
	if err != nil {
		return fmt.Errorf("rebuild index: %w", err)
	}
	body := renderIndex(pages)
	return writeIndex(wikiDir, body)
}

// AddToIndex is the fast path for ingest / save flows that only
// want to append one row. The table row goes just before the final
// newline of the existing index; if the index is empty or missing,
// falls back to RebuildIndex so the caller doesn't have to branch.
//
// tldr is optional — pass "" and AddToIndex derives it from the
// page body using the same first-sentence heuristic as RebuildIndex.
func AddToIndex(wikiDir string, page *Page, tldr string) error {
	if page == nil || page.Filename == "" {
		return fmt.Errorf("add to index: page is nil or missing Filename")
	}
	if tldr == "" {
		tldr = firstSentenceHeuristic(page.Body)
	}
	path := filepath.Join(wikiDir, IndexFilename)
	raw, err := os.ReadFile(path)
	if err != nil || !bytes.Contains(raw, []byte("| Page")) {
		// Either no index yet, or an empty-template index that
		// doesn't have the header row. Full rebuild so the table
		// starts correctly.
		return RebuildIndex(wikiDir)
	}
	// Duplicate-row guard: if the page filename already appears
	// (as a [[wikilink]]), rebuild to update its row rather than
	// append a second.
	needle := []byte("[[" + pageStemFromFilename(page.Filename) + "]]")
	if bytes.Contains(raw, needle) {
		return RebuildIndex(wikiDir)
	}
	row := renderRow(page, tldr) + "\n"
	// Trim trailing whitespace, append the row, one final newline.
	out := bytes.TrimRight(raw, " \t\n")
	out = append(out, '\n')
	out = append(out, []byte(row)...)
	return writeIndexBytes(wikiDir, out)
}

// renderIndex builds the full index body from a page list.
func renderIndex(pages []*Page) string {
	if len(pages) == 0 {
		return IndexEmptyBody
	}
	var b strings.Builder
	b.Grow(200 + 80*len(pages))
	b.WriteString(IndexHeader)
	b.WriteString("| Page | Type | TLDR |\n")
	b.WriteString("|------|------|------|\n")
	for _, p := range pages {
		b.WriteString(renderRow(p, firstSentenceHeuristic(p.Body)))
		b.WriteByte('\n')
	}
	return b.String()
}

// renderRow emits one `| [[stem]] | type | tldr |` line.
func renderRow(p *Page, tldr string) string {
	stem := pageStemFromFilename(p.Filename)
	typeCol := p.Type
	if typeCol == "" {
		typeCol = "—"
	}
	tldrCol := escapeTableCell(tldr)
	if tldrCol == "" {
		tldrCol = "—"
	}
	return fmt.Sprintf("| [[%s]] | %s | %s |", stem, typeCol, tldrCol)
}

// firstSentenceHeuristic returns the first sentence of body, capped
// at MaxTLDRRunes. "Sentence" = text up to the first period, question
// mark, or exclamation point followed by whitespace or end-of-string.
// Strips leading markdown hashes and blank lines.
func firstSentenceHeuristic(body string) string {
	body = strings.TrimSpace(body)
	// Skip a leading H1 / H2 heading — the TLDR should preview
	// content, not repeat the title.
	for strings.HasPrefix(body, "#") {
		if nl := strings.IndexByte(body, '\n'); nl >= 0 {
			body = strings.TrimSpace(body[nl+1:])
		} else {
			return ""
		}
	}
	// Find the first sentence boundary.
	end := -1
	for i := 0; i < len(body); i++ {
		c := body[i]
		if c != '.' && c != '!' && c != '?' {
			continue
		}
		if i+1 >= len(body) || isWhitespace(body[i+1]) {
			end = i + 1
			break
		}
	}
	if end < 0 {
		end = len(body)
	}
	sentence := strings.TrimSpace(body[:end])
	// Cap at MaxTLDRRunes (rune-aware so multi-byte UTF-8 doesn't
	// chop in the middle of a codepoint).
	runes := []rune(sentence)
	if len(runes) > MaxTLDRRunes {
		runes = runes[:MaxTLDRRunes-1]
		return strings.TrimSpace(string(runes)) + "…"
	}
	return sentence
}

// pageStemFromFilename returns the filename minus the .md extension.
// "circuit-breaker-pattern.md" → "circuit-breaker-pattern".
func pageStemFromFilename(fn string) string {
	return strings.TrimSuffix(fn, ".md")
}

// escapeTableCell replaces characters that would break a GFM table
// cell — pipe and newline — with safe equivalents.
func escapeTableCell(s string) string {
	s = strings.ReplaceAll(s, "|", `\|`)
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}

func isWhitespace(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r':
		return true
	}
	return false
}

func writeIndex(wikiDir, body string) error {
	return writeIndexBytes(wikiDir, []byte(body))
}

func writeIndexBytes(wikiDir string, body []byte) error {
	if err := os.MkdirAll(wikiDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", wikiDir, err)
	}
	if !bytes.HasSuffix(body, []byte("\n")) {
		body = append(body, '\n')
	}
	return os.WriteFile(filepath.Join(wikiDir, IndexFilename), body, 0o644)
}
