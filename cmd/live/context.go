package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/liveshortly/live/internal/hostcfg"
)

// digestPathFor resolves an argument to a handoff digest file. It accepts a
// session id (the common case) or a literal path, and falls back to the session
// this process belongs to — which is what the tmux popup binding passes.
func digestPathFor(arg string) string {
	if arg == "" {
		arg = os.Getenv("LIVE_SESSION_ID")
	}
	if arg == "" {
		return ""
	}
	if strings.ContainsRune(arg, filepath.Separator) || strings.HasSuffix(arg, ".md") {
		return arg
	}
	return filepath.Join(hostcfg.HandoffDir(), arg+".md")
}

// cmdContext prints the prior-context digest of a forked session. The history is
// printed once when the agent starts, but the agent's TUI scrolls it away; this
// brings it back without leaving the session (`live context`, or the tmux popup
// bound to prefix + h).
func cmdContext(args []string) int {
	var arg string
	if len(args) > 0 {
		arg = args[0]
	}
	path := digestPathFor(arg)
	if path == "" {
		fmt.Fprintln(os.Stderr, "live: no session — pass a session id: live context <session-id>")
		return 2
	}
	md, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "live: no prior context for this session (not a fork?) — looked in %s\n", path)
			return 1
		}
		fmt.Fprintf(os.Stderr, "live: %v\n", err)
		return 1
	}
	os.Stdout.Write(md)
	if len(md) > 0 && md[len(md)-1] != '\n' {
		fmt.Println()
	}
	return 0
}

// cmdHandoffShim prints the prior context into the agent's own terminal, then
// REPLACES itself with the agent:
//
//	live handoff-shim <digest-path> -- <agent-bin> [args…]
//
// Why a shim rather than printing before launch: in tmux the agent is started in
// a detached session and the operator then attaches, which repaints the screen
// and erases anything printed outside the pane. Printing from inside the pane
// puts the history in that pane's scrollback, where it survives — neither the
// codex nor the claude TUI takes the alternate screen, so scrollback is real.
//
// exec (not fork) so the agent keeps the pane's pid and signals behave: the
// shim must not sit between tmux and the agent.
func cmdHandoffShim(args []string) int {
	sep := -1
	for i, a := range args {
		if a == "--" {
			sep = i
			break
		}
	}
	if sep < 1 || sep == len(args)-1 {
		fmt.Fprintln(os.Stderr, "usage: live handoff-shim <digest-path> -- <agent> [args…]")
		return 2
	}
	path, agent := args[0], args[sep+1:]

	// Best-effort: a missing digest must never stop the agent from starting.
	if md, err := os.ReadFile(path); err == nil {
		fmt.Printf("\x1b[36m● live\x1b[0m prior context — you are continuing a previous session\n\n")
		os.Stdout.Write(md)
		fmt.Printf("\n\x1b[2m── scroll up for the full history · `live context` to reprint · Ctrl-B h for a popup ──\x1b[0m\n\n")
	}

	bin, err := exec.LookPath(agent[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "live: %v\n", err)
		return 1
	}
	if err := syscall.Exec(bin, agent, os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "live: exec %s: %v\n", bin, err)
		return 1
	}
	return 0 // unreachable
}

// handoffShimArgs wraps an agent launch so the pane prints the prior context
// first. Returns the argv unchanged when this session is not a fork (no digest),
// so a normal run pays nothing.
func handoffShimArgs(self, sessionID string, agent []string) []string {
	path := filepath.Join(hostcfg.HandoffDir(), sessionID+".md")
	if _, err := os.Stat(path); err != nil {
		return agent
	}
	out := []string{self, "handoff-shim", path, "--"}
	return append(out, agent...)
}

// bindContextPopup binds prefix + h inside our private tmux session so the
// operator can pull the prior context back up at any time. run is the caller's
// tmux runner (both tmux modes have one). Best-effort: a binding failure is
// never worth failing a session over.
func bindContextPopup(run func(args ...string) error, self, sessionID string) {
	path := filepath.Join(hostcfg.HandoffDir(), sessionID+".md")
	if _, err := os.Stat(path); err != nil {
		return // not a fork
	}
	cmd := fmt.Sprintf("%q context %q", self, path)
	_ = run("bind-key", "-T", "prefix", "h", "display-popup", "-E", "-w", "88%", "-h", "88%", cmd)
}
