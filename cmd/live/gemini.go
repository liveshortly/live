package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/liveshortly/live/internal/adapter"
	"github.com/liveshortly/live/internal/api"
	"github.com/liveshortly/live/internal/sidecar"
)

func isGemini(bin string) bool { return filepath.Base(bin) == "gemini" }

// geminiProfile captures gemini through its hooks (>= 0.26) and keeps raw PTY
// capture for older releases, which have no hook system.
func geminiProfile(bin string) agentProfile {
	if adapter.GeminiHooksSupported(bin) {
		return agentProfile{framework: "gemini-cli", captureMode: "hooks"}
	}
	return agentProfile{framework: "gemini-cli", captureMode: "pty"}
}

// geminiOwnApproval reports whether the operator chose gemini's approval mode
// themselves (--yolo / -y / --approval-mode …). live then stands down as the
// gate, the way it does for claude's --dangerously-skip-permissions.
func geminiOwnApproval(args []string) bool {
	for _, a := range args {
		if a == "--yolo" || a == "-y" || a == "--approval-mode" || strings.HasPrefix(a, "--approval-mode=") {
			return true
		}
	}
	return false
}

// geminiLaunch prepares a hooked gemini run: it installs the (inert) hooks
// extension and returns the args and the env that switch it on for this
// process only.
//
// With web permissions, live's BeforeTool hook is the gate. A hook "allow"
// does not skip gemini's own prompt (gemini checks policy after hooks), so
// gemini runs with --approval-mode=yolo and the gated hook decides; it fails
// closed (`live hook … --gated`). That trade is only made when the hook is
// certain to fire (adapter.GeminiGateBlocked); otherwise gemini's own prompt
// stays in charge. In an untrusted folder gemini refuses yolo by itself, so
// a tool is asked twice there (web, then gemini) — never zero times.
//
// A web-started (headless) run trusts the workspace for this process only: the
// daemon spawns solely in directories the operator registered, and nobody is
// there to answer gemini's trust dialog.
func geminiLaunch(sc *sidecar.Sidecar, self string, userArgs []string) (args, env []string, err error) {
	if err := adapter.EnsureGeminiExtension(); err != nil {
		return nil, nil, fmt.Errorf("gemini hooks extension: %w", err)
	}
	args = adapter.GeminiArgs(userArgs)
	env = []string{"LIVE_BIN=" + self}
	if headless() {
		env = append(env, "GEMINI_CLI_TRUST_WORKSPACE=true")
	}
	if sc.WebPermissions {
		cwd, _ := os.Getwd()
		if why := adapter.GeminiGateBlocked(cwd, args); why != "" {
			sc.WebPermissions = false
			fmt.Fprintf(os.Stderr, "\x1b[33m● live\x1b[0m gemini's own prompts approve tools this run: %s\n\n", why)
			sc.Log.Printf("gemini: web approvals off: %s", why)
		} else {
			args = append([]string{"--approval-mode=yolo"}, args...)
			env = append(env, "LIVE_GEMINI_GATE=--gated")
		}
	}
	return args, env, nil
}

// ── viewer messages into gemini's composer (LSH-17) ─────────────────────────
//
// gemini takes viewer messages at hook boundaries (BeforeTool/AfterTool/
// AfterAgent), and an idle gemini fires no hook, so a message sent to a
// web-started gemini waited until someone typed in the terminal. Like agy
// (LSH-15), an idle gemini now has the message typed into its composer; a
// busy one still gets it at its next hook.
//
// gemini 0.66 at rest (captured with network blocked, see the tests):
//
//	────────────────────────────────────
//	 Shift+Tab to accept edits
//
//	 >   Type your message or @path/to/file
//
// and while a turn runs a spinner line ends in "(esc to cancel, 3s)" above
// that rule. Older releases drew the composer in a ╭─╮ │ > … │ box.

const geminiPlaceholder = "Type your message"

// geminiBusy reports that a turn is running (the spinner's cancel hint).
func geminiBusy(pane string) bool { return strings.Contains(pane, "esc to cancel") }

// geminiStartupBlocker names a screen that needs the operator, or "".
func geminiStartupBlocker(pane string) string {
	low := strings.ToLower(pane)
	switch {
	case strings.Contains(low, "do you trust"):
		return "gemini is waiting for you to trust this folder in the terminal"
	case strings.Contains(low, "how would you like to authenticate") || strings.Contains(low, "waiting for auth"):
		return "gemini is waiting for sign-in — check the terminal"
	case strings.Contains(low, "allow execution") || strings.Contains(low, "apply this change") || strings.Contains(low, "do you want to proceed"):
		return "gemini is waiting for a tool approval in the terminal"
	}
	return ""
}

// geminiComposerLine returns the text in gemini's composer and whether gemini
// is idle at it: the bottom-most "> " line, with a rule at most three lines
// above, no dialog and no running turn. "" is the empty (placeholder) composer.
func geminiComposerLine(pane string) (text string, ok bool) {
	if geminiBusy(pane) || geminiStartupBlocker(pane) != "" {
		return "", false
	}
	lines := strings.Split(pane, "\n")
	for i := len(lines) - 1; i > 0; i-- {
		l := strings.TrimSpace(strings.Trim(strings.TrimSpace(lines[i]), "│"))
		if !strings.HasPrefix(l, ">") {
			continue
		}
		for j := i - 1; j >= 0 && j >= i-3; j-- {
			if strings.Contains(lines[j], "───") {
				s := strings.TrimSpace(strings.TrimPrefix(l, ">"))
				if strings.HasPrefix(s, geminiPlaceholder) {
					s = ""
				}
				return s, true
			}
		}
		return "", false // the bottom-most "> " is scrollback, not the composer
	}
	return "", false
}

func geminiComposerReady(pane string) bool { _, ok := geminiComposerLine(pane); return ok }

// geminiTurnWait bounds how long a message queued during a busy turn is
// watched for a turn end that no hook delivered it at.
// ponytail: fixed cap; past it the message just stays queued for the next hook.
var geminiTurnWait = 10 * time.Minute

// deliverGemini gets c to gemini exactly once. Idle at its composer: type it
// in. Busy: queue it for the next hook — and if the turn ends without one
// taking it (the turn's last hook fired just before it was queued), take it
// back and type it. Unqueue and the hooks share a lock, so only one wins.
// Returns errUnaccepted when the text was typed but never left the composer.
func (t *tmuxPane) deliverGemini(sc *sidecar.Sidecar, c api.Comment, text string) error {
	var pane string
	if !t.waitFor(func(p string) bool { pane = p; return geminiBusy(p) || geminiComposerReady(p) }, agyReadyWait) {
		if why := geminiStartupBlocker(pane); why != "" {
			return fmt.Errorf("%s", why)
		}
		return fmt.Errorf("gemini's composer never appeared")
	}
	if geminiBusy(pane) {
		sc.QueueForHooks(c)
		if !t.waitFor(geminiComposerReady, geminiTurnWait) || !sc.Unqueue(c) {
			return nil // a hook delivered it, or one still will
		}
		sc.Log.Printf("live gemini tmux: turn ended with a viewer comment undelivered; typing it")
	}
	return t.injectComposer("gemini", geminiComposerLine, geminiStartupBlocker, text)
}
