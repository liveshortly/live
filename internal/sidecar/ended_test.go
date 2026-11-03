package sidecar

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liveshortly/live/internal/api"
)

// The server answers an ended session's stream with session_ended and closes
// it. The sidecar used to reconnect every 16 s forever (LSH-19); it must stop
// and hand over to OnEnded so the session process can exit.
func TestSessionEndedStopsReconnecting(t *testing.T) {
	var connects atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sessions/s1/agent/stream" {
			connects.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"type\":\"session_ended\"}\n\n")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	cred := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(cred, []byte(`{"access_token":"t"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LIVESHORTLY_CRED_PATH", cred)

	ended := make(chan struct{})
	s := testSidecar()
	s.Client, s.SessionID = &api.Client{Base: srv.URL}, "s1"
	s.OnEnded = func() { close(ended) }
	s.Start(context.Background())
	defer s.Shutdown()

	select {
	case <-ended:
	case <-time.After(3 * time.Second):
		t.Fatal("OnEnded not called after session_ended")
	}
	time.Sleep(1500 * time.Millisecond) // past the first 1 s retry backoff
	if n := connects.Load(); n != 1 {
		t.Fatalf("stream connected %d times, want 1 (no reconnect after session_ended)", n)
	}
}
