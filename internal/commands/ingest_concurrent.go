package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ugurcan-aytar/anvil/internal/ingest"
	"github.com/ugurcan-aytar/anvil/internal/llm"
	"github.com/ugurcan-aytar/anvil/internal/wiki"
)

// pendingIngest is one file's state as it flows through the parallel
// pipeline: fan-out goroutines fill in `extraction`, the main
// goroutine drains results in file order and runs the serial
// reconcile + write + log tail.
type pendingIngest struct {
	idx        int // 1-based position in the batch for the [N/M] counter
	relPath    string
	absPath    string
	raw        []byte
	hash       string
	source     ingest.Source
	extraction *ingest.Extraction
	skip       bool
	err        error
}

// runIngestConcurrent drives the parallel extract path. Returns the
// aggregate summary; the caller still prints the grand total.
func runIngestConcurrent(
	ctx context.Context,
	client llm.Client,
	dbPath, wikiDir string,
	files []string,
	opts ingestOptions,
	workers int,
	counterFmt string,
) ingestSummary {
	summary := ingestSummary{}

	// Phase 1: serial read + hash + cache check. Fast; running this
	// in parallel would contend on the SQLite cache DB with no
	// benefit.
	tasks := make([]*pendingIngest, 0, len(files))
	for i, relPath := range files {
		absPath := filepath.Join(projectDirAbs(wikiDir), relPath)
		raw, err := os.ReadFile(absPath)
		if err != nil {
			summary.Errors++
			fmt.Fprintf(os.Stderr, "  ! %s: read source: %v\n", relPath, err)
			continue
		}
		if strings.TrimSpace(string(raw)) == "" {
			summary.Errors++
			fmt.Fprintf(os.Stderr, "  ! %s: source is empty\n", relPath)
			continue
		}
		hash := ingest.HashBytes(raw)
		t := &pendingIngest{idx: i + 1, relPath: relPath, absPath: absPath, raw: raw, hash: hash}
		if !opts.Force {
			already, err := ingest.IsAlreadyIngested(dbPath, relPath, hash)
			if err != nil {
				t.err = fmt.Errorf("cache check: %w", err)
			} else if already {
				t.skip = true
			}
		}
		tasks = append(tasks, t)
	}

	// Build the slug catalog once — every Extract prompt in this
	// batch sees the same canonical vocabulary.
	var existingSlugs []string
	if cat, err := ingest.LoadSlugCatalog(wikiDir); err == nil {
		existingSlugs = cat.Slugs()
	}

	// Phase 2: parallel Extract. Skipped / errored tasks short-circuit.
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for _, t := range tasks {
		if t.skip || t.err != nil {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(t *pendingIngest) {
			defer wg.Done()
			defer func() { <-sem }()
			t.source = ingest.Source{
				Path:          t.relPath,
				Title:         sourceTitle(t.relPath, t.raw),
				Content:       stripLeadingFrontmatter(string(t.raw)),
				ExistingSlugs: existingSlugs,
			}
			ext, err := ingest.Extract(ctx, client, t.source)
			if err != nil {
				t.err = fmt.Errorf("extract: %w", err)
				return
			}
			t.extraction = ext
		}(t)
	}
	wg.Wait()

	// Phase 3: serial reconcile + write + mark + log + print.
	for _, t := range tasks {
		if verbosity >= VerbosityNormal {
			fmt.Printf(counterFmt, t.idx, len(files))
		}
		if t.skip {
			if verbosity >= VerbosityNormal {
				fmt.Printf("%s ... skipped (unchanged)\n", t.relPath)
			}
			summary.Skipped++
			continue
		}
		if t.err != nil {
			summary.Errors++
			fmt.Fprintf(os.Stderr, "%s ... error: %v\n", t.relPath, t.err)
			continue
		}
		if verbosity >= VerbosityNormal {
			fmt.Printf("%s ... ", t.relPath)
		}
		started := time.Now()

		reconciled, err := ingest.Reconcile(t.extraction, wikiDir, t.relPath)
		if err != nil {
			summary.Errors++
			fmt.Fprintf(os.Stderr, "reconcile: %v\n", err)
			continue
		}
		printVerbose("  extracted: %d entities, %d concepts, %d claims\n",
			len(t.extraction.Entities), len(t.extraction.Concepts), len(t.extraction.Claims))

		if opts.DryRun {
			if verbosity >= VerbosityNormal {
				fmt.Printf("[dry-run] %d create / %d update (%s)\n",
					len(reconciled.Create), len(reconciled.Update),
					truncDur(time.Since(started)))
			}
			summary.Processed++
			continue
		}

		report := ingest.Write(ctx, client, reconciled, wikiDir, time.Now())
		for _, werr := range report.Errors {
			fmt.Fprintf(os.Stderr, "  ! write: %v\n", werr)
		}
		summary.Created += len(report.Created)
		summary.Updated += len(report.Updated)
		summary.Processed++

		if err := ingest.MarkIngested(dbPath, t.relPath, t.hash); err != nil {
			fmt.Fprintf(os.Stderr, "mark cache: %v\n", err)
		}
		if err := wiki.AppendLog(wikiDir, wiki.LogEntry{
			Timestamp: time.Now(),
			Type:      wiki.LogTypeIngest,
			Title:     t.relPath,
			Created:   report.Created,
			Updated:   report.Updated,
			Sources:   []string{t.relPath},
		}); err != nil {
			fmt.Fprintf(os.Stderr, "  ! append log: %v\n", err)
		}

		if verbosity >= VerbosityNormal {
			fmt.Printf("%d created, %d updated (%s)\n",
				len(report.Created), len(report.Updated),
				truncDur(time.Since(started)))
		}
		printVerbose("  created: %s\n", strings.Join(report.Created, ", "))
		printVerbose("  updated: %s\n", strings.Join(report.Updated, ", "))
	}
	return summary
}

// projectDirAbs returns the project root from wikiDir (which ends
// in "/wiki"). The concurrent path doesn't have the *engine handle
// and this helper keeps the caller-side plumbing clean.
func projectDirAbs(wikiDir string) string {
	return filepath.Dir(wikiDir)
}
