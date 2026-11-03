package adapter

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/creack/pty"
	"golang.org/x/term"
)

// PTY runs an arbitrary agent inside a pseudo-terminal, mirroring all I/O to
// the real terminal while teeing output into a capture callback. This is the
// universal fallback for agents without a hook system.
type PTY struct {
	mu   sync.Mutex
	ptmx *os.File
	done bool
	// early holds lines submitted before the agent started (a web message
	// drained in the gap between the sidecar and pty starting). They are
	// written the moment the pty exists; the tty buffers them until the
	// agent reads its input.
	early []string
	// OnInput, when set, sees every chunk the operator types before it is
	// forwarded to the agent (used to turn submitted lines into prompts).
	OnInput func([]byte)
}

// Notify prints an overlay line (e.g. a viewer comment) to the outer
// terminal, serialized against the output copy loop.
func (p *PTY) Notify(msg string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	os.Stdout.WriteString("\r\n\x1b[33m[live] " + msg + "\x1b[0m\r\n")
}

// Submit writes one line to the agent's stdin, as if it had been typed, and
// presses Enter. Serialized against Notify so an overlay cannot land mid-write.
//
// Callers must have decided that THIS agent accepts a typed line. Doing it for
// an arbitrary pty agent would be a command-injection hole: the pane may be a
// shell, and text arriving from a viewer would run on the operator's machine.
// Only known REPLs qualify — see canSubmitComments in the run path.
//
// text must already be a single sanitized line; a CR is appended here.
func (p *PTY) Submit(text string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.done {
		return nil // agent gone; nothing to type into
	}
	if p.ptmx == nil {
		p.early = append(p.early, text)
		return nil
	}
	_, err := p.ptmx.WriteString(text + "\r")
	return err
}

// headlessSize is the terminal geometry used when there is no real terminal to
// copy from. Wide enough that an agent's own wrapping does not shred the
// captured output into fragments.
var headlessSize = &pty.Winsize{Rows: 40, Cols: 120}

// resizePTY keeps the child's window size sensible.
//
// InheritSize fails when stdin is not a terminal, which is exactly the case for
// a session started by `live daemon`. Ignoring that failure leaves the pty at
// its default 0x0, and an agent asked to render into zero columns wraps every
// few characters — the web feed then shows a reply shredded into 4-character
// shards. Headless runs get an explicit size instead.
func resizePTY(ptmx *os.File) {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		_ = pty.InheritSize(os.Stdin, ptmx)
		return
	}
	_ = pty.Setsize(ptmx, headlessSize)
}

// Run starts the command and blocks until it exits. onChunk receives every
// raw output chunk (called from the copy goroutine).
func (p *PTY) Run(bin string, args []string, onChunk func([]byte)) (int, error) {
	cmd := exec.Command(bin, args...)
	cmd.Env = os.Environ()

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return 1, err
	}
	p.mu.Lock()
	p.ptmx = ptmx
	for _, line := range p.early {
		_, _ = ptmx.WriteString(line + "\r")
	}
	p.early = nil
	p.mu.Unlock()
	defer ptmx.Close()

	// Keep the child sized with the real terminal.
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	go func() {
		for range winch {
			resizePTY(ptmx)
		}
	}()
	winch <- syscall.SIGWINCH

	// Raw mode so keystrokes (incl. Ctrl-C) pass straight to the child.
	if term.IsTerminal(int(os.Stdin.Fd())) {
		oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
		if err == nil {
			defer term.Restore(int(os.Stdin.Fd()), oldState)
		}
	}

	go func() {
		if p.OnInput == nil {
			_, _ = io.Copy(ptmx, os.Stdin)
			return
		}
		in := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(in)
			if n > 0 {
				p.OnInput(in[:n])
				if _, werr := ptmx.Write(in[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	buf := make([]byte, 8192)
	for {
		n, rerr := ptmx.Read(buf)
		if n > 0 {
			p.mu.Lock()
			os.Stdout.Write(buf[:n])
			p.mu.Unlock()
			if onChunk != nil {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				onChunk(chunk)
			}
		}
		if rerr != nil {
			break // EIO on child exit is the normal end
		}
	}

	if err := cmd.Wait(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode(), nil
		}
		return 1, err
	}
	return 0, nil
}

// ── output scrubbing ─────────────────────────────────────────────────────────

var (
	ansiOSC = regexp.MustCompile(`\x1b\][^\x07\x1b]*(\x07|\x1b\\)`)
	// Braille-pattern glyphs are what CLI spinners animate with (ollama's
	// "thinking" ticker is ⠙⠹⠸⠼…). They are pure animation frames, so a capture
	// keeps a trail of them where the terminal showed one spinning character.
	// The trade: a session that legitimately prints Braille loses it. That is
	// vanishingly rare in an agent transcript, and unreadable output is the
	// certain cost of not doing this.
	spinner = regexp.MustCompile(`[\x{2800}-\x{28FF}][\x{2800}-\x{28FF} \t]*`)
)

// renderLine replays one line's cursor motion the way a terminal does, so the
// captured text is what a human SAW rather than every keystroke that produced
// it. It returns the line and the column the cursor was left at.
//
//   - \r returns to column 0 and \b steps back one; what follows OVERWRITES
//   - CSI n D / n C / n G move the cursor left / right / to a column
//   - CSI K erases to the end of the line (1 K: to the cursor, 2 K: all of it)
//   - every other escape sequence and control character is dropped
//
// Treating these as text or deleting them kept every intermediate frame of a
// redraw: "proces processing", ollama's grey placeholder glued onto the typed
// prompt (it prints the hint, steps back over it with CSI 28 D and erases it
// with CSI K on the first keystroke), and a duplicated word at a soft wrap
// (ollama steps back over the half-word, erases it and reprints it on the next
// line).
//
// ponytail: one rune = one column; wide (CJK/emoji) glyphs drift a cursor
// motion that follows them. Use go-runewidth here if that shows up in a feed.
func renderLine(line string) (string, int) {
	if !strings.ContainsAny(line, "\r\b\x1b") {
		return line, len([]rune(line))
	}
	var buf []rune
	col := 0
	rs := []rune(line)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case r == '\r':
			col = 0
		case r == '\b':
			if col > 0 {
				col--
			}
		case r == 0x1b && i+1 < len(rs) && rs[i+1] == '[':
			j := i + 2
			for j < len(rs) && rs[j] >= 0x20 && rs[j] <= 0x3f {
				j++
			}
			if j < len(rs) {
				buf, col = applyCSI(buf, col, string(rs[i+2:j]), rs[j])
			}
			i = j
		case r == 0x1b: // ESC + intermediates + final (charset selects etc.)
			for i+1 < len(rs) && rs[i+1] >= 0x20 && rs[i+1] <= 0x2f {
				i++
			}
			i++
		case r == '\t' || r >= 0x20 && r != 0x7f:
			for len(buf) < col {
				buf = append(buf, ' ')
			}
			if col < len(buf) {
				buf[col] = r
			} else {
				buf = append(buf, r)
			}
			col++
		}
	}
	return string(buf), col
}

// applyCSI applies the cursor/erase sequences that change what a line reads.
func applyCSI(buf []rune, col int, params string, final rune) ([]rune, int) {
	n, err := strconv.Atoi(strings.SplitN(params, ";", 2)[0])
	if err != nil {
		n = 0 // empty or private ("?25") parameter
	}
	step := max(n, 1)
	switch final {
	case 'D':
		col = max(col-step, 0)
	case 'C':
		col += step
	case 'G':
		col = step - 1
	case 'K':
		switch n {
		case 0:
			if col < len(buf) {
				buf = buf[:col]
			}
		case 1:
			for i := 0; i <= col && i < len(buf); i++ {
				buf[i] = ' '
			}
		case 2:
			for i := range buf {
				buf[i] = ' '
			}
		}
	}
	return buf, col
}

// render splits raw output into rendered lines; col is the cursor column on
// the last one.
func render(b []byte) (lines []string, col int) {
	s := ansiOSC.ReplaceAllString(string(b), "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines = strings.Split(s, "\n")
	for i, l := range lines {
		lines[i], col = renderLine(l)
	}
	return lines, col
}

// tidy drops spinner frames and trailing blanks, collapses blank runs and
// trims the block.
func tidy(lines []string) string {
	out := lines[:0]
	blank := 0
	for _, l := range lines {
		l = strings.TrimRight(spinner.ReplaceAllString(l, ""), " \t")
		if l == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// StripANSI renders captured terminal output (see renderLine) and removes
// escape sequences and control characters so it is readable in the web feed.
func StripANSI(b []byte) string {
	lines, _ := render(b)
	return tidy(lines)
}

// ── coalescing ───────────────────────────────────────────────────────────────

// Coalescer turns a PTY byte stream into feed rows (`output` events).
//
// A row is one or more WHOLE terminal lines. Complete lines are released once
// output has been idle for IdleGap; the line still being written is held until
// its newline arrives. Cutting on idle alone made a slow model (one token every
// few seconds) one row per token, and cut a soft-wrap redraw in half so the
// erase could no longer remove the half-word it erases.
//
// A held line is released early when it has been streaming for MaxHold, when
// the buffer reaches HardCap, or on Flush (operator input, agent exit). It
// stays as context so a later redraw still applies; only the text after what
// was already sent is emitted next time. Only text left of the cursor is sent,
// since anything to its right is a hint the next keystroke erases (ollama's
// "Send a message" placeholder).
//
// A line nothing new is written to is never force-released: that is a REPL
// prompt waiting for the operator, and the reply above it has already gone out
// on the idle cut. Not safe for concurrent use.
type Coalescer struct {
	IdleGap time.Duration
	MaxHold time.Duration
	HardCap int

	buf   []byte    // held-line context + bytes not yet emitted
	sent  string    // what of the held line has been emitted
	first time.Time // oldest unemitted write; zero when there is none
	last  time.Time
	dirty bool // written since the last idle check
}

// NewCoalescer returns the policy runPTYMode uses.
func NewCoalescer() *Coalescer {
	return &Coalescer{
		// Long enough to bridge the gaps between tokens of a normal-speed model,
		// short enough that the feed still feels live.
		IdleGap: 600 * time.Millisecond,
		// How long a line may stream before viewers see part of it. The working
		// heartbeat (Pending) tells them the agent is mid-answer meanwhile.
		MaxHold: 45 * time.Second,
		// Memory guard only.
		HardCap: 64 * 1024,
	}
}

// Write buffers a chunk. It returns text to emit when the buffer hit HardCap.
func (c *Coalescer) Write(b []byte, now time.Time) string {
	if c.first.IsZero() {
		c.first = now
	}
	c.last, c.dirty = now, true
	c.buf = append(c.buf, b...)
	if len(c.buf) >= c.HardCap {
		return c.flush(cursorCut)
	}
	return ""
}

// Pending reports output written but not yet emitted (heartbeat while held).
func (c *Coalescer) Pending() bool { return !c.first.IsZero() }

// Tick applies the idle and MaxHold cuts; call it periodically.
func (c *Coalescer) Tick(now time.Time) string {
	switch {
	case !c.first.IsZero() && now.Sub(c.first) >= c.MaxHold:
		return c.flush(wordCut)
	case c.dirty && now.Sub(c.last) >= c.IdleGap:
		return c.flush(lineCut)
	}
	return ""
}

// Flush emits everything, including the held line up to the cursor. Call it
// before an operator prompt and when the agent exits — the final output is
// never held back.
func (c *Coalescer) Flush() string { return c.flush(cursorCut) }

// How much of the held line a flush releases.
const (
	lineCut   = iota // none: complete lines only
	wordCut          // up to the last space left of the cursor, so a slow stream is not cut mid-word
	cursorCut        // up to the cursor
)

func (c *Coalescer) flush(cut int) string {
	c.dirty = false
	partial := cut != lineCut
	seg, rest := c.buf, []byte(nil)
	if !partial {
		nl := bytes.LastIndexByte(c.buf, '\n')
		if nl < 0 {
			return "" // only the held line; wait for its newline
		}
		seg, rest = c.buf[:nl+1], c.buf[nl+1:]
	} else if n := incompleteTail(c.buf); n > 0 {
		seg, rest = c.buf[:len(c.buf)-n], c.buf[len(c.buf)-n:]
	}
	lines, col := render(seg)
	sent := c.sent
	c.sent, c.first = "", time.Time{}
	if partial {
		// Keep the held line as context: its text and where the cursor was.
		held := []rune(lines[len(lines)-1])
		col = min(col, len(held))
		upto := col
		if cut == wordCut {
			if k := strings.LastIndexByte(string(held[:col]), ' '); k >= 0 {
				upto = len([]rune(string(held[:col])[:k+1]))
			}
		}
		lines[len(lines)-1] = string(held[:upto])
		c.sent = string(held[:upto])
		ctx := string(held) + "\r"
		if col > 0 {
			ctx += "\x1b[" + strconv.Itoa(col) + "C"
		}
		rest = append([]byte(ctx), rest...)
		if len(rest) > len(ctx) {
			c.first = c.last // an incomplete tail is still unemitted
		}
	}
	c.buf = rest

	// The first line was partly sent already: emit only what is new. When a
	// redraw erased sent text and reprinted it on the next line — a soft wrap
	// moving a half-written word down — skip the reprint too.
	l0 := []rune(lines[0])
	s := []rune(sent)
	cp := 0
	for cp < len(l0) && cp < len(s) && l0[cp] == s[cp] {
		cp++
	}
	lines[0] = string(l0[cp:])
	if erased := strings.TrimSpace(string(s[cp:])); erased != "" && strings.TrimSpace(lines[0]) == "" && len(lines) > 1 {
		lines[1] = strings.TrimPrefix(lines[1], erased)
	}
	return tidy(lines)
}

// incompleteTail is the length of a trailing escape sequence or UTF-8 rune a
// read cut in half; it waits for the rest instead of rendering as garbage.
func incompleteTail(b []byte) int {
	if e := bytes.LastIndexByte(b, 0x1b); e >= 0 && len(b)-e < 64 {
		t := b[e+1:]
		switch {
		case len(t) == 0:
			return len(b) - e
		case t[0] == '[':
			if i := bytes.IndexFunc(t[1:], func(r rune) bool { return r >= 0x40 && r <= 0x7e }); i < 0 {
				return len(b) - e
			}
		case t[0] == ']':
			if !bytes.ContainsAny(t, "\x07\\") {
				return len(b) - e
			}
		}
	}
	for n := 1; n <= 3 && n <= len(b); n++ {
		if r := b[len(b)-n]; r >= 0xc0 { // a lead byte
			if !utf8.FullRune(b[len(b)-n:]) {
				return n
			}
			return 0
		} else if r < 0x80 {
			return 0
		}
	}
	return 0
}
