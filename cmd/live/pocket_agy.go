package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// AGY can exit zero without a text reply when a tool turn is interrupted. Read
// its structured terminal status rather than treating every zero exit as success.
type pocketAgyOutput struct {
	line     []byte
	overflow bool
	text     pocketBuffer
	result   *struct {
		Status        string            `json:"status"`
		Response      string            `json:"response"`
		Error         json.RawMessage   `json:"error"`
		DeniedActions []json.RawMessage `json:"denied_actions"`
	}
}

func (o *pocketAgyOutput) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		end := bytes.IndexByte(p, '\n')
		take := len(p)
		if end >= 0 {
			take = end
		}
		if len(o.line)+take <= 1<<20 && !o.overflow {
			o.line = append(o.line, p[:take]...)
		} else {
			o.overflow = true
		}
		p = p[take:]
		if end < 0 {
			break
		}
		if !o.overflow {
			o.consume()
		}
		o.line = nil
		o.overflow = false
		p = p[1:]
	}
	return n, nil
}
func (o *pocketAgyOutput) consume() {
	var event struct {
		Event  string          `json:"event"`
		Result json.RawMessage `json:"result"`
		Step   struct {
			Type  string `json:"step_type"`
			Delta string `json:"text_delta"`
		} `json:"step_update"`
	}
	if json.Unmarshal(o.line, &event) != nil {
		return
	}
	if event.Event == "step_update" && event.Step.Type == "agent_response" {
		o.text.Write([]byte(event.Step.Delta))
	}
	if event.Event == "result" {
		_ = json.Unmarshal(event.Result, &o.result)
	}
}
func (o *pocketAgyOutput) answer() (string, error) {
	if len(o.line) > 0 && !o.overflow {
		o.consume()
	}
	if o.result == nil {
		return "", fmt.Errorf("AGY ended without a result. Check its CLI login and server log.")
	}
	if len(o.result.DeniedActions) > 0 {
		return "", fmt.Errorf("AGY denied the requested action: %s", string(o.result.DeniedActions[0]))
	}
	if o.result.Status != "SUCCESS" {
		return "", fmt.Errorf("AGY turn %s: %s", o.result.Status, string(o.result.Error))
	}
	text := strings.TrimSpace(o.result.Response)
	if text == "" {
		text = strings.TrimSpace(o.text.b.String())
	}
	if text == "" {
		text = "AGY completed the turn without a text reply."
	}
	return text, nil
}
