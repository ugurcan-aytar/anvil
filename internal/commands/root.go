package commands

import "github.com/spf13/cobra"

var rootCmd = &cobra.Command{
	Use:   "anvil",
	Short: "LLM-maintained wiki compiler",
	Long:  "anvil reads your raw sources and incrementally builds a structured, interlinked wiki. The LLM does the summarizing, cross-referencing, and maintenance. You curate sources and ask questions.",
}

// Execute runs the root command. Called from cmd/anvil/main.go.
func Execute() error {
	return rootCmd.Execute()
}

func init() {
	rootCmd.PersistentFlags().StringVar(&projectDir, "project", ".", "project directory (contains raw/, wiki/, ANVIL.md)")
	rootCmd.PersistentFlags().BoolVar(&noColor, "no-color", false, "disable colorized output")
	rootCmd.PersistentFlags().BoolVarP(&verboseFlag, "verbose", "v", false, "extra output: LLM prompt/response snippets + timing")
	rootCmd.PersistentFlags().BoolVarP(&quietFlag, "quiet", "q", false, "only print summary + errors (overrides --verbose)")

	// Cobra's PersistentPreRun runs before every subcommand's RunE,
	// so it's the right seam for resolving the verbosity level
	// from the two mutually-exclusive flags.
	rootCmd.PersistentPreRun = func(cmd *cobra.Command, args []string) {
		switch {
		case quietFlag:
			verbosity = VerbosityQuiet
		case verboseFlag:
			verbosity = VerbosityVerbose
		default:
			verbosity = VerbosityNormal
		}
	}

	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(ingestCmd)
	rootCmd.AddCommand(askCmd)
	rootCmd.AddCommand(lintCmd)
	rootCmd.AddCommand(saveCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(searchCmd)
	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(doctorCmd)
	rootCmd.AddCommand(configCmd)
	rootCmd.AddCommand(diffCmd)
	rootCmd.AddCommand(exportCmd)
	rootCmd.AddCommand(graphCmd)
}

// Verbosity levels. Subcommands read `verbosity` (set by
// PersistentPreRun) to decide what to print.
type Verbosity int

const (
	VerbosityQuiet   Verbosity = 0
	VerbosityNormal  Verbosity = 1
	VerbosityVerbose Verbosity = 2
)

var (
	// projectDir is the --project flag value; subcommands read it to
	// locate raw/, wiki/, ANVIL.md, and .anvil/index.db. "." means
	// "cwd" — anvil expects to be run from inside the project
	// directory most of the time.
	projectDir string
	// noColor mirrors the convention the sibling recall + brain CLIs
	// use. Subcommands consult it before emitting ANSI.
	noColor bool
	// verboseFlag / quietFlag are the raw bool values; resolve into
	// `verbosity` via PersistentPreRun. Tests that need to override
	// assign to verbosity directly — they bypass the flag layer.
	verboseFlag bool
	quietFlag   bool
	// verbosity is the resolved output level every subcommand reads.
	// Default is VerbosityNormal; PersistentPreRun flips it based on
	// --quiet / --verbose.
	verbosity = VerbosityNormal
)
