package main

import (
	"strings"
	"testing"

	"github.com/resapce/live/internal/api"
)

// On a fork the operator reads the history and drives — seedHandoff must NOT
// auto-submit a "continue" prompt. For claude it loads the digest as appended
// system context so the agent is oriented but idle; other agents get nothing
// appended (they open and wait).
func TestSeedHandoffDoesNotAutoContinue(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // sandbox HandoffDir writes
	t.Setenv("LIVE_NO_CONTEXT", "1")
	t.Setenv("LIVE_NO_PAUSE", "1")

	b := api.HandoffBundle{
		Markdown: "# prior context\n- did a thing",
		Source:   api.HandoffSource{ID: "src-1", Title: "Source"},
	}

	// claude: appends --append-system-prompt-file <path>, no positional prompt.
	got := seedHandoff(b, "sess-claude", []string{"--model", "opus"}, false, "/usr/local/bin/claude")
	if n := len(got); n < 2 || got[n-2] != "--append-system-prompt-file" {
		t.Fatalf("claude args should end with --append-system-prompt-file <path>, got %v", got)
	}
	for _, a := range got {
		if strings.Contains(a, "continuing a handed-off") || strings.Contains(a, "restate the state") {
			t.Fatalf("must not auto-submit a continue prompt, got %v", got)
		}
	}

	// non-claude (pty/codex): nothing appended — the agent opens and waits.
	got2 := seedHandoff(b, "sess-gem", []string{"--only"}, false, "/usr/bin/gemini")
	if len(got2) != 1 || got2[0] != "--only" {
		t.Fatalf("non-claude should not append args, got %v", got2)
	}
}
