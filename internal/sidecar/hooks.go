package sidecar

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/liveshortly/live/internal/api"
)

// handleHook maps a Claude Code hook payload (relayed by `live hook`) to feed
// events and hook JSON output. Event types and payload shapes mirror the
// LiveShortly plugin so the web viewer renders both identically.
func (s *Sidecar) handleHook(event string, p map[string]any) map[string]any {
	switch event {
	case "session-start":
		return s.hookSessionStart()
	case "user-prompt-submit":
		return s.hookUserPromptSubmit(p)
	case "pre-tool-use":
		return s.hookPreToolUse(p)
	case "post-tool-use":
		return s.hookPostToolUse(p)
	case "notification":
		return s.hookNotification(p)
	case "stop":
		return s.hookStop(p)
	}
	return nil
}

func (s *Sidecar) hookSessionStart() map[string]any {
	liveURL := s.WebURL + "/session/" + s.SessionID
	return map[string]any{
		"systemMessage": "You are live-streaming on 😍liveshortly. " +
			"Viewers are watching at: " + liveURL + " — " +
			"acknowledge viewer messages when they arrive and keep coding.",
	}
}

// channelTagRe matches a viewer comment the `live channel` MCP server injects
// as a synthetic user turn: <channel source="liveshortly-channel" ...>…</channel>.
var channelTagRe = regexp.MustCompile(`(?is)<channel\s[^>]*liveshortly[^>]*>.*?</channel>`)

// isChannelTurn reports whether the whole prompt is just a channel-wrapped
// viewer comment (nothing the user actually typed). Those already appear in
// the feed as clean viewer_comment events, so re-emitting the raw wrapper as a
// prompt would show the XML scaffolding around the message.
func isChannelTurn(text string) bool {
	return channelTagRe.MatchString(text) &&
		strings.TrimSpace(channelTagRe.ReplaceAllString(text, "")) == ""
}

func (s *Sidecar) hookUserPromptSubmit(p map[string]any) map[string]any {
	prompt := str(p, "prompt")
	// A channel-delivered viewer comment arrives here as a synthetic prompt;
	// skip it — the viewer_comment event already represents it cleanly.
	if isChannelTurn(prompt) {
		return nil
	}
	if prompt != "" {
		s.Emit("prompt", map[string]any{
			"content": truncate(prompt, 4000),
			"ts":      nowISO(),
		})
	}
	return s.injectComments("UserPromptSubmit")
}

var dangerPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\brm\s+-[rf]{1,2}\b`),
	regexp.MustCompile(`\bdrop\s+table\b`),
	regexp.MustCompile(`\btruncate\b`),
	regexp.MustCompile(`\bgit\s+(push\s+.*--force|reset\s+--hard)\b`),
	regexp.MustCompile(`\bpkill\b`),
	regexp.MustCompile(`\bkill\s+-9\b`),
	regexp.MustCompile(`\bchmod\s+777\b`),
	regexp.MustCompile(`\bmkfs\b`),
	regexp.MustCompile(`\bdd\s+if=`),
}

func isDangerous(cmd string) bool {
	low := strings.ToLower(cmd)
	for _, re := range dangerPatterns {
		if re.MatchString(low) {
			return true
		}
	}
	return false
}

func (s *Sidecar) hookPreToolUse(p map[string]any) map[string]any {
	tool := str(p, "tool_name")
	in := sub(p, "tool_input")

	switch tool {
	case "Edit", "Write", "MultiEdit":
		s.Emit("pre_tool", map[string]any{
			"tool":   tool,
			"action": "editing",
			"file":   filePath(in),
			"ts":     nowISO(),
		})
	case "Bash":
		cmd := str(in, "command")
		s.Emit("pre_tool", map[string]any{
			"tool":      "Bash",
			"action":    "running",
			"command":   truncate(cmd, 300),
			"dangerous": isDangerous(cmd),
			"ts":        nowISO(),
		})
	case "WebFetch":
		s.Emit("pre_tool", map[string]any{
			"tool":   "WebFetch",
			"action": "fetching",
			"url":    str(in, "url"),
			"ts":     nowISO(),
		})
	}

	// Opt-in web permissions: block here and let the owner answer Claude's
	// permission prompt from the web. awaitDecision surfaces an Approve/Deny
	// prompt to the feed and long-polls /decision; the verdict becomes Claude's
	// PreToolUse permission decision, pre-empting the local keyboard prompt.
	if s.WebPermissions {
		key := permKey(tool, in)
		// An earlier "always allow" for this tool/command auto-allows without
		// re-prompting — Claude Code's "don't ask again", owner-driven.
		if s.permAllowed(key) {
			s.Log.Printf("permission %q auto-allowed (remembered %q)", tool, key)
			return permOutput("allow", "Allowed earlier via LiveShortly (don't ask again)")
		}
		desc := permissionDesc(tool, in)
		var verdict string
		if s.Broker != nil {
			// tmux mode: surface the web prompt too, then race popup vs web.
			msg := "Claude wants to run " + tool
			if desc != "" {
				msg += ": " + desc
			}
			s.Emit("input_requested", map[string]any{"message": truncate(msg, 500), "kind": "permission", "ts": nowISO()})
			go desktopNotify([]api.Comment{{Username: "permission", Message: msg}})
			var source string
			verdict, source = s.Broker.Request(context.Background(), tool, desc)
			s.Log.Printf("permission %q → %s (%s)", tool, verdict, source)
		} else {
			verdict = s.awaitDecision(context.Background(), tool, desc)
		}
		reason := "Answered by the session owner via LiveShortly"
		if verdict == "always" {
			s.permRemember(key)
			verdict = "allow"
			reason = "Allowed via LiveShortly — won't ask again for " + key
			s.Log.Printf("permission %q allowed + remembered %q", tool, key)
		}
		return permOutput(verdict, reason)
	}
	return nil
}

// permOutput builds a PreToolUse permission-decision hook response.
func permOutput(decision, reason string) map[string]any {
	return map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       decision, // "allow" | "deny"
			"permissionDecisionReason": reason,
		},
	}
}

// permKey is the "always allow" memory key for a tool call. Bash remembers the
// leading command word (git, npm, echo…) — matching Claude's "don't ask again
// for X commands"; file tools remember the tool (accept-all-edits); WebFetch
// remembers the host.
func permKey(tool string, in map[string]any) string {
	switch tool {
	case "Bash":
		cmd := strings.TrimSpace(str(in, "command"))
		word := cmd
		if i := strings.IndexAny(cmd, " \t\n"); i > 0 {
			word = cmd[:i]
		}
		if word == "" {
			return "Bash"
		}
		return "Bash:" + word
	case "WebFetch":
		return "WebFetch:" + hostOf(str(in, "url"))
	}
	return tool // Edit / Write / MultiEdit → accept all of this kind
}

// hostOf extracts the host from a URL for coarse WebFetch memory.
func hostOf(raw string) string {
	if u, err := url.Parse(strings.TrimSpace(raw)); err == nil && u.Host != "" {
		return u.Host
	}
	return raw
}

// permissionDesc is the human-readable subject of a permission prompt.
func permissionDesc(tool string, in map[string]any) string {
	switch tool {
	case "Bash":
		return truncate(str(in, "command"), 300)
	case "Edit", "Write", "MultiEdit":
		return filePath(in)
	case "WebFetch":
		return str(in, "url")
	}
	return tool
}

func (s *Sidecar) hookPostToolUse(p map[string]any) map[string]any {
	tool := str(p, "tool_name")
	in := sub(p, "tool_input")

	switch tool {
	case "Edit", "Write", "MultiEdit":
		path := filePath(in)
		var diff string
		var added, removed int
		switch tool {
		case "Edit":
			diff, added, removed = unifiedDiff(path, str(in, "old_string"), str(in, "new_string"))
		case "Write":
			diff, added, removed = writeDiff(path, str(in, "content"))
		case "MultiEdit":
			var parts []string
			if edits, ok := in["edits"].([]any); ok {
				for _, e := range edits {
					em, _ := e.(map[string]any)
					d, a, r := unifiedDiff(path, str(em, "old_string"), str(em, "new_string"))
					parts = append(parts, d)
					added += a
					removed += r
				}
			}
			diff = strings.Join(parts, "\n")
		}
		s.Emit("file_write", map[string]any{
			"tool":    tool,
			"file":    path,
			"diff":    diff,
			"added":   added,
			"removed": removed,
			"ts":      nowISO(),
		})

	case "Bash":
		out := ""
		switch tr := p["tool_response"].(type) {
		case map[string]any:
			out = truncate(str(tr, "stdout"), 300)
		case string:
			out = truncate(tr, 300)
		}
		s.Emit("tool_call", map[string]any{
			"tool":    "Bash",
			"command": truncate(str(in, "command"), 500),
			"output":  out,
			"ts":      nowISO(),
		})
	}
	return s.injectComments("PostToolUse")
}

func (s *Sidecar) hookNotification(p map[string]any) map[string]any {
	msg := strings.TrimSpace(str(p, "message"))
	if msg == "" {
		msg = "Claude is waiting for your input"
	}
	kind := "input"
	low := strings.ToLower(msg)
	if strings.Contains(low, "permission") || strings.Contains(low, "approve") || strings.Contains(low, "allow") {
		kind = "permission"
	}
	s.Emit("input_requested", map[string]any{
		"message": truncate(msg, 500),
		"kind":    kind,
		"ts":      nowISO(),
	})
	return nil
}

func (s *Sidecar) hookStop(p map[string]any) map[string]any {
	// Viewer messages that arrived too late for a tool boundary would
	// otherwise sit invisible until the next user prompt. Block the stop
	// once so Claude addresses them before the turn ends.
	if comments := s.takePending(); len(comments) > 0 {
		lines := make([]string, 0, len(comments))
		for _, c := range comments {
			lines = append(lines, "  • "+username(c)+": "+c.Message)
		}
		s.Log.Printf("blocked stop to deliver %d viewer comment(s)", len(comments))
		return map[string]any{
			"decision": "block",
			"reason": "[live] Viewer message(s) arrived during this turn — respond to them now, then finish:\n" +
				strings.Join(lines, "\n"),
			"systemMessage": "[live] 💬 " + deliveredSummary(comments),
		}
	}

	// Report the true model once — a session is created before the first
	// assistant turn, so it starts model-less.
	s.mu.Lock()
	needModel := !s.modelReported
	s.mu.Unlock()
	if needModel {
		if model := detectModel(str(p, "transcript_path")); model != "" {
			s.mu.Lock()
			s.modelReported = true
			s.mu.Unlock()
			go func() {
				if err := s.Client.ReportModel(s.SessionID, model); err != nil {
					s.Log.Printf("report model: %v", err)
				}
			}()
		}
	}

	if last := str(p, "last_assistant_message"); last != "" {
		s.Emit("response", map[string]any{
			"content": truncate(last, 8000),
			"ts":      nowISO(),
		})
	}
	s.Emit("stream_end", map[string]any{"ts": nowISO()})
	return nil
}

// injectComments drains locally queued viewer comments into hook output as
// additionalContext so the agent sees them at the next boundary.
func (s *Sidecar) injectComments(hookEventName string) map[string]any {
	comments := s.takePending()
	if len(comments) == 0 {
		return nil
	}
	lines := []string{"[live] Viewer message(s) (respond if relevant):"}
	for _, c := range comments {
		lines = append(lines, "  • "+username(c)+": "+c.Message)
	}
	s.Log.Printf("injected %d viewer comment(s) at %s", len(comments), hookEventName)
	return map[string]any{
		// systemMessage is shown to the operator in the CLI — without it,
		// delivery is invisible and looks broken.
		"systemMessage": "[live] 💬 " + deliveredSummary(comments),
		"hookSpecificOutput": map[string]any{
			"hookEventName":     hookEventName,
			"additionalContext": strings.Join(lines, "\n"),
		},
	}
}

// deliveredSummary is the one-line operator-facing receipt for a batch.
func deliveredSummary(comments []api.Comment) string {
	if len(comments) == 1 {
		return username(comments[0]) + ": " + truncate(comments[0].Message, 120) + " → delivered to Claude"
	}
	names := make([]string, 0, len(comments))
	for _, c := range comments {
		names = append(names, username(c))
	}
	return fmt.Sprintf("%d viewer messages (%s) → delivered to Claude", len(comments), strings.Join(names, ", "))
}

func username(c api.Comment) string {
	if strings.TrimSpace(c.Username) != "" {
		return c.Username
	}
	return "viewer"
}

// detectModel scans a Claude Code JSONL transcript from the end for the most
// recent assistant turn's model id.
func detectModel(path string) string {
	if path == "" {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 256*1024), 8<<20)
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" {
			lines = append(lines, l)
		}
	}
	for i := len(lines) - 1; i >= 0; i-- {
		var obj struct {
			Message struct {
				Model string `json:"model"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(lines[i]), &obj) != nil {
			continue
		}
		if m := obj.Message.Model; m != "" && m != "<synthetic>" {
			return m
		}
	}
	return ""
}

// ── payload accessors ─────────────────────────────────────────────────────────

func str(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, _ := m[key].(string)
	return v
}

func sub(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	v, _ := m[key].(map[string]any)
	return v
}

func filePath(in map[string]any) string {
	for _, k := range []string{"file_path", "path", "new_path"} {
		if v := str(in, k); v != "" {
			return v
		}
	}
	return ""
}
