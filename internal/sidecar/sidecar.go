// Package sidecar is the long-lived in-process daemon that owns one live
// session: it queues and uploads events, receives viewer comments in real
// time (SSE when the server supports it, 3s polling otherwise), and serves
// the local hook endpoint that `live hook <event>` relays into.
package sidecar

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/LiveShortly/live/internal/api"
	"github.com/LiveShortly/live/internal/perm"
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

	// OnComment, when set (PTY mode), is the sole delivery for a viewer comment:
	// it's shown to the operator and nothing else routes it (the agent can't be
	// injected). When nil (hooks / channel modes) comments route to the channel
	// subscriber and the hook-boundary queue instead.
	OnComment func(api.Comment)

	// DisplayComment, when set, is an ADDITIVE terminal echo — it's called for
	// every viewer comment for visibility, and does NOT stop the comment from
	// also routing to the agent (channel subscriber + hook queue). Used by
	// rollout mode so a web comment shows in the CLI *and* reaches codex.
	DisplayComment func(api.Comment)

	// Broker, when set (tmux mode), races a CLI popup answer against the web
	// verdict for each permission. When nil, web-only (awaitDecision).
	Broker *perm.Broker

	// WebPermissions, when true, makes the PreToolUse hook block on the web
	// `/decision` queue and answer Claude's tool-permission prompt from the
	// owner's browser tap instead of the local keyboard. Opt-in.
	WebPermissions bool

	Log *log.Logger

	events chan event
	wg     sync.WaitGroup
	cancel context.CancelFunc

	mu            sync.Mutex
	pending       []api.Comment
	modelReported bool
	httpSrv       *http.Server

	subMu sync.Mutex
	subs  map[chan api.Comment]struct{}

	// permAllow remembers "always allow" decisions (web-permissions mode) so a
	// remembered tool/command auto-allows on later calls without re-prompting.
	permMu    sync.Mutex
	permAllow map[string]bool

	hookToken string
}

// permAllowed reports whether an "always allow" was recorded for this key.
func (s *Sidecar) permAllowed(key string) bool {
	s.permMu.Lock()
	defer s.permMu.Unlock()
	return s.permAllow[key]
}

// permRemember records an "always allow" for this key.
func (s *Sidecar) permRemember(key string) {
	s.permMu.Lock()
	defer s.permMu.Unlock()
	if s.permAllow == nil {
		s.permAllow = map[string]bool{}
	}
	s.permAllow[key] = true
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
	// Additive terminal echo: show every comment in the CLI without consuming
	// it — delivery to the agent still happens below.
	if s.DisplayComment != nil {
		for _, c := range comments {
			s.DisplayComment(c)
		}
	}
	if s.OnComment != nil {
		for _, c := range comments {
			s.OnComment(c)
		}
		return
	}
	// A connected channel subscriber (`live channel` MCP) can deliver into the
	// agent even while it is idle. We push to it AND always keep the comment in
	// the hook-boundary queue: a buffered send to the subscriber is not proof
	// Claude actually consumed it — the experimental channels path can silently
	// drop, in which case the comment (already popped from the server queue)
	// would be lost forever. The hook fallback (UserPromptSubmit/PostToolUse/
	// Stop) guarantees delivery. A rare duplicate when the channel *is* healthy
	// is acceptable; a silently-lost viewer message is not.
	routed := 0
	s.subMu.Lock()
	for _, c := range comments {
		for ch := range s.subs {
			select {
			case ch <- c:
				routed++
			default:
			}
		}
	}
	s.subMu.Unlock()

	// Always queue for hook-boundary injection (guaranteed-delivery backbone).
	s.mu.Lock()
	s.pending = append(s.pending, comments...)
	s.mu.Unlock()

	if routed > 0 {
		s.Log.Printf("%d viewer comment(s) pushed to channel", routed)
	}
	s.Log.Printf("%d viewer comment(s) queued for hook injection", len(comments))

	// The agent may be idle (no hook boundary coming) — surface arrival
	// out-of-band so the operator knows a message is waiting.
	go desktopNotify(comments)
}

// subscribe registers a channel-delivery consumer; anything already queued
// for hook injection is handed over immediately. The returned func
// unsubscribes.
func (s *Sidecar) subscribe() (chan api.Comment, func()) {
	ch := make(chan api.Comment, 64)
	s.subMu.Lock()
	if s.subs == nil {
		s.subs = make(map[chan api.Comment]struct{})
	}
	s.subs[ch] = struct{}{}
	s.subMu.Unlock()

	for _, c := range s.takePending() {
		select {
		case ch <- c:
		default:
		}
	}
	return ch, func() {
		s.subMu.Lock()
		delete(s.subs, ch)
		s.subMu.Unlock()
		// Anything still buffered was never delivered — put it back on the
		// hook-injection queue rather than losing it.
		for {
			select {
			case c := <-ch:
				s.mu.Lock()
				s.pending = append(s.pending, c)
				s.mu.Unlock()
			default:
				return
			}
		}
	}
}

//go:embed assets/icon.png
var iconPNG []byte

// iconPath writes the embedded LiveShortly icon to ~/.liveshortly/icon.png
// once and returns its path (empty on failure). Used to brand notifications.
func iconPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	p := filepath.Join(home, ".liveshortly", "icon.png")
	if fi, err := os.Stat(p); err == nil && fi.Size() == int64(len(iconPNG)) {
		return p
	}
	if os.MkdirAll(filepath.Dir(p), 0o755) != nil {
		return ""
	}
	if os.WriteFile(p, iconPNG, 0o644) != nil {
		return ""
	}
	return p
}

// desktopNotify shows a best-effort OS notification for a comment batch.
// It must never touch the terminal: the agent owns it. On macOS it uses
// terminal-notifier (which shows the LiveShortly icon) when installed,
// falling back to osascript (which always shows the Script Editor icon —
// an Apple limitation that cannot be overridden from AppleScript).
func desktopNotify(comments []api.Comment) {
	if len(comments) == 0 {
		return
	}
	who := comments[0].Username
	if who == "" {
		who = "viewer"
	}
	body := comments[0].Message
	if len(comments) > 1 {
		body = fmt.Sprintf("%s (+%d more)", body, len(comments)-1)
	}

	switch runtime.GOOS {
	case "darwin":
		if tn, err := exec.LookPath("terminal-notifier"); err == nil {
			args := []string{"-title", "LiveShortly", "-subtitle", who, "-message", body,
				"-sound", "default", "-open", "https://liveshortly.com"}
			if icon := iconPath(); icon != "" {
				args = append(args, "-appIcon", icon, "-contentImage", icon)
			}
			_ = exec.Command(tn, args...).Run()
			return
		}
		esc := func(s string) string { return strings.ReplaceAll(s, `"`, `'`) }
		script := fmt.Sprintf(`display notification %q with title "LiveShortly" subtitle %q`, esc(body), esc(who))
		_ = exec.Command("osascript", "-e", script).Run()
	case "linux":
		if p, err := exec.LookPath("notify-send"); err == nil {
			args := []string{"LiveShortly — " + who, body}
			if icon := iconPath(); icon != "" {
				args = append([]string{"-i", icon}, args...)
			}
			_ = exec.Command(p, args...).Run()
		}
	}
}

// RequestPermission surfaces a tool-permission prompt and returns the owner's
// verdict ("allow", "deny", or "always") — the web/broker flow used by the
// PreToolUse hook, exported for agents (e.g. the codex app-server client) that
// gate tool execution themselves. Blocks until answered, cancelled, or the
// grace window expires (→ "deny").
func (s *Sidecar) RequestPermission(ctx context.Context, tool, desc string) string {
	if s.Broker != nil {
		v, _ := s.Broker.Request(ctx, tool, desc)
		return v
	}
	return s.awaitDecision(ctx, tool, desc)
}

// permissionGrace bounds how long the channel waits for a UI verdict before
// defaulting to deny (the local terminal prompt remains answerable meanwhile).
const permissionGrace = 8 * time.Minute

// awaitDecision emits a permission prompt to the web feed and polls the
// server's decision queue until the owner answers, the context is cancelled
// (Claude resolved it locally, dropping the connection), or the grace window
// expires. Returns "allow" or "deny" ("deny" on timeout — the safe default).
func (s *Sidecar) awaitDecision(ctx context.Context, tool, desc string) string {
	msg := "Claude wants to run " + tool
	if desc != "" {
		msg += ": " + desc
	}
	s.Emit("input_requested", map[string]any{
		"message": truncate(msg, 500),
		"kind":    "permission",
		"ts":      nowISO(),
	})
	go desktopNotify([]api.Comment{{Username: "permission", Message: msg}})

	deadline := time.After(permissionGrace)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return "deny"
		case <-deadline:
			s.Log.Printf("permission prompt timed out → deny")
			return "deny"
		case <-tick.C:
			v, err := s.Client.GetDecision(s.SessionID)
			if err != nil || v == "" {
				continue
			}
			s.Log.Printf("permission %q resolved via UI → %s", tool, v)
			return v
		}
	}
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

	// tmux popup <-> broker: the `live perm-prompt` popup reads the pending
	// permission and posts the owner's keyboard answer.
	mux.HandleFunc("GET /perm/pending", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Live-Token") != s.hookToken {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		out := map[string]any{}
		if s.Broker != nil {
			if p := s.Broker.Current(); p != nil {
				out = map[string]any{"pending": true, "tool": p.Tool, "detail": p.Detail}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("POST /perm/answer", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Live-Token") != s.hookToken {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		var req struct {
			Decision string `json:"decision"`
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		_ = json.Unmarshal(body, &req)
		ok := false
		if s.Broker != nil && (req.Decision == "allow" || req.Decision == "deny" || req.Decision == "always") {
			if p := s.Broker.Current(); p != nil {
				ok = p.Resolve(req.Decision, "cli")
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": ok})
	})

	// Permission relay for the `live channel` MCP subprocess: surface the
	// prompt to the web UI and long-poll for the owner's allow/deny verdict.
	mux.HandleFunc("POST /permission", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Live-Token") != s.hookToken {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		var req struct {
			RequestID   string `json:"request_id"`
			ToolName    string `json:"tool_name"`
			Description string `json:"description"`
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		_ = json.Unmarshal(body, &req)

		verdict := s.awaitDecision(r.Context(), req.ToolName, req.Description)
		if verdict == "always" { // the channel path has no "remember" — treat as allow
			verdict = "allow"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"behavior": verdict})
	})

	// SSE feed of viewer comments for the `live channel` MCP subprocess.
	mux.HandleFunc("GET /comments/subscribe", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Live-Token") != s.hookToken {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		fl, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		fl.Flush()

		ch, unsub := s.subscribe()
		defer unsub()
		s.Log.Printf("channel subscriber connected")

		hb := time.NewTicker(15 * time.Second)
		defer hb.Stop()
		for {
			select {
			case c := <-ch:
				b, _ := json.Marshal(c)
				fmt.Fprintf(w, "data: %s\n\n", b)
				fl.Flush()
			case <-hb.C:
				fmt.Fprint(w, ": hb\n\n")
				fl.Flush()
			case <-r.Context().Done():
				s.Log.Printf("channel subscriber disconnected")
				return
			}
		}
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
