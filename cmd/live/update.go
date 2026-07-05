package main

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/resapce/live/internal/hostcfg"
)

// cmdUpdate implements `live update [--version vX.Y.Z]`: self-update from the
// release binaries published under <web>/install/ (see Makefile `publish`).
// Without --version it upgrades to latest.txt; with it, any published version
// can be installed, including downgrades.
func cmdUpdate(args []string) {
	requested := ""
	for len(args) > 0 {
		switch args[0] {
		case "--version", "-V":
			if len(args) < 2 {
				fmt.Fprintln(os.Stderr, "live: --version needs a value (e.g. v0.1.1)")
				os.Exit(2)
			}
			requested = args[1]
			args = args[2:]
		default:
			fmt.Fprintf(os.Stderr, "live: unknown update flag %q\n", args[0])
			os.Exit(2)
		}
	}

	base := installBase()
	hc := &http.Client{Timeout: 120 * time.Second}

	target := requested
	if target == "" {
		latest, err := fetchString(hc, base+"/latest.txt")
		if err != nil {
			fmt.Fprintf(os.Stderr, "live: could not resolve latest version: %v\n", err)
			os.Exit(1)
		}
		target = latest
	}
	target = "v" + strings.TrimPrefix(strings.TrimSpace(target), "v")

	if requested == "" && strings.TrimPrefix(target, "v") == strings.TrimPrefix(version, "v") {
		fmt.Printf("live %s is already the latest version\n", version)
		return
	}

	self, err := os.Executable()
	if err == nil {
		self, _ = filepath.EvalSymlinks(self)
	}
	if err != nil || self == "" {
		fmt.Fprintln(os.Stderr, "live: cannot locate own binary:", err)
		os.Exit(1)
	}
	if strings.Contains(self, "/Cellar/") {
		fmt.Fprintln(os.Stderr, "note: this copy is managed by Homebrew — `brew upgrade resapce/tap/live` is the cleaner path. Continuing anyway.")
	}

	asset := fmt.Sprintf("live-%s-%s-%s", target, runtime.GOOS, runtime.GOARCH)
	fmt.Printf("downloading %s …\n", asset)

	wantSum, err := fetchString(hc, base+"/"+asset+".sha256")
	if err != nil {
		fmt.Fprintf(os.Stderr, "live: version %s not published for %s/%s (%v)\n", target, runtime.GOOS, runtime.GOARCH, err)
		os.Exit(1)
	}

	resp, err := hc.Get(base + "/" + asset + ".gz")
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
			err = fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		fmt.Fprintf(os.Stderr, "live: download failed: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		fmt.Fprintln(os.Stderr, "live: bad archive:", err)
		os.Exit(1)
	}

	// Stage next to the real binary so the final rename is atomic.
	tmp, err := os.CreateTemp(filepath.Dir(self), ".live-update-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "live: cannot write to %s: %v\n", filepath.Dir(self), err)
		os.Exit(1)
	}
	defer os.Remove(tmp.Name())

	sum := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, sum), gz); err != nil {
		tmp.Close()
		fmt.Fprintln(os.Stderr, "live: download failed:", err)
		os.Exit(1)
	}
	tmp.Close()

	if got := hex.EncodeToString(sum.Sum(nil)); got != strings.Fields(wantSum)[0] {
		fmt.Fprintf(os.Stderr, "live: checksum mismatch (got %s, want %s) — aborting\n", got, wantSum)
		os.Exit(1)
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "live:", err)
		os.Exit(1)
	}

	// Sanity check: the staged binary must run and report the right version.
	out, err := exec.Command(tmp.Name(), "version").Output()
	if err != nil || !strings.Contains(string(out), strings.TrimPrefix(target, "v")) {
		fmt.Fprintf(os.Stderr, "live: staged binary failed verification (%q, %v) — aborting\n", strings.TrimSpace(string(out)), err)
		os.Exit(1)
	}

	if err := os.Rename(tmp.Name(), self); err != nil {
		fmt.Fprintf(os.Stderr, "live: could not replace %s: %v\n(try: sudo live update)\n", self, err)
		os.Exit(1)
	}
	fmt.Printf("✓ live %s → %s (%s)\n", version, target, self)
}

// installBase is the URL prefix the release binaries are served from.
func installBase() string {
	if v := strings.TrimSpace(os.Getenv("LIVE_INSTALL_BASE")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return hostcfg.WebURL() + "/install"
}

func fetchString(hc *http.Client, url string) (string, error) {
	resp, err := hc.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d for %s", resp.StatusCode, url)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return "", fmt.Errorf("empty response from %s", url)
	}
	return s, nil
}
