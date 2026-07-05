package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/resapce/live/internal/adapter"
	"github.com/resapce/live/internal/api"
	"github.com/resapce/live/internal/auth"
	"github.com/resapce/live/internal/hostcfg"
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
	for len(args) > 0 {
		switch args[0] {
		case "--title":
			if len(args) < 2 {
				fmt.Fprintln(os.Stderr, "live: --title needs a value")
				return 2
			}
			title = args[1]
			args = args[2:]
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

	sc := &sidecar.Sidecar{
		Client:    client,
		SessionID: sessionID,
		WebURL:    hostcfg.WebURL(),
		Log:       logger,
	}

	var exitCode int
	switch prof.captureMode {
	case "hooks":
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
	settings, cleanup, err := adapter.WriteClaudeSettings(self)
	if err != nil {
		fmt.Fprintf(os.Stderr, "live: %v\n", err)
		return 1
	}
	defer cleanup()

	args := []string{"--settings", settings}
	// Channel delivery lets viewer messages reach Claude even while it is
	// idle — the only path that works without a hook boundary. It stays
	// OPT-IN (LIVE_CHANNEL=1) because loading it requires Claude Code's
	// --dangerously-load-development-channels flag, which shows a mandatory
	// interactive "Loading development channels" confirmation on EVERY launch
	// (verified 2026-07-05, Claude Code v2.1.201; it does not persist). Making
	// it default-on would put that scary prompt in front of every `live
	// claude` startup, breaking seamless capture. Without it, viewer messages
	// still deliver at hook boundaries + the Stop-block, plus a desktop
	// notification when Claude is idle. Skipped in -p/--print regardless.
	if os.Getenv("LIVE_CHANNEL") == "1" && !isPrintMode(userArgs) {
		if mcpCfg, mcpCleanup, err := adapter.WriteClaudeMCPConfig(self); err == nil {
			defer mcpCleanup()
			args = append(args, "--mcp-config", mcpCfg, "--strict-mcp-config",
				"--dangerously-load-development-channels", "server:liveshortly-channel")
		}
	}
	args = append(args, userArgs...)
	code, err := adapter.RunWithHooks(binPath, args, []string{
		"LIVE_SIDECAR=" + addr,
		"LIVE_TOKEN=" + token,
		"LIVE_SESSION_ID=" + sc.SessionID,
	})
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
