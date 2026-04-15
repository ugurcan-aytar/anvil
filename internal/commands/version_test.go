package commands

import "testing"

// TestResolveVersionInfoDefaults: on a dev build (no ldflags),
// placeholders should be friendlier than the literal "unknown"
// string the package vars default to.
func TestResolveVersionInfoDefaults(t *testing.T) {
	prevV, prevC, prevD := Version, Commit, BuildDate
	t.Cleanup(func() { Version, Commit, BuildDate = prevV, prevC, prevD })

	Version = "dev"
	Commit = "unknown"
	BuildDate = "unknown"

	v, c, d := resolveVersionInfo()
	if v != "dev" {
		t.Errorf("version = %q, want dev", v)
	}
	if c != "HEAD" {
		t.Errorf("commit = %q, want HEAD", c)
	}
	if d != "local" {
		t.Errorf("built = %q, want local", d)
	}
}

// TestResolveVersionInfoReleaseValues: when ldflags inject real
// values, passthrough is the whole behaviour.
func TestResolveVersionInfoReleaseValues(t *testing.T) {
	prevV, prevC, prevD := Version, Commit, BuildDate
	t.Cleanup(func() { Version, Commit, BuildDate = prevV, prevC, prevD })

	Version = "0.2.7"
	Commit = "abc1234"
	BuildDate = "2026-04-16T03:00:00Z"

	v, c, d := resolveVersionInfo()
	if v != "0.2.7" || c != "abc1234" || d != "2026-04-16T03:00:00Z" {
		t.Errorf("resolveVersionInfo mangled release values: %q %q %q", v, c, d)
	}
}
