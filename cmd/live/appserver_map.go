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
	case "thread/started":
		c.noteModel(raw)
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
			c.emit("response", map[string]any{"content": clip(t, codexOutputCap), "ts": isoNow()})
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
			"output":  clip(strings.TrimSpace(out), codexOutputCap),
			"ts":      isoNow(),
		})
	case "fileChange":
		for _, f := range parseCodexFileChanges(it.Changes) {
			ev := map[string]any{"tool": "apply_patch", "file": f.path, "ts": isoNow()}
			if f.diff != "" {
				ev["diff"] = clip(f.diff, codexOutputCap)
			}
			c.emit("file_write", ev)
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
		c.answerApproval(id, method, "Shell", firstNonEmptyStr(p.Command, p.Reason))
	case "item/fileChange/requestApproval", "applyPatchApproval":
		c.answerApproval(id, method, "Edit", "apply a file change")
	case "item/permissions/requestApproval":
		go func() {
			// PermissionsRequestApprovalResponse: grant what was asked, or nothing.
			granted := json.RawMessage(`{}`)
			if v := c.requestPerm(context.Background(), "Permissions", "elevated permissions"); v == "allow" || v == "always" {
				var p struct {
					Permissions json.RawMessage `json:"permissions"`
				}
				if json.Unmarshal(raw, &p) == nil && len(p.Permissions) > 0 {
					granted = p.Permissions
				}
			}
			c.respond(id, map[string]any{"permissions": granted, "scope": "turn"})
		}()
	case "item/tool/requestUserInput":
		// No UI for codex's questions here: answer none so the tool proceeds.
		c.respond(id, map[string]any{"answers": map[string]any{}})
	case "mcpServer/elicitation/request":
		c.respond(id, map[string]any{"action": "decline"})
	case "item/tool/call":
		c.respond(id, map[string]any{"contentItems": []any{}, "success": false})
	default:
		// Anything else (auth refresh, attestation): fail explicitly so codex
		// doesn't hang waiting on us.
		_ = c.write(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32601, "message": "not supported by live: " + method}})
	}
}

// answerApproval surfaces a web permission prompt and answers in the method's
// own vocabulary: v2 item/*/requestApproval take accept/decline, the legacy
// execCommandApproval/applyPatchApproval take ReviewDecision approved/denied.
func (c *codexApp) answerApproval(id json.RawMessage, method, tool, desc string) {
	go func() {
		verdict := c.requestPerm(context.Background(), tool, desc)
		ok := verdict == "allow" || verdict == "always"
		decision := map[bool]string{true: "accept", false: "decline"}[ok]
		if method == "execCommandApproval" || method == "applyPatchApproval" {
			decision = map[bool]string{true: "approved", false: "denied"}[ok]
		}
		c.respond(id, map[string]any{"decision": decision})
	}()
}

// noteModel reports codex's model to the server once (from the thread/start
// response or the thread/started notification).
func (c *codexApp) noteModel(raw json.RawMessage) {
	var r struct {
		Model  string `json:"model"`
		Thread struct {
			Model string `json:"model"`
		} `json:"thread"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return
	}
	m := firstNonEmptyStr(r.Model, r.Thread.Model)
	c.mu.Lock()
	report := m != "" && !c.modelReported && c.reportModel != nil
	if report {
		c.modelReported = true
	}
	c.mu.Unlock()
	if report {
		c.reportModel(m)
	}
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

// codexValueFlags are the codex flags that take a separate value. That value
// is never a prompt: `live` injects `-c` overrides (permission hooks, the
// handoff digest), and the daemon appends `--model <id>` and
// `--ask-for-approval never` to web-started sessions — a trailing one would
// otherwise be submitted to the model as the opening turn.
var codexValueFlags = map[string]bool{
	"-c": true, "--config": true, "-m": true, "--model": true,
	"-a": true, "--ask-for-approval": true, "-s": true, "--sandbox": true,
	"-p": true, "--profile": true,
}

// initialPrompt picks the opening instruction out of the agent args: the LAST
// token that is neither a flag nor a flag's value. That's the plain
// `live codex "task"` positional.
func initialPrompt(args []string) string {
	for i := len(args) - 1; i >= 0; i-- {
		if strings.HasPrefix(args[i], "-") {
			continue
		}
		if i > 0 && codexValueFlags[args[i-1]] {
			continue // this is the flag's value, not a prompt
		}
		return args[i]
	}
	return ""
}

// argValue returns the value of the first of flags in args ("--model x" or
// "--model=x"), or "".
func argValue(args []string, flags ...string) string {
	for i, a := range args {
		for _, f := range flags {
			if a == f && i+1 < len(args) {
				return args[i+1]
			}
			if strings.HasPrefix(a, f+"=") {
				return strings.TrimPrefix(a, f+"=")
			}
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
// codexFileChange is one file of an app-server fileChange item; diff is the
// unified diff when codex sends one.
type codexFileChange struct{ path, diff string }

// changeDiff pulls a diff out of one change entry, whichever key codex used.
type changeDiff struct {
	Path        string `json:"path"`
	Diff        string `json:"diff"`
	UnifiedDiff string `json:"unified_diff"`
}

func (c changeDiff) text() string {
	if c.UnifiedDiff != "" {
		return c.UnifiedDiff
	}
	return c.Diff
}

func parseCodexFileChanges(raw json.RawMessage) []codexFileChange {
	if len(raw) == 0 {
		return nil
	}
	var asArr []changeDiff
	if json.Unmarshal(raw, &asArr) == nil {
		out := make([]codexFileChange, 0, len(asArr))
		for _, e := range asArr {
			if e.Path != "" {
				out = append(out, codexFileChange{e.Path, e.text()})
			}
		}
		return out
	}
	var asMap map[string]json.RawMessage
	if json.Unmarshal(raw, &asMap) == nil {
		out := make([]codexFileChange, 0, len(asMap))
		for path, v := range asMap {
			var d changeDiff
			_ = json.Unmarshal(v, &d)
			out = append(out, codexFileChange{path, d.text()})
		}
		return out
	}
	return nil
}
