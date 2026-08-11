package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/liveshortly/live/internal/auth"
)

// Session is a row from GET /api/sessions (and, with the extra detail fields
// populated, from GET /api/sessions/{id}). Nullable JSON fields decode to the
// zero value — encoding/json treats null as a no-op — so plain types are safe.
type Session struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	OwnerHandle string   `json:"owner_handle"`
	Model       string   `json:"model"`
	Framework   string   `json:"framework"`
	Agent       string   `json:"agent"`
	CaptureMode string   `json:"capture_mode"`
	Status      string   `json:"status"`
	Tags        []string `json:"tags"`
	Visibility  string   `json:"visibility"`
	LinkRole    string   `json:"link_role"`

	EventCount   int       `json:"event_count"`
	ViewCount    int       `json:"view_count"`
	BytesUsed    int64     `json:"bytes_used"`
	InputTokens  int64     `json:"input_tokens"`
	OutputTokens int64     `json:"output_tokens"`
	CreatedAt    time.Time `json:"created_at"`
	EndedAt      time.Time `json:"ended_at"`
	EndedReason  string    `json:"ended_reason"`

	ClientHandle string `json:"client_handle"`
	GitRemote    string `json:"git_remote"`
	GitBranch    string `json:"git_branch"`

	ShareCount int    `json:"share_count"`
	SharedRole string `json:"shared_role"` // set only on rows reached via a share
	ForkCount  int    `json:"fork_count"`

	// Detail-only (GET /api/sessions/{id}).
	AgentConnected bool        `json:"agent_connected"`
	WatcherCount   int         `json:"watcher_count"`
	ForkerCount    int         `json:"forker_count"`
	ForkedFrom     *ForkSource `json:"forked_from"`
	Events         []Event     `json:"events"`
}

// ForkSource is the light reference to a session's fork origin.
type ForkSource struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	OwnerHandle string `json:"owner_handle"`
	Seq         int    `json:"seq"`
}

// Live reports whether the session is still running.
func (s Session) Live() bool { return s.Status == "live" }

// Event is one entry in a session feed.
type Event struct {
	ID        string         `json:"id"`
	SessionID string         `json:"session_id"`
	Seq       int            `json:"seq"`
	Actor     string         `json:"actor"`
	EventType string         `json:"event_type"`
	Payload   map[string]any `json:"payload"`
	TS        time.Time      `json:"ts"`
}

// Str returns a string field of the payload, or "".
func (e Event) Str(keys ...string) string {
	for _, k := range keys {
		if v, ok := e.Payload[k].(string); ok && strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// ErrNoAccess is a 403 or 404 on a session the caller can't reach.
var ErrNoAccess = fmt.Errorf("no access to that session")

// ListOpts filters GET /api/sessions. Scope is mine|shared|all, Status is
// live|ended|all; empty means the server default.
type ListOpts struct {
	Scope  string
	Status string
	Query  string
	Limit  int
	Offset int
}

// ListSessions returns the caller's sessions (owned and/or shared) plus the
// total matching count, which may exceed len(results) when Limit truncates.
func (c *Client) ListSessions(o ListOpts) ([]Session, int, error) {
	q := url.Values{}
	for k, v := range map[string]string{"scope": o.Scope, "status": o.Status, "q": o.Query} {
		if v != "" {
			q.Set(k, v)
		}
	}
	if o.Limit > 0 {
		q.Set("limit", strconv.Itoa(o.Limit))
	}
	if o.Offset > 0 {
		q.Set("offset", strconv.Itoa(o.Offset))
	}
	var out struct {
		Results []Session `json:"results"`
		Total   int       `json:"total"`
	}
	if _, err := c.do("GET", "/api/sessions?"+q.Encode(), nil, &out); err != nil {
		return nil, 0, err
	}
	return out.Results, out.Total, nil
}

// GetSession returns one session with its full event log. Bumps view_count
// server-side, like any other read.
func (c *Client) GetSession(id string) (Session, error) {
	var s Session
	status, err := c.do("GET", "/api/sessions/"+id, nil, &s)
	if status == http.StatusForbidden || status == http.StatusNotFound {
		return Session{}, ErrNoAccess
	}
	return s, err
}

// PostComment sends a viewer message into a LIVE session — the same path the
// web composer uses, so it reaches the running agent. A 409 means the session
// already ended; 403 means the caller has read but not comment access.
func (c *Client) PostComment(id, message string) error {
	status, err := c.do("POST", "/api/sessions/"+id+"/comments",
		map[string]string{"message": message}, nil)
	switch status {
	case http.StatusForbidden:
		return fmt.Errorf("you have read access but not comment access to this session")
	case http.StatusConflict:
		return fmt.Errorf("session is no longer live")
	case http.StatusNotFound:
		return ErrNoAccess
	}
	return err
}

// PostDecision answers a live permission prompt — "allow", "deny", or "always".
// It is the same endpoint the web viewer uses and carries the same
// authorization (comment-level), so anyone who can talk to the session can
// unblock it. The running shim polls for this every second, so a waiting agent
// resumes as soon as it lands.
func (c *Client) PostDecision(id, decision string) error {
	switch decision {
	case "allow", "deny", "always":
	default:
		return fmt.Errorf("decision must be allow, deny, or always")
	}
	status, err := c.do("POST", "/api/sessions/"+id+"/decision",
		map[string]string{"decision": decision}, nil)
	switch status {
	case http.StatusForbidden:
		return fmt.Errorf("you have read access but not comment access to this session")
	case http.StatusConflict:
		return fmt.Errorf("session is no longer live")
	case http.StatusNotFound:
		return ErrNoAccess
	}
	return err
}

// CreateShare grants another account access to a session (owner only).
// role is "viewer" or "commenter" — a CLI joiner needs "commenter" to talk back.
func (c *Client) CreateShare(id, email, role string) error {
	status, err := c.do("POST", "/api/sessions/"+id+"/shares",
		map[string]string{"email": email, "role": role}, nil)
	if status == http.StatusForbidden || status == http.StatusNotFound {
		return ErrNoAccess
	}
	return err
}

// Share is one access grant on a session.
type Share struct {
	ID        string    `json:"id"`
	Email     string    `json:"grantee_email"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

// ListShares returns a session's grants (owner only).
func (c *Client) ListShares(id string) ([]Share, error) {
	var out struct {
		Shares []Share `json:"results"`
	}
	status, err := c.do("GET", "/api/sessions/"+id+"/shares", nil, &out)
	if status == http.StatusForbidden || status == http.StatusNotFound {
		return nil, ErrNoAccess
	}
	return out.Shares, err
}

// Host is one of the caller's machines currently running `live daemon`.
type Host struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Hostname string   `json:"hostname"`
	OS       string   `json:"os"`
	Arch     string   `json:"arch"`
	Dirs     []string `json:"dirs"`
	Agents   []string `json:"agents"`
	Models   []string `json:"models"`
	SeenAt   string   `json:"seen_at"`
}

// ListHosts returns the caller's ONLINE machines.
func (c *Client) ListHosts() ([]Host, error) {
	var out struct {
		Hosts []Host `json:"hosts"`
	}
	if _, err := c.do("GET", "/api/hosts", nil, &out); err != nil {
		return nil, err
	}
	return out.Hosts, nil
}

// ── Viewer stream ─────────────────────────────────────────────────────────────

// StreamFrame is one decoded frame of the viewer SSE stream. Control frames
// ("connected", "watchers", "session_ended") carry Type; everything else is a
// session Event, which has no Type field.
type StreamFrame struct {
	Type     string `json:"type"`
	Count    int    `json:"count"`  // watchers
	Status   string `json:"status"` // connected
	Event    Event  // decoded when Type == ""
	RawEvent bool
}

// ViewerStream consumes GET /api/sessions/{id}/stream — the same channel the
// web viewer uses: buffered replay first, then live events, then
// "session_ended". Blocks until the stream closes or ctx is done. onFrame runs
// on the reader goroutine; keep it quick.
func (c *Client) ViewerStream(ctx context.Context, id string, onFrame func(StreamFrame)) error {
	tok, err := auth.AccessToken()
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", c.Base+"/api/sessions/"+id+"/stream", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := (&http.Client{}).Do(req) // no timeout: long-lived stream
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusForbidden, http.StatusNotFound:
		return ErrNoAccess
	default:
		return fmt.Errorf("viewer stream: HTTP %d", resp.StatusCode)
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue // ": ping" heartbeats and blank separators
		}
		data := strings.TrimSpace(line[5:])
		var f StreamFrame
		if json.Unmarshal([]byte(data), &f) != nil {
			continue
		}
		if f.Type == "" {
			if json.Unmarshal([]byte(data), &f.Event) != nil {
				continue
			}
			f.RawEvent = true
		}
		onFrame(f)
		if f.Type == "session_ended" {
			return nil
		}
	}
	return sc.Err()
}
