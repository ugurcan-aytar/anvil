// Package wiki is anvil's on-disk wiki CRUD surface.
//
// A wiki/ directory is a flat pile of markdown files. Every file has a
// YAML frontmatter block delimited by "---" lines, followed by a
// markdown body. Frontmatter carries the page's identity (title, type)
// and its references (sources, related pages, timestamps). The body is
// opaque prose with embedded [[wikilink]] cross-references.
//
// Two files are reserved: index.md (auto-generated; see index.go) and
// log.md (append-only; see log.go). ListPages and Graph skip both by
// name.
package wiki

import (
	"bufio"
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ReservedFilenames is the set of wiki files that are not pages. Page
// enumeration and graph building skip them; writing a page under these
// names is rejected.
var ReservedFilenames = map[string]struct{}{
	"index.md": {},
	"log.md":   {},
}

// ValidPageTypes mirrors ANVIL.md's schema section. Callers that want
// to validate page.Type against this set can — but Page itself accepts
// any string so the schema can evolve without the parser rejecting
// user-authored pages.
var ValidPageTypes = []string{"source", "concept", "entity", "synthesis"}

// Page is a wiki markdown file's structured view. Frontmatter fields
// are YAML-tagged; Body + Filename are internal and survive a
// round-trip write/read unchanged.
type Page struct {
	Title   string   `yaml:"title"`
	Type    string   `yaml:"type"`
	Sources []string `yaml:"sources,omitempty"`
	Related []string `yaml:"related,omitempty"`
	Created string   `yaml:"created,omitempty"`
	Updated string   `yaml:"updated,omitempty"`

	// Body is the markdown content below the closing `---`. No
	// frontmatter bytes appear here — the serializer re-attaches
	// the frontmatter block on write.
	Body string `yaml:"-"`

	// Filename is the kebab-case.md leaf name (no directory). Set
	// on read; ignored on write (the caller supplies the name).
	Filename string `yaml:"-"`
}

// frontmatterDelim is the "---\n" separator between the YAML block
// and the body. Standard in Obsidian, Jekyll, Hugo; anvil matches.
const frontmatterDelim = "---"

// ReadPage parses a wiki markdown file. Returns an error if the file
// is missing, if the frontmatter is malformed, or if the file is a
// reserved name (index.md / log.md — those aren't pages).
func ReadPage(wikiDir, filename string) (*Page, error) {
	if _, reserved := ReservedFilenames[filename]; reserved {
		return nil, fmt.Errorf("read page %s: reserved filename (not a page)", filename)
	}
	path := filepath.Join(wikiDir, filename)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	p, err := parsePage(raw)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	p.Filename = filename
	return p, nil
}

// WritePage serialises p to wiki/<filename>. Filename must be
// kebab-case and end in .md; any absolute path or ..-containing input
// is rejected to stop a malformed Page from escaping the wiki dir.
// Reserved filenames (index.md / log.md) are also rejected — those
// have their own writers in index.go / log.go.
func WritePage(wikiDir string, page *Page) error {
	if page == nil {
		return fmt.Errorf("write page: nil")
	}
	name := page.Filename
	if name == "" {
		return fmt.Errorf("write page: Filename is empty")
	}
	if _, reserved := ReservedFilenames[name]; reserved {
		return fmt.Errorf("write page: %q is reserved (use index.go / log.go)", name)
	}
	if strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
		return fmt.Errorf("write page: filename %q contains path separators or '..'", name)
	}
	if !strings.HasSuffix(name, ".md") {
		return fmt.Errorf("write page: filename %q must end in .md", name)
	}
	if err := os.MkdirAll(wikiDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", wikiDir, err)
	}
	buf, err := serializePage(page)
	if err != nil {
		return err
	}
	path := filepath.Join(wikiDir, name)
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// ListPages enumerates every .md file under wikiDir that isn't a
// reserved filename. Results are sorted by filename so callers see a
// stable order across runs.
func ListPages(wikiDir string) ([]*Page, error) {
	entries, err := os.ReadDir(wikiDir)
	if err != nil {
		if errorsIsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read wiki dir %s: %w", wikiDir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		if _, reserved := ReservedFilenames[e.Name()]; reserved {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	pages := make([]*Page, 0, len(names))
	for _, n := range names {
		p, err := ReadPage(wikiDir, n)
		if err != nil {
			return nil, err
		}
		pages = append(pages, p)
	}
	return pages, nil
}

// DeletePage removes a page from the wiki directory. Does not touch
// index.md or log.md — callers that want to reflect the deletion
// there should rebuild the index and append a log entry.
func DeletePage(wikiDir, filename string) error {
	if _, reserved := ReservedFilenames[filename]; reserved {
		return fmt.Errorf("delete page: %q is reserved", filename)
	}
	path := filepath.Join(wikiDir, filename)
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("delete %s: %w", path, err)
	}
	return nil
}

// SlugFromTitle converts a human title like "Circuit Breaker Pattern"
// into a kebab-case filename like "circuit-breaker-pattern.md".
// Non-alphanumeric runs collapse into a single "-"; leading / trailing
// dashes are trimmed. The "-pattern" suffix in the example isn't
// preserved specially — it's just a word like any other.
func SlugFromTitle(title string) string {
	var b strings.Builder
	b.Grow(len(title) + 3)
	lastWasDash := true // start at true so leading punctuation doesn't emit a dash
	for _, r := range strings.ToLower(title) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastWasDash = false
		default:
			if !lastWasDash {
				b.WriteByte('-')
				lastWasDash = true
			}
		}
	}
	s := strings.TrimRight(b.String(), "-")
	if s == "" {
		// Fall back to a timestamp so callers always get a valid
		// filename. "Untitled" would collide across calls.
		s = fmt.Sprintf("page-%d", time.Now().Unix())
	}
	return s + ".md"
}

// wikilinkRE matches [[target]] or [[target|display text]] — the
// "target" portion is what the graph edge points at. Nested brackets
// and escaped brackets aren't supported; anvil's write path never
// emits them.
var wikilinkRE = regexp.MustCompile(`\[\[([^\[\]|]+)(?:\|[^\[\]]*)?\]\]`)

// ExtractWikilinks returns a deduplicated slice of wikilink targets
// found in body. Each target is the raw text between the double
// brackets (pipe-aliased links strip the display portion). Results
// keep the first-appearance order so graph output is stable.
func ExtractWikilinks(body string) []string {
	matches := wikilinkRE.FindAllStringSubmatch(body, -1)
	seen := map[string]struct{}{}
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		target := strings.TrimSpace(m[1])
		if target == "" {
			continue
		}
		if _, ok := seen[target]; ok {
			continue
		}
		seen[target] = struct{}{}
		out = append(out, target)
	}
	return out
}

// parsePage splits raw bytes into frontmatter + body. Pages without a
// frontmatter block parse as Page{Body: string(raw)} — callers that
// need frontmatter should check the zero value on Title / Type.
func parsePage(raw []byte) (*Page, error) {
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)

	// A file with no leading frontmatter delimiter is treated as
	// body-only. Rewind and stash the whole thing as body.
	if !startsWithDelim(raw) {
		return &Page{Body: string(raw)}, nil
	}

	var fmLines []string
	var bodyLines []string
	inFrontmatter := false
	closed := false
	lineNum := 0
	for sc.Scan() {
		lineNum++
		line := sc.Text()
		if lineNum == 1 && line == frontmatterDelim {
			inFrontmatter = true
			continue
		}
		if inFrontmatter && line == frontmatterDelim {
			inFrontmatter = false
			closed = true
			continue
		}
		if inFrontmatter {
			fmLines = append(fmLines, line)
			continue
		}
		bodyLines = append(bodyLines, line)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if !closed {
		return nil, fmt.Errorf("unterminated frontmatter (expected a closing %q)", frontmatterDelim)
	}
	var p Page
	if len(fmLines) > 0 {
		if err := yaml.Unmarshal([]byte(strings.Join(fmLines, "\n")), &p); err != nil {
			return nil, fmt.Errorf("frontmatter YAML: %w", err)
		}
	}
	// Skip one blank separator line the serializer writes between
	// the closing --- and the body — without this, every round
	// trip prepends a "\n" to the body.
	if len(bodyLines) > 0 && bodyLines[0] == "" {
		bodyLines = bodyLines[1:]
	}
	// Preserve a single trailing newline on the body if present in
	// the source; otherwise keep it free of it so Go string compares
	// in tests stay obvious.
	p.Body = strings.Join(bodyLines, "\n")
	if len(bodyLines) > 0 && bytes.HasSuffix(raw, []byte("\n")) {
		p.Body += "\n"
	}
	return &p, nil
}

// serializePage emits frontmatter + body in the on-disk shape. Always
// writes a frontmatter block so the round-trip reader recognises the
// file as a page; empty omitempty fields keep the block tidy.
func serializePage(p *Page) ([]byte, error) {
	yb, err := yaml.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("marshal frontmatter: %w", err)
	}
	var out bytes.Buffer
	out.WriteString(frontmatterDelim)
	out.WriteByte('\n')
	out.Write(yb)
	out.WriteString(frontmatterDelim)
	out.WriteByte('\n')
	if p.Body != "" {
		if !strings.HasPrefix(p.Body, "\n") {
			out.WriteByte('\n')
		}
		out.WriteString(p.Body)
		if !strings.HasSuffix(p.Body, "\n") {
			out.WriteByte('\n')
		}
	}
	return out.Bytes(), nil
}

func startsWithDelim(raw []byte) bool {
	// Matches "---\n" or "---\r\n" at offset 0 to play nice with
	// Windows-origin files even though anvil targets macOS + Linux.
	if len(raw) < 4 {
		return false
	}
	if !bytes.HasPrefix(raw, []byte(frontmatterDelim)) {
		return false
	}
	switch raw[3] {
	case '\n':
		return true
	case '\r':
		return len(raw) >= 5 && raw[4] == '\n'
	}
	return false
}

// errorsIsNotExist is a thin shim so we don't import "errors" just
// for the one check. Keeps the import list readable.
func errorsIsNotExist(err error) bool {
	var pe *fs.PathError
	if err == nil {
		return false
	}
	if err == fs.ErrNotExist {
		return true
	}
	return os.IsNotExist(err) || (pe != nil && os.IsNotExist(pe.Err))
}
