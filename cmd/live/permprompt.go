package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"golang.org/x/term"
)

// cmdPermPrompt is the tmux popup UI (`live perm-prompt <sidecarAddr> <token>`).
// It shows the pending tool-permission and reads one keystroke — y allow /
// a allow-always / n deny — posting the answer to the sidecar. If the web
// answers first, it auto-closes.
func cmdPermPrompt(args []string) {
	if len(args) < 2 {
		return
	}
	addr, token := args[0], args[1]
	hc := &http.Client{Timeout: 4 * time.Second}

	get := func() (pending bool, tool, detail string) {
		req, _ := http.NewRequest("GET", addr+"/perm/pending", nil)
		req.Header.Set("X-Live-Token", token)
		resp, err := hc.Do(req)
		if err != nil {
			return false, "", ""
		}
		defer resp.Body.Close()
		var out struct {
			Pending bool   `json:"pending"`
			Tool    string `json:"tool"`
			Detail  string `json:"detail"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return out.Pending, out.Tool, out.Detail
	}

	pending, tool, detail := get()
	if !pending {
		return // already resolved (e.g. from the web)
	}

	fmt.Printf("\r\n  \x1b[33mClaude wants to run \x1b[1m%s\x1b[0m", tool)
	if detail != "" {
		if len(detail) > 120 {
			detail = detail[:120] + "…"
		}
		fmt.Printf("\x1b[33m: %s\x1b[0m", detail)
	}
	fmt.Print("\r\n\r\n  \x1b[32m[y]\x1b[0m allow    \x1b[32m[a]\x1b[0m allow always    \x1b[31m[n]\x1b[0m deny\r\n\r\n  > ")

	// raw mode so single keystrokes register
	var restore func()
	if term.IsTerminal(int(os.Stdin.Fd())) {
		if old, err := term.MakeRaw(int(os.Stdin.Fd())); err == nil {
			restore = func() { term.Restore(int(os.Stdin.Fd()), old) }
		}
	}

	keyCh := make(chan byte, 1)
	go func() {
		b := make([]byte, 1)
		for {
			n, err := os.Stdin.Read(b)
			if n > 0 {
				switch b[0] {
				case 'y', 'Y', 'a', 'A', 'n', 'N', 3: // 3 = Ctrl-C
					keyCh <- b[0]
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// close the popup if the web answers first
	gone := make(chan struct{})
	go func() {
		t := time.NewTicker(500 * time.Millisecond)
		defer t.Stop()
		for range t.C {
			if p, _, _ := get(); !p {
				close(gone)
				return
			}
		}
	}()

	var decision string
	select {
	case k := <-keyCh:
		switch k {
		case 'y', 'Y':
			decision = "allow"
		case 'a', 'A':
			decision = "always"
		default:
			decision = "deny"
		}
	case <-gone:
		if restore != nil {
			restore()
		}
		return // answered elsewhere
	}
	if restore != nil {
		restore()
	}

	body, _ := json.Marshal(map[string]string{"decision": decision})
	req, _ := http.NewRequest("POST", addr+"/perm/answer", bytes.NewReader(body))
	req.Header.Set("X-Live-Token", token)
	req.Header.Set("Content-Type", "application/json")
	if resp, err := hc.Do(req); err == nil {
		resp.Body.Close()
	}
}
