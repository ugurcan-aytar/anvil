package commands

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ugurcan-aytar/anvil/internal/engine"
	"github.com/ugurcan-aytar/anvil/internal/llm"
	"github.com/ugurcan-aytar/anvil/internal/query"
)

// askOptions carries the `anvil ask` flags.
type askOptions struct {
	// Collection narrows retrieval to one of "wiki", "raw", or ""
	// (both). Passed straight through to query.Options.
	Collection string
	// TopK caps the combined hit count (wiki + raw).
	TopK int
	// NoSave skips the "Save this answer to wiki?" prompt. Useful in
	// CI / non-interactive contexts and scripts.
	NoSave bool
}

var askOpts askOptions

// askStdin is the source of the y/n prompt's input. Tests override
// it so the interactive prompt can be driven from a bytes.Buffer.
// Production path reads from real stdin.
var askStdin io.Reader = os.Stdin

// lastAnswerFilename is the single stashed-answer file. `anvil save`
// reads it, so the path is a shared contract between ask + save.
const lastAnswerFilename = "last-answer.json"

var askCmd = &cobra.Command{
	Use:   "ask <question>",
	Short: "Ask a question — searches wiki first (compiled), then raw (primary)",
	Long: `anvil ask retrieves relevant wiki pages + raw chunks for the
question, then asks the LLM to synthesise a grounded, cited answer.

Citations:
  [[stem]]            — links a compiled wiki page
  ` + "`raw/file.md`" + `       — references a primary source chunk
  Unverified citations are flagged after the answer.

After the answer prints, you're asked whether to save it as a
synthesis page in wiki/. Pass --no-save to skip that prompt.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAsk(cmd.Context(), args[0], askOpts)
	},
}

func init() {
	askCmd.Flags().StringVarP(&askOpts.Collection, "collection", "c", "",
		"restrict retrieval to 'wiki' or 'raw' (default: both)")
	askCmd.Flags().IntVarP(&askOpts.TopK, "limit", "n", 10,
		"cap the combined wiki + raw hit count")
	askCmd.Flags().BoolVar(&askOpts.NoSave, "no-save", false,
		"skip the interactive 'Save this answer to wiki?' prompt")
}

// runAsk is the entry point both Cobra and the integration tests use.
func runAsk(ctx context.Context, question string, opts askOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	eng, err := engine.Open(projectDir)
	if err != nil {
		return err
	}
	defer eng.Close()

	// Refresh the BM25 index so pages created since the last ingest
	// or since `anvil init` show up in search. Cheap for small wikis.
	if _, err := eng.Recall().Index(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: reindex failed: %v\n", err)
	}

	client, err := newLLMClient()
	if err != nil {
		if err == llm.ErrNoBackend {
			fmt.Fprintln(os.Stderr, llm.SetupGuidance())
		}
		return err
	}
	fmt.Printf("LLM backend: %s\n", client.Describe())

	qopts := query.Options{
		Collection: opts.Collection,
		TopK:       opts.TopK,
	}
	result, err := query.Query(ctx, eng, question, qopts)
	if err != nil {
		return err
	}
	fmt.Printf("Searching wiki... %d hits\n", len(result.WikiHits))
	fmt.Printf("Searching raw... %d hits\n", len(result.RawHits))

	if len(result.WikiHits) == 0 && len(result.RawHits) == 0 {
		fmt.Println()
		fmt.Println("No relevant notes found for this question. Try different keywords,")
		fmt.Println("or ingest more sources with `anvil ingest <file>`.")
		return nil
	}

	answer, err := query.Synthesize(ctx, client, question, result)
	if err != nil {
		return err
	}

	// Render answer + citation block.
	fmt.Println()
	fmt.Println(answer.Text)
	if len(answer.Sources) > 0 {
		fmt.Println()
		fmt.Println("Sources:")
		for _, s := range answer.Sources {
			fmt.Printf("  %s %s\n", s, categoryFor(s))
		}
	}
	if len(answer.Unverified) > 0 {
		fmt.Println()
		fmt.Println("⚠️  Unverified citations (not in retrieval context):")
		for _, s := range answer.Unverified {
			fmt.Printf("  %s\n", s)
		}
	}

	// Stash for `anvil save`, regardless of the user's y/n choice —
	// keeps a terminal-less `anvil save` flow usable later in the
	// session.
	if err := writeLastAnswer(eng.ProjectRoot(), question, answer); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not stash last answer: %v\n", err)
	}

	if opts.NoSave {
		return nil
	}
	fmt.Println()
	fmt.Print("Save this answer to wiki? (y/N): ")
	if !readYesNo(askStdin) {
		return nil
	}
	return runSave(ctx, saveOptions{})
}

// categoryFor returns "(compiled)" for wiki sources, "(primary)" for
// raw sources, and "" for anything else — used to annotate the
// Sources list in the user-facing output.
func categoryFor(source string) string {
	switch {
	case strings.HasPrefix(source, "wiki/"):
		return "(compiled)"
	case strings.HasPrefix(source, "raw/"):
		return "(primary)"
	default:
		return ""
	}
}

// readYesNo reads one line from r and returns true if it starts with
// "y" or "Y". Anything else — blank line, "n", "no" — is false. EOF
// returns false, matching typical shell conventions.
func readYesNo(r io.Reader) bool {
	reader := bufio.NewReader(r)
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return false
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	return line[0] == 'y' || line[0] == 'Y'
}

// lastAnswerRecord is the on-disk shape of .anvil/last-answer.json.
// Exported-ish via JSON tags so future tooling could parse it, but
// the Go type stays package-private because it's a private contract
// between ask + save.
type lastAnswerRecord struct {
	Question   string    `json:"question"`
	Answer     string    `json:"answer"`
	Sources    []string  `json:"sources,omitempty"`
	Unverified []string  `json:"unverified,omitempty"`
	Timestamp  time.Time `json:"timestamp"`
}

// writeLastAnswer persists the ask result so `anvil save` can read it.
func writeLastAnswer(projectRoot, question string, answer *query.Answer) error {
	rec := lastAnswerRecord{
		Question:   question,
		Answer:     answer.Text,
		Sources:    answer.Sources,
		Unverified: answer.Unverified,
		Timestamp:  time.Now().UTC(),
	}
	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Join(projectRoot, engine.DBSubdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, lastAnswerFilename), raw, 0o644)
}

// readLastAnswer loads the stashed record. Missing-file returns a
// distinguishable error the save command turns into a user-facing
// "run `anvil ask` first" hint.
func readLastAnswer(projectRoot string) (*lastAnswerRecord, error) {
	path := filepath.Join(projectRoot, engine.DBSubdir, lastAnswerFilename)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rec lastAnswerRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &rec, nil
}
