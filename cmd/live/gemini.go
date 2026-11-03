package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/liveshortly/live/internal/adapter"
	"github.com/liveshortly/live/internal/sidecar"
)

func isGemini(bin string) bool { return filepath.Base(bin) == "gemini" }

// geminiProfile captures gemini through its hooks (>= 0.26) and keeps raw PTY
// capture for older releases, which have no hook system.
func geminiProfile(bin string) agentProfile {
	if adapter.GeminiHooksSupported(bin) {
		return agentProfile{framework: "gemini-cli", captureMode: "hooks"}
	}
	return agentProfile{framework: "gemini-cli", captureMode: "pty"}
}

// geminiOwnApproval reports whether the operator chose gemini's approval mode
// themselves (--yolo / -y / --approval-mode …). live then stands down as the
// gate, the way it does for claude's --dangerously-skip-permissions.
func geminiOwnApproval(args []string) bool {
	for _, a := range args {
		if a == "--yolo" || a == "-y" || a == "--approval-mode" || strings.HasPrefix(a, "--approval-mode=") {
			return true
		}
	}
	return false
}

// geminiLaunch prepares a hooked gemini run: it installs the (inert) hooks
// extension and returns the args and the env that switch it on for this
// process only.
//
// With web permissions, live's BeforeTool hook is the gate. A hook "allow"
// does not skip gemini's own prompt (gemini checks policy after hooks), so
// gemini runs with --approval-mode=yolo and the gated hook decides; it fails
// closed (`live hook … --gated`). That trade is only made when the hook is
// certain to fire (adapter.GeminiGateBlocked); otherwise gemini's own prompt
// stays in charge. In an untrusted folder gemini refuses yolo by itself, so
// a tool is asked twice there (web, then gemini) — never zero times.
//
// A web-started (headless) run trusts the workspace for this process only: the
// daemon spawns solely in directories the operator registered, and nobody is
// there to answer gemini's trust dialog.
func geminiLaunch(sc *sidecar.Sidecar, self string, userArgs []string) (args, env []string, err error) {
	if err := adapter.EnsureGeminiExtension(); err != nil {
		return nil, nil, fmt.Errorf("gemini hooks extension: %w", err)
	}
	args = adapter.GeminiArgs(userArgs)
	env = []string{"LIVE_BIN=" + self}
	if headless() {
		env = append(env, "GEMINI_CLI_TRUST_WORKSPACE=true")
	}
	if sc.WebPermissions {
		cwd, _ := os.Getwd()
		if why := adapter.GeminiGateBlocked(cwd, args); why != "" {
			sc.WebPermissions = false
			fmt.Fprintf(os.Stderr, "\x1b[33m● live\x1b[0m gemini's own prompts approve tools this run: %s\n\n", why)
			sc.Log.Printf("gemini: web approvals off: %s", why)
		} else {
			args = append([]string{"--approval-mode=yolo"}, args...)
			env = append(env, "LIVE_GEMINI_GATE=--gated")
		}
	}
	return args, env, nil
}
