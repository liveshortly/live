package main

import (
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liveshortly/live/internal/api"
	"github.com/liveshortly/live/internal/sidecar"
)

// Frames from a real gemini 0.66.0 pane in tmux (110x32), started with a
// throwaway HOME, a dummy API key and outbound network denied by sandbox-exec,
// so no model call was made and nothing could auto-update (LSH-17).
const (
	geminiFrameIdle = `Tips for getting started:
4. Be specific for the best results

                                                                                              ? for shortcuts
──────────────────────────────────────────────────────────────────────────────────────────────────────────────
 Shift+Tab to accept edits

 >   Type your message or @path/to/file

 workspace (/directory)                                                          sandbox               /model
 /private/.../gemwork                                                            no sandbox              Auto`
	geminiFrameBusy = `4. Be specific for the best results

 > alice (live viewer): hello there



 ⠏ This is taking a bit longer, we're still on it. (esc to cancel, 25s)
──────────────────────────────────────────────────────────────────────────────────────────────────────────────
 Shift+Tab to accept edits

 > hi

 workspace (/directory)                                                          sandbox               /model`
	geminiFrameTyped = `ℹ Request cancelled.

──────────────────────────────────────────────────────────────────────────────────────────────────────────────
 Shift+Tab to accept edits

 > hi

 workspace (/directory)                                                          sandbox               /model`
	// Pre-0.30 releases boxed the composer.
	geminiFrameBoxed = `╭──────────────────────────────────────────╮
│ >   Type your message or @path/to/file   │
╰──────────────────────────────────────────╯`
	geminiFrameTrust = `Do you trust this folder?
● 1. Trust folder (gemwork)
  2. Trust parent folder
  3. Don't trust`
)

func TestGeminiComposerLine(t *testing.T) {
	cases := []struct {
		name, pane, text string
		ok               bool
	}{
		{"idle, empty composer", geminiFrameIdle, "", true},
		{"idle, text pending", geminiFrameTyped, "hi", true},
		{"older boxed composer", geminiFrameBoxed, "", true},
		{"busy: composer drawn but a turn is running", geminiFrameBusy, "", false},
		{"trust dialog", geminiFrameTrust, "", false},
		{"booting: nothing drawn", "", "", false},
		{"echoed prompt in scrollback is not the composer", "Tips\n\n > alice (live viewer): hi\n\nresponse…", "", false},
	}
	for _, c := range cases {
		text, ok := geminiComposerLine(c.pane)
		if ok != c.ok || text != c.text {
			t.Errorf("%s: got (%q,%v), want (%q,%v)", c.name, text, ok, c.text, c.ok)
		}
	}
	if !geminiBusy(geminiFrameBusy) || geminiBusy(geminiFrameIdle) {
		t.Error("busy detection")
	}
	if geminiStartupBlocker(geminiFrameTrust) == "" {
		t.Error("trust dialog must be reported to the session")
	}
}

// fakeGemini boots (dropping anything typed meanwhile), optionally runs a turn
// ("busy" arg), then sits at its composer; each submitted line runs a short
// turn that echoes it. Input typed during a turn is dropped, so the tests fail
// if live types into a busy gemini.
const fakeGemini = `#!/bin/bash
frame() { clear; echo "$1"; echo "$2"; echo "──────────"; echo; printf ' >   Type your message or @path/to/file'; printf '\r > '; }
turn() { frame "$1" "⠋ Thinking (esc to cancel, 1s)"; sleep 2; while read -r -t 0.1 _; do :; done; frame "$1" ""; }
echo "Initializing..."; sleep 1; while read -r -t 0.1 _; do :; done
log=""
if [ "$1" = busy ]; then turn "(startup turn)"; else frame "" ""; fi
while IFS= read -r line; do log="$log SUBMITTED: $line"; turn "$log"; done
`

func startFakeGemini(t *testing.T, sock string, args ...string) *tmuxPane {
	t.Helper()
	tmuxBin, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not installed")
	}
	script := filepath.Join(t.TempDir(), "gemini")
	if err := os.WriteFile(script, []byte(fakeGemini), 0o755); err != nil {
		t.Fatal(err)
	}
	p := &tmuxPane{bin: tmuxBin, sock: sock, sess: "gemini"}
	t.Cleanup(func() { _ = p.run("kill-server") })
	if out, err := p.output(append([]string{"new-session", "-d", "-s", p.sess, "-x", "100", "-y", "20", script}, args...)...); err != nil {
		t.Fatalf("new-session: %v: %s", err, out)
	}
	return p
}

func quietSidecar() *sidecar.Sidecar { return &sidecar.Sidecar{Log: log.New(io.Discard, "", 0)} }

// TestGeminiFirstMessageDuringBoot is the LSH-17 regression: a message sent
// while gemini boots is held, then typed in once gemini is idle at its
// composer — not left on the hook queue an idle gemini never drains.
func TestGeminiFirstMessageDuringBoot(t *testing.T) {
	p := startFakeGemini(t, "livetest-lsh17a")
	sc := quietSidecar()
	c := api.Comment{ID: "1", Username: "alice", Message: "hello; -n first"}
	msg := viewerTurn(c.Username, c.Message)
	if err := p.deliverGemini(sc, c, msg); err != nil {
		t.Fatalf("deliverGemini: %v\npane:\n%s", err, p.capture())
	}
	if !p.waitFor(func(s string) bool { return strings.Contains(s, "SUBMITTED: "+msg) }, 3*time.Second) {
		t.Fatalf("first message not delivered; pane:\n%s", p.capture())
	}
	if sc.Unqueue(c) {
		t.Fatal("typed message was also left on the hook queue (double delivery)")
	}
}

// A message that arrives mid-turn goes to the hook queue. If the turn ends
// without a hook taking it, it is taken back and typed — once.
func TestGeminiBusyMessageTypedWhenTurnEndsUndelivered(t *testing.T) {
	p := startFakeGemini(t, "livetest-lsh17b", "busy")
	if !p.waitFor(geminiBusy, 5*time.Second) {
		t.Fatalf("fake never got busy:\n%s", p.capture())
	}
	sc := quietSidecar()
	c := api.Comment{ID: "2", Username: "bob", Message: "while busy"}
	msg := viewerTurn(c.Username, c.Message)
	if err := p.deliverGemini(sc, c, msg); err != nil {
		t.Fatalf("deliverGemini: %v\npane:\n%s", err, p.capture())
	}
	if !p.waitFor(func(s string) bool { return strings.Count(s, "SUBMITTED: "+msg) == 1 }, 3*time.Second) {
		t.Fatalf("want the message typed exactly once; pane:\n%s", p.capture())
	}
}

// When a hook takes the message during the turn, it must not also be typed.
func TestGeminiBusyMessageTakenByHookIsNotTyped(t *testing.T) {
	p := startFakeGemini(t, "livetest-lsh17c", "busy")
	if !p.waitFor(geminiBusy, 5*time.Second) {
		t.Fatalf("fake never got busy:\n%s", p.capture())
	}
	sc := quietSidecar()
	c := api.Comment{ID: "3", Username: "carol", Message: "via hook"}
	go func() { // the AfterTool/AfterAgent hook draining the queue mid-turn
		for !sc.Unqueue(c) {
			time.Sleep(50 * time.Millisecond)
		}
	}()
	if err := p.deliverGemini(sc, c, viewerTurn(c.Username, c.Message)); err != nil {
		t.Fatalf("deliverGemini: %v", err)
	}
	time.Sleep(time.Second)
	if pane := p.capture(); strings.Contains(pane, "via hook") {
		t.Fatalf("hook-delivered message was typed too; pane:\n%s", pane)
	}
}
