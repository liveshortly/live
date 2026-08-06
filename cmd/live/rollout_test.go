package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// captured is a tiny emit sink for asserting what the mapper produced.
type captured struct {
	events []capEvent
}

type capEvent struct {
	typ     string
	payload map[string]any
}

func (c *captured) emit(t string, p map[string]any) { c.events = append(c.events, capEvent{t, p}) }

func (c *captured) ofType(t string) []capEvent {
	var out []capEvent
	for _, e := range c.events {
		if e.typ == t {
			out = append(out, e)
		}
	}
	return out
}

func newMapper(c *captured) *rolloutMapper {
	return &rolloutMapper{emit: c.emit, pending: map[string]codexCall{}}
}

// Lines below mirror the real codex 0.142.5 rollout schema (verified against
// ~/.codex/sessions/**/rollout-*.jsonl): a {timestamp,type,payload} envelope
// whose semantic type is payload.type.
func TestRolloutMapper_MessagesAndTools(t *testing.T) {
	c := &captured{}
	m := newMapper(c)

	lines := []string{
		// codex preamble user messages — must be skipped
		`{"type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"<permissions instructions>\nno"}]}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>\n  <cwd>/x</cwd>"}]}}`,
		// real user prompt → prompt
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Create greeting.txt"}]}}`,
		// assistant reply → response
		`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"On it."}]}}`,
		// function_call (exec_command) then its output → one tool_call
		`{"type":"response_item","payload":{"type":"function_call","name":"exec_command","call_id":"c1","arguments":"{\"cmd\":\"wc -c greeting.txt\",\"workdir\":\"/x\"}"}}`,
		`{"type":"response_item","payload":{"type":"function_call_output","call_id":"c1","output":"Chunk ID: 6925bc\nWall time: 0.0s\nProcess exited with code 0\nOutput:\n      11 greeting.txt\n"}}`,
		// end of turn
		`{"type":"event_msg","payload":{"type":"task_complete"}}`,
	}
	for _, l := range lines {
		m.handle([]byte(l))
	}

	if got := c.ofType("prompt"); len(got) != 1 || got[0].payload["content"] != "Create greeting.txt" {
		t.Fatalf("prompt events = %+v, want exactly the real user prompt", c.ofType("prompt"))
	}
	if got := c.ofType("response"); len(got) != 1 || got[0].payload["content"] != "On it." {
		t.Fatalf("response events = %+v, want the assistant reply", got)
	}
	tc := c.ofType("tool_call")
	if len(tc) != 1 {
		t.Fatalf("tool_call events = %d, want 1 (paired call+output)", len(tc))
	}
	if tc[0].payload["tool"] != "Shell" {
		t.Fatalf("tool label = %v, want Shell", tc[0].payload["tool"])
	}
	if tc[0].payload["command"] != "wc -c greeting.txt" {
		t.Fatalf("command = %v", tc[0].payload["command"])
	}
	// the "Chunk ID …/Output:\n" wrapper must be stripped
	if tc[0].payload["output"] != "11 greeting.txt" {
		t.Fatalf("output = %q, want unwrapped shell output", tc[0].payload["output"])
	}
	if len(c.ofType("stream_end")) != 1 {
		t.Fatalf("want one stream_end on task_complete")
	}
	// no unpaired output events (the function_call_output was consumed by the call)
	if got := c.ofType("output"); len(got) != 0 {
		t.Fatalf("unexpected output events: %+v", got)
	}
}

func TestRolloutMapper_ApplyPatch(t *testing.T) {
	c := &captured{}
	m := newMapper(c)
	patch := "*** Begin Patch\n*** Add File: hello.txt\n+Hello Codex\n*** End Patch"
	args, _ := json.Marshal(map[string]any{"input": patch}) // arguments is a JSON *string*
	line, _ := json.Marshal(map[string]any{
		"type": "response_item",
		"payload": map[string]any{
			"type": "function_call", "name": "apply_patch", "call_id": "p1",
			"arguments": string(args),
		},
	})
	m.handle(line)

	fw := c.ofType("file_write")
	if len(fw) != 1 {
		t.Fatalf("file_write events = %d, want 1; all=%+v", len(fw), c.events)
	}
	if fw[0].payload["file"] != "hello.txt" {
		t.Fatalf("file = %v, want hello.txt", fw[0].payload["file"])
	}
	if fw[0].payload["added"] != 1 {
		t.Fatalf("added = %v, want 1", fw[0].payload["added"])
	}
}

func TestCodexChannelArgs_Ephemeral(t *testing.T) {
	args := codexChannelArgs("/usr/local/bin/live", "http://127.0.0.1:5555", "tok123", "sess-1")
	joined := ""
	for i := 0; i < len(args); i += 2 {
		if args[i] != "-c" {
			t.Fatalf("arg %d = %q, want -c (all overrides must be launch-scoped)", i, args[i])
		}
		joined += args[i+1] + "\n"
	}
	// per-session, ephemeral: everything is under mcp_servers.* launch overrides;
	// nothing here should ever be a persistent `codex mcp add`.
	for _, want := range []string{
		`mcp_servers.liveshortly_channel.command="/usr/local/bin/live"`,
		`mcp_servers.liveshortly_channel.args=["channel"]`,
		`mcp_servers.liveshortly_channel.env.LIVE_SIDECAR="http://127.0.0.1:5555"`,
		`mcp_servers.liveshortly_channel.env.LIVE_TOKEN="tok123"`,
		`mcp_servers.liveshortly_channel.env.LIVE_SESSION_ID="sess-1"`,
		`mcp_servers.liveshortly_channel.env.LIVE_CHANNEL_TOOL="1"`,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("channel args missing %q; got:\n%s", want, joined)
		}
	}
}

func TestCodexPermArgs(t *testing.T) {
	args := codexPermArgs("/usr/local/bin/live")
	// must register an ephemeral PreToolUse hook + bypass the one-time trust prompt
	if len(args) != 3 || args[0] != "-c" || args[2] != "--dangerously-bypass-hook-trust" {
		t.Fatalf("perm args shape = %v", args)
	}
	toml := args[1]
	for _, want := range []string{
		"hooks.PreToolUse=",
		`matcher="Edit|Write|MultiEdit|Bash|WebFetch"`, // codex normalizes to Claude tool names
		`hook pre-tool-use`,                            // reuses the existing sidecar handler
	} {
		if !strings.Contains(toml, want) {
			t.Fatalf("perm TOML missing %q; got: %s", want, toml)
		}
	}
}

func TestCodexPermsWanted(t *testing.T) {
	if !codexPermsWanted(nil) {
		t.Fatal("web-perms must be ON by default (like live claude)")
	}
	if codexPermsWanted([]string{"-p", "x"}) {
		t.Fatal("headless -p runs must never gate")
	}
	t.Setenv("LIVE_CODEX_PERMS", "0")
	if codexPermsWanted(nil) {
		t.Fatal("LIVE_CODEX_PERMS=0 must disable web-perms")
	}
}

func TestCodexChannelWanted(t *testing.T) {
	if !codexChannelWanted(nil) {
		t.Fatal("channel should be on by default")
	}
	if codexChannelWanted([]string{"-p", "do it"}) {
		t.Fatal("channel must be off for headless -p runs")
	}
	t.Setenv("LIVE_CHANNEL", "0")
	if codexChannelWanted(nil) {
		t.Fatal("LIVE_CHANNEL=0 must disable the channel")
	}
}

func TestAppendSeedPrompt(t *testing.T) {
	// agents that accept an initial prompt get it appended
	got := appendSeedPrompt([]string{"--model", "gpt"}, "SEED", true)
	if len(got) != 3 || got[2] != "SEED" {
		t.Fatalf("acceptsInitialPrompt: got %v, want trailing SEED", got)
	}
	// pty agents that don't: userArgs unchanged
	got = appendSeedPrompt([]string{"--flag"}, "SEED", false)
	if len(got) != 1 || got[0] != "--flag" {
		t.Fatalf("non-accepting agent: userArgs should be untouched, got %v", got)
	}
}

func TestProfileForCodex(t *testing.T) {
	p := profileFor("codex")
	if p.framework != "codex" || p.captureMode != "rollout" || !p.acceptsInitialPrompt {
		t.Fatalf("codex profile = %+v, want {codex rollout true}", p)
	}
	// codex must inject the handoff seed (not the paste banner)
	if got := appendSeedPrompt(nil, "SEED", p.acceptsInitialPrompt); len(got) != 1 || got[0] != "SEED" {
		t.Fatalf("codex seed not injected: %v", got)
	}
}

// live ollama <args> must be captured as ollama, not as a generic terminal
// session — that labelling is the whole point of the profile entry.
func TestProfileForOllama(t *testing.T) {
	p := profileFor("ollama")
	if p.framework != "ollama" || p.captureMode != "pty" {
		t.Errorf("profileFor(ollama) = %+v, want framework=ollama captureMode=pty", p)
	}
	// ollama's positional prompt is only valid for `run`, so a handoff digest
	// must never be appended to an arbitrary `live ollama <subcommand>`.
	if p.acceptsInitialPrompt {
		t.Error("ollama must not accept an injected initial prompt")
	}
	// A full path still resolves by base name, the way the other agents do.
	if got := profileFor("/opt/homebrew/bin/ollama"); got.framework != "ollama" {
		t.Errorf("profileFor(abs path) = %q, want ollama", got.framework)
	}
}

// The model name reaches the daemon as an argv element. It is never passed to a
// shell, but the alphabet stays closed so a name can never be anything but a
// name.
func TestValidModelName(t *testing.T) {
	for _, ok := range []string{"llama3.2:1b", "qwen2.5:0.5b", "library/mistral:latest", "gemma2"} {
		if !validModelName(ok) {
			t.Errorf("validModelName(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "llama3 && rm -rf /", "llama3;reboot", "a b", "$(id)", "`id`", "model\nname", strings.Repeat("x", 129)} {
		if validModelName(bad) {
			t.Errorf("validModelName(%q) = true, want false", bad)
		}
	}
}
