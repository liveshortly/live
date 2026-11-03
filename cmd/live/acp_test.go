package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestReplayACP pushes testdata/acp/<protocol>/jsonrpc.jsonl (agent → client
// lines) through the ACP client and pins the feed events, permission prompts
// and the replies live writes back.
func TestReplayACP(t *testing.T) {
	for _, dir := range fixtureDirs(t, "acp", "jsonrpc.jsonl") {
		t.Run(fixtureName(dir), func(t *testing.T) {
			r := &recorder{}
			var mu sync.Mutex
			var stdin bytes.Buffer
			var asks []goldenEvent
			a := newACPClient(r.emit, func(string, ...any) {}, func(_ context.Context, tool, desc string) string {
				mu.Lock()
				asks = append(asks, goldenEvent{"_permission", map[string]any{"tool": tool, "detail": desc}})
				mu.Unlock()
				return "allow"
			})
			a.rpc.stdin = bufio.NewWriter(&lockedWriter{mu: &mu, w: &stdin})
			capture, err := os.ReadFile(filepath.Join(dir, "jsonrpc.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			a.rpc.readLoop(bytes.NewReader(capture))
			a.flushReply() // what the end of session/prompt does

			var replies []string
			deadline := time.Now().Add(5 * time.Second)
			for {
				mu.Lock()
				replies = strings.Split(strings.TrimSpace(stdin.String()), "\n")
				mu.Unlock()
				if len(replies) >= 2 || time.Now().After(deadline) {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			sort.Strings(replies)
			events := append(r.events, asks...)
			for _, rep := range replies {
				var v map[string]any
				_ = json.Unmarshal([]byte(rep), &v)
				events = append(events, goldenEvent{"_reply", v})
			}
			checkGolden(t, dir, events)
		})
	}
}

// Fail closed: deny, a timeout's "deny", and an allow with no allow option all
// pick a reject option or cancel — never an allow.
func TestACPDecideFailsClosed(t *testing.T) {
	req := func(kinds ...string) json.RawMessage {
		var opts []string
		for _, k := range kinds {
			opts = append(opts, fmt.Sprintf(`{"optionId":%q,"kind":%q}`, k, k))
		}
		return json.RawMessage(`{"toolCall":{"title":"rm -rf x","kind":"execute"},"options":[` + strings.Join(opts, ",") + `]}`)
	}
	for _, tc := range []struct {
		verdict string
		kinds   []string
		want    string
	}{
		{"deny", []string{"allow_once", "reject_once"}, "reject_once"},
		{"deny", []string{"allow_once", "allow_always"}, "cancelled"},
		{"", []string{"allow_once", "reject_always"}, "reject_always"},
		{"allow", []string{"reject_once"}, "cancelled"},
		{"always", []string{"allow_once", "allow_always"}, "allow_always"},
		{"allow", []string{"allow_always", "allow_once"}, "allow_once"},
	} {
		a := newACPClient(func(string, map[string]any) {}, func(string, ...any) {}, func(context.Context, string, string) string { return tc.verdict })
		out := a.decide(req(tc.kinds...))
		got := out["outcome"].(string)
		if got == "selected" {
			got = out["optionId"].(string)
		}
		if got != tc.want {
			t.Errorf("verdict %q options %v: got %s, want %s", tc.verdict, tc.kinds, got, tc.want)
		}
	}
}

// End to end over real stdio: this test binary re-runs itself as a fake ACP
// agent (TestHelperACPAgent). A comment sent mid-turn is queued and delivered
// as the next session/prompt, and each turn ends with stream_end.
func TestACPEndToEnd(t *testing.T) {
	r := &recorder{}
	a := newACPClient(r.emit, func(string, ...any) {}, func(context.Context, string, string) string { return "allow" })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	t.Setenv("LIVE_FAKE_ACP_AGENT", "1")
	if err := a.rpc.start(ctx, os.Args[0], "-test.run=TestHelperACPAgent"); err != nil {
		t.Fatal(err)
	}
	if err := a.open(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	a.send("first", true)
	a.send("second", false) // arrives while "first" runs
	deadline := time.Now().Add(10 * time.Second)
	for {
		r.mu.Lock()
		var ends int
		var replies []string
		for _, e := range r.events {
			if e.Type == "stream_end" {
				ends++
			}
			if e.Type == "response" {
				replies = append(replies, e.Payload["content"].(string))
			}
		}
		r.mu.Unlock()
		if ends == 2 {
			if strings.Join(replies, "|") != "echo: first|echo: second" {
				t.Fatalf("replies = %q", replies)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out; events = %+v", r.events)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestHelperACPAgent is the fake agent for TestACPEndToEnd: it answers
// initialize and session/new, and echoes each prompt back as a message chunk
// after a short "thinking" pause.
func TestHelperACPAgent(t *testing.T) {
	if os.Getenv("LIVE_FAKE_ACP_AGENT") != "1" {
		t.Skip("helper process")
	}
	in := bufio.NewScanner(os.Stdin)
	out := json.NewEncoder(os.Stdout)
	for in.Scan() {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Prompt []struct {
					Text string `json:"text"`
				} `json:"prompt"`
			} `json:"params"`
		}
		if json.Unmarshal(in.Bytes(), &m) != nil {
			continue
		}
		switch m.Method {
		case "initialize":
			_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{"protocolVersion": 1}})
		case "session/new":
			_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{"sessionId": "s"}})
		case "session/prompt":
			time.Sleep(200 * time.Millisecond)
			_ = out.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{
				"sessionId": "s", "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "echo: " + m.Params.Prompt[0].Text}},
			}})
			_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{"stopReason": "end_turn"}})
		}
	}
	os.Exit(0)
}
