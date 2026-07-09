package sidecar

import (
	"io"
	"log"
	"strings"
	"testing"

	"github.com/LiveShortly/live/internal/api"
)

func testSidecar(pending ...api.Comment) *Sidecar {
	return &Sidecar{
		Log:     log.New(io.Discard, "", 0),
		pending: pending,
		// events left nil: Emit's select falls through to drop, which is
		// fine for handler-logic tests.
	}
}

func TestStopBlocksWhenCommentsPending(t *testing.T) {
	s := testSidecar(api.Comment{Username: "rohit", Message: "add a smiley"})
	out := s.handleHook("stop", map[string]any{})
	if out == nil || out["decision"] != "block" {
		t.Fatalf("expected stop to block, got %v", out)
	}
	reason, _ := out["reason"].(string)
	if !strings.Contains(reason, "rohit: add a smiley") {
		t.Fatalf("reason missing comment: %q", reason)
	}
	if len(s.takePending()) != 0 {
		t.Fatal("pending queue should be drained by the block")
	}
	// Second stop with empty queue must not block (no loop).
	if out2 := s.handleHook("stop", map[string]any{}); out2 != nil && out2["decision"] == "block" {
		t.Fatalf("empty-queue stop must not block, got %v", out2)
	}
}

func TestInjectCommentsIsVisibleAndInjected(t *testing.T) {
	s := testSidecar(api.Comment{Username: "", Message: "hello there"})
	out := s.handleHook("user-prompt-submit", map[string]any{"prompt": "hi"})
	if out == nil {
		t.Fatal("expected hook output with pending comments")
	}
	sysMsg, _ := out["systemMessage"].(string)
	if !strings.Contains(sysMsg, "delivered to Claude") {
		t.Fatalf("operator receipt missing: %q", sysMsg)
	}
	hso, _ := out["hookSpecificOutput"].(map[string]any)
	ctx, _ := hso["additionalContext"].(string)
	if !strings.Contains(ctx, "viewer: hello there") {
		t.Fatalf("additionalContext missing comment (anon → viewer): %q", ctx)
	}
	if hso["hookEventName"] != "UserPromptSubmit" {
		t.Fatalf("wrong hookEventName: %v", hso["hookEventName"])
	}
}

func TestNoCommentsMeansNoHookOutput(t *testing.T) {
	s := testSidecar()
	if out := s.handleHook("user-prompt-submit", map[string]any{"prompt": "hi"}); out != nil {
		t.Fatalf("expected nil output with empty queue, got %v", out)
	}
}
