package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ugurcan-aytar/anvil/internal/engine"
	"github.com/ugurcan-aytar/anvil/internal/wiki"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Wiki health: page count, source count, cross-ref density, last ingest",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runStatus(projectDir)
	},
}

func runStatus(projectDir string) error {
	eng, err := engine.Open(projectDir)
	if err != nil {
		return err
	}
	defer eng.Close()

	// Pages + graph. ListPages and BuildGraph both skip reserved
	// filenames, so the counts below are "real" pages only.
	pages, err := wiki.ListPages(eng.WikiDir())
	if err != nil {
		return fmt.Errorf("list pages: %w", err)
	}
	g, err := wiki.BuildGraph(eng.WikiDir())
	if err != nil {
		return fmt.Errorf("build graph: %w", err)
	}

	// Page-type histogram from the frontmatter. An empty type
	// bucket under "(none)" signals pages the LLM hasn't typed yet.
	typeCounts := map[string]int{}
	for _, p := range pages {
		t := p.Type
		if t == "" {
			t = "(none)"
		}
		typeCounts[t]++
	}

	// Raw source count — read raw/ directly, not via recall. `raw`
	// is just files on disk; a shell ls is simpler and faster than
	// a collection listing.
	rawCount, err := countFiles(eng.RawDir(), true)
	if err != nil {
		return err
	}

	// Log entry count: grep the heading line pattern the log
	// writer emits.
	logLines, _ := countLogEntries(eng.WikiDir())

	// DB size — useful both for "is this project getting big" and
	// for confirming that `anvil init` wrote the file at all.
	dbSize := int64(0)
	if info, err := os.Stat(eng.DBPath()); err == nil {
		dbSize = info.Size()
	}

	// ------ Rendering ------

	fmt.Printf("Project: %s\n", eng.ProjectRoot())
	fmt.Printf("Wiki:    %d pages", len(pages))
	if len(pages) > 0 {
		fmt.Printf(" (%s)", formatTypeHistogram(typeCounts))
	}
	fmt.Println()
	fmt.Printf("Raw:     %d files\n", rawCount)
	fmt.Printf("Index:   %d entries in index.md\n", len(pages))
	fmt.Printf("Log:     %d entries in log.md\n", logLines)
	orphans := g.Orphans()
	missing := g.MissingPages()
	fmt.Printf("Graph:   %d orphan page(s), %d missing page(s)\n", len(orphans), len(missing))
	if len(orphans) > 0 {
		fmt.Printf("         orphans: %s\n", strings.Join(orphans, ", "))
	}
	if len(missing) > 0 {
		fmt.Printf("         missing: %s\n", strings.Join(missing, ", "))
	}
	relDB, _ := filepath.Rel(eng.ProjectRoot(), eng.DBPath())
	if relDB == "" {
		relDB = eng.DBPath()
	}
	fmt.Printf("DB:      %s (%s)\n", relDB, humanBytes(dbSize))
	return nil
}

func formatTypeHistogram(counts map[string]int) string {
	// Sort keys for deterministic output. "(none)" sinks to the
	// end so the interesting types print first.
	keys := make([]string, 0, len(counts))
	for k := range counts {
		if k != "(none)" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if _, ok := counts["(none)"]; ok {
		keys = append(keys, "(none)")
	}
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%d %s", counts[k], k))
	}
	return strings.Join(parts, ", ")
}

// countFiles returns the number of regular files under dir. When
// skipHidden is true, files starting with "." (like .gitkeep) are
// excluded from the count.
func countFiles(dir string, skipHidden bool) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read %s: %w", dir, err)
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if skipHidden && strings.HasPrefix(e.Name(), ".") {
			continue
		}
		n++
	}
	return n, nil
}

// countLogEntries greps wiki/log.md for heading lines of the form
// `## [YYYY-MM-DD] <type> | <title>`. Matches wiki.AppendLog's
// output exactly.
func countLogEntries(wikiDir string) (int, error) {
	raw, err := os.ReadFile(filepath.Join(wikiDir, wiki.LogFilename))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	n := 0
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "## [") {
			n++
		}
	}
	return n, nil
}

// humanBytes formats a byte count in IEC units. 0 returns "0 B" —
// useful distinguish "database doesn't exist" vs. "exists but empty".
func humanBytes(n int64) string {
	const (
		kib = 1024
		mib = 1024 * kib
		gib = 1024 * mib
	)
	switch {
	case n >= gib:
		return fmt.Sprintf("%.2f GB", float64(n)/float64(gib))
	case n >= mib:
		return fmt.Sprintf("%.2f MB", float64(n)/float64(mib))
	case n >= kib:
		return fmt.Sprintf("%.2f KB", float64(n)/float64(kib))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
