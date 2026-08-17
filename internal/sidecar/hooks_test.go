package sidecar

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liveshortly/live/internal/api"
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

func TestAgyPreInvocation(t *testing.T) {
	s := testSidecar(api.Comment{Username: "alice", Message: "can you check the tests?"})
	payload := map[string]any{
		"modelName": "gemini-3.7-flash-high",
		"userMessage": "implement feature X",
	}
	out := s.handleHook("pre-invocation", payload)
	if out == nil {
		t.Fatal("expected hook output")
	}
	injectSteps, ok := out["injectSteps"].([]any)
	if !ok || len(injectSteps) != 1 {
		t.Fatalf("expected 1 injectStep, got %v", out["injectSteps"])
	}
	step, _ := injectSteps[0].(map[string]any)
	msg, _ := step["ephemeralMessage"].(string)
	if !strings.Contains(msg, "alice: can you check the tests?") {
		t.Errorf("ephemeralMessage missing comment: %q", msg)
	}
}

func TestAgyPreToolUse(t *testing.T) {
	s := testSidecar()
	payload := map[string]any{
		"toolCall": map[string]any{
			"name": "run_command",
			"args": map[string]any{
				"CommandLine": "go test ./...",
			},
		},
	}
	out := s.handleHook("pre-tool-use", payload)
	if out == nil || out["decision"] != "allow" {
		t.Fatalf("expected decision allow, got %v", out)
	}
}

func TestAgyPreToolUsePermissions(t *testing.T) {
	s := testSidecar()
	s.WebPermissions = true
	s.permRemember("Bash:npm")

	payload := map[string]any{
		"toolCall": map[string]any{
			"name": "run_command",
			"args": map[string]any{
				"CommandLine": "npm install",
			},
		},
	}
	out := s.handleHook("pre-tool-use", payload)
	if out == nil || out["decision"] != "allow" {
		t.Fatalf("expected auto-allowed decision for remembered key, got %v", out)
	}
}

func TestAgyPostToolUse(t *testing.T) {
	s := testSidecar()
	payload := map[string]any{
		"toolCall": map[string]any{
			"name": "write_to_file",
			"args": map[string]any{
				"TargetFile":  "hello.txt",
				"CodeContent": "hello world",
			},
		},
	}
	out := s.handleHook("post-tool-use", payload)
	if out == nil {
		t.Fatal("expected empty map for post-tool-use")
	}
}

func TestAgyStop(t *testing.T) {
	s := testSidecar(api.Comment{Username: "bob", Message: "looks good"})
	payload := map[string]any{
		"fullyIdle": true,
		"modelName": "gemini-3.7-flash-high",
	}
	out := s.handleHook("stop", payload)
	if out == nil || out["decision"] != "continue" {
		t.Fatalf("expected decision continue when comments pending, got %v", out)
	}
	reason, _ := out["reason"].(string)
	if !strings.Contains(reason, "bob: looks good") {
		t.Errorf("reason missing comment: %q", reason)
	}

	// Next stop without comments allows
	out2 := s.handleHook("stop", payload)
	if out2 == nil || out2["decision"] != "allow" {
		t.Fatalf("expected decision allow when queue empty, got %v", out2)
	}
}

func TestAgyTranscriptExtraction(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "agy-transcript-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	transcriptPath := filepath.Join(tmpDir, "transcript.jsonl")
	content := `{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","content":"<USER_REQUEST>\nmake it work with agy\n</USER_REQUEST>"}
{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","tool_calls":[{"name":"run_command","args":{"CommandLine":"echo 1"}}]}
{"step_index":2,"source":"MODEL","type":"RUN_COMMAND","content":"Output:\n1\n"}
{"step_index":3,"source":"MODEL","type":"PLANNER_RESPONSE","content":"All done!"}
`
	if err := os.WriteFile(transcriptPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	prompt := lastUserPromptFromTranscript(transcriptPath)
	if prompt != "make it work with agy" {
		t.Errorf("prompt = %q, want %q", prompt, "make it work with agy")
	}

	resp := lastAssistantResponseFromTranscript(transcriptPath)
	if resp != "All done!" {
		t.Errorf("resp = %q, want %q", resp, "All done!")
	}

	toolOut := toolOutputFromTranscript(transcriptPath, 2)
	if toolOut != "1" {
		t.Errorf("toolOut = %q, want %q", toolOut, "1")
	}
}

func TestAgyViewerTurnSkipped(t *testing.T) {
	if !isViewerTurn("alice (live viewer): refactor code") {
		t.Fatal("expected viewerTurn to match")
	}
	if isViewerTurn("just a normal user prompt") {
		t.Fatal("normal prompt should not match viewerTurn")
	}
}

func TestQueueForHooks(t *testing.T) {
	s := testSidecar()
	s.QueueForHooks(api.Comment{Username: "charlie", Message: "run the linter"})
	pending := s.takePending()
	if len(pending) != 1 || pending[0].Username != "charlie" {
		t.Fatalf("expected 1 comment from charlie, got %v", pending)
	}
}
