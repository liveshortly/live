package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/liveshortly/live/internal/api"
	"github.com/liveshortly/live/internal/harness"
)

// Per-agent model catalog (LSH-16).
//
// At startup the daemon builds, for each agent it offers, the list of models
// the owner may pick from the web. The agent's own listing wins where one
// exists (codex's models cache, `agy models`, `cursor-agent --list-models`,
// `ollama list`); otherwise a short allowlist shipped with live.
//
// SECURITY — the list is built HERE and is the allowlist spawnSession checks a
// requested model against (modelArgs). The server relays a NAME picked from
// what this machine registered; the daemon maps it to the agent's fixed model
// flag as one argv element, never through a shell, and refuses anything else.

// modelFlags is the argv flag each built-in agent takes a model through. An
// agent missing here takes no model from the web (ollama is positional and
// handled separately in spawnSession).
var modelFlags = map[string]string{
	"claude":       "--model",
	"codex":        "--model",
	"gemini":       "--model",
	"agy":          "--model",
	"antigravity":  "--model",
	"cursor-agent": "--model",
	"pi":           "--model",
}

// Shipped fallbacks, used when the agent has no listing of its own. Update each
// release; a stale id fails at start with the agent's own error.
var (
	claudeModels = []api.ModelOption{{ID: "opus", Label: "Opus"}, {ID: "sonnet", Label: "Sonnet"}, {ID: "haiku", Label: "Haiku"}}
	codexModels  = []api.ModelOption{{ID: "gpt-5-codex", Label: "GPT-5 Codex"}, {ID: "gpt-5", Label: "GPT-5"}}
	geminiModels = []api.ModelOption{
		{ID: "gemini-2.5-pro", Label: "Gemini 2.5 Pro"}, {ID: "gemini-2.5-flash", Label: "Gemini 2.5 Flash"},
		{ID: "gemini-2.5-flash-lite", Label: "Gemini 2.5 Flash Lite"},
		{ID: "gemini-3-pro-preview", Label: "Gemini 3 Pro (preview)"}, {ID: "gemini-3-flash-preview", Label: "Gemini 3 Flash (preview)"},
	}
	cursorModels = []api.ModelOption{{ID: "auto", Label: "Auto"}, {ID: "sonnet-4.5", Label: "Claude Sonnet 4.5"}, {ID: "gpt-5", Label: "GPT-5"}}
)

// flagPattern bounds an operator-declared model_flag to a plain option name.
var flagPattern = regexp.MustCompile(`^-{1,2}[a-zA-Z][a-zA-Z0-9-]{0,31}$`)

// safeModelID is validModelName plus: never starts with '-', so a model id can
// never be read by the agent as a flag of its own.
func safeModelID(s string) bool {
	return validModelName(s) && !strings.HasPrefix(s, "-")
}

// modelFlagFor is the flag `agent` takes a model through, "" when none.
func modelFlagFor(agent string, custom map[string]agentCommand) string {
	if c, ok := custom[agent]; ok {
		return c.ModelFlag
	}
	return modelFlags[agent]
}

// buildModelCatalog lists, per offered agent, the models the web may choose.
func buildModelCatalog(agents []string, custom map[string]agentCommand, ollama []string) []api.AgentModels {
	home, _ := os.UserHomeDir()
	out := make([]api.AgentModels, 0, len(agents))
	for _, a := range agents {
		var models []api.ModelOption
		def := ""
		if c, ok := custom[a]; ok {
			for _, m := range c.Models {
				models = append(models, api.ModelOption{ID: m})
			}
		} else {
			switch a {
			case "claude":
				models, def = claudeModels, jsonString(filepath.Join(home, ".claude", "settings.json"), "model")
			case "codex":
				models = codexCachedModels(filepath.Join(home, ".codex", "models_cache.json"))
				if len(models) == 0 {
					models = codexModels
				}
				def = tomlTopString(filepath.Join(home, ".codex", "config.toml"), "model")
			case "gemini":
				models, def = geminiModels, jsonString(filepath.Join(home, ".gemini", "settings.json"), "model", "name")
			case "agy", "antigravity":
				models, def = parseListing(runListing(a, "models"))
			case "cursor-agent":
				models, def = parseListing(runListing(a, "--list-models"))
				if len(models) == 0 {
					models = cursorModels
				}
			case "pi":
				models = piModels(runListing(a, "--list-models"))
			case "ollama":
				for _, m := range ollama {
					models = append(models, api.ModelOption{ID: m})
				}
			}
		}
		out = append(out, api.AgentModels{
			Name:            a,
			Models:          cleanOptions(models, def),
			DefaultModel:    defaultIfSafe(def),
			ModelSwitchable: harness.ForAgent(a).ModelSwitch, // only claude takes `/model` mid-session (see setSessionModel)
		})
	}
	return out
}

// cleanOptions dedupes, drops unsafe ids, caps the list, and makes sure the
// operator's configured default is pickable even if no listing named it.
func cleanOptions(in []api.ModelOption, def string) []api.ModelOption {
	if safeModelID(def) {
		found := false
		for _, m := range in {
			found = found || m.ID == def
		}
		if !found {
			in = append([]api.ModelOption{{ID: def}}, in...)
		}
	}
	out := []api.ModelOption{}
	seen := map[string]bool{}
	for _, m := range in {
		if !safeModelID(m.ID) || seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		if len(m.Label) > 80 {
			m.Label = m.Label[:80]
		}
		out = append(out, m)
		if len(out) == maxSpawnableModels {
			break
		}
	}
	return out
}

func defaultIfSafe(def string) string {
	if safeModelID(def) {
		return def
	}
	return ""
}

// modelArgs maps a requested model to the agent's argv, after checking it
// against THIS machine's registered catalog. "" means the agent's default.
func modelArgs(agent, model string, reg api.HostRegistration, custom map[string]agentCommand) ([]string, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil, nil
	}
	if !safeModelID(model) {
		return nil, fmt.Errorf("model %q is not a valid model name", model)
	}
	flag := modelFlagFor(agent, custom)
	if flag == "" {
		return nil, fmt.Errorf("%s does not take a model from the web", agent)
	}
	for _, am := range reg.AgentModels {
		if am.Name != agent {
			continue
		}
		for _, m := range am.Models {
			if m.ID == model {
				return []string{flag, model}, nil
			}
		}
	}
	return nil, fmt.Errorf("model %q is not offered for %s on this machine", model, agent)
}

// runListing runs an agent's own model listing with a short timeout. Any
// failure (not logged in, offline, old version) returns "" and the caller
// falls back to the shipped list.
func runListing(bin string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, args...).Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// parseListing reads `id<TAB>label` (agy) or `id - label` (cursor-agent)
// lines; anything else is skipped. A label mentioning "default" or "current"
// marks the default.
// piModels parses `pi --list-models`, a whitespace-columned table:
//
//	provider    model               context  max-out  thinking  images
//	anthropic   claude-opus-5-5     1M       128K     yes       yes
//
// Pi takes `--model provider/id`, so the registered id is "provider/model" and
// the label is the bare model name.
func piModels(out string) []api.ModelOption {
	var models []api.ModelOption
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 || f[0] == "provider" {
			continue // blank lines and the header
		}
		id := f[0] + "/" + f[1]
		if !safeModelID(id) {
			continue
		}
		models = append(models, api.ModelOption{ID: id, Label: f[1]})
	}
	return models
}

func parseListing(out string) ([]api.ModelOption, string) {
	var models []api.ModelOption
	def := ""
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		id, label, ok := strings.Cut(line, "\t")
		if !ok {
			id, label, ok = strings.Cut(line, " - ")
		}
		id, label = strings.TrimSpace(id), strings.TrimSpace(label)
		if !ok || !safeModelID(id) {
			continue
		}
		low := strings.ToLower(label)
		if def == "" && (strings.Contains(low, "default") || strings.Contains(low, "current")) {
			def = id
		}
		models = append(models, api.ModelOption{ID: id, Label: label})
	}
	return models, def
}

// codexCachedModels reads codex's own model list (~/.codex/models_cache.json),
// keeping the ones codex itself shows in its picker.
func codexCachedModels(path string) []api.ModelOption {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var cache struct {
		Models []struct {
			Slug        string `json:"slug"`
			DisplayName string `json:"display_name"`
			Visibility  string `json:"visibility"`
		} `json:"models"`
	}
	if json.Unmarshal(b, &cache) != nil {
		return nil
	}
	var out []api.ModelOption
	for _, m := range cache.Models {
		if m.Visibility == "list" {
			out = append(out, api.ModelOption{ID: m.Slug, Label: m.DisplayName})
		}
	}
	return out
}

// jsonString reads a string at keys... in a JSON file; "" when absent. A key
// path may also stop early at a string (gemini's older `"model": "x"`).
func jsonString(path string, keys ...string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var v any
	if json.Unmarshal(b, &v) != nil {
		return ""
	}
	for _, k := range keys {
		m, ok := v.(map[string]any)
		if !ok {
			break
		}
		v = m[k]
	}
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

// tomlTopString reads `key = "value"` from the top-level table of a TOML file
// (before the first [section]). Enough for codex's `model = "..."`.
func tomlTopString(path, key string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			return ""
		}
		k, v, ok := strings.Cut(line, "=")
		if ok && strings.TrimSpace(k) == key {
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return ""
}
