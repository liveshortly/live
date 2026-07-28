package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The daemon's own checks are the real defense against a spawn command being
// more than "run a known agent in a directory the operator named" — the server
// runs the same checks, but this process must not depend on that. These cover
// the flag parsing that builds the allowlist and the id alphabet.

func TestParseDaemonFlags(t *testing.T) {
	dir := t.TempDir()

	o, err := parseDaemonFlags([]string{"--dir", dir, "--name", "laptop"})
	if err != nil {
		t.Fatalf("parseDaemonFlags: %v", err)
	}
	if o.name != "laptop" {
		t.Errorf("name = %q, want laptop", o.name)
	}
	// Paths are absolutised and cleaned so the later membership check can be a
	// plain string compare against a cleaned incoming path.
	if len(o.dirs) != 1 || o.dirs[0] != filepath.Clean(dir) {
		t.Errorf("dirs = %v, want [%s]", o.dirs, filepath.Clean(dir))
	}

	// A file, a missing path and a bare flag are all refused rather than
	// silently registered — a bad allowlist entry is a security bug.
	file := filepath.Join(dir, "notadir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"--dir", file},
		{"--dir", filepath.Join(dir, "does-not-exist")},
		{"--dir"},
		{"--name"},
		{"--nonsense"},
	} {
		if _, err := parseDaemonFlags(args); err == nil {
			t.Errorf("parseDaemonFlags(%v) = nil error, want refusal", args)
		}
	}

	if _, err := parseDaemonFlags([]string{"--help"}); err != flagHelp {
		t.Error("--help should return the flagHelp sentinel")
	}
}

func TestSanitizeHostID(t *testing.T) {
	for _, s := range []string{"h0123abcd", "laptop-1", "A_b"} {
		if got := sanitizeHostID(s); got != s {
			t.Errorf("sanitizeHostID(%q) = %q, want unchanged", s, got)
		}
	}
	// A corrupted or hand-edited host_id file must be replaced, not sent.
	for _, s := range []string{"", "  ", "has space", "a/b", "a;rm -rf /", "../../etc"} {
		if got := sanitizeHostID(s); got != "" {
			t.Errorf("sanitizeHostID(%q) = %q, want rejected", s, got)
		}
	}
}

func TestContains(t *testing.T) {
	dirs := []string{"/work/a", "/work/b"}
	if !contains(dirs, "/work/a") {
		t.Error("contains missed an exact member")
	}
	// Prefix and traversal must NOT pass: spawnSession cleans the incoming path
	// and then requires exact membership.
	for _, d := range []string{"/work", "/work/a/sub", "/work/ab", "/work/a/../b"} {
		if contains(dirs, d) {
			t.Errorf("contains accepted %q, want reject", d)
		}
	}
}

func TestParseRunFlagsAttach(t *testing.T) {
	o, rest, err := parseRunFlags([]string{"--attach", "abc123", "claude"})
	if err != nil {
		t.Fatalf("parseRunFlags: %v", err)
	}
	if o.attachID != "abc123" {
		t.Errorf("attachID = %q, want abc123", o.attachID)
	}
	if len(rest) != 1 || rest[0] != "claude" {
		t.Errorf("rest = %v, want [claude]", rest)
	}

	if _, _, err := parseRunFlags([]string{"--attach"}); err == nil {
		t.Error("--attach without a value should error")
	}
	// Attaching to an existing session and forking a new one are contradictory.
	if _, _, err := parseRunFlags([]string{"--attach", "a", "--session", "b", "claude"}); err == nil {
		t.Error("--attach with --session should error")
	}
}
