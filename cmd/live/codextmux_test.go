package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestComposerReady(t *testing.T) {
	cases := []struct {
		name string
		pane string
		want bool
	}{
		{"empty pane", "", false},
		{"composer prompt", "  ›  \n  send a message", true},
		{"ask codex placeholder", "Ask Codex to do something", true},
		{"trust dialog blocks", "Do you trust the contents of this directory?\n  ›  Yes", false},
		{"approval modal blocks", "Allow Codex to run `rm -rf`?\n › Yes, allow", false},
		{"no composer marker", "thinking…", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := composerReady(c.pane); got != c.want {
				t.Fatalf("composerReady(%q) = %v, want %v", c.pane, got, c.want)
			}
		})
	}
}

// TestViewerTurnIsSkippedByRolloutMapper guards the contract between the two
// halves of tmux mode: the text we type into codex must be recognised by the
// rollout mapper, or every viewer comment is double-counted in the feed (once
// as viewer_comment, once as the prompt codex "received").
func TestViewerTurnIsSkippedByRolloutMapper(t *testing.T) {
	turn := viewerTurn("alice", "refactor the parser")
	if !looksLikeViewerInput(turn) {
		t.Fatalf("rollout mapper would emit %q as a user prompt (double-count)", turn)
	}
	if looksLikeViewerInput("please refactor the parser") {
		t.Fatal("a genuine operator prompt must not be skipped")
	}
}

// TestInjectIntoCodexTUI drives the REAL codex TUI in a real tmux session and
// asserts a viewer comment lands in its composer. It only pastes (no Enter), so
// it costs no model call. Opt in with LIVE_E2E=1.
func TestInjectIntoCodexTUI(t *testing.T) {
	if os.Getenv("LIVE_E2E") != "1" {
		t.Skip("set LIVE_E2E=1 to run (needs tmux + codex + a trusted cwd)")
	}
	tmuxBin, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not installed")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex not installed")
	}

	p := &tmuxPane{bin: tmuxBin, sock: "livetest", sess: "e2e-inject"}
	_ = p.run("kill-session", "-t", p.sess)
	if out, err := p.output("new-session", "-d", "-s", p.sess, "-x", "120", "-y", "40", "codex"); err != nil {
		t.Fatalf("new-session: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = p.run("kill-session", "-t", p.sess) })

	if !p.waitComposer(30 * time.Second) {
		t.Fatalf("composer never became ready; pane:\n%s", p.capture())
	}

	// Arbitrary user input: a semicolon and a leading dash would break a naive
	// send-keys implementation.
	const comment = "alice (live viewer): -n refactor; ship it"
	if err := p.paste(comment); err != nil {
		t.Fatalf("paste: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if strings.Contains(p.capture(), "refactor; ship it") {
			return // landed
		}
		if time.Now().After(deadline) {
			t.Fatalf("comment never appeared in composer; pane:\n%s", p.capture())
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// TestInjectBlockedByModal asserts we refuse to type into a modal rather than
// silently answering the wrong question.
func TestInjectBlockedByModal(t *testing.T) {
	if os.Getenv("LIVE_E2E") != "1" {
		t.Skip("set LIVE_E2E=1 to run")
	}
	tmuxBin, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not installed")
	}
	p := &tmuxPane{bin: tmuxBin, sock: "livetest", sess: "e2e-modal"}
	_ = p.run("kill-session", "-t", p.sess)
	// A pane that never shows a composer.
	if out, err := p.output("new-session", "-d", "-s", p.sess, "-x", "80", "-y", "10",
		"sh", "-c", "printf 'Do you trust the contents of this directory?\\n'; sleep 30"); err != nil {
		t.Fatalf("new-session: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = p.run("kill-session", "-t", p.sess) })

	time.Sleep(500 * time.Millisecond)
	err = p.inject("should not be typed")
	if err == nil {
		t.Fatal("inject succeeded into a modal; expected refusal")
	}
	if !strings.Contains(err.Error(), "composer not ready") {
		t.Fatalf("unexpected error: %v", err)
	}
}
