package main

import (
	"bufio"
	"strings"
	"testing"
	"time"

	"github.com/liveshortly/live/internal/api"
)

func TestWrapFoldsAndKeepsEveryWord(t *testing.T) {
	text := "the quick brown fox jumps over the lazy dog"
	lines := wrap(text, 12)
	for _, l := range lines {
		if n := len([]rune(l)); n > 12 {
			t.Fatalf("line %q is %d columns, want ≤ 12", l, n)
		}
	}
	if got := strings.Join(lines, " "); got != text {
		t.Fatalf("wrap lost or reordered words: %q", got)
	}
}

func TestWrapHardSplitsAnUnbreakableWord(t *testing.T) {
	long := strings.Repeat("x", 25)
	lines := wrap(long, 10)
	for _, l := range lines {
		if n := len([]rune(l)); n > 10 {
			t.Fatalf("line is %d columns, want ≤ 10", n)
		}
	}
	if got := strings.Join(lines, ""); got != long {
		t.Fatalf("hard split lost characters: %q", got)
	}
}

func TestWrapKeepsAuthorLineBreaks(t *testing.T) {
	if got := wrap("one\ntwo", 40); len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Fatalf("wrap = %q", got)
	}
}

func TestSanitizeStripsEscapeSequences(t *testing.T) {
	in := "\x1b[31mred\x1b[0m\ttab\r\nnext\x07"
	want := "red tab\nnext"
	if got := sanitize(in); got != want {
		t.Fatalf("sanitize = %q, want %q", got, want)
	}
}

func TestShortenPathKeepsTheIdentifyingTail(t *testing.T) {
	long := "/private/tmp/claude-502/a-very-long-cache-key/scratchpad/demo/README.md"
	if got := shortenPath(long, 60); got != "…/scratchpad/demo/README.md" {
		t.Fatalf("shortenPath = %q", got)
	}
	if got := shortenPath("cmd/live/join.go", 60); got != "cmd/live/join.go" {
		t.Fatalf("short path should pass through, got %q", got)
	}
}

func TestCapLinesReportsWhatItHid(t *testing.T) {
	got := capLines([]string{"a", "b", "c", "d"}, 2)
	if len(got) != 3 || !strings.Contains(stripANSITest(got[2]), "2 more lines") {
		t.Fatalf("capLines = %q", got)
	}
}

// A prompt carries no author in its payload, so the block has to name the
// session owner — that omission is what made the old one-line form unreadable.
func TestRenderBlockNamesTheSpeaker(t *testing.T) {
	s := api.Session{OwnerHandle: "Rohit Sehgal", Model: "claude-opus-5", Agent: "claude-code"}
	when := time.Date(2026, 8, 11, 8, 30, 12, 0, time.Local)

	prompt := api.Event{EventType: "prompt", TS: when, Payload: map[string]any{"content": "are you connected ?"}}
	head := stripANSITest(renderBlock(prompt, s, "Rohit Sehgal", 80)[0])
	if !strings.Contains(head, "Rohit Sehgal") || !strings.Contains(head, "you") {
		t.Errorf("prompt header = %q, want the owner named and marked as you", head)
	}
	if head := stripANSITest(renderBlock(prompt, s, "Someone Else", 80)[0]); !strings.Contains(head, "owner") {
		t.Errorf("someone else's prompt header = %q, want it marked as owner", head)
	}

	resp := api.Event{EventType: "response", TS: when, Payload: map[string]any{"content": "yes"}}
	if head := stripANSITest(renderBlock(resp, s, "Rohit Sehgal", 80)[0]); !strings.Contains(head, "claude-opus-5") {
		t.Errorf("response header = %q, want the model named", head)
	}
}

func TestRenderBlockShowsBodyInFull(t *testing.T) {
	s := api.Session{OwnerHandle: "o"}
	long := strings.Repeat("word ", 60)
	lines := renderBlock(api.Event{EventType: "response", Payload: map[string]any{"content": long}}, s, "", 60)
	if len(lines) < 4 {
		t.Fatalf("a 300-character reply rendered in %d lines — it is being clipped", len(lines))
	}
	joined := stripANSITest(strings.Join(lines[1:], " "))
	if !strings.Contains(joined, "word word word") {
		t.Fatalf("body missing: %q", joined)
	}
}

func TestRenderBlockDropsDiffHeadersAndCountsChanges(t *testing.T) {
	e := api.Event{
		EventType: "file_write",
		Payload: map[string]any{
			"tool": "Edit", "file": "/a/b/c/README.md",
			"added": float64(1), "removed": float64(0),
			"diff": "--- /a/b/c/README.md\n+++ /a/b/c/README.md\n hello\n+v2 tui",
		},
	}
	out := stripANSITest(strings.Join(renderBlock(e, api.Session{}, "", 80), "\n"))
	if strings.Contains(out, "+++") || strings.Contains(out, "---") {
		t.Errorf("diff header should be dropped:\n%s", out)
	}
	if !strings.Contains(out, "+1") || !strings.Contains(out, "−0") {
		t.Errorf("change counts missing:\n%s", out)
	}
	if !strings.Contains(out, "+v2 tui") {
		t.Errorf("diff body missing:\n%s", out)
	}
}

func TestRenderBlockNamesWhoAnsweredAPermission(t *testing.T) {
	e := api.Event{EventType: "viewer_decision",
		Payload: map[string]any{"username": "Bob", "decision": "deny"}}
	got := stripANSITest(renderBlock(e, api.Session{}, "", 80)[0])
	if !strings.Contains(got, "Bob") || !strings.Contains(got, "refused") {
		t.Fatalf("viewer_decision = %q", got)
	}
}

func TestShortenPathsInAMessage(t *testing.T) {
	in := "Claude wants to run Write: /private/tmp/very/deep/cache/key/demo/perm2.txt"
	got := shortenPathsIn(in, 40)
	if !strings.HasPrefix(got, "Claude wants to run Write:") {
		t.Fatalf("the question itself was mangled: %q", got)
	}
	if !strings.Contains(got, "…/") || !strings.HasSuffix(got, "demo/perm2.txt") {
		t.Fatalf("path not shortened to its tail: %q", got)
	}
	if strings.Contains(got, "/private/tmp") {
		t.Fatalf("prefix should be gone: %q", got)
	}
}

func TestRenderBlockHidesStreamBoundaries(t *testing.T) {
	if got := renderBlock(api.Event{EventType: "stream_end"}, api.Session{}, "", 80); got != nil {
		t.Fatalf("stream_end rendered %q", got)
	}
}

// The editor must fall back to plain line reads when stdin is not a terminal,
// so `echo msg | live join` keeps working.
func TestEditorLineModeWithoutATerminal(t *testing.T) {
	e := &editor{} // on == false: exactly what newEditor returns for a pipe
	in := bufio.NewReader(strings.NewReader("hello there\n"))
	line, stop := e.ReadLine(in)
	if stop != stopNone || line != "hello there" {
		t.Fatalf("ReadLine = %q, %v", line, stop)
	}
	if _, stop := e.ReadLine(in); stop != stopEOF {
		t.Fatalf("second read should report EOF, got %v", stop)
	}
}

func TestEditorHistoryRecall(t *testing.T) {
	e := &editor{history: []string{"first", "second"}, hidx: 2}
	e.recall(-1)
	if string(e.buf) != "second" || e.pos != 6 {
		t.Fatalf("up once = %q pos %d", string(e.buf), e.pos)
	}
	e.recall(-1)
	if string(e.buf) != "first" {
		t.Fatalf("up twice = %q", string(e.buf))
	}
	e.recall(-1) // already at the oldest: stays put
	if string(e.buf) != "first" {
		t.Fatalf("up past the oldest = %q", string(e.buf))
	}
	e.recall(+1)
	e.recall(+1) // past the newest clears the line, like a shell
	if len(e.buf) != 0 {
		t.Fatalf("down past the newest = %q", string(e.buf))
	}
}

func TestVisibleLenIgnoresColour(t *testing.T) {
	if got := visibleLen(paint("abc", cGreen, cBold)); got != 3 {
		t.Fatalf("visibleLen = %d, want 3", got)
	}
}
