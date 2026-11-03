package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// newTestApp builds a codexApp whose sinks capture into c.
func newTestApp() (*codexApp, *captured) {
	cap := &captured{}
	var mu sync.Mutex
	app := &codexApp{
		emit: func(t string, p map[string]any) { mu.Lock(); cap.emit(t, p); mu.Unlock() },
		logf: func(string, ...any) {},
		requestPerm: func(context.Context, string, string) string {
			return "allow"
		},
		pending: map[int]chan rpcResp{},
	}
	return app, cap
}

// Notification shapes below mirror real codex app-server 0.142.5 output.
func TestCodexApp_NotificationMapping(t *testing.T) {
	app, cap := newTestApp()

	app.handleNotification("turn/started", json.RawMessage(`{"turn":{"id":"turn-1"}}`))
	if app.activeTur != "turn-1" {
		t.Fatalf("turn/started should set active turn, got %q", app.activeTur)
	}

	app.handleNotification("item/completed", json.RawMessage(
		`{"item":{"type":"userMessage","content":[{"type":"text","text":"build the thing"}]}}`))
	app.handleNotification("item/completed", json.RawMessage(
		`{"item":{"type":"commandExecution","command":"/bin/zsh -lc 'echo hi'","aggregatedOutput":"hi\n"}}`))
	app.handleNotification("item/completed", json.RawMessage(
		`{"item":{"type":"agentMessage","text":"done"}}`))
	app.handleNotification("turn/completed", json.RawMessage(`{"turn":{"id":"turn-1"}}`))

	if app.activeTur != "" {
		t.Fatalf("turn/completed should clear active turn, got %q", app.activeTur)
	}
	if got := cap.ofType("prompt"); len(got) != 1 || got[0].payload["content"] != "build the thing" {
		t.Fatalf("prompt = %+v", cap.ofType("prompt"))
	}
	tc := cap.ofType("tool_call")
	if len(tc) != 1 || tc[0].payload["tool"] != "Shell" || tc[0].payload["output"] != "hi" {
		t.Fatalf("tool_call = %+v", tc)
	}
	if got := cap.ofType("response"); len(got) != 1 || got[0].payload["content"] != "done" {
		t.Fatalf("response = %+v", got)
	}
	if len(cap.ofType("stream_end")) != 1 {
		t.Fatalf("want one stream_end")
	}
}

func TestCodexApp_InjectedCommentsNotDoubleEmitted(t *testing.T) {
	app, cap := newTestApp()
	// a comment we injected comes back as a userMessage prefixed with the marker
	app.handleNotification("item/completed", json.RawMessage(
		`{"item":{"type":"userMessage","content":[{"type":"text","text":"rohit (live viewer): try dark mode"}]}}`))
	if got := cap.ofType("prompt"); len(got) != 0 {
		t.Fatalf("injected viewer input must not re-emit as prompt, got %+v", got)
	}
}

func TestThreadIDOf(t *testing.T) {
	if id := threadIDOf(json.RawMessage(`{"thread":{"id":"abc"}}`)); id != "abc" {
		t.Fatalf("nested thread.id = %q", id)
	}
	if id := threadIDOf(json.RawMessage(`{"threadId":"xyz"}`)); id != "xyz" {
		t.Fatalf("flat threadId = %q", id)
	}
}

func TestInitialPrompt(t *testing.T) {
	if initialPrompt([]string{"--model", "gpt", "do the task"}) != "do the task" {
		t.Fatal("initialPrompt should return the last positional, not a flag value")
	}
	if initialPrompt([]string{"seed"}) != "seed" {
		t.Fatal("sole positional")
	}
	if initialPrompt([]string{"-p"}) != "" {
		t.Fatal("no positional → empty")
	}
	// `-c key=value` overrides are injected by live (perm hooks, handoff digest).
	// A trailing one must never be mistaken for the opening prompt.
	if p := initialPrompt([]string{"-c", "developer_instructions=prior context"}); p != "" {
		t.Fatalf("a -c value is not a prompt, got %q", p)
	}
	if p := initialPrompt([]string{"-c", "hooks.PreToolUse=[]", "real prompt"}); p != "real prompt" {
		t.Fatalf("positional after a -c override, got %q", p)
	}
}

// A fork must orient the app-server agent the same way the TUI is oriented.
// Without developerInstructions on thread/start, `LIVE_CODEX_APPSERVER=1
// live --session <id> codex` starts with no prior context at all.
func TestThreadStartParams(t *testing.T) {
	plain := threadStartParams("/repo", "")
	if _, ok := plain["developerInstructions"]; ok {
		t.Fatal("non-fork runs must omit developerInstructions entirely, not send an empty string")
	}
	if plain["cwd"] != "/repo" || plain["approvalPolicy"] != "on-request" || plain["sandbox"] != "workspace-write" {
		t.Fatalf("unexpected base params: %v", plain)
	}

	forked := threadStartParams("/repo", "prior context: we were fixing the parser")
	got, ok := forked["developerInstructions"].(string)
	if !ok || !strings.Contains(got, "fixing the parser") {
		t.Fatalf("fork must carry the digest into thread/start, got %v", forked["developerInstructions"])
	}
}

func TestParseCodexFileChanges(t *testing.T) {
	got := parseCodexFileChanges(json.RawMessage(`{"a.go":{},"b.go":{}}`))
	if len(got) != 2 {
		t.Fatalf("map form: %v", got)
	}
	got = parseCodexFileChanges(json.RawMessage(`[{"path":"c.go"}]`))
	if len(got) != 1 || got[0].path != "c.go" {
		t.Fatalf("array form: %v", got)
	}
	// LSH-02: keep the diff when codex sends one, under either key.
	got = parseCodexFileChanges(json.RawMessage(`[{"path":"d.go","unified_diff":"@@ -1 +1 @@\n-a\n+b"}]`))
	if len(got) != 1 || !strings.Contains(got[0].diff, "+b") {
		t.Fatalf("array diff: %v", got)
	}
	got = parseCodexFileChanges(json.RawMessage(`{"e.go":{"diff":"+x"}}`))
	if len(got) != 1 || got[0].diff != "+x" {
		t.Fatalf("map diff: %v", got)
	}
}

// TestCodexAppServerIntegration drives a REAL `codex app-server` end to end:
// initialize → thread/start → turn/start → assert the streamed items map to
// feed events. Gated behind LIVE_CODEX_IT=1 (needs codex auth + network).
func TestCodexAppServerIntegration(t *testing.T) {
	if os.Getenv("LIVE_CODEX_IT") != "1" {
		t.Skip("set LIVE_CODEX_IT=1 to run the live codex app-server integration test")
	}
	bin, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("codex not installed")
	}
	app, cap := newTestApp()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := app.start(ctx, bin, "app-server"); err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := app.request("initialize", map[string]any{
		"clientInfo": map[string]any{"name": "live-test", "version": "0"},
	}); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	cwd, _ := os.Getwd()
	thread, err := app.request("thread/start", map[string]any{
		"cwd": cwd, "approvalPolicy": "on-request", "sandbox": "workspace-write",
	})
	if err != nil {
		t.Fatalf("thread/start: %v", err)
	}
	app.threadID = threadIDOf(thread)
	if app.threadID == "" {
		t.Fatal("no thread id")
	}
	app.startTurn("Run the shell command: echo it-works-appserver. Then stop.")

	deadline := time.After(45 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("timed out; events so far: %d", len(cap.events))
		case <-time.After(500 * time.Millisecond):
			if len(cap.ofType("tool_call")) > 0 && len(cap.ofType("stream_end")) > 0 {
				return // saw a tool call + turn end: the pipeline works
			}
		}
	}
}

// Legacy approval methods answer in ReviewDecision words; the thread/start
// response's model is reported once.
func TestCodexApp_LegacyApprovalAndModel(t *testing.T) {
	var out bytes.Buffer
	var mu sync.Mutex
	app, _ := newTestApp()
	app.stdin = bufio.NewWriter(&lockedWriter{mu: &mu, w: &out})
	var models []string
	app.reportModel = func(m string) { models = append(models, m) }
	app.noteModel(json.RawMessage(`{"model":"gpt-5.5","thread":{"id":"t"}}`))
	app.noteModel(json.RawMessage(`{"thread":{"model":"other"}}`))
	if len(models) != 1 || models[0] != "gpt-5.5" {
		t.Fatalf("models = %v", models)
	}
	app.handleServerRequest(json.RawMessage(`7`), "execCommandApproval", json.RawMessage(`{"command":["ls"]}`))
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		s := out.String()
		mu.Unlock()
		if strings.Contains(s, `"decision":"approved"`) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("reply = %q", s)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// LSH-12: the app-server protocol requires the `initialized` notification after
// the initialize response and before thread/start.
func TestCodexApp_HandshakeOrder(t *testing.T) {
	app, _ := newTestApp()
	toApp, fromTest := io.Pipe() // codex → live
	fromApp, toTest := io.Pipe() // live → codex
	app.stdin = bufio.NewWriter(toTest)
	go app.readLoop(toApp)

	var got []string
	go func() {
		sc := bufio.NewScanner(fromApp)
		for sc.Scan() {
			var m struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			_ = json.Unmarshal(sc.Bytes(), &m)
			got = append(got, m.Method)
			if len(m.ID) > 0 {
				result := `{}`
				if m.Method == "thread/start" {
					result = `{"thread":{"id":"t1"}}`
				}
				fmt.Fprintf(fromTest, `{"id":%s,"result":%s}`+"\n", m.ID, result)
			}
		}
	}()

	thread, err := app.handshake("live", "/tmp", "")
	if err != nil {
		t.Fatal(err)
	}
	if threadIDOf(thread) != "t1" {
		t.Fatalf("thread = %s", thread)
	}
	if want := []string{"initialize", "initialized", "thread/start"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v", got, want)
	}
}
