// Package hostcfg resolves the LiveShortly API/web endpoints and credential
// path. It reads the same files the plugin ecosystem uses (~/.liveshortly/
// host.json, ~/.liveshortly/credentials.json) so an existing login keeps
// working with the shim.
package hostcfg

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

type hostFile struct {
	APIURL string `json:"api_url"`
	WebURL string `json:"web_url"`
}

func home() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return h
}

func loadHost() hostFile {
	var h hostFile
	b, err := os.ReadFile(filepath.Join(home(), ".liveshortly", "host.json"))
	if err == nil {
		_ = json.Unmarshal(b, &h)
	}
	return h
}

// DefaultAPIURL is the single-origin self-hosted web stack. Explicit host.json
// settings still win over shell environment variables during migration.
const DefaultAPIURL = "http://localhost:8080"

// APIURL returns the configured API origin: host.json, environment, local stack.
func APIURL() string {
	if h := loadHost(); h.APIURL != "" {
		return strings.TrimRight(h.APIURL, "/")
	}
	if v := strings.TrimSpace(os.Getenv("LIVESHORTLY_API_URL")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return DefaultAPIURL
}

// WebURL returns the explicit browser origin, or the API origin for a
// single-origin self-hosted installation. host.json wins, as in APIURL.
func WebURL() string {
	if h := loadHost(); h.WebURL != "" {
		return strings.TrimRight(h.WebURL, "/")
	}
	for _, k := range []string{"LIVESHORTLY_WEB_URL", "LIVESHORTLY_FRONTEND_URL"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return strings.TrimRight(v, "/")
		}
	}
	return APIURL()
}

// CredPath returns the credentials file path (shared with the plugin).
func CredPath() string {
	if v := strings.TrimSpace(os.Getenv("LIVESHORTLY_CRED_PATH")); v != "" {
		if strings.HasPrefix(v, "~/") {
			return filepath.Join(home(), v[2:])
		}
		return v
	}
	return filepath.Join(home(), ".liveshortly", "credentials.json")
}

// LogPath is where the shim writes diagnostics while an agent owns the
// terminal (we must never print over a TUI).
func LogPath() string {
	return filepath.Join(home(), ".liveshortly", "live.log")
}

// HandoffDir is where the shim stores handoff digests (one markdown file per
// forked session) that get handed to the new agent as opening context.
func HandoffDir() string {
	return filepath.Join(home(), ".liveshortly", "handoffs")
}

// HostIDPath holds this machine's stable daemon id, so restarting `live daemon`
// re-registers the same host instead of accumulating duplicates in the picker.
// The id is only meaningful inside the owning account (server keys are
// namespaced by user), so it is an identifier, not a secret.
func HostIDPath() string {
	return filepath.Join(home(), ".liveshortly", "host_id")
}
