package main

import (
	"context"
	"encoding/json"
	"strings"
)

// handleNotification maps codex app-server streamed notifications to the same
// sidecar feed events the rest of LiveShortly renders. We emit on item/completed
// (the settled item) and track turn state for comment steering.
func (c *codexApp) handleNotification(method string, raw json.RawMessage) {
	switch method {
	case "turn/started":
		var p struct {
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		if json.Unmarshal(raw, &p) == nil {
			c.mu.Lock()
			c.activeTur = p.Turn.ID
			c.mu.Unlock()
		}
	case "turn/completed":
		c.mu.Lock()
		c.activeTur = ""
		c.mu.Unlock()
		c.emit("stream_end", map[string]any{"ts": isoNow()})
	case "item/completed":
		c.emitItem(raw)
	}
}

// codexItem is the settled item payload for the types we surface.
type codexItem struct {
	Item struct {
		Type    string `json:"type"`
		Command string `json:"command"`
		Text    string `json:"text"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		AggregatedOutput string          `json:"aggregatedOutput"`
		Output           string          `json:"output"`
		Changes          json.RawMessage `json:"changes"`
	} `json:"item"`
}

func (c *codexApp) emitItem(raw json.RawMessage) {
	var p codexItem
	if json.Unmarshal(raw, &p) != nil {
		return
	}
	it := p.Item
	switch it.Type {
	case "userMessage":
		text := it.Text
		if text == "" {
			var b strings.Builder
			for _, part := range it.Content {
				b.WriteString(part.Text)
			}
			text = b.String()
		}
		text = strings.TrimSpace(text)
		// Comments injected from the web arrive back as user messages; they're
		// already shown as viewer_comment, so don't double-emit them as prompts.
		if text == "" || looksLikeViewerInput(text) {
			return
		}
		c.cliEcho("\x1b[36m❯\x1b[0m " + clip(text, 400))
		c.emit("prompt", map[string]any{"content": clip(text, 4000), "ts": isoNow()})
	case "agentMessage":
		if t := strings.TrimSpace(it.Text); t != "" {
			c.cliEcho("\x1b[32m🤖\x1b[0m " + clip(t, 800))
			c.emit("response", map[string]any{"content": clip(t, 8000), "ts": isoNow()})
		}
	case "commandExecution":
		out := it.AggregatedOutput
		if out == "" {
			out = it.Output
		}
		c.cliEcho("\x1b[2m⚡ " + clip(it.Command, 200) + "\x1b[0m")
		c.emit("tool_call", map[string]any{
			"tool":    "Shell",
			"command": clip(it.Command, 500),
			"output":  clip(strings.TrimSpace(out), 300),
			"ts":      isoNow(),
		})
	case "fileChange":
		for _, f := range parseCodexFileChanges(it.Changes) {
			c.emit("file_write", map[string]any{
				"tool": "apply_patch",
				"file": f,
				"ts":   isoNow(),
			})
		}
	}
}

// cliEcho prints a transcript line to the terminal (app-server mode is headless,
// so this is how the operator sees activity in the CLI). No-op in tests.
func (c *codexApp) cliEcho(line string) {
	if c.echo != nil {
		c.echo(line)
	}
}

// handleServerRequest answers codex's server→client requests. Approval requests
// are gated from the owner's browser via the sidecar; other requests get a safe
// default so codex never hangs waiting on us.
func (c *codexApp) handleServerRequest(id json.RawMessage, method string, raw json.RawMessage) {
	switch method {
	case "item/commandExecution/requestApproval", "execCommandApproval":
		var p struct {
			Command string `json:"command"`
			Reason  string `json:"reason"`
		}
		_ = json.Unmarshal(raw, &p)
		c.answerApproval(id, "Shell", firstNonEmptyStr(p.Command, p.Reason))
	case "item/fileChange/requestApproval", "applyPatchApproval":
		c.answerApproval(id, "Edit", "apply a file change")
	case "item/permissions/requestApproval":
		c.answerApproval(id, "Permissions", "elevated permissions")
	default:
		// Unknown request (e.g. tool user-input / elicitation): decline safely so
		// codex proceeds rather than blocking on a UI we don't render.
		c.respond(id, map[string]any{"decision": "denied"})
	}
}

// answerApproval surfaces a web permission prompt and maps the owner's verdict
// to codex's ReviewDecision.
func (c *codexApp) answerApproval(id json.RawMessage, tool, desc string) {
	go func() {
		verdict := c.requestPerm(context.Background(), tool, desc)
		decision := "denied"
		if verdict == "allow" || verdict == "always" {
			decision = "approved"
		}
		c.respond(id, map[string]any{"decision": decision})
	}()
}

// ── small helpers ─────────────────────────────────────────────────────────────

// threadIDOf pulls the thread id out of a thread/start result ({"thread":{"id"}}).
func threadIDOf(result json.RawMessage) string {
	var r struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
		ThreadID string `json:"threadId"`
	}
	if json.Unmarshal(result, &r) != nil {
		return ""
	}
	if r.Thread.ID != "" {
		return r.Thread.ID
	}
	return r.ThreadID
}

// initialPrompt picks the opening instruction out of the agent args: the LAST
// non-flag token. That's the plain `live codex "task"` positional, or the
// handoff seed prompt (appended last on a fork), and it dodges flag values like
// `--model gpt` that a leading-scan would wrongly grab.
func initialPrompt(args []string) string {
	for i := len(args) - 1; i >= 0; i-- {
		if !strings.HasPrefix(args[i], "-") {
			return args[i]
		}
	}
	return ""
}

// looksLikeViewerInput reports whether a user-message item is a comment we
// injected (prefixed "<who> (live viewer): …"), so it isn't re-emitted as a
// prompt on top of its viewer_comment event.
func looksLikeViewerInput(text string) bool {
	return strings.Contains(text, "(live viewer):")
}

// parseCodexFileChanges extracts touched file paths from a fileChange item's
// changes payload, which is a map of path → change or an array of {path}.
func parseCodexFileChanges(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var asMap map[string]json.RawMessage
	if json.Unmarshal(raw, &asMap) == nil {
		out := make([]string, 0, len(asMap))
		for path := range asMap {
			out = append(out, path)
		}
		return out
	}
	var asArr []struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(raw, &asArr) == nil {
		out := make([]string, 0, len(asArr))
		for _, e := range asArr {
			if e.Path != "" {
				out = append(out, e.Path)
			}
		}
		return out
	}
	return nil
}
