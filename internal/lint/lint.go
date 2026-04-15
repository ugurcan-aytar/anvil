package lint

import (
	"context"
	"fmt"

	"github.com/ugurcan-aytar/anvil/internal/llm"
	"github.com/ugurcan-aytar/anvil/internal/wiki"
)

// RunOptions controls which stages of lint fire. StructuralOnly=true
// skips every LLM-backed check and returns a report with the five
// structural fields populated plus the health score. Useful for CI
// hooks and the `anvil lint --structural-only` flag.
type RunOptions struct {
	StructuralOnly bool
}

// Run is the orchestrator `anvil lint` drives. Runs every
// structural check, then — unless StructuralOnly is set — every
// LLM-backed check. Composes a single LintReport with HealthScore
// populated from the final counts.
//
// Per-stage errors (missing wiki dir, unreadable index) are
// returned immediately; individual LLM-call failures are absorbed
// inside the detector functions so one bad prompt doesn't abort
// the whole run.
func Run(ctx context.Context, client llm.Client, wikiDir string, opts RunOptions) (*LintReport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	report := &LintReport{}

	pages, err := wiki.ListPages(wikiDir)
	if err != nil {
		return nil, fmt.Errorf("list pages: %w", err)
	}
	report.PageCount = len(pages)

	if report.Orphans, err = CheckOrphans(wikiDir); err != nil {
		return nil, err
	}
	if report.MissingPages, err = CheckMissingPages(wikiDir); err != nil {
		return nil, err
	}
	if report.BrokenLinks, err = CheckBrokenLinks(wikiDir); err != nil {
		return nil, err
	}
	if report.EmptyPages, err = CheckEmptyPages(wikiDir); err != nil {
		return nil, err
	}
	if report.StaleIndex, err = CheckStaleIndex(wikiDir); err != nil {
		return nil, err
	}

	if !opts.StructuralOnly && client != nil {
		if found, err := DetectContradictions(ctx, client, wikiDir); err == nil {
			report.Contradictions = found
		}
		if found, err := DetectStaleClaims(ctx, client, wikiDir); err == nil {
			report.StaleClaims = found
		}
		// Suggestions last — they rely on the same graph the other
		// checks already built, so the LLM sees a shape consistent
		// with the report.
		graph, gerr := wiki.BuildGraph(wikiDir)
		if gerr == nil {
			if sugs, err := Suggest(ctx, client, wikiDir, graph); err == nil {
				report.Suggestions = sugs
			}
		}
	}
	report.HealthScore = HealthScore(report)
	return report, nil
}
