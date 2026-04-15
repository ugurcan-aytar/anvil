// Package query is anvil's wiki-first retrieval + answer-synthesis
// surface. It wraps recall's hybrid search with a split: compiled
// knowledge in wiki/ is surfaced first, raw sources second. The
// synthesize stage hands the split context to the LLM and verifies
// citations before returning.
package query

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/ugurcan-aytar/recall/pkg/recall"

	"github.com/ugurcan-aytar/anvil/internal/engine"
	"github.com/ugurcan-aytar/anvil/internal/wiki"
)

// Hit is one search result surfaced to the user. Kept lean — score,
// snippet, and enough identity fields to trace the chunk back to its
// file and cite it in the synthesized answer.
type Hit struct {
	// Path is relative to the collection root (e.g. "circuit-breaker.md"
	// for a wiki hit, "system-design.md" for a raw hit).
	Path string
	// Title is the page's title when set in frontmatter, otherwise the
	// filename stem. The synthesizer uses it when rendering citations.
	Title string
	// Snippet is recall's highlighted excerpt around the match.
	Snippet string
	// Score is recall's fused (or BM25 fallback) score. Higher = better.
	Score float64
	// DocID is recall's stable document identifier — tests occasionally
	// assert on it but the user never sees this field.
	DocID string
}

// Result bundles the split hits plus the full wiki pages loaded
// from disk so the synthesizer can quote their entire body (not
// just the chunk snippet) when appropriate.
type Result struct {
	// WikiHits are hits from the wiki/ collection — compiled knowledge.
	// Presented first by the synthesizer.
	WikiHits []Hit
	// RawHits are hits from the raw/ collection — primary sources.
	// Presented second; the LLM is instructed to prefer wiki over raw
	// when they cover the same ground.
	RawHits []Hit
	// WikiPages are full wiki pages corresponding to WikiHits, in the
	// same order. Loaded so the synthesis prompt can include the whole
	// body instead of the 200-char snippet.
	WikiPages []*wiki.Page
}

// Options controls a Query call. Zero values are safe — TopK
// defaults to 10, Collection to "" (both wiki + raw), MinScore to 0.
type Options struct {
	// TopK caps the combined hit count (wiki + raw).
	TopK int
	// Collection narrows retrieval to one of "wiki", "raw", or ""
	// (both). The string form matches the flag the user passes on the
	// CLI and lets Query be driven directly from the Cobra wrapper.
	Collection string
	// MinScore drops hits below this floor. 0 disables the floor.
	MinScore float64
}

// DefaultTopK is the fallback when Options.TopK is zero. Matches the
// ingest pipeline's default so `anvil ask` feels consistent with
// `anvil search`.
const DefaultTopK = 10

// Query runs a hybrid search over the project's recall engine,
// splits hits by collection (wiki vs raw), and loads the full body
// of each wiki hit for downstream prompt assembly.
//
// Passing a nil embedder to recall.SearchHybrid is deliberate —
// anvil doesn't embed documents in Phase A3, and recall degrades to
// BM25 gracefully when no embedder or vector rows exist.
func Query(ctx context.Context, eng *engine.Engine, question string, opts Options) (*Result, error) {
	_ = ctx // recall's API doesn't thread ctx yet; kept for forward compat.
	if eng == nil {
		return nil, fmt.Errorf("query: engine is nil")
	}
	if strings.TrimSpace(question) == "" {
		return nil, fmt.Errorf("query: question is empty")
	}

	topK := opts.TopK
	if topK <= 0 {
		topK = DefaultTopK
	}
	searchOpts := []recall.SearchOption{recall.WithLimit(topK)}
	if col := normaliseCollection(opts.Collection); col != "" {
		searchOpts = append(searchOpts, recall.WithCollection(col))
	}
	if opts.MinScore > 0 {
		searchOpts = append(searchOpts, recall.WithMinScore(opts.MinScore))
	}

	// nil emb → recall falls back to BM25 (see recall/pkg/recall.go).
	// Hybrid is the right future default; BM25 today is fine because
	// there are no anvil embeddings to fuse against.
	fused, err := eng.Recall().SearchHybrid(nil, question, searchOpts...)
	if err != nil {
		return nil, fmt.Errorf("hybrid search: %w", err)
	}

	result := &Result{}
	for _, r := range fused {
		hit := Hit{
			Path:    r.Path,
			Title:   firstNonEmpty(r.Title, r.Path),
			Snippet: r.Snippet,
			Score:   r.FusedScore,
			DocID:   r.DocID,
		}
		switch r.CollectionName {
		case engine.CollWiki:
			result.WikiHits = append(result.WikiHits, hit)
		case engine.CollRaw:
			result.RawHits = append(result.RawHits, hit)
		default:
			// Unknown collection — treat as raw so the user still
			// sees it. anvil only registers two collections, so
			// this branch only fires when external tooling
			// populated the DB.
			result.RawHits = append(result.RawHits, hit)
		}
	}

	// Load full wiki pages for every wiki hit. A missing-on-disk page
	// (stale index, manual rm) is demoted to a warning; the hit is
	// dropped so the synthesizer never gets a nil *wiki.Page.
	wikiDir := eng.WikiDir()
	keptHits := make([]Hit, 0, len(result.WikiHits))
	for _, h := range result.WikiHits {
		page, err := wiki.ReadPage(wikiDir, h.Path)
		if err != nil {
			if os.IsNotExist(err) || isReservedErr(err) {
				// Silent drop for reserved files (index.md / log.md)
				// which recall indexed alongside real pages. Missing
				// files are likely stale-index issues.
				continue
			}
			fmt.Fprintf(os.Stderr, "warning: wiki hit %s unreadable: %v\n", h.Path, err)
			continue
		}
		keptHits = append(keptHits, h)
		result.WikiPages = append(result.WikiPages, page)
	}
	result.WikiHits = keptHits

	return result, nil
}

// normaliseCollection mirrors the search command's behaviour:
// "both"/"all"/"" → all collections, "wiki"/"raw" → that collection,
// anything else → pass-through (lets a future multi-collection
// deployment name a custom collection without code changes).
func normaliseCollection(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "both", "all":
		return ""
	case "wiki":
		return engine.CollWiki
	case "raw":
		return engine.CollRaw
	default:
		return v
	}
}

// isReservedErr detects the wiki package's "reserved filename" error
// so we can drop hits on index.md / log.md silently. Substring match
// keeps the coupling minimal.
func isReservedErr(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "reserved filename")
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
