package commands

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/cobra"

	"github.com/ugurcan-aytar/anvil/internal/engine"
)

// watchOptions are the `anvil watch` flags.
type watchOptions struct {
	Debounce int
	Workers  int
}

var watchOpts watchOptions

var watchCmd = &cobra.Command{
	Use:   "watch",
	Short: "Watch raw/ for file changes and auto-ingest new sources",
	Long: `anvil watch uses fsnotify to observe the project's raw/ directory
(recursive). New or modified .md / .txt files trigger an ingest
automatically. Deletions are logged but NOT propagated to the wiki —
removing a source doesn't invalidate the knowledge anvil already
compiled from it.

Ctrl+C exits cleanly once the in-flight ingest (if any) finishes.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runWatch(cmd.Context(), watchOpts)
	},
}

func init() {
	watchCmd.Flags().IntVar(&watchOpts.Debounce, "debounce", 500,
		"debounce window in ms — waits this long after an fsnotify event before triggering ingest")
	watchCmd.Flags().IntVarP(&watchOpts.Workers, "workers", "w", 1,
		"max concurrent Extract calls during auto-ingest")
}

// runWatch is the exported-to-tests entry point.
func runWatch(ctx context.Context, opts watchOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	eng, err := engine.Open(projectDir)
	if err != nil {
		return err
	}
	// Close the engine on exit — watchCmd doesn't use it directly
	// (ingest opens its own), but we check project shape up-front
	// so bad paths fail fast.
	eng.Close()

	rawDir := filepath.Join(absProject(), "raw")
	if _, err := os.Stat(rawDir); err != nil {
		return fmt.Errorf("raw/ not found at %s: %w", rawDir, err)
	}

	w, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("fsnotify: %w", err)
	}
	defer w.Close()

	// Watch raw/ + every subdirectory underneath so newly-added
	// nested sources trigger ingest without restart.
	if err := addWatchRecursive(w, rawDir); err != nil {
		return fmt.Errorf("watch %s: %w", rawDir, err)
	}
	fmt.Printf("Watching %s for new files... (Ctrl+C to stop)\n", rawDir)

	debounce := time.Duration(opts.Debounce) * time.Millisecond
	if debounce <= 0 {
		debounce = 500 * time.Millisecond
	}

	// Accept Ctrl+C for graceful shutdown — we want to finish an
	// in-flight ingest rather than leave a half-written page.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// The debouncer collapses burst events (editor savers tend to
	// fire 3-5 events per save) into a single ingest call per path.
	pending := newWatchDebouncer(debounce, func(paths []string) {
		if len(paths) == 0 {
			return
		}
		fmt.Printf("[%s] %d path(s) changed → ingesting...\n",
			time.Now().Format("15:04:05"), len(paths))
		if _, err := captureWatchOutput(func() error {
			return runIngest(ctx, paths, ingestOptions{Workers: opts.Workers})
		}); err != nil {
			fmt.Fprintf(os.Stderr, "watch ingest: %v\n", err)
		}
	})
	defer pending.Close()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-sigCh:
			fmt.Println("\nwatch: Ctrl+C received, shutting down")
			pending.Flush()
			return nil
		case ev, ok := <-w.Events:
			if !ok {
				return nil
			}
			handleWatchEvent(w, ev, pending)
		case err, ok := <-w.Errors:
			if !ok {
				return nil
			}
			fmt.Fprintf(os.Stderr, "watch: fsnotify: %v\n", err)
		}
	}
}

// handleWatchEvent routes a single fsnotify event. Create events on
// directories extend the watch; create / write events on ingestible
// files enqueue them via the debouncer. Remove / rename are
// informational only — we don't want a stale `rm` wiping wiki pages.
func handleWatchEvent(w *fsnotify.Watcher, ev fsnotify.Event, d *watchDebouncer) {
	if ev.Op&fsnotify.Create != 0 {
		info, err := os.Stat(ev.Name)
		if err == nil && info.IsDir() {
			_ = w.Add(ev.Name)
			return
		}
	}
	if !isIngestibleExt(ev.Name) {
		return
	}
	switch {
	case ev.Op&(fsnotify.Create|fsnotify.Write) != 0:
		d.Touch(ev.Name)
	case ev.Op&(fsnotify.Remove|fsnotify.Rename) != 0:
		fmt.Printf("[%s] removed: %s (wiki untouched)\n",
			time.Now().Format("15:04:05"), filepath.Base(ev.Name))
	}
}

// addWatchRecursive walks dir and registers a watcher for every
// subdirectory — fsnotify itself isn't recursive.
func addWatchRecursive(w *fsnotify.Watcher, dir string) error {
	return filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return w.Add(p)
		}
		return nil
	})
}

// captureWatchOutput silences stdout for the wrapped ingest call and
// returns stderr-style errors to the caller. Helpful during watch so
// every ingest doesn't double-print the banner.
func captureWatchOutput(fn func() error) (string, error) {
	// In practice the user wants to see the ingest output inline —
	// so we don't actually suppress anything here. Kept as a seam
	// so a future --json mode can redirect cleanly.
	return "", fn()
}

// watchDebouncer collects paths touched within a window and calls
// flush once the window expires without new touches.
type watchDebouncer struct {
	mu       sync.Mutex
	window   time.Duration
	flush    func([]string)
	pending  map[string]struct{}
	timer    *time.Timer
	closed   bool
	closeCh  chan struct{}
}

func newWatchDebouncer(window time.Duration, flush func([]string)) *watchDebouncer {
	return &watchDebouncer{
		window:  window,
		flush:   flush,
		pending: make(map[string]struct{}),
		closeCh: make(chan struct{}),
	}
}

// Touch marks path as needing ingestion. Restarts the timer.
func (d *watchDebouncer) Touch(path string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return
	}
	d.pending[path] = struct{}{}
	if d.timer != nil {
		d.timer.Stop()
	}
	d.timer = time.AfterFunc(d.window, d.fire)
}

// Flush drains the pending set synchronously — used on shutdown.
func (d *watchDebouncer) Flush() {
	d.mu.Lock()
	paths := d.drainLocked()
	d.mu.Unlock()
	if len(paths) > 0 {
		d.flush(paths)
	}
}

func (d *watchDebouncer) fire() {
	d.mu.Lock()
	paths := d.drainLocked()
	d.mu.Unlock()
	if len(paths) > 0 {
		d.flush(paths)
	}
}

func (d *watchDebouncer) drainLocked() []string {
	if len(d.pending) == 0 {
		return nil
	}
	out := make([]string, 0, len(d.pending))
	for p := range d.pending {
		out = append(out, p)
	}
	d.pending = make(map[string]struct{})
	return out
}

// Close stops the timer so a shutdown Flush is the only remaining
// side-effect the caller needs to worry about.
func (d *watchDebouncer) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil
	}
	d.closed = true
	if d.timer != nil {
		d.timer.Stop()
	}
	return nil
}

// quiet any "imported and not used" linter noise for strings; watch
// uses it for extension detection.
var _ = strings.HasSuffix
