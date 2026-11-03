// Package harness is the one source of truth for what live supports per agent
// harness (LSH-08). The CLI reads its capability decisions from Rows (display
// names, model switch, typed comments, pocket approvals), and
// `go run ./scripts/harnessmatrix` renders the same rows into README.md,
// docs/HARNESS-MATRIX.md and docs/harness-matrix.json. TestMatrixInSync fails
// when the checked-in copies drift from this table or from testdata/.
package harness

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Row is one harness in one run mode. Cells are ✅ / ⚠ (with the reason) / ❌.
type Row struct {
	Agents    []string // commands this row covers; Agents[0] is canonical
	Mode      string   // how to select this mode; "" for the default
	Framework string   // the framework live reports to the server
	Display   string   // name shown in prompts and the feed

	Capture    string
	Approvals  string
	Comments   string
	Handoff    string
	MinVersion string
	Gaps       string
	Summary    string // one line, for picker tooltips

	ModelSwitch     bool   // the daemon may type `/model` into this agent's pane
	TypedComments   bool   // viewer comments may be typed into the agent as a line
	PocketApprovals string // pocket.ai approval bridge: supported | unsupported | not-needed
	Fixtures        []string
}

// generic is the row every unknown command falls back to.
var generic = Row{
	Agents:     []string{"aider", "cursor-agent", "hermes", "copilot", "opencode", "crush", "goose", "amp", "qwen", "pi", "any command"},
	Framework:  "terminal",
	Display:    "The agent",
	Capture:    "⚠ terminal recording (raw lines, no structure)",
	Approvals:  "❌ the agent's own prompt, in the terminal only",
	Comments:   "⚠ shown to the operator only (opt in to typing with `type_comments` in agents.json or `LIVE_TYPE_COMMENTS=1`)",
	Handoff:    "⚠ paste the printed seed prompt",
	MinVersion: "—",
	Gaps:       "no prompt/reply/tool rows, no stream_end, no model",
	Summary:    "Terminal recording only: viewers see raw output; approvals stay in the terminal.",

	PocketApprovals: "unsupported",
	Fixtures:        []string{"aider", "cursor-agent", "hermes"},
}

// Rows is the support matrix, in display order. The last row is the fallback.
var Rows = []Row{
	{
		Agents: []string{"claude"}, Framework: "claude-code", Display: "Claude",
		Capture:    "✅ Claude Code hooks",
		Approvals:  "✅ gated from the web or tmux popup; fails closed",
		Comments:   "✅ injected (channel, hook boundaries, Stop)",
		Handoff:    "✅ seed prompt injected",
		MinVersion: "—",
		Gaps:       "—",
		Summary:    "Full support: structured feed, web approvals, live comments, model switch.",

		ModelSwitch: true, PocketApprovals: "supported", Fixtures: []string{"claude"},
	},
	{
		Agents: []string{"codex"}, Framework: "codex", Display: "Codex",
		Capture:    "✅ rollout file, codex in tmux",
		Approvals:  "✅ gated (PreToolUse hook); fails closed. Off with `--no-permissions`, `--yolo`, `-a never`",
		Comments:   "✅ typed into codex's composer",
		Handoff:    "✅ seed prompt injected",
		MinVersion: "—",
		Gaps:       "no mid-session model switch",
		Summary:    "Structured feed from codex's rollout file, web approvals, live comments.",

		PocketApprovals: "supported", Fixtures: []string{"codex"},
	},
	{
		Agents: []string{"codex"}, Mode: "`LIVE_TMUX=0`", Framework: "codex", Display: "Codex",
		Capture:    "✅ rollout file, codex in a PTY",
		Approvals:  "✅ gated (PreToolUse hook); fails closed",
		Comments:   "⚠ codex pulls them via the `poll_viewer_messages` channel tool (`LIVE_CHANNEL=0` turns it off)",
		Handoff:    "✅ seed prompt injected",
		MinVersion: "—",
		Gaps:       "comments wait until codex calls the channel tool; no model switch",
		Summary:    "Structured feed; comments arrive when codex polls for them.",

		PocketApprovals: "supported", Fixtures: []string{"codex"},
	},
	{
		Agents: []string{"codex"}, Mode: "`LIVE_CODEX_APPSERVER=1`", Framework: "codex", Display: "Codex",
		Capture:    "✅ app-server JSON-RPC (headless)",
		Approvals:  "✅ answered from the web; deny on timeout",
		Comments:   "✅ steer the running turn, or start a new one",
		Handoff:    "✅ developer instructions",
		MinVersion: "—",
		Gaps:       "file approvals say only \"apply a file change\"; no terminal UI; codex questions get no answers; no model switch",
		Summary:    "Headless codex driven over app-server: structured feed, web approvals, comments as turns.",

		PocketApprovals: "supported", Fixtures: []string{"codex-appserver"},
	},
	{
		Agents: []string{"agy", "antigravity"}, Framework: "agy", Display: "Antigravity",
		Capture:    "✅ hooks (`liveshortly` plugin)",
		Approvals:  "✅ gated; fails closed; live exits if the plugin can't install",
		Comments:   "✅ every comment in full, at the next model step",
		Handoff:    "✅ seed prompt injected",
		MinVersion: "—",
		Gaps:       "reply arrives when the turn ends; no model switch",
		Summary:    "Structured feed and web approvals; replies appear when the turn ends.",

		PocketApprovals: "supported", Fixtures: []string{"agy"},
	},
	{
		Agents: []string{"gemini"}, Framework: "gemini-cli", Display: "Gemini",
		Capture:    "✅ hooks (`liveshortly` extension)",
		Approvals:  "⚠ gated, fails closed — but gemini's own prompt stays in charge when hooks may not fire (hooks/extensions disabled, allowlist, GitHub Actions, your own `--yolo`/`--approval-mode`)",
		Comments:   "⚠ at tool and turn boundaries only",
		Handoff:    "⚠ paste the printed seed prompt",
		MinVersion: "0.26",
		Gaps:       "untrusted folder: approve twice; env redaction strips `LIVE_TOKEN` (capture off, gated tools denied); comments wait while idle; no model switch",
		Summary:    "Structured feed and web approvals via hooks; comments land at tool or turn boundaries.",

		PocketApprovals: "unsupported", Fixtures: []string{"gemini"},
	},
	{
		Agents: []string{"gemini"}, Mode: "gemini < 0.26", Framework: "gemini-cli", Display: "Gemini",
		Capture:    "⚠ terminal recording",
		Approvals:  "❌ gemini's own prompt, in the terminal only",
		Comments:   "⚠ shown to the operator only",
		Handoff:    "⚠ paste the printed seed prompt",
		MinVersion: "—",
		Gaps:       "no structure; upgrade to 0.26+",
		Summary:    "Old gemini: terminal recording only. Upgrade to 0.26+ for hooks.",

		PocketApprovals: "unsupported", Fixtures: []string{"gemini-pty"},
	},
	{
		Agents: []string{"ollama"}, Framework: "ollama", Display: "Ollama",
		Capture:    "⚠ terminal recording, whole lines",
		Approvals:  "— no tools",
		Comments:   "✅ typed in as one line",
		Handoff:    "⚠ paste the printed seed prompt",
		MinVersion: "—",
		Gaps:       "no prompt/tool rows, no stream_end, no model",
		Summary:    "Local chat REPL: replies stream as lines; comments are typed in.",

		TypedComments: true, PocketApprovals: "not-needed", Fixtures: []string{"ollama"},
	},
	generic,
}

// ForAgent returns the default-mode row for a command, or the fallback row.
func ForAgent(name string) Row {
	name = filepath.Base(name)
	for _, r := range Rows {
		for _, a := range r.Agents {
			if a == name {
				return r
			}
		}
	}
	return generic
}

// ForFramework returns the default-mode row for a framework, or the fallback.
func ForFramework(fw string) Row {
	for _, r := range Rows {
		if r.Framework == fw {
			return r
		}
	}
	return generic
}

// ForDisplay returns the row whose display name is d, or the fallback.
func ForDisplay(d string) Row {
	for _, r := range Rows {
		if r.Display == d {
			return r
		}
	}
	return generic
}

func (r Row) model() string {
	if r.ModelSwitch {
		return "✅ `/model` from the session page"
	}
	return "❌"
}

func (r Row) name() string {
	cmds := make([]string, len(r.Agents))
	for i, a := range r.Agents {
		cmds[i] = "`" + a + "`"
	}
	n := strings.Join(cmds, ", ")
	if r.Framework == generic.Framework {
		n = strings.Join(cmds[:len(cmds)-1], ", ") + " and any other command"
	}
	if r.Mode != "" {
		n += " (" + r.Mode + ")"
	}
	return n
}

// tested lists testdata/<fixture>/<version> folders as "fixture version".
func tested(root string, fixtures []string) (string, error) {
	var out []string
	for _, f := range fixtures {
		dirs, err := os.ReadDir(filepath.Join(root, "testdata", f))
		if err != nil {
			return "", fmt.Errorf("fixture %s: %w", f, err)
		}
		for _, d := range dirs {
			if d.IsDir() {
				out = append(out, f+" "+d.Name())
			}
		}
	}
	sort.Strings(out)
	if len(out) == 0 {
		return "—", nil
	}
	return strings.Join(out, ", "), nil
}

// Markdown renders the support table. root is the repo root (for testdata/).
func Markdown(root string) (string, error) {
	var b strings.Builder
	b.WriteString("| Agent | Capture | Approvals | Viewer comments | Model switch | Handoff | Min version | Golden replays | pocket.ai approvals | Known gaps |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|---|\n")
	for _, r := range Rows {
		t, err := tested(root, r.Fixtures)
		if err != nil {
			return "", err
		}
		cells := []string{r.name(), r.Capture, r.Approvals, r.Comments, r.model(), r.Handoff, r.MinVersion, t, r.PocketApprovals, r.Gaps}
		for i, c := range cells {
			cells[i] = strings.ReplaceAll(c, "|", "\\|")
		}
		b.WriteString("| " + strings.Join(cells, " | ") + " |\n")
	}
	b.WriteString("\n✅ works · ⚠ partial (reason given) · ❌ not supported. Generated from `internal/harness` by `go run ./scripts/harnessmatrix`; do not edit by hand.\n")
	return b.String(), nil
}

// JSON renders the rows for the web docs page and the new-session picker.
func JSON(root string) ([]byte, error) {
	type out struct {
		Agents          []string `json:"agents"`
		Mode            string   `json:"mode,omitempty"`
		Framework       string   `json:"framework"`
		Display         string   `json:"display"`
		Summary         string   `json:"summary"`
		Capture         string   `json:"capture"`
		Approvals       string   `json:"approvals"`
		Comments        string   `json:"comments"`
		ModelSwitch     bool     `json:"model_switch"`
		Handoff         string   `json:"handoff"`
		MinVersion      string   `json:"min_version"`
		Tested          string   `json:"tested"`
		PocketApprovals string   `json:"pocket_approvals"`
		Gaps            string   `json:"gaps"`
	}
	rows := []out{}
	for _, r := range Rows {
		t, err := tested(root, r.Fixtures)
		if err != nil {
			return nil, err
		}
		rows = append(rows, out{r.Agents, strings.Trim(r.Mode, "`"), r.Framework, r.Display, r.Summary, r.Capture, r.Approvals, r.Comments, r.ModelSwitch, r.Handoff, r.MinVersion, t, r.PocketApprovals, r.Gaps})
	}
	b, err := json.MarshalIndent(map[string]any{
		"source": "github.com/liveshortly/live internal/harness (generated; do not edit)",
		"rows":   rows,
	}, "", "  ")
	return append(b, '\n'), err
}

const (
	startMarker = "<!-- harness-matrix:start -->"
	endMarker   = "<!-- harness-matrix:end -->"
)

// Sync renders the matrix into README.md, docs/HARNESS-MATRIX.md (between the
// markers) and docs/harness-matrix.json under root. With write false it changes
// nothing and returns the files that differ.
func Sync(root string, write bool) (drifted []string, err error) {
	md, err := Markdown(root)
	if err != nil {
		return nil, err
	}
	js, err := JSON(root)
	if err != nil {
		return nil, err
	}
	want := map[string]func(old string) (string, error){
		"README.md":                func(old string) (string, error) { return splice(old, md) },
		"docs/HARNESS-MATRIX.md":   func(old string) (string, error) { return splice(old, md) },
		"docs/harness-matrix.json": func(string) (string, error) { return string(js), nil },
	}
	names := []string{"README.md", "docs/HARNESS-MATRIX.md", "docs/harness-matrix.json"}
	for _, name := range names {
		path := filepath.Join(root, name)
		old, err := os.ReadFile(path)
		if err != nil && !(os.IsNotExist(err) && strings.HasSuffix(name, ".json")) {
			return nil, err
		}
		next, err := want[name](string(old))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if next == string(old) {
			continue
		}
		drifted = append(drifted, name)
		if write {
			if err := os.WriteFile(path, []byte(next), 0o644); err != nil {
				return nil, err
			}
		}
	}
	return drifted, nil
}

func splice(doc, table string) (string, error) {
	i := strings.Index(doc, startMarker)
	j := strings.Index(doc, endMarker)
	if i < 0 || j < i {
		return "", fmt.Errorf("missing %s … %s markers", startMarker, endMarker)
	}
	return doc[:i+len(startMarker)] + "\n" + table + doc[j:], nil
}
