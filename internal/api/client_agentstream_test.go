package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The agent stream must route viewer frames to wake and control commands
// (set_model, abort, and any future type) to onCmd, and surface session_ended.
func TestAgentStreamDispatch(t *testing.T) {
	withFakeCreds(t)

	frames := []string{
		`{"type":"connected","session_id":"s1"}`,
		`{"type":"viewer_comment","comment":{"username":"a","message":"hi"}}`,
		`{"type":"set_model","session_id":"s1","model":"openai/gpt-5"}`,
		`{"type":"abort","session_id":"s1"}`,
		`{"type":"future_cmd","blob":"x"}`, // unknown control type: still reaches onCmd
		`{"type":"session_ended","session_id":"s1"}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		for _, f := range frames {
			fmt.Fprintf(w, "data: %s\n\n", f)
			fl.Flush()
		}
	}))
	defer srv.Close()

	var wakes []string
	var cmds []AgentCommand
	c := &Client{Base: srv.URL, hc: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err := c.AgentStream(ctx, "s1",
		func(t string) { wakes = append(wakes, t) },
		func(cmd AgentCommand) { cmds = append(cmds, cmd) },
	)
	if !errors.Is(err, ErrSessionEnded) {
		t.Fatalf("err = %v, want ErrSessionEnded", err)
	}

	if len(wakes) != 1 || wakes[0] != "viewer_comment" {
		t.Fatalf("wakes = %v, want [viewer_comment]", wakes)
	}
	// set_model, abort, future_cmd — connected/viewer_comment/session_ended are not commands.
	if len(cmds) != 3 {
		t.Fatalf("cmds = %+v, want 3 (set_model, abort, future_cmd)", cmds)
	}
	if cmds[0].Type != "set_model" || cmds[0].Model != "openai/gpt-5" {
		t.Errorf("cmds[0] = %+v, want set_model openai/gpt-5", cmds[0])
	}
	if cmds[1].Type != "abort" {
		t.Errorf("cmds[1] = %+v, want abort", cmds[1])
	}
	if cmds[2].Type != "future_cmd" {
		t.Errorf("cmds[2] = %+v, want future_cmd (pluggable)", cmds[2])
	}
}

// onCmd may be nil without panicking (agents that take commands another way).
func TestAgentStreamNilOnCmd(t *testing.T) {
	withFakeCreds(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"type\":\"set_model\",\"model\":\"x/y\"}\n\n")
		fl.Flush()
		fmt.Fprint(w, "data: {\"type\":\"session_ended\"}\n\n")
		fl.Flush()
	}))
	defer srv.Close()

	c := &Client{Base: srv.URL, hc: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := c.AgentStream(ctx, "s1", func(string) {}, nil); !errors.Is(err, ErrSessionEnded) {
		t.Fatalf("err = %v, want ErrSessionEnded", err)
	}
}
