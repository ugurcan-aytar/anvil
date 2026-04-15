// Package engine is anvil's thin wrapper over recall.Engine.
//
// Every anvil project is a folder with a `.anvil/index.db` SQLite
// database managed by recall. engine.Open picks up that DB (creates
// it on first call), registers the project's raw/ and wiki/
// directories as recall collections, and hands back the recall
// engine so commands can search it directly.
//
// This is the `brain/internal/engine` pattern carried forward —
// brain centralises engine lifecycle so every subcommand calls
// engine.Open() once and defers Close. anvil does the same.
package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/ugurcan-aytar/recall/pkg/recall"
)

// Collection names are fixed. anvil's whole model rests on the
// raw / wiki split — recall uses these two names everywhere a
// search is scoped.
const (
	CollRaw  = "raw"
	CollWiki = "wiki"
)

// DBSubdir + DBFilename compose the project-local database path.
// Exposed so `anvil status` can stat the file without a second call.
const (
	DBSubdir   = ".anvil"
	DBFilename = "index.db"
)

// Engine wraps *recall.Engine alongside the project root so commands
// can reach for raw/ and wiki/ paths without re-deriving them.
//
// The embedder is lazily resolved — BM25-only paths (status, lint
// --structural-only, raw search) don't pay the GGUF load / API
// construction cost. See Embedder() for the resolution rules.
type Engine struct {
	rcl         *recall.Engine
	projectRoot string

	embOnce sync.Once
	emb     recall.Embedder
	embErr  error
}

// Open creates (or opens) the project's recall engine at
// <projectDir>/.anvil/index.db and makes sure both raw and wiki
// collections are registered. projectDir may be relative ("." means
// cwd); the engine carries the absolute form so later Get calls
// don't drift if the caller chdir's.
func Open(projectDir string) (*Engine, error) {
	absRoot, err := filepath.Abs(projectDir)
	if err != nil {
		return nil, fmt.Errorf("resolve project dir %q: %w", projectDir, err)
	}
	// Fail fast if the directory isn't an anvil project — no
	// raw/, no wiki/, no ANVIL.md. `anvil init` is the only
	// command that should bypass this check; callers wanting to
	// bootstrap a fresh project go through InitialiseProject
	// below.
	if err := ensureProject(absRoot); err != nil {
		return nil, err
	}
	dbDir := filepath.Join(absRoot, DBSubdir)
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", dbDir, err)
	}
	dbPath := filepath.Join(dbDir, DBFilename)
	rcl, err := recall.NewEngine(recall.WithDBPath(dbPath))
	if err != nil {
		return nil, fmt.Errorf("open recall engine at %s: %w", dbPath, err)
	}
	e := &Engine{rcl: rcl, projectRoot: absRoot}
	if err := e.ensureCollections(); err != nil {
		rcl.Close()
		return nil, err
	}
	return e, nil
}

// Recall returns the underlying recall engine. Commands that want
// the full public API (Index, Embed, SearchBM25, SearchVector,
// SearchHybrid, Get, Expand, Rerank, …) go through this handle.
func (e *Engine) Recall() *recall.Engine { return e.rcl }

// ProjectRoot returns the absolute project directory.
func (e *Engine) ProjectRoot() string { return e.projectRoot }

// RawDir returns <project>/raw.
func (e *Engine) RawDir() string { return filepath.Join(e.projectRoot, "raw") }

// WikiDir returns <project>/wiki.
func (e *Engine) WikiDir() string { return filepath.Join(e.projectRoot, "wiki") }

// DBPath returns the absolute path to .anvil/index.db.
func (e *Engine) DBPath() string {
	return filepath.Join(e.projectRoot, DBSubdir, DBFilename)
}

// Embedder returns the active embedder, constructed on the first
// call. Copies brain's lazy-resolution pattern: we only pay for
// model load or API client construction when a caller actually
// needs embeddings.
//
// Returns (nil, nil) when embedding isn't configured and the
// failure is a graceful fallback case — e.g. the default build
// (no embed_llama tag) with no RECALL_EMBED_PROVIDER set. Callers
// pass a nil Embedder to recall.SearchHybrid and recall degrades
// to BM25 cleanly.
//
// Returns (nil, err) for misconfiguration the user should know
// about — e.g. RECALL_EMBED_PROVIDER=openai without OPENAI_API_KEY.
func (e *Engine) Embedder() (recall.Embedder, error) {
	if e == nil {
		return nil, fmt.Errorf("engine: nil receiver")
	}
	e.embOnce.Do(func() {
		emb, err := recall.ResolveEmbedder()
		if err != nil {
			// Graceful case: no-op when local isn't compiled in
			// AND the user hasn't opted into an API backend.
			if errors.Is(err, recall.ErrLocalEmbedderNotCompiled) &&
				recall.ResolveAPIProvider() == recall.ProviderLocal {
				return
			}
			e.embErr = err
			return
		}
		e.emb = emb
	})
	return e.emb, e.embErr
}

// SetEmbedder overrides the lazy-resolved embedder. Used by tests
// to inject recall.MockEmbedder without going through the env-driven
// factory. Must be called before the first Embedder() call.
func (e *Engine) SetEmbedder(emb recall.Embedder) {
	if e == nil {
		return
	}
	e.embOnce.Do(func() { e.emb = emb })
}

// Close releases the embedder (if constructed) and the recall
// engine. Safe to call multiple times.
func (e *Engine) Close() error {
	if e == nil || e.rcl == nil {
		return nil
	}
	var embErr error
	if e.emb != nil {
		embErr = e.emb.Close()
	}
	rErr := e.rcl.Close()
	if embErr != nil {
		return embErr
	}
	return rErr
}

// ensureProject verifies projectRoot looks like an initialised anvil
// project — ANVIL.md exists. Subcommands rely on this to bail out
// with an actionable message instead of silently creating a .db in
// whatever folder the user happens to be in.
func ensureProject(projectRoot string) error {
	anvilMD := filepath.Join(projectRoot, "ANVIL.md")
	if _, err := os.Stat(anvilMD); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf(
				"not an anvil project at %s (missing ANVIL.md). Run `anvil init` first",
				projectRoot,
			)
		}
		return fmt.Errorf("stat ANVIL.md: %w", err)
	}
	return nil
}

// ensureCollections registers the raw and wiki collections with
// recall if they aren't already present. Recall's AddCollection is
// idempotent on the collection name — adding an existing one
// returns an error we can safely swallow.
func (e *Engine) ensureCollections() error {
	existing, err := e.rcl.ListCollections()
	if err != nil {
		return fmt.Errorf("list collections: %w", err)
	}
	have := map[string]bool{}
	for _, c := range existing {
		have[c.Name] = true
	}
	add := func(name, absPath string) error {
		if have[name] {
			return nil
		}
		if _, err := os.Stat(absPath); err != nil {
			if os.IsNotExist(err) {
				// Allowed: an anvil project can be shipped
				// without raw/ or wiki/ populated yet. Skip;
				// the collection will be added on first
				// ingest / write via the idempotent retry.
				return nil
			}
			return fmt.Errorf("stat %s: %w", absPath, err)
		}
		if _, err := e.rcl.AddCollection(name, absPath, "", ""); err != nil {
			// Duplicate-name races (concurrent anvil runs) are
			// harmless — recall's store layer surfaces them via
			// the "already exists" / "UNIQUE constraint" message
			// on the collections table. Everything else is a real
			// error.
			if !isAlreadyExistsErr(err) {
				return fmt.Errorf("add collection %q: %w", name, err)
			}
		}
		return nil
	}
	if err := add(CollRaw, e.RawDir()); err != nil {
		return err
	}
	if err := add(CollWiki, e.WikiDir()); err != nil {
		return err
	}
	return nil
}

// isAlreadyExistsErr matches the error recall returns when a
// collection with the given name is already registered. recall's
// store layer uses a plain fmt.Errorf with "already exists" in the
// message, so a substring match is the minimal coupling.
func isAlreadyExistsErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return containsFold(msg, "already exists") || containsFold(msg, "unique constraint")
}

// containsFold is a tiny ASCII case-insensitive strings.Contains.
func containsFold(s, sub string) bool {
	ls, lsub := toLower(s), toLower(sub)
	for i := 0; i+len(lsub) <= len(ls); i++ {
		if ls[i:i+len(lsub)] == lsub {
			return true
		}
	}
	return false
}

func toLower(s string) string {
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out[i] = c
	}
	return string(out)
}

