package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/liveshortly/live/internal/api"
	"github.com/liveshortly/live/internal/sidecar"
)

// Pi native mode: drive the `pi` agent (pi.dev) over its own RPC protocol
// (`pi --mode rpc`) instead of recording its terminal. Pi exposes a
// bidirectional JSONL protocol — commands in on stdin, responses + session
// events out on stdout — plus an extension-UI dialog subprotocol for
// permissions and questions. That gives the same structured feed, web
// approvals and comment delivery claude gets from hooks.
//
// Wire shapes (docs ship with the pi package: docs/json.md, docs/rpc.md,
// docs/rpc-commands.md, docs/rpc-extension-ui.md):
//   - commands  : {"id","type",...}                (we write)
//   - responses : {"id","type":"response","command","success","data"}
//   - events    : {"type",...}                     (no id — session activity)
//   - dialogs   : {"type":"extension_ui_request","id","method",...}
//
// Mapping to the live feed:
//   - assistant text  → "response" (streamed, flushed per turn/tool)
//   - assistant think → "thinking"
//   - tool_execution  → "tool_call"
//   - confirm / select(Allow,Block) dialog → the web permission flow
//   - other select/input dialog → "question_requested" (shown; Phase 2 answers)
//   - viewer comments → prompt / steer
//   - turn_end        → "stream_end"

// runPiMode runs `pi --mode rpc` until it exits or the operator hits Ctrl-C.
// userArgs carries any model flag the daemon chose (--model provider/id);
// preamble (the handoff digest on a fork) is prepended to the first prompt.
func runPiMode(sc *sidecar.Sidecar, binPath string, userArgs []string, preamble string) int {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)
	if headless() {
		sc.OnEnded = func() { endOnSignal(sig) }
	}

	p := newPiClient(sc.Emit, sc.Log.Printf, sc.RequestPermission)
	p.echo = func(s string) { fmt.Fprintln(os.Stderr, s) }
	p.preamble = preamble
	p.reportModel = func(model string) {
		go func() {
			if err := sc.Client.ReportModel(sc.SessionID, model); err != nil {
				sc.Log.Printf("pi: report model: %v", err)
			}
		}()
	}

	// pi runs the agent loop in rpc mode; --no-session keeps pi from persisting
	// its own session file (live owns session lifecycle). The operator's model
	// flag, if any, rides on userArgs.
	args := append([]string{"--mode", "rpc", "--no-session"}, userArgs...)
	if err := p.rpc.start(ctx, binPath, args...); err != nil {
		fmt.Fprintf(os.Stderr, "live: pi: %v\n", err)
		return 1
	}
	// Report the active model once pi is up (get_state is a no-cost query).
	go p.reportInitialModel()

	sc.OnComment = func(cm api.Comment) {
		who := cm.Username
		if who == "" {
			who = "viewer"
		}
		p.cliEcho("\x1b[33m💬 " + who + ":\x1b[0m " + cm.Message)
		p.send(fmt.Sprintf("%s (live viewer): %s", who, cm.Message), false)
	}
	sc.WebPermissions = liveGating
	sc.Start(ctx)

	fmt.Fprint(os.Stderr, "\x1b[32m● live\x1b[0m pi connected — watch, message, and approve from the session page. Ctrl-C to end.\n\n")
	fmt.Fprint(os.Stderr, "\x1b[36m● live\x1b[0m send the first instruction from the session page to begin.\n")

	select {
	case <-sig:
		_ = p.rpc.write(map[string]any{"type": "abort"})
		cancel()
		_ = p.rpc.cmd.Process.Signal(syscall.SIGTERM)
		return 130
	case <-p.rpc.done:
		return p.rpc.exitCode()
	}
}

// ── transport ───────────────────────────────────────────────────────────────

// piRPC is a newline-delimited JSON transport over pi's stdio. Unlike codexApp
// it is not JSON-RPC: commands carry a free-form {id,type}, responses echo the
// id, and events carry no id. Each record is one line.
type piRPC struct {
	logf    func(string, ...any)
	cmd     *exec.Cmd
	stdin   *json.Encoder
	writeMu sync.Mutex
	done    chan struct{}
	exit    int

	mu       sync.Mutex
	seq      int
	pending  map[string]chan json.RawMessage
	onEvent  func(eventType string, raw json.RawMessage)
	onDialog func(raw json.RawMessage)
}

func (r *piRPC) start(ctx context.Context, bin string, args ...string) error {
	r.cmd = exec.CommandContext(ctx, bin, args...)
	r.cmd.Stderr = os.Stderr // pi logs diagnostics to stderr; stdout is JSONL
	stdin, err := r.cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := r.cmd.StdoutPipe()
	if err != nil {
		return err
	}
	r.stdin = json.NewEncoder(stdin)
	r.done = make(chan struct{})
	if r.pending == nil {
		r.pending = map[string]chan json.RawMessage{}
	}
	if err := r.cmd.Start(); err != nil {
		return err
	}
	go r.read(stdout)
	go func() {
		err := r.cmd.Wait()
		if ee, ok := err.(*exec.ExitError); ok {
			r.exit = ee.ExitCode()
		} else if err != nil {
			r.exit = 1
		}
		close(r.done)
	}()
	return nil
}

func (r *piRPC) read(stdout interface{ Read([]byte) (int, error) }) {
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024) // tool output can be large
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" {
			continue
		}
		var head struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		}
		raw := json.RawMessage(line)
		if json.Unmarshal(raw, &head) != nil {
			continue
		}
		switch {
		case head.Type == "response":
			r.mu.Lock()
			ch := r.pending[head.ID]
			delete(r.pending, head.ID)
			r.mu.Unlock()
			if ch != nil {
				ch <- raw
			}
		case head.Type == "extension_ui_request":
			if r.onDialog != nil {
				r.onDialog(raw)
			}
		default:
			if r.onEvent != nil {
				r.onEvent(head.Type, raw)
			}
		}
	}
}

func (r *piRPC) write(obj any) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	return r.stdin.Encode(obj)
}

// request sends a command with an id and waits for its response.
func (r *piRPC) request(cmdType string, fields map[string]any) (json.RawMessage, error) {
	r.mu.Lock()
	r.seq++
	id := fmt.Sprintf("c%d", r.seq)
	ch := make(chan json.RawMessage, 1)
	r.pending[id] = ch
	r.mu.Unlock()

	msg := map[string]any{"id": id, "type": cmdType}
	for k, v := range fields {
		msg[k] = v
	}
	if err := r.write(msg); err != nil {
		return nil, err
	}
	select {
	case resp := <-ch:
		return resp, nil
	case <-r.done:
		return nil, fmt.Errorf("pi exited before responding to %s", cmdType)
	}
}

func (r *piRPC) exitCode() int { return r.exit }

// ── client ──────────────────────────────────────────────────────────────────

type piClient struct {
	rpc         *piRPC
	emit        func(string, map[string]any)
	logf        func(string, ...any)
	requestPerm func(context.Context, string, string) string
	echo        func(string)
	reportModel func(string)

	mu       sync.Mutex
	busy     bool
	queue    []string
	preamble string
	reply    strings.Builder
	thinking strings.Builder
	tools    map[string]*piTool
}

func newPiClient(emit func(string, map[string]any), logf func(string, ...any), perm func(context.Context, string, string) string) *piClient {
	p := &piClient{emit: emit, logf: logf, requestPerm: perm, tools: map[string]*piTool{}}
	p.rpc = &piRPC{logf: logf, pending: map[string]chan json.RawMessage{}}
	p.rpc.onEvent = p.handleEvent
	p.rpc.onDialog = p.handleDialog
	return p
}

func (p *piClient) reportInitialModel() {
	resp, err := p.rpc.request("get_state", nil)
	if err != nil {
		return
	}
	var r struct {
		Data struct {
			Model struct {
				Provider string `json:"provider"`
				ID       string `json:"id"`
			} `json:"model"`
		} `json:"data"`
	}
	if json.Unmarshal(resp, &r) == nil && r.Data.Model.ID != "" && p.reportModel != nil {
		id := r.Data.Model.ID
		if r.Data.Model.Provider != "" {
			id = r.Data.Model.Provider + "/" + id
		}
		p.reportModel(id)
	}
}

// send submits text as the next turn, or steers it into the running one.
// fromOperator marks a prompt the feed should show as a prompt (viewer comments
// already appear as viewer_comment).
func (p *piClient) send(text string, fromOperator bool) {
	p.mu.Lock()
	if p.preamble != "" {
		text = p.preamble + "\n\n" + text
		p.preamble = ""
	}
	busy := p.busy
	if busy {
		p.queue = append(p.queue, text)
	} else {
		p.busy = true
	}
	p.mu.Unlock()

	if fromOperator {
		p.emit("prompt", map[string]any{"content": clip(text, 4000), "ts": isoNow()})
	}
	if busy {
		// pi supports mid-turn steering; the running turn picks it up.
		_ = p.rpc.write(map[string]any{"type": "steer", "message": text})
		return
	}
	go p.runTurn(text)
}

func (p *piClient) runTurn(text string) {
	for {
		if _, err := p.rpc.request("prompt", map[string]any{"message": text}); err != nil {
			p.logf("pi: prompt: %v", err)
		}
		// A prompt response means accepted, not finished; agent_settled (handled
		// in handleEvent) flushes the reply and ends the turn. Drain the queue.
		p.mu.Lock()
		if len(p.queue) == 0 {
			p.busy = false
			p.mu.Unlock()
			return
		}
		text = strings.Join(p.queue, "\n")
		p.queue = nil
		p.mu.Unlock()
	}
}

// ── event translation ───────────────────────────────────────────────────────

type piTool struct {
	Name string
	Args json.RawMessage
}

func (p *piClient) handleEvent(eventType string, raw json.RawMessage) {
	switch eventType {
	case "message_update":
		var e struct {
			Ev struct {
				Type  string `json:"type"`
				Delta string `json:"delta"`
			} `json:"assistantMessageEvent"`
		}
		if json.Unmarshal(raw, &e) != nil {
			return
		}
		switch e.Ev.Type {
		case "text_delta":
			p.mu.Lock()
			p.reply.WriteString(e.Ev.Delta)
			p.mu.Unlock()
		case "thinking_delta":
			p.mu.Lock()
			p.thinking.WriteString(e.Ev.Delta)
			p.mu.Unlock()
		case "thinking_end":
			p.flushThinking()
		}
	case "tool_execution_start":
		var e struct {
			ID   string          `json:"toolCallId"`
			Name string          `json:"toolName"`
			Args json.RawMessage `json:"args"`
		}
		if json.Unmarshal(raw, &e) == nil && e.ID != "" {
			// Assistant prose so far belongs before the tool row.
			p.flushReply()
			p.mu.Lock()
			p.tools[e.ID] = &piTool{Name: e.Name, Args: e.Args}
			p.mu.Unlock()
		}
	case "tool_execution_end":
		p.emitTool(raw)
	case "turn_end", "agent_settled":
		p.flushThinking()
		p.flushReply()
		p.emit("stream_end", map[string]any{"ts": isoNow()})
	case "thinking_level_changed", "queue_update", "session_info_changed",
		"compaction_start", "compaction_end", "agent_start", "agent_end",
		"turn_start", "message_start", "message_end", "auto_retry_start",
		"auto_retry_end", "extension_error", "bash_execution_update":
		// not surfaced in the feed (yet)
	}
}

func (p *piClient) emitTool(raw json.RawMessage) {
	var e struct {
		ID      string          `json:"toolCallId"`
		Name    string          `json:"toolName"`
		IsError bool            `json:"isError"`
		Result  json.RawMessage `json:"result"`
	}
	if json.Unmarshal(raw, &e) != nil || e.ID == "" {
		return
	}
	p.mu.Lock()
	t := p.tools[e.ID]
	delete(p.tools, e.ID)
	p.mu.Unlock()
	name := e.Name
	if t != nil && t.Name != "" {
		name = t.Name
	}
	command := piToolCommand(name, toolArgs(t))
	out := piResultText(e.Result)
	if e.IsError {
		command += " (failed)"
	}
	p.cliEcho("\x1b[2m⚡ " + clip(command, 200) + "\x1b[0m")
	p.emit("tool_call", map[string]any{
		"tool":    piToolLabel(name),
		"command": clip(command, 500),
		"output":  clip(strings.TrimSpace(out), codexOutputCap),
		"ts":      isoNow(),
	})
}

func toolArgs(t *piTool) json.RawMessage {
	if t == nil {
		return nil
	}
	return t.Args
}

// piToolCommand renders a one-line subject for a tool call from its args.
func piToolCommand(name string, args json.RawMessage) string {
	var m map[string]any
	if json.Unmarshal(args, &m) != nil || m == nil {
		return name
	}
	for _, k := range []string{"command", "cmd", "path", "file", "filePath", "pattern", "query", "url"} {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
			if parts, ok := v.([]any); ok {
				words := make([]string, len(parts))
				for i, w := range parts {
					words[i] = fmt.Sprint(w)
				}
				return strings.Join(words, " ")
			}
		}
	}
	return name
}

// piToolLabel maps pi's tool names to the feed's tool vocabulary.
func piToolLabel(name string) string {
	switch strings.ToLower(name) {
	case "bash", "shell", "exec":
		return "Shell"
	case "read", "read_file", "cat":
		return "Read"
	case "edit", "write", "write_file", "apply_patch":
		return "Edit"
	case "search", "grep", "glob", "find":
		return "Search"
	case "fetch", "web_fetch", "webfetch":
		return "WebFetch"
	}
	if name == "" {
		return "Tool"
	}
	return name
}

// piResultText pulls display text out of a pi tool result ({content:[{text}]}).
func piResultText(result json.RawMessage) string {
	var r struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(result, &r) != nil {
		return string(result)
	}
	var b strings.Builder
	for _, c := range r.Content {
		if c.Type == "text" {
			b.WriteString(c.Text)
		}
	}
	if b.Len() == 0 {
		return string(result)
	}
	return b.String()
}

func (p *piClient) flushReply() {
	p.mu.Lock()
	text := strings.TrimSpace(p.reply.String())
	p.reply.Reset()
	p.mu.Unlock()
	if text != "" {
		p.cliEcho("\x1b[32m🤖\x1b[0m " + clip(text, 800))
		p.emit("response", map[string]any{"content": clip(text, codexOutputCap), "ts": isoNow()})
	}
}

func (p *piClient) flushThinking() {
	p.mu.Lock()
	text := strings.TrimSpace(p.thinking.String())
	p.thinking.Reset()
	p.mu.Unlock()
	if text != "" {
		p.emit("thinking", map[string]any{"content": clip(text, codexOutputCap), "ts": isoNow()})
	}
}

// ── permission / question dialogs ────────────────────────────────────────────

func (p *piClient) handleDialog(raw json.RawMessage) {
	var d struct {
		ID      string   `json:"id"`
		Method  string   `json:"method"`
		Title   string   `json:"title"`
		Message string   `json:"message"`
		Options []string `json:"options"`
	}
	if json.Unmarshal(raw, &d) != nil {
		return
	}
	switch d.Method {
	case "confirm":
		go p.answerConfirm(d.ID, firstNonEmptyStr(d.Title, d.Message, "Proceed?"))
	case "select":
		if allow, block, ok := permOptions(d.Options); ok {
			go p.answerPermission(d.ID, firstNonEmptyStr(d.Title, "a tool call"), allow, block)
			return
		}
		// A genuine multi-choice question: show it; Phase 2 feeds a real answer
		// back. For now cancel so the agent falls back to its default path.
		p.emit("question_requested", map[string]any{
			"message": firstNonEmptyStr(d.Title, "pi is asking a question"),
			"kind":    "question",
			"options": d.Options,
			"ts":      isoNow(),
		})
		_ = p.rpc.write(map[string]any{"type": "extension_ui_response", "id": d.ID, "cancelled": true})
	case "input", "editor":
		p.emit("question_requested", map[string]any{
			"message": firstNonEmptyStr(d.Title, "pi is asking for input"),
			"kind":    "input",
			"ts":      isoNow(),
		})
		_ = p.rpc.write(map[string]any{"type": "extension_ui_response", "id": d.ID, "cancelled": true})
	default:
		// fire-and-forget (notify/setStatus/setWidget/setTitle/set_editor_text):
		// no response expected.
	}
}

func (p *piClient) answerConfirm(id, title string) {
	verdict := "allow"
	if liveGating {
		p.flushReply()
		verdict = p.requestPerm(context.Background(), "Confirm", title)
	}
	confirmed := verdict == "allow" || verdict == "always"
	_ = p.rpc.write(map[string]any{"type": "extension_ui_response", "id": id, "confirmed": confirmed})
}

func (p *piClient) answerPermission(id, title, allowOpt, blockOpt string) {
	verdict := "allow"
	if liveGating {
		p.flushReply()
		verdict = p.requestPerm(context.Background(), "Tool", title)
	}
	choice := blockOpt
	if verdict == "allow" || verdict == "always" {
		choice = allowOpt
	}
	_ = p.rpc.write(map[string]any{"type": "extension_ui_response", "id": id, "value": choice})
}

// permOptions recognises a permission-style select: a two-option allow/deny
// choice. Returns the allow and deny option strings as pi labelled them.
func permOptions(opts []string) (allow, block string, ok bool) {
	if len(opts) != 2 {
		return "", "", false
	}
	for _, o := range opts {
		switch strings.ToLower(strings.TrimSpace(o)) {
		case "allow", "yes", "approve", "accept", "proceed", "continue":
			allow = o
		case "block", "deny", "no", "reject", "cancel", "stop":
			block = o
		}
	}
	if allow != "" && block != "" {
		return allow, block, true
	}
	return "", "", false
}

func (p *piClient) cliEcho(line string) {
	if p.echo != nil {
		p.echo(line)
	}
}
