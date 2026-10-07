package hostcfg

import (
	"os"
	"path/filepath"
	"testing"
)

// writeHostFile plants a host.json in a temp HOME and points HOME at it.
func writeHostFile(t *testing.T, body string) {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, ".liveshortly")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "host.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
}

// host.json must win over the environment. It is the explicit, tool-managed
// switch (`make use-local` / `use-prod`); LIVESHORTLY_API_URL is routinely left
// exported in a shell profile from old debugging. v1.3.0 had this inverted, and
// a stale export in a .zshrc silently redirected every command — including token
// refresh — away from the production host the user had configured.
func TestHostFileOverridesEnv(t *testing.T) {
	writeHostFile(t, `{"api_url":"https://live.example.test","web_url":"https://live.example.test"}`)

	t.Setenv("LIVESHORTLY_API_URL", "http://localhost:8000/")
	t.Setenv("LIVESHORTLY_WEB_URL", "http://localhost:3000/")

	if got := APIURL(); got != "https://live.example.test" {
		t.Errorf("APIURL = %q, want the host.json value — a stale env export must not hijack it", got)
	}
	if got := WebURL(); got != "https://live.example.test" {
		t.Errorf("WebURL = %q, want the host.json value", got)
	}
}

// With no host.json the env var is still the way to point at a stack.
func TestEnvUsedWhenNoHostFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("LIVESHORTLY_API_URL", "http://localhost:8080/")
	t.Setenv("LIVESHORTLY_WEB_URL", "http://localhost:8080/")

	if got := APIURL(); got != "http://localhost:8080" {
		t.Errorf("APIURL = %q, want the env value with the trailing slash trimmed", got)
	}
	if got := WebURL(); got != "http://localhost:8080" {
		t.Errorf("WebURL = %q, want the env value", got)
	}
}

func TestHostFileUsedWhenEnvUnset(t *testing.T) {
	writeHostFile(t, `{"api_url":"https://example.test","web_url":"https://web.example.test"}`)
	t.Setenv("LIVESHORTLY_API_URL", "")
	t.Setenv("LIVESHORTLY_WEB_URL", "")
	t.Setenv("LIVESHORTLY_FRONTEND_URL", "")

	if got := APIURL(); got != "https://example.test" {
		t.Errorf("APIURL = %q, want the host.json value", got)
	}
	if got := WebURL(); got != "https://web.example.test" {
		t.Errorf("WebURL = %q, want the host.json value", got)
	}
}

// A fresh install must not contact the retired hosted service.
func TestDefaultsToSelfHostedStack(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("LIVESHORTLY_API_URL", "")
	t.Setenv("LIVESHORTLY_WEB_URL", "")
	t.Setenv("LIVESHORTLY_FRONTEND_URL", "")

	if got := APIURL(); got != DefaultAPIURL {
		t.Errorf("APIURL = %q, want %q", got, DefaultAPIURL)
	}
	if got := WebURL(); got != DefaultAPIURL {
		t.Errorf("WebURL = %q, want %q", got, DefaultAPIURL)
	}
}
