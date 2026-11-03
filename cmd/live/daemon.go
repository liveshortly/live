package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/liveshortly/live/internal/api"
	"github.com/liveshortly/live/internal/auth"
	"github.com/liveshortly/live/internal/harness"
	"github.com/liveshortly/live/internal/hostcfg"
)

// `live daemon` makes this machine spawnable from the browser: it registers the
// directories and agents it is willing to run, holds the server's command
// stream open, and starts a session when the web asks for one. The session that
// results is identical to one started by typing `live claude` — same hooks,
// same permission gating, same feed.
//
// SECURITY — a spawn command is remote code execution on this machine, so the
// daemon trusts the server for NOTHING beyond "the owner asked for this":
//
//   - The command line is built here. The server sends an agent NAME; anything
//     this process did not itself register and resolve on this machine's PATH
//     is dropped (see agents.go for how that set is built).
//   - The directory must be one THIS process registered at startup. The server
//     checks too, but that check is a courtesy — this one is the defense.
//   - ollama additionally carries a MODEL name, because `ollama run` is
//     meaningless without one. It is treated exactly like the directory: it
//     must be one THIS process enumerated from the local `ollama list`. It is
//     never a free-form argument, so the server still cannot compose a command
//     line — it can only choose from what this machine already has.
//   - Nothing from the command is ever passed to a shell.
//
// The blast radius is therefore exactly: run one of the agents THIS machine
// registered at startup, in one of the directories the operator named on the
// command line, and for ollama against a model already pulled on this machine.
// Widening the set of agent names (agents.json) does not widen that radius —
// the operator declares the set, exactly as they declare --dir.

const daemonUsage = `live daemon — let this machine start sessions from the web

Usage:
  live daemon [--dir <path>]... [--name <label>]

  --dir <path>    A directory sessions may be started in (repeatable).
                  Defaults to the current directory. This list is the ONLY
                  place a web-started session can run — nothing else on this
                  machine is reachable.
  --name <label>  How this machine appears in the picker (default: hostname).
  --agents-file   Extra agent harnesses this machine can run, as JSON
                  (default: ~/.liveshortly/agents.json, optional).

Agents: every harness on PATH that live knows by name is offered automatically.
For anything else — a different name, a path, a fixed argv prefix — list it:

  [{"name": "hermes"},
   {"name": "pi", "command": "pi.dev", "args": ["chat"]}]

Standalone Pocket server (no web login required):
  live daemon --server --key <key> --dir /path/to/project
  Set LIVE_POCKET_KEY or use --key. Any non-empty key is accepted.
  --listen defaults to :8787 (all interfaces).
  --state-dir stores sessions (default ~/.liveshortly/pocket/liveshortly-pocket).
  --agents-file loads additional local CLI adapters from a JSON file.
  --tls-cert and --tls-key enable HTTPS. Use HTTPS for internet access.

Leave it running; open the site and hit "+ New session".
`

type daemonOpts struct {
	dirs                                               []string
	name                                               string
	server                                             bool
	key, listen, stateDir, tlsCert, tlsKey, agentsFile string
}

func parseDaemonFlags(args []string) (daemonOpts, error) {
	var o daemonOpts
	for i := 0; i < len(args); {
		switch args[i] {
		case "--server":
			o.server = true
			i++
		case "--agents-file", "--key", "--listen", "--state-dir", "--tls-cert", "--tls-key":
			if i+1 >= len(args) {
				return o, fmt.Errorf("%s needs a value", args[i])
			}
			value := args[i+1]
			switch args[i] {
			case "--agents-file":
				o.agentsFile = value
			case "--key":
				o.key = value
			case "--listen":
				o.listen = value
			case "--state-dir":
				o.stateDir = value
			case "--tls-cert":
				o.tlsCert = value
			case "--tls-key":
				o.tlsKey = value
			}
			i += 2
		case "--dir":
			if i+1 >= len(args) {
				return o, fmt.Errorf("--dir needs a value")
			}
			abs, err := filepath.Abs(args[i+1])
			if err != nil {
				return o, fmt.Errorf("--dir %s: %w", args[i+1], err)
			}
			st, err := os.Stat(abs)
			if err != nil || !st.IsDir() {
				return o, fmt.Errorf("--dir %s: not a directory", args[i+1])
			}
			o.dirs = append(o.dirs, filepath.Clean(abs))
			i += 2
		case "--name":
			if i+1 >= len(args) {
				return o, fmt.Errorf("--name needs a value")
			}
			o.name = args[i+1]
			i += 2
		case "--help", "-h":
			return o, flagHelp
		default:
			return o, fmt.Errorf("unknown flag: %s", args[i])
		}
	}
	return o, nil
}

// flagHelp is a sentinel: the caller prints usage and exits 0.
var flagHelp = errors.New("help requested")

// cmdDaemon implements `live daemon`. Returns the process exit code.
func cmdDaemon(args []string) int {
	opts, err := parseDaemonFlags(args)
	if err == flagHelp {
		fmt.Print(daemonUsage)
		return 0
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "live:", err)
		return 2
	}

	if opts.server {
		return cmdPocketServer(opts)
	}
	if opts.key != "" || opts.listen != "" || opts.stateDir != "" || opts.tlsCert != "" || opts.tlsKey != "" {
		fmt.Fprintln(os.Stderr, "live: server flags require --server")
		return 2
	}
	if _, err := auth.AccessToken(); err != nil {
		if _, err := auth.DeviceLogin(); err != nil {
			fmt.Fprintln(os.Stderr, "live:", err)
			return 1
		}
	}

	if len(opts.dirs) == 0 {
		wd, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(os.Stderr, "live: cannot resolve the current directory:", err)
			return 1
		}
		opts.dirs = []string{filepath.Clean(wd)}
	}

	// The operator's adapter file, if they have one. A file that exists but
	// does not parse stops startup: dropping agents silently would leave them
	// wondering why the picker is short.
	custom, err := loadAgentCommands(opts.agentsFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "live:", err)
		return 2
	}
	agents := discoverAgents(spawnableBuiltins, custom)
	// ollama with no models pulled cannot be spawned — `ollama run` needs one —
	// so drop it rather than advertise a target that would fail on click.
	models := availableModels()
	if len(models) == 0 {
		agents = without(agents, "ollama")
	}
	if len(agents) == 0 {
		fmt.Fprintln(os.Stderr, "live: no agent harness found on PATH — nothing to spawn")
		fmt.Fprintf(os.Stderr, "      looked for: %s\n", strings.Join(spawnableBuiltins, ", "))
		fmt.Fprintf(os.Stderr, "      anything else: list it in %s\n", hostcfg.AgentsPath())
		return 1
	}

	// tmux is how a spawned agent gets a real terminal with no operator
	// attached — and it is what lets the operator later attach to a session
	// they started from the browser. Without it there is nothing to run in.
	if _, err := exec.LookPath("tmux"); err != nil {
		fmt.Fprintln(os.Stderr, "live: tmux is required for `live daemon` (a web-started agent needs a terminal to run in)")
		fmt.Fprintln(os.Stderr, "      install it:  brew install tmux   |   apt install tmux")
		return 1
	}

	hostID, err := loadOrCreateHostID()
	if err != nil {
		fmt.Fprintln(os.Stderr, "live: could not establish a host id:", err)
		return 1
	}
	hostname, _ := os.Hostname()
	name := strings.TrimSpace(opts.name)
	if name == "" {
		name = hostname
	}

	self, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "live: cannot resolve the live binary:", err)
		return 1
	}

	reg := api.HostRegistration{
		HostID:   hostID,
		Name:     name,
		Hostname: hostname,
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		Dirs:     opts.dirs,
		Agents:   agents,
		Models:   models,
		// Per-agent model choice (LSH-16); spawnSession checks against it.
		AgentModels: buildModelCatalog(agents, custom, models),
	}
	client := api.New()

	fmt.Fprintf(os.Stderr, "\x1b[32m● live\x1b[0m daemon ready as \x1b[1m%s\x1b[0m — start sessions from %s\n", name, hostcfg.WebURL())
	fmt.Fprintf(os.Stderr, "        agents: %s\n", strings.Join(agents, ", "))
	for _, am := range reg.AgentModels {
		if len(am.Models) > 0 {
			ids := make([]string, len(am.Models))
			for i, m := range am.Models {
				ids[i] = m.ID
			}
			fmt.Fprintf(os.Stderr, "        models: %s — %s\n", am.Name, strings.Join(ids, ", "))
		}
	}
	for _, d := range opts.dirs {
		fmt.Fprintf(os.Stderr, "        dir:    %s\n", d)
	}
	fmt.Fprintln(os.Stderr)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go reapLoop(ctx, client)

	// Reconnect loop. The stream is the heartbeat: while it is up the server
	// keeps this machine listed, so a dropped connection must be re-established
	// (and re-registered — the record may have lapsed meanwhile).
	backoff := time.Second
	for ctx.Err() == nil {
		if err := client.RegisterHost(reg); err != nil {
			fmt.Fprintf(os.Stderr, "live: register failed (%v) — retrying in %s\n", err, backoff)
			if !sleepCtx(ctx, backoff) {
				return 0
			}
			backoff = nextBackoff(backoff)
			continue
		}

		err := client.HostStream(ctx, hostID, func(cmd api.SpawnCommand) {
			switch cmd.Type {
			case "spawn":
				if err := spawnSession(client, self, reg, custom, cmd); err != nil {
					fmt.Fprintf(os.Stderr, "\x1b[31m● live\x1b[0m spawn refused: %v\n", err)
				}
			case "set_model":
				if err := setSessionModel(cmd); err != nil {
					fmt.Fprintf(os.Stderr, "\x1b[31m● live\x1b[0m model switch refused: %v\n", err)
				}
			}
		})
		switch {
		case ctx.Err() != nil:
			return 0
		case errors.Is(err, api.ErrHostStreamUnsupported):
			fmt.Fprintln(os.Stderr, "live: this server does not support web-started sessions (upgrade the server)")
			return 1
		case err != nil:
			fmt.Fprintf(os.Stderr, "live: stream ended (%v) — reconnecting in %s\n", err, backoff)
		default:
			// Clean EOF (server restart, proxy timeout): reconnect promptly.
			backoff = time.Second
		}
		if !sleepCtx(ctx, backoff) {
			return 0
		}
		backoff = nextBackoff(backoff)
	}
	return 0
}

// spawnSession validates a spawn command against THIS machine's registration
// and, if it holds up, starts the agent in a detached tmux session.
//
// Every rejection ends the session server-side: the web is already showing a
// session page, and leaving it waiting forever for an agent that will never
// attach is worse than showing it ended.
func spawnSession(client *api.Client, self string, reg api.HostRegistration, custom map[string]agentCommand, cmd api.SpawnCommand) error {
	fail := func(format string, a ...any) error {
		err := fmt.Errorf(format, a...)
		if cmd.SessionID != "" {
			_, _ = client.Stop(cmd.SessionID)
		}
		return err
	}

	if cmd.SessionID == "" {
		return fmt.Errorf("spawn command carried no session id")
	}
	// Re-check the agent locally: the server's validation is not this process's
	// trust boundary. The name must be one THIS process registered, and the
	// executable it maps to is resolved here — the wire never carries a path.
	if !contains(reg.Agents, cmd.Agent) {
		return fail("agent %q is not offered by this machine", cmd.Agent)
	}
	command, fixedArgs := cmd.Agent, []string(nil)
	typeComments, acp := false, false
	if def, ok := custom[cmd.Agent]; ok {
		command = def.Command
		typeComments, acp = def.TypeComments, def.ACP
		// {prompt} is a Pocket (one-shot) substitution; an interactive session
		// has no prompt to put there, so those args are left out rather than
		// passed through with the placeholder intact.
		for _, a := range def.Args {
			if !strings.Contains(a, "{prompt}") {
				fixedArgs = append(fixedArgs, a)
			}
		}
	}
	bin, err := exec.LookPath(command)
	if err != nil {
		return fail("%s is not on PATH anymore", command)
	}
	// Re-check the directory locally, after cleaning — this is the check that
	// actually bounds what a compromised web session could reach.
	cwd := filepath.Clean(cmd.Cwd)
	if !contains(reg.Dirs, cwd) {
		return fail("directory %q is not registered on this machine", cmd.Cwd)
	}
	if st, err := os.Stat(cwd); err != nil || !st.IsDir() {
		return fail("directory %q is gone", cwd)
	}

	// Run the ordinary capture path — `live --attach <id> <agent>` — with no
	// controlling terminal. live puts the agent in its own tmux session as
	// usual and, seeing no tty, holds it open instead of attaching (see
	// attachOrWait). Wrapping in tmux HERE would nest two tmux servers and the
	// inner attach would fail.
	//
	// This is exec with an argv, not a shell: nothing from the command is ever
	// interpolated into a string a shell would parse.
	// ollama needs a model on the command line. Re-enumerate rather than trust
	// the registration we sent (or anything the server echoed back): this is
	// the same trust boundary the directory check enforces, and a model can be
	// removed while the daemon is running.
	agentArgs := append([]string{}, fixedArgs...)
	if cmd.Agent == "ollama" {
		model := strings.TrimSpace(cmd.Model)
		if model == "" {
			return fail("ollama spawn carried no model")
		}
		if !safeModelID(model) {
			return fail("model %q is not a valid ollama model name", model)
		}
		if !contains(availableModels(), model) {
			return fail("model %q is not pulled on this machine", model)
		}
		agentArgs = []string{"run", model}
	} else {
		// Every other agent: the model must be in the catalog THIS process
		// built at startup, and maps to the agent's own fixed flag (LSH-16).
		margs, err := modelArgs(cmd.Agent, cmd.Model, reg, custom)
		if err != nil {
			return fail("%v", err)
		}
		agentArgs = append(agentArgs, margs...)
	}

	// Owner-requested permission bypass. The spawn came from the owner's own
	// account targeting their own machine, so this is equivalent to them
	// running `claude --dangerously-skip-permissions` locally: live's web
	// permission probes turn off (--no-permissions) and the agent's own
	// prompts are skipped via its documented flag where one exists. The
	// boolean maps to fixed flags here — nothing from the wire is forwarded.
	liveArgs := []string{"--attach", cmd.SessionID}
	if cmd.BypassPermissions {
		liveArgs = append(liveArgs, "--no-permissions")
		switch cmd.Agent {
		case "claude":
			agentArgs = append(agentArgs, "--dangerously-skip-permissions")
		case "gemini":
			agentArgs = append(agentArgs, "--yolo")
		case "codex":
			// No prompts, but keep codex's sandbox: anything outside it fails
			// instead of asking, rather than running unsandboxed.
			agentArgs = append(agentArgs, "--ask-for-approval", "never")
		}
	}

	child := exec.Command(self, append(append(liveArgs, bin), agentArgs...)...)
	child.Dir = cwd
	child.Env = envWithout(os.Environ(), "LIVE_ACP") // ACP is per agent, never inherited
	if typeComments {
		child.Env = append(child.Env, "LIVE_TYPE_COMMENTS=1")
	}
	if acp {
		child.Env = append(child.Env, "LIVE_ACP=1")
	}
	child.Stdin = nil // no tty — this is what selects the headless path
	// Diagnostics land in the shared log; the daemon's own terminal stays quiet.
	if logf, err := os.OpenFile(hostcfg.LogPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		defer logf.Close()
		child.Stdout, child.Stderr = logf, logf
	}
	// Its own process group, so a session outlives the daemon that started it
	// (restarting `live daemon` must not kill running sessions).
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := child.Start(); err != nil {
		return fail("could not start %s: %v", cmd.Agent, err)
	}
	// Reap it in the background; we never block the command stream on an agent.
	// The capture process ends the session itself on a clean exit; if it died
	// without doing so (killed, crashed), end it here so it doesn't stay "live"
	// forever. The tmux reaper only sees tmux panes, and pty agents have none.
	go func() {
		_ = child.Wait()
		_, _ = client.Stop(cmd.SessionID)
	}()

	fmt.Fprintf(os.Stderr, "\x1b[32m● live\x1b[0m started %s in %s → %s/session/%s\n",
		cmd.Agent, cwd, hostcfg.WebURL(), cmd.SessionID)
	if acp || profileFor(bin).captureMode == "pty" {
		fmt.Fprintf(os.Stderr, "        headless terminal session (pid %d) — watch it on the web; no local pane to attach\n", child.Process.Pid)
	} else {
		fmt.Fprintf(os.Stderr, "        attach locally: tmux -L %s attach -t live-%s\n", tmuxSocket, cmd.SessionID[:8])
	}
	return nil
}

// tmuxSocket is the dedicated tmux server the shim uses, so live sessions never
// mix with the operator's own tmux. Mirrors the socket name in run.go.
const tmuxSocket = "liveshortly"

// reapOrphanSessions archives sessions whose capture process is gone.
//
// A spawned `live --attach` child runs in its own process group so a session
// outlives the daemon that started it (see spawnSession). The flip side: kill
// that child — reboot, kill -9, OOM — and its tmux pane survives with no sidecar
// behind it. The hook relay then fails silently (`live hook` exits 0 on a
// connection error, by design), so nothing is captured and nobody ever calls
// Stop: the session sits "live" on the web forever, waiting on an agent that no
// longer reports to anyone.
//
// So at startup, for every live-* pane on our socket, probe the sidecar address
// recorded in that pane's environment. Nothing listening means the capture
// process is gone, and the session is archived server-side.
//
// The pane itself is deliberately left running. This socket also carries plain
// `live claude` runs the operator started by hand, and killing someone's agent
// mid-task to tidy up a feed is not a trade we get to make for them — the pane
// stays attachable, it just stops pretending to be captured.
//
// Safe to run alongside spawnSession: a live-* pane only comes into existence
// after StartHookServer has bound (its address is passed to tmux new-session as
// `-e LIVE_SIDECAR`), so a session being spawned right now can never be seen
// half-built and mistaken for an orphan.
func reapOrphanSessions(client *api.Client) {
	tmux := func(args ...string) *exec.Cmd {
		c := exec.Command("tmux", append([]string{"-L", tmuxSocket}, args...)...)
		// nested-tmux guard: our isolated socket must not inherit $TMUX.
		c.Env = envWithout(os.Environ(), "TMUX", "TMUX_PANE")
		return c
	}
	out, err := tmux("list-sessions", "-F", "#{session_name}").Output()
	if err != nil {
		return // no tmux server on our socket: nothing was left behind
	}
	for _, sess := range strings.Fields(string(out)) {
		if !strings.HasPrefix(sess, "live-") {
			continue
		}
		id := paneEnv(tmux, sess, "LIVE_SESSION_ID")
		addr := paneEnv(tmux, sess, "LIVE_SIDECAR")
		if id == "" || addr == "" || sidecarAlive(addr) {
			continue
		}
		if _, err := client.Stop(id); err != nil {
			fmt.Fprintf(os.Stderr, "live: could not archive orphaned session %s: %v\n", id, err)
			continue
		}
		fmt.Fprintf(os.Stderr, "\x1b[33m● live\x1b[0m archived orphaned session %s — its capture process is gone (tmux pane %s left running: tmux -L %s attach -t %s)\n",
			id, sess, tmuxSocket, sess)
	}
}

// reapInterval is how often the daemon re-checks for orphaned sessions. Cheap
// enough to be invisible (a couple of tmux calls, plus one localhost probe per
// live pane) and short enough that a capture process killed mid-afternoon does
// not leave its session showing "live" until the next daemon restart.
const reapInterval = 5 * time.Minute

// reapLoop sweeps at startup and then on an interval, until ctx is done.
func reapLoop(ctx context.Context, client *api.Client) {
	reapOrphanSessions(client)
	t := time.NewTicker(reapInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			reapOrphanSessions(client)
		}
	}
}

// paneEnv reads one variable out of a tmux session's environment.
func paneEnv(tmux func(...string) *exec.Cmd, sess, key string) string {
	out, err := tmux("show-environment", "-t", sess, key).Output()
	if err != nil {
		return ""
	}
	return parseShowEnv(string(out))
}

// parseShowEnv pulls the value out of `tmux show-environment` output, which is
// "KEY=value" when set and "-KEY" when explicitly unset.
func parseShowEnv(out string) string {
	_, val, ok := strings.Cut(strings.TrimSpace(out), "=")
	if !ok {
		return ""
	}
	return val
}

// sidecarAlive reports whether anything is still listening on a session's hook
// address. Any HTTP answer counts — an unauthenticated probe earns a 403, and a
// 403 is proof something is home. Wrong in one direction only: a recycled port
// reads as alive and its session is left untouched, which is the harmless way to
// be wrong. Archiving a session whose agent is still working is not.
//
// Hence two attempts. This now runs on a timer rather than once per daemon
// start, so a single localhost probe timing out under load must not be enough to
// archive a session whose agent is very much alive.
func sidecarAlive(addr string) bool {
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, err := (&http.Client{Timeout: 2 * time.Second}).Get(addr + "/perm/pending")
		if err == nil {
			resp.Body.Close()
			return true
		}
	}
	return false
}

// maxSpawnableModels bounds what a machine advertises, so a host with a large
// model library cannot bloat the registration payload.
const maxSpawnableModels = 50

// availableModels lists the ollama models pulled on this machine, by parsing
// `ollama list`. Returns nil when ollama is absent or reports nothing, in which
// case ollama is simply not offered as a spawn target.
//
// This list IS the allowlist the spawn path validates against, so it is built
// here and never accepted from the server.
func availableModels() []string {
	if _, err := exec.LookPath("ollama"); err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ollama", "list").Output()
	if err != nil {
		return nil
	}
	var models []string
	seen := map[string]bool{}
	for i, line := range strings.Split(string(out), "\n") {
		if i == 0 {
			continue // header: NAME  ID  SIZE  MODIFIED
		}
		name, _, found := strings.Cut(strings.TrimSpace(line), " ")
		if !found && name == "" {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "" || seen[name] || !validModelName(name) {
			continue
		}
		seen[name] = true
		models = append(models, name)
		if len(models) == maxSpawnableModels {
			break
		}
	}
	return models
}

// validModelName bounds a model tag to the characters ollama actually uses
// (namespace/model:tag). Belt-and-braces: the name is never shell-interpolated,
// but a name is also a spawn argument, so keep the alphabet closed.
func validModelName(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.', c == '-', c == '_', c == ':', c == '/':
		default:
			return false
		}
	}
	return true
}

// loadOrCreateHostID returns this machine's stable daemon id, minting one on
// first run. Server keys are namespaced by user, so the id identifies a machine
// within one account and is not a secret.
func loadOrCreateHostID() (string, error) {
	path := hostcfg.HostIDPath()
	if b, err := os.ReadFile(path); err == nil {
		if id := sanitizeHostID(string(b)); id != "" {
			return id, nil
		}
	}
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	id := "h" + hex.EncodeToString(raw)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(id), 0o600); err != nil {
		return "", err
	}
	return id, nil
}

// sanitizeHostID mirrors the server's id alphabet, so a hand-edited or
// corrupted host_id file is replaced rather than rejected on every register.
func sanitizeHostID(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 64 {
		return ""
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return ""
		}
	}
	return s
}

// without returns list minus want, preserving order.
func without(list []string, want string) []string {
	out := list[:0:0]
	for _, s := range list {
		if s != want {
			out = append(out, s)
		}
	}
	return out
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// sleepCtx waits d, or returns false if ctx is cancelled first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func nextBackoff(d time.Duration) time.Duration {
	d *= 2
	if d > 30*time.Second {
		return 30 * time.Second
	}
	return d
}

// claudeModelAliases is the fixed set of model aliases a web command may hand
// to claude — the same allowlist the server enforces; re-checked here because
// the server is not this process's trust boundary.
var claudeModelAliases = map[string]bool{
	"sonnet": true,
	"opus":   true,
	"haiku":  true,
}

// sessionIDRe bounds a session id to the UUID alphabet before it is used in a
// tmux target name.
var sessionIDRe = regexp.MustCompile(`^[a-f0-9-]{8,64}$`)

// setSessionModel types `/model <alias>` into the session's tmux pane — the
// same slash command the operator would use in the terminal. The alias comes
// from a fixed allowlist and the session id from the UUID alphabet, so nothing
// free-form from the wire reaches tmux. A session this machine does not host
// simply has no matching tmux session, and the send fails quietly.
func setSessionModel(cmd api.SpawnCommand) error {
	id := strings.TrimSpace(cmd.SessionID)
	model := strings.TrimSpace(cmd.Model)
	if !sessionIDRe.MatchString(id) {
		return fmt.Errorf("invalid session id")
	}
	if !claudeModelAliases[model] {
		return fmt.Errorf("unknown model alias %q", model)
	}
	target := "live-" + id[:8]
	// The pane may be on another of the owner's machines: not an error worth
	// ending anything over — just report and move on.
	if err := exec.Command("tmux", "-L", tmuxSocket, "has-session", "-t", target).Run(); err != nil {
		return fmt.Errorf("session %s is not hosted on this machine", id[:8])
	}
	// `/model` is Claude Code's command; never type it into another agent's
	// pane. Panes record their agent in LIVE_AGENT_NAME (older panes don't,
	// and keep the previous behaviour).
	if out, err := exec.Command("tmux", "-L", tmuxSocket, "show-environment", "-t", target, "LIVE_AGENT_NAME").Output(); err == nil {
		if v := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(out)), "LIVE_AGENT_NAME=")); v != "" && !harness.ForDisplay(v).ModelSwitch {
			return fmt.Errorf("%s can't switch models mid-session", v)
		}
	}
	// Literal text first (-l so "/model" is not interpreted), then Enter.
	if err := exec.Command("tmux", "-L", tmuxSocket, "send-keys", "-t", target, "-l", "/model "+model).Run(); err != nil {
		return fmt.Errorf("could not type into the session pane: %v", err)
	}
	if err := exec.Command("tmux", "-L", tmuxSocket, "send-keys", "-t", target, "Enter").Run(); err != nil {
		return fmt.Errorf("could not submit the model switch: %v", err)
	}
	fmt.Fprintf(os.Stderr, "\x1b[32m● live\x1b[0m switched %s to /model %s\n", id[:8], model)
	return nil
}
