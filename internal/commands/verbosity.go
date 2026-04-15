package commands

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ugurcan-aytar/anvil/internal/llm"
)

// printNormal writes to stdout unless VerbosityQuiet is active.
// Used for per-file progress and other normal-mode chatter.
func printNormal(format string, args ...any) {
	if verbosity >= VerbosityNormal {
		fmt.Printf(format, args...)
	}
}

// printVerbose writes to stderr only under --verbose. Timings and
// LLM prompt/response snippets route through here so they don't
// contaminate machine-parseable normal-mode output.
func printVerbose(format string, args ...any) {
	if verbosity >= VerbosityVerbose {
		fmt.Fprintf(os.Stderr, format, args...)
	}
}

// wrapClient returns c unchanged under quiet / normal verbosity. When
// --verbose is active, it returns a decorator that logs the first
// ~200 chars of every system + user prompt and every reply, plus the
// per-call wall clock. Useful for debugging ingest drift or
// contradictory LLM behaviour without flooding normal-mode output.
func wrapClient(c llm.Client) llm.Client {
	if c == nil || verbosity < VerbosityVerbose {
		return c
	}
	return &verboseClient{inner: c}
}

// snippetLen caps how many characters of a prompt / reply we log in
// verbose mode. Long enough to identify the call, short enough to
// keep the terminal readable.
const snippetLen = 200

type verboseClient struct {
	inner llm.Client
}

func (v *verboseClient) Complete(ctx context.Context, system, user string) (string, error) {
	started := time.Now()
	fmt.Fprintf(os.Stderr, "[llm] → system: %s\n", abbrev(system, snippetLen))
	fmt.Fprintf(os.Stderr, "[llm] → user:   %s\n", abbrev(user, snippetLen))
	reply, err := v.inner.Complete(ctx, system, user)
	elapsed := time.Since(started).Round(100 * time.Millisecond)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[llm] ← error after %s: %v\n", elapsed, err)
		return reply, err
	}
	fmt.Fprintf(os.Stderr, "[llm] ← reply (%s): %s\n", elapsed, abbrev(reply, snippetLen))
	return reply, err
}

func (v *verboseClient) Describe() string { return v.inner.Describe() + " [verbose]" }

// abbrev returns at most n runes of s, with "…" appended when
// truncated. Keeps whitespace collapsed so a multi-line prompt
// renders on one log line.
func abbrev(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}
