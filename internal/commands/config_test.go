package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestConfigSetGetRoundTrip: set → persisted to disk → get reads it back.
func TestConfigSetGetRoundTrip(t *testing.T) {
	root := bootstrapProject(t)
	withProjectDir(t, root, func() {
		if err := runConfigSet("model", "claude-sonnet-4-6"); err != nil {
			t.Fatalf("set model: %v", err)
		}
		if err := runConfigSet("topk", "20"); err != nil {
			t.Fatalf("set topk: %v", err)
		}
		out, err := captureStdout(t, func() error { return runConfigGet("model") })
		if err != nil {
			t.Fatalf("get model: %v", err)
		}
		if strings.TrimSpace(out) != "claude-sonnet-4-6" {
			t.Errorf("get model = %q", strings.TrimSpace(out))
		}
		out, err = captureStdout(t, func() error { return runConfigGet("topk") })
		if err != nil {
			t.Fatalf("get topk: %v", err)
		}
		if strings.TrimSpace(out) != "20" {
			t.Errorf("get topk = %q", strings.TrimSpace(out))
		}
	})
	// File shape is JSON — sanity check.
	raw, err := os.ReadFile(filepath.Join(root, ".anvil", ConfigFilename))
	if err != nil {
		t.Fatalf("config file: %v", err)
	}
	for _, want := range []string{`"model"`, `"topk"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("config.json missing %q; body:\n%s", want, raw)
		}
	}
}

// TestConfigListShowsAllKeys: list should enumerate every recognised
// key even when nothing is set.
func TestConfigListShowsAllKeys(t *testing.T) {
	root := bootstrapProject(t)
	var out string
	withProjectDir(t, root, func() {
		var err error
		out, err = captureStdout(t, func() error { return runConfigList() })
		if err != nil {
			t.Fatalf("list: %v", err)
		}
	})
	for key := range configFields {
		if !strings.Contains(out, key+":") {
			t.Errorf("list missing %q; body:\n%s", key, out)
		}
	}
}

// TestConfigSetRejectsUnknownKey: typos surface immediately.
func TestConfigSetRejectsUnknownKey(t *testing.T) {
	root := bootstrapProject(t)
	withProjectDir(t, root, func() {
		err := runConfigSet("nonsense", "x")
		if err == nil {
			t.Fatal("unknown key should error")
		}
		if !strings.Contains(err.Error(), "unknown config key") {
			t.Errorf("err = %v", err)
		}
	})
}

// TestConfigSetRejectsInvalidValue: setters parse integers / bools;
// garbage input fails early.
func TestConfigSetRejectsInvalidValue(t *testing.T) {
	root := bootstrapProject(t)
	withProjectDir(t, root, func() {
		if err := runConfigSet("topk", "abc"); err == nil {
			t.Error("non-int topk should error")
		}
		if err := runConfigSet("workers", "0"); err == nil {
			t.Error("workers must be >= 1")
		}
		if err := runConfigSet("auto-save", "maybe"); err == nil {
			t.Error("non-bool auto-save should error")
		}
	})
}

// TestLoadConfigMissingFile: fresh project (no config.json yet) → empty struct, no error.
func TestLoadConfigMissingFile(t *testing.T) {
	root := bootstrapProject(t)
	c, err := LoadConfig(root)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if c.Model != "" || c.TopK != 0 {
		t.Errorf("missing file should yield zero-value config; got %+v", c)
	}
}
