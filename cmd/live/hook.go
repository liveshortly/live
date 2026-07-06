package main

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"
)

// cmdHook is the relay the agent's hook system invokes (`live hook <event>`).
// It forwards the hook's stdin JSON to the parent shim's sidecar and prints
// the sidecar's hook-output JSON. It must be fast, silent, and always emit
// valid JSON + exit 0 so a broken sidecar can never wedge the agent.
func cmdHook(args []string) {
	out := relay(args)
	if len(bytes.TrimSpace(out)) == 0 {
		out = []byte("{}")
	}
	os.Stdout.Write(out)
	if !bytes.HasSuffix(out, []byte("\n")) {
		os.Stdout.WriteString("\n")
	}
}

func relay(args []string) []byte {
	if len(args) < 1 {
		return nil
	}
	addr := os.Getenv("LIVE_SIDECAR")
	token := os.Getenv("LIVE_TOKEN")
	if addr == "" || token == "" {
		return nil // not running under `live` — no-op
	}

	body, _ := io.ReadAll(io.LimitReader(os.Stdin, 16<<20))

	req, err := http.NewRequest("POST", addr+"/hook/"+args[0], bytes.NewReader(body))
	if err != nil {
		return nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Live-Token", token)

	// Default to a snappy timeout; web-permissions mode raises it via
	// LIVE_HOOK_TIMEOUT so the PreToolUse relay can block on a human verdict.
	timeout := 6 * time.Second
	if v := os.Getenv("LIVE_HOOK_TIMEOUT"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
			timeout = time.Duration(secs) * time.Second
		}
	}
	hc := &http.Client{Timeout: timeout}
	resp, err := hc.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	out, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil
	}
	return out
}
