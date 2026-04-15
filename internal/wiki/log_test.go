package wiki

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAppendLogFresh(t *testing.T) {
	dir := t.TempDir()
	entry := LogEntry{
		Timestamp: time.Date(2026, 4, 15, 10, 30, 0, 0, time.UTC),
		Type:      LogTypeIngest,
		Title:     "interesting-paper.md",
		Created:   []string{"circuit-breaker-pattern.md", "retry-pattern.md"},
		Updated:   []string{"distributed-systems.md"},
		Sources:   []string{"raw/interesting-paper.md"},
	}
	if err := AppendLog(dir, entry); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, LogFilename))
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.HasPrefix(s, LogHeader) {
		t.Errorf("missing # Log header:\n%s", s)
	}
	want := "## [2026-04-15] ingest | interesting-paper.md"
	if !strings.Contains(s, want) {
		t.Errorf("missing grep-friendly heading %q:\n%s", want, s)
	}
	if !strings.Contains(s, "Created: circuit-breaker-pattern.md, retry-pattern.md") {
		t.Errorf("Created line missing:\n%s", s)
	}
	if !strings.Contains(s, "Sources: raw/interesting-paper.md") {
		t.Errorf("Sources line missing:\n%s", s)
	}
}

func TestAppendLogReplacesEmptySeed(t *testing.T) {
	dir := t.TempDir()
	// Simulate `anvil init` seeding an empty log.
	os.WriteFile(filepath.Join(dir, LogFilename), []byte(LogEmptyBody), 0o644)

	if err := AppendLog(dir, LogEntry{Type: LogTypeIngest, Title: "first.md"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, LogFilename))
	if strings.Contains(string(raw), "No entries yet.") {
		t.Errorf("empty-seed placeholder should be replaced:\n%s", raw)
	}
	if !strings.Contains(string(raw), "## [") {
		t.Errorf("entry heading missing:\n%s", raw)
	}
}

func TestAppendLogMultipleEntriesGrepable(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 3; i++ {
		entry := LogEntry{
			Timestamp: time.Date(2026, 4, 15+i, 10, 0, 0, 0, time.UTC),
			Type:      LogTypeIngest,
			Title:     "source-" + string(rune('a'+i)) + ".md",
		}
		if err := AppendLog(dir, entry); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := os.ReadFile(filepath.Join(dir, LogFilename))
	lines := strings.Split(string(raw), "\n")
	// grep -E "^## \[" equivalent — should produce exactly 3 lines.
	headings := 0
	for _, l := range lines {
		if strings.HasPrefix(l, "## [") {
			headings++
		}
	}
	if headings != 3 {
		t.Errorf("grep '^## \\[' should match 3 lines, got %d:\n%s", headings, raw)
	}
}

func TestAppendLogFreeFormDetails(t *testing.T) {
	dir := t.TempDir()
	entry := LogEntry{
		Type:    LogTypeQuery,
		Title:   `"how does circuit breaker work"`,
		Details: "Answer filed as: wiki/circuit-breaker-query.md",
	}
	if err := AppendLog(dir, entry); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, LogFilename))
	if !strings.Contains(string(raw), "Answer filed as: wiki/circuit-breaker-query.md") {
		t.Errorf("Details missing:\n%s", raw)
	}
}
