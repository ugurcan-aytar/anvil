package ingest

import (
	"os"
	"path/filepath"
	"testing"
)

// tempCacheDB returns an isolated .anvil/index.db path per test.
func tempCacheDB(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "index.db")
}

func TestHashBytesIsStable(t *testing.T) {
	a := HashBytes([]byte("same content"))
	b := HashBytes([]byte("same content"))
	if a != b {
		t.Errorf("hashes differ for equal input: %q vs %q", a, b)
	}
	if a == HashBytes([]byte("different")) {
		t.Error("different inputs must hash differently")
	}
	if len(a) != 64 {
		t.Errorf("hex SHA-256 should be 64 chars; got %d", len(a))
	}
}

func TestHashFileReadsDisk(t *testing.T) {
	f := filepath.Join(t.TempDir(), "x.md")
	if err := os.WriteFile(f, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := HashFile(f)
	if err != nil {
		t.Fatalf("HashFile: %v", err)
	}
	if got != HashBytes([]byte("hello world")) {
		t.Errorf("HashFile != HashBytes(contents)")
	}
}

// ============================================================
// Missing row returns (false, nil).
// ============================================================

func TestIsAlreadyIngestedMissingRow(t *testing.T) {
	dbPath := tempCacheDB(t)
	got, err := IsAlreadyIngested(dbPath, "raw/x.md", "abc")
	if err != nil {
		t.Fatalf("IsAlreadyIngested: %v", err)
	}
	if got {
		t.Errorf("no rows yet → want false, got true")
	}
}

// ============================================================
// Mark → check (same hash) → true.
// ============================================================

func TestMarkThenCheckReturnsTrue(t *testing.T) {
	dbPath := tempCacheDB(t)
	hash := HashBytes([]byte("v1"))
	if err := MarkIngested(dbPath, "raw/a.md", hash); err != nil {
		t.Fatalf("MarkIngested: %v", err)
	}
	got, err := IsAlreadyIngested(dbPath, "raw/a.md", hash)
	if err != nil {
		t.Fatalf("IsAlreadyIngested: %v", err)
	}
	if !got {
		t.Errorf("same hash → want true")
	}
}

// ============================================================
// Modify file (hash changes) → false.
// ============================================================

func TestModifiedContentReturnsFalse(t *testing.T) {
	dbPath := tempCacheDB(t)
	if err := MarkIngested(dbPath, "raw/a.md", HashBytes([]byte("v1"))); err != nil {
		t.Fatalf("MarkIngested: %v", err)
	}
	got, err := IsAlreadyIngested(dbPath, "raw/a.md", HashBytes([]byte("v2")))
	if err != nil {
		t.Fatalf("IsAlreadyIngested: %v", err)
	}
	if got {
		t.Errorf("hash mismatch → want false, got true")
	}
}

// ============================================================
// Re-mark updates the stored hash (upsert semantics).
// ============================================================

func TestMarkOverwritesHash(t *testing.T) {
	dbPath := tempCacheDB(t)
	if err := MarkIngested(dbPath, "raw/a.md", "oldhash"); err != nil {
		t.Fatal(err)
	}
	if err := MarkIngested(dbPath, "raw/a.md", "newhash"); err != nil {
		t.Fatal(err)
	}
	// The new hash is now the source of truth.
	if ok, _ := IsAlreadyIngested(dbPath, "raw/a.md", "newhash"); !ok {
		t.Error("post-reupsert newhash should match")
	}
	if ok, _ := IsAlreadyIngested(dbPath, "raw/a.md", "oldhash"); ok {
		t.Error("post-reupsert oldhash should no longer match")
	}
}

// ============================================================
// Cache auto-creates parent dirs (.anvil/ may not exist yet).
// ============================================================

func TestCacheCreatesParentDir(t *testing.T) {
	// Point at a not-yet-created nested path.
	dbPath := filepath.Join(t.TempDir(), "nested", ".anvil", "index.db")
	if err := MarkIngested(dbPath, "raw/a.md", "h"); err != nil {
		t.Fatalf("MarkIngested should create parent dirs: %v", err)
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Errorf("DB file not created: %v", err)
	}
}
