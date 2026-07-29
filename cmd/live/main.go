// live — stream any coding-agent session to LiveShortly.
//
//	live login | logout | whoami | version
//	live claude [claude args...]     capture via injected lifecycle hooks
//	live <any-command> [args...]     capture via PTY output recording
//	live hook <event>                internal: hook relay (spawned by agents)
package main

import (
	"fmt"
	"os"

	"github.com/liveshortly/live/internal/api"
	"github.com/liveshortly/live/internal/auth"
	"github.com/liveshortly/live/internal/hostcfg"
)

var version = "1.0.1" // overridden via -ldflags "-X main.version=..."

const usage = `live — stream your coding-agent session to LiveShortly

Usage:
  live login                 Sign in (browser device flow)
  live logout                Delete stored credentials
  live whoami                Show the signed-in account
  live version               Print version
  live update                Self-update to the latest release
  live update --version vX.Y.Z   Install a specific published version
  live uninstall             Remove the binary + sign out (add --purge for all state)

  live claude [args...]      Run Claude Code, captured via lifecycle hooks
  live <command> [args...]   Run any agent/CLI, captured via PTY recording
  live daemon [--dir <p>]    Let this machine start sessions from the web
  live -- <command> [...]    Same, unambiguous form
  live handoff <session-id>  Print the copyable command to continue a session
  live context [<id>]        Reprint a fork's prior-context digest

Continue (fork) an existing session as a NEW session you own:
  live claude --session <id>       Continue from a session's latest state
  live claude --handoff <code>     Continue from a pinned handoff snapshot
  The original session is untouched; the fork is a new session owned by you,
  and your agent orients itself from the prior context on launch. Works with
  any agent (live gemini --session <id>, …). --session/--handoff need read
  access to the source (a private, unshared session returns "no access").
  The history is printed in the agent's own terminal — scroll up to re-read it,
  press Ctrl-B h for a popup, or run "live context" from another shell.

Start sessions from the browser (no terminal needed):
  live daemon                      Register this machine and wait for requests
  live daemon --dir ~/code/api     Offer one specific directory (repeatable)
  Leave it running, then hit "+ New session" on the site and pick the machine,
  the agent and the directory. A web-started session is a normal session: same
  hooks, same permission gating. Take one over locally any time with
  "tmux -L liveshortly attach -t live-<first 8 chars of the session id>".
  Only the directories you pass to --dir (default: the current one) can ever be
  used, and only claude / codex / gemini can ever be started.

Permission control (always ON for live claude):
  You approve Claude's tool runs from a tmux popup (if tmux is installed) or
  the session page — allow / deny / allow-always, owner only. While on, it
  gates every tool, so your local allowlist is bypassed. Without tmux you
  approve from the session page. Use --no-permissions to turn it off for a run.

Options (before the command):
  --title <t>                Session title (default: current directory name)
  --no-permissions           Turn permission control off for this run
  --session <id>             Continue from an existing session (fork)
  --handoff <code>           Continue from a handoff snapshot (fork)
  --pre-digest               Pre-compress the handoff context with claude -p

Environment:
  LIVESHORTLY_API_URL        API base (default from ~/.liveshortly/host.json)
  LIVESHORTLY_CRED_PATH      Credential file (default ~/.liveshortly/credentials.json)
  LIVE_CHANNEL=0             Disable the viewer channel (instant idle comment
                             delivery); comments then arrive at hook boundaries
`

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "login":
		cmdLogin()
	case "logout":
		auth.Delete()
		fmt.Println("logged out")
	case "whoami":
		cmdWhoami()
	case "version", "--version", "-v":
		fmt.Println("live", version)
	case "update":
		cmdUpdate(os.Args[2:])
	case "uninstall":
		os.Exit(cmdUninstall(os.Args[2:]))
	case "help", "--help", "-h":
		fmt.Print(usage)
	case "hook":
		cmdHook(os.Args[2:])
	case "perm-prompt":
		cmdPermPrompt(os.Args[2:])
	case "channel":
		cmdChannel()
	case "daemon":
		os.Exit(cmdDaemon(os.Args[2:]))
	case "handoff":
		os.Exit(cmdHandoff(os.Args[2:]))
	case "context":
		os.Exit(cmdContext(os.Args[2:]))
	case "handoff-shim": // internal: prints prior context, then execs the agent
		os.Exit(cmdHandoffShim(os.Args[2:]))
	default:
		os.Exit(cmdRun(os.Args[1:]))
	}
}

// cmdHandoff mints a handoff code for a session and prints the copyable command
// that continues it as a new session. `live handoff <session-id>`.
func cmdHandoff(args []string) int {
	if len(args) < 1 || args[0] == "" {
		fmt.Fprintln(os.Stderr, "usage: live handoff <session-id>")
		return 2
	}
	id := args[0]
	if _, err := auth.AccessToken(); err != nil {
		fmt.Fprintln(os.Stderr, "live:", err)
		return 1
	}
	info, err := api.New().GenerateHandoff(id)
	if err != nil {
		if err == api.ErrForkForbidden {
			fmt.Fprintln(os.Stderr, "live: you don't have access to hand off this session")
			return 1
		}
		fmt.Fprintf(os.Stderr, "live: could not create handoff: %v\n", err)
		return 1
	}
	cmd := info.Command
	if cmd == "" && info.Code != "" {
		cmd = "live claude --handoff " + info.Code
	}
	if cmd == "" {
		fmt.Fprintln(os.Stderr, "live: server returned no handoff command")
		return 1
	}
	fmt.Println(cmd)
	return 0
}

func cmdLogin() {
	if c := auth.Load(); c != nil && c.User != nil {
		fmt.Printf("already signed in as %s — `live logout` first to switch accounts\n", c.User.Email)
		return
	}
	c, err := auth.DeviceLogin()
	if err != nil {
		fmt.Fprintln(os.Stderr, "login failed:", err)
		os.Exit(1)
	}
	who := "signed in"
	if c.User != nil && c.User.Email != "" {
		who = c.User.Email
	}
	fmt.Printf("✓ %s (%s)\n", who, hostcfg.APIURL())
}

func cmdWhoami() {
	c := auth.Load()
	if c == nil {
		fmt.Println("not signed in — run `live login`")
		os.Exit(1)
	}
	email, name := "", ""
	if c.User != nil {
		email, name = c.User.Email, c.User.Name
	}
	fmt.Printf("%s (%s)\napi: %s\n", name, email, hostcfg.APIURL())
	if _, err := auth.AccessToken(); err != nil {
		fmt.Println("token: INVALID — run `live login`")
		os.Exit(1)
	}
	fmt.Println("token: ok")
}
