package commands

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

// Version / Commit / BuildDate are injected via -ldflags at release
// build time (see .github/workflows/release.yml and .goreleaser.yaml).
// Default values flag a dev build so users running from source see
// something truthful instead of a stale tag.
var (
	Version   = "dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print anvil version and build info",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Printf("anvil %s\n", Version)
		fmt.Printf("  commit:  %s\n", Commit)
		fmt.Printf("  built:   %s\n", BuildDate)
		fmt.Printf("  go:      %s\n", runtime.Version())
		fmt.Printf("  os/arch: %s/%s\n", runtime.GOOS, runtime.GOARCH)
		return nil
	},
}
