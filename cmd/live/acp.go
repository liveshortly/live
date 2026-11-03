package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/liveshortly/live/internal/api"
	"github.com/liveshortly/live/internal/sidecar"
)

// ACP mode (LSH-13): drive ANY agent that speaks the Agent Client Protocol
// (agentclientprotocol.com) — JSON-RPC over the agent's stdio — instead of
// recording its terminal. One adapter, no per-agent code: an agent released
// after this binary gets the same structured feed, web approvals and comment
// delivery as long as it speaks ACP.
//
// Opt in per run with LIVE_ACP=1, or per agent with "acp": true in
// agents.json (the daemon then sets LIVE_ACP=1 for that spawn). The argv is
// the agent's own ACP entry point, e.g. `LIVE_ACP=1 live goose acp` or
// `LIVE_ACP=1 live gemini --experimental-acp`.
//
// Mapping:
//   - session/update agent_message_chunk → one "response" per turn segment;
//   - tool_call / tool_call_update, once completed or failed → "tool_call"
//     (execute → Shell) or "file_write" per diff;
//   - session/request_permission → the sidecar's web permission flow. Anything
//     but allow picks a reject option, so a timeout or lost connection fails
//     closed;
//   - viewer comments → the next session/prompt (queued while a turn runs:
//     ACP has no steer);
//   - session/prompt returning → "stream_end".
//
// live advertises no fs or terminal capability, so the agent does its own file
// and shell work and reports it through tool calls.

func acpWanted() bool { return os.Getenv("LIVE_ACP") == "1" }

// runACPMode runs binPath+agentArgs as an ACP agent until it exits or the
// operator hits Ctrl-C. preamble (the handoff digest on a fork) is prepended to
// the first prompt so the agent is oriented without a turn of its own.
func runACPMode(sc *sidecar.Sidecar, binPath string, agentArgs []string, preamble string) int {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)

	a := newACPClient(sc.Emit, sc.Log.Printf, sc.RequestPermission)
	a.echo = func(s string) { fmt.Fprintln(os.Stderr, s) }
	a.preamble = preamble
	a.reportModel = func(model string) {
		go func() {
			if err := sc.Client.ReportModel(sc.SessionID, model); err != nil {
				sc.Log.Printf("acp: report model: %v", err)
			}
		}()
	}
	if err := a.rpc.start(ctx, binPath, agentArgs...); err != nil {
		fmt.Fprintf(os.Stderr, "live: acp: %v\n", err)
		return 1
	}
	cwd, _ := os.Getwd()
	if err := a.open(cwd); err != nil {
		fmt.Fprintf(os.Stderr, "live: acp: %v (does %s speak ACP with these arguments?)\n", err, binPath)
		_ = a.rpc.cmd.Process.Signal(syscall.SIGTERM)
		return 1
	}

	sc.OnComment = func(cm api.Comment) {
		who := cm.Username
		if who == "" {
			who = "viewer"
		}
		a.cliEcho("\x1b[33m💬 " + who + ":\x1b[0m " + cm.Message)
		a.send(fmt.Sprintf("%s (live viewer): %s", who, cm.Message), false)
	}
	sc.WebPermissions = liveGating
	sc.Start(ctx)

	fmt.Fprint(os.Stderr, "\x1b[32m● live\x1b[0m ACP agent connected — watch, message, and approve from the session page. Ctrl-C to end.\n\n")
	fmt.Fprint(os.Stderr, "\x1b[36m● live\x1b[0m send the first instruction from the session page to begin.\n")

	select {
	case <-sig:
		_ = a.rpc.write(map[string]any{"jsonrpc": "2.0", "method": "session/cancel", "params": map[string]any{"sessionId": a.sessionID}})
		cancel()
		_ = a.rpc.cmd.Process.Signal(syscall.SIGTERM)
		return 130
	case <-a.rpc.done:
		return a.rpc.exitCode()
	}
}

type acpClient struct {
	rpc         *codexApp // the shared newline-delimited JSON-RPC transport
	emit        func(string, map[string]any)
	logf        func(string, ...any)
	requestPerm func(context.Context, string, string) string
	echo        func(string) // nil in tests
	reportModel func(string) // nil: don't

	sessionID string

	mu       sync.Mutex
	busy     bool     // a session/prompt is in flight
	queue    []string // prompts that arrived while busy
	preamble string   // prepended to the first prompt, then cleared
	reply    strings.Builder
	tools    map[string]*acpTool
}

func newACPClient(emit func(string, map[string]any), logf func(string, ...any), perm func(context.Context, string, string) string) *acpClient {
	a := &acpClient{emit: emit, logf: logf, requestPerm: perm, tools: map[string]*acpTool{}}
	a.rpc = &codexApp{logf: logf, pending: map[int]chan rpcResp{}}
	a.rpc.notification = a.handleNotification
	a.rpc.serverRequest = a.handleRequest
	return a
}

// open runs initialize + session/new and records the session id.
func (a *acpClient) open(cwd string) error {
	if _, err := a.rpc.request("initialize", map[string]any{
		"protocolVersion":    1,
		"clientCapabilities": map[string]any{"fs": map[string]any{"readTextFile": false, "writeTextFile": false}, "terminal": false},
		"clientInfo":         map[string]any{"name": "live", "version": version},
	}); err != nil {
		return fmt.Errorf("initialize failed: %w", err)
	}
	res, err := a.rpc.request("session/new", map[string]any{"cwd": cwd, "mcpServers": []any{}})
	if err != nil {
		return fmt.Errorf("session/new failed: %w", err)
	}
	var r struct {
		SessionID string `json:"sessionId"`
		Models    struct {
			CurrentModelID string `json:"currentModelId"`
		} `json:"models"`
	}
	if json.Unmarshal(res, &r) != nil || r.SessionID == "" {
		return fmt.Errorf("session/new returned no sessionId")
	}
	a.sessionID = r.SessionID
	if r.Models.CurrentModelID != "" && a.reportModel != nil {
		a.reportModel(r.Models.CurrentModelID)
	}
	return nil
}

// send submits text as the next turn, or queues it behind the running one.
// fromOperator marks a prompt the feed should show as a prompt (viewer comments
// already appear as viewer_comment).
func (a *acpClient) send(text string, fromOperator bool) {
	a.mu.Lock()
	if a.busy {
		a.queue = append(a.queue, text)
		a.mu.Unlock()
		return
	}
	a.busy = true
	if a.preamble != "" {
		text = a.preamble + "\n\n" + text
		a.preamble = ""
	}
	a.mu.Unlock()
	if fromOperator {
		a.emit("prompt", map[string]any{"content": clip(text, 4000), "ts": isoNow()})
	}
	go a.runTurn(text)
}

func (a *acpClient) runTurn(text string) {
	for {
		_, err := a.rpc.request("session/prompt", map[string]any{
			"sessionId": a.sessionID,
			"prompt":    []any{map[string]any{"type": "text", "text": text}},
		})
		if err != nil {
			a.logf("acp: session/prompt: %v", err)
		}
		a.flushReply()
		a.emit("stream_end", map[string]any{"ts": isoNow()})

		a.mu.Lock()
		if len(a.queue) == 0 {
			a.busy = false
			a.mu.Unlock()
			return
		}
		// Everything that arrived during the turn goes in as one prompt.
		text = strings.Join(a.queue, "\n")
		a.queue = nil
		a.mu.Unlock()
	}
}

// acpTool accumulates a tool call across tool_call and tool_call_update.
type acpTool struct {
	Title     string          `json:"title"`
	Kind      string          `json:"kind"`
	Status    string          `json:"status"`
	RawInput  json.RawMessage `json:"rawInput"`
	RawOutput json.RawMessage `json:"rawOutput"`
	Content   []acpContent    `json:"content"`
}

type acpContent struct {
	Type    string `json:"type"` // content | diff | terminal
	Content struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Path    string  `json:"path"`
	OldText *string `json:"oldText"`
	NewText string  `json:"newText"`
}

func (a *acpClient) handleNotification(method string, raw json.RawMessage) {
	if method != "session/update" {
		return
	}
	var p struct {
		Update json.RawMessage `json:"update"`
	}
	var u struct {
		SessionUpdate string          `json:"sessionUpdate"`
		Content       json.RawMessage `json:"content"` // a block for chunks, an array for tools
		ToolCallID    string          `json:"toolCallId"`
	}
	if json.Unmarshal(raw, &p) != nil || json.Unmarshal(p.Update, &u) != nil {
		return
	}
	switch u.SessionUpdate {
	case "agent_message_chunk":
		var block struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(u.Content, &block) == nil && block.Type == "text" {
			a.mu.Lock()
			a.reply.WriteString(block.Text)
			a.mu.Unlock()
		}
	case "tool_call", "tool_call_update":
		// The agent's text so far belongs before the tool row in the feed.
		a.flushReply()
		a.trackTool(u.ToolCallID, p.Update)
	}
}

// trackTool merges an update into the call and emits it once it settles.
// Updates only carry the fields that changed, so non-empty fields overwrite.
func (a *acpClient) trackTool(id string, raw json.RawMessage) {
	var upd acpTool
	if id == "" || json.Unmarshal(raw, &upd) != nil {
		return
	}
	a.mu.Lock()
	t := a.tools[id]
	if t == nil {
		t = &acpTool{}
		a.tools[id] = t
	}
	if upd.Title != "" {
		t.Title = upd.Title
	}
	if upd.Kind != "" {
		t.Kind = upd.Kind
	}
	if upd.Status != "" {
		t.Status = upd.Status
	}
	if len(upd.RawInput) > 0 {
		t.RawInput = upd.RawInput
	}
	if len(upd.RawOutput) > 0 {
		t.RawOutput = upd.RawOutput
	}
	if upd.Content != nil {
		t.Content = upd.Content
	}
	done := t.Status == "completed" || t.Status == "failed"
	if done {
		delete(a.tools, id)
	}
	a.mu.Unlock()
	if done {
		a.emitTool(t)
	}
}

func (a *acpClient) emitTool(t *acpTool) {
	var out strings.Builder
	diffs := 0
	for _, c := range t.Content {
		switch c.Type {
		case "diff":
			old := ""
			if c.OldText != nil {
				old = *c.OldText
			}
			diffs++
			a.cliEcho("\x1b[2m✎ " + c.Path + "\x1b[0m")
			a.emit("file_write", map[string]any{
				"tool": "Edit", "file": c.Path,
				"diff": clip(sidecar.UnifiedDiff(c.Path, old, c.NewText), codexOutputCap),
				"ts":   isoNow(),
			})
		case "content":
			if c.Content.Type == "text" {
				out.WriteString(c.Content.Text)
			}
		}
	}
	if diffs > 0 && out.Len() == 0 {
		return // the file rows are the whole story
	}
	if out.Len() == 0 && len(t.RawOutput) > 0 {
		out.Write(t.RawOutput)
	}
	tool, command := acpToolLabel(t.Kind), t.Title
	if t.Kind == "execute" {
		var in struct {
			Command any `json:"command"`
		}
		if json.Unmarshal(t.RawInput, &in) == nil && in.Command != nil {
			if s, ok := in.Command.(string); ok {
				command = s
			} else if parts, ok := in.Command.([]any); ok {
				words := make([]string, len(parts))
				for i, w := range parts {
					words[i] = fmt.Sprint(w)
				}
				command = strings.Join(words, " ")
			}
		}
	}
	if t.Status == "failed" {
		command += " (failed)"
	}
	a.cliEcho("\x1b[2m⚡ " + clip(command, 200) + "\x1b[0m")
	a.emit("tool_call", map[string]any{
		"tool":    tool,
		"command": clip(command, 500),
		"output":  clip(strings.TrimSpace(out.String()), codexOutputCap),
		"ts":      isoNow(),
	})
}

// acpToolLabel names an ACP ToolKind the way the feed names tools elsewhere.
func acpToolLabel(kind string) string {
	switch kind {
	case "execute":
		return "Shell"
	case "read":
		return "Read"
	case "edit":
		return "Edit"
	case "delete":
		return "Delete"
	case "move":
		return "Move"
	case "search":
		return "Search"
	case "fetch":
		return "WebFetch"
	case "think":
		return "Think"
	}
	return "Tool"
}

func (a *acpClient) flushReply() {
	a.mu.Lock()
	text := strings.TrimSpace(a.reply.String())
	a.reply.Reset()
	a.mu.Unlock()
	if text != "" {
		a.cliEcho("\x1b[32m🤖\x1b[0m " + clip(text, 800))
		a.emit("response", map[string]any{"content": clip(text, codexOutputCap), "ts": isoNow()})
	}
}

// handleRequest answers the agent's client-side calls. Only permission is
// served; fs/* and terminal/* were not advertised, so they get an error.
func (a *acpClient) handleRequest(id json.RawMessage, method string, raw json.RawMessage) {
	if method != "session/request_permission" {
		_ = a.rpc.write(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32601, "message": "not supported by live: " + method}})
		return
	}
	go func() {
		a.rpc.respond(id, map[string]any{"outcome": a.decide(raw)})
	}()
}

// decide turns a permission request into an ACP outcome. It fails closed: a
// verdict other than allow/always, or no matching option, never selects an
// allow option.
func (a *acpClient) decide(raw json.RawMessage) map[string]any {
	var p struct {
		ToolCall acpTool `json:"toolCall"`
		Options  []struct {
			OptionID string `json:"optionId"`
			Kind     string `json:"kind"`
		} `json:"options"`
	}
	_ = json.Unmarshal(raw, &p)
	verdict := "allow" // live --no-permissions: the operator opted out of gating
	if liveGating {
		a.flushReply()
		verdict = a.requestPerm(context.Background(), acpToolLabel(p.ToolCall.Kind), firstNonEmptyStr(p.ToolCall.Title, "a tool call"))
	}
	want := map[string][]string{
		"allow":  {"allow_once", "allow_always"},
		"always": {"allow_always", "allow_once"},
	}[verdict]
	if want == nil {
		want = []string{"reject_once", "reject_always"}
	}
	for _, k := range want {
		for _, o := range p.Options {
			if o.Kind == k {
				return map[string]any{"outcome": "selected", "optionId": o.OptionID}
			}
		}
	}
	return map[string]any{"outcome": "cancelled"}
}

func (a *acpClient) cliEcho(line string) {
	if a.echo != nil {
		a.echo(line)
	}
}
