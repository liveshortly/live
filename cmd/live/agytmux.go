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

// errAgyUnaccepted means the text was pasted but agy never took it: it is
// still sitting in the composer, so it must NOT also be queued for hooks.
type errAgyUnaccepted struct{}

func (errAgyUnaccepted) Error() string { return "agy didn't accept the message" }

// injectAgy types text into agy's composer once it is ready, submits it, and
// confirms agy took it (the composer no longer holds it), retrying Enter once.
func (t *tmuxPane) injectAgy(text string) error {
	if !t.waitFor(agyComposerReady, agyReadyWait) {
		if why := agyStartupBlocker(t.capture()); why != "" {
			return fmt.Errorf("%s", why)
		}
		return fmt.Errorf("agy's composer never appeared")
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
		s, ok := agyComposerLine(pane)
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
	return errAgyUnaccepted{}
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
