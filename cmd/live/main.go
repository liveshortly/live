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

	"github.com/resapce/live/internal/auth"
	"github.com/resapce/live/internal/hostcfg"
)

var version = "0.2.0" // overridden via -ldflags "-X main.version=..."

const usage = `live — stream your coding-agent session to LiveShortly

Usage:
  live login                 Sign in (browser device flow)
  live logout                Delete stored credentials
  live whoami                Show the signed-in account
  live version               Print version
  live update                Self-update to the latest release
  live update --version vX.Y.Z   Install a specific published version

  live claude [args...]      Run Claude Code, captured via lifecycle hooks
  live <command> [args...]   Run any agent/CLI, captured via PTY recording
  live -- <command> [...]    Same, unambiguous form

Options (before the command):
  --title <t>                Session title (default: current directory name)

Environment:
  LIVESHORTLY_API_URL        API base (default from ~/.liveshortly/host.json)
  LIVESHORTLY_CRED_PATH      Credential file (default ~/.liveshortly/credentials.json)
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
	case "help", "--help", "-h":
		fmt.Print(usage)
	case "hook":
		cmdHook(os.Args[2:])
	case "channel":
		cmdChannel()
	default:
		os.Exit(cmdRun(os.Args[1:]))
	}
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
