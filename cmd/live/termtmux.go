package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/liveshortly/live/internal/adapter"
	"github.com/liveshortly/live/internal/api"
	"github.com/liveshortly/live/internal/sidecar"
)

// runTerminalTmuxMode runs an agent with no hooks or rollout log (pi, ollama,
// agents.json entries) in its own tmux session (LSH-20).
//
// It replaces the bare PTY for these agents because the PTY broke both ways
// for full-screen TUIs: the session had no pane to attach to, and the output
// coalescer waits for newlines a TUI that repaints with cursor moves never
// writes, so the web showed nothing but "working". tmux renders the screen
// for us — capture-pane is the screen as the operator sees it — and gives
// the same send-keys handle the agy/codex modes type viewer messages with.
func runTerminalTmuxMode(sc *sidecar.Sidecar, tmuxBin, binPath string, userArgs []string) int {
	const sock = "liveshortly"
	sess := "live-" + sc.SessionID[:8]
	t := &tmuxPane{bin: tmuxBin, sock: sock, sess: sess}
	_ = t.run("kill-session", "-t", sess) // clear any stale session

	notice := func(msg string) {
		sc.Emit("input_requested", map[string]any{"message": msg, "kind": "input", "ts": time.Now().UTC().Format(time.RFC3339)})
	}
	attachHint := fmt.Sprintf("open the terminal (`tmux -L %s attach -t %s`)", sock, sess)

	// Viewer comments are typed in by one worker, in order, each once the
	// screen is idle at a prompt — same contract as runPTYMode.
	pattern := readyPattern(sc)
	canType := canSubmitComments(binPath)
	queue := make(chan api.Comment, 64)
	go func() {
		for c := range queue {
			who := c.Username
			if who == "" {
				who = "viewer"
			}
			line := viewerLine(who, c.Message)
			if line == "" {
				continue
			}
			if err := t.typeTerminal(line, pattern, func(why string) {
				sc.Log.Printf("live tmux: comment from %s waiting: %s", who, why)
				notice(why + " — the viewer message is typed in once it is idle at its prompt")
			}); err != nil {
				sc.Log.Printf("live tmux: comment from %s not delivered: %v", who, err)
				notice(fmt.Sprintf("message not delivered to the agent: %v — %s to continue", err, attachHint))
				continue
			}
			sc.Log.Printf("live tmux: delivered comment from %s", who)
		}
	}()
	sc.OnComment = func(c api.Comment) {
		if !canType {
			notice("this agent can't take messages from the web — " + attachHint)
			return
		}
		select {
		case queue <- c:
		default:
			notice("too many messages waiting; this one was not sent to the agent")
		}
	}
	sc.OnEnded = func() { _ = t.run("kill-session", "-t", sess) }
	sc.Start(context.Background())

	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "live: %v\n", err)
		return 1
	}
	agent := handoffShimArgs(self, sc.SessionID, append([]string{binPath}, userArgs...))
	// A detached session defaults to 80x24, which wraps most TUIs into
	// unreadable capture; give it a desktop-terminal size.
	newArgs := []string{"new-session", "-d", "-s", sess, "-x", "160", "-y", "48",
		"-e", "LIVE_SESSION_ID=" + sc.SessionID, "-e", "LIVE_AGENT_NAME=" + sc.Agent}
	newArgs = append(newArgs, pinnedEnvArgs()...)
	newArgs = append(newArgs, agent...)
	if out, err := t.output(newArgs...); err != nil {
		fmt.Fprintf(os.Stderr, "live: tmux new-session failed: %v: %s\n", err, out)
		return 1
	}
	_ = t.run("set-option", "-t", sess, "status", "off")

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		t.streamScreen(sc, stop)
	}()

	fmt.Fprintf(os.Stderr, "\x1b[32m● live\x1b[0m %s in tmux — attach with: tmux -L %s attach -t %s\n", sc.Agent, sock, sess)
	exitCode := attachOrWait(t.cmd, sess)
	close(stop)
	<-done
	_ = t.run("kill-session", "-t", sess)
	return exitCode
}

// screenSettle is how long the pane must stay unchanged before it counts as
// idle: a turn repaints continuously, an agent at its prompt doesn't.
var screenSettle = 1500 * time.Millisecond

// streamScreen emits what the agent printed as `output` events. Each time the
// screen settles it emits the lines that were not on the previously emitted
// screen; while it is changing it heartbeats `stream_start` so the web's
// "working" badge stays lit (see runPTYMode).
func (t *tmuxPane) streamScreen(sc *sidecar.Sidecar, stop <-chan struct{}) {
	var last, emitted string
	var changed, pinged time.Time
	tick := time.NewTicker(300 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			if d := newScreenLines(emitted, last); d != "" {
				sc.Emit("output", map[string]any{"text": d, "ts": time.Now().UTC().Format(time.RFC3339)})
			}
			return
		case now := <-tick.C:
			scr := t.capture()
			if scr == "" {
				continue
			}
			if scr != last {
				last, changed = scr, now
				if emitted != "" && now.Sub(pinged) >= 2*time.Second {
					pinged = now
					sc.Emit("stream_start", map[string]any{"ts": now.UTC().Format(time.RFC3339)})
				}
				continue
			}
			if now.Sub(changed) < screenSettle || scr == emitted {
				continue
			}
			if d := newScreenLines(emitted, scr); d != "" {
				const maxText = 8000
				if len(d) > maxText {
					d = clipLine(d, maxText)
				}
				sc.Emit("output", map[string]any{"text": d, "ts": now.UTC().Format(time.RFC3339)})
			}
			emitted = scr
		}
	}
}

// newScreenLines returns the non-blank lines of cur that prev did not have
// (as a multiset, so a repeated line counts), in screen order.
// ponytail: line-set diff, not scrollback tracking; a status line whose
// numbers change is re-emitted, and a line that scrolled off between two
// settles is missed. capture-pane -S with history would close that.
func newScreenLines(prev, cur string) string {
	seen := map[string]int{}
	for _, l := range strings.Split(prev, "\n") {
		seen[strings.TrimRight(l, " ")]++
	}
	var out []string
	for _, l := range strings.Split(cur, "\n") {
		l = strings.TrimRight(l, " ")
		if strings.TrimSpace(l) == "" || boxOnly(l) {
			continue
		}
		if seen[l] > 0 {
			seen[l]--
			continue
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

// boxOnly reports a line made only of rule/box-drawing characters.
func boxOnly(l string) bool {
	for _, r := range strings.TrimSpace(l) {
		if r < 0x2500 || r > 0x257F {
			return false
		}
	}
	return true
}

// lastLines returns the last n non-blank lines of a pane capture.
func lastLines(pane string, n int) string {
	lines := strings.Split(pane, "\n")
	var out []string
	for i := len(lines) - 1; i >= 0 && len(out) < n; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			out = append([]string{lines[i]}, out...)
		}
	}
	return strings.Join(out, "\n")
}

// typeTerminal types one line into a terminal agent once its screen has
// settled with no dialog or running turn on it (and, when set, the agent's
// ready pattern matching), confirms the text shows up, then submits it.
// stuck is called once if the agent is still not ready after ReadyWait.
func (t *tmuxPane) typeTerminal(text string, pattern *regexp.Regexp, stuck func(why string)) error {
	start := time.Now()
	var prev string
	var since time.Time
	for {
		scr := t.capture()
		if scr == "" && t.run("has-session", "-t", t.sess) != nil {
			return fmt.Errorf("the agent exited")
		}
		if scr != prev {
			prev, since = scr, time.Now()
		}
		tail := lastLines(scr, 6)
		why := adapter.ScreenBlocker(tail)
		if why == "" && time.Since(since) < screenSettle {
			why = "the agent is busy"
		}
		if why == "" && pattern != nil && !pattern.MatchString(tail) {
			why = "the agent's prompt is not on screen"
		}
		if why == "" {
			break
		}
		if time.Since(start) > adapter.TypeWait {
			return fmt.Errorf("%s", why)
		}
		if stuck != nil && time.Since(start) > adapter.ReadyWait {
			stuck(why)
			stuck = nil
		}
		time.Sleep(200 * time.Millisecond)
	}

	probe := text
	if r := []rune(probe); len(r) > 16 {
		probe = string(r[:16])
	}
	if err := t.paste(text); err != nil {
		return err
	}
	shown := func(pane string) bool { return strings.Contains(squashSpaces(pane), squashSpaces(probe)) }
	if !t.waitFor(shown, adapter.AcceptWait) {
		return fmt.Errorf("the agent did not show the typed message, so it was not sent")
	}
	before := lastLines(t.capture(), 3)
	for _, key := range []string{"Enter", "C-m"} {
		time.Sleep(120 * time.Millisecond)
		if out, err := t.output("send-keys", "-t", t.sess, key); err != nil {
			return fmt.Errorf("send-keys %s: %v: %s", key, err, out)
		}
		if t.waitFor(func(p string) bool { return lastLines(p, 3) != before }, adapter.AcceptWait) {
			return nil
		}
	}
	return fmt.Errorf("the message is typed in but the agent did not take it")
}

// squashSpaces drops all whitespace, so a probe still matches when the
// agent's composer wrapped it.
func squashSpaces(s string) string { return strings.Join(strings.Fields(s), "") }

// tmuxAvailable returns tmux's path, or "" when it is not installed.
func tmuxAvailable() string {
	p, err := exec.LookPath("tmux")
	if err != nil {
		return ""
	}
	return p
}
