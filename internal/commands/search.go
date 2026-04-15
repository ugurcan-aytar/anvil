package commands

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/ugurcan-aytar/recall/pkg/recall"

	"github.com/ugurcan-aytar/anvil/internal/engine"
)

// searchOptions are the flags riding alongside `anvil search`.
type searchOptions struct {
	Limit      int
	Collection string // "raw", "wiki", or "" (both)
}

var searchOpts searchOptions

var searchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Raw recall search across wiki + raw collections (no LLM)",
	Long: `anvil search runs a BM25 full-text query through recall against
one or both of the project's collections. No LLM involvement — this
is the primitive for eyeballing what the retrieval layer sees before
adding LLM-powered features like ` + "`anvil ask`" + ` or ` + "`anvil ingest`" + `.

Scoping:
  --collection wiki    compiled-knowledge pages only
  --collection raw     immutable source files only
  (default)            both collections, ranked together
`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSearch(args[0], searchOpts)
	},
}

func init() {
	searchCmd.Flags().IntVarP(&searchOpts.Limit, "limit", "n", 10, "number of results")
	searchCmd.Flags().StringVarP(&searchOpts.Collection, "collection", "c", "",
		"restrict to 'raw' or 'wiki' (default: both)")
}

func runSearch(query string, opts searchOptions) error {
	eng, err := engine.Open(projectDir)
	if err != nil {
		return err
	}
	defer eng.Close()

	// Refresh the index so newly-written wiki pages show up in
	// search immediately — important during interactive use when
	// users run `anvil search` right after editing a page.
	if _, err := eng.Recall().Index(); err != nil {
		return fmt.Errorf("reindex collections: %w", err)
	}

	collectionArg := normaliseCollection(opts.Collection)
	sopts := []recall.SearchOption{recall.WithLimit(opts.Limit)}
	if collectionArg != "" {
		sopts = append(sopts, recall.WithCollection(collectionArg))
	}

	results, err := eng.Recall().SearchBM25(query, sopts...)
	if err != nil {
		return fmt.Errorf("search: %w", err)
	}
	if len(results) == 0 {
		fmt.Println("No results.")
		return nil
	}

	// Terminal rendering: one result per stanza (path + score +
	// snippet). No colour for now — `--no-color` is defined on the
	// root command but we don't reach for it here since the output
	// is already mostly plain text.
	for i, r := range results {
		if i > 0 {
			fmt.Println()
		}
		label := r.Path
		if r.CollectionName != "" {
			label = r.CollectionName + "/" + r.Path
		}
		fmt.Printf("%s  #%s  (score %.2f)\n", label, shortDocID(r.DocID), r.Score)
		if r.Title != "" && r.Title != r.Path {
			fmt.Printf("  %s\n", r.Title)
		}
		snippet := strings.TrimSpace(r.Snippet)
		if snippet != "" {
			fmt.Printf("  %s\n", snippet)
		}
	}
	return nil
}

// normaliseCollection translates the user's --collection flag into
// the comma-free value recall's SearchOptions.Collection expects.
// "both" and "" map to the empty value (all collections).
func normaliseCollection(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "both", "all":
		return ""
	case "wiki":
		return engine.CollWiki
	case "raw":
		return engine.CollRaw
	default:
		// Pass through — lets power users name custom collections
		// in case we ever expose multi-collection support. recall
		// will error if the name doesn't exist.
		return v
	}
}

// shortDocID returns the last 6 chars of a recall docid so terminal
// output stays readable. Full docid available via --json (to be
// added in a later phase if needed).
func shortDocID(id string) string {
	if len(id) <= 6 {
		return id
	}
	return id[len(id)-6:]
}
