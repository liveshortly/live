package adapter

import (
	"strings"
	"testing"
	"time"
)

// A bare \r returns the cursor to column 0 and the next write overwrites. If it
// is treated as a newline instead, every frame of an in-place redraw survives —
// which is how a spinner became a wall of lines and "processing" arrived in the
// feed as "proces processing".
func TestStripANSIResolvesCarriageReturns(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"overwrite is applied", "proces\rprocessing", "processing"},
		{"shorter write leaves the tail", "abcdef\rxy", "xycdef"},
		{"spinner collapses to its last frame", "\r- \r\\ \r| \r/ \rdone", "done"},
		{"progress bar keeps only the final state", "10%\r50%\r100%", "100%"},
		{"crlf is still a real line break", "one\r\ntwo", "one\ntwo"},
		{"plain text is untouched", "hello world", "hello world"},
		// \b is a cursor motion, not a character to delete. Stripping it left
		// both the mistyped and corrected text in the feed.
		{"backspace erases", "as if th\b\bthey", "as if they"},
		{"backspace then retype", "proces\b\b\b\b\b\bprocessing", "processing"},
		{"backspace at column 0 is a no-op", "\bhi", "hi"},
		// Spinner frames are animation, not content.
		{"braille spinner is dropped", "\u2819 \u2839 \u2838 A pseudo-terminal", "A pseudo-terminal"},
		{"spinner alone leaves nothing", "\u2819 \u2839 \u2838", ""},
	}
	for _, c := range cases {
		if got := StripANSI([]byte(c.in)); got != c.want {
			t.Errorf("%s: StripANSI(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// ollama 0.30.10 (testdata/ollama/0.30.10): the REPL prints a grey hint, steps
// back over it, and erases it on the first keystroke; a soft wrap steps back
// over the half-written word, erases it and reprints it on the next line.
func TestStripANSIAppliesCursorMotion(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"placeholder is erased by the first keystroke",
			">>> \x1b[38;5;245mSend a message (/? for help)\x1b[28D\x1b[0m\x1b[KIn one short sentence.", ">>> In one short sentence."},
		{"soft wrap does not duplicate the moved word", "How can I be\x1b[2D\x1b[K\r\nbe of service?", "How can I\nbe of service?"},
		{"column move then erase", "spinning\x1b[1G\x1b[Kdone", "done"},
		{"erase whole line", "abc\x1b[2K\rxy", "xy"},
		{"private modes are ignored", "\x1b[?25lhi\x1b[?25h", "hi"},
	}
	for _, c := range cases {
		if got := StripANSI([]byte(c.in)); got != c.want {
			t.Errorf("%s: StripANSI(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

func TestCoalescer(t *testing.T) {
	t0 := time.Unix(0, 0)
	at := func(s float64) time.Time { return t0.Add(time.Duration(s * float64(time.Second))) }
	c := NewCoalescer()
	var rows []string
	tick := func(s float64) {
		if r := c.Tick(at(s)); r != "" {
			rows = append(rows, r)
		}
	}

	// Slow tokens on one line: held through idle gaps, no row per token.
	c.Write([]byte("Hello"), at(0))
	tick(5)
	c.Write([]byte(" there, how"), at(10))
	tick(20)
	if len(rows) != 0 {
		t.Fatalf("held line released early: %q", rows)
	}
	// MaxHold releases it up to the last word boundary.
	tick(45)
	// Soft-wrap redraw of text already sent: "how" must not repeat.
	c.Write([]byte("\x1b[3D\x1b[K\r\nhow are you?\r\n\r\n>>> \x1b[38;5;245mSend a message\x1b[14D\x1b[0m"), at(50))
	tick(51)
	// The prompt is never force-released while nothing new is written to it.
	tick(200)
	if c.Pending() {
		t.Error("idle prompt still reported as pending")
	}
	if len(rows) != 2 || rows[0] != "Hello there," || rows[1] != "how are you?" {
		t.Fatalf("rows = %q", rows)
	}
	// Exit flushes the rest, up to the cursor (not the placeholder).
	if got := c.Flush(); got != ">>>" {
		t.Errorf("final flush = %q, want %q", got, ">>>")
	}
}

// A word already sent, then moved to the next line by a soft-wrap redraw, is
// not sent twice.
func TestCoalescerWrapAfterSend(t *testing.T) {
	t0 := time.Unix(0, 0)
	c := NewCoalescer()
	c.Write([]byte("How can I be"), t0)
	if got := c.Flush(); got != "How can I be" {
		t.Fatalf("flush = %q", got)
	}
	c.Write([]byte("\x1b[2D\x1b[K\r\nbe of service?\r\n"), t0.Add(time.Second))
	if got := c.Tick(t0.Add(2 * time.Second)); got != "of service?" {
		t.Errorf("after wrap = %q, want %q", got, "of service?")
	}
}

// A web message can be drained before the agent's pty exists (sidecar starts
// first). It used to be dropped as "agent gone"; it must reach the agent.
func TestPTYSubmitBeforeStart(t *testing.T) {
	p := &PTY{}
	if err := p.Submit("hello"); err != nil {
		t.Fatal(err)
	}
	var out []byte
	if _, err := p.Run("sh", []string{"-c", `read l; echo "got:$l"`}, func(b []byte) { out = append(out, b...) }); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "got:hello") {
		t.Fatalf("early line not delivered; output %q", out)
	}
}
