package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"syscall"

	"github.com/liveshortly/live/internal/auth"
	"github.com/liveshortly/live/internal/hostcfg"
)

// cmdHost shows or switches the server this machine talks to (LSI-15).
//
//	live host              print the server and where it comes from
//	live host set <url>    write ~/.liveshortly/host.json
//	live host reset        delete host.json: back to https://liveshortly.com
func cmdHost(args []string) int {
	switch {
	case len(args) == 0:
	case args[0] == "set" && len(args) == 2:
		if err := hostcfg.SetHost(args[1]); err != nil {
			fmt.Fprintln(os.Stderr, "live:", err)
			return 2
		}
	case args[0] == "reset" && len(args) == 1:
		if err := hostcfg.ResetHost(); err != nil {
			fmt.Fprintln(os.Stderr, "live:", err)
			return 1
		}
	default:
		fmt.Fprintln(os.Stderr, "usage: live host [set <url> | reset]")
		return 2
	}
	fmt.Printf("%s (%s)\n", hostcfg.APIURL(), hostcfg.Source())
	return 0
}

// loginHint turns a device-login error into one plain sentence plus the next
// step. The raw error is still printed above it for debugging.
func loginHint(err error, api string) string {
	host := api
	if u, perr := url.Parse(api); perr == nil && u.Host != "" {
		host = u.Host
	}
	var dns *net.DNSError
	var nerr net.Error
	var httpErr *auth.HTTPError
	var unknownCA x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var certErr *tls.CertificateVerificationError
	switch {
	case errors.Is(err, syscall.ECONNREFUSED), errors.As(err, &dns),
		errors.As(err, &nerr) && nerr.Timeout():
		msg := "Can't reach " + host + "."
		if isLocal(host) {
			msg += " Your config points to a local server that isn't running."
		}
		if hostcfg.Source() == "host.json" {
			return msg + " Check your connection, or run `live host reset` to switch to liveshortly.com."
		}
		return msg + " Check your connection."
	case errors.As(err, &certErr), errors.As(err, &unknownCA), errors.As(err, &hostname),
		errors.As(err, &httpErr) && httpErr.Code >= 500:
		return "LiveShortly is having trouble. Try again shortly."
	case errors.As(err, &httpErr):
		return "This version of live is too old for the server. Update live (`live update`)."
	}
	return ""
}

func isLocal(host string) bool {
	h := host
	if hh, _, err := net.SplitHostPort(host); err == nil {
		h = hh
	}
	h = strings.Trim(h, "[]")
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}
