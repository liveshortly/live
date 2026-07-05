// Package sidecar is the long-lived in-process daemon that owns one live
// session: it queues and uploads events, receives viewer comments in real
// time (SSE when the server supports it, 3s polling otherwise), and serves
// the local hook endpoint that `live hook <event>` relays into.
package sidecar

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/resapce/live/internal/api"
)

type event struct {
	Type    string
	Payload map[string]any
}

// Sidecar drives one live session end to end.
type Sidecar struct {
	Client    *api.Client
	SessionID string
	WebURL    string

	// OnComment, when set (PTY mode), is called for every viewer comment as
	// it arrives. When nil (hooks mode) comments accumulate in a local queue
	// and are injected into the agent at the next hook boundary.
	OnComment func(api.Comment)

	Log *log.Logger

	events chan event
	wg     sync.WaitGroup
	cancel context.CancelFunc

	mu            sync.Mutex
	pending       []api.Comment
	modelReported bool
	httpSrv       *http.Server

	hookToken string
}

// Start launches the upload worker and the comment receiver.
func (s *Sidecar) Start(ctx context.Context) {
	ctx, s.cancel = context.WithCancel(ctx)
	s.events = make(chan event, 2048)
	if s.Log == nil {
		s.Log = log.New(io.Discard, "", 0)
	}

	s.wg.Add(1)
	go s.uploadWorker()

	s.wg.Add(1)
	go s.receiveComments(ctx)
}

// Emit queues one event for upload; never blocks the caller for long — when
// the queue is full the event is dropped (feed is best-effort, agent is not).
func (s *Sidecar) Emit(eventType string, payload map[string]any) {
	select {
	case s.events <- event{eventType, payload}:
	default:
		s.Log.Printf("event queue full, dropped %s", eventType)
	}
}

// Shutdown flushes queued events (bounded), stops the receivers and the hook
// server. Safe to call once.
func (s *Sidecar) Shutdown() {
	s.cancel()
	close(s.events)

	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(12 * time.Second):
		s.Log.Printf("shutdown: flush timed out")
	}
	if s.httpSrv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.httpSrv.Shutdown(ctx)
	}
}

func (s *Sidecar) uploadWorker() {
	defer s.wg.Done()
	for ev := range s.events {
		var err error
		for attempt := 0; attempt < 3; attempt++ {
			if err = s.Client.EmitEvent(s.SessionID, ev.Type, ev.Payload); err == nil {
				break
			}
			time.Sleep(time.Duration(attempt+1) * 400 * time.Millisecond)
		}
		if err != nil {
			s.Log.Printf("emit %s failed: %v", ev.Type, err)
		}
	}
}

// ── Viewer comments ───────────────────────────────────────────────────────────

// receiveComments prefers the agent SSE stream (real-time push; each push is
// acked by draining the pending queue). If the server predates the stream it
// falls back to 3s polling — same cadence as the old plugin channel.
func (s *Sidecar) receiveComments(ctx context.Context) {
	defer s.wg.Done()

	sseGone := false
	backoff := time.Second
	for ctx.Err() == nil {
		if !sseGone {
			err := s.Client.AgentStream(ctx, s.SessionID, func(string) { s.drain() })
			if err == api.ErrAgentStreamUnsupported {
				sseGone = true
				s.Log.Printf("agent stream unsupported — falling back to polling")
				continue
			}
			if ctx.Err() != nil {
				return
			}
			s.Log.Printf("agent stream dropped (%v), retrying in %s", err, backoff)
			s.drain() // catch anything missed while disconnected
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
			if backoff < 10*time.Second {
				backoff *= 2
			}
			continue
		}

		select {
		case <-time.After(3 * time.Second):
			s.drain()
		case <-ctx.Done():
			return
		}
	}
}

// drain pops all pending viewer comments from the server and routes them.
func (s *Sidecar) drain() {
	comments, err := s.Client.DrainPending(s.SessionID)
	if err != nil || len(comments) == 0 {
		return
	}
	if s.OnComment != nil {
		for _, c := range comments {
			s.OnComment(c)
		}
		return
	}
	s.mu.Lock()
	s.pending = append(s.pending, comments...)
	s.mu.Unlock()
	s.Log.Printf("%d viewer comment(s) queued for injection", len(comments))
}

// takePending returns and clears locally queued comments (hooks mode).
func (s *Sidecar) takePending() []api.Comment {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.pending
	s.pending = nil
	return out
}

// ── Local hook endpoint ───────────────────────────────────────────────────────

// StartHookServer binds 127.0.0.1:0 and serves POST /hook/{event} for the
// `live hook` relay. Returns the address and the shared secret the relay must
// present.
func (s *Sidecar) StartHookServer() (addr, token string, err error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	s.hookToken = hex.EncodeToString(raw)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", "", err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /hook/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Live-Token") != s.hookToken {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		eventName := strings.TrimPrefix(r.URL.Path, "/hook/")
		var payload map[string]any
		body, _ := io.ReadAll(io.LimitReader(r.Body, 16<<20))
		if len(body) > 0 {
			_ = json.Unmarshal(body, &payload)
		}
		resp := s.handleHook(eventName, payload)
		if resp == nil {
			resp = map[string]any{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	s.httpSrv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := s.httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
			s.Log.Printf("hook server: %v", err)
		}
	}()
	return fmt.Sprintf("http://%s", ln.Addr().String()), s.hookToken, nil
}

func nowISO() string { return time.Now().UTC().Format(time.RFC3339) }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
