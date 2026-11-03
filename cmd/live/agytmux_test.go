package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Frames captured from a real agy 1.4.12 pane (no model call).
const (
	agyFrameSigningIn = `      ▄▀▀▄
  Welcome to the Antigravity CLI. You are currently not signed in.
  ⣷  Signing in...`
	agyFrameTrust = `Accessing workspace:
/tmp/agyprobe
Do you trust the contents of this project?
Antigravity CLI requires permission to read, edit, and execute files here.
> Yes, I trust this folder
  No, exit
  ↑/↓ Navigate · enter Confirm`
	agyFrameComposer = `  Antigravity CLI 1.4.12
  /tmp/agyprobe
 ┃ esc to dismiss
────────────────────────────────────────
> %s
────────────────────────────────────────
⚡ Gemini-3.8 Flash`
)

func TestAgyComposerLine(t *testing.T) {
	cases := []struct {
		name, pane, text string
		ok               bool
	}{
		{"boot splash (input is dropped here)", agyFrameSigningIn, "", false},
		{"trust prompt", agyFrameTrust, "", false},
		{"empty composer", strings.Replace(agyFrameComposer, "%s", "", 1), "", true},
		{"pending text", strings.Replace(agyFrameComposer, "%s", "alice (live viewer): hi", 1), "alice (live viewer): hi", true},
		{"quoted > in scrollback is not the composer", "> some markdown quote\nthinking…", "", false},
	}
	for _, c := range cases {
		text, ok := agyComposerLine(c.pane)
		if ok != c.ok || text != c.text {
			t.Errorf("%s: got (%q,%v), want (%q,%v)", c.name, text, ok, c.text, c.ok)
		}
	}
	if agyStartupBlocker(agyFrameTrust) == "" || agyStartupBlocker(agyFrameSigningIn) == "" {
		t.Error("trust / sign-in screens must be reported to the session")
	}
}

// fakeAgy mimics agy's boot: it shows the sign-in splash for a while and DROPS
// everything typed during it (what real agy does with paste+Enter there), then
// draws its composer and echoes each submitted line.
const fakeAgy = `#!/bin/bash
echo "  Welcome to the Antigravity CLI. You are currently not signed in."
echo "  Signing in..."
sleep 1.5
while read -r -t 0.2 _; do :; done
draw() { clear; echo "  Antigravity CLI"; echo "$1"; echo "──────────"; printf '> '; }
draw ""
while IFS= read -r line; do draw "SUBMITTED: $line"; done
`

// TestAgyFirstPromptDuringBoot is the LSH-15 regression: a viewer message sent
// within a second of starting agy must be delivered, not typed into the boot
// splash and lost.
func TestAgyFirstPromptDuringBoot(t *testing.T) {
	tmuxBin, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not installed")
	}
	script := filepath.Join(t.TempDir(), "agy")
	if err := os.WriteFile(script, []byte(fakeAgy), 0o755); err != nil {
		t.Fatal(err)
	}
	p := &tmuxPane{bin: tmuxBin, sock: "livetest-lsh15", sess: "agy-boot"}
	t.Cleanup(func() { _ = p.run("kill-server") })
	if out, err := p.output("new-session", "-d", "-s", p.sess, "-x", "100", "-y", "20", script); err != nil {
		t.Fatalf("new-session: %v: %s", err, out)
	}

	const msg = "alice (live viewer): hello; -n first"
	if err := p.injectAgy(msg); err != nil {
		t.Fatalf("injectAgy: %v\npane:\n%s", err, p.capture())
	}
	if pane := p.capture(); !strings.Contains(pane, "SUBMITTED: "+msg) {
		t.Fatalf("first message not delivered; pane:\n%s", pane)
	}
}

// TestAgyUnacceptedIsReported: text agy never takes must come back as
// errAgyUnaccepted (so it is surfaced, not silently queued twice).
func TestAgyUnacceptedIsReported(t *testing.T) {
	tmuxBin, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not installed")
	}
	// A composer that echoes input but never submits it.
	script := filepath.Join(t.TempDir(), "agy")
	if err := os.WriteFile(script, []byte("#!/bin/bash\nstty -icanon\necho ──────────\nprintf '> '\nsleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := agyAcceptWait
	agyAcceptWait = 500 * time.Millisecond
	t.Cleanup(func() { agyAcceptWait = old })
	p := &tmuxPane{bin: tmuxBin, sock: "livetest-lsh15b", sess: "agy-stuck"}
	t.Cleanup(func() { _ = p.run("kill-server") })
	if out, err := p.output("new-session", "-d", "-s", p.sess, "-x", "100", "-y", "20", script); err != nil {
		t.Fatalf("new-session: %v: %s", err, out)
	}
	if _, ok := p.injectAgy("bob (live viewer): stuck").(errAgyUnaccepted); !ok {
		t.Fatalf("want errAgyUnaccepted; pane:\n%s", p.capture())
	}
}
