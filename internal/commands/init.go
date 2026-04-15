package commands

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/ugurcan-aytar/anvil/internal/engine"
	"github.com/ugurcan-aytar/anvil/internal/wiki"
)

// defaultAnvilMD is the schema file `anvil init` seeds. Phase A2+
// ingest reads it before every LLM call so the wiki stays
// consistent as it grows — the LLM is expected to follow whatever
// conventions this file states.
const defaultAnvilMD = `# ANVIL.md — Wiki Schema

This file tells anvil how the wiki is structured.

## Page Types
- **source**: summary of a raw source document
- **concept**: an idea, framework, or topic
- **entity**: a person, company, tool, or project
- **synthesis**: cross-cutting analysis connecting multiple pages

## Conventions
- One page per concept/entity/source
- Filenames: kebab-case (e.g., ` + "`circuit-breaker-pattern.md`" + `)
- Cross-references: [[wikilink]] syntax
- Every page has YAML frontmatter (title, type, sources, related, created, updated)
- index.md lists every page with a one-line TLDR
- log.md is append-only: one entry per ingest/query/lint operation
`

// defaultProjectGitignore keeps .anvil/ out of the user's repo
// (binary DB) while letting raw/ and wiki/ be version-controlled.
const defaultProjectGitignore = `# anvil local database — do not commit
.anvil/

# Common junk
.DS_Store
Thumbs.db
`

var initCmd = &cobra.Command{
	Use:   "init [name]",
	Short: "Create a new anvil project (raw/, wiki/, ANVIL.md)",
	Long: `anvil init scaffolds a new project directory with the layout
every other anvil command expects: raw/ (immutable sources), wiki/
(LLM-generated pages), ANVIL.md (schema), and .anvil/ (recall's
SQLite database).

With no argument, initialises in the current directory. With a name,
creates a new sub-directory of that name.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		target, err := resolveInitTarget(args)
		if err != nil {
			return err
		}
		return initProject(target)
	},
}

// resolveInitTarget turns the positional args into an absolute
// project directory. `anvil init` (no args) uses cwd; `anvil init
// my-research` creates ./my-research. An existing target is an
// error — anvil doesn't overwrite.
func resolveInitTarget(args []string) (string, error) {
	if len(args) == 0 {
		// In-place init: target the current directory. Refuse if
		// it already looks like an anvil project.
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("get cwd: %w", err)
		}
		if _, err := os.Stat(filepath.Join(cwd, "ANVIL.md")); err == nil {
			return "", fmt.Errorf("%s is already an anvil project (ANVIL.md exists)", cwd)
		}
		return cwd, nil
	}
	abs, err := filepath.Abs(args[0])
	if err != nil {
		return "", fmt.Errorf("resolve path %q: %w", args[0], err)
	}
	if _, err := os.Stat(abs); err == nil {
		return "", fmt.Errorf("directory already exists: %s", abs)
	}
	return abs, nil
}

// initProject writes the four directories + four seed files +
// opens the recall engine once so .anvil/index.db is created and
// both collections are registered.
func initProject(target string) error {
	// Defence in depth: resolveInitTarget already guards the CLI
	// entry points, but direct callers (e.g. integration tests)
	// shouldn't be able to silently clobber an existing project.
	if _, err := os.Stat(filepath.Join(target, "ANVIL.md")); err == nil {
		return fmt.Errorf("anvil project already exists at %s", target)
	}

	// Create directory tree. `raw/.gitkeep` so git retains the
	// empty folder; wiki/ will hold index.md + log.md straight
	// away so it stays non-empty too.
	for _, sub := range []string{".anvil", "raw", "wiki"} {
		if err := os.MkdirAll(filepath.Join(target, sub), 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", sub, err)
		}
	}
	if err := os.WriteFile(filepath.Join(target, "raw", ".gitkeep"), nil, 0o644); err != nil {
		return fmt.Errorf("write raw/.gitkeep: %w", err)
	}

	// Seed files.
	writes := []struct {
		rel, body string
	}{
		{"ANVIL.md", defaultAnvilMD},
		{".gitignore", defaultProjectGitignore},
		{"wiki/" + wiki.IndexFilename, wiki.IndexEmptyBody},
		{"wiki/" + wiki.LogFilename, wiki.LogEmptyBody},
	}
	for _, w := range writes {
		path := filepath.Join(target, w.rel)
		if err := os.WriteFile(path, []byte(w.body), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", w.rel, err)
		}
	}

	// Open the recall engine once — creates .anvil/index.db and
	// registers the raw + wiki collections. Close immediately;
	// subsequent subcommands re-open per invocation.
	eng, err := engine.Open(target)
	if err != nil {
		return fmt.Errorf("initialise recall engine: %w", err)
	}
	if err := eng.Close(); err != nil {
		return fmt.Errorf("close engine: %w", err)
	}

	fmt.Printf("Initialised anvil project at %s\n", target)
	fmt.Println("  raw/        drop your source files here")
	fmt.Println("  wiki/       generated pages (index.md, log.md seeded)")
	fmt.Println("  ANVIL.md    schema — tells anvil how the wiki is structured")
	fmt.Println("  .anvil/     local recall database (gitignored)")
	fmt.Println()
	fmt.Println("Next: drop a source into raw/, then run `anvil ingest raw/<file>`.")
	return nil
}
