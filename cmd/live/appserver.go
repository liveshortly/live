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

	"github.com/liveshortly/live/internal/api"
	"github.com/liveshortly/live/internal/sidecar"
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
// devInstructions carries the handoff digest on a fork. It orients the agent
// without submitting a turn — the same contract as the codex TUI's
// `-c developer_instructions=…` and claude's --append-system-prompt-file. Empty
// on a normal (non-fork) run.
func runAppServerMode(sc *sidecar.Sidecar, binPath string, userArgs []string, devInstructions string) int {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)
	if headless() {
		sc.OnEnded = func() { endOnSignal(sig) }
	}

	c := &codexApp{
		emit:        sc.Emit,
		logf:        sc.Log.Printf,
		requestPerm: sc.RequestPermission,
		echo:        func(s string) { fmt.Fprintln(os.Stderr, s) }, // live CLI transcript
		pending:     map[int]chan rpcResp{},
		reportModel: func(model string) {
			go func() {
				if err := sc.Client.ReportModel(sc.SessionID, model); err != nil {
					sc.Log.Printf("appserver: report model: %v", err)
				}
			}()
		},
	}
	if err := c.start(ctx, binPath, "app-server"); err != nil {
		fmt.Fprintf(os.Stderr, "live: codex app-server: %v\n", err)
		return 1
	}

	// Handshake + open a thread rooted at the current working dir. on-request
	// approval + workspace-write sandbox means codex escalates risky actions as
	// approval requests we can gate from the web.
	cwd, _ := os.Getwd()
	thread, err := c.handshake("live", cwd, devInstructions, argValue(userArgs, "--model", "-m"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "live: codex %v\n", err)
		return 1
	}
	c.threadID = threadIDOf(thread)
	c.noteModel(thread)
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
		c.echo("\x1b[33m💬 " + who + ":\x1b[0m " + cm.Message)
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
	select {
	case <-sig:
		cancel()
		_ = c.cmd.Process.Signal(syscall.SIGTERM)
		return 130
	case <-c.done:
		cancel()
		return c.exitCode()
	}
}

// threadStartParams builds the thread/start payload. on-request approval +
// workspace-write sandbox make codex escalate risky actions as approval requests
// we gate from the web. On a fork, developerInstructions carries the handoff
// digest; the key is omitted entirely when there is none, since codex rejects a
// null where it expects a string.
func threadStartParams(cwd, devInstructions, model string) map[string]any {
	p := map[string]any{
		"cwd":            cwd,
		"approvalPolicy": appServerApprovalPolicy(),
		"sandbox":        "workspace-write",
	}
	if devInstructions != "" {
		p["developerInstructions"] = devInstructions
	}
	if model != "" {
		p["model"] = model // app-server ignores argv, so a web-picked --model rides here
	}
	return p
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
	echo        func(string)                                 // live CLI transcript (nil in tests)
	reportModel func(string)                                 // report codex's model once (nil: don't)

	notification  func(string, json.RawMessage)
	serverRequest func(json.RawMessage, string, json.RawMessage)
	directory     string
	cmd           *exec.Cmd
	done          chan struct{}

	wmu   sync.Mutex // serializes writes to codex stdin
	stdin *bufio.Writer

	mu            sync.Mutex
	nextID        int
	pending       map[int]chan rpcResp
	threadID      string
	activeTur     string // id of the in-progress turn (for steer), "" when idle
	modelReported bool
	exit          int
}

// start launches binPath with args (codex: "app-server"; an ACP agent: its own
// argv) and wires its stdio to the JSON-RPC client.
func (c *codexApp) start(ctx context.Context, binPath string, args ...string) error {
	c.done = make(chan struct{})
	c.cmd = exec.CommandContext(ctx, binPath, args...)
	c.cmd.Env = envWithout(os.Environ(), "LIVE_POCKET_KEY")
	c.cmd.Dir = c.directory
	c.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.cmd.Cancel = func() error { return syscall.Kill(-c.cmd.Process.Pid, syscall.SIGKILL) }
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
	readDone := make(chan struct{})
	go func() { c.readLoop(stdout); close(readDone) }()
	go func() {
		<-readDone
		err := c.cmd.Wait()
		c.mu.Lock()
		if c.cmd.ProcessState != nil {
			c.exit = c.cmd.ProcessState.ExitCode()
		} else if err != nil {
			c.exit = 1
		}
		c.mu.Unlock()
		close(c.done)
	}()
	return nil
}

// handshake runs the app-server opening sequence: initialize, then the
// `initialized` notification the protocol requires before any other request,
// then thread/start. It returns the thread/start result.
func (c *codexApp) handshake(client, cwd, devInstructions, model string) (json.RawMessage, error) {
	if _, err := c.request("initialize", map[string]any{
		"clientInfo": map[string]any{"name": client, "version": version},
	}); err != nil {
		return nil, fmt.Errorf("initialize failed: %w", err)
	}
	if err := c.write(map[string]any{"method": "initialized"}); err != nil {
		return nil, fmt.Errorf("initialized failed: %w", err)
	}
	thread, err := c.request("thread/start", threadStartParams(cwd, devInstructions, model))
	if err != nil {
		return nil, fmt.Errorf("thread/start failed: %w", err)
	}
	return thread, nil
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
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}
	var resp rpcResp
	select {
	case resp = <-ch:
	case <-c.done:
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("%s: codex app-server exited", method)
	}
	if resp.err != nil {
		return nil, fmt.Errorf("%s: %s", method, resp.err.Message)
	}
	return resp.result, nil
}

func (c *codexApp) exitCode() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.exit
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
			if c.serverRequest != nil {
				c.serverRequest(msg.ID, msg.Method, msg.Params)
			} else {
				c.handleServerRequest(msg.ID, msg.Method, msg.Params)
			}
		case msg.Method != "":
			if c.notification != nil {
				c.notification(msg.Method, msg.Params)
			} else {
				c.handleNotification(msg.Method, msg.Params)
			}
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

// appServerApprovalPolicy is on-request (escalations come to the web) unless
// the operator opted out of gating, in which case codex is told not to ask.
func appServerApprovalPolicy() string {
	if !liveGating {
		return "never"
	}
	return "on-request"
}
