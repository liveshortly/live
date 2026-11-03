package adapter

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode"
)

// ── typing a viewer message into a terminal agent (LSH-19) ──────────────────
//
// A terminal agent has no hook or channel, so the only way a viewer message
// reaches an idle one is to type it in. Typed blind it was lost while the
// agent booted, or it answered whatever yes/no dialog was on screen. TypeLine
// waits until the agent is idle at its prompt, types the line, confirms the
// echo, then presses Enter (once more if the agent doesn't react) — the same
// contract as the tmux composer typing for agy and gemini.

// Ready says when a terminal agent may be typed into.
type Ready struct {
	// Idle is how long output must have been quiet: a turn streams output,
	// so a quiet agent is waiting at its prompt.
	Idle time.Duration
	// Pattern, when set (agents.json ready_pattern), must match the last
	// lines on screen: the agent's own prompt marker.
	Pattern *regexp.Regexp
	// Hold, when set, reports that typing now would collide with the operator
	// (they have a half-typed line).
	Hold func() bool
}

// DefaultIdle is the quiet period Ready uses when Idle is zero.
const DefaultIdle = 1500 * time.Millisecond

// Timings for TypeLine; vars so tests can shorten them.
var (
	ReadyWait  = 20 * time.Second // then the operator is told what blocks it
	TypeWait   = 10 * time.Minute // give up on a turn that never ends
	AcceptWait = 5 * time.Second  // for the echo, and for a reaction to Enter
)

// busyMarkers are the "a turn is running" hints agent TUIs draw.
var busyMarkers = []string{"esc to interrupt", "esc to cancel", "ctrl+c to interrupt", "ctrl-c to interrupt"}

// modalMarkers are dialogs the Enter after a typed line would answer.
// ponytail: substring list over the last lines; add per-agent markers when an
// agent's dialog slips through.
var modalMarkers = []string{
	"(y)es/(n)o", "[y/n]", "(y/n)", "[yes/no]", "do you trust", "trust this folder",
	"trust the files", "allow once", "always allow", "press enter to continue",
	"sign in to", "log in to", "select an option",
}

// screenLines is how many trailing non-blank lines count as "on screen".
const screenLines = 6

// screen returns the last non-blank lines of output, rendered.
// ponytail: the output stream's tail, not a terminal emulator; an agent that
// redraws with absolute cursor moves shows its most recent writes, which is
// what the markers need. A vt emulator would give the real screen.
func screen(tail []byte) string {
	lines := strings.Split(StripANSI(tail), "\n")
	var out []string
	for i := len(lines) - 1; i >= 0 && len(out) < screenLines; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			out = append([]string{lines[i]}, out...)
		}
	}
	return strings.Join(out, "\n")
}

// blocker names what keeps the agent from taking a typed line now, or "".
func (r Ready) blocker(scr string, idle time.Duration, started bool) string {
	low := strings.ToLower(scr)
	for _, m := range modalMarkers {
		if strings.Contains(low, m) {
			return "the agent is waiting for an answer in the terminal"
		}
	}
	want := r.Idle
	if want == 0 {
		want = DefaultIdle
	}
	if !started || idle < want {
		return "the agent is busy"
	}
	for _, m := range busyMarkers {
		if strings.Contains(low, m) {
			return "the agent is busy"
		}
	}
	if r.Pattern != nil && !r.Pattern.MatchString(scr) {
		return "the agent's prompt is not on screen"
	}
	if r.Hold != nil && r.Hold() {
		return "the operator is typing"
	}
	return ""
}

// record keeps the output tail for readiness and echo checks. Caller holds mu.
func (p *PTY) record(b []byte, now time.Time) {
	const tailCap = 16 << 10
	p.tail = append(p.tail, b...)
	if len(p.tail) > tailCap {
		p.tail = append([]byte(nil), p.tail[len(p.tail)-tailCap:]...)
	}
	p.outN += len(b)
	p.lastOut = now
}

// state snapshots what readiness needs.
func (p *PTY) state() (scr string, idle time.Duration, started, done bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.lastOut.IsZero() {
		idle = time.Since(p.lastOut)
	}
	return screen(p.tail), idle, p.ptmx != nil && !p.lastOut.IsZero(), p.done
}

// since returns the rendered output written after mark (an outN value), and
// whether there was any.
func (p *PTY) since(mark int) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := min(p.outN-mark, len(p.tail))
	return StripANSI(p.tail[len(p.tail)-n:]), n > 0
}

// write types s into the agent and returns the output mark before it.
func (p *PTY) write(s string) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ptmx == nil || p.done {
		return 0, errors.New("the agent is not running")
	}
	mark := p.outN
	_, err := p.ptmx.WriteString(s)
	return mark, err
}

// waitOutput polls until output after mark satisfies cond, or d elapses.
func (p *PTY) waitOutput(mark int, cond func(string) bool, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		if out, ok := p.since(mark); ok && cond(out) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// squash drops whitespace, so an echo that wraps or redraws still matches.
func squash(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
}

// TypeLine types one sanitized line into the agent once r says it is ready,
// confirms it was echoed, and submits it. stuck, when set, is called once if
// the agent is still not ready after ReadyWait (TypeLine keeps waiting until
// TypeWait). A line that was never echoed is not submitted.
func (p *PTY) TypeLine(text string, r Ready, stuck func(why string)) error {
	start := time.Now()
	for {
		scr, idle, started, done := p.state()
		if done {
			return errors.New("the agent exited")
		}
		why := r.blocker(scr, idle, started)
		if why == "" {
			break
		}
		if time.Since(start) > TypeWait {
			return errors.New(why)
		}
		if stuck != nil && time.Since(start) > ReadyWait {
			stuck(why)
			stuck = nil
		}
		time.Sleep(200 * time.Millisecond)
	}

	probe := []rune(squash(text))
	if len(probe) > 16 {
		probe = probe[:16]
	}
	mark, err := p.write(text)
	if err != nil {
		return err
	}
	if !p.waitOutput(mark, func(out string) bool { return strings.Contains(squash(out), string(probe)) }, AcceptWait) {
		return errors.New("the agent did not show the typed message, so it was not sent")
	}
	for range 2 {
		// A gap before Enter, so a TUI's paste detection reads it as a key
		// press and submits rather than inserting a newline.
		time.Sleep(120 * time.Millisecond)
		if mark, err = p.write("\r"); err != nil {
			return err
		}
		if p.waitOutput(mark, func(string) bool { return true }, AcceptWait) {
			return nil
		}
	}
	return fmt.Errorf("the message is typed in but the agent did not take it")
}

// Stop asks the agent to exit (SIGTERM); Run then returns as usual.
func (p *PTY) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cmd != nil && p.cmd.Process != nil && !p.done {
		_ = p.cmd.Process.Signal(syscall.SIGTERM)
	}
}
