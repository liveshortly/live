package adapter

import (
	"encoding/json"
	"os"
	"path/filepath"
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

	// 3. Check mcp_config.json
	mcpBytes, err := os.ReadFile(filepath.Join(tmp, "mcp_config.json"))
	if err != nil {
		t.Fatalf("reading mcp_config.json: %v", err)
	}
	var mcpObj struct {
		McpServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(mcpBytes, &mcpObj); err != nil {
		t.Fatalf("unmarshaling mcp_config.json: %v", err)
	}
	ch, ok := mcpObj.McpServers["liveshortly-channel"]
	if !ok {
		t.Fatalf("missing liveshortly-channel in mcp_config.json")
	}
	if ch.Command != liveBin {
		t.Errorf("mcp command = %q, want %q", ch.Command, liveBin)
	}
}
