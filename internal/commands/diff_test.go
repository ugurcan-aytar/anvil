package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ugurcan-aytar/anvil/internal/wiki"
)

// TestDiffNoBaselineReportsAllAdded: fresh project, pages on disk,
// diff should label every page as Added.
func TestDiffNoBaselineReportsAllAdded(t *testing.T) {
	root := bootstrapProject(t)
	p := &wiki.Page{
		Filename: "page-a.md",
		Title:    "A",
		Type:     "concept",
		Body:     "body",
		Created:  "2026-04-16",
		Updated:  "2026-04-16",
	}
	if err := wiki.WritePage(filepath.Join(root, "wiki"), p); err != nil {
		t.Fatal(err)
	}
	var out string
	withProjectDir(t, root, func() {
		var err error
		out, err = captureStdout(t, func() error { return runDiff(context.Background()) })
		if err != nil {
			t.Fatalf("runDiff: %v", err)
		}
	})
	if !strings.Contains(out, "No previous snapshot") {
		t.Errorf("first-run banner missing; body:\n%s", out)
	}
	if !strings.Contains(out, "wiki/page-a.md") {
		t.Errorf("Added row missing; body:\n%s", out)
	}
}

// TestDiffDetectsAddedModifiedDeleted: write a baseline, mutate,
// verify all three buckets.
func TestDiffDetectsAddedModifiedDeleted(t *testing.T) {
	root := bootstrapProject(t)
	wikiDir := filepath.Join(root, "wiki")
	writePageHelper := func(name, body string) {
		p := &wiki.Page{
			Filename: name,
			Title:    name,
			Type:     "concept",
			Body:     body,
			Created:  "2026-04-16",
			Updated:  "2026-04-16",
		}
		if err := wiki.WritePage(wikiDir, p); err != nil {
			t.Fatal(err)
		}
	}
	writePageHelper("keep.md", "keep v1")
	writePageHelper("edit-me.md", "v1")
	writePageHelper("delete-me.md", "going away")

	snapPath := filepath.Join(root, ".anvil", wiki.SnapshotFilename)
	snap, _ := wiki.CaptureSnapshot(wikiDir)
	if err := wiki.SaveSnapshot(snapPath, snap); err != nil {
		t.Fatal(err)
	}

	// Mutations.
	writePageHelper("edit-me.md", "v2 updated body")
	if err := os.Remove(filepath.Join(wikiDir, "delete-me.md")); err != nil {
		t.Fatal(err)
	}
	writePageHelper("new-page.md", "fresh body")

	var out string
	withProjectDir(t, root, func() {
		out, _ = captureStdout(t, func() error { return runDiff(context.Background()) })
	})
	for _, want := range []string{
		"Added:",
		"wiki/new-page.md",
		"Modified:",
		"wiki/edit-me.md",
		"Deleted:",
		"wiki/delete-me.md",
		"3 change(s)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("diff missing %q; body:\n%s", want, out)
		}
	}
}

// TestDiffNoChanges: when snapshot matches the current state,
// report "No changes".
func TestDiffNoChanges(t *testing.T) {
	root := bootstrapProject(t)
	p := &wiki.Page{
		Filename: "static.md",
		Title:    "Static",
		Type:     "concept",
		Body:     "steady",
		Created:  "2026-04-16",
		Updated:  "2026-04-16",
	}
	if err := wiki.WritePage(filepath.Join(root, "wiki"), p); err != nil {
		t.Fatal(err)
	}
	snapPath := filepath.Join(root, ".anvil", wiki.SnapshotFilename)
	snap, _ := wiki.CaptureSnapshot(filepath.Join(root, "wiki"))
	_ = wiki.SaveSnapshot(snapPath, snap)

	var out string
	withProjectDir(t, root, func() {
		out, _ = captureStdout(t, func() error { return runDiff(context.Background()) })
	})
	if !strings.Contains(out, "No changes") {
		t.Errorf("clean state should say 'No changes'; body:\n%s", out)
	}
}
