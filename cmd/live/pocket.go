package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

type pocketMessage struct {
	Role string `json:"role"`
	Text string `json:"text"`
}
type pocketSession struct {
	ID          string             `json:"id"`
	Agent       string             `json:"agent"`
	Model       string             `json:"model,omitempty"`
	Directory   string             `json:"directory"`
	Title       string             `json:"title"`
	Status      string             `json:"status"`
	Messages    []pocketMessage    `json:"messages"`
	Error       string             `json:"error,omitempty"`
	Updated     time.Time          `json:"updated"`
	Permissions []pocketPermission `json:"permissions,omitempty"`
	runContext  context.Context
	cancel      context.CancelFunc
}
type pocketServer struct {
	turns               sync.WaitGroup
	mu                  sync.Mutex
	name, key, stateDir string
	dirs, agents        []string
	models              []string
	discover            func() ([]string, []string)
	sessions            map[string]*pocketSession
	run                 func(context.Context, string, string, string, string) (string, error)
}

func cmdPocketServer(o daemonOpts) int {
	if o.key == "" {
		o.key = os.Getenv("LIVE_POCKET_KEY")
	}
	if o.key == "" {
		fmt.Fprintln(os.Stderr, "live: supply an access key with LIVE_POCKET_KEY or --key")
		return 2
	}
	if (o.tlsCert == "") != (o.tlsKey == "") {
		fmt.Fprintln(os.Stderr, "live: supply both --tls-cert and --tls-key")
		return 2
	}
	if o.listen == "" {
		o.listen = ":8787"
	}
	if len(o.dirs) == 0 {
		wd, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		o.dirs = []string{wd}
	}
	if o.stateDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		o.stateDir = filepath.Join(home, ".liveshortly", "pocket", "liveshortly-pocket")
	}
	if err := os.MkdirAll(o.stateDir, 0700); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	// A lock covers both the session files and the application lifecycle.
	lock, err := os.OpenFile(filepath.Join(o.stateDir, "server.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		fmt.Fprintln(os.Stderr, "live: this state directory already has a running server")
		return 1
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	custom, err := loadPocketAgentCommands(o.agentsFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "live:", err)
		return 2
	}
	name := o.name
	if name == "" {
		name, _ = os.Hostname()
	}
	s := &pocketServer{name: name, key: o.key, stateDir: o.stateDir, dirs: o.dirs, sessions: map[string]*pocketSession{}}
	s.discover = func() ([]string, []string) { return discoverPocketAgents(custom), availableModels() }
	s.agents, s.models = s.discover()
	s.run = func(ctx context.Context, agent, dir, prompt, model string) (string, error) {
		return runPocketAgent(ctx, agent, dir, prompt, model, custom)
	}
	files, err := filepath.Glob(filepath.Join(o.stateDir, "*.json"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		var session pocketSession
		if err != nil || json.Unmarshal(b, &session) != nil || !pocketID.MatchString(session.ID) {
			fmt.Fprintln(os.Stderr, "live: unreadable session file:", f)
			return 1
		}
		session.Permissions = nil // A restarted process cannot resume a native approval.
		if session.Status == "running" {
			session.Status = "interrupted"
			session.Error = "Daemon stopped during this turn. Send a new message to continue."
		}
		s.sessions[session.ID] = &session
	}
	listener, err := net.Listen("tcp", o.listen)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	server := &http.Server{Handler: s, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		s.mu.Lock()
		for _, v := range s.sessions {
			if v.cancel != nil {
				v.cancel()
			}
		}
		s.mu.Unlock()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(shutdown)
		s.turns.Wait()
	}()
	fmt.Fprintf(os.Stderr, "live daemon · %s · listening on %s\nAgents: %s\nWorkspaces: %s\n", s.name, listener.Addr(), strings.Join(s.agents, ", "), strings.Join(o.dirs, ", "))
	if o.tlsCert != "" {
		err = server.ServeTLS(listener, o.tlsCert, o.tlsKey)
	} else {
		err = server.Serve(listener)
	}
	if err == http.ErrServerClosed {
		<-shutdownDone
	}
	if err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

var pocketID = regexp.MustCompile(`^[a-f0-9]{32}$`)

func pocketJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
func pocketFail(w http.ResponseWriter, code int, msg string) {
	pocketJSON(w, code, map[string]string{"error": msg})
}
func pocketDecode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		pocketFail(w, 400, "Invalid JSON request")
		return false
	}
	if d.Decode(&struct{}{}) != io.EOF {
		pocketFail(w, 400, "Expected one JSON object")
		return false
	}
	return true
}
func (s *pocketServer) save(v *pocketSession) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	p := filepath.Join(s.stateDir, v.ID+".json")
	if err = os.WriteFile(p+".tmp", b, 0600); err != nil {
		return err
	}
	return os.Rename(p+".tmp", p)
}

type pocketAgentStatus struct {
	ID             string `json:"id"`
	Approvals      string `json:"approvals"`
	Installed      bool   `json:"installed"`
	ActiveSessions int    `json:"activeSessions"`
	LastStatus     string `json:"lastStatus,omitempty"`
	LastError      string `json:"lastError,omitempty"`
}

// Called under the server lock. Installation and past runs are reported
// separately: discovery cannot prove a provider login or remaining quota.
func (s *pocketServer) agentStatuses() []pocketAgentStatus {
	result := make([]pocketAgentStatus, 0, len(s.agents))
	for _, agent := range s.agents {
		status := pocketAgentStatus{ID: agent, Installed: contains(s.agents, agent), Approvals: "unsupported"}
		if agent == "codex" || agent == "claude" || agent == "agy" || agent == "antigravity" {
			status.Approvals = "supported"
		}
		if agent == "ollama" {
			status.Approvals = "not-needed"
		}
		var latest time.Time
		for _, session := range s.sessions {
			if session.Agent != agent {
				continue
			}
			if session.Status == "running" {
				status.ActiveSessions++
			}
			if len(session.Messages) > 0 && session.Status != "running" && session.Updated.After(latest) {
				latest = session.Updated
				status.LastStatus = session.Status
				status.LastError = session.Error
			}
		}
		result = append(result, status)
	}
	return result
}

func (s *pocketServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	got := sha256.Sum256([]byte(r.Header.Get("Authorization")))
	want := sha256.Sum256([]byte("Bearer " + s.key))
	if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
		pocketFail(w, 401, "Access key is incorrect")
		return
	}
	// Refresh executable/model discovery on each connection test, before locking.
	if r.URL.Path == "/v1/info" && r.Method == "GET" && s.discover != nil {
		agents, models := s.discover()
		s.mu.Lock()
		s.agents = agents
		s.models = models
		s.mu.Unlock()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := strings.TrimSuffix(r.URL.Path, "/")
	if path == "/v1/info" && r.Method == "GET" {
		pocketJSON(w, 200, map[string]any{"name": s.name, "models": s.models, "agents": s.agents, "directories": s.dirs, "protocolVersion": 1, "agentStatuses": s.agentStatuses()})
		return
	}
	if path == "/v1/sessions" {
		switch r.Method {
		case "GET":
			list := make([]*pocketSession, 0, len(s.sessions))
			for _, v := range s.sessions {
				summary := *v
				summary.Messages = []pocketMessage{}
				list = append(list, &summary)
			}
			sort.Slice(list, func(i, j int) bool { return list[i].Updated.After(list[j].Updated) })
			pocketJSON(w, 200, list)
			return
		case "POST":
			var in struct {
				Agent     string `json:"agent"`
				Model     string `json:"model"`
				Directory string `json:"directory"`
			}
			if !pocketDecode(w, r, &in) {
				return
			}
			if !contains(s.agents, in.Agent) || !contains(s.dirs, in.Directory) {
				pocketFail(w, 400, "Choose an advertised agent and workspace")
				return
			}
			if in.Agent == "ollama" && (!validModelName(in.Model) || !contains(s.models, in.Model)) {
				pocketFail(w, 400, "Choose an installed Ollama model")
				return
			}
			if len(s.sessions) >= 200 {
				pocketFail(w, 409, "Session limit reached; delete an old session")
				return
			}
			raw := make([]byte, 16)
			if _, err := rand.Read(raw); err != nil {
				pocketFail(w, 500, "Cannot generate session ID")
				return
			}
			v := &pocketSession{ID: hex.EncodeToString(raw), Agent: in.Agent, Model: in.Model, Directory: in.Directory, Title: "New session", Status: "idle", Messages: []pocketMessage{}, Updated: time.Now()}
			if s.save(v) != nil {
				pocketFail(w, 500, "Cannot save session")
				return
			}
			s.sessions[v.ID] = v
			pocketJSON(w, 201, v)
			return
		}
	}
	parts := strings.Split(strings.TrimPrefix(path, "/v1/sessions/"), "/")
	if !strings.HasPrefix(path, "/v1/sessions/") || len(parts) > 3 {
		pocketFail(w, 404, "Unknown endpoint")
		return
	}
	v := s.sessions[parts[0]]
	if v == nil {
		pocketFail(w, 404, "Session not found")
		return
	}
	if len(parts) == 3 && parts[1] == "permissions" && r.Method == "POST" {
		s.answerPocketPermission(w, r, v, parts[2])
		return
	}
	if len(parts) == 1 {
		if r.Method == "GET" {
			pocketJSON(w, 200, v)
			return
		}
		if r.Method == "DELETE" {
			if v.Status == "running" {
				pocketFail(w, 409, "Stop the session first")
				return
			}
			if err := os.Remove(filepath.Join(s.stateDir, v.ID+".json")); err != nil {
				pocketFail(w, 500, "Cannot delete session")
				return
			}
			delete(s.sessions, v.ID)
			pocketJSON(w, 200, map[string]bool{"deleted": true})
			return
		}
	}
	if len(parts) == 2 && parts[1] == "stop" && r.Method == "POST" {
		if v.cancel != nil {
			v.cancel()
		}
		pocketJSON(w, 202, v)
		return
	}
	if len(parts) == 2 && parts[1] == "messages" && r.Method == "POST" {
		var in struct {
			Text string `json:"text"`
		}
		if !pocketDecode(w, r, &in) {
			return
		}
		if strings.TrimSpace(in.Text) == "" || len(in.Text) > 32000 {
			pocketFail(w, 400, "Message must contain 1–32000 bytes")
			return
		}
		if v.Status == "running" {
			pocketFail(w, 409, "This session already has an active turn")
			return
		}
		active := 0
		for _, other := range s.sessions {
			if other.Status == "running" {
				active++
			}
		}
		if active >= 4 {
			pocketFail(w, 429, "Four turns are already running; try again shortly")
			return
		}
		if !contains(s.dirs, v.Directory) || !contains(s.agents, v.Agent) {
			pocketFail(w, 409, "Session workspace or agent is no longer offered")
			return
		}
		if len(v.Messages) >= 100 {
			pocketFail(w, 409, "Start a new session after 50 turns")
			return
		}
		previous := *v
		v.Messages = append(v.Messages, pocketMessage{"user", in.Text})
		v.Status = "running"
		v.Error = ""
		v.Updated = time.Now()
		if len(v.Messages) == 1 {
			v.Title = string([]rune(in.Text)[:min(60, len([]rune(in.Text)))])
		}
		if s.save(v) != nil {
			*v = previous
			pocketFail(w, 500, "Cannot save turn")
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		v.cancel = cancel
		v.runContext = ctx
		var prompt strings.Builder
		prompt.WriteString("Continue this conversation. Prior messages are context; respond to the final user message.\n")
		// Bound the context passed to the CLI, retaining the most recent complete messages.
		start := len(v.Messages)
		size := 0
		for start > 0 && size+len(v.Messages[start-1].Text) < 96000 {
			start--
			size += len(v.Messages[start].Text)
		}
		for _, m := range v.Messages[start:] {
			fmt.Fprintf(&prompt, "\n%s:\n%s\n", m.Role, m.Text)
		}
		s.turns.Add(1)
		go s.turn(ctx, cancel, v.ID, v.Agent, v.Directory, prompt.String(), v.Model)
		pocketJSON(w, 202, v)
		return
	}
	pocketFail(w, 405, "Method not allowed")
}
func (s *pocketServer) turn(ctx context.Context, cancel context.CancelFunc, id, agent, dir, prompt, model string) {
	defer s.turns.Done()
	defer cancel()
	ctx = context.WithValue(ctx, pocketPermissionKey{}, pocketPermissionAsk(func(tool, detail string) string {
		return s.requestPocketPermission(ctx, id, tool, detail)
	}))
	output, err := s.run(ctx, agent, dir, prompt, model)
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.sessions[id]
	v.cancel = nil
	v.runContext = nil
	v.Permissions = nil
	v.Status = "idle"
	v.Updated = time.Now()
	if err != nil {
		v.Status = "failed"
		v.Error = err.Error()
		if ctx.Err() != nil {
			v.Status = "interrupted"
			v.Error = "Turn stopped or timed out."
		}
	} else if strings.TrimSpace(output) == "" {
		v.Status = "failed"
		v.Error = "Agent returned no text. Check its login on the server."
	} else {
		v.Messages = append(v.Messages, pocketMessage{"assistant", output})
	}
	if err := s.save(v); err != nil {
		v.Status = "failed"
		v.Error = "Could not save the completed turn."
	}
}

// CLI subprocesses use argv and stdin, never shell interpolation. Agent permission
// checks remain native for Codex/Claude; AGY delegates to the fail-closed hook
// gate just as in runHooked. The shared key grants access as the daemon's OS user.
func runPocketAgent(ctx context.Context, agent, dir, prompt, model string, custom map[string]pocketAgentCommand) (string, error) {
	var args []string
	command := agent
	switch agent {
	case "codex":
		return runPocketCodex(ctx, dir, prompt)
	case "claude":
		args = []string{"--print", "--output-format", "text"}
	case "gemini":
		args = []string{"--prompt", "Continue the conversation provided on stdin.", "--output-format", "text", "--approval-mode", "default"}
	case "agy", "antigravity":
		args = []string{"--output-format", "stream-json", "--print=" + prompt}
	case "ollama":
		if !validModelName(model) {
			return "", fmt.Errorf("Select an installed Ollama model")
		}
		// Recheck before running so a removed model is never pulled implicitly.
		if !contains(availableModels(), model) {
			return "", fmt.Errorf("Ollama model is no longer installed; refresh the connection")
		}
		args = []string{"run", model, "--nowordwrap"}
	case "cursor-agent":
		args = []string{"--print", "--output-format", "text", "--", prompt}
	case "aider":
		f, err := os.CreateTemp("", "live-pocket-prompt-*")
		if err != nil {
			return "", err
		}
		defer os.Remove(f.Name())
		if _, err = f.WriteString(prompt); err != nil {
			f.Close()
			return "", err
		}
		f.Close()
		args = []string{"--message-file", f.Name(), "--no-auto-commits", "--no-stream", "--no-pretty"}
	default:
		adapter, ok := custom[agent]
		if !ok {
			return "", fmt.Errorf("Unsupported agent")
		}
		command = adapter.Command
		for _, arg := range adapter.Args {
			args = append(args, strings.ReplaceAll(arg, "{prompt}", prompt))
		}
	}
	extraEnv := []string{}
	if agent == "claude" || agent == "agy" || agent == "antigravity" {
		env, settings, cleanup, err := pocketHookBridge(ctx, agent)
		if err != nil {
			return "", err
		}
		defer cleanup()
		extraEnv = env
		if agent == "claude" {
			args = append(args, "--settings", settings)
		} else {
			// Match runHooked/runInTmux: AGY delegates its terminal prompts to
			// our PreToolUse gate. Only do this after installing/enabling the
			// hook; every tool still waits for an explicit remote verdict.
			args = append([]string{"--dangerously-skip-permissions"}, args...)
		}
	}
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir = dir
	if agent != "agy" && agent != "antigravity" && agent != "cursor-agent" {
		cmd.Stdin = strings.NewReader(prompt)
	}
	cmd.Env = append(envWithout(os.Environ(), "LIVE_POCKET_KEY", "CLAUDECODE", "LIVE_SIDECAR", "LIVE_TOKEN", "LIVE_POCKET_HOOK", "LIVE_HOOK_TIMEOUT"), extraEnv...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	out := &pocketBuffer{}
	stderr := &pocketBuffer{}
	agyOutput := &pocketAgyOutput{}
	cmd.Stdout = out
	if agent == "agy" || agent == "antigravity" {
		cmd.Stdout = agyOutput
	}
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.b.String())
		if detail == "" {
			detail = strings.TrimSpace(out.b.String())
		}
		if len(detail) > 4000 {
			detail = detail[:4000]
		}
		return "", fmt.Errorf("%s exited unsuccessfully. Check CLI authentication and permissions on the server. %s", agent, detail)
	}
	if agent == "agy" || agent == "antigravity" {
		return agyOutput.answer()
	}

	return out.b.String(), nil
}

type pocketBuffer struct{ b strings.Builder }

func (b *pocketBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := (256 << 10) - b.b.Len()
	if remaining > 0 {
		b.b.Write(p[:min(remaining, n)])
	}
	return n, nil
}
