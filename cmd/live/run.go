package main

import (
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

	"github.com/resapce/live/internal/adapter"
	"github.com/resapce/live/internal/api"
	"github.com/resapce/live/internal/auth"
	"github.com/resapce/live/internal/hostcfg"
	"github.com/resapce/live/internal/perm"
	"github.com/resapce/live/internal/sidecar"
)

// agentProfile maps a command name to how we capture it.
type agentProfile struct {
	framework   string
	captureMode string // "hooks" | "pty"
}

func profileFor(bin string) agentProfile {
	switch filepath.Base(bin) {
	case "claude":
		return agentProfile{"claude-code", "hooks"}
	case "gemini":
		return agentProfile{"gemini-cli", "pty"}
	case "codex":
		return agentProfile{"codex", "pty"}
	default:
		return agentProfile{"terminal", "pty"}
	}
}

// runOpts holds the flags parsed off the front of `live [opts] <agent> …`.
type runOpts struct {
	title       string
	webPerms    bool
	handoffCode string // --handoff <code>  → fork from a pinned handoff snapshot
	sessionID   string // --session <id>    → fork from a session's latest state
	preDigest   bool   // --pre-digest      → pre-compress the digest with claude -p
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
	if opts.handoffCode != "" || opts.sessionID != "" {
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
	// Headless -p/--print runs are non-interactive automation — no human is
	// there to answer a permission, so never gate them.
	if isPrintMode(userArgs) {
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

	// Seed the new agent with the prior context. For hooks agents (claude) this
	// appends an opening prompt that points at the digest file; for pty agents
	// it writes the file and prints a banner to paste. Must run before dispatch
	// so the (possibly augmented) userArgs reach the launcher.
	if forked {
		userArgs = seedHandoff(bundle, sessionID, hooks, userArgs, opts.preDigest, binPath)
	}

	var exitCode int
	switch {
	case tmuxMode:
		exitCode = runHooksTmuxMode(sc, binPath, userArgs)
	case hooks:
		exitCode = runHooksMode(sc, binPath, userArgs)
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

	newArgs := []string{"new-session", "-d", "-s", sess,
		"-e", "LIVE_SIDECAR=" + addr, "-e", "LIVE_TOKEN=" + token, "-e", "LIVE_SESSION_ID=" + sc.SessionID,
		// The PreToolUse relay blocks on the broker (popup or web); its HTTP
		// client must outlast the 8m decision grace, under the 10m hook.
		"-e", "LIVE_HOOK_TIMEOUT=590",
		binPath}
	newArgs = append(newArgs, claudeArgs...)
	if out, err := tmux(newArgs...).CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "live: tmux new-session failed: %v: %s\n", err, out)
		return 1
	}
	_ = tmux("set-option", "-t", sess, "status", "off").Run()

	// popup driver: raise a display-popup whenever a permission is pending
	popupCmd := fmt.Sprintf("%q perm-prompt %q %q", self, addr, token)
	go func() {
		for range broker.Notify() {
			_ = tmux("display-popup", "-t", sess, "-w", "76", "-h", "9", "-E", popupCmd).Run()
		}
	}()

	// attach the user (foreground; returns when Claude exits and the session ends)
	att := tmux("attach-session", "-t", sess)
	att.Stdin, att.Stdout, att.Stderr = os.Stdin, os.Stdout, os.Stderr
	signal.Ignore(os.Interrupt, syscall.SIGQUIT)
	defer signal.Reset(os.Interrupt, syscall.SIGQUIT)
	_ = att.Run()

	_ = tmux("kill-session", "-t", sess).Run()
	return 0
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
// pops a one-time "Loading development channels" confirmation (remembered after
// you accept it once). Returns nil (channels off) in -p/--print mode, when
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
	fmt.Fprint(os.Stderr, "\x1b[33m● live\x1b[0m loading viewer channel — accept Claude's \"Loading development channels\" prompt on first launch (one-time)\n\n")
	return []string{
		"--mcp-config", mcpCfg, "--strict-mcp-config",
		"--dangerously-load-development-channels", "server:liveshortly-channel",
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

// seedHandoff persists the handoff digest and orients the new agent with it.
// The digest is deliberately raw — the CLI does not summarize it; the user's
// own agent does the comprehension. Returns userArgs, possibly with an opening
// prompt appended (hooks agents only). Best-effort: a write failure degrades to
// a plain live session rather than aborting the run.
func seedHandoff(b api.HandoffBundle, sessionID string, hooks bool, userArgs []string, preDigest bool, claudeBin string) []string {
	path, err := writeHandoffFile(sessionID, b.Markdown)
	if err != nil {
		fmt.Fprintf(os.Stderr, "live: could not write handoff digest (%v) — continuing without prior context\n", err)
		return userArgs
	}

	// Optional pre-compression with the user's own Claude (off by default).
	if preDigest && hooks {
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

	seed := fmt.Sprintf("You are continuing a handed-off LiveShortly session. Read %s for the full prior context, then restate the state in 2–3 lines and continue. The working tree may differ from the original machine.", path)
	if hooks {
		// claude takes an initial prompt as a trailing positional arg.
		return append(userArgs, seed)
	}
	// pty agents: we can't reliably inject a first prompt agent-agnostically, so
	// tell the operator what to paste.
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
