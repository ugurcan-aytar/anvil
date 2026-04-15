package lint

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/ugurcan-aytar/anvil/internal/wiki"
)

// EmptyBodyThreshold is the trimmed-rune count below which a page is
// considered "empty" — body-wise. 50 runes covers a single short
// paragraph and is a reasonable signal that an ingest stub or a
// manually-created page never got real content.
const EmptyBodyThreshold = 50

// CheckOrphans returns pages that exist on disk but nothing links to.
// Thin wrapper over wiki.BuildGraph's Orphans — kept separate so the
// lint orchestrator can call it without importing wiki at multiple
// layers.
func CheckOrphans(wikiDir string) ([]string, error) {
	g, err := wiki.BuildGraph(wikiDir)
	if err != nil {
		return nil, fmt.Errorf("build graph: %w", err)
	}
	return g.Orphans(), nil
}

// CheckMissingPages returns [[wikilink]] targets that nobody can
// open because no file backs them. These are either typos ("[[cb]]"
// instead of "[[circuit-breaker]]") or the topics the LLM knew about
// but anvil hasn't yet ingested a source for.
func CheckMissingPages(wikiDir string) ([]string, error) {
	g, err := wiki.BuildGraph(wikiDir)
	if err != nil {
		return nil, fmt.Errorf("build graph: %w", err)
	}
	return g.MissingPages(), nil
}

// CheckBrokenLinks returns every reference (body wikilink OR
// frontmatter `related:` entry) whose target page doesn't exist.
// Contains more detail than CheckMissingPages — callers learn which
// source page hosts each dangling reference.
//
// The same (source → target) can appear twice in the output when it
// lives in both the body and the frontmatter; that's deliberate so
// the `anvil lint` UI can point the user at both occurrences.
func CheckBrokenLinks(wikiDir string) ([]BrokenLink, error) {
	pages, err := wiki.ListPages(wikiDir)
	if err != nil {
		return nil, fmt.Errorf("list pages: %w", err)
	}
	known := map[string]struct{}{}
	for _, p := range pages {
		known[stemOf(p.Filename)] = struct{}{}
	}

	var broken []BrokenLink
	for _, p := range pages {
		src := stemOf(p.Filename)
		for _, target := range wiki.ExtractWikilinks(p.Body) {
			stem := stemOf(target)
			if _, ok := known[stem]; ok {
				continue
			}
			broken = append(broken, BrokenLink{
				SourcePage: src,
				Target:     stem,
				Location:   "body",
			})
		}
		for _, rel := range p.Related {
			stem := stemOf(rel)
			if _, ok := known[stem]; ok {
				continue
			}
			broken = append(broken, BrokenLink{
				SourcePage: src,
				Target:     stem,
				Location:   "frontmatter",
			})
		}
	}
	sort.Slice(broken, func(i, j int) bool {
		if broken[i].SourcePage != broken[j].SourcePage {
			return broken[i].SourcePage < broken[j].SourcePage
		}
		if broken[i].Target != broken[j].Target {
			return broken[i].Target < broken[j].Target
		}
		return broken[i].Location < broken[j].Location
	})
	return broken, nil
}

// CheckEmptyPages returns pages whose body (frontmatter stripped,
// leading/trailing whitespace trimmed) is below EmptyBodyThreshold
// runes. Usually indicates a stubbed page that never received a
// follow-up ingest.
func CheckEmptyPages(wikiDir string) ([]string, error) {
	pages, err := wiki.ListPages(wikiDir)
	if err != nil {
		return nil, fmt.Errorf("list pages: %w", err)
	}
	var empty []string
	for _, p := range pages {
		body := strings.TrimSpace(p.Body)
		if utf8.RuneCountInString(body) < EmptyBodyThreshold {
			empty = append(empty, stemOf(p.Filename))
		}
	}
	sort.Strings(empty)
	return empty, nil
}

// CheckStaleIndex returns pages that exist on disk but don't appear
// as [[stem]] in wiki/index.md. A missing index entry isn't broken
// exactly, but it means the page won't show up when the user
// browses the table of contents — so lint flags it, and --fix
// rebuilds the whole index.
func CheckStaleIndex(wikiDir string) ([]string, error) {
	pages, err := wiki.ListPages(wikiDir)
	if err != nil {
		return nil, fmt.Errorf("list pages: %w", err)
	}
	indexPath := filepath.Join(wikiDir, wiki.IndexFilename)
	raw, err := os.ReadFile(indexPath)
	if err != nil {
		if os.IsNotExist(err) {
			// No index at all: every page is effectively stale.
			// Treat that as a special case rather than silently
			// returning zero results.
			stems := make([]string, 0, len(pages))
			for _, p := range pages {
				stems = append(stems, stemOf(p.Filename))
			}
			sort.Strings(stems)
			return stems, nil
		}
		return nil, fmt.Errorf("read index: %w", err)
	}

	indexBody := string(raw)
	var missing []string
	for _, p := range pages {
		stem := stemOf(p.Filename)
		needle := "[[" + stem + "]]"
		if !strings.Contains(indexBody, needle) {
			missing = append(missing, stem)
		}
	}
	sort.Strings(missing)
	return missing, nil
}

// stemOf strips ".md" from a filename (no-op if the suffix isn't
// present). Used everywhere the lint code prefers the stem form
// for display consistency with the [[wikilink]] syntax.
func stemOf(filename string) string {
	return strings.TrimSuffix(filename, ".md")
}
