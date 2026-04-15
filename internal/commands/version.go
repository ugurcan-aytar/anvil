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
		v, c, d := resolveVersionInfo()
		fmt.Printf("anvil %s\n", v)
		fmt.Printf("  commit:  %s\n", c)
		fmt.Printf("  built:   %s\n", d)
		fmt.Printf("  go:      %s\n", runtime.Version())
		fmt.Printf("  os/arch: %s/%s\n", runtime.GOOS, runtime.GOARCH)
		return nil
	},
}

// resolveVersionInfo returns the (version, commit, builtAt) triple
// the command prints. Release builds inject Version/Commit/BuildDate
// via -ldflags; local builds leave them at their zero defaults, in
// which case we substitute friendlier "dev" / "HEAD" / "local"
// placeholders so nothing renders as "unknown" twice.
func resolveVersionInfo() (version, commit, builtAt string) {
	version = Version
	if version == "" || version == "unknown" {
		version = "dev"
	}
	commit = Commit
	if commit == "" || commit == "unknown" {
		commit = "HEAD"
	}
	builtAt = BuildDate
	if builtAt == "" || builtAt == "unknown" {
		builtAt = "local"
	}
	return version, commit, builtAt
}
