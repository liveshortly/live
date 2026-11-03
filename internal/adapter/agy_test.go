package adapter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteAgyPluginFiles(t *testing.T) {
	tmp, err := os.MkdirTemp("", "agy-adapter-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmp)

	liveBin := "/usr/local/bin/live"
	if err := WriteAgyPluginFiles(tmp, liveBin, true); err != nil {
		t.Fatalf("WriteAgyPluginFiles failed: %v", err)
	}

	// 1. Check plugin.json
	pluginBytes, err := os.ReadFile(filepath.Join(tmp, "plugin.json"))
	if err != nil {
		t.Fatalf("reading plugin.json: %v", err)
	}
	var pluginObj map[string]any
	if err := json.Unmarshal(pluginBytes, &pluginObj); err != nil {
		t.Fatalf("unmarshaling plugin.json: %v", err)
	}
	if pluginObj["name"] != "liveshortly" {
		t.Errorf("plugin name = %v, want liveshortly", pluginObj["name"])
	}

	// 2. Check hooks.json
	hooksBytes, err := os.ReadFile(filepath.Join(tmp, "hooks.json"))
	if err != nil {
		t.Fatalf("reading hooks.json: %v", err)
	}
	if !strings.Contains(string(hooksBytes), `hook pre-tool-use --gated`) {
		t.Error("web-perms PreToolUse hook must pass --gated so it fails closed")
	}
	var hooksObj struct {
		LiveShortly struct {
			PreInvocation []any `json:"PreInvocation"`
			PreToolUse    []any `json:"PreToolUse"`
			PostToolUse   []any `json:"PostToolUse"`
			Stop          []any `json:"Stop"`
		} `json:"liveshortly"`
	}
	if err := json.Unmarshal(hooksBytes, &hooksObj); err != nil {
		t.Fatalf("unmarshaling hooks.json: %v", err)
	}
	if len(hooksObj.LiveShortly.PreInvocation) != 1 {
		t.Errorf("PreInvocation len = %d, want 1", len(hooksObj.LiveShortly.PreInvocation))
	}
	if len(hooksObj.LiveShortly.PreToolUse) != 1 {
		t.Errorf("PreToolUse len = %d, want 1", len(hooksObj.LiveShortly.PreToolUse))
	}
	if len(hooksObj.LiveShortly.PostToolUse) != 1 {
		t.Errorf("PostToolUse len = %d, want 1", len(hooksObj.LiveShortly.PostToolUse))
	}
	if len(hooksObj.LiveShortly.Stop) != 1 {
		t.Errorf("Stop len = %d, want 1", len(hooksObj.LiveShortly.Stop))
	}
}

// fakeAgy writes an executable named name into a temp dir that exits with
// code for `plugin <verb>` when verb == failVerb, else 0.
func fakeAgy(t *testing.T, name, failVerb string) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n[ \"$2\" = \"" + failVerb + "\" ] && { echo boom >&2; exit 3; }\nexit 0\n"
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// LSH-03: a failed install/enable must abort, never fall through to an
// ungated agy run.
func TestEnsureAgyPluginFailsWhenInstallOrEnableFails(t *testing.T) {
	for _, verb := range []string{"install", "enable"} {
		bin := fakeAgy(t, "agy", verb)
		if cleanup, err := EnsureAgyPlugin(bin, "/usr/local/bin/live", true); err == nil {
			cleanup()
			t.Fatalf("plugin %s failing: EnsureAgyPlugin returned nil error", verb)
		}
	}
}

// The plugin goes into the binary the user launched, e.g. `antigravity`.
func TestEnsureAgyPluginUsesLaunchedBinary(t *testing.T) {
	bin := fakeAgy(t, "antigravity", "none")
	cleanup, err := EnsureAgyPlugin(bin, "/usr/local/bin/live", true)
	if err != nil {
		t.Fatalf("EnsureAgyPlugin(antigravity) = %v", err)
	}
	cleanup()
	if _, err := EnsureAgyPlugin(filepath.Join(t.TempDir(), "missing-agy"), "/x/live", true); err == nil {
		t.Fatal("missing binary: want error")
	}
}
