package main

import "testing"

func TestNewScreenLines(t *testing.T) {
	prev := "π pi\n────\n> \n$0.00 status"
	cur := "π pi\nPONG\n────\n> \n$0.01 status"
	if got, want := newScreenLines(prev, cur), "PONG\n$0.01 status"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if got := newScreenLines(cur, cur); got != "" {
		t.Fatalf("unchanged screen emitted %q", got)
	}
	// A repeated line is new the second time it appears.
	if got := newScreenLines("ok", "ok\nok"); got != "ok" {
		t.Fatalf("repeat: got %q", got)
	}
}

func TestClaudeComposerAndBlocked(t *testing.T) {
	pane := "▎ --dangerously-load-development-channels blocked by org policy\n──────\n❯ hello\n──────\n  ⏵⏵ auto mode on"
	if !claudeChannelBlocked(pane) {
		t.Fatal("blocked warning not detected")
	}
	if text, ok := claudeComposerLine(pane); !ok || text != "hello" {
		t.Fatalf("composer: %q %v", text, ok)
	}
	if _, ok := claudeComposerLine("Yes, I trust this folder\nEnter to confirm\n──\n❯ 1. Yes"); ok {
		t.Fatal("trust prompt read as composer")
	}
}

func TestNewScreenLinesDropsNoise(t *testing.T) {
	cur := ">>> Rohit (live viewer): say PONG\n⠸\nPONG\n>>> Send a message (/? for help)"
	if got := newScreenLines("", cur); got != "PONG" {
		t.Fatalf("got %q", got)
	}
}
