package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// cmdChannel is a minimal MCP stdio server that Claude Code spawns via the
// temp --mcp-config written by the claude adapter. It declares the
// experimental claude/channel capability and pushes viewer comments —
// received from the parent shim's sidecar over localhost SSE — into the
// conversation as channel notifications, which reach Claude even while idle.
//
// It is intentionally dependency-free: MCP stdio is newline-delimited
// JSON-RPC 2.0, and this server only ever answers initialize/tools/list and
// emits notifications.
func cmdChannel() {
	addr := os.Getenv("LIVE_SIDECAR")
	token := os.Getenv("LIVE_TOKEN")

	var outMu sync.Mutex
	send := func(v any) {
		b, err := json.Marshal(v)
		if err != nil {
			return
		}
		outMu.Lock()
		os.Stdout.Write(append(b, '\n'))
		outMu.Unlock()
	}

	var logw io.Writer = os.Stderr
	if home, err := os.UserHomeDir(); err == nil {
		if f, err := os.OpenFile(home+"/.liveshortly/channel.log",
			os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
			defer f.Close()
			logw = f
		}
	}
	logf := func(format string, args ...any) {
		fmt.Fprintf(logw, "[live-channel] "+format+"\n", args...)
	}

	started := false
	startForwarder := func() {
		if started || addr == "" || token == "" {
			return
		}
		started = true
		go forwardComments(addr, token, send, logf)
	}

	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var msg struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		if json.Unmarshal([]byte(line), &msg) != nil {
			continue
		}

		switch msg.Method {
		case "initialize":
			var p struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			if p.ProtocolVersion == "" {
				p.ProtocolVersion = "2024-11-05"
			}
			send(map[string]any{
				"jsonrpc": "2.0",
				"id":      msg.ID,
				"result": map[string]any{
					"protocolVersion": p.ProtocolVersion,
					"capabilities": map[string]any{
						"experimental": map[string]any{
							"claude/channel": map[string]any{},
						},
						"tools": map[string]any{},
					},
					"serverInfo": map[string]any{
						"name":    "liveshortly-channel",
						"version": version,
					},
					"instructions": "Messages arrive as channel events from live viewers watching this session. " +
						"Acknowledge them and respond inline in your current work.",
				},
			})

		case "notifications/initialized":
			// Claude is ready — start pushing viewer comments.
			startForwarder()

		case "tools/list":
			send(map[string]any{
				"jsonrpc": "2.0", "id": msg.ID,
				"result": map[string]any{"tools": []any{}},
			})

		case "ping":
			send(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{}})

		default:
			if len(msg.ID) > 0 { // request → must answer; notifications are ignored
				send(map[string]any{
					"jsonrpc": "2.0", "id": msg.ID,
					"error": map[string]any{"code": -32601, "message": "method not found: " + msg.Method},
				})
			}
		}
	}
	// stdin closed → Claude exited; we're done.
}

// forwardComments consumes the sidecar's SSE comment feed and emits one
// channel notification per comment, reconnecting until stdin closes the
// process.
func forwardComments(addr, token string, send func(any), logf func(string, ...any)) {
	nextID := 1
	backoff := time.Second
	for {
		err := consumeSSE(addr, token, func(data string) {
			var c struct {
				Username string `json:"username"`
				Message  string `json:"message"`
			}
			if json.Unmarshal([]byte(data), &c) != nil {
				return
			}
			who := c.Username
			if who == "" {
				who = "viewer"
			}
			logf("viewer comment from %s: %s", who, c.Message)
			send(map[string]any{
				"jsonrpc": "2.0",
				"method":  "notifications/claude/channel",
				"params": map[string]any{
					"content": who + ": " + c.Message,
					"meta": map[string]any{
						"chat_id": fmt.Sprintf("%d", nextID),
						"source":  "viewer-comment",
					},
				},
			})
			nextID++
		})
		logf("sidecar feed dropped (%v), retrying in %s", err, backoff)
		time.Sleep(backoff)
		if backoff < 10*time.Second {
			backoff *= 2
		}
	}
}

func consumeSSE(addr, token string, onData func(string)) error {
	req, err := http.NewRequest("GET", addr+"/comments/subscribe", nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Live-Token", token)
	resp, err := (&http.Client{}).Do(req) // no timeout: long-lived stream
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "data:") {
			onData(strings.TrimSpace(line[5:]))
		}
	}
	return sc.Err()
}
