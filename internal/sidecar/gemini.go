package sidecar

import (
	"context"
	"strings"

	"github.com/liveshortly/live/internal/api"
)

// handleGeminiHook maps Gemini CLI hook payloads (`live hook gemini-<event>`;
// schemas in gemini-cli docs/hooks/reference.md) onto the same feed events a
// Claude session produces. Gemini reads a flat {"decision","reason"} verdict
// and hookSpecificOutput.additionalContext, so most output is Claude-shaped.
func (s *Sidecar) handleGeminiHook(event string, p map[string]any) map[string]any {
	switch event {
	case "session-start":
		return s.hookSessionStart()
	case "before-agent":
		// Viewer messages delivered by AfterAgent come back as a retry prompt;
		// they are already in the feed as viewer_comment events.
		if prompt := strings.TrimSpace(str(p, "prompt")); prompt != "" && !strings.HasPrefix(prompt, geminiViewerHeader) {
			s.Emit("prompt", map[string]any{"content": truncate(prompt, maxCaptureBytes), "ts": nowISO()})
		}
		return s.injectComments("BeforeAgent")
	case "before-model":
		s.maybeReportModel(str(sub(p, "llm_request"), "model"))
		return nil
	case "before-tool":
		return s.geminiBeforeTool(p)
	case "after-tool":
		return s.geminiAfterTool(p)
	case "notification": // ToolPermission: gemini's own prompt is on screen
		return s.hookNotification(p)
	case "after-agent":
		return s.geminiAfterAgent(p)
	}
	return nil
}

// geminiReadOnly are built-in tools gemini's default policy runs without
// asking (docs/reference/tools.md). Everything else, MCP tools and tools added
// in later versions included, goes to the owner: unknown means gated.
var geminiReadOnly = map[string]bool{
	"read_file": true, "read_many_files": true, "list_directory": true, "glob": true,
	"grep_search": true, "search_file_content": true, "google_web_search": true,
	"write_todos": true, "ask_user": true, "activate_skill": true, "get_internal_docs": true,
	"enter_plan_mode": true, "list_mcp_resources": true, "read_mcp_resource": true,
	"update_topic": true, "complete_task": true,
}

// geminiTool maps a gemini tool call onto the feed's tool name, the
// permission subject shown to the owner, and the "always allow" key.
func geminiTool(name string, in map[string]any) (tool, desc, key string) {
	switch name {
	case "run_shell_command":
		cmd := str(in, "command")
		return "Bash", truncate(cmd, 300), permKey("Bash", map[string]any{"command": cmd})
	case "write_file":
		return "Write", str(in, "file_path"), "Write"
	case "replace":
		return "Edit", str(in, "file_path"), "Edit"
	case "web_fetch":
		return "WebFetch", truncate(str(in, "prompt"), 300), "WebFetch"
	}
	return name, name, name
}

func (s *Sidecar) geminiBeforeTool(p map[string]any) map[string]any {
	name := str(p, "tool_name")
	tool, desc, key := geminiTool(name, sub(p, "tool_input"))

	ev := map[string]any{"tool": tool, "action": "using", "ts": nowISO()}
	switch tool {
	case "Bash":
		ev["action"], ev["command"], ev["dangerous"] = "running", desc, isDangerous(desc)
	case "Write", "Edit":
		ev["action"], ev["file"] = "editing", desc
	case "WebFetch":
		ev["action"], ev["url"] = "fetching", desc
	}
	s.Emit("pre_tool", ev)

	// Not gated, or nothing to gate: "allow" only means "no objection" —
	// gemini's own policy (and prompt, unless yolo) still applies.
	allow := map[string]any{"decision": "allow"}
	if !s.WebPermissions || geminiReadOnly[name] || strings.HasPrefix(name, "tracker_") {
		return allow
	}
	if s.permAllowed(key) {
		allow["reason"] = "Allowed earlier via LiveShortly (don't ask again)"
		return allow
	}
	if s.Broker != nil { // awaitDecision announces itself; the broker doesn't
		msg := s.agentName() + " wants to run " + tool
		if desc != "" && desc != tool {
			msg += ": " + desc
		}
		s.Emit("input_requested", map[string]any{"message": truncate(msg, 500), "kind": "permission", "ts": nowISO()})
		go desktopNotify(s.WebURL, s.SessionID, []api.Comment{{Username: "permission", Message: msg}})
	}
	verdict := s.RequestPermission(context.Background(), tool, desc)
	s.Log.Printf("permission %q (gemini %s) → %s", tool, name, verdict)
	switch verdict {
	case "always":
		s.permRemember(key)
		return map[string]any{"decision": "allow", "reason": "Allowed via LiveShortly — won't ask again for " + key}
	case "allow":
		return map[string]any{"decision": "allow", "reason": "Answered by the session owner via LiveShortly"}
	}
	return map[string]any{"decision": "deny", "reason": "The session owner denied this tool call via LiveShortly"}
}

func (s *Sidecar) geminiAfterTool(p map[string]any) map[string]any {
	name := str(p, "tool_name")
	in := sub(p, "tool_input")
	tr := sub(p, "tool_response")
	switch name {
	case "write_file", "replace":
		if tr["error"] != nil {
			break // the edit didn't land
		}
		path := str(in, "file_path")
		tool, diff, added, removed := "Write", "", 0, 0
		if name == "replace" {
			tool = "Edit"
			diff, added, removed = unifiedDiff(path, str(in, "old_string"), str(in, "new_string"))
		} else {
			diff, added, removed = writeDiff(path, str(in, "content"))
		}
		s.Emit("file_write", map[string]any{"tool": tool, "file": path, "diff": diff, "added": added, "removed": removed, "ts": nowISO()})
	case "run_shell_command":
		out := str(tr, "returnDisplay") // the command's output; llmContent wraps it for the model
		if out == "" {
			out = str(tr, "llmContent")
		}
		s.Emit("tool_call", map[string]any{
			"tool":    "Bash",
			"command": truncate(str(in, "command"), 500),
			"output":  truncate(out, maxCaptureBytes),
			"ts":      nowISO(),
		})
	}
	return s.injectComments("AfterTool")
}

// geminiViewerHeader opens viewerMessageBlock, the retry prompt AfterAgent
// uses to deliver viewer messages, so BeforeAgent can tell it from a prompt
// the operator typed.
const geminiViewerHeader = "[live] Viewer message(s)"

func (s *Sidecar) geminiAfterAgent(p map[string]any) map[string]any {
	if last := strings.TrimSpace(str(p, "prompt_response")); last != "" {
		s.Emit("response", map[string]any{"content": truncate(last, maxCaptureBytes), "ts": nowISO()})
	}
	// Messages that missed every tool boundary: "deny" makes gemini run one
	// more turn with reason as the prompt, like Claude's blocked Stop.
	if comments := s.takePending(); len(comments) > 0 {
		s.Log.Printf("gemini: re-opened the turn to deliver %d viewer comment(s)", len(comments))
		return map[string]any{
			"decision":      "deny",
			"reason":        viewerMessageBlock(comments),
			"systemMessage": "[live] 💬 " + s.deliveredSummary(comments),
		}
	}
	s.Emit("stream_end", map[string]any{"ts": nowISO()})
	return nil
}
