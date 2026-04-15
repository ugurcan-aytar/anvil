// Append-only ingest / query / lint log.
//
// wiki/log.md records what anvil did and when. The format is
// grep-friendly: every entry leads with `## [YYYY-MM-DD] <type> |
// <title>` so a one-liner like
//
//   grep '^## \[' wiki/log.md | tail -5
//
// pulls the five most recent entries. Entries after the heading
// can carry arbitrary markdown — AppendLog formats the standard
// Created / Updated / Sources / Details shape but callers with a
// free-form Details string get it verbatim.

package wiki

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// LogFilename is the reserved log file.
const LogFilename = "log.md"

// LogHeader is the document-level title. Always one line, always
// at offset 0 — the append path does an os.OpenFile with O_APPEND,
// so a missing header means AppendLog writes it before the first
// entry.
const LogHeader = "# Log\n"

// LogEmptyBody is the anvil-init seed so the file exists with a
// sensible placeholder before the first entry lands.
const LogEmptyBody = LogHeader + "\nNo entries yet.\n"

// LogType is the vocabulary AppendLog accepts. Not enforced at the
// type level (plain string), but these are the values the log
// reader / dashboard code expects to see.
const (
	LogTypeIngest = "ingest"
	LogTypeQuery  = "query"
	LogTypeLint   = "lint"
	LogTypeSave   = "save"
)

// LogEntry is one grep-able record. Timestamp is required; the rest
// are optional and omitted from the rendered output when empty.
type LogEntry struct {
	Timestamp time.Time
	Type      string   // ingest / query / lint / save
	Title     string   // e.g. source filename, question text, lint run id
	Created   []string // wiki files created by this op
	Updated   []string // wiki files touched by this op
	Sources   []string // raw/ paths referenced
	Details   string   // free-form markdown body (trailing newline optional)
}

// AppendLog writes entry to wiki/log.md. Creates the file + header
// on the first call. If the file exists but is an `anvil init` seed
// (header only, no entries), the "No entries yet." placeholder gets
// replaced.
func AppendLog(wikiDir string, entry LogEntry) error {
	if err := os.MkdirAll(wikiDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", wikiDir, err)
	}
	path := filepath.Join(wikiDir, LogFilename)

	// Compose the new entry independently so we can either write it
	// after the seed's header or append to an established log.
	body := renderLogEntry(entry)

	existing, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("read %s: %w", path, err)
		}
		// Fresh log: header + first entry.
		return os.WriteFile(path, []byte(LogHeader+"\n"+body), 0o644)
	}
	// Seed-only log ("# Log\n\nNo entries yet.\n") → replace the
	// placeholder so the grep pattern stays clean.
	if strings.TrimSpace(string(existing)) == strings.TrimSpace(LogEmptyBody) {
		return os.WriteFile(path, []byte(LogHeader+"\n"+body), 0o644)
	}
	// Established log → append with a single blank-line separator.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	separator := ""
	if !strings.HasSuffix(string(existing), "\n\n") {
		if strings.HasSuffix(string(existing), "\n") {
			separator = "\n"
		} else {
			separator = "\n\n"
		}
	}
	_, err = f.WriteString(separator + body)
	return err
}

func renderLogEntry(e LogEntry) string {
	ts := e.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}
	typ := e.Type
	if typ == "" {
		typ = "event"
	}
	title := e.Title
	if title == "" {
		title = "-"
	}
	var b strings.Builder
	b.Grow(128 + len(e.Details))
	fmt.Fprintf(&b, "## [%s] %s | %s\n", ts.Format("2006-01-02"), typ, title)
	if len(e.Created) > 0 {
		fmt.Fprintf(&b, "Created: %s\n", strings.Join(e.Created, ", "))
	}
	if len(e.Updated) > 0 {
		fmt.Fprintf(&b, "Updated: %s\n", strings.Join(e.Updated, ", "))
	}
	if len(e.Sources) > 0 {
		fmt.Fprintf(&b, "Sources: %s\n", strings.Join(e.Sources, ", "))
	}
	if strings.TrimSpace(e.Details) != "" {
		// Ensure a blank line between the structured block above
		// and a free-form Details body.
		if len(e.Created)+len(e.Updated)+len(e.Sources) > 0 {
			b.WriteByte('\n')
		}
		d := strings.TrimRight(e.Details, "\n")
		b.WriteString(d)
		b.WriteByte('\n')
	}
	return b.String()
}
