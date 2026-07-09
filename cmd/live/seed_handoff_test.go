package main

import (
	"strings"
	"testing"

	"github.com/liveshortly/live/internal/api"
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

	// plain pty agents: nothing appended — the agent opens and waits.
	got2 := seedHandoff(b, "sess-gem", []string{"--only"}, false, "/usr/bin/gemini")
	if len(got2) != 1 || got2[0] != "--only" {
		t.Fatalf("pty agents should not append args, got %v", got2)
	}
}

// Codex has no --append-system-prompt-file; it is oriented with a
// `-c developer_instructions=<digest>` override instead. The override must be
// PREPENDED so the operator's positional prompt stays the last token, and it
// must never be auto-submitted as a turn.
func TestSeedHandoffOrientsCodex(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("LIVE_NO_CONTEXT", "1")
	t.Setenv("LIVE_NO_PAUSE", "1")
	t.Setenv("LIVE_CODEX_APPSERVER", "")

	b := api.HandoffBundle{
		Markdown: "# prior context\n- we were fixing the parser",
		Source:   api.HandoffSource{ID: "src-1", Title: "Source"},
	}

	got := seedHandoff(b, "sess-codex", []string{"do the task"}, false, "/usr/local/bin/codex")
	if len(got) != 3 || got[0] != "-c" {
		t.Fatalf("codex args should start with -c <override>, got %v", got)
	}
	if !strings.HasPrefix(got[1], "developer_instructions=") {
		t.Fatalf("expected developer_instructions override, got %q", got[1])
	}
	if !strings.Contains(got[1], "fixing the parser") {
		t.Fatalf("digest not carried into the override: %q", got[1])
	}
	if got[2] != "do the task" {
		t.Fatalf("operator's positional prompt must stay last, got %v", got)
	}
	// ...and initialPrompt must still pick the operator's prompt, not the -c value.
	if p := initialPrompt(got); p != "do the task" {
		t.Fatalf("initialPrompt picked %q, want the operator's prompt", p)
	}

	// app-server mode launches `codex app-server` and ignores userArgs, so no
	// -c override is added there.
	t.Setenv("LIVE_CODEX_APPSERVER", "1")
	got2 := seedHandoff(b, "sess-codex2", []string{"do the task"}, false, "/usr/local/bin/codex")
	if len(got2) != 1 || got2[0] != "do the task" {
		t.Fatalf("app-server mode should not add a -c override, got %v", got2)
	}
}

func TestCodexInstructionsClipsLargeDigest(t *testing.T) {
	if codexInstructions("   ") != "" {
		t.Fatal("empty digest → no override")
	}
	big := strings.Repeat("x", maxCodexInstructions+5000)
	out := codexInstructions(big)
	if len(out) > maxCodexInstructions+400 {
		t.Fatalf("oversized override (%d bytes) risks ARG_MAX", len(out))
	}
	if !strings.Contains(out, "truncated") {
		t.Fatal("clipped digest should say so")
	}
}
