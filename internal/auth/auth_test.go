package auth

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeCreds plants a credential file in a temp HOME and points the config
// there, returning the path so a test can assert on its survival.
func writeCreds(t *testing.T, issuedFor string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	// host.json wins over the environment, so a leftover export in the test
	// runner's own shell cannot influence which host these tests target.
	t.Setenv("LIVESHORTLY_API_URL", "")
	t.Setenv("LIVESHORTLY_CRED_PATH", "")

	path := filepath.Join(home, ".liveshortly", "credentials.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"api_url":"` + issuedFor + `","access_token":"a","refresh_token":"r",` +
		`"expires_at":"2000-01-01T00:00:00Z"}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// pointHostAt writes host.json so hostcfg resolves to the given base.
func pointHostAt(t *testing.T, base string) {
	t.Helper()
	dir := filepath.Join(os.Getenv("HOME"), ".liveshortly")
	if err := os.WriteFile(filepath.Join(dir, "host.json"),
		[]byte(`{"api_url":"`+base+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A 401 from a DIFFERENT host than the one that issued the credentials is a
// misconfiguration, not a revocation. Deleting the file there destroys a
// working login for the real host — which is exactly what happened when a
// stale LIVESHORTLY_API_URL pointed a production refresh at a local stack.
func TestRefreshKeepsCredentialsOn401FromAnotherHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	path := writeCreds(t, "https://liveshortly.com") // issued by production
	pointHostAt(t, srv.URL)                          // but configured to talk to a local stack

	_, err := Refresh()
	if err == nil {
		t.Fatal("expected an error when refreshing against the wrong host")
	}
	if !strings.Contains(err.Error(), "issued for") {
		t.Errorf("error should name the mismatch, got: %v", err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatal("credentials were deleted by a 401 from a host that never issued them")
	}
}

// A 401 from the issuing host IS a revocation — drop the dead credentials.
func TestRefreshDeletesCredentialsOn401FromIssuer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	path := writeCreds(t, srv.URL) // issued by the same host we refresh against
	pointHostAt(t, srv.URL)

	if _, err := Refresh(); err != ErrNotLoggedIn {
		t.Fatalf("Refresh() error = %v, want ErrNotLoggedIn", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Error("a revoked token should have removed the credential file")
	}
}

// Credentials predating the api_url field must keep the old behavior rather
// than becoming un-deletable.
func TestRefreshDeletesWhenIssuerUnknown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	path := writeCreds(t, "") // no api_url recorded
	pointHostAt(t, srv.URL)

	if _, err := Refresh(); err != ErrNotLoggedIn {
		t.Fatalf("Refresh() error = %v, want ErrNotLoggedIn", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Error("with no recorded issuer, a 401 should still delete")
	}
}
