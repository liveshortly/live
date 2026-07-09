package main

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"github.com/liveshortly/live/internal/api"
)

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSITest(s string) string { return ansiRe.ReplaceAllString(s, "") }

func TestClipLine(t *testing.T) {
	if got := clipLine("  a\n b   c \r\n", 100); got != "a b c" {
		t.Fatalf("clipLine collapse = %q", got)
	}
	if got := clipLine("abcdefghij", 4); got != "abcd…" {
		t.Fatalf("clipLine bound = %q", got)
	}
}

func TestPriorTurnLabel(t *testing.T) {
	cases := []struct {
		turn api.HandoffTurn
		want string
	}{
		{api.HandoffTurn{Role: "user"}, "user:"},
		{api.HandoffTurn{Tool: "Bash"}, "Bash:"},
		{api.HandoffTurn{Tool: "Edit", File: "run.go"}, "Edit run.go:"},
	}
	for _, c := range cases {
		if got := priorTurnLabel(c.turn); got != c.want {
			t.Fatalf("label(%+v) = %q, want %q", c.turn, got, c.want)
		}
	}
}

func TestPrintPriorContext(t *testing.T) {
	// no turns → nothing printed
	var empty bytes.Buffer
	printPriorContext(&empty, api.HandoffBundle{}, "/tmp/x.md")
	if empty.Len() != 0 {
		t.Fatalf("expected no output for zero turns, got %q", empty.String())
	}

	// renders each turn + bounds to the most recent maxTurns, footer points at path
	turns := make([]api.HandoffTurn, 0, 15)
	for i := 0; i < 15; i++ {
		turns = append(turns, api.HandoffTurn{Role: "user", Text: "msg"})
	}
	turns[14] = api.HandoffTurn{Tool: "Bash", Text: "go test ./..."}
	var buf bytes.Buffer
	printPriorContext(&buf, api.HandoffBundle{Turns: turns}, "/home/u/.liveshortly/handoffs/s.md")
	out := stripANSITest(buf.String())
	if !strings.Contains(out, "prior context (15 turn(s))") {
		t.Fatalf("missing header/count: %q", out)
	}
	if !strings.Contains(out, "3 earlier turn(s) omitted") { // 15 - 12
		t.Fatalf("missing omission notice: %q", out)
	}
	if !strings.Contains(out, "Bash: go test ./...") {
		t.Fatalf("last (tool) turn not rendered: %q", out)
	}
	if !strings.Contains(out, "full prior context at /home/u/.liveshortly/handoffs/s.md") {
		t.Fatalf("missing digest path footer: %q", out)
	}
}
