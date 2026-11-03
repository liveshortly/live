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

// A fresh install with no host.json and no env reaches the hosted service
// (D131). Literal on purpose: v2.3.0 shipped localhost:8080 here, so
// `live login` could not reach liveshortly.com.
func TestDefaultsToHostedService(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("LIVESHORTLY_API_URL", "")
	t.Setenv("LIVESHORTLY_WEB_URL", "")
	t.Setenv("LIVESHORTLY_FRONTEND_URL", "")

	const want = "https://liveshortly.com"
	if got := APIURL(); got != want {
		t.Errorf("APIURL = %q, want %q", got, want)
	}
	if got := WebURL(); got != want {
		t.Errorf("WebURL = %q, want %q", got, want)
	}
}

// `live host set` / `reset` (LSI-15): set wins over env, reset returns to the
// default, and junk URLs are refused.
func TestSetAndResetHost(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("LIVESHORTLY_API_URL", "")
	if err := SetHost("ftp://nope"); err == nil {
		t.Fatal("SetHost accepted a non-http URL")
	}
	if err := SetHost("http://localhost:8080/"); err != nil {
		t.Fatal(err)
	}
	if APIURL() != "http://localhost:8080" || WebURL() != "http://localhost:8080" || Source() != "host.json" {
		t.Fatalf("after set: %s %s %s", APIURL(), WebURL(), Source())
	}
	if err := ResetHost(); err != nil {
		t.Fatal(err)
	}
	if err := ResetHost(); err != nil {
		t.Fatal("second reset:", err)
	}
	if APIURL() != DefaultAPIURL || Source() != "default" {
		t.Fatalf("after reset: %s %s", APIURL(), Source())
	}
}
