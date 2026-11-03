// Package hostcfg resolves the LiveShortly API/web endpoints and credential
// path. It reads the same files the plugin ecosystem uses (~/.liveshortly/
// host.json, ~/.liveshortly/credentials.json) so an existing login keeps
// working with the shim.
package hostcfg

import (
	"encoding/json"
	"fmt"
	"net/url"
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

func hostPath() string { return filepath.Join(home(), ".liveshortly", "host.json") }

func loadHost() hostFile {
	var h hostFile
	b, err := os.ReadFile(hostPath())
	if err == nil {
		_ = json.Unmarshal(b, &h)
	}
	return h
}

// DefaultAPIURL is the hosted service (D131: hosted first, self-host
// first-class). Self-hosters point elsewhere with host.json or
// LIVESHORTLY_API_URL; web and API share one origin, so WebURL follows.
const DefaultAPIURL = "https://liveshortly.com"

// APIURL returns the API origin. Order: host.json, LIVESHORTLY_API_URL, hosted
// default. host.json deliberately wins: it is the explicit switch (`make
// use-local` / `use-server` write it), whereas the env var is often left
// exported from old debugging — v1.3.0 inverted this and a stale export
// silently redirected every command, token refresh included.
func APIURL() string {
	if h := loadHost(); h.APIURL != "" {
		return strings.TrimRight(h.APIURL, "/")
	}
	if v := strings.TrimSpace(os.Getenv("LIVESHORTLY_API_URL")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return DefaultAPIURL
}

// Source names where APIURL came from: "host.json", "LIVESHORTLY_API_URL" or
// "default" — shown by `live host` / `live login` so a stale override is visible.
func Source() string {
	if loadHost().APIURL != "" {
		return "host.json"
	}
	if strings.TrimSpace(os.Getenv("LIVESHORTLY_API_URL")) != "" {
		return "LIVESHORTLY_API_URL"
	}
	return "default"
}

// SetHost points this machine at a single-origin server by writing host.json
// (`live host set <url>`). The URL must be absolute http(s).
func SetHost(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("not an http(s) URL: %q", raw)
	}
	origin := strings.TrimRight(u.String(), "/")
	b, _ := json.Marshal(hostFile{APIURL: origin, WebURL: origin})
	if err := os.MkdirAll(filepath.Dir(hostPath()), 0o700); err != nil {
		return err
	}
	return os.WriteFile(hostPath(), append(b, '\n'), 0o600)
}

// ResetHost removes host.json so the hosted default applies again
// (`live host reset`). A missing file is already reset.
func ResetHost() error {
	if err := os.Remove(hostPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
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

// AgentsPath is the optional adapter file listing extra agent harnesses this
// machine can run (see cmd/live/agents.go). Absent on most machines, where the
// probed builtins are enough.
func AgentsPath() string {
	return filepath.Join(home(), ".liveshortly", "agents.json")
}
