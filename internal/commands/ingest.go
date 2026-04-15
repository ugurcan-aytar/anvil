package commands

import (
	"fmt"

	"github.com/spf13/cobra"
)

var ingestCmd = &cobra.Command{
	Use:   "ingest <file>",
	Short: "Ingest a source file into the wiki (LLM extracts entities, creates/updates pages)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("not implemented — lands in Phase A2")
	},
}
