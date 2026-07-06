package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
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

// cmdRun implements `live [--title t] <agent> [args...]`. Returns the exit
// code to propagate.
func cmdRun(args []string) int {
	title := ""
	// Permission control is ON by default; disable with LIVE_WEB_PERMS=0.
	// tmux popups are ON by default when tmux is present; disable with
	// LIVE_TMUX=0. Flags below still force-enable for convenience.
	wpEnv := os.Getenv("LIVE_WEB_PERMS")
	tmEnv := os.Getenv("LIVE_TMUX")
	webPerms := wpEnv != "0"
	tmuxWanted := tmEnv != "0"
	for len(args) > 0 {
		switch args[0] {
		case "--title":
			if len(args) < 2 {
				fmt.Fprintln(os.Stderr, "live: --title needs a value")
				return 2
			}
			title = args[1]
			args = args[2:]
		case "--web-permissions":
			webPerms = true
			args = args[1:]
		case "--no-permissions":
			webPerms = false
			args = args[1:]
		case "--":
			args = args[1:]
			goto parsed
		default:
			goto parsed
		}
	}
parsed:
	if len(args) == 0 {
		fmt.Print(usage)
		return 2
	}

	bin, userArgs := args[0], args[1:]
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
	sessionID, err := client.CreateSession(title, prof.framework, prof.framework, prof.captureMode)
	if err != nil {
		fmt.Fprintf(os.Stderr, "live: could not start session: %v\n", err)
		return 1
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
	if webPerms && hooks && !tmuxMode && !hasTmux && wpEnv == "" && tmEnv == "" {
		fmt.Fprint(os.Stderr, "\x1b[33m● live\x1b[0m tip: install tmux for in-terminal permission popups — using Claude's native prompts for now\n\n")
		webPerms = false
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

	newArgs := []string{"new-session", "-d", "-s", sess,
		"-e", "LIVE_SIDECAR=" + addr, "-e", "LIVE_TOKEN=" + token, "-e", "LIVE_SESSION_ID=" + sc.SessionID,
		// The PreToolUse relay blocks on the broker (popup or web); its HTTP
		// client must outlast the 8m decision grace, under the 10m hook.
		"-e", "LIVE_HOOK_TIMEOUT=590",
		binPath, "--settings", settings}
	newArgs = append(newArgs, userArgs...)
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
	// Channel delivery lets viewer messages reach Claude even while it is
	// idle — the only path that works without a hook boundary, so it is ON by
	// default for interactive sessions. Loading it needs Claude Code's
	// --dangerously-load-development-channels flag, which shows a "Loading
	// development channels" confirmation — but that approval is remembered
	// after you accept it once (verified 2026-07-06, Claude Code v2.1.201:
	// confirm on launch 1, launch 2 skips the dialog), so it is a one-time
	// hurdle, not a per-session one. Set LIVE_CHANNEL=0 to force pure
	// hook-boundary delivery. Skipped in -p/--print regardless (headless
	// Claude ignores channel messages; the hook + Stop-block path handles it).
	if os.Getenv("LIVE_CHANNEL") != "0" && !isPrintMode(userArgs) {
		if mcpCfg, mcpCleanup, err := adapter.WriteClaudeMCPConfig(self); err == nil {
			defer mcpCleanup()
			args = append(args, "--mcp-config", mcpCfg, "--strict-mcp-config",
				"--dangerously-load-development-channels", "server:liveshortly-channel")
		}
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
func isPrintMode(args []string) bool {
	for _, a := range args {
		if a == "-p" || a == "--print" {
			return true
		}
	}
	return false
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
