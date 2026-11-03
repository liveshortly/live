package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/liveshortly/live/internal/hostcfg"
)

// Agent discovery.
//
// Which agents a machine can offer is a property of that machine's PATH, not of
// this file: `live <anything>` already captures an unknown CLI through the
// generic pty path (see profileFor's default), so any harness the operator has
// installed is runnable. Two inputs produce the set:
//
//   - the builtin names below, probed with exec.LookPath, and
//   - ~/.liveshortly/agents.json (or --agents-file), for anything else —
//     including a harness installed under a path or a name live has never
//     heard of.
//
// SECURITY — this widens the NAMES a machine may advertise. It does not widen
// what the server can ask for. The server still sends an agent name and never
// an argv; the daemon resolves that name against the map it built HERE, at
// startup, from this machine's PATH and this operator's file, and refuses a
// name that is not in it (see spawnSession). That is the same shape as the
// --dir allowlist: the operator declares the set, the server only chooses from
// it.

// nativeAgents are the agents live has first-class support for: a capture
// profile beyond plain pty (hooks, rollout), agent-specific launch flags, or
// both. The names are reserved — an agents.json entry may not shadow one,
// because the special handling keyed on the name would then be wrong.
var nativeAgents = []string{"claude", "codex", "gemini", "agy", "antigravity", "ollama"}

// pocketBuiltins are the agents `live daemon --server` can drive ONE-SHOT, in
// the Pocket flow: each needs a known non-interactive flag set (--print and
// friends — see runPocketAgent). A harness missing from this list can still be
// offered by Pocket through an agents.json adapter, which supplies the flags.
//
// This is deliberately narrower than spawnableBuiltins: driving a CLI headless
// takes per-tool knowledge, whereas running one in a terminal does not.
var pocketBuiltins = []string{"codex", "claude", "gemini", "agy", "antigravity", "ollama", "aider", "cursor-agent"}

// spawnableBuiltins are the agents `live daemon` probes for interactive
// sessions. It is a superset of pocketBuiltins: an interactive run only needs
// a terminal, which pty capture gives every CLI, so a name costs one LookPath
// and nothing else. Order here is the order the web picker shows.
//
// A harness that is not listed is NOT excluded — it is simply not guessed at.
// Name it in agents.json and the daemon offers it too.
var spawnableBuiltins = []string{
	// native capture
	"claude", "codex", "gemini", "agy", "antigravity", "ollama",
	// known harnesses, generic pty capture
	"aider", "cursor-agent", "copilot", "opencode", "crush", "goose",
	"amp", "qwen", "pi", "hermes",
}

// agentCommand is one entry of the operator's agents.json: a name to advertise
// and the local executable to run for it. Args is a fixed argv prefix. In the
// Pocket (one-shot) flow an arg may contain {prompt}, which is substituted with
// the turn's prompt; an interactive spawn has no prompt, so those args are
// dropped there.
//
// Adapters are configured by the machine's operator, never supplied by clients.
type agentCommand struct {
	Name    string   `json:"name"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
	// TypeComments opts this agent into having viewer comments typed into it
	// as a turn (sanitised, one line, like ollama). Off by default: in a TUI
	// that is showing a yes/no dialog, the Enter that submits the line can
	// accept the dialog's default, so only the operator can say it is safe.
	TypeComments bool `json:"type_comments"`
	// ACP runs the agent over the Agent Client Protocol instead of a terminal
	// (LSH-13): Command+Args must be its ACP entry point, e.g. `goose acp`.
	ACP bool `json:"acp"`
	// Models the web may start this agent with (LSH-16), passed as
	// `<model_flag> <id>`. Both come from the operator, never from a client.
	Models    []string `json:"models"`
	ModelFlag string   `json:"model_flag"`
}

var validAgentName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

// maxCustomAgents bounds how many adapters one machine may declare, so a
// runaway file cannot bloat every registration payload.
const maxCustomAgents = 64

// loadAgentCommands reads the adapter file. An explicit path must exist; the
// default path (~/.liveshortly/agents.json) is optional, since most machines
// never need one. A file that exists but does not parse is always an error —
// silently dropping an operator's agents would be worse than refusing to start.
func loadAgentCommands(path string) (map[string]agentCommand, error) {
	result := map[string]agentCommand{}
	optional := path == ""
	if optional {
		path = hostcfg.AgentsPath()
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if optional && errors.Is(err, os.ErrNotExist) {
			return result, nil
		}
		return nil, err
	}
	var commands []agentCommand
	if err = json.Unmarshal(b, &commands); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(commands) > maxCustomAgents {
		return nil, fmt.Errorf("%s: too many adapters (max %d)", path, maxCustomAgents)
	}
	for _, c := range commands {
		c.Name = strings.TrimSpace(c.Name)
		c.Command = strings.TrimSpace(c.Command)
		if c.Command == "" {
			c.Command = c.Name // the common case: the name IS the executable
		}
		if !validAgentName.MatchString(c.Name) {
			return nil, fmt.Errorf("adapter %q needs a name of letters, digits, dash or underscore", c.Name)
		}
		if contains(nativeAgents, c.Name) {
			return nil, fmt.Errorf("adapter %s conflicts with a built-in agent", c.Name)
		}
		if len(c.Models) > 0 && !flagPattern.MatchString(c.ModelFlag) {
			return nil, fmt.Errorf("adapter %s lists models but no model_flag such as \"--model\"", c.Name)
		}
		for _, m := range c.Models {
			if !safeModelID(m) {
				return nil, fmt.Errorf("adapter %s: model %q is not a valid model name", c.Name, m)
			}
		}
		if _, exists := result[c.Name]; exists {
			return nil, fmt.Errorf("duplicate adapter %s", c.Name)
		}
		result[c.Name] = c
	}
	return result, nil
}

// discoverAgents reports the agents this machine can actually run, in picker
// order: probed builtins first, then the operator's adapters. An adapter whose
// name matches a non-native builtin replaces the probe — an explicit path the
// operator wrote beats one live guessed.
func discoverAgents(builtins []string, custom map[string]agentCommand) []string {
	result := []string{}
	for _, name := range builtins {
		if _, shadowed := custom[name]; shadowed {
			continue // listed below, with the operator's own command
		}
		if _, err := exec.LookPath(name); err == nil {
			result = append(result, name)
		}
	}
	var extra []string
	for name, c := range custom {
		if _, err := exec.LookPath(c.Command); err == nil {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)
	return append(result, extra...)
}

// agentPATH is the PATH agents are probed and spawned with (LSH-18). A daemon
// started by the Mac app or launchd inherits a minimal PATH, so harnesses
// installed by npm, bun, pnpm, volta, mise and friends were invisible to it.
// It joins the inherited PATH, the user's login-shell PATH and the usual
// global-bin dirs, keeping the first copy of each. A shell that hangs or fails
// just contributes nothing.
func agentPATH(shell, home, inherited string) string {
	parts := filepath.SplitList(inherited)
	parts = append(parts, filepath.SplitList(loginShellPATH(shell))...)
	pnpm := os.Getenv("PNPM_HOME")
	if pnpm == "" {
		pnpm = filepath.Join(home, "Library", "pnpm")
	}
	// ponytail: no `npm prefix -g` — it costs a node start; Homebrew's prefix is
	// already on the login PATH and ~/.npm-global covers the usual custom one.
	for _, d := range []string{".local/bin", ".bun/bin", ".npm-global/bin", ".volta/bin",
		".local/share/mise/shims", ".asdf/shims", "go/bin", ".cargo/bin", ".local/share/pnpm"} {
		parts = append(parts, filepath.Join(home, d))
	}
	parts = append(parts, pnpm)
	seen := map[string]bool{}
	out := []string{}
	for _, p := range parts {
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return strings.Join(out, string(os.PathListSeparator))
}

// loginShellPATH asks the user's shell for the PATH their terminal sees. The
// marker skips whatever an interactive rc file prints first.
func loginShellPATH(shell string) string {
	if shell == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell, "-lic", `printf '\n__LIVE_PATH__%s' "$PATH"`)
	cmd.WaitDelay = time.Second // a backgrounded rc child must not hold us past the timeout
	out, _ := cmd.Output()
	if i := bytes.LastIndex(out, []byte("__LIVE_PATH__")); i >= 0 {
		return strings.TrimSpace(string(out[i+len("__LIVE_PATH__"):]))
	}
	return ""
}

// widenPATH sets this process's PATH to agentPATH, so exec.LookPath and every
// spawned agent (which inherits the environment) see the same directories.
func widenPATH() {
	home, _ := os.UserHomeDir()
	os.Setenv("PATH", agentPATH(os.Getenv("SHELL"), home, os.Getenv("PATH")))
}

// cmdAgents prints what discovery sees on this machine: each builtin found
// (with its path) or not, and each agents.json entry.
func cmdAgents(args []string) int {
	file := ""
	if len(args) == 2 && args[0] == "--agents-file" {
		file = args[1]
	} else if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "usage: live agents [--agents-file <path>]")
		return 2
	}
	widenPATH()
	custom, err := loadAgentCommands(file)
	if err != nil {
		fmt.Fprintln(os.Stderr, "live:", err)
		return 2
	}
	for _, name := range spawnableBuiltins {
		if _, shadowed := custom[name]; shadowed {
			continue
		}
		if p, err := exec.LookPath(name); err == nil {
			fmt.Printf("  found      %-14s %s\n", name, p)
		} else {
			fmt.Printf("  not found  %s\n", name)
		}
	}
	names := make([]string, 0, len(custom))
	for n := range custom {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if p, err := exec.LookPath(custom[n].Command); err == nil {
			fmt.Printf("  agents.json %-13s %s\n", n, p)
		} else {
			fmt.Printf("  agents.json %-13s %s (not executable)\n", n, custom[n].Command)
		}
	}
	fmt.Printf("\nAny other CLI (e.g. pig): add it to %s as\n", hostcfg.AgentsPath())
	fmt.Println(`  [{"name":"pig","command":"/full/path/to/pig"}]` + "\nthen restart `live daemon`.")
	return 0
}
