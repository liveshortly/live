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
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/liveshortly/live/internal/api"
	"github.com/liveshortly/live/internal/auth"
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
//     outside spawnableAgents is dropped.
//   - The directory must be one THIS process registered at startup. The server
//     checks too, but that check is a courtesy — this one is the defense.
//   - Nothing from the command is ever passed to a shell.
//
// The blast radius is therefore exactly: run one of three known agents, in one
// of the directories the operator named on the command line.

// spawnableAgents is the closed set of agents a spawn command may name. Keep in
// step with the server's allowlist in handlers/hosts.go.
var spawnableAgents = []string{"claude", "codex", "gemini"}

const daemonUsage = `live daemon — let this machine start sessions from the web

Usage:
  live daemon [--dir <path>]... [--name <label>]

  --dir <path>    A directory sessions may be started in (repeatable).
                  Defaults to the current directory. This list is the ONLY
                  place a web-started session can run — nothing else on this
                  machine is reachable.
  --name <label>  How this machine appears in the picker (default: hostname).

Leave it running; open the site and hit "+ New session".
`

type daemonOpts struct {
	dirs []string
	name string
}

func parseDaemonFlags(args []string) (daemonOpts, error) {
	var o daemonOpts
	for i := 0; i < len(args); {
		switch args[i] {
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

	agents := availableAgents()
	if len(agents) == 0 {
		fmt.Fprintf(os.Stderr, "live: none of %s are installed — nothing to spawn\n",
			strings.Join(spawnableAgents, ", "))
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
	}
	client := api.New()

	fmt.Fprintf(os.Stderr, "\x1b[32m● live\x1b[0m daemon ready as \x1b[1m%s\x1b[0m — start sessions from %s\n", name, hostcfg.WebURL())
	fmt.Fprintf(os.Stderr, "        agents: %s\n", strings.Join(agents, ", "))
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
			if err := spawnSession(client, self, reg, cmd); err != nil {
				fmt.Fprintf(os.Stderr, "\x1b[31m● live\x1b[0m spawn refused: %v\n", err)
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
func spawnSession(client *api.Client, self string, reg api.HostRegistration, cmd api.SpawnCommand) error {
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
	// Re-check the agent locally: the server's allowlist is not this process's
	// trust boundary.
	if !contains(reg.Agents, cmd.Agent) {
		return fail("agent %q is not offered by this machine", cmd.Agent)
	}
	bin, err := exec.LookPath(cmd.Agent)
	if err != nil {
		return fail("%s is not on PATH anymore", cmd.Agent)
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
	child := exec.Command(self, "--attach", cmd.SessionID, bin)
	child.Dir = cwd
	child.Env = os.Environ()
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
	go func() { _ = child.Wait() }()

	fmt.Fprintf(os.Stderr, "\x1b[32m● live\x1b[0m started %s in %s → %s/session/%s\n",
		cmd.Agent, cwd, hostcfg.WebURL(), cmd.SessionID)
	fmt.Fprintf(os.Stderr, "        attach locally: tmux -L %s attach -t live-%s\n", tmuxSocket, cmd.SessionID[:8])
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

// availableAgents reports which spawnable agents are actually installed here.
func availableAgents() []string {
	var out []string
	for _, a := range spawnableAgents {
		if _, err := exec.LookPath(a); err == nil {
			out = append(out, a)
		}
	}
	return out
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
