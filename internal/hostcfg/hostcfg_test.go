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

// The env var must win over host.json. Overriding HOME to get a different
// host.json is not an equivalent workaround: it also relocates ~/.claude and
// ~/.codex, which drops the agent into its first-run wizard — invisible and
// unanswerable for a session started from the web.
func TestEnvOverridesHostFile(t *testing.T) {
	writeHostFile(t, `{"api_url":"https://liveshortly.com","web_url":"https://liveshortly.com"}`)

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

// With nothing configured at all, live must reach production — it ships to
// consumers, so a missing config file is not a reason to talk to localhost.
func TestDefaultsToProduction(t *testing.T) {
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
