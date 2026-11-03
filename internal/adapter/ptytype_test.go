package adapter

import (
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestReadyBlocker(t *testing.T) {
	r := Ready{Idle: time.Second}
	cases := []struct {
		name, scr string
		idle      time.Duration
		started   bool
		want      string
	}{
		{"idle at prompt", "aider v0.82\n> ", 2 * time.Second, true, ""},
		{"not started", "", 0, false, "the agent is busy"},
		{"still streaming", "> ", 100 * time.Millisecond, true, "the agent is busy"},
		{"mid-turn hint", "⠙ Thinking (esc to interrupt)\n> ", 5 * time.Second, true, "the agent is busy"},
		{"yes/no dialog", "Add file to the chat? (Y)es/(N)o [Yes]:", 5 * time.Second, true, "the agent is waiting for an answer in the terminal"},
		{"trust dialog", "Do you trust the files in this folder?", 5 * time.Second, true, "the agent is waiting for an answer in the terminal"},
	}
	for _, c := range cases {
		if got := r.blocker(c.scr, c.idle, c.started); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}

	r.Pattern = regexp.MustCompile(`(?m)^> ?$`)
	if got := r.blocker("loading…", 5*time.Second, true); got != "the agent's prompt is not on screen" {
		t.Errorf("pattern unmatched: got %q", got)
	}
	if got := r.blocker("hi\n> ", 5*time.Second, true); got != "" {
		t.Errorf("pattern matched: got %q", got)
	}
	r.Hold = func() bool { return true }
	if got := r.blocker("> ", 5*time.Second, true); got != "the operator is typing" {
		t.Errorf("hold: got %q", got)
	}
}

func TestScreenKeepsLastLines(t *testing.T) {
	got := screen([]byte("1\n2\n3\n\n\n4\n5\n6\n7\n\x1b[1m> \x1b[0m"))
	if got != "3\n4\n5\n6\n7\n>" {
		t.Errorf("screen = %q", got)
	}
}

// A message that arrives before the agent is up waits for its prompt, is
// typed, echoed and submitted exactly once (LSH-19).
func TestTypeLineWaitsForPromptAndSubmits(t *testing.T) {
	p := &PTY{}
	errc := make(chan error, 1)
	go func() {
		errc <- p.TypeLine("viewer (live viewer): hello there", Ready{Idle: 300 * time.Millisecond}, nil)
	}()
	var out []byte
	done := make(chan struct{})
	var mu sync.Mutex
	go func() {
		defer close(done)
		_, _ = p.Run("sh", []string{"-c", `sleep 0.3; printf 'ready> '; read l; echo "got:[$l]"; sleep 1`}, func(b []byte) {
			mu.Lock()
			out = append(out, b...)
			mu.Unlock()
		})
	}()
	if err := <-errc; err != nil {
		t.Fatalf("TypeLine: %v", err)
	}
	<-done
	if !strings.Contains(string(out), "got:[viewer (live viewer): hello there]") {
		t.Fatalf("line not delivered once; output %q", out)
	}
}

// Text the agent never echoes is not submitted: Enter could answer something
// we can't see.
func TestTypeLineWithoutEchoIsNotSubmitted(t *testing.T) {
	defer func(d time.Duration) { AcceptWait = d }(AcceptWait)
	AcceptWait = 500 * time.Millisecond
	p := &PTY{}
	go func() { _, _ = p.Run("sh", []string{"-c", `stty -echo; printf '> '; sleep 3`}, nil) }()
	err := p.TypeLine("hello", Ready{Idle: 200 * time.Millisecond}, nil)
	if err == nil || !strings.Contains(err.Error(), "not sent") {
		t.Fatalf("want a not-sent error, got %v", err)
	}
	p.Stop()
}

// Stop ends the agent, so Run returns and the session process can exit.
func TestPTYStop(t *testing.T) {
	p := &PTY{}
	ret := make(chan struct{})
	go func() { _, _ = p.Run("sh", []string{"-c", `printf 'up'; sleep 30`}, nil); close(ret) }()
	for _, _, started, _ := p.state(); !started; _, _, started, _ = p.state() {
		time.Sleep(20 * time.Millisecond)
	}
	p.Stop()
	select {
	case <-ret:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after Stop")
	}
}
