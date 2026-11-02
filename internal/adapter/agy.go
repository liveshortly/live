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
	"strings"
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
	preToolTimeout, preToolEvent := 10, "pre-tool-use"
	if webPerms {
		// --gated: `live hook` denies (fails closed) when the sidecar can't answer.
		preToolTimeout, preToolEvent = 600, "pre-tool-use --gated"
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
			"PreToolUse":    []any{agyHook(liveBin, preToolEvent, preToolTimeout, "*")},
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

// EnsureAgyPlugin creates/updates the LiveShortly plugin for agy and installs
// and enables it with the same binary the user launched (`agy` or
// `antigravity`), so the hooks land in the agy that is about to run.
//
// Install or enable failing is an error, never ignored: live launches agy
// with its own prompts skipped and relies on these hooks for capture and
// approvals, so a silent failure would run every tool ungated.
func EnsureAgyPlugin(agyBin, liveBin string, webPerms bool) (cleanup func(), err error) {
	if agyBin == "" {
		agyBin = "agy"
	}
	if p, err := exec.LookPath(agyBin); err == nil {
		agyBin = p
	} else {
		return nil, fmt.Errorf("can't find %s on PATH: %w", agyBin, err)
	}
	dir, err := os.MkdirTemp("", "live-agy-plugin-*")
	if err != nil {
		return nil, err
	}
	cleanup = func() {
		_ = exec.Command(agyBin, "plugin", "uninstall", "liveshortly").Run()
		_ = os.RemoveAll(dir)
	}

	if err := WriteAgyPluginFiles(dir, liveBin, webPerms); err != nil {
		cleanup()
		return nil, err
	}
	if out, err := agyCommand(agyBin, "plugin", "install", dir).CombinedOutput(); err != nil {
		cleanup()
		return nil, fmt.Errorf("couldn't install LiveShortly's agy plugin (%s plugin install): %v: %s", filepath.Base(agyBin), err, strings.TrimSpace(string(out)))
	}
	if out, err := agyCommand(agyBin, "plugin", "enable", "liveshortly").CombinedOutput(); err != nil {
		cleanup()
		return nil, fmt.Errorf("couldn't enable LiveShortly's agy plugin (%s plugin enable): %v: %s", filepath.Base(agyBin), err, strings.TrimSpace(string(out)))
	}
	return cleanup, nil
}

// agyCommand is a seam for tests.
var agyCommand = exec.Command
