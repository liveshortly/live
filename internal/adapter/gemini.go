// The gemini adapter wires live's capture hooks into Gemini CLI (>= 0.26;
// gemini-cli docs/hooks/reference.md, docs/extensions/reference.md).
//
// Gemini has no per-run --settings flag, and the system-settings override
// (GEMINI_CLI_SYSTEM_SETTINGS_PATH) is ignored unless the file sits in a
// root-owned directory. Hooks are therefore shipped as a small extension in
// ~/.gemini/extensions/liveshortly. It is inert outside live: every hook
// command is "${LIVE_BIN:-true}" …, so a plain `gemini` runs `true` (no
// output = no objection) and only a process launched by `live gemini`, which
// sets LIVE_BIN, reaches the relay. Per-run behaviour (sidecar address, the
// approval gate) travels in that process's environment.
package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

// GeminiExtension is the extension's name (and its directory).
const GeminiExtension = "liveshortly"

// geminiHooksMinor is the first Gemini CLI minor (0.x) that ships hooks.
const geminiHooksMinor = 26

var semverRe = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

// GeminiHooksSupported reports whether the gemini at bin has lifecycle hooks.
// An unknown version counts as unsupported.
func GeminiHooksSupported(bin string) bool {
	return geminiVersionHasHooks(GeminiVersion(bin))
}

func geminiVersionHasHooks(v string) bool {
	m := semverRe.FindStringSubmatch(v)
	if m == nil {
		return false
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	return major > 0 || minor >= geminiHooksMinor
}

// GeminiVersion reads the version from the npm package.json beside the
// resolved launcher (instant), falling back to `gemini --version`, which
// boots node and takes seconds.
func GeminiVersion(bin string) string {
	if p, err := exec.LookPath(bin); err == nil {
		if real, err := filepath.EvalSymlinks(p); err == nil {
			dir := filepath.Dir(real)
			for i := 0; i < 3; i++ { // bundle/gemini.js → package root
				var pkg struct{ Name, Version string }
				if b, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil &&
					json.Unmarshal(b, &pkg) == nil && pkg.Name == "@google/gemini-cli" {
					return pkg.Version
				}
				dir = filepath.Dir(dir)
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, bin, "--version").Output()
	return string(out)
}

// geminiDir is gemini's user directory (GEMINI_CLI_HOME relocates it).
func geminiDir() string {
	home := os.Getenv("GEMINI_CLI_HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return filepath.Join(home, ".gemini")
}

// geminiHook is one hook definition. Gemini runs it through bash -c, so the
// shell expands ${LIVE_BIN:-true} and $LIVE_GEMINI_GATE per process.
func geminiHook(name, args string, timeoutMs int) []any {
	return []any{map[string]any{"hooks": []any{map[string]any{
		"name":    "liveshortly-" + name,
		"type":    "command",
		"command": `"${LIVE_BIN:-true}" hook ` + args,
		"timeout": timeoutMs,
	}}}}
}

// geminiHooksJSON is the extension's hooks/hooks.json. BeforeTool gets 10
// minutes, past the relay's 590s HTTP timeout: gemini treats a hook timeout as
// a warning and RUNS the tool, so the relay must give up (and deny) first.
func geminiHooksJSON() []byte {
	b, _ := json.MarshalIndent(map[string]any{"hooks": map[string]any{
		"SessionStart": geminiHook("session-start", "gemini-session-start", 15_000),
		"BeforeAgent":  geminiHook("before-agent", "gemini-before-agent", 10_000),
		"BeforeModel":  geminiHook("before-model", "gemini-before-model", 5_000),
		"BeforeTool":   geminiHook("before-tool", "gemini-before-tool $LIVE_GEMINI_GATE", 600_000),
		"AfterTool":    geminiHook("after-tool", "gemini-after-tool", 10_000),
		"Notification": geminiHook("notification", "gemini-notification", 10_000),
		"AfterAgent":   geminiHook("after-agent", "gemini-after-agent", 15_000),
	}}, "", "  ")
	return b
}

const geminiManifest = `{
  "name": "liveshortly",
  "version": "1.0.0",
  "description": "LiveShortly capture hooks. Inert unless gemini is started by ` + "`live gemini`" + `."
}
`

// EnsureGeminiExtension writes (or refreshes) the liveshortly extension.
// The content is static, so concurrent sessions and plain gemini runs share it.
func EnsureGeminiExtension() error {
	dir := filepath.Join(geminiDir(), "extensions", GeminiExtension)
	files := map[string][]byte{
		filepath.Join(dir, "gemini-extension.json"): []byte(geminiManifest),
		filepath.Join(dir, "hooks", "hooks.json"):   geminiHooksJSON(),
	}
	for path, want := range files {
		if got, err := os.ReadFile(path); err == nil && bytes.Equal(got, want) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, want, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// RemoveGeminiExtension deletes the extension (live uninstall).
func RemoveGeminiExtension() error {
	return os.RemoveAll(filepath.Join(geminiDir(), "extensions", GeminiExtension))
}

// geminiSettingsFiles are every settings scope gemini merges
// (docs/reference/configuration.md).
func geminiSettingsFiles(cwd string) []string {
	sysDir := "/etc/gemini-cli"
	switch runtime.GOOS {
	case "darwin":
		sysDir = "/Library/Application Support/GeminiCli"
	case "windows":
		sysDir = `C:\ProgramData\gemini-cli`
	}
	sys := os.Getenv("GEMINI_CLI_SYSTEM_SETTINGS_PATH")
	if sys == "" {
		sys = filepath.Join(sysDir, "settings.json")
	}
	defaults := os.Getenv("GEMINI_CLI_SYSTEM_DEFAULTS_PATH")
	if defaults == "" {
		defaults = filepath.Join(sysDir, "system-defaults.json")
	}
	return []string{sys, defaults, filepath.Join(geminiDir(), "settings.json"), filepath.Join(cwd, ".gemini", "settings.json")}
}

// GeminiGateBlocked returns why live's BeforeTool hook can't be trusted to
// fire in a gemini started in cwd with args, or "" when it can. Live only
// skips gemini's own prompt (--approval-mode=yolo) when this is "", because
// yolo without the hook would run every tool unreviewed. Any doubt — an
// unreadable settings file included — blocks.
//
// ponytail: static checks against gemini 0.65's settings; a gemini that
// disables hooks some new way would slip past. Upgrade path: a SessionStart
// handshake before the first prompt is accepted, if gemini ever offers one.
func GeminiGateBlocked(cwd string, args []string) string {
	// Gemini strips every non-allow-listed variable (LIVE_BIN included) from
	// hook environments under GitHub Actions; the hook would then run `true`.
	if os.Getenv("GITHUB_SHA") != "" || os.Getenv("SURFACE") == "Github" {
		return "gemini sanitizes hook environments under GitHub Actions"
	}
	for _, path := range geminiSettingsFiles(cwd) {
		b, err := os.ReadFile(path)
		if err != nil {
			continue // absent scope
		}
		var s struct {
			HooksConfig struct {
				Enabled  *bool
				Disabled []string
			}
			Admin    struct{ Extensions struct{ Enabled *bool } }
			Security struct {
				AllowedExtensions []string
			}
		}
		if err := json.Unmarshal(b, &s); err != nil {
			return fmt.Sprintf("can't read %s to confirm hooks are on: %v", path, err)
		}
		switch {
		case s.HooksConfig.Enabled != nil && !*s.HooksConfig.Enabled:
			return "hooks are disabled (hooksConfig.enabled=false in " + path + ")"
		case slices.Contains(s.HooksConfig.Disabled, "liveshortly-before-tool"):
			return "the liveshortly-before-tool hook is disabled in " + path
		case s.Admin.Extensions.Enabled != nil && !*s.Admin.Extensions.Enabled:
			return "extensions are disabled by admin settings in " + path
		case len(s.Security.AllowedExtensions) > 0:
			return "security.allowedExtensions in " + path + " admits only installed extensions"
		}
	}
	// `-e` picks extensions for the run (live appends its own); without it,
	// a `gemini extensions disable liveshortly` entry would switch it off.
	if !geminiExtensionsFlag(args) {
		var en map[string]any
		if b, err := os.ReadFile(filepath.Join(geminiDir(), "extensions", "extension-enablement.json")); err == nil &&
			json.Unmarshal(b, &en) == nil && en[GeminiExtension] != nil {
			return "the liveshortly extension has enable/disable overrides (gemini extensions enable liveshortly)"
		}
	}
	return ""
}

// geminiExtensionsFlag reports whether args choose extensions with -e.
func geminiExtensionsFlag(args []string) bool {
	for _, a := range args {
		if a == "-e" || a == "--extensions" || strings.HasPrefix(a, "-e=") || strings.HasPrefix(a, "--extensions=") {
			return true
		}
	}
	return false
}

// GeminiArgs makes sure the run loads the liveshortly extension: an explicit
// `-e` list replaces the enabled set, so live's extension is added to it.
func GeminiArgs(args []string) []string {
	if geminiExtensionsFlag(args) {
		return append(slices.Clone(args), "-e", GeminiExtension)
	}
	return args
}
