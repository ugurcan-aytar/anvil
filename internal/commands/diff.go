package commands

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/ugurcan-aytar/anvil/internal/engine"
	"github.com/ugurcan-aytar/anvil/internal/wiki"
)

var diffCmd = &cobra.Command{
	Use:   "diff",
	Short: "Show wiki changes since the last ingest",
	Long: `anvil diff compares the current wiki against a snapshot saved by
the previous ingest (stored at .anvil/wiki-snapshot.json). Reports
added, modified, and deleted pages — no git required.

The first invocation on a never-ingested project reports every
existing page as "Added".`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runDiff(cmd.Context())
	},
}

func runDiff(ctx context.Context) error {
	_ = ctx
	eng, err := engine.Open(projectDir)
	if err != nil {
		return err
	}
	defer eng.Close()

	snapPath := filepath.Join(eng.ProjectRoot(), engine.DBSubdir, wiki.SnapshotFilename)

	baseline, err := wiki.LoadSnapshot(snapPath)
	if err != nil {
		return err
	}
	current, err := wiki.CaptureSnapshot(eng.WikiDir())
	if err != nil {
		return err
	}
	report := wiki.CompareSnapshot(baseline, current)

	fmt.Println("anvil diff")
	if baseline != nil {
		fmt.Printf("Changes since last snapshot (%s):\n\n", baseline.Timestamp.Format("2006-01-02 15:04"))
	} else {
		fmt.Println("No previous snapshot — every current page reported as Added.")
		fmt.Println()
	}

	printBucket("Added", report.Added)
	printBucket("Modified", report.Modified)
	printBucket("Deleted", report.Deleted)

	fmt.Println()
	if report.TotalChanges() == 0 {
		fmt.Println("No changes.")
	} else {
		fmt.Printf("%d change(s) total.\n", report.TotalChanges())
	}
	return nil
}

// printBucket renders one category of the diff report. An empty
// bucket is explicit so the user sees the shape ("Deleted: (none)")
// rather than wondering whether the check ran.
func printBucket(label string, items []string) {
	if len(items) == 0 {
		fmt.Printf("  %-9s (none)\n", label+":")
		return
	}
	for i, item := range items {
		if i == 0 {
			fmt.Printf("  %-9s wiki/%s\n", label+":", item)
			continue
		}
		fmt.Printf("  %-9s wiki/%s\n", "", item)
	}
}
