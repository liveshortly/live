package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func waitPocketPermission(t *testing.T, s *pocketServer, base string) pocketSession {
	t.Helper()
	for i := 0; i < 200; i++ {
		var v pocketSession
		w := pocketRequest(s, "GET", base, "")
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		if len(v.Permissions) > 0 {
			return v
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("No permission reached the client")
	return pocketSession{}
}

func TestPocketPermissionLifecycle(t *testing.T) {
	for _, decision := range []string{"allow", "deny", "stop"} {
		t.Run(decision, func(t *testing.T) {
			s := testPocket(t)
			result := make(chan string, 1)
			s.run = func(ctx context.Context, a, d, p, m string) (string, error) {
				v := pocketAsk(ctx, "Bash", "touch example.txt")
				result <- v
				return v, ctx.Err()
			}
			v := createPocket(t, s)
			base := "/v1/sessions/" + v.ID
			pocketRequest(s, "POST", base+"/messages", `{"text":"run"}`)
			v = waitPocketPermission(t, s, base)
			path := base + "/permissions/" + v.Permissions[0].ID
			select {
			case <-result:
				t.Fatal("Tool ran without permission")
			default:
			}
			// Requests survive phone reconnects, but the access key is required.
			again := waitPocketPermission(t, s, base)
			if again.Permissions[0].ID != v.Permissions[0].ID {
				t.Fatal("Unstable request ID")
			}
			w := httptest.NewRecorder()
			s.ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader(`{"decision":"allow"}`)))
			if w.Code != 401 {
				t.Fatal(w.Code)
			}
			if w := pocketRequest(s, "POST", path, `{"decision":"always"}`); w.Code != 400 {
				t.Fatal(w.Code)
			}
			other := createPocket(t, s)
			if w := pocketRequest(s, "POST", "/v1/sessions/"+other.ID+"/permissions/"+v.Permissions[0].ID, `{"decision":"allow"}`); w.Code != 409 {
				t.Fatal(w.Code)
			}
			want := decision
			if decision == "stop" {
				pocketRequest(s, "POST", base+"/stop", "")
				want = "deny"
			} else {
				if w := pocketRequest(s, "POST", path, fmt.Sprintf(`{"decision":%q}`, decision)); w.Code != 200 {
					t.Fatal(w.Body.String())
				}
			}
			select {
			case got := <-result:
				if got != want {
					t.Fatalf("got %s want %s", got, want)
				}
			case <-time.After(time.Second):
				t.Fatal("Agent did not resume")
			}
			if w := pocketRequest(s, "POST", path, `{"decision":"allow"}`); w.Code != 409 {
				t.Fatal("Replayed decision accepted", w.Code)
			}
			for i := 0; i < 200; i++ {
				var latest pocketSession
				json.Unmarshal(pocketRequest(s, "GET", base, "").Body.Bytes(), &latest)
				if latest.Status != "running" && len(latest.Permissions) == 0 {
					return
				}
				time.Sleep(5 * time.Millisecond)
			}
			t.Fatal("Turn remained pending")
		})
	}
}

func TestPocketCodexNativeApproval(t *testing.T) {
	dir := t.TempDir()
	// A protocol fixture emulates the installed app-server schema. The output is
	// emitted only after receiving the correct native decision for the same ID.
	script := `#!/usr/bin/env python3
import json,sys
for line in sys.stdin:
 m=json.loads(line)
 method=m.get('method')
 if method=='initialize': result={}
 elif method=='thread/start':
  assert m['params']['approvalPolicy']=='on-request'
  result={'thread':{'id':'thread-test'}}
 elif method=='turn/start':
  print(json.dumps({'id':m['id'],'result':{}}),flush=True)
  print(json.dumps({'id':'native-approval','method':'item/commandExecution/requestApproval','params':{'command':'touch example.txt','reason':'Needs permission'}}),flush=True)
  continue
 elif m.get('id')=='native-approval':
  assert m['result']['decision'] in ['accept','decline']
  print(json.dumps({'method':'item/completed','params':{'item':{'type':'agentMessage','text':m['result']['decision']}}}),flush=True)
  print(json.dumps({'method':'turn/completed','params':{'turn':{'status':'completed'}}}),flush=True)
  continue
 else: continue
 print(json.dumps({'id':m['id'],'result':result}),flush=True)
`
	if err := os.WriteFile(filepath.Join(dir, "codex"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, verdict := range []string{"allow", "deny"} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		ctx = context.WithValue(ctx, pocketPermissionKey{}, pocketPermissionAsk(func(tool, detail string) string {
			if tool != "Shell" || !strings.Contains(detail, "touch example.txt") {
				t.Error(tool, detail)
			}
			return verdict
		}))
		output, err := runPocketCodex(ctx, dir, "run the command")
		cancel()
		want := "accept"
		if verdict == "deny" {
			want = "decline"
		}
		if err != nil || strings.TrimSpace(output) != want {
			t.Fatal(output, err)
		}
	}
}

func TestPocketClaudeHookBridge(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = context.WithValue(ctx, pocketPermissionKey{}, pocketPermissionAsk(func(tool, detail string) string {
		if tool != "Bash" || !strings.Contains(detail, "touch example.txt") {
			t.Error(tool, detail)
		}
		return "deny"
	}))
	env, settings, cleanup, err := pocketHookBridge(ctx, "claude")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	b, err := os.ReadFile(settings)
	if err != nil || !strings.Contains(string(b), "PreToolUse") {
		t.Fatal(string(b), err)
	}
	values := map[string]string{}
	for _, entry := range env {
		parts := strings.SplitN(entry, "=", 2)
		values[parts[0]] = parts[1]
	}
	url := values["LIVE_POCKET_HOOK_URL"] + "/hook/pre-tool-use"
	for _, authorized := range []bool{false, true} {
		req, err := http.NewRequest("POST", url, strings.NewReader(`{"tool_name":"Bash","tool_input":{"command":"touch example.txt"}}`))
		if err != nil {
			t.Fatal(err)
		}
		if authorized {
			req.Header.Set("X-Live-Token", values["LIVE_POCKET_HOOK_TOKEN"])
		}
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if !authorized {
			if response.StatusCode != 401 {
				t.Fatal(response.StatusCode)
			}
			continue
		}
		if response.StatusCode != 200 || !strings.Contains(string(data), `"permissionDecision":"deny"`) {
			t.Fatal(response.StatusCode, string(data))
		}
	}
}

func TestPocketAgyStructuredResult(t *testing.T) {
	for _, tc := range []struct {
		input, want string
		fail        bool
	}{
		{`{"event":"result","result":{"status":"SUCCESS","response":"done"}}`, "done", false},
		{`{"event":"result","result":{"status":"SUCCESS","response":""}}`, "AGY completed the turn without a text reply.", false},
		{`{"event":"result","result":{"status":"ERROR","error":"permission denied"}}`, "", true},
		{`{"event":"init"}`, "", true},
		{`{"event":"result","result":{"status":"SUCCESS","denied_actions":[{"action":"command"}]}}`, "", true},
	} {
		o := &pocketAgyOutput{}
		// Deliberately split JSON across writes, as real subprocess pipes do.
		for _, b := range []byte(tc.input) {
			o.Write([]byte{b})
		}
		got, err := o.answer()
		if got != tc.want || (err != nil) != tc.fail {
			t.Fatal(got, err)
		}
	}
}

func TestPocketConcurrentPermissionRequests(t *testing.T) {
	s := testPocket(t)
	answers := make(chan string, 2)
	s.run = func(ctx context.Context, a, d, p, m string) (string, error) {
		var group sync.WaitGroup
		for _, tool := range []string{"one", "two"} {
			group.Add(1)
			go func(tool string) { defer group.Done(); answers <- tool + ":" + pocketAsk(ctx, tool, tool) }(tool)
		}
		group.Wait()
		return "done", nil
	}
	session := createPocket(t, s)
	base := "/v1/sessions/" + session.ID
	pocketRequest(s, "POST", base+"/messages", `{"text":"run two tools"}`)
	var pending pocketSession
	for i := 0; i < 200; i++ {
		pending = waitPocketPermission(t, s, base)
		if len(pending.Permissions) == 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(pending.Permissions) != 2 {
		t.Fatal("Concurrent request overwritten")
	}
	for _, p := range pending.Permissions {
		decision := "deny"
		if p.Tool == "one" {
			decision = "allow"
		}
		if w := pocketRequest(s, "POST", base+"/permissions/"+p.ID, fmt.Sprintf(`{"decision":%q}`, decision)); w.Code != 200 {
			t.Fatal(w.Body.String())
		}
	}
	got := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case answer := <-answers:
			got[answer] = true
		case <-time.After(time.Second):
			t.Fatal("Blocked approval")
		}
	}
	if !got["one:allow"] || !got["two:deny"] {
		t.Fatal(got)
	}
	// Wait for persistence before TempDir cleanup.
	for i := 0; i < 200; i++ {
		var v pocketSession
		json.Unmarshal(pocketRequest(s, "GET", base, "").Body.Bytes(), &v)
		if v.Status != "running" {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("Turn did not complete")
}

func TestPocketHookRelayFailsClosed(t *testing.T) {
	t.Setenv("LIVE_POCKET_HOOK", "claude")
	t.Setenv("LIVE_POCKET_HOOK_URL", "http://127.0.0.1:1")
	t.Setenv("LIVE_POCKET_HOOK_TOKEN", "test-token")
	// cmdPocketHook rewrites these to drive the shared relay; restore after test.
	t.Setenv("LIVE_SIDECAR", "")
	t.Setenv("LIVE_TOKEN", "")
	t.Setenv("LIVE_HOOK_TIMEOUT", "")
	input, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	input.WriteString(`{"tool_name":"Bash"}`)
	input.Seek(0, 0)
	defer input.Close()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldIn, oldOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = input, w
	defer func() { os.Stdin, os.Stdout = oldIn, oldOut }()
	cmdPocketHook([]string{"claude"})
	w.Close()
	out, _ := io.ReadAll(r)
	r.Close()
	if !strings.Contains(string(out), `"permissionDecision":"deny"`) {
		t.Fatal(string(out))
	}
}
