package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testPocket(t *testing.T) *pocketServer {
	t.Helper()
	return &pocketServer{name: "test", key: "k", stateDir: t.TempDir(), dirs: []string{"/workspace"}, agents: []string{"codex", "claude", "gemini"}, sessions: map[string]*pocketSession{}, run: func(ctx context.Context, a, d, p, model string) (string, error) { return "answer", nil }}
}
func pocketRequest(s *pocketServer, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+s.key)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func createPocket(t *testing.T, s *pocketServer) pocketSession {
	t.Helper()
	w := pocketRequest(s, "POST", "/v1/sessions", `{"agent":"codex","directory":"/workspace"}`)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var v pocketSession
	json.Unmarshal(w.Body.Bytes(), &v)
	return v
}
func TestPocketAuthAndAllowlists(t *testing.T) {
	s := testPocket(t)
	for _, path := range []string{"/v1/info", "/v1/sessions", "/v1/sessions/anything/stop"} {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 401 {
			t.Fatal(path, w.Code)
		}
	}
	r := httptest.NewRequest("GET", "/v1/info", nil)
	r.Header.Set("Authorization", "Bearer "+s.key)
	r.Header.Set("X-Liveshortly-Application", "other")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	// Application names no longer participate in authentication.
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := pocketRequest(s, "GET", "/v1/info", ""); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	for _, body := range []string{`{"agent":"sh","directory":"/workspace"}`, `{"agent":"codex","directory":"/workspace/../etc"}`, `{"agent":"codex","directory":"/workspace","args":["--unsafe"]}`, `{} {}`} {
		if w := pocketRequest(s, "POST", "/v1/sessions", body); w.Code != 400 {
			t.Fatal(body, w.Code)
		}
	}
}
func TestPocketLifecycleAndCancellation(t *testing.T) {
	s := testPocket(t)
	started := make(chan string, 1)
	s.run = func(ctx context.Context, a, d, p, model string) (string, error) {
		started <- p
		<-ctx.Done()
		return "", ctx.Err()
	}
	v := createPocket(t, s)
	base := "/v1/sessions/" + v.ID
	if w := pocketRequest(s, "POST", base+"/messages", `{"text":"hello"}`); w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	select {
	case p := <-started:
		if !strings.Contains(p, "hello") {
			t.Fatal(p)
		}
	case <-time.After(time.Second):
		t.Fatal("not started")
	}
	if w := pocketRequest(s, "POST", base+"/messages", `{"text":"duplicate"}`); w.Code != 409 {
		t.Fatal(w.Code)
	}
	if w := pocketRequest(s, "DELETE", base, ""); w.Code != 409 {
		t.Fatal(w.Code)
	}
	if w := pocketRequest(s, "POST", base+"/stop", ""); w.Code != 202 {
		t.Fatal(w.Code)
	}
	for i := 0; i < 100; i++ {
		w := pocketRequest(s, "GET", base, "")
		json.Unmarshal(w.Body.Bytes(), &v)
		if v.Status == "interrupted" {
			break
		}
		time.Sleep(time.Millisecond * 10)
	}
	if v.Status != "interrupted" {
		t.Fatal(v.Status)
	}
	b, err := os.ReadFile(filepath.Join(s.stateDir, v.ID+".json"))
	if err != nil || !strings.Contains(string(b), "interrupted") {
		t.Fatal(string(b), err)
	}
	if w := pocketRequest(s, "DELETE", base, ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := pocketRequest(s, "GET", base, ""); w.Code != 404 {
		t.Fatal(w.Code)
	}
}
func TestPocketConversationAndPersistenceFailure(t *testing.T) {
	s := testPocket(t)
	prompts := make(chan string, 2)
	s.run = func(ctx context.Context, a, d, p, model string) (string, error) {
		prompts <- p
		return "remembered answer", nil
	}
	v := createPocket(t, s)
	base := "/v1/sessions/" + v.ID
	for _, msg := range []string{"first", "second"} {
		w := pocketRequest(s, "POST", base+"/messages", `{"text":"`+msg+`"}`)
		if w.Code != 202 {
			t.Fatal(w.Body.String())
		}
		for i := 0; i < 100; i++ {
			w = pocketRequest(s, "GET", base, "")
			json.Unmarshal(w.Body.Bytes(), &v)
			if v.Status != "running" {
				break
			}
			time.Sleep(time.Millisecond * 10)
		}
		if v.Status != "idle" {
			t.Fatal(v.Status)
		}
	}
	<-prompts
	p := <-prompts
	if !strings.Contains(p, "first") || !strings.Contains(p, "remembered answer") || !strings.Contains(p, "second") {
		t.Fatal(p)
	}
	if len(v.Messages) != 4 {
		t.Fatal(v.Messages)
	}
	s.stateDir = filepath.Join(s.stateDir, "missing")
	if w := pocketRequest(s, "POST", base+"/messages", `{"text":"not persisted"}`); w.Code != 500 {
		t.Fatal(w.Code)
	}
	w := pocketRequest(s, "GET", base, "")
	json.Unmarshal(w.Body.Bytes(), &v)
	if len(v.Messages) != 4 || v.Status != "idle" {
		t.Fatal(v)
	}
}
func TestPocketBodyLimit(t *testing.T) {
	s := testPocket(t)
	v := createPocket(t, s)
	w := pocketRequest(s, http.MethodPost, "/v1/sessions/"+v.ID+"/messages", `{"text":"`+strings.Repeat("x", 140000)+`"}`)
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
}

func TestPocketAgentStatusSeparatesInstallationFromRuns(t *testing.T) {
	s := testPocket(t)
	s.agents = []string{"codex", "claude"}
	s.sessions["one"] = &pocketSession{Agent: "codex", Status: "running"}
	s.sessions["two"] = &pocketSession{Agent: "claude", Status: "failed", Error: "Provider quota exceeded", Messages: []pocketMessage{{Role: "user", Text: "hello"}}, Updated: time.Now()}
	w := pocketRequest(s, "GET", "/v1/info", "")
	var response struct {
		Statuses []pocketAgentStatus `json:"agentStatuses"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Statuses) != 2 || response.Statuses[0].ActiveSessions != 1 || response.Statuses[0].LastStatus != "" || response.Statuses[1].LastError != "Provider quota exceeded" {
		t.Fatal(response)
	}
}

func TestPocketOllamaModelSelection(t *testing.T) {
	s := testPocket(t)
	s.agents = []string{"ollama", "agy"}
	s.models = []string{"tiny:latest"}
	for _, model := range []string{"", "not-installed"} {
		body := fmt.Sprintf(`{"agent":"ollama","directory":"/workspace","model":%q}`, model)
		if w := pocketRequest(s, "POST", "/v1/sessions", body); w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	w := pocketRequest(s, "POST", "/v1/sessions", `{"agent":"ollama","directory":"/workspace","model":"tiny:latest"}`)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var session pocketSession
	json.Unmarshal(w.Body.Bytes(), &session)
	if session.Model != "tiny:latest" {
		t.Fatal(session)
	}
	captured := make(chan string, 1)
	s.run = func(ctx context.Context, a, d, p, model string) (string, error) {
		captured <- model
		return "done", nil
	}
	if w := pocketRequest(s, "POST", "/v1/sessions/"+session.ID+"/messages", `{"text":"hello"}`); w.Code != 202 {
		t.Fatal(w.Code)
	}
	select {
	case model := <-captured:
		if model != "tiny:latest" {
			t.Fatal(model)
		}
	case <-time.After(time.Second):
		t.Fatal("no run")
	}
	// Wait for the worker to persist before TempDir cleanup.
	for i := 0; i < 100; i++ {
		s.mu.Lock()
		running := s.sessions[session.ID].Status == "running"
		s.mu.Unlock()
		if !running {
			return
		}
		time.Sleep(time.Millisecond * 10)
	}
	t.Fatal("turn did not finish")
}

func TestPocketDiscoveryAndCustomAdapters(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	for _, name := range []string{"codex", "agy", "ollama", "custom-cli"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n/bin/cat\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	config := filepath.Join(dir, "agents.json")
	os.WriteFile(config, []byte(`[{"name":"my-agent","command":"custom-cli","args":[]}]`), 0600)
	adapters, err := loadPocketAgentCommands(config)
	if err != nil {
		t.Fatal(err)
	}
	agents := discoverPocketAgents(adapters)
	if strings.Join(agents, ",") != "codex,agy,ollama,my-agent" {
		t.Fatal(agents)
	}
	out, err := runPocketAgent(context.Background(), "my-agent", dir, "literal ; $(not-a-command)", "", adapters)
	if err != nil || out != "literal ; $(not-a-command)" {
		t.Fatal(out, err)
	}
}
