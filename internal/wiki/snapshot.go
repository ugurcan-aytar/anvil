package wiki

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SnapshotFilename is the per-project file that records each wiki
// page's content hash. Sits next to .anvil/index.db so `rm
// .anvil/*` resets everything in one go.
const SnapshotFilename = "wiki-snapshot.json"

// Snapshot is the on-disk diff baseline: filename → sha256 of the
// page's on-disk bytes. The Timestamp is purely for display.
type Snapshot struct {
	Timestamp time.Time         `json:"timestamp"`
	Hashes    map[string]string `json:"hashes"`
}

// DiffReport is what CompareSnapshot returns. Added / Modified /
// Deleted are sorted filenames so the CLI prints stably.
type DiffReport struct {
	Added    []string
	Modified []string
	Deleted  []string
}

// TotalChanges sums every bucket — handy for "N changes total" and
// "no changes" early-outs.
func (d *DiffReport) TotalChanges() int {
	if d == nil {
		return 0
	}
	return len(d.Added) + len(d.Modified) + len(d.Deleted)
}

// CaptureSnapshot walks wikiDir, hashes every page's bytes, and
// returns a Snapshot ready to serialise. Reserved files
// (index.md / log.md) and non-.md files are skipped — the same set
// ListPages skips. Errors from ReadFile bubble so a partly-readable
// wiki doesn't silently produce a bad baseline.
func CaptureSnapshot(wikiDir string) (*Snapshot, error) {
	entries, err := os.ReadDir(wikiDir)
	if err != nil {
		if os.IsNotExist(err) {
			return &Snapshot{Timestamp: time.Now(), Hashes: map[string]string{}}, nil
		}
		return nil, fmt.Errorf("read %s: %w", wikiDir, err)
	}
	hashes := make(map[string]string, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		if _, reserved := ReservedFilenames[e.Name()]; reserved {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(wikiDir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", e.Name(), err)
		}
		sum := sha256.Sum256(raw)
		hashes[e.Name()] = hex.EncodeToString(sum[:])
	}
	return &Snapshot{Timestamp: time.Now(), Hashes: hashes}, nil
}

// SaveSnapshot writes snap to path, creating parent dirs as needed.
func SaveSnapshot(path string, snap *Snapshot) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

// LoadSnapshot reads and decodes a snapshot file. A missing file is
// NOT an error — it returns (nil, nil) so the caller can treat
// "nothing to diff against" as a distinct state (first diff ever).
func LoadSnapshot(path string) (*Snapshot, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var snap Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if snap.Hashes == nil {
		snap.Hashes = map[string]string{}
	}
	return &snap, nil
}

// CompareSnapshot diffs baseline against current, sorting every
// output bucket so the CLI renders deterministically.
//
// Baseline nil → every current page counts as Added (the "first
// diff ever" case).
func CompareSnapshot(baseline, current *Snapshot) *DiffReport {
	report := &DiffReport{}
	if current == nil {
		current = &Snapshot{Hashes: map[string]string{}}
	}
	var baseHashes map[string]string
	if baseline != nil {
		baseHashes = baseline.Hashes
	}
	for name, curHash := range current.Hashes {
		baseHash, ok := baseHashes[name]
		switch {
		case !ok:
			report.Added = append(report.Added, name)
		case baseHash != curHash:
			report.Modified = append(report.Modified, name)
		}
	}
	for name := range baseHashes {
		if _, ok := current.Hashes[name]; !ok {
			report.Deleted = append(report.Deleted, name)
		}
	}
	sort.Strings(report.Added)
	sort.Strings(report.Modified)
	sort.Strings(report.Deleted)
	return report
}
