package llm

// Claude CLI fallback — batch mode.
//
// Users with a Claude Code subscription but no API key can still drive
// anvil by having the `claude` binary on PATH. We exec it with -p
// (print mode, no TTY), pipe system via --system-prompt, and capture
// stdout. No stream-json parsing: this backend is batch-only and the
// default `claude -p` output IS the assistant text.
//
// ANVIL_CLAUDE_BIN overrides the binary name for forks like opencode
// that speak the same -p / --system-prompt contract.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// cliClient shells out to the claude binary on each Complete call. One
// subprocess per request — anvil's ingest pipeline has at most a few
// calls per source file, so startup cost isn't amortised across many
// prompts the way brain's chat REPL would require.
type cliClient struct {
	// Binary is the executable to invoke. Set by newCLIClient from
	// claudeBinary(); exposed so tests can point at a fake script
	// in a tempdir.
	Binary string
	// Model is sent via --model when non-empty. We honour ANVIL_MODEL
	// here too — the CLI accepts Claude model IDs natively.
	Model string
}

// ErrCLIMissing is returned when the configured binary can't be
// executed. Select() guards against this by probing PATH, but an
// operator who rotates their PATH mid-run can still trip it.
var ErrCLIMissing = errors.New("claude CLI not found (install or set ANVIL_CLAUDE_BIN)")

func newCLIClient() *cliClient {
	return &cliClient{
		Binary: claudeBinary(),
		Model:  resolveModel("ANVIL_MODEL", ""),
	}
}

// Complete invokes `claude -p --system-prompt SYSTEM USER` and returns
// stdout. Non-zero exit codes are surfaced with stderr attached so a
// misconfigured CLI (no auth, bad flag) doesn't silently look like an
// empty completion.
func (c *cliClient) Complete(ctx context.Context, system, user string) (string, error) {
	bin := c.Binary
	if bin == "" {
		bin = claudeBinary()
	}
	// `--tools ""` forces the CLI into text-only mode. Without it
	// claude tries to fulfil "write a wiki page" by calling its
	// Write tool, fails the sandbox permission check, and leaks
	// the failure message into the response ("I need write
	// permission to create the file…"). That garbage then lands
	// in the wiki page body via the ingest writer. Explicitly
	// disabling tools is the root-cause fix; callers want text,
	// not a remote write.
	args := []string{"-p", "--no-session-persistence", "--tools", ""}
	if strings.TrimSpace(system) != "" {
		args = append(args, "--system-prompt", system)
	}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	args = append(args, user)

	cmd := exec.CommandContext(ctx, bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// Distinguish the two common failure modes so setup-time
		// issues surface cleanly: (a) binary missing — either a
		// PATH lookup miss (wrapped in *exec.Error) or an absolute
		// path that doesn't exist (os.ErrNotExist), (b) binary
		// ran but exited non-zero.
		var execErr *exec.Error
		if errors.As(err, &execErr) || errors.Is(err, os.ErrNotExist) {
			return "", ErrCLIMissing
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			tail := strings.TrimSpace(stderr.String())
			if tail == "" {
				tail = "(no stderr)"
			}
			return "", fmt.Errorf("claude CLI exited %d: %s", exitErr.ExitCode(), tail)
		}
		return "", fmt.Errorf("claude CLI: %w", err)
	}
	return strings.TrimRight(stdout.String(), "\n"), nil
}

func (c *cliClient) Describe() string {
	label := c.Binary
	if label == "" {
		label = claudeBinary()
	}
	if c.Model != "" {
		return fmt.Sprintf("Claude CLI %s (%s)", label, c.Model)
	}
	return fmt.Sprintf("Claude CLI %s", label)
}
