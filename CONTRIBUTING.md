# Contributing to anvil

## Development Setup

### Prerequisites

- Go 1.24+
- At least one LLM backend: `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, or `claude` CLI on PATH
- (Optional) [recall](https://github.com/ugurcan-aytar/recall) for search features

### Build & Test

```bash
git clone https://github.com/ugurcan-aytar/anvil.git
cd anvil
go build ./...
go test ./...
```

## Code Style

- Always run `gofmt` before committing
- One file per CLI subcommand in `internal/commands/`
- One responsibility per package
- No clever abstractions — straightforward code preferred

## Commit Convention

[Conventional Commits](https://www.conventionalcommits.org/):

- `feat:` — new feature
- `fix:` — bug fix
- `docs:` — documentation only
- `refactor:` — code restructure
- `test:` — tests only
- `chore:` — build/tooling

Commit messages: explain WHY, not WHAT. The diff shows what.

## Pull Request Process

1. Fork the repository
2. Create a feature branch: `git checkout -b feat/my-feature`
3. `go build ./...` and `go test ./...` must pass
4. Push and open a Pull Request
