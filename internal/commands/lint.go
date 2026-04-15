package commands

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ugurcan-aytar/anvil/internal/engine"
	"github.com/ugurcan-aytar/anvil/internal/lint"
	"github.com/ugurcan-aytar/anvil/internal/llm"
	"github.com/ugurcan-aytar/anvil/internal/wiki"
)

// lintOptions carries the `anvil lint` flags.
type lintOptions struct {
	// StructuralOnly skips every LLM-backed check. Runs in well
	// under a second on typical wikis — suitable for pre-commit
	// hooks or CI.
	StructuralOnly bool
	// Fix applies the safe auto-fixes: rebuilds wiki/index.md from
	// the current page set (so stale-index entries resolve). Does
	// not create missing pages, resolve broken links, or edit
	// contradictory bodies — those need human judgement.
	Fix bool
}

var lintOpts lintOptions

var lintCmd = &cobra.Command{
	Use:   "lint",
	Short: "Wiki health check — orphans, broken links, contradictions, stale claims",
	Long: `anvil lint runs structural checks (orphan pages, missing wikilink
targets, broken links, empty pages, stale index entries) plus, when
an LLM backend is available, contradiction detection, stale-claim
detection, and improvement suggestions.

--structural-only skips every LLM call and returns in well under a
second. --fix applies safe auto-fixes (currently: rebuild
wiki/index.md so stale-index findings disappear).`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runLint(cmd.Context(), lintOpts)
	},
}

func init() {
	lintCmd.Flags().BoolVar(&lintOpts.StructuralOnly, "structural-only", false,
		"skip LLM-backed checks (contradictions, stale claims, suggestions)")
	lintCmd.Flags().BoolVar(&lintOpts.Fix, "fix", false,
		"apply safe auto-fixes (currently: rebuild wiki/index.md)")
}

// runLint is the shared entry point for both Cobra and the
// integration test. Kept package-private so the rest of the CLI
// doesn't depend on the command surface.
func runLint(ctx context.Context, opts lintOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	eng, err := engine.Open(projectDir)
	if err != nil {
		return err
	}
	defer eng.Close()

	var client llm.Client
	if !opts.StructuralOnly {
		client, err = newLLMClient()
		if err != nil {
			if err == llm.ErrNoBackend {
				// No backend → downgrade to structural-only and
				// warn the user rather than failing. Lint works
				// even without credentials — this lowers the
				// barrier for CI setups.
				fmt.Fprintln(os.Stderr,
					"warning: no LLM backend configured — running structural checks only.")
				fmt.Fprintln(os.Stderr, llm.SetupGuidance())
				opts.StructuralOnly = true
				client = nil
			} else {
				return err
			}
		} else {
			fmt.Printf("LLM backend: %s\n", client.Describe())
		}
	}

	report, err := lint.Run(ctx, client, eng.WikiDir(), lint.RunOptions{StructuralOnly: opts.StructuralOnly})
	if err != nil {
		return err
	}
	renderLintReport(report, opts.StructuralOnly)

	if opts.Fix {
		if err := applyFixes(eng.WikiDir(), report); err != nil {
			return err
		}
	}
	return nil
}

// renderLintReport prints the full report in the layout the A4 spec
// shows — one section per check, ✓ for clean, ⚠ for findings,
// health score last.
func renderLintReport(r *lint.LintReport, structuralOnly bool) {
	fmt.Println()
	fmt.Println("Checking wiki health...")
	fmt.Println()

	fmt.Println("Structural checks:")
	fmt.Printf("  ✓ %d pages scanned\n", r.PageCount)
	renderStringList("orphan pages", r.Orphans)
	renderMissingPages(r)
	renderEmptyPages(r.EmptyPages)
	renderStringList("page(s) not in index", r.StaleIndex)

	if structuralOnly {
		fmt.Println()
		fmt.Println("LLM checks: skipped (--structural-only).")
	} else {
		fmt.Println()
		fmt.Println("Contradiction check (LLM):")
		renderContradictions(r.Contradictions)

		fmt.Println()
		fmt.Println("Stale claims:")
		renderStaleClaims(r.StaleClaims)

		fmt.Println()
		fmt.Println("Suggestions:")
		renderSuggestions(r.Suggestions)
	}

	fmt.Println()
	fmt.Printf("Health: %.0f/100\n", r.HealthScore)
}

// renderStringList prints either a ✓ "0 <label>" line or a ⚠ list
// of findings. Used for the orphan / stale-index blocks which have
// no extra structure beyond a page name.
func renderStringList(label string, items []string) {
	if len(items) == 0 {
		fmt.Printf("  ✓ 0 %s\n", label)
		return
	}
	preview := items
	if len(preview) > 5 {
		preview = preview[:5]
	}
	fmt.Printf("  ⚠ %d %s: %s\n", len(items), label, joinList(preview))
	if len(items) > 5 {
		fmt.Printf("         (... %d more)\n", len(items)-5)
	}
}

// renderMissingPages prints the missing-pages block with one
// backlink hint per missing target so the user knows where the
// phantom reference lives.
func renderMissingPages(r *lint.LintReport) {
	if len(r.MissingPages) == 0 {
		fmt.Printf("  ✓ 0 missing pages\n")
		return
	}
	fmt.Printf("  ⚠ %d missing page(s): %s\n",
		len(r.MissingPages), joinList(r.MissingPages))
	// Show up to 3 broken-link sources for context.
	shown := 0
	for _, bl := range r.BrokenLinks {
		if shown >= 3 {
			break
		}
		if bl.Location != "body" {
			continue
		}
		fmt.Printf("         [[%s]] referenced by [[%s]]\n", bl.Target, bl.SourcePage)
		shown++
	}
}

// renderEmptyPages flags stub pages so they stand out in the report.
func renderEmptyPages(empty []string) {
	if len(empty) == 0 {
		fmt.Printf("  ✓ 0 empty pages\n")
		return
	}
	fmt.Printf("  ⚠ %d empty page(s): %s\n", len(empty), joinList(empty))
}

// renderContradictions prints each contradiction block in the
// multi-line format the spec shows.
func renderContradictions(cs []lint.Contradiction) {
	if len(cs) == 0 {
		fmt.Println("  ✓ No contradictions detected")
		return
	}
	fmt.Printf("  ⚠ %d contradiction(s) found:\n", len(cs))
	for _, c := range cs {
		fmt.Printf("    [[%s]] says %q\n", c.PageA, c.ClaimA)
		fmt.Printf("    [[%s]] says %q\n", c.PageB, c.ClaimB)
		if c.Detail != "" {
			fmt.Printf("    detail: %s\n", c.Detail)
		}
	}
}

// renderStaleClaims mirrors the contradiction layout for the
// stale-claims block.
func renderStaleClaims(cs []lint.StaleClaim) {
	if len(cs) == 0 {
		fmt.Println("  ✓ No stale claims detected")
		return
	}
	fmt.Printf("  ⚠ %d stale claim(s):\n", len(cs))
	for _, c := range cs {
		fmt.Printf("    [[%s]]: %s\n", c.Page, c.Claim)
		if c.Detail != "" {
			fmt.Printf("    detail: %s\n", c.Detail)
		}
	}
}

// renderSuggestions renders the numbered list verbatim; each entry
// is one line as returned by lint.Suggest.
func renderSuggestions(s []string) {
	if len(s) == 0 {
		fmt.Println("  (none)")
		return
	}
	for i, sug := range s {
		fmt.Printf("  %d. %s\n", i+1, sug)
	}
}

// joinList is strings.Join with a maximum inline width — keeps the
// lint output one line per section even when a wiki has many
// findings.
func joinList(xs []string) string {
	out := ""
	for i, x := range xs {
		if i > 0 {
			out += ", "
		}
		out += x
	}
	return out
}

// applyFixes is the safe-fix path behind `anvil lint --fix`. The
// only currently-safe automated change is a full index rebuild,
// which resolves every StaleIndex entry without touching page
// content. Future fixes (pruning broken links, deleting orphans)
// should stay opt-in — they're destructive.
func applyFixes(wikiDir string, report *lint.LintReport) error {
	if len(report.StaleIndex) == 0 {
		fmt.Println()
		fmt.Println("Fix: nothing to rebuild — index is already current.")
		return nil
	}
	if err := wiki.RebuildIndex(wikiDir); err != nil {
		return fmt.Errorf("rebuild index: %w", err)
	}
	fmt.Println()
	fmt.Printf("Fix: rebuilt wiki/index.md (%d page(s) re-added).\n", len(report.StaleIndex))
	return nil
}
