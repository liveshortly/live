package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/liveshortly/live/internal/adapter"
	"github.com/liveshortly/live/internal/api"
	"github.com/liveshortly/live/internal/auth"
	"github.com/liveshortly/live/internal/hostcfg"
	"github.com/liveshortly/live/internal/perm"
	"github.com/liveshortly/live/internal/sidecar"
)

// agentProfile maps a command name to how we capture it.
type agentProfile struct {
	framework   string
	captureMode string // "hooks" | "pty" | "rollout"
	// acceptsInitialPrompt is true for agents that take an initial [prompt]
	// positional arg (claude, codex). Used to inject the handoff seed prompt
	// automatically instead of asking the operator to paste it.
	acceptsInitialPrompt bool
}

func profileFor(bin string) agentProfile {
	switch filepath.Base(bin) {
	case "claude":
		return agentProfile{framework: "claude-code", captureMode: "hooks", acceptsInitialPrompt: true}
	case "gemini":
		return agentProfile{framework: "gemini-cli", captureMode: "pty"}
	case "codex":
		return agentProfile{framework: "codex", captureMode: "rollout", acceptsInitialPrompt: true}
	default:
		return agentProfile{framework: "terminal", captureMode: "pty"}
	}
}

// runOpts holds the flags parsed off the front of `live [opts] <agent> …`.
type runOpts struct {
	title       string
	webPerms    bool
	handoffCode string // --handoff <code>  → fork from a pinned handoff snapshot
	sessionID   string // --session <id>    → fork from a session's latest state
	preDigest   bool   // --pre-digest      → pre-compress the digest with claude -p
	// attachID is set by `live daemon` when the web already created the session:
	// capture attaches to that id instead of creating one. Internal — a human
	// never types it, so it is leading-position only.
	attachID string // --attach <id>
}

// parseRunFlags pulls the live-specific options out of the argument list and
// returns them plus the remaining args (the agent command and its arguments).
//
// Leading options (--title, --no-permissions, and the fork flags) are consumed
// up to the first non-flag token. The fork flags (--handoff/--session/
// --pre-digest) are ALSO honored when they appear AFTER the agent name, since
// that's how the feature reads — `live claude --session <id>`; the rest of the
// agent's args pass through untouched. An explicit `--` ends live's parsing:
// everything after it (including any --session) goes verbatim to the agent.
// --handoff and --session are mutually exclusive. Split out for unit-testing.
func parseRunFlags(args []string) (runOpts, []string, error) {
	// tmux popups + web permission control are hardcoded ON for live claude
	// (equivalent to LIVE_TMUX=1 LIVE_WEB_PERMS=1). --no-permissions turns
	// permission control off for a single run; headless -p/--print disables it
	// automatically in cmdRun.
	o := runOpts{webPerms: true}

	// forkFlag consumes a fork option at args[i], advancing the index. Shared by
	// the leading-flag loop and the post-agent scan so both placements behave
	// identically. Returns the new index, or an error on a missing value.
	forkFlag := func(args []string, i int) (int, bool, error) {
		switch args[i] {
		case "--handoff":
			if i+1 >= len(args) {
				return i, true, fmt.Errorf("--handoff needs a value")
			}
			o.handoffCode = args[i+1]
			return i + 2, true, nil
		case "--session":
			if i+1 >= len(args) {
				return i, true, fmt.Errorf("--session needs a value")
			}
			o.sessionID = args[i+1]
			return i + 2, true, nil
		case "--pre-digest":
			o.preDigest = true
			return i + 1, true, nil
		}
		return i, false, nil
	}

	// Phase 1: leading options, up to the agent command or `--`.
	i := 0
	doubleDash := false
leading:
	for i < len(args) {
		if ni, ok, err := forkFlag(args, i); err != nil {
			return o, nil, err
		} else if ok {
			i = ni
			continue
		}
		switch args[i] {
		case "--title":
			if i+1 >= len(args) {
				return o, nil, fmt.Errorf("--title needs a value")
			}
			o.title = args[i+1]
			i += 2
		case "--attach":
			if i+1 >= len(args) {
				return o, nil, fmt.Errorf("--attach needs a value")
			}
			o.attachID = args[i+1]
			i += 2
		case "--web-permissions":
			o.webPerms = true
			i++
		case "--no-permissions":
			o.webPerms = false
			i++
		case "--":
			i++
			doubleDash = true
			break leading
		default:
			break leading
		}
	}
	rest := args[i:]

	// Phase 2: the agent + its args. Unless `--` was used, pull any fork flags
	// out of the agent args too (so `live claude --session X` forks), leaving
	// everything else for the agent.
	if !doubleDash && len(rest) > 0 {
		out := []string{rest[0]}
		j := 1
		for j < len(rest) {
			if nj, ok, err := forkFlag(rest, j); err != nil {
				return o, nil, err
			} else if ok {
				j = nj
				continue
			}
			out = append(out, rest[j])
			j++
		}
		rest = out
	}

	if o.handoffCode != "" && o.sessionID != "" {
		return o, nil, fmt.Errorf("--handoff and --session are mutually exclusive")
	}
	// Attaching means the session already exists; forking means creating a new
	// one from an old one. Asking for both is a caller bug, not a fallback.
	if o.attachID != "" && (o.handoffCode != "" || o.sessionID != "") {
		return o, nil, fmt.Errorf("--attach cannot be combined with --handoff or --session")
	}
	return o, rest, nil
}

// cmdRun implements `live [opts] <agent> [args...]`. Returns the exit code to
// propagate.
func cmdRun(args []string) int {
	opts, rest, err := parseRunFlags(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "live:", err)
		return 2
	}
	title := opts.title
	webPerms := opts.webPerms
	tmuxWanted := true
	if len(rest) == 0 {
		fmt.Print(usage)
		return 2
	}

	bin, userArgs := rest[0], rest[1:]
	binPath, err := exec.LookPath(bin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "live: command not found: %s\n", bin)
		return 127
	}

	if _, err := auth.AccessToken(); err != nil {
		fmt.Fprintln(os.Stderr, "live:", err)
		return 1
	}

	prof := profileFor(bin)
	if title == "" {
		if wd, err := os.Getwd(); err == nil {
			title = filepath.Base(wd)
		} else {
			title = bin
		}
		title += " (live)"
	}

	client := api.New()

	// A handoff/fork creates a brand-new session owned by the current user that
	// continues from another (live or archived, possibly someone else's). The
	// server returns a digest of the source; everything downstream runs
	// unchanged against the new sessionID.
	var (
		sessionID string
		bundle    api.HandoffBundle
		forked    bool
	)
	if opts.attachID != "" {
		// Web-spawned run (`live daemon`): the server created the session before
		// telling this machine about it, so capture attaches to that id. Nothing
		// downstream changes — the session is a normal live session.
		sessionID = opts.attachID
	} else if opts.handoffCode != "" || opts.sessionID != "" {
		ref := api.ForkRef{HandoffCode: opts.handoffCode, SessionID: opts.sessionID}
		sessionID, bundle, err = client.CreateSessionForked(title, prof.framework, prof.framework, prof.captureMode, ref)
		if err != nil {
			if errors.Is(err, api.ErrForkForbidden) {
				fmt.Fprintln(os.Stderr, "live: you don't have access to fork this session")
			} else {
				fmt.Fprintf(os.Stderr, "live: could not fork session: %v\n", err)
			}
			return 1
		}
		forked = true
	} else {
		sessionID, err = client.CreateSession(title, prof.framework, prof.framework, prof.captureMode)
		if err != nil {
			fmt.Fprintf(os.Stderr, "live: could not start session: %v\n", err)
			return 1
		}
	}
	liveURL := hostcfg.WebURL() + "/session/" + sessionID
	fmt.Fprintf(os.Stderr, "\x1b[32m● live\x1b[0m streaming → %s\n\n", liveURL)

	// Diagnostics go to a file — the agent owns the terminal from here on.
	logf, _ := os.OpenFile(hostcfg.LogPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	logger := log.New(os.Stderr, "[live] ", log.LstdFlags)
	if logf != nil {
		defer logf.Close()
		logger = log.New(logf, sessionID[:8]+" ", log.LstdFlags)
	}

	hooks := prof.captureMode == "hooks"
	// Stand live's permission broker down when the run has opted out of gating:
	// headless -p/--print (non-interactive automation, no human to answer), or any
	// of the agent's own "don't ask" flags (--dangerously-skip-permissions,
	// --permission-mode bypassPermissions). Otherwise live's PreToolUse hook would
	// re-gate every tool from the web/popup even though the agent was told to skip,
	// so the flag looks ignored.
	if isPrintMode(userArgs) || permissionsBypassed(userArgs) {
		webPerms = false
	}
	hasTmux := false
	if hooks {
		if _, err := exec.LookPath("tmux"); err == nil {
			hasTmux = true
		}
	}

	// Resolve the permission mode. Default: tmux popups when tmux is present;
	// otherwise, on a pure default (no LIVE_* set), fall back to Claude's
	// native terminal prompts rather than stranding a solo keyboard user on
	// web-only. Explicit LIVE_WEB_PERMS=1 forces web-only even without tmux.
	tmuxMode := webPerms && tmuxWanted && hooks && hasTmux
	if webPerms && hooks && !tmuxMode && !hasTmux {
		fmt.Fprint(os.Stderr, "\x1b[33m● live\x1b[0m no tmux — approve tool runs from the session page (install tmux for in-terminal popups)\n\n")
	}

	sc := &sidecar.Sidecar{
		Client:         client,
		SessionID:      sessionID,
		WebURL:         hostcfg.WebURL(),
		WebPermissions: webPerms && hooks,
		Log:            logger,
	}
	if sc.WebPermissions {
		where := "the session page"
		if tmuxMode {
			where = "a CLI popup or the session page"
		}
		fmt.Fprintf(os.Stderr, "\x1b[33m● live\x1b[0m permission control ON — approve tool runs from %s (LIVE_WEB_PERMS=0 to disable)\n\n", where)
	}

	// Seed the new agent with the prior context. The operator reads the history
	// and drives — no auto-continue. For claude the digest is loaded as appended
	// system context so the agent is oriented but idle until the operator types.
	// Must run before dispatch so the (possibly augmented) userArgs reach the
	// launcher.
	if forked {
		userArgs = seedHandoff(bundle, sessionID, userArgs, opts.preDigest, binPath)
	}

	// app-server mode ignores userArgs, so the digest cannot ride in on a `-c`
	// override the way it does for the codex TUI. It is passed to thread/start as
	// developerInstructions instead. Empty when this is not a fork.
	var devInstructions string
	if forked && filepath.Base(binPath) == "codex" {
		devInstructions = codexInstructions(bundle.Markdown)
	}

	var exitCode int
	switch {
	case tmuxMode:
		exitCode = runHooksTmuxMode(sc, binPath, userArgs)
	case hooks:
		exitCode = runHooksMode(sc, binPath, userArgs)
	case prof.captureMode == "rollout" && os.Getenv("LIVE_CODEX_APPSERVER") == "1":
		// Real-time headless mode: codex driven over the app-server protocol,
		// with browser-answered permissions and push viewer comments.
		exitCode = runAppServerMode(sc, binPath, userArgs, devInstructions)
	case prof.captureMode == "rollout" && os.Getenv("LIVE_TMUX") != "0" && !isPrintMode(userArgs):
		// Interactive codex TUI in tmux; viewer comments are typed into its
		// composer, so they reach codex even while it is idle.
		exitCode = runCodexTmuxMode(sc, binPath, userArgs)
	case prof.captureMode == "rollout":
		exitCode = runRolloutMode(sc, binPath, userArgs)
	default:
		exitCode = runPTYMode(sc, binPath, userArgs)
	}

	sc.Shutdown()
	if url, err := client.Stop(sessionID); err == nil {
		fmt.Fprintf(os.Stderr, "\n\x1b[32m● live\x1b[0m session archived → %s\n", url)
	} else {
		fmt.Fprintf(os.Stderr, "\nlive: session stop failed (will be reaped): %v\n", err)
	}
	return exitCode
}

// runHooksTmuxMode (experimental, LIVE_TMUX=1) runs Claude inside an isolated
// tmux session so a permission prompt can appear in a clean display-popup
// (keyboard-answerable) without corrupting Claude's TUI — racing the web
// verdict. Falls back to web-only if tmux is missing.
func runHooksTmuxMode(sc *sidecar.Sidecar, binPath string, userArgs []string) int {
	tmuxBin, err := exec.LookPath("tmux")
	if err != nil {
		fmt.Fprintln(os.Stderr, "live: LIVE_TMUX set but tmux not found — using web-only permissions")
		return runHooksMode(sc, binPath, userArgs)
	}

	broker := perm.New(func(ctx context.Context) (string, error) {
		return sc.Client.GetDecision(sc.SessionID)
	}, 8*time.Minute)
	sc.Broker = broker

	sc.Start(context.Background())
	addr, token, err := sc.StartHookServer()
	if err != nil {
		fmt.Fprintf(os.Stderr, "live: sidecar failed: %v\n", err)
		return 1
	}
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "live: %v\n", err)
		return 1
	}
	settings, cleanup, err := adapter.WriteClaudeSettings(self, true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "live: %v\n", err)
		return 1
	}
	defer cleanup()

	const sock = "liveshortly"
	sess := "live-" + sc.SessionID[:8]
	tmux := func(args ...string) *exec.Cmd {
		c := exec.Command(tmuxBin, append([]string{"-L", sock}, args...)...)
		// nested-tmux guard: our isolated socket must not inherit $TMUX.
		c.Env = envWithout(os.Environ(), "TMUX", "TMUX_PANE")
		return c
	}
	_ = tmux("kill-session", "-t", sess).Run() // clear any stale session

	// Same viewer-channel wiring as the plain hooks mode — without this, tmux
	// sessions (the default when tmux is installed) never load channels, so the
	// "Loading development channels" prompt never appears.
	claudeArgs := []string{"--settings", settings}
	if chArgs, chCleanup := channelArgs(self, userArgs); chArgs != nil {
		if chCleanup != nil {
			defer chCleanup()
		}
		claudeArgs = append(claudeArgs, chArgs...)
	}
	claudeArgs = append(claudeArgs, userArgs...)

	// On a fork, print the prior context inside the pane (it lands in scrollback)
	// rather than outside it, where attach-session would repaint over it.
	agent := handoffShimArgs(self, sc.SessionID, append([]string{binPath}, claudeArgs...))

	newArgs := []string{"new-session", "-d", "-s", sess,
		"-e", "LIVE_SIDECAR=" + addr, "-e", "LIVE_TOKEN=" + token, "-e", "LIVE_SESSION_ID=" + sc.SessionID,
		// The PreToolUse relay blocks on the broker (popup or web); its HTTP
		// client must outlast the 8m decision grace, under the 10m hook.
		"-e", "LIVE_HOOK_TIMEOUT=590"}
	newArgs = append(newArgs, pinnedEnvArgs()...)
	newArgs = append(newArgs, agent...)
	if out, err := tmux(newArgs...).CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "live: tmux new-session failed: %v: %s\n", err, out)
		return 1
	}
	_ = tmux("set-option", "-t", sess, "status", "off").Run()
	bindContextPopup(func(a ...string) error { return tmux(a...).Run() }, self, sc.SessionID)

	// Web-started run: nobody can answer the channel warning, so do it here.
	// An operator at a real terminal answers it themselves.
	if headless() {
		go confirmChannelPrompt(tmux, sess)
	}

	// popup driver: raise a display-popup whenever a permission is pending
	popupCmd := fmt.Sprintf("%q perm-prompt %q %q", self, addr, token)
	go func() {
		for range broker.Notify() {
			_ = tmux("display-popup", "-t", sess, "-w", "76", "-h", "9", "-E", popupCmd).Run()
		}
	}()

	// attach the user (foreground; returns when Claude exits and the session ends)
	attachOrWait(tmux, sess)

	_ = tmux("kill-session", "-t", sess).Run()
	return 0
}

// headless reports that no operator is at this terminal — the state `live
// daemon` starts a web-requested session in. It is the single discriminator
// between "the human drives the pane" and "the browser drives the pane".
func headless() bool { return !term.IsTerminal(int(os.Stdin.Fd())) }

// confirmChannelPrompt answers Claude's development-channels warning for a
// web-started session.
//
// That warning appears on EVERY launch under
// --dangerously-load-development-channels (it is not remembered) and blocks the
// session behind a keypress. With nobody at the terminal there is no one to
// press it, and simply dropping the channel is not an alternative: the channel
// is the only path that reaches an IDLE agent, which is exactly the state a
// browser-driven session sits in between viewer messages.
//
// Deliberately narrow. It fires only in the headless path, only while the pane
// is showing THIS warning naming OUR channel, and only once. What it approves
// is a channel live wrote itself moments earlier, in a temp file, pointing at
// live's own binary — for a session the operator asked for by registering this
// machine. It will never answer any other prompt: an unrecognized screen is
// left alone for the operator to handle over tmux attach.
func confirmChannelPrompt(tmux func(args ...string) *exec.Cmd, sess string) {
	const (
		marker   = "WARNING: Loading development channels"
		ourChan  = "liveshortly-channel"
		deadline = 45 * time.Second
	)
	stop := time.Now().Add(deadline)
	for time.Now().Before(stop) {
		out, err := tmux("capture-pane", "-t", sess, "-p").Output()
		if err != nil {
			return // pane gone
		}
		pane := string(out)
		if strings.Contains(pane, marker) && strings.Contains(pane, ourChan) {
			_ = tmux("send-keys", "-t", sess, "Enter").Run()
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// attachOrWait hands the tmux session to the operator when there is a terminal
// to attach to, and otherwise just waits for the agent to finish.
//
// The headless branch is what makes a web-started session possible: `live
// daemon` launches this process with no controlling terminal, where
// attach-session fails with "open terminal failed: not a terminal". Returning
// on that error would kill the pane the instant it started, so instead we hold
// the session open and let the browser drive it. The operator can still take it
// over at any time with `tmux -L liveshortly attach -t <sess>`.
//
// Ceiling: the wait ends when the agent exits. Ending the session from the web
// archives the feed but leaves the pane running until the agent itself stops.
func attachOrWait(tmux func(args ...string) *exec.Cmd, sess string) {
	if !headless() {
		att := tmux("attach-session", "-t", sess)
		att.Stdin, att.Stdout, att.Stderr = os.Stdin, os.Stdout, os.Stderr
		signal.Ignore(os.Interrupt, syscall.SIGQUIT)
		defer signal.Reset(os.Interrupt, syscall.SIGQUIT)
		_ = att.Run()
		return
	}
	for {
		if err := tmux("has-session", "-t", sess).Run(); err != nil {
			return // the pane's process exited
		}
		time.Sleep(2 * time.Second)
	}
}

// pinnedEnvKeys are the variables a captured tmux session must carry from THIS
// process rather than inherit from the tmux server. HOME decides which
// ~/.claude the agent reads; PATH decides which binaries it finds; the
// LIVESHORTLY_* vars decide which API `live hook` posts to from inside the pane.
var pinnedEnvKeys = []string{
	"HOME", "PATH", "USER", "SHELL", "LANG",
	"LIVESHORTLY_API_URL", "LIVESHORTLY_WEB_URL", "LIVESHORTLY_FRONTEND_URL",
	"LIVESHORTLY_CRED_PATH",
}

// pinnedEnvArgs returns the `-e KEY=VALUE` pairs that pin those variables onto
// a new tmux session.
//
// Our tmux server (socket "liveshortly") outlives any single session, and tmux
// seeds each new session from the SERVER's environment for every variable
// outside `update-environment` — a default list that includes neither HOME nor
// PATH nor anything of ours. So without this, the second session on a
// long-lived server silently runs with the FIRST session's environment: the
// agent reads a different HOME, finds no config, and restarts its first-run
// wizard, while hooks post to whatever API URL the server was born with.
func pinnedEnvArgs() []string {
	var out []string
	for _, k := range pinnedEnvKeys {
		if v, ok := os.LookupEnv(k); ok {
			out = append(out, "-e", k+"="+v)
		}
	}
	return out
}

// envWithout returns env minus the named keys.
func envWithout(env []string, keys ...string) []string {
	drop := map[string]bool{}
	for _, k := range keys {
		drop[k] = true
	}
	out := env[:0:0]
	for _, e := range env {
		name := e
		if i := indexByte(e, '='); i >= 0 {
			name = e[:i]
		}
		if !drop[name] {
			out = append(out, e)
		}
	}
	return out
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// runHooksMode captures via the agent's own lifecycle hooks (Claude Code).
func runHooksMode(sc *sidecar.Sidecar, binPath string, userArgs []string) int {
	sc.Start(context.Background())
	addr, token, err := sc.StartHookServer()
	if err != nil {
		fmt.Fprintf(os.Stderr, "live: sidecar failed: %v\n", err)
		return 1
	}

	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "live: %v\n", err)
		return 1
	}
	settings, cleanup, err := adapter.WriteClaudeSettings(self, sc.WebPermissions)
	if err != nil {
		fmt.Fprintf(os.Stderr, "live: %v\n", err)
		return 1
	}
	defer cleanup()

	env := []string{
		"LIVE_SIDECAR=" + addr,
		"LIVE_TOKEN=" + token,
		"LIVE_SESSION_ID=" + sc.SessionID,
	}
	if sc.WebPermissions {
		// The PreToolUse relay blocks on a human verdict; give its HTTP client
		// headroom past the sidecar's decision grace so it doesn't time out
		// mid-wait. Must exceed awaitDecision's 8m grace, under the 10m hook.
		env = append(env, "LIVE_HOOK_TIMEOUT=590")
	}

	args := []string{"--settings", settings}
	if chArgs, chCleanup := channelArgs(self, userArgs); chArgs != nil {
		if chCleanup != nil {
			defer chCleanup()
		}
		args = append(args, chArgs...)
	}
	args = append(args, userArgs...)
	code, err := adapter.RunWithHooks(binPath, args, env)
	if err != nil {
		fmt.Fprintf(os.Stderr, "live: %v\n", err)
	}
	return code
}

// isPrintMode reports whether the agent is being run headlessly (-p/--print),
// where channel delivery does not apply.
// channelArgs returns the Claude Code flags that load the viewer "channel" —
// the MCP-backed path that delivers viewer comments into Claude even while it
// sits idle. Loading it needs --dangerously-load-development-channels, which
// pops a "Loading development channels" confirmation on EVERY launch — it is
// not remembered, and --channels (the approved-channel flag) rejects `server:`
// entries like ours. A web-started session answers it via confirmChannelPrompt,
// since no operator is there to. Returns nil (channels off) in -p/--print mode, when
// LIVE_CHANNEL=0, or if the temp MCP config can't be written — and logs WHY on
// stderr, so a missing prompt is diagnosable instead of a silent no-op. cleanup
// removes the temp MCP config (nil when no args). Shared by both interactive
// run modes (plain hooks + tmux) so channels behave the same with or without
// tmux. Set LIVE_CHANNEL=0 to force pure hook-boundary delivery.
func channelArgs(self string, userArgs []string) (args []string, cleanup func()) {
	if isPrintMode(userArgs) {
		return nil, nil // headless -p/--print: no human, channels are irrelevant
	}
	if os.Getenv("LIVE_CHANNEL") == "0" {
		fmt.Fprint(os.Stderr, "\x1b[33m● live\x1b[0m viewer channel OFF (LIVE_CHANNEL=0) — comments arrive at hook boundaries\n\n")
		return nil, nil
	}
	mcpCfg, mcpCleanup, err := adapter.WriteClaudeMCPConfig(self)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\x1b[33m● live\x1b[0m viewer channel could not load (%v) — comments arrive at hook boundaries\n\n", err)
		return nil, nil
	}
	fmt.Fprint(os.Stderr, "\x1b[33m● live\x1b[0m loading viewer channel — accept Claude's \"Loading development channels\" prompt each launch\n\n")
	// Order matters: --dangerously-load-development-channels is VARIADIC — it eats
	// every following non-flag token as a channel name. The callers append the
	// user's args (and, on a fork/handoff, the seeded opening prompt) AFTER these,
	// so the channel flag must NOT be last: a trailing prompt positional would be
	// swallowed as a bogus channel, claude would start with no prompt and exit
	// instantly. Keep the boolean --strict-mcp-config last so it terminates the
	// variadic and the prompt reaches claude.
	return []string{
		"--mcp-config", mcpCfg,
		"--dangerously-load-development-channels", "server:liveshortly-channel",
		"--strict-mcp-config",
	}, mcpCleanup
}

func isPrintMode(args []string) bool {
	for _, a := range args {
		if a == "-p" || a == "--print" {
			return true
		}
	}
	return false
}

// permissionsBypassed reports whether the agent args opt out of permission
// prompts by any means the agent offers: --dangerously-skip-permissions, or
// --permission-mode bypassPermissions (space- or =-separated). When set, the
// operator has explicitly said "don't ask", so live's own web/tmux broker stands
// down and lets the flags through to the agent unopposed.
func permissionsBypassed(args []string) bool {
	for i, a := range args {
		switch {
		case a == "--dangerously-skip-permissions":
			return true
		case a == "--permission-mode=bypassPermissions":
			return true
		case a == "--permission-mode" && i+1 < len(args) && args[i+1] == "bypassPermissions":
			return true
		}
	}
	return false
}

// seedHandoff persists the handoff digest and orients the new agent with it.
// The digest is deliberately raw — the CLI does not summarize it; the user's
// own agent does the comprehension.
//
// On a fork the operator wants to READ the prior history and then drive, so we
// do NOT auto-submit an opening prompt. For claude we load the digest as
// appended system context (`--append-system-prompt-file`): the agent is oriented
// the moment the operator types but does nothing until then. Returns userArgs,
// possibly with that flag appended. Best-effort: a write failure degrades to a
// plain live session rather than aborting the run.
func seedHandoff(b api.HandoffBundle, sessionID string, userArgs []string, preDigest bool, claudeBin string) []string {
	path, err := writeHandoffFile(sessionID, b.Markdown)
	if err != nil {
		fmt.Fprintf(os.Stderr, "live: could not write handoff digest (%v) — continuing without prior context\n", err)
		return userArgs
	}

	// Optional pre-compression with the user's own Claude (off by default). This
	// runs `claude -p`, so it only applies when the launched binary is claude —
	// codex uses `codex exec`, not `-p`.
	if preDigest && filepath.Base(claudeBin) == "claude" {
		if compact, derr := preDigestMarkdown(claudeBin, b.Markdown); derr == nil && compact != "" {
			if werr := os.WriteFile(path, []byte(compact), 0o644); werr == nil {
				fmt.Fprint(os.Stderr, "\x1b[36m● live\x1b[0m pre-digest: compressed the prior context with claude -p\n")
			}
		}
	}

	src := b.Source
	fmt.Fprintf(os.Stderr, "\x1b[36m● live\x1b[0m continuing from \x1b[1m%s\x1b[0m", firstNonEmpty(src.Title, src.ID, "a prior session"))
	if src.OwnerHandle != "" {
		fmt.Fprintf(os.Stderr, " (@%s)", src.OwnerHandle)
	}
	fmt.Fprintf(os.Stderr, " — digest at %s\n", path)
	if b.Truncated {
		fmt.Fprint(os.Stderr, "\x1b[33m● live\x1b[0m the prior context was long; the digest keeps the most recent turns (truncated)\n")
	}
	fmt.Fprintln(os.Stderr)

	// Auto-show the prior context in the terminal so the operator sees what
	// they forked from without opening the file or the web. LIVE_NO_CONTEXT=1
	// suppresses it.
	if os.Getenv("LIVE_NO_CONTEXT") != "1" {
		printPriorContext(os.Stderr, b, path)
	}

	// Don't auto-continue. For claude, load the digest as appended system context
	// so the agent is oriented but idle until the operator types; pause first so
	// the printed history is readable before claude's tmux TUI replaces the screen.
	if filepath.Base(claudeBin) == "claude" {
		waitForEnterBeforeAgent()
		return append(userArgs, "--append-system-prompt-file", path)
	}

	// Codex: same contract, different lever. `-c developer_instructions=<digest>`
	// orients the agent without submitting a turn — codex has no
	// --append-system-prompt-file. Prepended (not appended) so the operator's
	// positional prompt stays the LAST token for initialPrompt.
	//
	// Pause for the same reason claude does: codex runs inside tmux, and
	// attach-session repaints the whole screen, erasing the prior context we
	// just printed. Without this the operator never sees it.
	if filepath.Base(claudeBin) == "codex" {
		waitForEnterBeforeAgent()
		// app-server mode launches `codex app-server` and ignores userArgs, so a
		// -c override there would be silently dropped. cmdRun passes the same
		// digest to thread/start as developerInstructions instead.
		if os.Getenv("LIVE_CODEX_APPSERVER") == "1" {
			return userArgs
		}
		if instr := codexInstructions(b.Markdown); instr != "" {
			return append([]string{"-c", "developer_instructions=" + instr}, userArgs...)
		}
		return userArgs
	}

	// Other agents (pty): don't auto-submit either — the agent opens and waits;
	// the prior context is shown above and saved at the digest path.
	fmt.Fprintf(os.Stderr, "\x1b[36m● live\x1b[0m your agent will wait — prior context is shown above and saved at %s\n\n", path)
	return userArgs
}

// maxCodexInstructions bounds the digest we pass as a `-c` value. The whole
// launch command must fit in ARG_MAX, and a fork of a long session can carry a
// large digest; the full text always remains at the digest path.
const maxCodexInstructions = 64 << 10

// codexInstructions frames the digest as prior context and clips it to a size
// that is safe to pass on the command line.
func codexInstructions(markdown string) string {
	md := strings.TrimSpace(markdown)
	if md == "" {
		return ""
	}
	if len(md) > maxCodexInstructions {
		md = md[:maxCodexInstructions] + "\n\n[truncated — see the digest file for the full prior context]"
	}
	return "You are continuing a prior coding session. Here is its context.\n" +
		"Do not act on it until the operator asks; use it to answer their first message.\n\n" + md
}

// waitForEnterBeforeAgent pauses so the operator can read the printed prior
// context before the agent's full-screen (tmux) TUI replaces it. No-op when
// stdin isn't an interactive terminal (piped/headless) or LIVE_NO_PAUSE=1.
func waitForEnterBeforeAgent() {
	if os.Getenv("LIVE_NO_PAUSE") == "1" {
		return
	}
	if fi, err := os.Stdin.Stat(); err != nil || (fi.Mode()&os.ModeCharDevice) == 0 {
		return // not a TTY — nothing to pause for
	}
	fmt.Fprint(os.Stderr, "\x1b[36m● live\x1b[0m press \x1b[1mEnter\x1b[0m to open your agent — it has the prior context and will wait for your first message… ")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	fmt.Fprintln(os.Stderr)
}

// printPriorContext renders a compact, bounded view of the forked-from session's
// digest on fork. It prefers the structured Turns (role + text, tool/file
// annotated); the full digest remains at `path`. Nothing is written when there
// are no turns. Writer-injected so the format is unit-testable.
func printPriorContext(w io.Writer, b api.HandoffBundle, path string) {
	if len(b.Turns) == 0 {
		return
	}
	const maxTurns = 12

	fmt.Fprintf(w, "\x1b[36m● live\x1b[0m \x1b[1mprior context\x1b[0m (%d turn(s)):\n", len(b.Turns))

	turns := b.Turns
	if len(turns) > maxTurns {
		fmt.Fprintf(w, "    \x1b[2m… %d earlier turn(s) omitted — see %s\x1b[0m\n", len(turns)-maxTurns, path)
		turns = turns[len(turns)-maxTurns:]
	}
	for _, t := range turns {
		label := priorTurnLabel(t)
		fmt.Fprintf(w, "    \x1b[2m▸\x1b[0m \x1b[36m%s\x1b[0m %s\n", label, clipLine(t.Text, 160))
	}
	fmt.Fprintf(w, "\x1b[36m● live\x1b[0m full prior context at %s\n\n", path)
}

// priorTurnLabel formats the role/tool prefix for one digest turn.
func priorTurnLabel(t api.HandoffTurn) string {
	if t.Tool != "" {
		if t.File != "" {
			return t.Tool + " " + t.File + ":"
		}
		return t.Tool + ":"
	}
	if t.Role != "" {
		return t.Role + ":"
	}
	return "-"
}

// clipLine collapses a turn's text to a single bounded line for terminal display.
func clipLine(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(s, "\r", " "), "\n", " "))
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// appendSeedPrompt injects the handoff orient prompt as a trailing positional
// arg for agents that accept an initial [prompt] (claude, codex). For other
// pty agents it can't be injected agent-agnostically, so it prints a paste
// banner and leaves userArgs untouched. Split out (and pure w.r.t. userArgs) so
// the injection contract is unit-testable.
//
// NB flag ordering: the seed is appended LAST. Any launcher that also prepends
// its own flags (e.g. claude's channel args) must not end on a variadic flag,
// or the trailing prompt gets swallowed — see the 6a75b17 fix + its test.
func appendSeedPrompt(userArgs []string, seed string, acceptsInitialPrompt bool) []string {
	if acceptsInitialPrompt {
		return append(userArgs, seed)
	}
	fmt.Fprintf(os.Stderr, "\x1b[36m● live\x1b[0m paste this to orient your agent:\n    %s\n\n", seed)
	return userArgs
}

// writeHandoffFile stores a session's digest markdown at
// ~/.liveshortly/handoffs/<id>.md and returns the path.
func writeHandoffFile(sessionID, markdown string) (string, error) {
	dir := hostcfg.HandoffDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, sessionID+".md")
	if err := os.WriteFile(path, []byte(markdown), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// preDigestMarkdown runs a one-shot headless `claude -p` over the raw digest to
// pre-compress it into a tighter brief. Best-effort; the caller keeps the raw
// digest on any error.
func preDigestMarkdown(claudeBin, markdown string) (string, error) {
	if claudeBin == "" {
		return "", fmt.Errorf("no claude binary for pre-digest")
	}
	cmd := exec.Command(claudeBin, "-p",
		"Compress the following handoff digest into a tight brief a coding agent can act on: goal, key decisions, current state, and next steps. Preserve file paths and identifiers verbatim. Output only the brief.")
	cmd.Stdin = strings.NewReader(markdown)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return strings.TrimSpace(out.String()), nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// runPTYMode captures raw terminal output for agents without hooks. Viewer
// comments are shown to the operator as an overlay (they cannot be injected
// into an arbitrary agent).
func runPTYMode(sc *sidecar.Sidecar, binPath string, userArgs []string) int {
	p := &adapter.PTY{}
	sc.OnComment = func(c api.Comment) {
		who := c.Username
		if who == "" {
			who = "viewer"
		}
		p.Notify("💬 " + who + ": " + c.Message)
	}
	sc.Start(context.Background())

	// Batch output: flush at 2s or 8KB, ANSI-stripped, into `output` events.
	var mu sync.Mutex
	var buf []byte
	flush := func() {
		mu.Lock()
		chunk := buf
		buf = nil
		mu.Unlock()
		if len(chunk) == 0 {
			return
		}
		text := adapter.StripANSI(chunk)
		if text == "" {
			return
		}
		if len(text) > 2000 {
			text = text[:2000] + "…"
		}
		sc.Emit("output", map[string]any{"text": text, "ts": time.Now().UTC().Format(time.RFC3339)})
	}
	stopFlusher := make(chan struct{})
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				flush()
			case <-stopFlusher:
				return
			}
		}
	}()

	code, err := p.Run(binPath, userArgs, func(chunk []byte) {
		mu.Lock()
		buf = append(buf, chunk...)
		full := len(buf) >= 8192
		mu.Unlock()
		if full {
			flush()
		}
	})
	close(stopFlusher)
	flush()
	if err != nil {
		fmt.Fprintf(os.Stderr, "live: %v\n", err)
	}
	return code
}
