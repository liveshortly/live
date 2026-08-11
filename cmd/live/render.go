package main

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/liveshortly/live/internal/api"
)

// This file renders a session's feed the way the web viewer does: chat turns as
// speaker-labelled blocks with their text in full, and the agent's work steps as
// compact rows underneath. The one-line-per-event form it replaced fit a listing
// but read as a log — you could not tell who said what, and every reply longer
// than the terminal was cut off at an ellipsis.

// ansiSeqRe matches the escape sequences a PTY-captured payload carries. They
// are stripped before display: reprinting a foreign terminal's cursor moves
// inside our own layout corrupts it.
var ansiSeqRe = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]|\x1b[@-Z\\-_]|\r`)

// sanitize makes arbitrary payload text safe to print inside our layout:
// escape sequences gone, other control characters dropped, tabs widened.
func sanitize(s string) string {
	s = ansiSeqRe.ReplaceAllString(s, "")
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n':
			return r
		case r == '\t':
			return ' '
		case unicode.IsControl(r):
			return -1
		}
		return r
	}, s)
}

// wrap breaks text into lines of at most width columns, preserving the author's
// own line breaks and folding on word boundaries. width < 8 is treated as 8 so a
// narrow terminal degrades instead of looping.
func wrap(s string, width int) []string {
	if width < 8 {
		width = 8
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		if strings.TrimSpace(para) == "" {
			out = append(out, "")
			continue
		}
		line := ""
		for _, word := range strings.Fields(para) {
			switch {
			case line == "":
				line = word
			case len([]rune(line))+1+len([]rune(word)) <= width:
				line += " " + word
			default:
				out = append(out, line)
				line = word
			}
			// A single word longer than the width (a URL, a path) is hard-split
			// rather than left to overflow the block.
			for len([]rune(line)) > width {
				r := []rune(line)
				out = append(out, string(r[:width]))
				line = string(r[width:])
			}
		}
		if line != "" {
			out = append(out, line)
		}
	}
	// Collapse the blank lines the author's own double newlines produce.
	var packed []string
	for i, l := range out {
		if l == "" && (i == 0 || i == len(out)-1 || out[max(0, i-1)] == "") {
			continue
		}
		packed = append(packed, l)
	}
	return packed
}

// capLines trims a body to n lines, appending a count of what was hidden.
func capLines(lines []string, n int) []string {
	if len(lines) <= n {
		return lines
	}
	return append(lines[:n], paint(fmt.Sprintf("… %d more lines", len(lines)-n), cDim))
}

// agentLabel names whatever is actually doing the work, most specific first:
// the model if the session has reported one, else the agent that was launched.
// It is deliberately the real identifier rather than a friendly brand — on a
// machine running several agents, "claude-opus-5 is working" and "codex is
// working" tell you which one; "Claude is working" does not, and would be an
// outright lie next to a gemini or ollama session.
func agentLabel(s api.Session) string {
	if s.Model != "" {
		return s.Model
	}
	if a := agentOf(s); a != "" && a != "terminal" {
		return a
	}
	return "the agent"
}

// chatTypes are the events that read as somebody speaking, and so get a
// speaker-labelled block rather than a work row.
var chatTypes = map[string]bool{"prompt": true, "response": true, "viewer_comment": true}

// speaker resolves who produced an event and how to label them. A `prompt`
// carries no author in its payload — it is by definition the session owner
// typing — and a `response` is the model that answered.
func speaker(e api.Event, s api.Session, me string) (name, role, color string) {
	switch e.EventType {
	case "viewer_comment":
		name = e.Str("username")
		if name == "" {
			name = "viewer"
		}
		return name, "viewer", cViolet
	case "response":
		return agentLabel(s), "agent", cGreen
	case "prompt":
		name = e.Str("username")
		if name == "" {
			name = s.OwnerHandle
		}
		if name == "" {
			name = "owner"
		}
		role := "owner"
		if me != "" && strings.EqualFold(me, name) {
			role = "you"
		}
		return name, role, cAmber
	}
	name = s.Agent
	if name == "" {
		name = "agent"
	}
	return name, "agent", cDim
}

// renderBlock formats one event as the lines to print: a header naming the
// speaker (or the tool) and a wrapped body. Returns nil for events that should
// not be shown at all.
func renderBlock(e api.Event, s api.Session, me string, width int) []string {
	if hiddenEvents[e.EventType] {
		return nil
	}
	if width < 30 {
		width = 30
	}
	const indent = "   "
	body := width - len(indent) - 1
	ts := paint(e.TS.Local().Format("15:04:05"), cDim)
	glyph, color := eventMark(e.EventType)

	if chatTypes[e.EventType] {
		name, role, nameColor := speaker(e, s, me)
		head := fmt.Sprintf(" %s %s %s %s",
			paint(glyph, color),
			paint(name, nameColor, cBold),
			paint("· "+role, cDim),
			ts)
		lines := []string{head}
		text := sanitize(e.Str("message", "content", "text"))
		if text == "" {
			text = paint("(no text)", cDim)
		}
		for _, l := range capLines(wrap(text, body), 60) {
			lines = append(lines, indent+l)
		}
		return append(lines, "")
	}

	// Somebody answered a permission prompt — from a joined terminal or the web.
	if e.EventType == "viewer_decision" {
		who := orDefault(e.Str("username"), "someone")
		verdict, color := "allowed the request", cGreen
		switch e.Str("decision") {
		case "deny":
			verdict, color = "refused the request", cRed
		case "always":
			verdict = "allowed the request, and this tool from now on"
		}
		return []string{fmt.Sprintf(" %s %s %s %s",
			paint(glyph, color), paint(who, color, cBold), paint(verdict, cDim), ts), ""}
	}

	// The agent is asking its own operator for something — a permission prompt or
	// an idle input wait. It is not a work step and reads better as a notice.
	if e.EventType == "input_requested" {
		kind := e.Str("kind")
		if kind == "" {
			kind = "input"
		}
		lines := []string{fmt.Sprintf(" %s %s %s",
			paint(glyph, color), paint("waiting for "+kind, cAmber, cBold), ts)}
		if msg := shortenPathsIn(sanitize(e.Str("message")), body); msg != "" {
			for _, l := range capLines(wrap(msg, body), 8) {
				lines = append(lines, indent+l)
			}
		}
		return append(lines, "")
	}

	// Work steps: a one-line header naming the tool and its target, then any
	// captured output or diff as a dim sub-block.
	tool := e.Str("tool_name", "tool", "name")
	if tool == "" {
		tool = e.EventType
	}
	target := e.Str("command", "url")
	isPath := false
	if target == "" {
		target, isPath = e.Str("file_path", "path", "file"), true
	}
	nameColor := []string{cBold}
	head := ""
	if e.EventType == "pre_tool" {
		// The "started" half of a tool run: the tool_call event that follows
		// carries the result, so this one stays visually subordinate. It still
		// earns its place — on a slow command it is the only sign of life.
		nameColor = []string{cDim}
		head = fmt.Sprintf(" %s %s %s", paint(glyph, color),
			paint(tool, nameColor...), paint("· "+orDefault(e.Str("action"), "running"), cDim))
	} else {
		head = fmt.Sprintf(" %s %s", paint(glyph, color), paint(tool, nameColor...))
	}
	if e.EventType == "file_write" {
		if a, r := numOf(e, "added"), numOf(e, "removed"); a+r > 0 {
			head += "  " + paint(fmt.Sprintf("+%d", a), cGreen) + " " + paint(fmt.Sprintf("−%d", r), cRed)
		}
	}
	if b, ok := e.Payload["dangerous"].(bool); ok && b {
		head += "  " + paint("⚠ flagged", cRed)
	}
	head += "  " + ts
	lines := []string{head}
	if target != "" {
		targetColor := cCyan
		if e.EventType == "pre_tool" {
			targetColor = cDim
		}
		shown := sanitize(target)
		if isPath {
			shown = shortenPath(shown, body)
		}
		for _, l := range capLines(wrap(shown, body), 4) {
			lines = append(lines, indent+paint(l, targetColor))
		}
	}
	if diff := sanitize(e.Str("diff")); diff != "" {
		var rows []string
		for _, l := range strings.Split(diff, "\n") {
			// The ---/+++ header repeats the path already shown above it.
			if strings.HasPrefix(l, "---") || strings.HasPrefix(l, "+++") {
				continue
			}
			rows = append(rows, l)
		}
		for _, l := range capLines(rows, 16) {
			lines = append(lines, indent+diffLine(clipLine(l, body)))
		}
	} else if out := sanitize(e.Str("output", "text", "stdout")); out != "" {
		for _, l := range capLines(wrap(out, body-2), 8) {
			lines = append(lines, indent+paint("⎿ "+l, cDim))
		}
	}
	return append(lines, "")
}

// orDefault returns s, or fallback when s is empty.
func orDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// shortenPath reduces a long absolute path to the tail that identifies the
// file — "…/demo/README.md". A deep temp or build path is mostly prefix nobody
// reads, and the full form is one `live info --json` away.
func shortenPath(s string, width int) string {
	if len([]rune(s)) <= width && strings.Count(s, "/") <= 3 {
		return s
	}
	parts := strings.Split(strings.TrimSuffix(s, "/"), "/")
	if len(parts) <= 3 {
		return s
	}
	tail := parts[len(parts)-3:]
	// Still too wide (one very long filename): fall back to the last segment.
	if out := "…/" + strings.Join(tail, "/"); len([]rune(out)) <= width {
		return out
	}
	return "…/" + parts[len(parts)-1]
}

// shortenPathsIn applies shortenPath to the path-shaped words of a sentence.
// A permission prompt quotes the tool's argument verbatim, which for a file
// tool is an absolute path long enough to bury the question being asked.
func shortenPathsIn(s string, width int) string {
	out := make([]string, 0, 8)
	for _, w := range strings.Fields(s) {
		if strings.HasPrefix(w, "/") {
			w = shortenPath(w, width)
		}
		out = append(out, w)
	}
	return strings.Join(out, " ")
}

// diffLine colours a unified-diff row by its marker.
func diffLine(l string) string {
	switch {
	case strings.HasPrefix(l, "+"):
		return paint(l, cGreen)
	case strings.HasPrefix(l, "-"):
		return paint(l, cRed)
	case strings.HasPrefix(l, "@@"):
		return paint(l, cCyan)
	}
	return paint(l, cDim)
}

// numOf reads a numeric payload field (JSON numbers decode as float64).
func numOf(e api.Event, key string) int {
	switch v := e.Payload[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}

// printBlocks writes the rendered events for a session, skipping hidden ones.
func printBlocks(events []api.Event, s api.Session, me string, width int) {
	for _, e := range events {
		for _, l := range renderBlock(e, s, me, width) {
			fmt.Println(l)
		}
	}
}
