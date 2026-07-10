// Package api is the LiveShortly REST/SSE client used by the shim. All
// endpoints and payload shapes follow CONTRACT.md in the LiveShortly repo.
package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/liveshortly/live/internal/auth"
	"github.com/liveshortly/live/internal/hostcfg"
)

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

	hc := &http.Client{} // no timeout: long-lived stream
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
			return nil
		}
	}
	return sc.Err()
}
