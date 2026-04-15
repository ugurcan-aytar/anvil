package commands

import (
	"fmt"

	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Wiki health: page count, source count, cross-ref density, last ingest",
	RunE: func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("not implemented — lands in Phase A1")
	},
}
