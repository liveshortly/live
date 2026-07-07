package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// withFakeCreds points auth at a throwaway credentials file with a far-future
// expiry, so Client.do's auth.AccessToken() succeeds without a refresh call.
func withFakeCreds(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	cred := `{"access_token":"testtoken","expires_at":"` +
		time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `"}`
	path := filepath.Join(dir, "credentials.json")
	if err := os.WriteFile(path, []byte(cred), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LIVESHORTLY_CRED_PATH", path)
}

func TestCreateSessionForked(t *testing.T) {
	withFakeCreds(t)

	var lastBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/sessions" {
			http.Error(w, "unexpected", http.StatusNotFound)
			return
		}
		b, _ := io.ReadAll(r.Body)
		lastBody = nil
		_ = json.Unmarshal(b, &lastBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "new_session_1",
			"handoff": map[string]any{
				"markdown": "# Prior context\n- did a thing",
				"turns": []map[string]any{
					{"seq": 1, "role": "user", "text": "hello"},
					{"seq": 2, "role": "assistant", "text": "hi"},
				},
				"source": map[string]any{
					"id": "src_9", "title": "Old session", "owner_handle": "alice",
					"snapshot_seq": 42,
				},
				"truncated": true,
			},
		})
	}))
	defer srv.Close()

	c := &Client{Base: srv.URL, hc: srv.Client()}

	t.Run("handoff code posts forked_from", func(t *testing.T) {
		id, bundle, err := c.CreateSessionForked("t", "claude-code", "claude-code", "hooks",
			ForkRef{HandoffCode: "ho_abc"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if id != "new_session_1" {
			t.Fatalf("id = %q", id)
		}
		if lastBody["forked_from"] != "ho_abc" {
			t.Fatalf("forked_from = %v (body %v)", lastBody["forked_from"], lastBody)
		}
		if _, ok := lastBody["forked_from_session_id"]; ok {
			t.Fatalf("session-id key should be absent for a handoff code: %v", lastBody)
		}
		if bundle.Markdown == "" || bundle.Source.Title != "Old session" || !bundle.Truncated {
			t.Fatalf("bundle not decoded: %+v", bundle)
		}
		if bundle.Source.SnapshotSeq != 42 || len(bundle.Turns) != 2 {
			t.Fatalf("bundle detail wrong: %+v", bundle)
		}
	})

	t.Run("session id posts forked_from_session_id and seq", func(t *testing.T) {
		seq := 17
		id, _, err := c.CreateSessionForked("t", "claude-code", "claude-code", "hooks",
			ForkRef{SessionID: "sess_5", Seq: &seq})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if id != "new_session_1" {
			t.Fatalf("id = %q", id)
		}
		if lastBody["forked_from_session_id"] != "sess_5" {
			t.Fatalf("forked_from_session_id = %v", lastBody["forked_from_session_id"])
		}
		// JSON numbers decode to float64.
		if lastBody["forked_from_seq"] != float64(17) {
			t.Fatalf("forked_from_seq = %v", lastBody["forked_from_seq"])
		}
		if _, ok := lastBody["forked_from"]; ok {
			t.Fatalf("handoff-code key should be absent for a session id: %v", lastBody)
		}
	})

	t.Run("no ref is an error", func(t *testing.T) {
		if _, _, err := c.CreateSessionForked("t", "f", "a", "m", ForkRef{}); err == nil {
			t.Fatal("expected error for empty ForkRef")
		}
	})
}

func TestCreateSessionForkedForbidden(t *testing.T) {
	withFakeCreds(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()

	c := &Client{Base: srv.URL, hc: srv.Client()}
	_, _, err := c.CreateSessionForked("t", "f", "a", "m", ForkRef{SessionID: "private_1"})
	if !errors.Is(err, ErrForkForbidden) {
		t.Fatalf("expected ErrForkForbidden, got %v", err)
	}
}
