package main

// Golden replay tests for the harness capture paths (LSH-00).
//
// Each fixture under testdata/<agent>/<version>/ is a recorded (or, where no
// free real run was possible, synthetic — see testdata/README.md) capture of
// what a harness emits. These tests push it through the same mapper `live`
// uses at runtime and compare the resulting feed events with
// events.golden.json next to the capture. Timestamps are dropped.
//
// A diff here means the feed a viewer sees changed. If that was intended,
// regenerate and review the golden diff:
//
//	go test ./... -run Replay -update

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liveshortly/live/internal/adapter"
)

var update = flag.Bool("update", false, "rewrite events.golden.json from the current mapper output")

const testdataRoot = "../../testdata"

type goldenEvent struct {
	Type    string         `json:"type"`
	Payload map[string]any `json:"payload,omitempty"`
}

type recorder struct {
	mu     sync.Mutex
	events []goldenEvent
}

func (r *recorder) emit(typ string, p map[string]any) {
	clean := map[string]any{}
	for k, v := range p {
		if k != "ts" {
			clean[k] = v
		}
	}
	r.mu.Lock()
	r.events = append(r.events, goldenEvent{typ, clean})
	r.mu.Unlock()
}

// fixtureDirs returns every testdata/<agent>/<version> dir holding file.
func fixtureDirs(t *testing.T, agentGlob, file string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(testdataRoot, agentGlob, "*", file))
	if err != nil || len(m) == 0 {
		t.Fatalf("no fixtures for %s/*/%s", agentGlob, file)
	}
	for i := range m {
		m[i] = filepath.Dir(m[i])
	}
	return m
}

func fixtureName(dir string) string {
	rel, _ := filepath.Rel(testdataRoot, dir)
	return filepath.ToSlash(rel)
}

func readLines(t *testing.T, path string) [][]byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out [][]byte
	for _, l := range bytes.Split(b, []byte("\n")) {
		if len(bytes.TrimSpace(l)) > 0 {
			out = append(out, l)
		}
	}
	return out
}

func checkGolden(t *testing.T, dir string, events []goldenEvent) {
	t.Helper()
	if events == nil {
		events = []goldenEvent{}
	}
	got, err := json.MarshalIndent(events, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join(dir, "events.golden.json")
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run: go test ./... -run Replay -update)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s: feed events differ from golden.\n--- got\n%s\n--- want\n%s", path, got, want)
	}
}

// codex: rollout jsonl tail (default tmux mode and LIVE_TMUX=0).
func TestReplayCodexRollout(t *testing.T) {
	for _, dir := range fixtureDirs(t, "codex", "rollout.jsonl") {
		t.Run(fixtureName(dir), func(t *testing.T) {
			r := &recorder{}
			m := &rolloutMapper{
				emit:        r.emit,
				reportModel: func(model string) { r.emit("_model", map[string]any{"model": model}) },
				pending:     map[string]codexCall{},
			}
			for _, l := range readLines(t, filepath.Join(dir, "rollout.jsonl")) {
				m.handle(l)
			}
			checkGolden(t, dir, r.events)
		})
	}
}

// codex: LIVE_CODEX_APPSERVER=1. The capture is codex's stdout (JSON-RPC
// responses, notifications and server→client requests) fed through the real
// readLoop demux. Approvals are answered "allow"; the permission asks and the
// replies sent back to codex are appended (sorted) after the feed events since
// they are answered on goroutines.
func TestReplayCodexAppServer(t *testing.T) {
	for _, dir := range fixtureDirs(t, "codex-appserver", "jsonrpc.jsonl") {
		t.Run(fixtureName(dir), func(t *testing.T) {
			r := &recorder{}
			var mu sync.Mutex
			var asks []goldenEvent
			var stdin bytes.Buffer
			app := &codexApp{
				emit: r.emit,
				logf: func(string, ...any) {},
				requestPerm: func(_ context.Context, tool, desc string) string {
					mu.Lock()
					asks = append(asks, goldenEvent{"_permission", map[string]any{"tool": tool, "detail": desc}})
					mu.Unlock()
					return "allow"
				},
				pending: map[int]chan rpcResp{},
				stdin:   bufio.NewWriter(&lockedWriter{mu: &mu, w: &stdin}),
			}
			capture, err := os.ReadFile(filepath.Join(dir, "jsonrpc.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			app.readLoop(bytes.NewReader(capture))

			wantReplies := 0
			for _, l := range readLines(t, filepath.Join(dir, "jsonrpc.jsonl")) {
				var msg struct {
					ID     json.RawMessage `json:"id"`
					Method string          `json:"method"`
				}
				if json.Unmarshal(l, &msg) == nil && msg.Method != "" && len(msg.ID) > 0 {
					wantReplies++
				}
			}
			var replies []string
			deadline := time.Now().Add(5 * time.Second)
			for {
				mu.Lock()
				replies = strings.Split(strings.TrimSpace(stdin.String()), "\n")
				if replies[0] == "" {
					replies = nil
				}
				mu.Unlock()
				if len(replies) >= wantReplies || time.Now().After(deadline) {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			sort.Strings(replies)
			sort.Slice(asks, func(i, j int) bool { return asks[i].Payload["tool"].(string) < asks[j].Payload["tool"].(string) })

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

type lockedWriter struct {
	mu *sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// PTY agents (gemini, ollama, aider, cursor-agent, hermes, …): raw terminal
// bytes with their recorded arrival times, batched and scrubbed into `output`
// events.
//
// The batching below MIRRORS runPTYMode's flusher (run.go) on the recorded
// clock instead of a wall-clock ticker: flush when output has been idle for
// idleGap or held for maxHold, or the buffer reaches hardCap; each flush is
// StripANSI'd and clipped to maxText. stream_start heartbeats are omitted.
// Keep these in sync when that policy changes (LSH-04/05).
func TestReplayPTY(t *testing.T) {
	const (
		idleGap = 600 * time.Millisecond
		maxHold = 45 * time.Second
		hardCap = 64 * 1024
		maxText = 8000
	)
	for _, dir := range fixtureDirs(t, "*", "pty.jsonl") {
		t.Run(fixtureName(dir), func(t *testing.T) {
			r := &recorder{}
			var buf []byte
			var first, last time.Duration
			flush := func() {
				chunk := buf
				buf = nil
				if len(chunk) == 0 {
					return
				}
				text := adapter.StripANSI(chunk)
				if text == "" {
					return
				}
				if len(text) > maxText {
					text = clipLine(text, maxText)
				}
				r.emit("output", map[string]any{"text": text})
			}
			for _, l := range readLines(t, filepath.Join(dir, "pty.jsonl")) {
				var c struct {
					TMs  int64  `json:"t_ms"`
					Data string `json:"data"`
				}
				if err := json.Unmarshal(l, &c); err != nil {
					t.Fatalf("bad chunk %s: %v", l, err)
				}
				at := time.Duration(c.TMs) * time.Millisecond
				if len(buf) > 0 && (at-last >= idleGap || at-first >= maxHold) {
					flush()
				}
				if len(buf) == 0 {
					first = at
				}
				last = at
				buf = append(buf, c.Data...)
				if len(buf) >= hardCap {
					flush()
				}
			}
			flush() // process exit
			checkGolden(t, dir, r.events)
		})
	}
}
