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
}

// Notify prints an overlay line (e.g. a viewer comment) to the outer
// terminal, serialized against the output copy loop.
func (p *PTY) Notify(msg string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	os.Stdout.WriteString("\r\n\x1b[33m[live] " + msg + "\x1b[0m\r\n")
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
			_ = pty.InheritSize(os.Stdin, ptmx)
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

	go func() { _, _ = io.Copy(ptmx, os.Stdin) }()

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
)

// StripANSI removes escape sequences and control characters so captured
// terminal output is readable in the web feed.
func StripANSI(b []byte) string {
	s := string(b)
	s = ansiOSC.ReplaceAllString(s, "")
	s = ansiCSI.ReplaceAllString(s, "")
	s = ansiMisc.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = ctrl.ReplaceAllString(s, "")

	// Collapse redraw noise: drop runs of blank lines.
	lines := strings.Split(s, "\n")
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
