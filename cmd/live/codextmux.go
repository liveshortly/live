package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/liveshortly/live/internal/api"
	"github.com/liveshortly/live/internal/perm"
	"github.com/liveshortly/live/internal/sidecar"
)

// runCodexTmuxMode runs the real codex TUI inside an isolated tmux session and
// PUSHES viewer comments into it as keystrokes. It is the codex analogue of
// runHooksTmuxMode: the operator drives codex normally, the web drives it too.
//
// Why tmux rather than a bare PTY: codex is a standard MCP client, so it only
// sees viewer comments when it decides to call the poll_viewer_messages tool —
// which an IDLE codex never does. tmux gives a scriptable handle on the pane
// (send-keys to type, capture-pane to see what's on screen), so a comment lands
// in codex's composer the moment it arrives, idle or mid-turn. Feed events
// still come from the structured rollout tailer, not from scraping the screen.
func runCodexTmuxMode(sc *sidecar.Sidecar, binPath string, userArgs []string) int {
	tmuxBin, err := exec.LookPath("tmux")
	if err != nil {
		fmt.Fprintln(os.Stderr, "live: tmux not found — falling back to PTY rollout mode")
		return runRolloutMode(sc, binPath, userArgs)
	}

	const sock = "liveshortly"
	sess := "live-" + sc.SessionID[:8]
	t := &tmuxPane{bin: tmuxBin, sock: sock, sess: sess}
	_ = t.run("kill-session", "-t", sess) // clear any stale session

	// Permissions: the PreToolUse hook blocks on the broker, which races a
	// display-popup answer against the web verdict — same contract as claude.
	broker := perm.New(func(ctx context.Context) (string, error) {
		return sc.Client.GetDecision(sc.SessionID)
	}, 8*time.Minute)
	sc.Broker = broker

	// Viewer comments: type them straight into codex's composer. OnComment (not
	// DisplayComment) is the SOLE delivery path — DisplayComment is additive and
	// would leave the comment queued for hook injection too, delivering it twice.
	sc.OnComment = func(c api.Comment) {
		who := c.Username
		if who == "" {
			who = "viewer"
		}
		if err := t.inject(viewerTurn(who, c.Message)); err != nil {
			sc.Log.Printf("codextmux: inject: %v", err)
		}
	}
	sc.Start(context.Background())

	addr, token, err := sc.StartHookServer()
	if err != nil {
		fmt.Fprintf(os.Stderr, "live: sidecar failed: %v\n", err)
		return 1
	}
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "live: %v\n", err)
		return 1
	}

	// Gate codex's tool use through LiveShortly. No channel MCP here: push via
	// send-keys replaces the poll_viewer_messages pull entirely.
	codexArgs := userArgs
	if codexPermsWanted(userArgs) {
		sc.WebPermissions = true
		codexArgs = append(codexPermArgs(self), codexArgs...)
	}

	before := snapshotRollouts()
	cwd, _ := os.Getwd()
	tctx, stopTail := context.WithCancel(context.Background())
	tailDone := make(chan struct{})
	go func() {
		defer close(tailDone)
		tailNewRollout(tctx, sc, before, cwd)
	}()

	// On a fork, print the prior context inside the pane (it lands in scrollback)
	// rather than outside it, where attach-session would repaint over it.
	agent := handoffShimArgs(self, sc.SessionID, append([]string{binPath}, codexArgs...))

	newArgs := []string{"new-session", "-d", "-s", sess,
		"-e", "LIVE_SIDECAR=" + addr,
		"-e", "LIVE_TOKEN=" + token,
		"-e", "LIVE_SESSION_ID=" + sc.SessionID,
		"-e", "LIVE_HOOK_TIMEOUT=590"}
	newArgs = append(newArgs, agent...)
	if out, err := t.output(newArgs...); err != nil {
		stopTail()
		<-tailDone
		fmt.Fprintf(os.Stderr, "live: tmux new-session failed: %v: %s\n", err, out)
		return 1
	}
	_ = t.run("set-option", "-t", sess, "status", "off")
	bindContextPopup(t.run, self, sc.SessionID)

	// popup driver: raise a display-popup whenever a permission is pending
	popupCmd := fmt.Sprintf("%q perm-prompt %q %q", self, addr, token)
	go func() {
		for range broker.Notify() {
			_ = t.run("display-popup", "-t", sess, "-w", "76", "-h", "9", "-E", popupCmd)
		}
	}()

	fmt.Fprint(os.Stderr, "\x1b[32m● live\x1b[0m codex in tmux — viewer messages type straight into the composer.\n")

	att := t.cmd("attach-session", "-t", sess)
	att.Stdin, att.Stdout, att.Stderr = os.Stdin, os.Stdout, os.Stderr
	signal.Ignore(os.Interrupt, syscall.SIGQUIT)
	defer signal.Reset(os.Interrupt, syscall.SIGQUIT)
	_ = att.Run()

	_ = t.run("kill-session", "-t", sess)
	stopTail()
	<-tailDone
	return 0
}

// viewerTurn formats a comment for the composer. The "(live viewer):" marker is
// load-bearing: the rollout mapper uses it to skip the turn, which is already
// shown in the feed as a viewer_comment event.
func viewerTurn(who, message string) string {
	return fmt.Sprintf("%s (live viewer): %s", who, message)
}

// ── tmux pane handle ─────────────────────────────────────────────────────────

type tmuxPane struct {
	bin  string
	sock string
	sess string
}

func (t *tmuxPane) cmd(args ...string) *exec.Cmd {
	c := exec.Command(t.bin, append([]string{"-L", t.sock}, args...)...)
	// nested-tmux guard: our isolated socket must not inherit $TMUX.
	c.Env = envWithout(os.Environ(), "TMUX", "TMUX_PANE")
	return c
}

func (t *tmuxPane) run(args ...string) error { return t.cmd(args...).Run() }

func (t *tmuxPane) output(args ...string) ([]byte, error) {
	return t.cmd(args...).CombinedOutput()
}

// capture returns the visible pane contents.
func (t *tmuxPane) capture() string {
	out, err := t.cmd("capture-pane", "-t", t.sess, "-p").Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// composerReady reports whether codex is showing its input composer, so typed
// text lands in the prompt rather than in a modal (trust dialog, slash menu,
// native approval). Injecting blind is how a comment silently answers the wrong
// question.
func composerReady(pane string) bool {
	if pane == "" {
		return false
	}
	low := strings.ToLower(pane)
	for _, modal := range []string{"do you trust", "allow codex to", "yes, allow", "no, and tell"} {
		if strings.Contains(low, modal) {
			return false
		}
	}
	return strings.Contains(pane, "›") || strings.Contains(pane, "Ask Codex")
}

// inject types text into codex's composer and submits it. The text goes through
// a tmux paste buffer rather than send-keys arguments: a comment is arbitrary
// user input, and tmux would otherwise interpret `;` as a command separator and
// leading `-` as a flag. -p uses bracketed paste so an embedded newline cannot
// submit the turn early.
func (t *tmuxPane) inject(text string) error {
	if !t.waitComposer(5 * time.Second) {
		return fmt.Errorf("composer not ready (modal on screen?) — comment not delivered")
	}
	if err := t.paste(text); err != nil {
		return err
	}
	// Settle: the TUI needs a beat to absorb the paste before Enter submits it.
	time.Sleep(120 * time.Millisecond)
	return t.submit()
}

// paste types text into the composer without submitting it.
func (t *tmuxPane) paste(text string) error {
	buf := "live-" + t.sess
	load := t.cmd("load-buffer", "-b", buf, "-")
	load.Stdin = bytes.NewReader([]byte(text))
	if out, err := load.CombinedOutput(); err != nil {
		return fmt.Errorf("load-buffer: %v: %s", err, out)
	}
	if out, err := t.output("paste-buffer", "-b", buf, "-t", t.sess, "-d", "-p"); err != nil {
		return fmt.Errorf("paste-buffer: %v: %s", err, out)
	}
	return nil
}

// submit presses Enter, sending the composed text as a turn.
func (t *tmuxPane) submit() error {
	if out, err := t.output("send-keys", "-t", t.sess, "Enter"); err != nil {
		return fmt.Errorf("send-keys Enter: %v: %s", err, out)
	}
	return nil
}

// waitComposer polls the pane until codex is accepting input, or gives up.
func (t *tmuxPane) waitComposer(d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		if composerReady(t.capture()) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
}
