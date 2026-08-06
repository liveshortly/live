package adapter

import "testing"

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
