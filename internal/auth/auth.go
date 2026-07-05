// Package auth implements the LiveShortly device-flow login and the shared
// credential store at ~/.liveshortly/credentials.json (see AUTH.md in the
// LiveShortly repo — this file format is a contract shared with the plugin).
package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/resapce/live/internal/hostcfg"
)

// User is the signed-in identity as returned by the device poll.
type User struct {
	Email string `json:"email,omitempty"`
	Name  string `json:"name,omitempty"`
}

// Creds mirrors the credentials.json contract.
type Creds struct {
	APIURL       string `json:"api_url,omitempty"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresAt    string `json:"expires_at,omitempty"`
	User         *User  `json:"user,omitempty"`
}

var httpc = &http.Client{Timeout: 15 * time.Second}

// ErrNotLoggedIn is returned when no usable credentials exist.
var ErrNotLoggedIn = errors.New("not signed in — run `live login`")

// Load reads credentials.json; nil when missing or unreadable.
func Load() *Creds {
	b, err := os.ReadFile(hostcfg.CredPath())
	if err != nil {
		return nil
	}
	var c Creds
	if json.Unmarshal(b, &c) != nil || c.AccessToken == "" {
		return nil
	}
	return &c
}

// Save writes credentials.json with mode 0600.
func Save(c *Creds) error {
	path := hostcfg.CredPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

// Delete removes the credential file (logout).
func Delete() {
	_ = os.Remove(hostcfg.CredPath())
}

// AccessToken returns a valid access token, refreshing when it is within 60s
// of expiry. Returns ErrNotLoggedIn when there is nothing usable.
func AccessToken() (string, error) {
	c := Load()
	if c == nil {
		return "", ErrNotLoggedIn
	}
	if exp, err := time.Parse(time.RFC3339, c.ExpiresAt); err == nil {
		if time.Now().After(exp.Add(-60 * time.Second)) {
			return Refresh()
		}
	}
	return c.AccessToken, nil
}

// Refresh mints a fresh access token from the stored refresh token and
// rewrites the credential file. A 401 (revoked/expired refresh token) deletes
// the file, matching the plugin's behavior.
func Refresh() (string, error) {
	c := Load()
	if c == nil || c.RefreshToken == "" {
		return "", ErrNotLoggedIn
	}
	body, _ := json.Marshal(map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": c.RefreshToken,
	})
	resp, err := httpc.Post(hostcfg.APIURL()+"/auth/token", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("token refresh: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		Delete()
		return "", ErrNotLoggedIn
	}
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("token refresh: HTTP %d", resp.StatusCode)
	}
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.AccessToken == "" {
		return "", fmt.Errorf("token refresh: bad response")
	}
	c.AccessToken = out.AccessToken
	if out.RefreshToken != "" {
		c.RefreshToken = out.RefreshToken
	}
	if out.ExpiresIn > 0 {
		c.ExpiresAt = time.Now().UTC().Add(time.Duration(out.ExpiresIn) * time.Second).Format(time.RFC3339)
	}
	if err := Save(c); err != nil {
		return "", err
	}
	return c.AccessToken, nil
}

// DeviceLogin runs the browser device flow: POST /auth/device/start, open the
// approval page, poll /auth/device/poll until approved, save credentials.
func DeviceLogin() (*Creds, error) {
	api := hostcfg.APIURL()
	resp, err := httpc.Post(api+"/auth/device/start", "application/json", bytes.NewReader([]byte("{}")))
	if err != nil {
		return nil, fmt.Errorf("device start: %w", err)
	}
	defer resp.Body.Close()
	var start struct {
		DeviceCode              string `json:"device_code"`
		UserCode                string `json:"user_code"`
		VerificationURI         string `json:"verification_uri"`
		VerificationURIComplete string `json:"verification_uri_complete"`
		Interval                int    `json:"interval"`
		ExpiresIn               int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&start); err != nil || start.DeviceCode == "" {
		return nil, fmt.Errorf("device start: bad response (HTTP %d)", resp.StatusCode)
	}

	approveURL := start.VerificationURIComplete
	if approveURL == "" {
		approveURL = start.VerificationURI
	}
	fmt.Printf("Open to approve this device:\n\n    %s\n\n(code: %s)\n", approveURL, start.UserCode)
	openBrowser(approveURL)

	interval := time.Duration(max(start.Interval, 2)) * time.Second
	deadline := time.Now().Add(time.Duration(max(start.ExpiresIn, 60)) * time.Second)

	for time.Now().Before(deadline) {
		time.Sleep(interval)
		body, _ := json.Marshal(map[string]string{"device_code": start.DeviceCode})
		pr, err := httpc.Post(api+"/auth/device/poll", "application/json", bytes.NewReader(body))
		if err != nil {
			continue
		}
		var poll struct {
			Status       string `json:"status"`
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			ExpiresIn    int    `json:"expires_in"`
			User         *User  `json:"user"`
		}
		derr := json.NewDecoder(pr.Body).Decode(&poll)
		status := pr.StatusCode
		pr.Body.Close()
		if derr != nil || status >= 500 {
			continue
		}
		if status >= 400 {
			return nil, fmt.Errorf("device poll: HTTP %d (code expired or denied)", status)
		}
		if poll.AccessToken == "" {
			continue // still pending
		}
		c := &Creds{
			APIURL:       api,
			AccessToken:  poll.AccessToken,
			RefreshToken: poll.RefreshToken,
			User:         poll.User,
		}
		if poll.ExpiresIn > 0 {
			c.ExpiresAt = time.Now().UTC().Add(time.Duration(poll.ExpiresIn) * time.Second).Format(time.RFC3339)
		}
		if err := Save(c); err != nil {
			return nil, err
		}
		return c, nil
	}
	return nil, errors.New("device login timed out — try again")
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
