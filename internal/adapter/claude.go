// Package adapter launches coding agents under Live capture. The claude
// adapter injects lifecycle hooks via a temporary --settings file; every hook
// is the `live hook <event>` relay, which forwards the payload to the
// in-process sidecar over localhost.
package adapter

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

// claudeHook builds one hook entry invoking the live binary relay.
func claudeHook(liveBin, event string, timeoutSec int, matcher string) map[string]any {
	entry := map[string]any{
		"hooks": []any{map[string]any{
			"type":    "command",
			"command": fmt.Sprintf("%q hook %s", liveBin, event),
			"timeout": timeoutSec,
		}},
	}
	if matcher != "" {
		entry["matcher"] = matcher
	}
	return entry
}

// WriteClaudeSettings writes the temporary settings file passed to
// `claude --settings`. It wires all capture hooks and disables the legacy
// liveshortly marketplace plugin so a session is never captured twice. When
// webPerms is set, the PreToolUse hook is given a long timeout so it can block
// while the owner answers the permission prompt from the web.
func WriteClaudeSettings(liveBin string, webPerms bool) (path string, cleanup func(), err error) {
	// PreToolUse normally returns instantly; in web-permissions mode it blocks
	// on a human verdict (awaitDecision grace is 8m), so give it 10m headroom.
	preToolTimeout, preToolEvent := 8, "pre-tool-use"
	if webPerms {
		// --gated: the hook is the only approval gate, so `live hook` denies
		// (fails closed) when the sidecar can't answer.
		preToolTimeout, preToolEvent = 600, "pre-tool-use --gated"
	}
	settings := map[string]any{
		"enabledPlugins": map[string]any{
			"liveshortly@liveshortly": false,
		},
		// Pre-approve the channel MCP server so Claude does not halt at the
		// interactive "enable these MCP servers?" trust prompt — that dialog
		// blocks the whole session (viewer input never reaches Claude).
		// Paired with --strict-mcp-config so only our server loads and no
		// project .mcp.json server is auto-enabled.
		"enabledMcpjsonServers": []string{"liveshortly-channel"},
		"hooks": map[string]any{
			"SessionStart":     []any{claudeHook(liveBin, "session-start", 15, "")},
			"UserPromptSubmit": []any{claudeHook(liveBin, "user-prompt-submit", 10, "")},
			"PreToolUse": []any{
				claudeHook(liveBin, preToolEvent, preToolTimeout, "Edit|Write|MultiEdit|Bash|WebFetch"),
				// Mirror the agent's interactive AskUserQuestion prompt to the web
				// feed (read-only). Separate matcher: it only surfaces the question,
				// never gates the tool — the answer is still made in the terminal.
				claudeHook(liveBin, "ask-question", 10, "AskUserQuestion"),
			},
			"PostToolUse":      []any{claudeHook(liveBin, "post-tool-use", 10, "Edit|Write|MultiEdit|Bash")},
			"Notification":     []any{claudeHook(liveBin, "notification", 10, "")},
			"Stop":             []any{claudeHook(liveBin, "stop", 15, "")},
		},
	}
	b, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return "", nil, err
	}
	f, err := os.CreateTemp("", "live-claude-settings-*.json")
	if err != nil {
		return "", nil, err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", nil, err
	}
	f.Close()
	return f.Name(), func() { os.Remove(f.Name()) }, nil
}

// WriteClaudeMCPConfig writes the temporary --mcp-config file that loads the
// `live channel` MCP server for this session only — it delivers viewer
// comments into the conversation even while Claude is idle, and vanishes
// with the session (nothing is registered globally).
func WriteClaudeMCPConfig(liveBin string) (path string, cleanup func(), err error) {
	cfg := map[string]any{
		"mcpServers": map[string]any{
			"liveshortly-channel": map[string]any{
				"command": liveBin,
				"args":    []string{"channel"},
			},
		},
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", nil, err
	}
	f, err := os.CreateTemp("", "live-claude-mcp-*.json")
	if err != nil {
		return "", nil, err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", nil, err
	}
	f.Close()
	return f.Name(), func() { os.Remove(f.Name()) }, nil
}

// RunWithHooks launches the agent with inherited stdio (it owns the real
// terminal) and the sidecar address in its environment for the hook relay.
// Blocks until the agent exits and returns its exit code.
func RunWithHooks(bin string, args []string, extraEnv []string) (int, error) {
	cmd := exec.Command(bin, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), extraEnv...)

	// Ctrl-C / Ctrl-\ belong to the agent (claude uses them for turn
	// control); the shim must survive them to stop the session cleanly.
	signal.Ignore(os.Interrupt, syscall.SIGQUIT)
	defer signal.Reset(os.Interrupt, syscall.SIGQUIT)

	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode(), nil
		}
		return 1, err
	}
	return 0, nil
}
