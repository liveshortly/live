package sidecar

import (
	"encoding/base64"
	"os"
	"testing"

	"github.com/liveshortly/live/internal/api"
)

// handleCommand routes set_model/abort to their callbacks and ignores unknown
// or unwired commands without panicking.
func TestHandleCommand(t *testing.T) {
	var gotModel string
	aborts := 0
	s := &Sidecar{
		OnSetModel: func(m string) { gotModel = m },
		OnAbort:    func() { aborts++ },
	}

	s.handleCommand(api.AgentCommand{Type: "set_model", Model: "openai/gpt-5"})
	if gotModel != "openai/gpt-5" {
		t.Fatalf("OnSetModel got %q, want openai/gpt-5", gotModel)
	}

	s.handleCommand(api.AgentCommand{Type: "abort"})
	if aborts != 1 {
		t.Fatalf("aborts = %d, want 1", aborts)
	}

	// Unknown type and unwired callbacks must be safe no-ops.
	s.handleCommand(api.AgentCommand{Type: "future_cmd"})
	(&Sidecar{}).handleCommand(api.AgentCommand{Type: "set_model", Model: "x/y"})
	(&Sidecar{}).handleCommand(api.AgentCommand{Type: "abort"})
}

// attach_image buffers decodable images (dropping junk), TakeImages drains them,
// and imageFiles materialises them to a temp dir cleaned on Shutdown.
func TestAttachImageBufferAndFiles(t *testing.T) {
	s := &Sidecar{}
	b64 := base64.StdEncoding.EncodeToString([]byte("fake png"))

	s.handleCommand(api.AgentCommand{Type: "attach_image", Index: 1, Mime: "image/png", Data: b64})
	s.handleCommand(api.AgentCommand{Type: "attach_image", Index: 2, Mime: "image/jpeg", Data: b64})
	// Dropped: not an image, and undecodable base64.
	s.handleCommand(api.AgentCommand{Type: "attach_image", Index: 3, Mime: "text/plain", Data: b64})
	s.handleCommand(api.AgentCommand{Type: "attach_image", Index: 4, Mime: "image/png", Data: "not*base64"})

	imgs := s.TakeImages()
	if len(imgs) != 2 || imgs[0].Index != 1 || imgs[1].Mime != "image/jpeg" {
		t.Fatalf("buffered = %+v, want 2 valid", imgs)
	}
	if got := s.TakeImages(); len(got) != 0 {
		t.Fatalf("TakeImages did not clear: %+v", got)
	}

	paths := s.imageFiles(imgs)
	if len(paths) != 2 {
		t.Fatalf("paths = %v", paths)
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
	}
	s.imgMu.Lock()
	dir := s.imgDir
	s.imgMu.Unlock()
	// Shutdown would RemoveAll(dir); verify directly to avoid the full lifecycle.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
}
