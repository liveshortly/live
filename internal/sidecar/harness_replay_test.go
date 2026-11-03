package sidecar

// Golden replay of hook-based harness captures (claude, agy) — LSH-00. See
// cmd/live/harness_replay_test.go and testdata/README.md for the format.
//
// Each line of testdata/<agent>/<version>/hooks.jsonl is one of:
//
//	{"hook": "<live hook event>", "payload": {…}}  hook stdin, as relayed by `live hook`
//	{"comment": {"username": …, "message": …}}     a viewer comment arriving now
//	{"verdict": "allow"|"deny"|"always"}            the owner's answer to the next permission ask
//
// "$FIXTURE" in a payload string is replaced with the fixture dir (for
// transcript paths). Web permissions are ON (the `live claude`/`live agy`
// default); an ask with no queued verdict is answered "allow". The golden holds
// the feed events in order, plus "_hook_output" (what the agent got back) and
// "_model" (the model reported to the server).
//
//	go test ./... -run Replay -update

import (
	"bytes"
	"encoding/json"
	"flag"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liveshortly/live/internal/api"
	"github.com/liveshortly/live/internal/perm"
)

var update = flag.Bool("update", false, "rewrite events.golden.json from the current hook handler output")

const testdataRoot = "../../testdata"

type goldenEvent struct {
	Type    string         `json:"type"`
	Payload map[string]any `json:"payload,omitempty"`
}

func TestReplayHooks(t *testing.T) {
	// desktopNotify shells out to terminal-notifier/osascript on every
	// permission ask; an empty PATH keeps the replay from popping real
	// notifications.
	t.Setenv("PATH", "")

	files, _ := filepath.Glob(filepath.Join(testdataRoot, "*", "*", "hooks.jsonl"))
	if len(files) == 0 {
		t.Fatal("no hooks.jsonl fixtures")
	}
	for _, f := range files {
		dir := filepath.Dir(f)
		name, _ := filepath.Rel(testdataRoot, dir)
		t.Run(filepath.ToSlash(name), func(t *testing.T) { replayHooks(t, dir) })
	}
}

func replayHooks(t *testing.T, dir string) {
	// Model reports go to a stub API so they can be asserted.
	models := make(chan string, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Model != "" {
			models <- body.Model
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	cred := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(cred, []byte(`{"access_token":"replay"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LIVESHORTLY_CRED_PATH", cred)
	t.Setenv("HOME", t.TempDir()) // no host.json: never the operator's configured server
	t.Setenv("LIVESHORTLY_API_URL", srv.URL)

	var verdicts []string
	var vmu sync.Mutex
	broker := perm.New(nil, time.Minute)
	stop := make(chan struct{})
	defer close(stop)
	go func() { // stands in for the CLI popup: answers each ask from the fixture
		for {
			select {
			case p := <-broker.Notify():
				vmu.Lock()
				v := "allow"
				if len(verdicts) > 0 {
					v, verdicts = verdicts[0], verdicts[1:]
				}
				vmu.Unlock()
				p.Resolve(v, "replay")
			case <-stop:
				return
			}
		}
	}()

	s := &Sidecar{
		Client:         api.New(),
		SessionID:      "replay",
		WebURL:         "http://localhost:8080",
		Broker:         broker,
		WebPermissions: true,
		Agent:          AgentDisplayName(fixtureFramework[filepath.Base(filepath.Dir(dir))]),
		Log:            log.New(io.Discard, "", 0),
		events:         make(chan event, 1024),
	}

	var out []goldenEvent
	drain := func() {
		for {
			select {
			case e := <-s.events:
				out = append(out, goldenEvent{e.Type, dropTS(e.Payload)})
			default:
				return
			}
		}
	}

	raw, err := os.ReadFile(filepath.Join(dir, "hooks.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.ReplaceAll(raw, []byte("$FIXTURE"), []byte(dir))
	for _, line := range bytes.Split(raw, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var step struct {
			Hook    string         `json:"hook"`
			Payload map[string]any `json:"payload"`
			Comment *api.Comment   `json:"comment"`
			Verdict string         `json:"verdict"`
		}
		if err := json.Unmarshal(line, &step); err != nil {
			t.Fatalf("bad fixture line %s: %v", line, err)
		}
		switch {
		case step.Comment != nil:
			s.QueueForHooks(*step.Comment)
		case step.Verdict != "":
			vmu.Lock()
			verdicts = append(verdicts, step.Verdict)
			vmu.Unlock()
		case step.Hook != "":
			res := s.handleHook(step.Hook, step.Payload)
			drain()
			if res != nil {
				res["hook"] = step.Hook
				out = append(out, goldenEvent{"_hook_output", res})
			}
		}
	}
	// The model is reported once, asynchronously.
	select {
	case m := <-models:
		out = append(out, goldenEvent{"_model", map[string]any{"model": m}})
	case <-time.After(2 * time.Second):
	}
	checkGolden(t, dir, out)
}

// fixtureFramework maps a testdata/<agent> dir to the session framework `live`
// sets for it (profileFor in cmd/live/run.go), which names the agent in asks.
var fixtureFramework = map[string]string{"claude": "claude-code", "agy": "agy"}

func dropTS(p map[string]any) map[string]any {
	clean := map[string]any{}
	for k, v := range p {
		if k != "ts" {
			clean[k] = v
		}
	}
	return clean
}

func checkGolden(t *testing.T, dir string, events []goldenEvent) {
	t.Helper()
	got, err := json.MarshalIndent(events, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join(dir, "events.golden.json")
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run: go test ./... -run Replay -update)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s: feed events differ from golden.\n--- got\n%s\n--- want\n%s", path, got, strings.TrimSpace(string(want)))
	}
}
