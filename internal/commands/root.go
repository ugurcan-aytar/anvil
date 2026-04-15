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

	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(ingestCmd)
	rootCmd.AddCommand(askCmd)
	rootCmd.AddCommand(lintCmd)
	rootCmd.AddCommand(saveCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(searchCmd)
	rootCmd.AddCommand(versionCmd)
}

var (
	// projectDir is the --project flag value; subcommands read it to
	// locate raw/, wiki/, ANVIL.md, and .anvil/index.db. "." means
	// "cwd" — anvil expects to be run from inside the project
	// directory most of the time.
	projectDir string
	// noColor mirrors the convention the sibling recall + brain CLIs
	// use. Subcommands consult it before emitting ANSI.
	noColor bool
)
