package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/liveshortly/live/internal/api"
	"github.com/liveshortly/live/internal/sidecar"
)

// ── ollama through its own API (LSH-21) ─────────────────────────────────────
//
// `ollama run` captured through a terminal reads as noise on the web: its
// ">>> Send a message" placeholder, the echo of typed text, spinner frames
// and every partial redraw each became an activity row, and the reply never
// showed as an assistant message. Instead, `live ollama run <model>` holds
// the conversation itself and talks to the local ollama server
// (/api/chat, streaming): the operator types at a plain ">>> " prompt, viewer
// comments become real user turns, and each turn is emitted as one prompt,
// one response (and its reasoning, if any) and a stream_end.

// ollamaAPIArgs accepts exactly `run <model> [prompt…]`. Anything with a flag
// (--verbose, --format…) keeps the plain terminal path, which handles it.
func ollamaAPIArgs(args []string) (model, first string, ok bool) {
	if len(args) < 2 || args[0] != "run" || os.Getenv("LIVE_OLLAMA_PTY") == "1" {
		return "", "", false
	}
	for _, a := range args[1:] {
		if strings.HasPrefix(a, "-") {
			return "", "", false
		}
	}
	return args[1], strings.Join(args[2:], " "), true
}

// ollamaBase is the ollama server's URL, honoring OLLAMA_HOST the way the
// ollama CLI does ("host:port", a bare host, or a full URL; 0.0.0.0 means
// "listen everywhere", which the client reaches on loopback).
func ollamaBase() string {
	h := strings.TrimSpace(os.Getenv("OLLAMA_HOST"))
	if h == "" {
		return "http://127.0.0.1:11434"
	}
	if !strings.Contains(h, "://") {
		h = "http://" + h
	}
	h = strings.Replace(h, "://0.0.0.0", "://127.0.0.1", 1)
	if strings.Count(h, ":") < 2 { // no port
		h += ":11434"
	}
	return strings.TrimRight(h, "/")
}

// ollamaReachable waits briefly: the Ollama app may still be starting its
// server (it does on login, or when a previous `ollama run` launched it).
func ollamaReachable(base string) bool {
	c := &http.Client{Timeout: 2 * time.Second}
	for deadline := time.Now().Add(10 * time.Second); ; {
		if resp, err := c.Get(base + "/api/tags"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return true
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(500 * time.Millisecond)
	}
}

type ollamaMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ollamaTurn is one user message waiting for the model; viewer turns are
// already on the web as viewer_comment events, so they emit no prompt.
type ollamaTurn struct {
	text   string
	viewer bool
	who    string
}

func runOllamaAPIMode(sc *sidecar.Sidecar, base, model, first string) int {
	turns := make(chan ollamaTurn, 64)
	sc.OnComment = func(c api.Comment) {
		if t := strings.TrimSpace(c.Message); t != "" {
			select {
			case turns <- ollamaTurn{text: t, viewer: true, who: c.Username}:
			default:
				sc.Emit("input_requested", map[string]any{"message": "too many messages waiting; this one was not sent to the model", "kind": "input", "ts": nowRFC3339()})
			}
		}
	}
	done := make(chan struct{})
	var once sync.Once
	end := func() { once.Do(func() { close(done) }) }
	sc.OnEnded = func() { end() }
	sc.Start(context.Background())

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() { <-sigs; end() }()

	interactive := !headless()
	prompt := func() {
		if interactive {
			fmt.Print("\x1b[2m>>> \x1b[0m")
		}
	}
	fmt.Fprintf(os.Stderr, "\x1b[32m● live\x1b[0m chatting with %s through ollama (%s) — type a message, /bye to quit\n", model, base)

	if interactive {
		go func() {
			in := bufio.NewScanner(os.Stdin)
			in.Buffer(make([]byte, 64*1024), 1024*1024)
			for in.Scan() {
				line := strings.TrimSpace(in.Text())
				switch {
				case line == "":
					prompt()
				case line == "/bye" || line == "/exit":
					end()
					return
				case strings.HasPrefix(line, "/"):
					fmt.Println("only /bye works here (live talks to ollama through its API)")
					prompt()
				default:
					turns <- ollamaTurn{text: line}
				}
			}
			end() // EOF
		}()
	}

	var history []ollamaMsg
	if first != "" {
		turns <- ollamaTurn{text: first}
	}
	prompt()
	for {
		select {
		case <-done:
			fmt.Println()
			return 0
		case t := <-turns:
			if t.viewer {
				who := t.who
				if who == "" {
					who = "viewer"
				}
				fmt.Printf("\r\x1b[2K\x1b[36m%s (viewer)\x1b[0m %s\n", who, t.text)
			} else {
				sc.Emit("prompt", map[string]any{"content": t.text, "ts": nowRFC3339()})
			}
			history = append(history, ollamaMsg{Role: "user", Content: t.text})
			reply, err := ollamaChat(sc, base, model, history, done)
			if err != nil {
				fmt.Fprintf(os.Stderr, "\nlive: ollama: %v\n", err)
				sc.Emit("input_requested", map[string]any{"message": "ollama: " + err.Error(), "kind": "input", "ts": nowRFC3339()})
				history = history[:len(history)-1] // the turn didn't happen
			} else {
				history = append(history, ollamaMsg{Role: "assistant", Content: reply})
			}
			prompt()
		}
	}
}

// ollamaChat streams one reply to the terminal and emits it as reasoning (if
// any), response and stream_end. It returns the reply text for the history.
func ollamaChat(sc *sidecar.Sidecar, base, model string, history []ollamaMsg, done <-chan struct{}) (string, error) {
	body, _ := json.Marshal(map[string]any{"model": model, "messages": history, "stream": true})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-done:
			cancel()
		case <-ctx.Done():
		}
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return "", fmt.Errorf("%s", e.Error)
	}

	sc.Emit("stream_start", map[string]any{"ts": nowRFC3339()})
	last := time.Now()
	var content, thinking strings.Builder
	dec := json.NewDecoder(resp.Body)
	for {
		var chunk struct {
			Message struct {
				Content  string `json:"content"`
				Thinking string `json:"thinking"`
			} `json:"message"`
			Done  bool   `json:"done"`
			Error string `json:"error"`
		}
		if err := dec.Decode(&chunk); err != nil {
			if ctx.Err() != nil {
				return content.String(), nil // session ended mid-reply
			}
			return "", err
		}
		if chunk.Error != "" {
			return "", fmt.Errorf("%s", chunk.Error)
		}
		if chunk.Message.Thinking != "" {
			thinking.WriteString(chunk.Message.Thinking)
			fmt.Print("\x1b[2m" + chunk.Message.Thinking + "\x1b[0m")
		}
		if chunk.Message.Content != "" {
			content.WriteString(chunk.Message.Content)
			fmt.Print(chunk.Message.Content)
		}
		// Keep the web's "working" badge lit while tokens stream (it clears
		// on silence, see runPTYMode).
		if time.Since(last) >= 2*time.Second {
			last = time.Now()
			sc.Emit("stream_start", map[string]any{"ts": nowRFC3339()})
		}
		if chunk.Done {
			break
		}
	}
	fmt.Println()

	reasoning, reply := splitThink(thinking.String(), content.String())
	if reasoning != "" {
		sc.Emit("reasoning", map[string]any{"content": reasoning, "ts": nowRFC3339()})
	}
	if reply != "" {
		sc.Emit("response", map[string]any{"content": reply, "ts": nowRFC3339()})
	}
	sc.Emit("stream_end", map[string]any{"ts": nowRFC3339()})
	return content.String(), nil
}

// splitThink separates reasoning from the answer. Newer ollama sends it in
// message.thinking; models served without that still inline it as a
// leading <think>…</think> block in the content.
func splitThink(thinking, content string) (reasoning, reply string) {
	reasoning, reply = strings.TrimSpace(thinking), strings.TrimSpace(content)
	if strings.HasPrefix(reply, "<think>") {
		if i := strings.Index(reply, "</think>"); i >= 0 {
			inline := strings.TrimSpace(reply[len("<think>"):i])
			reply = strings.TrimSpace(reply[i+len("</think>"):])
			if reasoning == "" {
				reasoning = inline
			}
		}
	}
	return reasoning, reply
}

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339) }
