package adapter

import (
	"io"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"syscall"

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
	if p.ptmx == nil || p.done {
		return nil // agent gone; nothing to type into
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
	ansiCSI  = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
	ansiOSC  = regexp.MustCompile(`\x1b\][^\x07\x1b]*(\x07|\x1b\\)`)
	ansiMisc = regexp.MustCompile(`\x1b[@-_]`)
	ctrl     = regexp.MustCompile(`[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]`)
	// Braille-pattern glyphs are what CLI spinners animate with (ollama's
	// "thinking" ticker is ⠙⠹⠸⠼…). They are pure animation frames, so a capture
	// keeps a trail of them where the terminal showed one spinning character.
	// The trade: a session that legitimately prints Braille loses it. That is
	// vanishingly rare in an agent transcript, and unreadable output is the
	// certain cost of not doing this.
	spinner = regexp.MustCompile(`[\x{2800}-\x{28FF}][\x{2800}-\x{28FF} \t]*`)
)

// applyOverwrite replays a single line's cursor motion the way a terminal does,
// so the captured text is what a human SAW rather than every keystroke that
// produced it:
//
//   - \r returns to column 0; what follows OVERWRITES what was there
//   - \b steps back one column; the next character replaces it
//
// Both were previously discarded — \r became a line break and \b was stripped
// as a control character — which kept every intermediate frame of an in-place
// redraw. That is why the feed showed "proces processing" and "as if th they"
// where the terminal showed "processing" and "as if they".
func applyOverwrite(line string) string {
	if !strings.ContainsAny(line, "\r\b") {
		return line
	}
	var buf []rune
	col := 0
	for _, r := range line {
		switch r {
		case '\r':
			col = 0
		case '\b':
			if col > 0 {
				col--
			}
		default:
			if col < len(buf) {
				buf[col] = r
			} else {
				buf = append(buf, r)
			}
			col++
		}
	}
	return string(buf)
}

// StripANSI removes escape sequences and control characters so captured
// terminal output is readable in the web feed.
func StripANSI(b []byte) string {
	s := string(b)
	s = ansiOSC.ReplaceAllString(s, "")
	s = ansiCSI.ReplaceAllString(s, "")
	s = ansiMisc.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\r\n", "\n")

	// Resolve redraws BEFORE stripping control characters — \b has to be
	// applied as a cursor motion, and the ctrl pass below would delete it.
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		l = applyOverwrite(l)
		l = spinner.ReplaceAllString(l, "")
		lines[i] = strings.TrimRight(ctrl.ReplaceAllString(l, ""), " \t")
	}
	out := lines[:0]
	blank := 0
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
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
