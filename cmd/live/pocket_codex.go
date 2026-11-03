package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

func runPocketCodex(parent context.Context, dir, prompt string) (string, error) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	type turnResult struct {
		text string
		err  error
	}
	completed := make(chan turnResult, 1)
	var output strings.Builder // Only the reader goroutine writes; completion synchronizes the read.
	c := &codexApp{directory: dir, pending: map[int]chan rpcResp{}}
	c.notification = func(method string, raw json.RawMessage) {
		switch method {
		case "item/completed":
			var item codexItem
			if json.Unmarshal(raw, &item) == nil && item.Item.Type == "agentMessage" && output.Len() < 256<<10 {
				output.WriteString(item.Item.Text)
				output.WriteString("\n")
			}
		case "turn/completed":
			var p struct {
				Turn struct {
					Status string `json:"status"`
					Error  *struct {
						Message string `json:"message"`
					} `json:"error"`
				} `json:"turn"`
			}
			_ = json.Unmarshal(raw, &p)
			var err error
			if p.Turn.Error != nil {
				err = fmt.Errorf("Codex: %s", p.Turn.Error.Message)
			} else if p.Turn.Status == "failed" || p.Turn.Status == "interrupted" {
				err = fmt.Errorf("Codex turn %s", p.Turn.Status)
			}
			select {
			case completed <- turnResult{strings.TrimSpace(output.String()), err}:
			default:
			}
		}
	}
	c.serverRequest = func(id json.RawMessage, method string, raw json.RawMessage) {
		go func() {
			switch method {
			case "item/commandExecution/requestApproval", "item/fileChange/requestApproval", "execCommandApproval", "applyPatchApproval":
				tool := "Shell"
				if strings.Contains(method, "fileChange") || method == "applyPatchApproval" {
					tool = "Edit files"
				}
				decision := "decline"
				var options struct {
					AvailableDecisions []json.RawMessage `json:"availableDecisions"`
				}
				_ = json.Unmarshal(raw, &options)
				if len(options.AvailableDecisions) > 0 {
					hasDecline := false
					for _, option := range options.AvailableDecisions {
						if string(option) == `"decline"` {
							hasDecline = true
						}
					}
					if !hasDecline {
						decision = "cancel"
					}
				}
				if pocketAsk(ctx, tool, prettyPocketJSON(raw)) == "allow" {
					decision = "accept"
				}
				if method == "execCommandApproval" || method == "applyPatchApproval" {
					if decision == "accept" {
						decision = "approved"
					} else {
						decision = "denied"
					}
				}
				c.respond(id, map[string]any{"decision": decision})
			case "item/permissions/requestApproval":
				permissions := json.RawMessage(`{}`)
				if pocketAsk(ctx, "Additional permissions (this turn)", prettyPocketJSON(raw)) == "allow" {
					var p struct {
						Permissions json.RawMessage `json:"permissions"`
					}
					_ = json.Unmarshal(raw, &p)
					if len(p.Permissions) > 0 {
						permissions = p.Permissions
					}
				}
				c.respond(id, map[string]any{"permissions": permissions, "scope": "turn"})
			default:
				// Unsupported interactive requests fail explicitly instead of hanging.
				_ = c.write(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32601, "message": "This request is not supported by pocket.ai: " + method}})
			}
		}()
	}
	if err := c.start(ctx, "codex", "app-server"); err != nil {
		return "", err
	}
	defer func() { cancel(); <-c.done }()
	thread, err := c.handshake("pocket_ai", dir, "", "")
	if err != nil {
		return "", err
	}
	id := threadIDOf(thread)
	if id == "" {
		return "", fmt.Errorf("Codex returned no thread ID")
	}
	if _, err = c.request("turn/start", map[string]any{"threadId": id, "input": []any{map[string]any{"type": "text", "text": prompt}}}); err != nil {
		return "", err
	}
	select {
	case result := <-completed:
		return result.text, result.err
	case <-ctx.Done():
		return "", ctx.Err()
	case <-c.done:
		return "", fmt.Errorf("Codex exited before completing the turn (code %d)", c.exitCode())
	}
}
