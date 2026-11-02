package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var pocketPluginMu sync.Mutex

// A separate plugin leaves the website's LiveShortly plugin untouched. It is
// inert outside a pocket child process and uses a private, per-turn token.
func pocketHookBridge(ctx context.Context, agent string) ([]string, string, func(), error) {
	bin, err := os.Executable()
	if err != nil {
		return nil, "", nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, "", nil, err
	}
	tokenBytes := make([]byte, 32)
	if _, err = rand.Read(tokenBytes); err != nil {
		listener.Close()
		return nil, "", nil, err
	}
	token := hex.EncodeToString(tokenBytes)
	bridge := &http.Server{ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 9 * time.Minute}
	bridge.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/hook/pre-tool-use" || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Live-Token")), []byte(token)) != 1 {
			http.Error(w, "Unauthorized", 401)
			return
		}
		var payload struct {
			ToolName  string          `json:"tool_name"`
			ToolInput json.RawMessage `json:"tool_input"`
			ToolCall  struct {
				Name string          `json:"name"`
				Args json.RawMessage `json:"args"`
			} `json:"toolCall"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
		if json.NewDecoder(r.Body).Decode(&payload) != nil {
			http.Error(w, "Invalid hook", 400)
			return
		}
		tool, detail := payload.ToolName, payload.ToolInput
		if tool == "" {
			tool, detail = payload.ToolCall.Name, payload.ToolCall.Args
		}
		decision := "deny"
		if tool != "" {
			decision = pocketAsk(ctx, tool, prettyPocketJSON(detail))
		}
		pocketJSON(w, 200, pocketHookResult(agent, decision))
	})
	go bridge.Serve(listener)
	cleanup := func() { bridge.Close() }
	hookAgent := agent
	if hookAgent == "antigravity" {
		hookAgent = "agy"
	}
	command := "'" + strings.ReplaceAll(bin, "'", "'\"'\"'") + "' pocket-hook " + hookAgent
	hook := map[string]any{"matcher": "*", "hooks": []any{map[string]any{"type": "command", "command": command, "timeout": 600}}}
	var settings string
	if agent == "claude" {
		f, e := os.CreateTemp("", "live-pocket-claude-*.json")
		if e != nil {
			cleanup()
			return nil, "", nil, e
		}
		settings = f.Name()
		e = json.NewEncoder(f).Encode(map[string]any{"hooks": map[string]any{"PreToolUse": []any{hook}}})
		closeErr := f.Close()
		cleanup = func() { bridge.Close(); os.Remove(settings) }
		if e != nil {
			cleanup()
			return nil, "", nil, e
		}
		if closeErr != nil {
			cleanup()
			return nil, "", nil, closeErr
		}
	} else {
		if err := installPocketAgyPlugin(ctx, agent, hook); err != nil {
			cleanup()
			return nil, "", nil, err
		}
	}
	env := []string{"LIVE_POCKET_HOOK=" + hookAgent, "LIVE_POCKET_HOOK_URL=http://" + listener.Addr().String(), "LIVE_POCKET_HOOK_TOKEN=" + token}
	return env, settings, cleanup, nil
}

func installPocketAgyPlugin(ctx context.Context, agent string, hook any) error {
	pocketPluginMu.Lock()
	defer pocketPluginMu.Unlock()
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, ".liveshortly", "pocket-hook-plugin")
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	for name, value := range map[string]any{"plugin.json": map[string]any{"name": "liveshortly-pocket"}, "hooks.json": map[string]any{"liveshortly-pocket": map[string]any{"PreToolUse": []any{hook}}}} {
		b, e := json.Marshal(value)
		if e != nil {
			return e
		}
		path := filepath.Join(dir, name)
		if e = os.WriteFile(path+".tmp", b, 0600); e != nil {
			return e
		}
		if e = os.Rename(path+".tmp", path); e != nil {
			return e
		}
	}
	installCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(installCtx, agent, "plugin", "install", dir)
	cmd.Env = envWithout(os.Environ(), "LIVE_POCKET_KEY")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("Install pocket approval hook: %w: %s", err, string(out))
	}
	enable := exec.CommandContext(installCtx, agent, "plugin", "enable", "liveshortly-pocket")
	enable.Env = cmd.Env
	if out, err := enable.CombinedOutput(); err != nil {
		return fmt.Errorf("Enable pocket approval hook: %w: %s", err, string(out))
	}
	return nil
}

func pocketHookResult(agent, decision string) any {
	reason := "Decision from pocket.ai"
	if agent == "claude" {
		return map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": "PreToolUse", "permissionDecision": decision, "permissionDecisionReason": reason}}
	}
	return map[string]any{"decision": decision, "reason": reason}
}

func cmdPocketHook(args []string) {
	if len(args) != 1 || os.Getenv("LIVE_POCKET_HOOK") != args[0] {
		fmt.Println("{}")
		return
	}
	// Reuse the established relay with a fail-closed fallback for this mode.
	os.Setenv("LIVE_SIDECAR", os.Getenv("LIVE_POCKET_HOOK_URL"))
	os.Setenv("LIVE_TOKEN", os.Getenv("LIVE_POCKET_HOOK_TOKEN"))
	os.Setenv("LIVE_HOOK_TIMEOUT", "540")
	body, _ := io.ReadAll(io.LimitReader(os.Stdin, 16<<20))
	out, err := relay("pre-tool-use", body)
	if err != nil || !hasVerdict(out) {
		out, _ = json.Marshal(pocketHookResult(args[0], "deny"))
	}
	fmt.Println(string(out))
}
