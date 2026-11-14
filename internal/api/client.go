// Package api is the LiveShortly REST/SSE client used by the shim. All
// endpoints and payload shapes follow CONTRACT.md in the LiveShortly repo.
package api

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/liveshortly/live/internal/auth"
	"github.com/liveshortly/live/internal/hostcfg"
)

// streamClient returns an HTTP client for long-lived SSE streams. It forces
// HTTP/1.1: over HTTP/2, Cloudflare recycles edge connections and resets
// in-flight long-lived streams with RST_STREAM INTERNAL_ERROR ("stream error:
// … INTERNAL_ERROR; received from peer"). HTTP/1.1 chunked SSE survives that,
// and the server's 15s heartbeat beats Cloudflare's idle timeout. No request
// timeout — these streams are meant to stay open. Transport is cloned from the
// default so proxy and dial settings are preserved.
func streamClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ForceAttemptHTTP2 = false
	tr.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	return &http.Client{Transport: tr}
}

// Client is a bearer-authenticated LiveShortly API client with automatic
// one-shot token refresh on 401.
type Client struct {
	Base string
	hc   *http.Client
}

func New() *Client {
	return &Client{Base: hostcfg.APIURL(), hc: &http.Client{Timeout: 25 * time.Second}}
}

// do performs an authed request, refreshing the token once on 401.
// out may be nil. Returns the final HTTP status.
func (c *Client) do(method, path string, body any, out any) (int, error) {
	tok, err := auth.AccessToken()
	if err != nil {
		return 0, err
	}
	status, err := c.send(method, path, body, out, tok)
	if status == http.StatusUnauthorized {
		if tok, rerr := auth.Refresh(); rerr == nil {
			status, err = c.send(method, path, body, out, tok)
		}
	}
	return status, err
}

func (c *Client) send(method, path string, body any, out any, tok string) (int, error) {
	var rdr *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, c.Base+path, rdr)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		// The server explains refusals in {"error": "..."} (storage full,
		// session limit, too many live sessions); show that, not just a code.
		var e struct {
			Error string `json:"error"`
		}
		if json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&e) == nil && e.Error != "" {
			return resp.StatusCode, fmt.Errorf("%s (%s %s: HTTP %d)", e.Error, method, path, resp.StatusCode)
		}
		return resp.StatusCode, fmt.Errorf("%s %s: HTTP %d", method, path, resp.StatusCode)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp.StatusCode, fmt.Errorf("%s %s: decode: %w", method, path, err)
		}
	}
	return resp.StatusCode, nil
}

// ── Sessions ──────────────────────────────────────────────────────────────────

// CreateSession opens a live session and returns its id.
// agent/captureMode are forward-compatible fields the server may ignore.
func (c *Client) CreateSession(title, framework, agent, captureMode string) (string, error) {
	body := map[string]any{
		"title":        title,
		"framework":    framework,
		"agent":        agent,
		"capture_mode": captureMode,
	}
	var out struct {
		ID string `json:"id"`
	}
	if _, err := c.do("POST", "/api/sessions", body, &out); err != nil {
		return "", err
	}
	if out.ID == "" {
		return "", fmt.Errorf("create session: empty id")
	}
	return out.ID, nil
}

// ── Handoff / fork ────────────────────────────────────────────────────────────

// ErrForkForbidden means the caller can't read the source session (server 403),
// so it can't be forked. Surfaced with a clean message by the CLI.
var ErrForkForbidden = errors.New("you don't have access to fork this session")

// ForkRef identifies the session to continue from. Exactly one of HandoffCode
// or SessionID is set; Seq optionally pins a snapshot (only meaningful with
// SessionID — a handoff code already carries its own pinned snapshot).
type ForkRef struct {
	HandoffCode string // "ho_…" → forked_from
	SessionID   string // → forked_from_session_id
	Seq         *int   // → forked_from_seq (optional)
}

// HandoffTurn is one condensed turn from the source session's digest.
type HandoffTurn struct {
	Seq  int    `json:"seq"`
	Role string `json:"role"`
	Text string `json:"text"`
	Tool string `json:"tool,omitempty"`
	File string `json:"file,omitempty"`
}

// HandoffSource describes the session that was forked from.
type HandoffSource struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	OwnerHandle string `json:"owner_handle"`
	Agent       string `json:"agent"`
	GitRemote   string `json:"git_remote"`
	GitBranch   string `json:"git_branch"`
	Model       string `json:"model"`
	CreatedAt   string `json:"created_at"`
	EndedAt     string `json:"ended_at"`
	Status      string `json:"status"`
	SnapshotSeq int    `json:"snapshot_seq"`
}

// HandoffBundle is the digest the server returns when creating a forked
// session: raw markdown plus structured turns and source metadata. The CLI
// hands the markdown to the new agent as opening context and does not
// summarize it — the user's own agent does the comprehension.
type HandoffBundle struct {
	Markdown  string        `json:"markdown"`
	Turns     []HandoffTurn `json:"turns"`
	Source    HandoffSource `json:"source"`
	Truncated bool          `json:"truncated"`
}

// CreateSessionForked opens a new session (owned by the caller) that continues
// from another session, and returns its id plus the handoff digest. Server-side
// authorization (canRead) and the source's fork_count increment happen
// automatically; a 403 is mapped to ErrForkForbidden.
func (c *Client) CreateSessionForked(title, framework, agent, captureMode string, from ForkRef) (string, HandoffBundle, error) {
	body := map[string]any{
		"title":        title,
		"framework":    framework,
		"agent":        agent,
		"capture_mode": captureMode,
	}
	switch {
	case from.HandoffCode != "":
		body["forked_from"] = from.HandoffCode
	case from.SessionID != "":
		body["forked_from_session_id"] = from.SessionID
		if from.Seq != nil {
			body["forked_from_seq"] = *from.Seq
		}
	default:
		return "", HandoffBundle{}, fmt.Errorf("fork: no handoff code or session id")
	}

	var out struct {
		ID      string        `json:"id"`
		Handoff HandoffBundle `json:"handoff"`
	}
	status, err := c.do("POST", "/api/sessions", body, &out)
	if status == http.StatusForbidden {
		return "", HandoffBundle{}, ErrForkForbidden
	}
	if err != nil {
		return "", HandoffBundle{}, err
	}
	if out.ID == "" {
		return "", HandoffBundle{}, fmt.Errorf("fork session: empty id")
	}
	return out.ID, out.Handoff, nil
}

// HandoffInfo is the copyable-command payload from POST
// /api/sessions/{id}/handoff — used to mint `live claude --handoff <code>`.
type HandoffInfo struct {
	Code        string `json:"code"`
	SessionID   string `json:"session_id"`
	SnapshotSeq int    `json:"snapshot_seq"`
	Command     string `json:"command"`
	ExpiresAt   string `json:"expires_at"`
}

// GenerateHandoff mints a handoff code + copyable command for a session the
// caller can read.
func (c *Client) GenerateHandoff(id string) (HandoffInfo, error) {
	var out HandoffInfo
	status, err := c.do("POST", "/api/sessions/"+id+"/handoff", nil, &out)
	if status == http.StatusForbidden {
		return HandoffInfo{}, ErrForkForbidden
	}
	if err != nil {
		return HandoffInfo{}, err
	}
	return out, nil
}

// ReportModel sets the session's model label once it is known.
func (c *Client) ReportModel(id, model string) error {
	_, err := c.do("PATCH", "/api/sessions/"+id, map[string]string{"model": model}, nil)
	return err
}

// EmitEvent appends one event to the session feed.
// EmitResult carries the server's response to an emitted event. It is empty for
// a normal event; when the event tipped the owner over their storage quota the
// server auto-ends the session and returns the continuation details here.
type EmitResult struct {
	Ended          bool   `json:"ended"`
	Reason         string `json:"reason"`
	SummaryURL     string `json:"summary_url"`
	HandoffCommand string `json:"handoff_command"`
}

// QuotaEnded reports whether this event ended the session for hitting the quota.
func (r EmitResult) QuotaEnded() bool { return r.Ended && r.Reason == "quota" }

func (c *Client) EmitEvent(id, eventType string, payload map[string]any) (EmitResult, error) {
	var res EmitResult
	_, err := c.do("POST", "/api/sessions/"+id+"/events", map[string]any{
		"event_type": eventType,
		"payload":    payload,
		"actor":      "agent",
	}, &res)
	return res, err
}

// Stop ends the session and returns the shareable replay URL.
func (c *Client) Stop(id string) (string, error) {
	var out struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	if _, err := c.do("POST", "/api/sessions/"+id+"/stop", nil, &out); err != nil {
		return "", err
	}
	if out.URL != "" {
		return out.URL, nil
	}
	sid := out.ID
	if sid == "" {
		sid = id
	}
	return hostcfg.WebURL() + "/session/" + sid, nil
}

// ── Viewer comments ───────────────────────────────────────────────────────────

// Comment is one pending viewer message.
type Comment struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Message  string `json:"message"`
}

// DrainPending atomically pops all queued viewer comments (this is also the
// "ack" for anything delivered over the agent stream).
func (c *Client) DrainPending(id string) ([]Comment, error) {
	var out struct {
		Comments []Comment `json:"comments"`
	}
	if _, err := c.do("GET", "/api/sessions/"+id+"/comments/pending", nil, &out); err != nil {
		return nil, err
	}
	return out.Comments, nil
}

// GetDecision pops the next queued viewer permission verdict, if any.
// Returns "allow", "deny", or "" when no verdict is waiting.
func (c *Client) GetDecision(id string) (string, error) {
	var out struct {
		Decision string `json:"decision"`
	}
	if _, err := c.do("GET", "/api/sessions/"+id+"/decision", nil, &out); err != nil {
		return "", err
	}
	if out.Decision == "allow" || out.Decision == "deny" || out.Decision == "always" {
		return out.Decision, nil
	}
	return "", nil
}

// ── Agent stream (server change in flight — see docs/SERVER-PROMPT.md) ────────

// ErrAgentStreamUnsupported means the server predates GET .../agent/stream;
// callers should fall back to polling DrainPending.
var ErrAgentStreamUnsupported = fmt.Errorf("agent stream not supported by server")

// ErrSessionEnded means the server ended the session (it sent session_ended
// before closing the stream). Reconnecting only gets the same answer again.
var ErrSessionEnded = fmt.Errorf("session ended on the server")

// AgentStream connects to the agent-facing SSE channel and invokes wake for
// every pushed message (the caller then drains the pending queue as the ack).
// Blocks until the stream closes or ctx is done.
func (c *Client) AgentStream(ctx context.Context, id string, wake func(msgType string)) error {
	tok, err := auth.AccessToken()
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", c.Base+"/api/sessions/"+id+"/agent/stream", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "text/event-stream")

	hc := streamClient()
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return ErrAgentStreamUnsupported
	default:
		return fmt.Errorf("agent stream: HTTP %d", resp.StatusCode)
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var msg struct {
			Type string `json:"type"`
		}
		if json.Unmarshal([]byte(strings.TrimSpace(line[5:])), &msg) != nil {
			continue
		}
		switch msg.Type {
		case "viewer_comment", "viewer_decision":
			wake(msg.Type)
		case "session_ended":
			return ErrSessionEnded
		}
	}
	return sc.Err()
}

// ── Hosts: web-spawned sessions ───────────────────────────────────────────────

// HostRegistration is what `live daemon` tells the server about this machine.
// Dirs is the spawn allowlist: the server will not accept a session request for
// any other directory, and the daemon checks it again before spawning.
type HostRegistration struct {
	HostID   string   `json:"host_id"`
	Name     string   `json:"name,omitempty"`
	Hostname string   `json:"hostname,omitempty"`
	OS       string   `json:"os,omitempty"`
	Arch     string   `json:"arch,omitempty"`
	Dirs     []string `json:"dirs"`
	Agents   []string `json:"agents"`
	// Models are the ollama models pulled on this machine. Empty unless ollama
	// is offered. This list is what the spawn path validates against, so it is
	// enumerated locally and never accepted from the server.
	Models []string `json:"models,omitempty"`
	// AgentModels is the per-agent model catalog (LSH-16): what the web may
	// offer for each agent. Built locally; a spawn's model is re-checked
	// against it. A separate field so servers that predate it ignore it.
	AgentModels []AgentModels `json:"agent_models,omitempty"`
	// Version is this daemon's `live` version (LSH-19), so the server and UIs
	// can tell an out-of-date daemon apart. Servers that predate it ignore it.
	Version string `json:"version,omitempty"`
}

// AgentModels lists the models one agent on this machine may be started with.
// Models empty means the agent takes no model from the web (its own default).
type AgentModels struct {
	Name            string        `json:"name"`
	Models          []ModelOption `json:"models"`
	DefaultModel    string        `json:"default_model,omitempty"`
	ModelSwitchable bool          `json:"model_switchable,omitempty"`
}

// ModelOption is one pickable model: the id passed to the agent, and a label.
type ModelOption struct {
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`
}

// SpawnCommand is a request from the web to start a session on this machine.
// It carries an agent NAME, never a command line — the daemon builds that.
type SpawnCommand struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	Agent     string `json:"agent"`
	Cwd       string `json:"cwd"`
	Title     string `json:"title"`
	// Model names the model to run: one this machine registered for Agent in
	// AgentModels, not a free-form argument. The daemon re-checks it (ollama
	// against a fresh `ollama list`) and maps it to the agent's own flag.
	Model string `json:"model,omitempty"`
	// BypassPermissions is the owner's request to run the agent unattended:
	// no web permission probes from live, and the agent's own prompt flag
	// (claude --dangerously-skip-permissions / gemini --yolo) where one exists.
	// It is a boolean the daemon maps to KNOWN flags — never a free-form arg.
	BypassPermissions bool `json:"bypass_permissions,omitempty"`
}

// ErrHostStreamUnsupported means the server predates GET /api/hosts/{id}/stream.
var ErrHostStreamUnsupported = fmt.Errorf("host stream not supported by server")

// RegisterHost registers (or refreshes) this machine as spawnable.
func (c *Client) RegisterHost(reg HostRegistration) error {
	_, err := c.do("POST", "/api/hosts/register", reg, nil)
	return err
}

// HostStream connects to the daemon-facing SSE command channel and invokes
// onSpawn for every spawn command. Blocks until the stream closes or ctx is
// done. A registration that has lapsed server-side surfaces as a 404, which the
// caller handles by re-registering.
func (c *Client) HostStream(ctx context.Context, hostID string, onSpawn func(SpawnCommand)) error {
	tok, err := auth.AccessToken()
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", c.Base+"/api/hosts/"+hostID+"/stream", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "text/event-stream")

	hc := streamClient()
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return ErrHostRegistrationLapsed
	case http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return ErrHostStreamUnsupported
	default:
		return fmt.Errorf("host stream: HTTP %d", resp.StatusCode)
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var cmd SpawnCommand
		if json.Unmarshal([]byte(strings.TrimSpace(line[5:])), &cmd) != nil {
			continue
		}
		// Every typed command goes to the callback; the daemon switches on
		// Type (spawn, set_model, …) so new command kinds need no client change.
		if cmd.Type != "" {
			onSpawn(cmd)
		}
	}
	return sc.Err()
}

// ErrHostRegistrationLapsed means the server has no record of this host (the
// TTL expired while the daemon was disconnected); re-register and reconnect.
var ErrHostRegistrationLapsed = fmt.Errorf("host registration lapsed")
