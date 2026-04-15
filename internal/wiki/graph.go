// Wikilink cross-reference graph.
//
// Given a wiki/ directory, BuildGraph walks every page, extracts the
// [[wikilink]] targets from each body, and returns a Graph that
// answers three questions:
//
//   * Orphans()       — pages with zero inbound links.
//   * Backlinks(page) — who links TO this page.
//   * MissingPages()  — wikilink targets that have no backing file.
//
// The graph is a plain in-memory map; building it over a
// few-thousand-page wiki is a handful of milliseconds plus the
// filesystem walk.

package wiki

import (
	"sort"
)

// Graph is the parsed cross-reference structure.
type Graph struct {
	// outgoing maps a page's stem (filename minus .md) to the
	// unique set of stems it links to.
	outgoing map[string]map[string]struct{}
	// incoming is the reverse: stem → set of stems that link to it.
	incoming map[string]map[string]struct{}
	// allPages is the set of stems that actually exist as files,
	// used to separate orphans (exist, no backlinks) from missing
	// pages (referenced, don't exist).
	allPages map[string]struct{}
}

// BuildGraph walks wikiDir and returns a populated Graph.
// ReadPage-level errors (malformed frontmatter, missing file) bubble
// up — the graph wants an authoritative page list.
func BuildGraph(wikiDir string) (*Graph, error) {
	pages, err := ListPages(wikiDir)
	if err != nil {
		return nil, err
	}
	g := &Graph{
		outgoing: map[string]map[string]struct{}{},
		incoming: map[string]map[string]struct{}{},
		allPages: map[string]struct{}{},
	}
	for _, p := range pages {
		src := pageStemFromFilename(p.Filename)
		g.allPages[src] = struct{}{}
		if _, ok := g.outgoing[src]; !ok {
			g.outgoing[src] = map[string]struct{}{}
		}
		for _, target := range ExtractWikilinks(p.Body) {
			tgt := pageStemFromFilename(target) // tolerate "[[x.md]]"
			g.outgoing[src][tgt] = struct{}{}
			if _, ok := g.incoming[tgt]; !ok {
				g.incoming[tgt] = map[string]struct{}{}
			}
			g.incoming[tgt][src] = struct{}{}
		}
	}
	return g, nil
}

// Orphans returns pages that exist as files but have zero inbound
// links. Sorted for deterministic output. Pages referenced only by
// themselves count as orphans too (self-links aren't inbound from
// another page).
func (g *Graph) Orphans() []string {
	if g == nil {
		return nil
	}
	out := make([]string, 0)
	for page := range g.allPages {
		inbound := g.incoming[page]
		// Filter out self-links for the orphan check.
		hasExternal := false
		for src := range inbound {
			if src != page {
				hasExternal = true
				break
			}
		}
		if !hasExternal {
			out = append(out, page)
		}
	}
	sort.Strings(out)
	return out
}

// Backlinks returns the pages that link to `page` (stem or full
// filename accepted). Sorted, deduped, excludes self-links.
func (g *Graph) Backlinks(page string) []string {
	if g == nil {
		return nil
	}
	page = pageStemFromFilename(page)
	inbound := g.incoming[page]
	out := make([]string, 0, len(inbound))
	for src := range inbound {
		if src == page {
			continue
		}
		out = append(out, src)
	}
	sort.Strings(out)
	return out
}

// MissingPages returns [[wikilink]] targets that appear in page
// bodies but don't correspond to an actual file. These are the
// "phantom" entries a wiki grows as it references things it hasn't
// gotten around to documenting. Sorted for stable lint output.
func (g *Graph) MissingPages() []string {
	if g == nil {
		return nil
	}
	out := make([]string, 0)
	for target := range g.incoming {
		if _, exists := g.allPages[target]; !exists {
			out = append(out, target)
		}
	}
	sort.Strings(out)
	return out
}

// PageCount returns the number of real (filesystem-backed) pages
// in the graph. Useful for `anvil status` numerics.
func (g *Graph) PageCount() int {
	if g == nil {
		return 0
	}
	return len(g.allPages)
}
