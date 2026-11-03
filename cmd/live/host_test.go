package main

import (
	"fmt"
	"net"
	"strings"
	"syscall"
	"testing"

	"github.com/liveshortly/live/internal/auth"
)

func TestLoginHint(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no host.json: no reset hint
	refused := fmt.Errorf("device start: %w", &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED})
	cases := []struct {
		err  error
		api  string
		want string
	}{
		{refused, "http://localhost:8080", "local server that isn't running"},
		{refused, "https://liveshortly.com", "Can't reach liveshortly.com."},
		{&net.DNSError{Err: "no such host", Name: "x"}, "https://x.test", "Can't reach x.test."},
		{&auth.HTTPError{Op: "device start", Code: 502}, "https://liveshortly.com", "having trouble"},
		{&auth.HTTPError{Op: "device start", Code: 404}, "https://liveshortly.com", "too old"},
	}
	for _, c := range cases {
		if got := loginHint(c.err, c.api); !strings.Contains(got, c.want) {
			t.Errorf("loginHint(%v, %s) = %q, want %q", c.err, c.api, got, c.want)
		}
	}
	if got := loginHint(fmt.Errorf("device login timed out"), "https://liveshortly.com"); got != "" {
		t.Errorf("unknown error should get no hint, got %q", got)
	}
}
