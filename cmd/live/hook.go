package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

// cmdHook is the relay the agent's hook system invokes (`live hook <event>`).
// It forwards the hook's stdin JSON to the parent shim's sidecar and prints
// the sidecar's hook-output JSON. It must be fast, silent, and always emit
// valid JSON + exit 0 so a broken sidecar can never wedge the agent.
//
// `live hook pre-tool-use --gated` (gemini: `gemini-before-tool --gated`) is
// installed only when LiveShortly is the approval gate (web permissions; the
// agent's own prompt is skipped). There a sidecar that can't answer must never
// let the tool run, so the relay denies.
func cmdHook(args []string) {
	event := ""
	if len(args) > 0 {
		event = args[0]
	}
	gated := slices.Contains(args[1:], "--gated")
	var body []byte
	if os.Getenv("LIVE_SIDECAR") != "" || gated {
		body, _ = io.ReadAll(io.LimitReader(os.Stdin, 16<<20))
	}
	out, err := relay(event, body)
	out = hookOutput(event, gated, body, out, err)
	os.Stdout.Write(out)
	if !bytes.HasSuffix(out, []byte("\n")) {
		os.Stdout.WriteString("\n")
	}
}

// hookOutput turns the relay result into what the agent reads. Gated
// pre-tool-use fails closed: any error, or a reply without a verdict, denies.
func hookOutput(event string, gated bool, body, out []byte, err error) []byte {
	if gated {
		if err == nil && out == nil {
			err = errors.New("not running under live (LIVE_SIDECAR/LIVE_TOKEN unset)")
		}
		if err == nil && !hasVerdict(out) {
			err = fmt.Errorf("malformed sidecar reply: %.200q", out)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "live: approvals offline — tool call denied: %v\n", err)
			return denyOutput(event, body, err)
		}
		return out
	}
	if len(bytes.TrimSpace(out)) > 0 {
		return out
	}
	switch event {
	case "pre-tool-use", "stop": // ungated: the agent's own prompt still gates tools
		return []byte(`{"decision":"allow"}`)
	case "pre-invocation":
		return []byte(`{"injectSteps":[]}`)
	}
	return []byte("{}")
}

// hasVerdict reports whether a pre-tool-use reply carries an explicit
// decision, in Claude/codex (hookSpecificOutput) or agy (top-level) shape.
func hasVerdict(out []byte) bool {
	var r struct {
		Decision string `json:"decision"`
		HSO      struct {
			PermissionDecision string `json:"permissionDecision"`
		} `json:"hookSpecificOutput"`
	}
	if json.Unmarshal(out, &r) != nil {
		return false
	}
	ok := func(d string) bool { return d == "allow" || d == "deny" || d == "ask" }
	return ok(r.Decision) || ok(r.HSO.PermissionDecision)
}

// denyOutput is a fail-closed verdict in the shape the calling agent expects
// (agy payloads carry "toolCall" and gemini reads the same flat shape — keyed
// on the event, so an unreadable body can't pick the shape gemini ignores;
// Claude and codex share hookSpecificOutput).
func denyOutput(event string, body []byte, cause error) []byte {
	reason := "LiveShortly approvals are offline (" + cause.Error() + "), so this tool call was denied for safety. " +
		"Tell the user approvals are unavailable; restarting the live session restores them."
	var p map[string]any
	_ = json.Unmarshal(body, &p)
	var v any
	if p["toolCall"] != nil || strings.HasPrefix(event, "gemini-") {
		v = map[string]any{"decision": "deny", "reason": reason}
	} else {
		v = map[string]any{"hookSpecificOutput": map[string]any{
			"hookEventName": "PreToolUse", "permissionDecision": "deny", "permissionDecisionReason": reason}}
	}
	b, _ := json.Marshal(v)
	return b
}

// relay posts body to the sidecar's /hook/<event> and returns its reply.
// (nil, nil) means not running under `live` — a no-op.
func relay(event string, body []byte) ([]byte, error) {
	addr := os.Getenv("LIVE_SIDECAR")
	token := os.Getenv("LIVE_TOKEN")
	if event == "" || addr == "" || token == "" {
		return nil, nil
	}

	req, err := http.NewRequest("POST", addr+"/hook/"+event, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Live-Token", token)

	// Default to a snappy timeout; web-permissions mode raises it via
	// LIVE_HOOK_TIMEOUT so the PreToolUse relay can block on a human verdict.
	timeout := 6 * time.Second
	if v := os.Getenv("LIVE_HOOK_TIMEOUT"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
			timeout = time.Duration(secs) * time.Second
		}
	}
	hc := &http.Client{Timeout: timeout}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sidecar unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sidecar returned HTTP %d", resp.StatusCode)
	}
	out, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("reading sidecar reply: %w", err)
	}
	return out, nil
}
