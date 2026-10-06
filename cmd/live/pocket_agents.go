package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
)

var pocketBuiltins = []string{"codex", "claude", "gemini", "agy", "antigravity", "ollama", "aider", "cursor-agent"}

type pocketAgentCommand struct {
	Name    string   `json:"name"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// Adapters are configured by the server operator, never supplied by clients.
// Commands receive prompts on stdin (or via a {prompt} argv placeholder) and
// must print their response to stdout. No shell is used.
func loadPocketAgentCommands(path string) (map[string]pocketAgentCommand, error) {
	result := map[string]pocketAgentCommand{}
	if path == "" {
		return result, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var commands []pocketAgentCommand
	if err = json.Unmarshal(b, &commands); err != nil {
		return nil, err
	}
	valid := regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)
	for _, c := range commands {
		if !valid.MatchString(c.Name) || c.Command == "" {
			return nil, fmt.Errorf("adapter needs a valid name and executable command")
		}
		if contains(pocketBuiltins, c.Name) {
			return nil, fmt.Errorf("adapter %s conflicts with a built-in agent", c.Name)
		}
		if _, exists := result[c.Name]; exists {
			return nil, fmt.Errorf("duplicate adapter %s", c.Name)
		}
		result[c.Name] = c
	}
	return result, nil
}

func discoverPocketAgents(custom map[string]pocketAgentCommand) []string {
	result := []string{}
	for _, name := range pocketBuiltins {
		if _, err := exec.LookPath(name); err == nil {
			result = append(result, name)
		}
	}
	var extra []string
	for name, c := range custom {
		if _, err := exec.LookPath(c.Command); err == nil {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)
	return append(result, extra...)
}
