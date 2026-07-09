package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDigestPathFor(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("LIVE_SESSION_ID", "")

	// A bare session id resolves under the handoff dir.
	got := digestPathFor("abc123")
	if filepath.Base(got) != "abc123.md" {
		t.Fatalf("session id should resolve to <id>.md, got %q", got)
	}

	// A literal path is used as-is — the tmux popup binding passes one.
	if p := digestPathFor("/tmp/x/y.md"); p != "/tmp/x/y.md" {
		t.Fatalf("literal path must pass through, got %q", p)
	}

	// Falls back to the session this process belongs to.
	t.Setenv("LIVE_SESSION_ID", "sess-9")
	if p := digestPathFor(""); filepath.Base(p) != "sess-9.md" {
		t.Fatalf("empty arg should fall back to LIVE_SESSION_ID, got %q", p)
	}

	t.Setenv("LIVE_SESSION_ID", "")
	if p := digestPathFor(""); p != "" {
		t.Fatalf("no id anywhere → empty, got %q", p)
	}
}

// A normal (non-fork) run must not pay for the shim: no digest, no wrapper.
func TestHandoffShimArgsOnlyWrapsForks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	agent := []string{"/usr/bin/codex", "-c", "k=v", "do the task"}

	got := handoffShimArgs("/usr/local/bin/live", "no-such-session", agent)
	if len(got) != len(agent) || got[0] != agent[0] {
		t.Fatalf("non-fork must launch the agent directly, got %v", got)
	}

	// Now create the digest: the launch is wrapped, and the agent argv survives
	// intact after the `--` separator (its args carry a 40KB -c override).
	dir := filepath.Join(home, ".liveshortly", "handoffs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "sess-1.md")
	if err := os.WriteFile(path, []byte("# prior\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got = handoffShimArgs("/usr/local/bin/live", "sess-1", agent)
	want := []string{"/usr/local/bin/live", "handoff-shim", path, "--"}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("arg %d = %q, want %q (full: %v)", i, got[i], w, got)
		}
	}
	rest := got[len(want):]
	if len(rest) != len(agent) {
		t.Fatalf("agent argv mangled: %v", rest)
	}
	for i := range agent {
		if rest[i] != agent[i] {
			t.Fatalf("agent argv[%d] = %q, want %q", i, rest[i], agent[i])
		}
	}
}
