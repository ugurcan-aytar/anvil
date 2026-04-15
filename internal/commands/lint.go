package commands

import (
	"fmt"

	"github.com/spf13/cobra"
)

var lintCmd = &cobra.Command{
	Use:   "lint",
	Short: "Wiki health check — orphan pages, broken links, contradictions, stale claims",
	RunE: func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("not implemented — lands in Phase A4")
	},
}
