package ingest

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	// mattn/go-sqlite3 is already pulled in transitively by recall.
	// Importing it for side-effects here registers the driver so
	// `sql.Open("sqlite3", ...)` works even when recall isn't yet
	// holding a handle to the same DB.
	_ "github.com/mattn/go-sqlite3"
)

// cacheSchema is the ingested_sources table — the one anvil-owned
// table that lives in the same .anvil/index.db recall manages. Keeping
// it here (not a separate file) makes `anvil status` a single-file
// concern and means `rm .anvil/index.db` resets everything in one go.
const cacheSchema = `CREATE TABLE IF NOT EXISTS ingested_sources (
    path         TEXT PRIMARY KEY,
    content_hash TEXT NOT NULL,
    ingested_at  TEXT NOT NULL
)`

// HashBytes returns the hex-encoded SHA-256 of b. Exported so higher
// layers (command, integration tests) can compute content hashes
// without reading the file twice.
func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// HashFile is the convenience form: read the file and hash the bytes.
// Errors propagate verbatim so the caller can distinguish "missing
// file" from "permission denied".
func HashFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return HashBytes(raw), nil
}

// IsAlreadyIngested returns true when path has been recorded with
// this exact contentHash. A missing row is NOT an error — it just
// returns (false, nil). A different stored hash returns (false, nil)
// too; the caller's job is to re-ingest.
func IsAlreadyIngested(dbPath, path, contentHash string) (bool, error) {
	db, err := openCache(dbPath)
	if err != nil {
		return false, err
	}
	defer db.Close()

	var stored string
	err = db.QueryRow(
		`SELECT content_hash FROM ingested_sources WHERE path = ?`, path,
	).Scan(&stored)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("query ingested_sources: %w", err)
	}
	return stored == contentHash, nil
}

// MarkIngested records that path was ingested with the given hash at
// time.Now(). UPSERT semantics — repeated ingest of the same path
// updates the hash + timestamp instead of erroring.
func MarkIngested(dbPath, path, contentHash string) error {
	db, err := openCache(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(
		`INSERT INTO ingested_sources(path, content_hash, ingested_at) VALUES(?, ?, ?)
         ON CONFLICT(path) DO UPDATE SET content_hash = excluded.content_hash, ingested_at = excluded.ingested_at`,
		path, contentHash, time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("upsert ingested_sources: %w", err)
	}
	return nil
}

// openCache opens the shared .anvil/index.db and ensures the
// ingested_sources table exists. A 5s busy_timeout absorbs overlap
// with recall's own writes — both processes use WAL so concurrent
// readers never block, and the short writer contention is harmless
// for anvil's interactive ingest workload.
func openCache(dbPath string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", filepath.Dir(dbPath), err)
	}
	// `_busy_timeout` + `_journal_mode=WAL` are passed via the DSN so
	// they apply before the first query. recall also sets WAL; both
	// tools converging on the same setting is intentional.
	dsn := dbPath + "?_busy_timeout=5000&_journal_mode=WAL"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", dbPath, err)
	}
	if _, err := db.Exec(cacheSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create ingested_sources: %w", err)
	}
	return db, nil
}
