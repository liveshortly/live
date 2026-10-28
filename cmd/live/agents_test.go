package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubBins puts executable no-ops on a PATH containing only dir.
func stubBins(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("#!/bin/sh\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// A machine running a harness live has never heard of must still be able to
// offer it: that is the whole point of the adapter file.
func TestDiscoverAgentsOffersUnknownHarnesses(t *testing.T) {
	dir := stubBins(t, "claude", "hermes", "pi.dev")
	cfg := filepath.Join(dir, "agents.json")
	// "pi" is a builtin NAME but the executable here is pi.dev, so only the
	// adapter can find it; "hermes" is found by the builtin probe alone.
	os.WriteFile(cfg, []byte(`[{"name":"pi","command":"pi.dev","args":["chat"]}]`), 0o600)

	custom, err := loadAgentCommands(cfg)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(discoverAgents(spawnableBuiltins, custom), ",")
	if got != "claude,hermes,pi" {
		t.Fatalf("discovered %q", got)
	}
}

// An adapter may replace a guessed builtin (an explicit path the operator wrote
// beats one live probed) but never a native agent, whose special handling is
// keyed on the name.
func TestLoadAgentCommandsRejectsNativeShadowing(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "agents.json")
	for _, body := range []string{
		`[{"name":"claude","command":"/tmp/fake"}]`,
		`[{"name":"ollama","command":"/tmp/fake"}]`,
		`[{"name":"bad name","command":"x"}]`,
		`[{"name":"dup","command":"a"},{"name":"dup","command":"b"}]`,
		`not json`,
	} {
		os.WriteFile(cfg, []byte(body), 0o600)
		if _, err := loadAgentCommands(cfg); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
}

// The default file is optional — most machines never have one — but a path the
// operator named explicitly must exist, or their agents would vanish silently.
func TestLoadAgentCommandsDefaultIsOptional(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	got, err := loadAgentCommands("")
	if err != nil || len(got) != 0 {
		t.Fatalf("default path: %v %v", got, err)
	}
	if _, err := loadAgentCommands(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Fatal("missing explicit path was accepted")
	}
}

// Omitting "command" is the common case: the name IS the executable.
func TestLoadAgentCommandsDefaultsCommandToName(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "agents.json")
	os.WriteFile(cfg, []byte(`[{"name":"hermes"}]`), 0o600)
	got, err := loadAgentCommands(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got["hermes"].Command != "hermes" {
		t.Fatalf("command = %q", got["hermes"].Command)
	}
}

// Pocket can only drive a harness it knows non-interactive flags for, so its
// builtin list is narrower — but every name on it must also be spawnable.
func TestPocketBuiltinsAreSpawnable(t *testing.T) {
	for _, a := range pocketBuiltins {
		if !contains(spawnableBuiltins, a) {
			t.Fatalf("%s is pocket-only", a)
		}
	}
	for _, a := range nativeAgents {
		if !contains(spawnableBuiltins, a) {
			t.Fatalf("%s is native but not spawnable", a)
		}
	}
}
