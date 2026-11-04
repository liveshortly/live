package main

import (
	"fmt"
	"strings"
	"time"
)

// agy's composer is a "> " line directly under a horizontal rule, at the bottom
// of the screen. Until it is drawn agy is booting ("Signing in..."), and
// anything typed then is silently dropped — paste and Enter both report
// success, so the first viewer message vanished without a trace (LSH-15).
// composerReady's loose markers ("Antigravity", "agy", ">") all match the boot
// splash (and any cwd containing "agy"), so agy gets its own check.

// agyComposerLine returns the text in agy's composer and whether the composer
// is on screen and accepting input.
func agyComposerLine(pane string) (text string, ok bool) {
	low := strings.ToLower(pane)
	if strings.Contains(low, "signing in") || strings.Contains(low, "do you trust") {
		return "", false
	}
	lines := strings.Split(pane, "\n")
	for i := len(lines) - 1; i > 0; i-- {
		l := strings.TrimSpace(lines[i])
		if strings.HasPrefix(l, ">") && strings.Contains(lines[i-1], "───") {
			return strings.TrimSpace(strings.TrimPrefix(l, ">")), true
		}
	}
	return "", false
}

func agyComposerReady(pane string) bool { _, ok := agyComposerLine(pane); return ok }

// agyStartupBlocker names a startup screen that needs the operator, or "".
func agyStartupBlocker(pane string) string {
	low := strings.ToLower(pane)
	switch {
	case strings.Contains(low, "do you trust"):
		return "agy is waiting for you to approve this folder in the terminal"
	case strings.Contains(low, "signing in") || strings.Contains(low, "not signed in"):
		return "agy is still signing in — check the terminal"
	}
	return ""
}

// agyTimeouts: how long to wait for the composer before the first inject, and
// how long a submitted message may sit in the composer before we call it lost.
var agyReadyWait, agyAcceptWait = 20 * time.Second, 5 * time.Second

// errUnaccepted means the text was pasted but the agent never took it: it is
// still sitting in the composer, so it must NOT also be queued for hooks.
type errUnaccepted struct{ agent string }

func (e errUnaccepted) Error() string { return e.agent + " didn't accept the message" }

// injectAgy types text into agy's composer once it is ready (LSH-15).
func (t *tmuxPane) injectAgy(text string) error {
	return t.injectComposer("agy", agyComposerLine, agyStartupBlocker, text)
}

// injectComposer types text into an agent's composer once composer() reports
// it on screen and accepting input, submits it, and confirms the agent took
// it (the composer no longer holds it), retrying Enter once.
func (t *tmuxPane) injectComposer(agent string, composer func(string) (string, bool), blocker func(string) string, text string) error {
	ready := func(pane string) bool { _, ok := composer(pane); return ok }
	if !t.waitFor(ready, agyReadyWait) {
		if why := blocker(t.capture()); why != "" {
			return fmt.Errorf("%s", why)
		}
		return fmt.Errorf("%s's composer never appeared", agent)
	}
	if err := t.paste(text); err != nil {
		return err
	}
	// Probe on the head of the first line: the composer may wrap or truncate.
	probe := strings.SplitN(text, "\n", 2)[0]
	if len(probe) > 24 {
		probe = probe[:24]
	}
	gone := func(pane string) bool {
		s, ok := composer(pane)
		return !ok || !strings.Contains(s, probe)
	}
	for _, key := range []string{"Enter", "C-m"} {
		time.Sleep(120 * time.Millisecond)
		if out, err := t.output("send-keys", "-t", t.sess, key); err != nil {
			return fmt.Errorf("send-keys %s: %v: %s", key, err, out)
		}
		if t.waitFor(gone, agyAcceptWait) {
			return nil
		}
	}
	return errUnaccepted{agent}
}

// waitFor polls the pane until cond holds or d elapses.
func (t *tmuxPane) waitFor(cond func(string) bool, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		if cond(t.capture()) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// ── claude with channels blocked (LSH-20) ───────────────────────────────────
//
// claude hears an idle viewer through the `live channel` MCP. An org policy can
// switch development channels off; claude then prints a warning and silently
// drops every inbound message, while the channel subscriber still connects —
// so live reported each comment as delivered and claude never saw one. When
// the warning is on screen, comments are typed into the composer instead.

// claudeChannelBlocked reports claude's "channels blocked" startup warning.
func claudeChannelBlocked(pane string) bool {
	return strings.Contains(pane, "blocked by org policy") ||
		strings.Contains(pane, "Inbound messages will be silently dropped")
}

// claudeComposerLine returns the text in claude's composer ("❯ " directly
// under a rule) and whether it is on screen.
func claudeComposerLine(pane string) (text string, ok bool) {
	if promptOnScreen(pane) >= 0 {
		return "", false
	}
	lines := strings.Split(pane, "\n")
	for i := len(lines) - 1; i > 0; i-- {
		l := strings.TrimSpace(lines[i])
		if strings.HasPrefix(l, "❯") && strings.Contains(lines[i-1], "───") {
			return strings.TrimSpace(strings.TrimPrefix(l, "❯")), true
		}
	}
	return "", false
}

// claudeStartupBlocker names a startup screen that needs the operator, or "".
func claudeStartupBlocker(pane string) string {
	if promptOnScreen(pane) >= 0 {
		return "claude is waiting at a startup prompt in the terminal"
	}
	return ""
}
