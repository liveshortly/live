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

// handleHook maps an agent hook payload (relayed by `live hook`) to feed
// events and hook JSON output. Supports both Claude Code and Antigravity (agy).
func (s *Sidecar) handleHook(event string, p map[string]any) map[string]any {
	if ev, ok := strings.CutPrefix(event, "gemini-"); ok {
		return s.handleGeminiHook(ev, p)
	}
	switch event {
	case "session-start":
		return s.hookSessionStart()
	case "user-prompt-submit":
		return s.hookUserPromptSubmit(p)
	case "pre-invocation":
		return s.hookPreInvocation(p)
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

var viewerTurnRe = regexp.MustCompile(`(?is)^[^\n:]+\s*\(live viewer\):\s*(.*)$`)

func isViewerTurn(text string) bool {
	return viewerTurnRe.MatchString(strings.TrimSpace(text))
}

var userRequestTagRe = regexp.MustCompile(`(?is)<USER_REQUEST>(.*?)</USER_REQUEST>`)

// cleanUserPrompt unwraps <USER_REQUEST>...</USER_REQUEST> tags from AGY transcript
// prompts and trims whitespace.
func cleanUserPrompt(raw string) string {
	if m := userRequestTagRe.FindStringSubmatch(raw); len(m) > 1 {
		return strings.TrimSpace(m[1])
	}
	return strings.TrimSpace(raw)
}

func (s *Sidecar) hookUserPromptSubmit(p map[string]any) map[string]any {
	prompt := str(p, "prompt")
	// A channel-delivered or tmux-injected viewer comment arrives here as a synthetic prompt;
	// skip it — the viewer_comment event already represents it cleanly.
	if isChannelTurn(prompt) || isViewerTurn(prompt) {
		return nil
	}
	if prompt != "" {
		s.Emit("prompt", map[string]any{
			"content": truncate(prompt, maxCaptureBytes),
			"ts":      nowISO(),
		})
	}
	return s.injectComments("UserPromptSubmit")
}

// hookPreInvocation handles AGY's PreInvocation lifecycle hook.
func (s *Sidecar) hookPreInvocation(p map[string]any) map[string]any {
	// Report model if present in payload
	if model := str(p, "modelName"); model != "" {
		s.maybeReportModel(model)
	}

	// Extract prompt from payload or from transcript
	prompt := str(p, "prompt")
	if prompt == "" {
		prompt = str(p, "userMessage")
	}
	turn := prompt
	if prompt == "" {
		var step int
		prompt, step = lastUserPromptFromTranscript(str(p, "transcriptPath"))
		turn = fmt.Sprintf("%s#%d", str(p, "transcriptPath"), step)
	}
	prompt = cleanUserPrompt(prompt)

	// PreInvocation fires before every model step, not once per user turn:
	// emit the prompt only when the turn (transcript USER_INPUT step) changes.
	s.mu.Lock()
	newTurn := turn != s.lastPromptTurn
	s.lastPromptTurn = turn
	s.mu.Unlock()

	if newTurn && prompt != "" && !isChannelTurn(prompt) && !isViewerTurn(prompt) {
		s.Emit("prompt", map[string]any{
			"content": truncate(prompt, maxCaptureBytes),
			"ts":      nowISO(),
		})
	}

	// Inject pending comments into AGY's PreInvocation output
	if comments := s.takePending(); len(comments) > 0 {
		s.Log.Printf("injected %d viewer comment(s) at PreInvocation", len(comments))
		return map[string]any{
			"injectSteps": []any{
				map[string]any{
					"ephemeralMessage": viewerMessageBlock(comments),
				},
			},
		}
	}
	return map[string]any{"injectSteps": []any{}}
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
	var (
		isAGY         = p["toolCall"] != nil
		tool          string
		in            map[string]any
		permTool      string
		permDescStr   string
		permMemoryKey string
	)

	if isAGY {
		tc := sub(p, "toolCall")
		tool = str(tc, "name")
		in = sub(tc, "args")

		switch tool {
		case "run_command":
			cmd := str(in, "CommandLine")
			s.Emit("pre_tool", map[string]any{
				"tool":      "Bash",
				"action":    "running",
				"command":   truncate(cmd, 300),
				"dangerous": isDangerous(cmd),
				"ts":        nowISO(),
			})
			permTool = "Bash"
			permDescStr = truncate(cmd, 300)
			permMemoryKey = permKey("Bash", map[string]any{"command": cmd})
		case "write_to_file", "write_file":
			path := agyPath(in)
			s.Emit("pre_tool", map[string]any{
				"tool":   "Write",
				"action": "editing",
				"file":   path,
				"ts":     nowISO(),
			})
			permTool = "Write"
			permDescStr = path
			permMemoryKey = "Write"
		case "replace_file_content", "edit_file":
			path := agyPath(in)
			s.Emit("pre_tool", map[string]any{
				"tool":   "Edit",
				"action": "editing",
				"file":   path,
				"ts":     nowISO(),
			})
			permTool = "Edit"
			permDescStr = path
			permMemoryKey = "Edit"
		case "view_file", "read_file":
			path := agyPath(in)
			s.Emit("pre_tool", map[string]any{
				"tool":   "View",
				"action": "viewing",
				"file":   path,
				"ts":     nowISO(),
			})
			permTool = "View"
			permDescStr = path
			permMemoryKey = "View"
		case "read_url_content", "search_web":
			u := str(in, "Url")
			if u == "" {
				u = str(in, "query")
			}
			s.Emit("pre_tool", map[string]any{
				"tool":   "WebFetch",
				"action": "fetching",
				"url":    u,
				"ts":     nowISO(),
			})
			permTool = "WebFetch"
			permDescStr = u
			permMemoryKey = "WebFetch:" + hostOf(u)
		default:
			s.Emit("pre_tool", map[string]any{
				"tool":   tool,
				"action": "executing",
				"ts":     nowISO(),
			})
			permTool = tool
			permDescStr = tool
			permMemoryKey = tool
		}
	} else {
		// Claude Code format
		tool = str(p, "tool_name")
		in = sub(p, "tool_input")

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
		permTool = tool
		permDescStr = permissionDesc(tool, in)
		permMemoryKey = permKey(tool, in)
	}

	// Opt-in web permissions: block here and let the owner answer the
	// permission prompt from the web or popup.
	if s.WebPermissions {
		if s.permAllowed(permMemoryKey) {
			s.Log.Printf("permission %q auto-allowed (remembered %q)", permTool, permMemoryKey)
			if isAGY {
				return map[string]any{
					"decision": "allow",
					"reason":   "Allowed earlier via LiveShortly (don't ask again)",
				}
			}
			return permOutput("allow", "Allowed earlier via LiveShortly (don't ask again)")
		}

		var verdict string
		if s.Broker != nil {
			msg := s.agentName() + " wants to run " + permTool
			if permDescStr != "" {
				msg += ": " + permDescStr
			}
			s.Emit("input_requested", map[string]any{"message": truncate(msg, 500), "kind": "permission", "ts": nowISO()})
			go desktopNotify(s.WebURL, s.SessionID, []api.Comment{{Username: "permission", Message: msg}})
			var source string
			verdict, source = s.Broker.Request(context.Background(), permTool, permDescStr)
			s.Log.Printf("permission %q → %s (%s)", permTool, verdict, source)
		} else {
			verdict = s.awaitDecision(context.Background(), permTool, permDescStr)
		}
		reason := "Answered by the session owner via LiveShortly"
		if verdict == "always" {
			s.permRemember(permMemoryKey)
			verdict = "allow"
			reason = "Allowed via LiveShortly — won't ask again for " + permMemoryKey
			s.Log.Printf("permission %q allowed + remembered %q", permTool, permMemoryKey)
		}
		if isAGY {
			return map[string]any{
				"decision": verdict,
				"reason":   reason,
			}
		}
		return permOutput(verdict, reason)
	}

	if isAGY {
		return map[string]any{"decision": "allow"}
	}
	return nil
}

// agyPath is the file an agy file tool targets: TargetFile / AbsolutePath on
// the older tools, Path on the agy ≥ 1.4.9 edit_file / write_file / read_file.
func agyPath(in map[string]any) string {
	for _, k := range []string{"Path", "TargetFile", "AbsolutePath"} {
		if v := str(in, k); v != "" {
			return v
		}
	}
	return ""
}

// permOutput builds a PreToolUse permission-decision hook response for Claude Code.
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
		if cmd == "" {
			cmd = strings.TrimSpace(str(in, "CommandLine"))
		}
		word := cmd
		if i := strings.IndexAny(cmd, " \t\n"); i > 0 {
			word = cmd[:i]
		}
		if word == "" {
			return "Bash"
		}
		return "Bash:" + word
	case "WebFetch":
		u := str(in, "url")
		if u == "" {
			u = str(in, "Url")
		}
		return "WebFetch:" + hostOf(u)
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
	isAGY := p["toolCall"] != nil
	if isAGY {
		tc := sub(p, "toolCall")
		tool := str(tc, "name")
		in := sub(tc, "args")

		switch tool {
		case "write_to_file", "write_file":
			path := agyPath(in)
			content := str(in, "CodeContent")
			if tool == "write_file" {
				content = str(in, "Content")
			}
			diff, added, removed := writeDiff(path, content)
			s.Emit("file_write", map[string]any{
				"tool":    "Write",
				"file":    path,
				"diff":    diff,
				"added":   added,
				"removed": removed,
				"ts":      nowISO(),
			})
		case "replace_file_content", "edit_file":
			path := agyPath(in)
			oldStr, newStr := str(in, "TargetContent"), str(in, "ReplacementContent")
			if tool == "edit_file" {
				oldStr, newStr = str(in, "Target"), str(in, "Replacement")
			}
			diff, added, removed := unifiedDiff(path, oldStr, newStr)
			s.Emit("file_write", map[string]any{
				"tool":    "Edit",
				"file":    path,
				"diff":    diff,
				"added":   added,
				"removed": removed,
				"ts":      nowISO(),
			})
		case "run_command":
			cmd := str(in, "CommandLine")
			stepIdx := int(num(p, "stepIdx"))
			out := toolOutputFromTranscript(str(p, "transcriptPath"), stepIdx)
			if out == "" {
				out = str(p, "error")
			}
			s.Emit("tool_call", map[string]any{
				"tool":    "Bash",
				"command": truncate(cmd, 500),
				"output":  truncate(out, maxCaptureBytes),
				"ts":      nowISO(),
			})
		}
		return map[string]any{}
	}

	// Claude Code format
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
			out = truncate(str(tr, "stdout"), maxCaptureBytes)
		case string:
			out = truncate(tr, maxCaptureBytes)
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
		msg = s.agentName() + " is waiting for your input"
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
	isAGY := p["fullyIdle"] != nil || p["terminationReason"] != nil || (str(p, "modelName") != "" && str(p, "transcriptPath") != "" && p["last_assistant_message"] == nil)

	// Report the true model once — a session is created before the first
	// assistant turn, so it starts model-less.
	if model := str(p, "modelName"); model != "" {
		s.maybeReportModel(model)
	} else if model := detectModel(str(p, "transcript_path")); model != "" {
		s.maybeReportModel(model)
	} else if model := detectModel(str(p, "transcriptPath")); model != "" {
		s.maybeReportModel(model)
	}

	// Viewer messages that arrived too late for a tool boundary would
	// otherwise sit invisible until the next user prompt. Block the stop
	// once so the agent addresses them before the turn ends.
	if comments := s.takePending(); len(comments) > 0 {
		last := str(p, "last_assistant_message")
		if last == "" {
			last = lastAssistantResponseFromTranscript(str(p, "transcriptPath"))
		}
		if last != "" {
			s.Emit("response", map[string]any{
				"content": truncate(last, maxCaptureBytes),
				"ts":      nowISO(),
			})
		}
		lines := make([]string, 0, len(comments))
		for _, c := range comments {
			lines = append(lines, username(c)+": "+c.Message)
		}
		s.Log.Printf("blocked stop to deliver %d viewer comment(s)", len(comments))
		if isAGY {
			return map[string]any{
				"decision": "continue",
				"reason":   "[live] 💬 " + s.deliveredSummary(comments) + "\n" + strings.Join(lines, "\n"),
			}
		}
		return map[string]any{
			"decision":      "block",
			"reason":        strings.Join(lines, "\n"),
			"systemMessage": "[live] 💬 " + s.deliveredSummary(comments),
		}
	}

	last := str(p, "last_assistant_message")
	if last == "" {
		last = lastAssistantResponseFromTranscript(str(p, "transcriptPath"))
	}
	if last != "" {
		s.Emit("response", map[string]any{
			"content": truncate(last, maxCaptureBytes),
			"ts":      nowISO(),
		})
	}
	s.Emit("stream_end", map[string]any{"ts": nowISO()})

	if isAGY {
		return map[string]any{"decision": "allow"}
	}
	return nil
}

func (s *Sidecar) maybeReportModel(model string) {
	s.mu.Lock()
	needModel := !s.modelReported
	if needModel {
		s.modelReported = true
	}
	s.mu.Unlock()
	if needModel && model != "" && model != "<synthetic>" && s.Client != nil {
		go func() {
			if err := s.Client.ReportModel(s.SessionID, model); err != nil {
				s.Log.Printf("report model: %v", err)
			}
		}()
	}
}

// injectComments drains locally queued viewer comments into hook output as
// additionalContext so the agent sees them at the next boundary.
func (s *Sidecar) injectComments(hookEventName string) map[string]any {
	comments := s.takePending()
	if len(comments) == 0 {
		return nil
	}
	s.Log.Printf("injected %d viewer comment(s) at %s", len(comments), hookEventName)
	return map[string]any{
		// systemMessage is shown to the operator in the CLI — without it,
		// delivery is invisible and looks broken.
		"systemMessage": "[live] 💬 " + s.deliveredSummary(comments),
		"hookSpecificOutput": map[string]any{
			"hookEventName":     hookEventName,
			"additionalContext": viewerMessageBlock(comments),
		},
	}
}

// viewerMessageBlock is what the agent itself reads: every comment in full,
// one per line with its author. Never the operator receipt, which is cut.
func viewerMessageBlock(comments []api.Comment) string {
	lines := []string{"[live] Viewer message(s) (respond if relevant):"}
	for _, c := range comments {
		lines = append(lines, "  • "+username(c)+": "+c.Message)
	}
	return strings.Join(lines, "\n")
}

// deliveredSummary is the one-line operator-facing receipt for a batch.
func (s *Sidecar) deliveredSummary(comments []api.Comment) string {
	to := " → delivered to " + s.agentName()
	if len(comments) == 1 {
		return username(comments[0]) + ": " + truncate(comments[0].Message, 120) + to
	}
	names := make([]string, 0, len(comments))
	for _, c := range comments {
		names = append(names, username(c))
	}
	return fmt.Sprintf("%d viewer messages (%s)", len(comments), strings.Join(names, ", ")) + to
}

func username(c api.Comment) string {
	if strings.TrimSpace(c.Username) != "" {
		return c.Username
	}
	return "viewer"
}

// lastUserPromptFromTranscript scans a transcript JSONL file from the end
// for the most recent USER_INPUT content.
func lastUserPromptFromTranscript(path string) (prompt string, stepIdx int) {
	if path == "" {
		return "", 0
	}
	f, err := os.Open(path)
	if err != nil {
		return "", 0
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
		var step struct {
			StepIndex int    `json:"step_index"`
			Type      string `json:"type"`
			Content   string `json:"content"`
		}
		if json.Unmarshal([]byte(lines[i]), &step) == nil && step.Type == "USER_INPUT" && step.Content != "" {
			return cleanUserPrompt(step.Content), step.StepIndex
		}
	}
	return "", 0
}

// lastAssistantResponseFromTranscript scans a transcript JSONL file from the end
// for the most recent PLANNER_RESPONSE content.
func lastAssistantResponseFromTranscript(path string) string {
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
		var step struct {
			Type    string `json:"type"`
			Content string `json:"content"`
		}
		if json.Unmarshal([]byte(lines[i]), &step) == nil && step.Type == "PLANNER_RESPONSE" && step.Content != "" {
			return strings.TrimSpace(step.Content)
		}
	}
	return ""
}

// toolOutputFromTranscript extracts command output for a given stepIdx.
func toolOutputFromTranscript(path string, stepIdx int) string {
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
		var step struct {
			StepIndex int    `json:"step_index"`
			Type      string `json:"type"`
			Content   string `json:"content"`
		}
		if json.Unmarshal([]byte(lines[i]), &step) == nil {
			if stepIdx > 0 && step.StepIndex == stepIdx && step.Content != "" {
				return cleanToolOutput(step.Content)
			}
			if stepIdx <= 0 && step.Type != "PLANNER_RESPONSE" && step.Type != "USER_INPUT" && step.Content != "" {
				return cleanToolOutput(step.Content)
			}
		}
	}
	return ""
}

// cleanToolOutput strips agy's run_command step header. agy ≥ 1.4.9 writes
// "Created At: …\nCompleted At: …\nCommand exited with code N.\n\nCommand output:\n";
// only those exact leading lines are removed (a non-zero exit line is kept).
// Older agy wrote "…The command exited with code N…\nOutput:\n".
func cleanToolOutput(raw string) string {
	rest, hdr := raw, false
	for _, p := range []string{"Created At: ", "Completed At: "} {
		if strings.HasPrefix(rest, p) {
			_, rest, _ = strings.Cut(rest, "\n")
			hdr = true
		}
	}
	if hdr {
		exit := ""
		if strings.HasPrefix(rest, "Command exited with code ") {
			exit, rest, _ = strings.Cut(rest, "\n")
			if exit == "Command exited with code 0." {
				exit = ""
			}
		}
		if out, ok := strings.CutPrefix(strings.TrimLeft(rest, "\n"), "Command output:\n"); ok {
			return strings.TrimSpace(exit + "\n" + out)
		}
		if exit != "" {
			rest = exit + "\n" + rest
		}
		raw = rest
	}
	if idx := strings.Index(raw, "Output:\n"); idx >= 0 {
		out := raw[idx+len("Output:\n"):]
		out = strings.ReplaceAll(out, "\t", "")
		return strings.TrimSpace(out)
	}
	return strings.TrimSpace(raw)
}

// detectModel scans a Claude Code or AGY JSONL transcript from the end for the most
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
			ModelName string `json:"modelName"`
			Model     string `json:"model"`
		}
		if json.Unmarshal([]byte(lines[i]), &obj) != nil {
			continue
		}
		if m := obj.Message.Model; m != "" && m != "<synthetic>" {
			return m
		}
		if m := obj.ModelName; m != "" && m != "<synthetic>" {
			return m
		}
		if m := obj.Model; m != "" && m != "<synthetic>" {
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

func num(m map[string]any, key string) float64 {
	if m == nil {
		return 0
	}
	switch v := m[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case json.Number:
		n, _ := v.Float64()
		return n
	}
	return 0
}

func sub(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	v, _ := m[key].(map[string]any)
	return v
}

func filePath(in map[string]any) string {
	for _, k := range []string{"file_path", "path", "new_path", "TargetFile", "AbsolutePath"} {
		if v := str(in, k); v != "" {
			return v
		}
	}
	return ""
}
