package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/liveshortly/live/internal/auth"
)

// cmdUninstall removes the installed live binary and signs you out. With
// --purge it also deletes the whole ~/.liveshortly state directory (host.json,
// logs, source checkout, cached icon). Reinstall any time with the install.sh
// one-liner. Note: live wires no persistent Claude hooks — its hook settings
// are written to an ephemeral temp file per session and cleaned up on exit —
// so there is nothing to unregister from ~/.claude.
func cmdUninstall(args []string) int {
	purge, yes := false, false
	for _, a := range args {
		switch a {
		case "--purge":
			purge = true
		case "-y", "--yes":
			yes = true
		case "-h", "--help":
			fmt.Print("usage: live uninstall [--purge] [--yes]\n\n" +
				"  Removes the live binary and signs you out.\n" +
				"  --purge   also delete ~/.liveshortly (state, logs, source checkout)\n" +
				"  --yes,-y  skip the confirmation prompt\n")
			return 0
		default:
			fmt.Fprintf(os.Stderr, "live: unknown flag %q (try: live uninstall --help)\n", a)
			return 2
		}
	}

	self, _ := os.Executable()
	home, _ := os.UserHomeDir()
	stateDir := filepath.Join(home, ".liveshortly")

	fmt.Println("live uninstall will remove:")
	if self != "" {
		fmt.Printf("  • binary       %s\n", self)
	}
	if purge {
		fmt.Printf("  • all state    %s  (credentials, host.json, logs, source)\n", stateDir)
	} else {
		fmt.Printf("  • credentials  %s  (sign out)\n", filepath.Join(stateDir, "credentials.json"))
	}

	if !yes {
		fmt.Print("\nproceed? [y/N] ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if s := strings.ToLower(strings.TrimSpace(line)); s != "y" && s != "yes" {
			fmt.Println("aborted — nothing removed")
			return 1
		}
	}

	// 1. Credentials, or the whole state dir under --purge.
	if purge {
		if err := os.RemoveAll(stateDir); err != nil {
			fmt.Fprintf(os.Stderr, "live: could not remove %s: %v\n", stateDir, err)
		} else {
			fmt.Printf("✓ removed %s\n", stateDir)
		}
	} else {
		auth.Delete()
		fmt.Println("✓ signed out (removed credentials.json)")
	}

	// Clean up agy plugin if agy is present
	if agyBin, err := exec.LookPath("agy"); err == nil {
		_ = exec.Command(agyBin, "plugin", "uninstall", "liveshortly").Run()
	}

	// 2. The binary itself. On Unix a running executable can be unlinked — the
	//    inode survives until this process exits — so removing self is safe.
	//    A root-owned install (e.g. /usr/local/bin) needs sudo; guide the user.
	if self != "" {
		if err := os.Remove(self); err != nil {
			fmt.Fprintf(os.Stderr, "live: could not remove %s: %v\n", self, err)
			fmt.Fprintf(os.Stderr, "      remove it manually: sudo rm %q\n", self)
		} else {
			fmt.Printf("✓ removed %s\n", self)
		}
	}

	fmt.Println("\ndone. reinstall any time:")
	fmt.Println("  curl -fsSL https://raw.githubusercontent.com/liveshortly/live-dist/main/install.sh | bash")
	if !purge {
		fmt.Println("\n(kept ~/.liveshortly — run `live uninstall --purge` to remove everything)")
	}
	return 0
}
