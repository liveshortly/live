package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// decisionOf extracts the verdict from Claude/codex or agy hook output.
func decisionOf(t *testing.T, out []byte) string {
	t.Helper()
	var r struct {
		Decision string `json:"decision"`
		HSO      struct {
			PermissionDecision string `json:"permissionDecision"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		t.Fatalf("hook output is not JSON: %q", out)
	}
	if r.HSO.PermissionDecision != "" {
		return r.HSO.PermissionDecision
	}
	return r.Decision
}

func TestHookFailsClosedWhenGated(t *testing.T) {
	reply := func(status int, body string, delay time.Duration) string {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(delay)
			w.WriteHeader(status)
			w.Write([]byte(body))
		}))
		t.Cleanup(srv.Close)
		return srv.URL
	}
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()

	allow := `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow"}}`
	deny := `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny"}}`
	claudeBody := `{"tool_name":"Bash","tool_input":{"command":"rm -rf /"}}`
	agyBody := `{"toolCall":{"name":"run_command"}}`

	for _, tc := range []struct {
		name, addr, body string
		gated            bool
		want             string
	}{
		{"unreachable", deadURL, claudeBody, true, "deny"},
		{"timeout", reply(200, allow, 2*time.Second), claudeBody, true, "deny"},
		{"5xx", reply(500, "boom", 0), claudeBody, true, "deny"},
		{"malformed JSON", reply(200, `{"hookSpecific`, 0), claudeBody, true, "deny"},
		{"no verdict", reply(200, `{}`, 0), claudeBody, true, "deny"},
		{"no sidecar env", "", claudeBody, true, "deny"},
		{"no sidecar env agy", "", agyBody, true, "deny"},
		{"unreachable agy", deadURL, agyBody, true, "deny"},
		{"explicit deny", reply(200, deny, 0), claudeBody, true, "deny"},
		{"explicit allow", reply(200, allow, 0), claudeBody, true, "allow"},
		{"agy allow", reply(200, `{"decision":"allow"}`, 0), agyBody, true, "allow"},
		// Ungated (bypass / agent's own prompt in charge): unchanged behaviour.
		{"bypass unreachable", deadURL, claudeBody, false, "allow"},
		{"bypass 5xx", reply(500, "boom", 0), claudeBody, false, "allow"},
		{"bypass no sidecar env", "", claudeBody, false, "allow"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("LIVE_SIDECAR", tc.addr)
			t.Setenv("LIVE_TOKEN", "tok")
			t.Setenv("LIVE_HOOK_TIMEOUT", "1")
			out, err := relay("pre-tool-use", []byte(tc.body))
			got := decisionOf(t, hookOutput("pre-tool-use", tc.gated, []byte(tc.body), out, err))
			if got != tc.want {
				t.Fatalf("decision = %q, want %q", got, tc.want)
			}
		})
	}
}

// relay with no LIVE_SIDECAR is a no-op (nil, nil); hookOutput decides.
func TestRelayWithoutSidecarIsEmpty(t *testing.T) {
	t.Setenv("LIVE_SIDECAR", "")
	if out, err := relay("pre-tool-use", []byte(`{}`)); out != nil || err != nil {
		t.Fatalf("relay = %q, %v; want nil, nil", out, err)
	}
}
