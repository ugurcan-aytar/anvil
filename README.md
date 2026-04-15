# anvil

**LLM-maintained wiki compiler.** Drop in sources, get a structured, interlinked wiki.
Source-grounded, compounding knowledge. Single Go binary.

[![Go Version](https://img.shields.io/badge/go-1.24%2B-00ADD8)](https://go.dev/)
[![License MIT](https://img.shields.io/badge/license-MIT-blue)](LICENSE)
[![Platform](https://img.shields.io/badge/platform-macOS%20%7C%20Linux-lightgrey)](#install)
[![CI](https://github.com/ugurcan-aytar/anvil/actions/workflows/ci.yml/badge.svg)](https://github.com/ugurcan-aytar/anvil/actions/workflows/ci.yml)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen)](CONTRIBUTING.md)

---

## The idea

Retrieval-Augmented Generation re-derives knowledge from scratch on every
query. You ask the same thing twice and the system reads the same
documents twice. Nothing accumulates, nothing compiles, nothing
compounds.

anvil flips that. The LLM reads your sources **once**, distils them into
a cross-referenced wiki of entity pages / concept pages / claim
summaries / synthesis notes, and maintains that wiki as new sources
arrive — adding pages, reconciling contradictions, refreshing stale
claims, flagging orphans. The wiki is plain markdown on disk. Open it
in [Obsidian](https://obsidian.md) or any editor. Search it with
[recall](https://github.com/ugurcan-aytar/recall). Ask questions that
hit the compiled knowledge first and fall back to raw sources only when
the wiki doesn't answer.

The compounding is the whole point. Every source you add makes every
future question sharper, because the wiki already holds the answer
distilled from the last hundred sources you fed it.

Based on [Karpathy's LLM Wiki
gist](https://gist.github.com/karpathy/442a6bf555914893e9891c11519de94f).

---

## Three layers

```
my-research/
├── .anvil/
│   └── index.db       ← recall SQLite index (project-local, portable)
├── raw/               ← immutable sources: papers, notes, transcripts
├── wiki/              ← LLM-generated markdown: entity / concept / synthesis pages
└── ANVIL.md           ← schema: tells the LLM how the wiki is structured
```

1. **`raw/`** — every source you want the wiki to cover. Articles,
   meeting transcripts, a chapter copy-paste, your own scratch notes.
   Immutable from anvil's perspective; you edit by dropping in new
   versions.
2. **`wiki/`** — LLM-generated pages. Entity pages (one per person /
   company / product / concept), summary pages, synthesis pages, a
   generated `index.md` and an append-only `log.md` of every ingest.
3. **`ANVIL.md`** — the schema. Naming conventions, frontmatter shape,
   [[wikilink]] conventions, page types the LLM is allowed to create.
   anvil reads it before every LLM call so the wiki stays consistent
   as it grows.

---

## Quick start

```bash
# One-time: install anvil (see below) and set at least one LLM backend
export ANTHROPIC_API_KEY=sk-ant-…

# Create a project
anvil init my-research
cd my-research

# Drop a source — any markdown / text file
cp ~/reading/interesting-paper.md raw/

# Let the LLM read it and update the wiki
anvil ingest raw/interesting-paper.md

# Ask a question — hits the wiki first, raw second, synthesises an answer
anvil ask "what are the main claims?"

# Save the answer as a new wiki page (synthesis layer)
anvil save

# Check wiki health — orphan pages, broken links, contradictions
anvil lint
```

---

## Commands

| Command | What it does |
|---|---|
| `anvil init [name]` | Create project: `raw/`, `wiki/`, `ANVIL.md`, `wiki/index.md`, `wiki/log.md`, `.anvil/index.db` |
| `anvil ingest <file>` | Read source → LLM extracts entities / concepts / claims → creates or updates wiki pages → refreshes index + log |
| `anvil ask "<question>"` | Search wiki first (compiled knowledge), then raw (primary sources), synthesise a cited answer |
| `anvil save` | Persist the last `ask` answer as a new wiki page |
| `anvil lint` | Detect orphan pages, broken cross-references, contradictions, stale claims, missing pages |
| `anvil search "<query>"` | Raw recall search across `wiki/` + `raw/` collections — no LLM, for debugging retrieval |
| `anvil status` | Wiki health: page count, source count, cross-ref density, orphan count, last ingest timestamp |
| `anvil version` | Print version, commit, build date, Go version, OS/arch |

---

## Architecture

```
anvil/
├── cmd/anvil/         # CLI entry point (thin main.go — ≤15 lines)
├── internal/
│   ├── commands/      # One Cobra command per file
│   ├── wiki/          # Wiki CRUD: page create/update, index, log, graph, frontmatter
│   ├── ingest/        # Source → wiki transformation pipeline
│   ├── lint/          # Wiki health checks
│   ├── llm/           # LLM integration (Anthropic + OpenAI-compat + CLI fallback)
│   └── schema/        # ANVIL.md parsing and validation
└── go.mod             # imports recall as a Go library
```

Retrieval is powered by [recall](https://github.com/ugurcan-aytar/recall),
anvil's sibling search engine, imported as a Go library (`pkg/recall`).
Each anvil project opens its own local SQLite DB at
`.anvil/index.db` — zero shared state with other anvil projects on the
same machine, zero shared state with
[brain](https://github.com/ugurcan-aytar/brain) (which uses
`~/.recall/index.db` globally). Zip the project folder, move it to
another machine, run `anvil status` — everything it needs is in the
same directory.

---

## Install

### Homebrew (macOS & Linux)

Lands in v0.2.0. Until then, use source build or prebuilt tarball.

### Pre-built binary

Grab a tarball from the [releases
page](https://github.com/ugurcan-aytar/anvil/releases), extract, drop
`anvil` on your `$PATH`. Ships for `darwin_arm64` and `linux_amd64`.
Checksums in `checksums.txt`.

### From source

```bash
git clone https://github.com/ugurcan-aytar/anvil.git
cd anvil
go build -tags sqlite_fts5 -o anvil ./cmd/anvil
./anvil version
```

Requires Go 1.24+ with CGo enabled (the default — recall's
`mattn/go-sqlite3` + `sqlite-vec` need it).

---

## Configuration

| Variable | Default | Purpose |
|---|---|---|
| `ANTHROPIC_API_KEY` | — | Native Anthropic API (recommended) |
| `OPENAI_API_KEY` | — | Any OpenAI-compatible `/v1/chat/completions` endpoint |
| `OPENAI_BASE_URL` | `https://api.openai.com/v1` | Override for Ollama, OpenRouter, LM Studio, LiteLLM, Groq, Together, Fireworks, etc. |
| `ANVIL_CLAUDE_BIN` | `claude` | Claude CLI binary name (fork-compatible via e.g. `opencode`) |

anvil picks the first configured backend in priority order: Anthropic →
OpenAI-compatible → Claude CLI. Set none and anvil hard-fails on the
first `ingest` / `ask` call with an actionable error.

---

## Status

anvil is pre-1.0. The CLI surface, project layout, and SQLite schema
may shift between minor versions; semver patches stay
backwards-compatible. See [ROADMAP.md](https://github.com/ugurcan-aytar/anvil)
(visible on the public issue tracker only) for what's landing when.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Bug reports / feature requests:
[open an issue](https://github.com/ugurcan-aytar/anvil/issues). Security
issues: [SECURITY.md](SECURITY.md).

## Credits

anvil implements [Karpathy's LLM Wiki
pattern](https://gist.github.com/karpathy/442a6bf555914893e9891c11519de94f)
as a CLI tool.

Search and retrieval powered by
[recall](https://github.com/ugurcan-aytar/recall) — local-first hybrid
search engine (BM25 + vector + RRF fusion, cross-encoder rerank, query
expansion, HyDE, incremental embedding). recall is the same author's
project and was itself inspired by [qmd](https://github.com/tobi/qmd)
by Tobi Lütke.

See also [brain](https://github.com/ugurcan-aytar/brain) for
retrieval-first Q&A over the same note collections — a complementary
tool for when you want a grounded answer without maintaining a wiki.

## License

MIT — see [LICENSE](LICENSE).
