package adapter

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGeminiVersionHasHooks(t *testing.T) {
	for v, want := range map[string]bool{
		"0.65.0\n": true, "0.26.0": true, "0.25.9": false, "1.0.0": true, "": false, "garbage": false,
	} {
		if got := geminiVersionHasHooks(v); got != want {
			t.Errorf("geminiVersionHasHooks(%q) = %v, want %v", v, got, want)
		}
	}
}

// isolate points every gemini settings scope at an empty temp tree.
func isolate(t *testing.T) (home, cwd string) {
	home, cwd = t.TempDir(), t.TempDir()
	t.Setenv("GEMINI_CLI_HOME", home)
	t.Setenv("GEMINI_CLI_SYSTEM_SETTINGS_PATH", filepath.Join(home, "none-sys.json"))
	t.Setenv("GEMINI_CLI_SYSTEM_DEFAULTS_PATH", filepath.Join(home, "none-def.json"))
	t.Setenv("GITHUB_SHA", "")
	t.Setenv("SURFACE", "")
	return home, cwd
}

func TestGeminiExtensionHooks(t *testing.T) {
	home, _ := isolate(t)
	if err := EnsureGeminiExtension(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(home, ".gemini", "extensions", "liveshortly", "hooks", "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string
				Timeout int
			}
		}
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	for _, ev := range []string{"SessionStart", "BeforeAgent", "BeforeModel", "BeforeTool", "AfterTool", "Notification", "AfterAgent"} {
		if len(f.Hooks[ev]) != 1 {
			t.Fatalf("%s hook missing", ev)
		}
	}
	bt := f.Hooks["BeforeTool"][0].Hooks[0]
	if bt.Timeout != 600_000 || !strings.HasSuffix(bt.Command, "hook gemini-before-tool $LIVE_GEMINI_GATE") {
		t.Fatalf("BeforeTool hook = %+v", bt)
	}

	// The command is run by bash -c: inert (`true`, no output) without live,
	// the gated relay under it.
	run := func(env ...string) string {
		c := exec.Command("bash", "-c", bt.Command)
		c.Env = append(os.Environ(), env...)
		out, _ := c.Output()
		return string(out)
	}
	if out := run("LIVE_BIN=", "LIVE_GEMINI_GATE="); out != "" {
		t.Fatalf("plain gemini must see no hook output, got %q", out)
	}
	if out := run("LIVE_BIN=echo", "LIVE_GEMINI_GATE=--gated"); out != "hook gemini-before-tool --gated\n" {
		t.Fatalf("live run must reach the gated relay, got %q", out)
	}
	if err := EnsureGeminiExtension(); err != nil { // idempotent
		t.Fatal(err)
	}
}

func TestGeminiGateBlocked(t *testing.T) {
	home, cwd := isolate(t)
	if why := GeminiGateBlocked(cwd, nil); why != "" {
		t.Fatalf("clean setup blocked: %s", why)
	}
	user := filepath.Join(home, ".gemini", "settings.json")
	os.MkdirAll(filepath.Dir(user), 0o755)
	for body, blocked := range map[string]bool{
		`{"hooksConfig":{"enabled":true}}`:                         false,
		`{"hooksConfig":{"enabled":false}}`:                        true,
		`{"hooksConfig":{"disabled":["liveshortly-before-tool"]}}`: true,
		`{"admin":{"extensions":{"enabled":false}}}`:               true,
		`{"security":{"allowedExtensions":["github.com/x/.*"]}}`:   true,
		`{ // comments can't be verified
		}`: true,
	} {
		os.WriteFile(user, []byte(body), 0o644)
		if got := GeminiGateBlocked(cwd, nil) != ""; got != blocked {
			t.Errorf("%s: blocked=%v, want %v", body, got, blocked)
		}
	}
	os.Remove(user)

	en := filepath.Join(home, ".gemini", "extensions", "extension-enablement.json")
	os.MkdirAll(filepath.Dir(en), 0o755)
	os.WriteFile(en, []byte(`{"liveshortly":{"overrides":["!/x/*"]}}`), 0o644)
	if GeminiGateBlocked(cwd, nil) == "" {
		t.Error("disabled extension must block")
	}
	if args := GeminiArgs([]string{"-e", "foo"}); GeminiGateBlocked(cwd, args) != "" || strings.Join(args, " ") != "-e foo -e liveshortly" {
		t.Errorf("-e list must gain liveshortly (and then it loads regardless): %v", args)
	}
	os.Remove(en)

	t.Setenv("GITHUB_SHA", "abc")
	if GeminiGateBlocked(cwd, nil) == "" {
		t.Error("GitHub Actions strips hook env; must block")
	}
}
