// Package adapter launches coding agents under Live capture. The agy
// adapter registers lifecycle hooks via an ephemeral/managed plugin
// installed into Antigravity (agy).
package adapter

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// agyHook builds one hook entry for AGY's hooks.json schema (grouped).
func agyHook(liveBin, event string, timeoutSec int, matcher string) map[string]any {
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

// agyFlatHook builds a flat hook entry for AGY (PreInvocation, Stop).
func agyFlatHook(liveBin, event string, timeoutSec int) map[string]any {
	return map[string]any{
		"type":    "command",
		"command": fmt.Sprintf("%q hook %s", liveBin, event),
		"timeout": timeoutSec,
	}
}

// WriteAgyPluginFiles writes plugin.json and hooks.json into targetDir.
func WriteAgyPluginFiles(targetDir, liveBin string, webPerms bool) error {
	preToolTimeout := 10
	if webPerms {
		preToolTimeout = 600
	}

	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return err
	}

	// 1. plugin.json
	manifest := map[string]any{
		"name": "liveshortly",
	}
	mb, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(targetDir, "plugin.json"), mb, 0o644); err != nil {
		return err
	}

	// 2. hooks.json
	hooks := map[string]any{
		"liveshortly": map[string]any{
			"PreInvocation": []any{agyFlatHook(liveBin, "pre-invocation", 10)},
			"PreToolUse":    []any{agyHook(liveBin, "pre-tool-use", preToolTimeout, "*")},
			"PostToolUse":   []any{agyHook(liveBin, "post-tool-use", 10, "*")},
			"Stop":          []any{agyFlatHook(liveBin, "stop", 15)},
		},
	}
	hb, err := json.MarshalIndent(hooks, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(targetDir, "hooks.json"), hb, 0o644); err != nil {
		return err
	}

	_ = os.Remove(filepath.Join(targetDir, "mcp_config.json"))
	return nil
}

// EnsureAgyPlugin creates/updates the LiveShortly plugin for agy and installs it
// using `agy plugin install <dir>`.
func EnsureAgyPlugin(liveBin string, webPerms bool) (cleanup func(), err error) {
	dir, err := os.MkdirTemp("", "live-agy-plugin-*")
	if err != nil {
		return nil, err
	}
	cleanup = func() { _ = os.RemoveAll(dir) }

	if err := WriteAgyPluginFiles(dir, liveBin, webPerms); err != nil {
		cleanup()
		return nil, err
	}

	// Install plugin into agy if agy binary is present
	if agyBin, err := exec.LookPath("agy"); err == nil {
		cmd := exec.Command(agyBin, "plugin", "install", dir)
		_ = cmd.Run()
	}

	return cleanup, nil
}
