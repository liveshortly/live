package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"

	"github.com/resapce/live/internal/api"
	"github.com/resapce/live/internal/sidecar"
)

// runAppServerMode drives a HEADLESS `codex app-server` over newline-delimited
// JSON-RPC and bridges it to the live sidecar — the real-time alternative to
// the interactive-TUI rollout mode (opt-in via LIVE_CODEX_APPSERVER=1):
//
//   - streamed item notifications become feed events (Emit), live;
//   - approval requests (exec/patch) are answered from the owner's BROWSER via
//     the sidecar permission flow — real permission prompts in the UI;
//   - viewer comments are injected as real turns in real time (turn/steer while
//     a turn runs, turn/start when idle) — push, not poll.
//
// Codex is headless here: there is no terminal TUI. The operator drives and
// watches the session from the web; the terminal just shows status and holds
// the process until Ctrl-C.
func runAppServerMode(sc *sidecar.Sidecar, binPath string, userArgs []string) int {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := &codexApp{
		emit:        sc.Emit,
		logf:        sc.Log.Printf,
		requestPerm: sc.RequestPermission,
		pending:     map[int]chan rpcResp{},
	}
	if err := c.start(ctx, binPath); err != nil {
		fmt.Fprintf(os.Stderr, "live: codex app-server: %v\n", err)
		return 1
	}

	// Handshake + open a thread rooted at the current working dir. on-request
	// approval + workspace-write sandbox means codex escalates risky actions as
	// approval requests we can gate from the web.
	cwd, _ := os.Getwd()
	if _, err := c.request("initialize", map[string]any{
		"clientInfo": map[string]any{"name": "live", "version": version},
	}); err != nil {
		fmt.Fprintf(os.Stderr, "live: codex initialize failed: %v\n", err)
		return 1
	}
	thread, err := c.request("thread/start", map[string]any{
		"cwd":            cwd,
		"approvalPolicy": "on-request",
		"sandbox":        "workspace-write",
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "live: codex thread/start failed: %v\n", err)
		return 1
	}
	c.threadID = threadIDOf(thread)
	if c.threadID == "" {
		fmt.Fprintln(os.Stderr, "live: codex app-server returned no thread id")
		return 1
	}

	// Viewer comments → real-time input. Push into codex as a real turn: steer
	// the running turn, or start a new one when idle.
	sc.OnComment = func(cm api.Comment) {
		who := cm.Username
		if who == "" {
			who = "viewer"
		}
		c.injectInput(fmt.Sprintf("%s (live viewer): %s", who, cm.Message))
	}
	sc.WebPermissions = true // approvals are answered from the web
	sc.Start(ctx)

	fmt.Fprint(os.Stderr, "\x1b[32m● live\x1b[0m codex app-server (real-time) — watch, steer, and approve from the session page. Ctrl-C to end.\n\n")

	// Kick off the first turn from the initial prompt (a plain `live codex "task"`
	// or the injected handoff seed). Without one, the session waits for the first
	// web message.
	if prompt := initialPrompt(userArgs); prompt != "" {
		c.startTurn(prompt)
	} else {
		fmt.Fprint(os.Stderr, "\x1b[36m● live\x1b[0m send the first instruction from the session page to begin.\n")
	}

	// Headless: block until the operator ends it (Ctrl-C) or codex exits.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	select {
	case <-sig:
	case <-c.done:
	}
	signal.Stop(sig)
	cancel()
	_ = c.cmd.Process.Signal(syscall.SIGTERM)
	return 0
}

// ── the JSON-RPC client ───────────────────────────────────────────────────────

type rpcResp struct {
	result json.RawMessage
	err    *rpcError
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type codexApp struct {
	// sinks — injected so the client is testable without a live sidecar.
	emit        func(string, map[string]any)                 // feed events
	logf        func(string, ...any)                         // diagnostics
	requestPerm func(context.Context, string, string) string // web permission verdict

	cmd  *exec.Cmd
	done chan struct{}

	wmu   sync.Mutex // serializes writes to codex stdin
	stdin *bufio.Writer

	mu        sync.Mutex
	nextID    int
	pending   map[int]chan rpcResp
	threadID  string
	activeTur string // id of the in-progress turn (for steer), "" when idle
}

func (c *codexApp) start(ctx context.Context, binPath string) error {
	c.done = make(chan struct{})
	c.cmd = exec.CommandContext(ctx, binPath, "app-server")
	c.cmd.Env = os.Environ()
	stdin, err := c.cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := c.cmd.StdoutPipe()
	if err != nil {
		return err
	}
	c.cmd.Stderr = nil // codex app-server diagnostics: discard
	c.stdin = bufio.NewWriter(stdin)
	if err := c.cmd.Start(); err != nil {
		return err
	}
	go c.readLoop(stdout)
	go func() { c.cmd.Wait(); close(c.done) }()
	return nil
}

// request sends a JSON-RPC request and blocks for its response.
func (c *codexApp) request(method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan rpcResp, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	if err := c.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	resp := <-ch
	if resp.err != nil {
		return nil, fmt.Errorf("%s: %s", method, resp.err.Message)
	}
	return resp.result, nil
}

func (c *codexApp) respond(id json.RawMessage, result any) {
	_ = c.write(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (c *codexApp) write(obj map[string]any) error {
	b, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if _, err := c.stdin.Write(append(b, '\n')); err != nil {
		return err
	}
	return c.stdin.Flush()
}

// readLoop demultiplexes codex's output: responses to our requests, server→
// client requests (approvals), and notifications (streamed items).
func (c *codexApp) readLoop(stdout interface{ Read([]byte) (int, error) }) {
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 16<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  *rpcError       `json:"error"`
		}
		if json.Unmarshal(line, &msg) != nil {
			continue
		}
		switch {
		case msg.Method == "" && len(msg.ID) > 0:
			// response to one of our requests
			var id int
			if json.Unmarshal(msg.ID, &id) == nil {
				c.mu.Lock()
				ch := c.pending[id]
				delete(c.pending, id)
				c.mu.Unlock()
				if ch != nil {
					ch <- rpcResp{result: msg.Result, err: msg.Error}
				}
			}
		case msg.Method != "" && len(msg.ID) > 0:
			c.handleServerRequest(msg.ID, msg.Method, msg.Params)
		case msg.Method != "":
			c.handleNotification(msg.Method, msg.Params)
		}
	}
}

// ── input injection (viewer comments / initial prompt) ────────────────────────

func (c *codexApp) startTurn(text string) {
	go func() {
		_, err := c.request("turn/start", map[string]any{
			"threadId": c.threadID,
			"input":    []any{map[string]any{"type": "text", "text": text}},
		})
		if err != nil {
			c.logf("appserver: turn/start: %v", err)
		}
	}()
}

// injectInput delivers a viewer comment into codex right now: steer the running
// turn if there is one, otherwise start a fresh turn.
func (c *codexApp) injectInput(text string) {
	c.mu.Lock()
	turn := c.activeTur
	c.mu.Unlock()
	if turn == "" {
		c.startTurn(text)
		return
	}
	go func() {
		_, err := c.request("turn/steer", map[string]any{
			"threadId":       c.threadID,
			"expectedTurnId": turn,
			"input":          []any{map[string]any{"type": "text", "text": text}},
		})
		if err != nil {
			// A turn may have just finished; fall back to a new turn.
			c.logf("appserver: turn/steer failed (%v) — starting a new turn", err)
			c.startTurn(text)
		}
	}()
}
