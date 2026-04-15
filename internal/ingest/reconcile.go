package ingest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ugurcan-aytar/anvil/internal/wiki"
)

// PageDraft is a create-a-new-page intent. The writer stage hands this
// to the LLM's write prompt and persists the result via wiki.WritePage.
type PageDraft struct {
	Slug        string // "circuit-breaker.md" — already kebab-cased + suffixed
	Name        string // "Circuit Breaker" — human title
	Type        string // "entity" or "concept"
	Description string
	Claims      []Claim
	Connections []Connection
	SourcePath  string // raw/<file> — carried into frontmatter + prompt
}

// PageUpdate is a merge-new-info-into-existing-page intent. Existing
// is the current on-disk page; NewInfo is the rendered summary of what
// this ingest adds (description, claims, connections). The writer
// stage feeds Existing + NewInfo to the update prompt.
type PageUpdate struct {
	Slug        string
	Name        string
	Type        string
	Existing    *wiki.Page
	NewInfo     string
	SourcePath  string
	Claims      []Claim
	Connections []Connection
}

// ReconcileResult is what the reconcile stage hands to the writer
// stage. Create and Update are disjoint — any given slug appears in
// exactly one list.
type ReconcileResult struct {
	Create []PageDraft
	Update []PageUpdate
}

// namedItem is the common shape entity + concept iterate through.
// Kept unexported because callers never construct it directly.
type namedItem struct {
	Name        string
	Description string
	Kind        string // "entity" or "concept"
}

// Reconcile diffs extraction against the wiki on disk and decides
// which pages need creating vs updating. Each unique (slug) entry
// appears once — duplicates within the same extraction (e.g. the
// same name in both entities and concepts) are deduped in favour of
// the first occurrence.
//
// sourcePath is the path of the source being ingested; it's carried
// into drafts so the writer can stamp it into frontmatter.
func Reconcile(extraction *Extraction, wikiDir, sourcePath string) (*ReconcileResult, error) {
	if extraction == nil {
		return nil, fmt.Errorf("reconcile: extraction is nil")
	}
	if wikiDir == "" {
		return nil, fmt.Errorf("reconcile: wikiDir is empty")
	}

	items := make([]namedItem, 0, len(extraction.Entities)+len(extraction.Concepts))
	for _, e := range extraction.Entities {
		items = append(items, namedItem{Name: e.Name, Description: e.Description, Kind: "entity"})
	}
	for _, c := range extraction.Concepts {
		items = append(items, namedItem{Name: c.Name, Description: c.Description, Kind: "concept"})
	}

	result := &ReconcileResult{}
	seenSlugs := map[string]struct{}{}

	for _, item := range items {
		if strings.TrimSpace(item.Name) == "" {
			continue
		}
		slug := wiki.SlugFromTitle(item.Name)
		if _, dup := seenSlugs[slug]; dup {
			continue
		}
		seenSlugs[slug] = struct{}{}

		claims := claimsFor(item.Name, extraction.Claims)
		connections := connectionsFor(item.Name, extraction.Connections)

		existing, err := readIfExists(wikiDir, slug)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", slug, err)
		}
		if existing == nil {
			result.Create = append(result.Create, PageDraft{
				Slug:        slug,
				Name:        item.Name,
				Type:        item.Kind,
				Description: item.Description,
				Claims:      claims,
				Connections: connections,
				SourcePath:  sourcePath,
			})
			continue
		}
		result.Update = append(result.Update, PageUpdate{
			Slug:        slug,
			Name:        item.Name,
			Type:        item.Kind,
			Existing:    existing,
			NewInfo:     renderNewInfo(item, claims, connections),
			SourcePath:  sourcePath,
			Claims:      claims,
			Connections: connections,
		})
	}
	return result, nil
}

// readIfExists returns the wiki page at wikiDir/slug if it exists.
// Missing is not an error — callers treat nil as "create fresh".
// Other errors (permissions, malformed frontmatter) do propagate.
func readIfExists(wikiDir, slug string) (*wiki.Page, error) {
	path := filepath.Join(wikiDir, slug)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return wiki.ReadPage(wikiDir, slug)
}

// claimsFor returns the subset of claims that name `target` in their
// Related list. Comparison is case-insensitive on trimmed strings —
// the LLM isn't perfectly consistent about casing.
func claimsFor(target string, all []Claim) []Claim {
	norm := strings.ToLower(strings.TrimSpace(target))
	out := make([]Claim, 0)
	for _, c := range all {
		for _, r := range c.Related {
			if strings.ToLower(strings.TrimSpace(r)) == norm {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

// connectionsFor returns connections where the target appears on
// either side. Same normalisation as claimsFor.
func connectionsFor(target string, all []Connection) []Connection {
	norm := strings.ToLower(strings.TrimSpace(target))
	out := make([]Connection, 0)
	for _, c := range all {
		if strings.ToLower(strings.TrimSpace(c.From)) == norm ||
			strings.ToLower(strings.TrimSpace(c.To)) == norm {
			out = append(out, c)
		}
	}
	return out
}

// renderNewInfo produces the "new facts to merge" summary fed into
// the update prompt. Human-readable bullet lists beat structured data
// here because the LLM is the consumer — it needs prose, not YAML.
func renderNewInfo(item namedItem, claims []Claim, connections []Connection) string {
	var b strings.Builder
	if item.Description != "" {
		fmt.Fprintf(&b, "Summary: %s\n\n", item.Description)
	}
	if len(claims) > 0 {
		b.WriteString("Claims:\n")
		for _, c := range claims {
			fmt.Fprintf(&b, "- %s\n", c.Claim)
		}
		b.WriteByte('\n')
	}
	if len(connections) > 0 {
		b.WriteString("Connections:\n")
		for _, c := range connections {
			rel := c.Relationship
			if rel == "" {
				rel = "related to"
			}
			fmt.Fprintf(&b, "- %s %s %s\n", c.From, rel, c.To)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
