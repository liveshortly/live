package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

// The orphan sweep decides whether to archive somebody's session, so both of
// its inputs are worth pinning: how a pane's environment is read, and what
// counts as "the sidecar is still there".

func TestParseShowEnv(t *testing.T) {
	if got := parseShowEnv("LIVE_SIDECAR=http://127.0.0.1:51234\n"); got != "http://127.0.0.1:51234" {
		t.Errorf("set variable parsed as %q", got)
	}
	// tmux reports an unset variable as "-KEY" — that must not read as a value.
	for _, out := range []string{"-LIVE_SIDECAR\n", "", "   "} {
		if got := parseShowEnv(out); got != "" {
			t.Errorf("parseShowEnv(%q) = %q, want empty", out, got)
		}
	}
}

func TestSidecarAlive(t *testing.T) {
	// The probe is unauthenticated, so a live sidecar answers 403 — which is
	// still proof it is there. Only a refused connection means gone.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	if !sidecarAlive(srv.URL) {
		t.Error("403 from a live sidecar must count as alive — else a running session gets archived")
	}
	srv.Close()
	if sidecarAlive(srv.URL) {
		t.Error("a closed listener must read as dead")
	}
}

// A tmux server on our socket outlives any single session and seeds new
// sessions from its OWN environment for anything outside update-environment
// (which lists neither HOME nor PATH). Pinning these per-session is what stops
// session #2 from silently running with session #1's HOME — the failure that
// drops the agent back into its first-run wizard.
func TestPinnedEnvArgs(t *testing.T) {
	t.Setenv("HOME", "/tmp/pinned-home")
	t.Setenv("LIVESHORTLY_API_URL", "http://localhost:8080")
	t.Setenv("LIVESHORTLY_CRED_PATH", "/tmp/creds.json")

	args := pinnedEnvArgs()

	// Every entry must be a well-formed `-e KEY=VALUE` pair.
	if len(args)%2 != 0 {
		t.Fatalf("odd argument count %d: %v", len(args), args)
	}
	got := map[string]string{}
	for i := 0; i < len(args); i += 2 {
		if args[i] != "-e" {
			t.Fatalf("args[%d] = %q, want -e", i, args[i])
		}
		k, v, ok := strings.Cut(args[i+1], "=")
		if !ok {
			t.Fatalf("malformed pair %q", args[i+1])
		}
		got[k] = v
	}

	for k, want := range map[string]string{
		"HOME":                  "/tmp/pinned-home",
		"LIVESHORTLY_API_URL":   "http://localhost:8080",
		"LIVESHORTLY_CRED_PATH": "/tmp/creds.json",
	} {
		if got[k] != want {
			t.Errorf("%s pinned as %q, want %q", k, got[k], want)
		}
	}

	// An unset variable must be omitted, not pinned to "" — an empty PATH or
	// HOME in the pane is worse than inheriting one.
	t.Setenv("LIVESHORTLY_WEB_URL", "")
	os.Unsetenv("LIVESHORTLY_WEB_URL")
	for _, a := range pinnedEnvArgs() {
		if strings.HasPrefix(a, "LIVESHORTLY_WEB_URL=") {
			t.Errorf("unset variable was pinned: %q", a)
		}
	}
}
