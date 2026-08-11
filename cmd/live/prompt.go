package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"sync"

	"golang.org/x/term"
)

// A live session prints to the same terminal you are typing into. In line mode
// that is unusable: an event arriving mid-word splices itself into your input
// and the shell's echo leaves the fragment stranded on screen. editor owns the
// bottom line instead — it draws the prompt itself, and every printed line goes
// through Print, which erases the prompt, writes above it, and redraws it with
// your text and cursor intact.
type editor struct {
	mu     sync.Mutex
	buf    []rune
	pos    int  // cursor index into buf
	on     bool // owns the bottom line: erase before printing, redraw after
	raw    bool // terminal in raw mode, so keys arrive one at a time
	status func() string

	history []string
	hidx    int // index into history while browsing; len(history) == not browsing

	restore func()
}

// newEditor puts stdin in raw mode and draws the prompt. status is called on
// every redraw for the segment shown before the prompt (watchers, uptime, …).
// The returned editor is always usable: if stdin is not a terminal it degrades
// to plain line reads with no redraw.
func newEditor(status func() string) *editor {
	e := &editor{status: status}
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return e
	}
	st, err := term.MakeRaw(fd)
	if err != nil {
		return e
	}
	e.on, e.raw = true, true
	e.restore = func() { _ = term.Restore(fd, st) }
	e.redraw()
	return e
}

// newStatusLine is the read-only half: it owns a bottom line that events print
// above, but never touches raw mode or reads a key. `live watch` uses it to
// carry the working indicator without capturing the terminal.
func newStatusLine(status func() string) *editor {
	e := &editor{status: status}
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		return e
	}
	e.on = true
	e.redraw()
	return e
}

// Close leaves raw mode and clears the prompt line.
func (e *editor) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.on {
		return
	}
	fmt.Print("\r\033[K")
	e.on = false
	if e.restore != nil {
		e.restore()
	}
}

// Print writes lines above the prompt without disturbing what is being typed.
// Safe to call from any goroutine.
func (e *editor) Print(lines ...string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.on {
		fmt.Print("\r\033[K")
	}
	for _, l := range lines {
		// Raw mode disables the newline→CRLF translation the tty normally does,
		// so every line has to carry its own carriage return.
		fmt.Print(l, e.nl())
	}
	if e.on {
		e.draw()
	}
}

// nl is the line terminator to print. Raw mode disables the tty's own
// newline→CRLF translation, so every line has to carry its own carriage return.
func (e *editor) nl() string {
	if e.raw {
		return "\r\n"
	}
	return "\n"
}

// prompt is the input line's leading segment: an optional status chip, then ❯.
func (e *editor) prompt() string {
	s := ""
	if e.status != nil {
		if st := e.status(); st != "" {
			// NOT re-coloured here. Wrapping the whole status in dim used to
			// leave `\033[2m` in effect when a colour inside it began, so an
			// "is working" green or a BLOCKED amber rendered faint — the two
			// things on this line that most need to be seen. The status owns
			// its own colours.
			s = st + " "
		}
	}
	if !e.raw {
		return s // read-only: a status line, not something you can type into
	}
	return s + paint("❯", cCyan) + " "
}

// draw renders the prompt and buffer at the cursor's line. Caller holds mu.
func (e *editor) draw() {
	p := e.prompt()
	// Budget the visible text by columns, not bytes; the status chip and ❯ are
	// already on the line, and ANSI codes occupy no columns.
	avail := termWidth() - visibleLen(p) - 1
	if avail < 8 {
		avail = 8
	}
	text, cursor := e.buf, e.pos
	if len(text) > avail { // scroll the window to keep the cursor visible
		start := cursor - avail + 1
		if start < 0 {
			start = 0
		}
		if start+avail > len(text) {
			start = len(text) - avail
		}
		text, cursor = text[start:start+avail], cursor-start
	}
	fmt.Print("\r\033[K", p, string(text))
	if back := len(text) - cursor; back > 0 {
		fmt.Printf("\033[%dD", back)
	}
}

func (e *editor) redraw() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.on {
		e.draw()
	}
}

// visibleLen is a string's column count ignoring ANSI colour codes.
func visibleLen(s string) int { return len([]rune(ansiSeqRe.ReplaceAllString(s, ""))) }

// ErrEditorClosed-style sentinels: ReadLine reports why it stopped.
type editorStop int

const (
	stopNone   editorStop = iota
	stopEOF               // Ctrl-D on an empty line, or stdin closed
	stopSignal            // Ctrl-C
)

// ReadLine blocks for one submitted line. ok is false when the user asked to
// leave (Ctrl-C, Ctrl-D on an empty line, or EOF).
func (e *editor) ReadLine(in *bufio.Reader) (string, editorStop) {
	if !e.raw {
		line, err := in.ReadString('\n')
		if err != nil && strings.TrimSpace(line) == "" {
			return "", stopEOF
		}
		return strings.TrimSpace(line), stopNone
	}
	for {
		r, _, err := in.ReadRune()
		if err != nil {
			return "", stopEOF
		}
		switch r {
		case '\r', '\n':
			e.mu.Lock()
			line := strings.TrimSpace(string(e.buf))
			e.buf, e.pos = nil, 0
			if line != "" {
				e.history = append(e.history, line)
			}
			e.hidx = len(e.history)
			fmt.Print("\r\033[K")
			e.draw()
			e.mu.Unlock()
			if line == "" {
				continue
			}
			return line, stopNone

		case 3: // Ctrl-C
			return "", stopSignal

		case 4: // Ctrl-D — only leaves on an empty line, like a shell
			e.mu.Lock()
			empty := len(e.buf) == 0
			e.mu.Unlock()
			if empty {
				return "", stopEOF
			}

		case 21: // Ctrl-U — clear the line
			e.edit(func() { e.buf, e.pos = nil, 0 })

		case 23: // Ctrl-W — delete the previous word
			e.edit(func() {
				i := e.pos
				for i > 0 && e.buf[i-1] == ' ' {
					i--
				}
				for i > 0 && e.buf[i-1] != ' ' {
					i--
				}
				e.buf = append(e.buf[:i], e.buf[e.pos:]...)
				e.pos = i
			})

		case 1: // Ctrl-A
			e.edit(func() { e.pos = 0 })
		case 5: // Ctrl-E
			e.edit(func() { e.pos = len(e.buf) })

		case 127, 8: // Backspace
			e.edit(func() {
				if e.pos > 0 {
					e.buf = append(e.buf[:e.pos-1], e.buf[e.pos:]...)
					e.pos--
				}
			})

		case 27: // escape sequence: arrows, home, end
			e.escape(in)

		default:
			if r < 32 {
				continue // other control keys are not bound
			}
			e.edit(func() {
				e.buf = append(e.buf[:e.pos], append([]rune{r}, e.buf[e.pos:]...)...)
				e.pos++
			})
		}
	}
}

// escape consumes one CSI sequence and applies it. Unknown sequences are
// swallowed so their bytes never land in the buffer as literal text.
func (e *editor) escape(in *bufio.Reader) {
	r1, _, err := in.ReadRune()
	if err != nil || (r1 != '[' && r1 != 'O') {
		return
	}
	r2, _, err := in.ReadRune()
	if err != nil {
		return
	}
	switch r2 {
	case 'D': // left
		e.edit(func() {
			if e.pos > 0 {
				e.pos--
			}
		})
	case 'C': // right
		e.edit(func() {
			if e.pos < len(e.buf) {
				e.pos++
			}
		})
	case 'A': // up — previous sent message
		e.edit(func() { e.recall(-1) })
	case 'B': // down — next sent message
		e.edit(func() { e.recall(+1) })
	case 'H':
		e.edit(func() { e.pos = 0 })
	case 'F':
		e.edit(func() { e.pos = len(e.buf) })
	case '1', '3', '4', '7', '8': // home/delete/end variants: consume the tilde
		if r3, _, err := in.ReadRune(); err == nil && r3 == '~' {
			switch r2 {
			case '3': // delete
				e.edit(func() {
					if e.pos < len(e.buf) {
						e.buf = append(e.buf[:e.pos], e.buf[e.pos+1:]...)
					}
				})
			case '1', '7':
				e.edit(func() { e.pos = 0 })
			case '4', '8':
				e.edit(func() { e.pos = len(e.buf) })
			}
		}
	}
}

// recall moves through previously sent messages. Caller holds mu.
func (e *editor) recall(delta int) {
	if len(e.history) == 0 {
		return
	}
	e.hidx += delta
	if e.hidx < 0 {
		e.hidx = 0
	}
	if e.hidx >= len(e.history) {
		e.hidx = len(e.history)
		e.buf, e.pos = nil, 0
		return
	}
	e.buf = []rune(e.history[e.hidx])
	e.pos = len(e.buf)
}

// edit applies a buffer mutation and repaints the line.
func (e *editor) edit(f func()) {
	e.mu.Lock()
	defer e.mu.Unlock()
	f()
	if e.on {
		e.draw()
	}
}
