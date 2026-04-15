package commands

import (
	"fmt"

	"github.com/spf13/cobra"
)

var askCmd = &cobra.Command{
	Use:   "ask <question>",
	Short: "Ask a question — searches wiki first (compiled knowledge), then raw sources",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("not implemented — lands in Phase A3")
	},
}
