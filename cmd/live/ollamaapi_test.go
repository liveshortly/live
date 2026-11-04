package main

import "testing"

func TestOllamaAPIArgs(t *testing.T) {
	if m, f, ok := ollamaAPIArgs([]string{"run", "phi4", "hello", "there"}); !ok || m != "phi4" || f != "hello there" {
		t.Fatalf("run with prompt: %q %q %v", m, f, ok)
	}
	for _, args := range [][]string{{"list"}, {"run"}, {"run", "phi4", "--verbose"}, {"pull", "x"}} {
		if _, _, ok := ollamaAPIArgs(args); ok {
			t.Errorf("%v should keep the terminal path", args)
		}
	}
}

func TestSplitThink(t *testing.T) {
	r, a := splitThink("", "<think>\nhmm\n</think>\n\nPONG")
	if r != "hmm" || a != "PONG" {
		t.Fatalf("inline: %q %q", r, a)
	}
	r, a = splitThink("why", "PONG")
	if r != "why" || a != "PONG" {
		t.Fatalf("field: %q %q", r, a)
	}
}

func TestOllamaBase(t *testing.T) {
	for in, want := range map[string]string{
		"":                       "http://127.0.0.1:11434",
		"0.0.0.0":                "http://127.0.0.1:11434",
		"0.0.0.0:9999":           "http://127.0.0.1:9999",
		"https://ollama.lan:443": "https://ollama.lan:443",
	} {
		t.Setenv("OLLAMA_HOST", in)
		if got := ollamaBase(); got != want {
			t.Errorf("OLLAMA_HOST=%q: got %q want %q", in, got, want)
		}
	}
}
