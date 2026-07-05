// Package api is the LiveShortly REST/SSE client used by the shim. All
// endpoints and payload shapes follow CONTRACT.md in the LiveShortly repo.
package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/resapce/live/internal/auth"
	"github.com/resapce/live/internal/hostcfg"
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

// ReportModel sets the session's model label once it is known.
func (c *Client) ReportModel(id, model string) error {
	_, err := c.do("PATCH", "/api/sessions/"+id, map[string]string{"model": model}, nil)
	return err
}

// EmitEvent appends one event to the session feed.
func (c *Client) EmitEvent(id, eventType string, payload map[string]any) error {
	_, err := c.do("POST", "/api/sessions/"+id+"/events", map[string]any{
		"event_type": eventType,
		"payload":    payload,
		"actor":      "agent",
	}, nil)
	return err
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
	if out.Decision == "allow" || out.Decision == "deny" {
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
