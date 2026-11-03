package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liveshortly/live/internal/api"
	"github.com/liveshortly/live/internal/harness"
)

func TestModelArgs(t *testing.T) {
	custom := map[string]agentCommand{"hermes": {Name: "hermes", Models: []string{"nous/hermes-4"}, ModelFlag: "-m"}}
	reg := api.HostRegistration{AgentModels: []api.AgentModels{
		{Name: "claude", Models: claudeModels},
		{Name: "codex", Models: []api.ModelOption{{ID: "gpt-5-codex"}}},
		{Name: "hermes", Models: []api.ModelOption{{ID: "nous/hermes-4"}}},
		{Name: "aider", Models: []api.ModelOption{{ID: "x"}}}, // no flag known: never forwarded
	}}

	ok := []struct{ agent, model, want string }{
		{"claude", "", ""},
		{"claude", "opus", "--model opus"},
		{"codex", " gpt-5-codex ", "--model gpt-5-codex"},
		{"hermes", "nous/hermes-4", "-m nous/hermes-4"},
	}
	for _, c := range ok {
		got, err := modelArgs(c.agent, c.model, reg, custom)
		if err != nil || strings.Join(got, " ") != c.want {
			t.Errorf("modelArgs(%q,%q) = %q, %v; want %q", c.agent, c.model, got, err, c.want)
		}
	}

	reject := []struct{ agent, model string }{
		{"claude", "gpt-5-codex"},                    // another agent's model
		{"codex", "gpt-6"},                           // not in the catalog
		{"claude", "--dangerously-skip-permissions"}, // a flag, not a model
		{"claude", "-opus"},
		{"claude", "opus; rm -rf ~"},
		{"claude", "opus --yolo"},
		{"gemini", "gemini-2.5-pro"}, // agent has no catalog entry
		{"aider", "x"},               // no known model flag
	}
	for _, c := range reject {
		if got, err := modelArgs(c.agent, c.model, reg, custom); err == nil {
			t.Errorf("modelArgs(%q,%q) = %q; want rejection", c.agent, c.model, got)
		}
	}
}

func TestParseListing(t *testing.T) {
	agy := "Fetching available models...\ngemini-3-flash\tGemini 3 Flash\nclaude-sonnet-4-6\tClaude Sonnet 4.6\n--evil\tbad\n"
	m, def := parseListing(agy)
	if len(m) != 2 || m[0].ID != "gemini-3-flash" || m[1].Label != "Claude Sonnet 4.6" || def != "" {
		t.Fatalf("agy: %+v %q", m, def)
	}
	cursor := "Available models\n\nauto - Auto\nsonnet-4.5 - Claude 4.5 Sonnet (current, default)\n"
	m, def = parseListing(cursor)
	if len(m) != 2 || def != "sonnet-4.5" {
		t.Fatalf("cursor: %+v %q", m, def)
	}
}

func TestCleanOptionsAddsDefault(t *testing.T) {
	got := cleanOptions([]api.ModelOption{{ID: "opus"}, {ID: "opus"}, {ID: "-x"}}, "claude-custom-1")
	if len(got) != 2 || got[0].ID != "claude-custom-1" || got[1].ID != "opus" {
		t.Fatalf("%+v", got)
	}
	if got := cleanOptions(nil, "--yolo"); len(got) != 0 {
		t.Fatalf("unsafe default leaked: %+v", got)
	}
}

func TestCodexConfigAndCache(t *testing.T) {
	dir := t.TempDir()
	toml := filepath.Join(dir, "config.toml")
	os.WriteFile(toml, []byte("model = \"gpt-6-sol\"\n[profiles.x]\nmodel = \"other\"\n"), 0o600)
	if got := tomlTopString(toml, "model"); got != "gpt-6-sol" {
		t.Fatalf("toml: %q", got)
	}
	cache := filepath.Join(dir, "models_cache.json")
	os.WriteFile(cache, []byte(`{"models":[{"slug":"gpt-6-sol","display_name":"GPT-6 Sol","visibility":"list"},{"slug":"hidden","visibility":"hide"}]}`), 0o600)
	if got := codexCachedModels(cache); len(got) != 1 || got[0].ID != "gpt-6-sol" {
		t.Fatalf("cache: %+v", got)
	}
	js := filepath.Join(dir, "s.json")
	os.WriteFile(js, []byte(`{"model":{"name":"gemini-3-pro-preview"}}`), 0o600)
	if got := jsonString(js, "model", "name"); got != "gemini-3-pro-preview" {
		t.Fatalf("json: %q", got)
	}
}

func TestAgentsFileModelsValidated(t *testing.T) {
	dir := t.TempDir()
	for _, body := range []string{
		`[{"name":"hermes","models":["a"]}]`,                        // no flag
		`[{"name":"hermes","models":["a"],"model_flag":"; sh"}]`,    // not a flag
		`[{"name":"hermes","models":["--yolo"],"model_flag":"-m"}]`, // model is a flag
	} {
		p := filepath.Join(dir, "agents.json")
		os.WriteFile(p, []byte(body), 0o600)
		if _, err := loadAgentCommands(p); err == nil {
			t.Errorf("accepted %s", body)
		}
	}
}

// The LSH-00 matrix's model cells must say what the daemon actually does: every
// agent that takes a model from the web has a ✅/⚠ "Model choice" cell, and only
// rows the daemon advertises as switchable claim a mid-session switch.
func TestMatrixModelCellsMatchCatalog(t *testing.T) {
	for _, a := range append([]string{"ollama"}, mapKeys(modelFlags)...) {
		if c := harness.ForAgent(a).ModelChoice; c == "" || strings.HasPrefix(c, "❌") {
			t.Errorf("%s takes a model from the web, but the matrix says %q", a, c)
		}
	}
	// no agy: its catalog runs the real binary
	for _, am := range buildModelCatalog([]string{"claude", "codex", "gemini"}, nil, nil) {
		if am.ModelSwitchable != (am.Name == "claude") {
			t.Errorf("%s: model_switchable = %v; only claude switches mid-session", am.Name, am.ModelSwitchable)
		}
	}
	for _, r := range harness.Rows {
		if r.ModelChoice == "" || r.FirstMessage == "" {
			t.Errorf("%v: matrix row missing a model-choice or first-message cell", r.Agents)
		}
	}
}

func mapKeys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
