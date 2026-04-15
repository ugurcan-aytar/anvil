package wiki

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTestPage(t *testing.T, wikiDir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(wikiDir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCaptureSnapshotSkipsReserved(t *testing.T) {
	dir := t.TempDir()
	writeTestPage(t, dir, "page-a.md", "body A")
	writeTestPage(t, dir, "index.md", "# Index")
	writeTestPage(t, dir, "log.md", "# Log")

	snap, err := CaptureSnapshot(dir)
	if err != nil {
		t.Fatalf("CaptureSnapshot: %v", err)
	}
	if _, ok := snap.Hashes["page-a.md"]; !ok {
		t.Errorf("page-a should be snapshot'd; got %v", snap.Hashes)
	}
	for _, reserved := range []string{"index.md", "log.md"} {
		if _, ok := snap.Hashes[reserved]; ok {
			t.Errorf("%s should be excluded", reserved)
		}
	}
}

func TestSnapshotSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	writeTestPage(t, dir, "x.md", "body x")
	snap, _ := CaptureSnapshot(dir)
	path := filepath.Join(dir, "snap.json")
	if err := SaveSnapshot(path, snap); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Hashes["x.md"] != snap.Hashes["x.md"] {
		t.Errorf("round-trip hash mismatch")
	}
}

func TestLoadSnapshotMissingFileReturnsNilNil(t *testing.T) {
	snap, err := LoadSnapshot(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Errorf("missing file shouldn't error: %v", err)
	}
	if snap != nil {
		t.Errorf("missing file should return nil; got %+v", snap)
	}
}

func TestCompareSnapshotDetectsChanges(t *testing.T) {
	dir := t.TempDir()
	writeTestPage(t, dir, "a.md", "v1")
	writeTestPage(t, dir, "b.md", "gone-after")
	base, _ := CaptureSnapshot(dir)

	// Mutate: change a, delete b, add c.
	writeTestPage(t, dir, "a.md", "v2")
	_ = os.Remove(filepath.Join(dir, "b.md"))
	writeTestPage(t, dir, "c.md", "new")
	cur, _ := CaptureSnapshot(dir)

	report := CompareSnapshot(base, cur)
	if len(report.Added) != 1 || report.Added[0] != "c.md" {
		t.Errorf("Added = %v", report.Added)
	}
	if len(report.Modified) != 1 || report.Modified[0] != "a.md" {
		t.Errorf("Modified = %v", report.Modified)
	}
	if len(report.Deleted) != 1 || report.Deleted[0] != "b.md" {
		t.Errorf("Deleted = %v", report.Deleted)
	}
	if report.TotalChanges() != 3 {
		t.Errorf("total = %d, want 3", report.TotalChanges())
	}
}

func TestCompareSnapshotNilBaselineMarksEverythingAdded(t *testing.T) {
	dir := t.TempDir()
	writeTestPage(t, dir, "first.md", "body")
	cur, _ := CaptureSnapshot(dir)
	report := CompareSnapshot(nil, cur)
	if len(report.Added) != 1 || report.Added[0] != "first.md" {
		t.Errorf("nil baseline should mark all as Added; got %+v", report)
	}
	if report.TotalChanges() != 1 {
		t.Errorf("total = %d", report.TotalChanges())
	}
}

func TestCompareSnapshotNoChanges(t *testing.T) {
	dir := t.TempDir()
	writeTestPage(t, dir, "steady.md", "never-changes")
	snap1, _ := CaptureSnapshot(dir)
	snap2, _ := CaptureSnapshot(dir)
	report := CompareSnapshot(snap1, snap2)
	if report.TotalChanges() != 0 {
		t.Errorf("expected zero changes; got %+v", report)
	}
}
