package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/liveshortly/live/internal/adapter"
	"github.com/liveshortly/live/internal/api"
	"github.com/liveshortly/live/internal/sidecar"
)

// runRolloutMode runs codex interactively in a PTY (so the operator drives it
// normally) while capturing STRUCTURED events by tailing codex's own rollout
// session file (~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl) and mapping them
// to the same sidecar feed events Claude produces. This lifts `live codex` from
// raw-terminal-scrape (pty tier) to structured parity — prompts, assistant
// replies, tool calls, and file edits show up as clean feed rows.
func runRolloutMode(sc *sidecar.Sidecar, binPath string, userArgs []string) int {
	p := &adapter.PTY{}
	if headless() {
		sc.OnEnded = p.Stop
	}

	// Viewer comments always ECHO in the CLI (DisplayComment) so the operator
	// sees them live; when the channel is on they ALSO route to codex via the
	// session-scoped `live channel` MCP (the poll tool). Set before Start so
	// drain() sees the echo hook.
	channelOn := codexChannelWanted(userArgs)
	sc.DisplayComment = func(c api.Comment) {
		who := c.Username
		if who == "" {
			who = "viewer"
		}
		p.Notify("💬 " + who + ": " + c.Message)
	}

	sc.Start(context.Background())

	// Load the channel MCP into codex — strictly per-session and ephemeral: it's
	// passed as `-c mcp_servers.*` launch overrides, NOT registered in codex's
	// global config (never `codex mcp add`). Nothing persists past this run.
	permsOn := codexPermsWanted(userArgs)
	if channelOn || permsOn {
		if self, err := os.Executable(); err == nil {
			if addr, token, herr := sc.StartHookServer(); herr == nil {
				if channelOn {
					userArgs = append(codexChannelArgs(self, addr, token, sc.SessionID), userArgs...)
					fmt.Fprint(os.Stderr, "\x1b[33m● live\x1b[0m viewer channel ON — codex pulls viewer messages via the poll_viewer_messages tool (LIVE_CHANNEL=0 to disable)\n\n")
				}
				if permsOn {
					// The channel MCP and the perm hook both connect back to the
					// sidecar; codex inherits these from live's env (PTY passes
					// os.Environ()).
					os.Setenv("LIVE_SIDECAR", addr)
					os.Setenv("LIVE_TOKEN", token)
					os.Setenv("LIVE_HOOK_TIMEOUT", "590")
					sc.WebPermissions = true
					userArgs = append(codexPermArgs(self), userArgs...)
					fmt.Fprint(os.Stderr, "\x1b[33m● live\x1b[0m web-permissions ON — approve codex tool runs from the session page (LIVE_CODEX_PERMS=0 to disable). Unanswered prompts deny after the grace window.\n\n")
				}
			} else {
				sc.Log.Printf("rollout: hook server failed, channel/perms off: %v", herr)
			}
		}
	}

	// Snapshot the rollout files that already exist, then launch codex and tail
	// whichever NEW file it creates for this session.
	before := snapshotRollouts()
	cwd, _ := os.Getwd()
	tctx, stopTail := context.WithCancel(context.Background())
	tailDone := make(chan struct{})
	go func() {
		defer close(tailDone)
		tailNewRollout(tctx, sc, before, cwd)
	}()

	// nil onChunk: the terminal is mirrored by the PTY, but we do NOT scrape it
	// for feed events — the rollout tailer is the structured source of truth.
	code, err := p.Run(binPath, userArgs, nil)
	stopTail()
	<-tailDone
	if err != nil {
		fmt.Fprintf(os.Stderr, "live: %v\n", err)
	}
	return code
}

// codexChannelWanted reports whether to load the viewer-comment channel MCP for
// this codex run: on by default, off for headless -p/--print runs (no human) or
// when LIVE_CHANNEL=0.
func codexChannelWanted(userArgs []string) bool {
	if os.Getenv("LIVE_CHANNEL") == "0" || isPrintMode(userArgs) {
		return false
	}
	return true
}

// codexChannelArgs returns the ephemeral `-c mcp_servers.*` overrides that load
// the `live channel` MCP into a single codex launch. These live only for this
// invocation — nothing is written to ~/.codex/config.toml. LIVE_CHANNEL_TOOL=1
// makes the channel serve comments as the standard poll_viewer_messages tool
// (codex is a standard MCP client and ignores Claude's channel notifications).
func codexChannelArgs(self, addr, token, sessionID string) []string {
	const p = "mcp_servers.liveshortly_channel"
	kv := func(key, tomlVal string) []string { return []string{"-c", key + "=" + tomlVal} }
	q := func(s string) string { return fmt.Sprintf("%q", s) } // TOML basic string

	var a []string
	a = append(a, kv(p+".command", q(self))...)
	a = append(a, kv(p+".args", `["channel"]`)...)
	a = append(a, kv(p+".env.LIVE_SIDECAR", q(addr))...)
	a = append(a, kv(p+".env.LIVE_TOKEN", q(token))...)
	a = append(a, kv(p+".env.LIVE_SESSION_ID", q(sessionID))...)
	a = append(a, kv(p+".env.LIVE_CHANNEL_TOOL", q("1"))...)
	return a
}

// codexPermsWanted reports whether to gate codex tool execution through the
// LiveShortly web-permission flow. ON by default (like `live claude`): a
// session-scoped PreToolUse hook sends approvals to the owner's browser and its
// verdict is authoritative, so codex never shows a blocking native prompt.
// Disable with LIVE_CODEX_PERMS=0; always off for headless -p runs, for
// `live --no-permissions` (liveGating=false, also what a daemon bypass spawn
// passes) and for codex's own "don't ask" flags.
func codexPermsWanted(userArgs []string) bool {
	return liveGating && os.Getenv("LIVE_CODEX_PERMS") != "0" && !isPrintMode(userArgs) && !codexApprovalsBypassed(userArgs)
}

// liveGating is false when the operator opted out of live's permission gating
// (--no-permissions / --dangerously-skip-permissions before the agent). Set by
// cmdRun before dispatching to a capture mode.
var liveGating = true

// codexApprovalsBypassed reports whether codex was told not to ask:
// --dangerously-bypass-approvals-and-sandbox, --yolo, or -a/--ask-for-approval never.
func codexApprovalsBypassed(args []string) bool {
	for i, a := range args {
		switch {
		case a == "--dangerously-bypass-approvals-and-sandbox", a == "--yolo":
			return true
		case a == "--ask-for-approval=never", a == "-a=never":
			return true
		case (a == "-a" || a == "--ask-for-approval") && i+1 < len(args) && args[i+1] == "never":
			return true
		}
	}
	return false
}

// codexPermArgs returns the ephemeral launch overrides that gate codex tool use
// through LiveShortly. It registers a session-scoped PreToolUse hook (inline
// TOML, no plugin install) that runs `live hook pre-tool-use`; the sidecar
// answers it with the owner's browser verdict. codex's PreToolUse hook output
// is wire-identical to Claude's, so the existing sidecar handler is reused
// as-is. --dangerously-bypass-hook-trust skips the one-time trust prompt for
// this automated, self-vetted hook. Nothing persists past the run.
func codexPermArgs(self string) []string {
	// The hook command codex runs: `"<live>" hook pre-tool-use --gated`
	// (--gated: deny, never allow, when the sidecar can't answer).
	cmd := fmt.Sprintf("%q hook pre-tool-use --gated", self)
	// codex normalizes its hook payloads to Claude's vocabulary (verified: a
	// shell call arrives as tool_name="Bash", tool_input={"command":…}), so the
	// matcher uses Claude's tool names and the existing sidecar handler is reused
	// unchanged. %q re-encodes cmd + matcher as valid TOML basic strings.
	toml := fmt.Sprintf(
		`hooks.PreToolUse=[{matcher=%q,hooks=[{type="command",command=%q,timeout=600}]}]`,
		"Edit|Write|MultiEdit|Bash|WebFetch", cmd,
	)
	return []string{"-c", toml, "--dangerously-bypass-hook-trust"}
}

// codexSessionsDir returns the directory codex writes rollout files under,
// honoring CODEX_HOME.
func codexSessionsDir() string {
	if h := os.Getenv("CODEX_HOME"); h != "" {
		return filepath.Join(h, "sessions")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex", "sessions")
}

func isRolloutFile(name string) bool {
	return strings.HasPrefix(name, "rollout-") && strings.HasSuffix(name, ".jsonl")
}

// snapshotRollouts records the rollout files that exist right now so a
// subsequently-created one can be identified as this session's.
func snapshotRollouts() map[string]bool {
	seen := map[string]bool{}
	dir := codexSessionsDir()
	if dir == "" {
		return seen
	}
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() && isRolloutFile(d.Name()) {
			seen[path] = true
		}
		return nil
	})
	return seen
}

// tailNewRollout discovers the rollout file codex just created for our session
// (a file not present in `before`, preferring one whose recorded cwd matches
// ours) and tails it until ctx is cancelled.
func tailNewRollout(ctx context.Context, sc *sidecar.Sidecar, before map[string]bool, cwd string) {
	// Codex writes its rollout file when the FIRST PROMPT is sent, not when it
	// launches — so this waits for the user, and the user takes as long as they
	// take. Giving up on a timer captured nothing at all for any session whose
	// first message was more than 30s after launch, which is most of them, and
	// certain whenever codex opens with an update prompt. The session's own
	// context is the only correct bound: it ends when the session does.
	var path string
	warned := false
	start := time.Now()
	for {
		if ctx.Err() != nil {
			return
		}
		var fresh []string
		for p := range snapshotRollouts() {
			if !before[p] {
				fresh = append(fresh, p)
			}
		}
		if p := pickRollout(fresh, cwd); p != "" {
			path = p
			break
		}
		// Poll briskly at first so a prompt sent immediately is picked up fast,
		// then ease off — past this point we are waiting on a human.
		wait := 300 * time.Millisecond
		if elapsed := time.Since(start); elapsed > 30*time.Second {
			wait = time.Second
			if !warned {
				warned = true
				sc.Log.Printf("rollout: still waiting for codex to open a session file " +
					"(it writes one on the first prompt) — capture starts then")
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
	sc.Log.Printf("rollout: tailing %s", path)
	tailRolloutFile(ctx, sc, path)
}

// pickRollout chooses the best candidate rollout file: the newest one whose
// session_meta cwd matches ours; failing that, the newest overall.
func pickRollout(cands []string, cwd string) string {
	var best, bestAny string
	var bestT, bestAnyT time.Time
	for _, p := range cands {
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		if fi.ModTime().After(bestAnyT) {
			bestAny, bestAnyT = p, fi.ModTime()
		}
		if cwd != "" && rolloutCwd(p) == cwd && fi.ModTime().After(bestT) {
			best, bestT = p, fi.ModTime()
		}
	}
	if best != "" {
		return best
	}
	return bestAny
}

// rolloutCwd reads the session_meta line of a rollout file and returns its cwd.
func rolloutCwd(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	// session_meta is the first line; read a bounded chunk.
	buf := make([]byte, 8192)
	n, _ := f.Read(buf)
	line := buf[:n]
	if i := bytes.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	var env struct {
		Type    string `json:"type"`
		Payload struct {
			Cwd string `json:"cwd"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &env) == nil && env.Type == "session_meta" {
		return env.Payload.Cwd
	}
	return ""
}

// tailRolloutFile follows an actively-written rollout file, dispatching each
// complete JSONL line to the mapper. It reads from a byte offset and buffers
// partial trailing lines, polling for growth. On ctx cancel it does one final
// read so the last events codex wrote before exit are not lost.
func tailRolloutFile(ctx context.Context, sc *sidecar.Sidecar, path string) {
	m := &rolloutMapper{
		emit: sc.Emit,
		reportModel: func(model string) {
			go func() {
				if err := sc.Client.ReportModel(sc.SessionID, model); err != nil {
					sc.Log.Printf("rollout: report model: %v", err)
				}
			}()
		},
		pending: map[string]codexCall{},
	}
	var offset int64
	var partial []byte

	drain := func() {
		f, err := os.Open(path)
		if err != nil {
			return
		}
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			f.Close()
			return
		}
		data, _ := io.ReadAll(f)
		f.Close()
		offset += int64(len(data))
		partial = append(partial, data...)
		for {
			i := bytes.IndexByte(partial, '\n')
			if i < 0 {
				break
			}
			line := partial[:i]
			partial = append([]byte(nil), partial[i+1:]...)
			m.handle(line)
		}
	}

	tick := time.NewTicker(300 * time.Millisecond)
	defer tick.Stop()
	for {
		drain()
		select {
		case <-ctx.Done():
			drain() // final flush
			return
		case <-tick.C:
		}
	}
}

// ── codex rollout → feed-event mapping ────────────────────────────────────────

type codexCall struct {
	name    string
	command string
	skip    bool // output belongs to a call already shown (apply_patch) or not worth a row (wait)
}

type rolloutMapper struct {
	emit          func(string, map[string]any) // feed-event sink (sidecar.Emit)
	reportModel   func(string)                 // report the codex model once
	pending       map[string]codexCall         // call_id → function_call awaiting its output
	modelReported bool
}

func (m *rolloutMapper) handle(line []byte) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return
	}
	var env struct {
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	if json.Unmarshal(line, &env) != nil {
		return
	}
	switch env.Type {
	case "session_meta", "turn_context":
		m.maybeReportModel(env.Payload)
	case "response_item":
		m.handleResponseItem(env.Payload)
	case "event_msg":
		m.handleEventMsg(env.Payload)
	}
}

func (m *rolloutMapper) handleResponseItem(raw json.RawMessage) {
	var p struct {
		Type    string `json:"type"`
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Name      string          `json:"name"`
		CallID    string          `json:"call_id"`
		Arguments string          `json:"arguments"`
		Output    json.RawMessage `json:"output"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return
	}
	switch p.Type {
	case "message":
		text := joinContent(p.Content)
		if text == "" {
			return
		}
		switch p.Role {
		case "user":
			// Skip codex's injected preambles and any viewer comment we routed
			// into codex — via the channel MCP (<channel …>) or typed into the
			// composer in tmux mode. Both already show as viewer_comment events.
			if isCodexPreamble(text) || looksLikeChannelTurn(text) || looksLikeViewerInput(text) {
				return
			}
			m.emit("prompt", map[string]any{"content": clip(text, 4000), "ts": isoNow()})
		case "assistant":
			m.emit("response", map[string]any{"content": clip(text, 8000), "ts": isoNow()})
			// developer/system roles: internal scaffolding, skip.
		}
	case "function_call":
		switch p.Name {
		case "apply_patch":
			m.emitApplyPatch(p.Arguments)
			m.pending[p.CallID] = codexCall{skip: true}
		case "wait": // codex ≥0.163 code-mode helper that polls a running exec cell
			m.pending[p.CallID] = codexCall{skip: true}
		default:
			m.pending[p.CallID] = codexCall{name: p.Name, command: extractCommand(p.Arguments)}
		}
	case "function_call_output":
		out := parseCodexOutput(p.Output)
		if call, ok := m.pending[p.CallID]; ok {
			delete(m.pending, p.CallID)
			if call.skip {
				return
			}
			m.emit("tool_call", map[string]any{
				"tool":    codexToolLabel(call.name),
				"command": clip(call.command, 500),
				"output":  clip(out, codexOutputCap),
				"ts":      isoNow(),
			})
			return
		}
		if out != "" {
			m.emit("output", map[string]any{"text": clip(out, 2000), "ts": isoNow()})
		}
	}
}

func (m *rolloutMapper) handleEventMsg(raw json.RawMessage) {
	var p struct {
		Type string `json:"type"`
		Item struct {
			Type             string          `json:"type"`
			Command          []string        `json:"command"`
			AggregatedOutput string          `json:"aggregated_output"`
			Changes          json.RawMessage `json:"changes"`
		} `json:"item"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return
	}
	switch p.Type {
	case "task_complete", "turn_aborted":
		m.emit("stream_end", map[string]any{"ts": isoNow()})
	case "item_completed":
		// codex ≥0.163 runs shell and patches inside a code-mode `exec`
		// custom_tool_call (a JS cell, not worth a row) and reports what the
		// cell did as these settled items.
		switch p.Item.Type {
		case "CommandExecution":
			m.supersedePending()
			m.emit("tool_call", map[string]any{
				"tool":    "Shell",
				"command": clip(shellCommand(p.Item.Command), 500),
				"output":  clip(strings.TrimSpace(p.Item.AggregatedOutput), codexOutputCap),
				"ts":      isoNow(),
			})
		case "FileChange":
			for _, f := range parseCodexFileChanges(p.Item.Changes) {
				added, removed := diffCounts(f.diff)
				m.emit("file_write", map[string]any{
					"tool":    "apply_patch",
					"file":    f.path,
					"diff":    clip(strings.TrimRight(f.diff, "\n"), codexOutputCap),
					"added":   added,
					"removed": removed,
					"ts":      isoNow(),
				})
			}
		}
	}
}

// supersedePending drops rows for shell calls still awaiting output: once codex
// reports a CommandExecution item, that item is the row, so a direct
// function_call exec_command must not add a second one.
func (m *rolloutMapper) supersedePending() {
	for id, call := range m.pending {
		if codexToolLabel(call.name) == "Shell" {
			m.pending[id] = codexCall{skip: true}
		}
	}
}

// shellCommand unwraps codex's ["/bin/zsh","-lc","<cmd>"] argv to <cmd>.
func shellCommand(argv []string) string {
	if len(argv) == 3 && (argv[1] == "-lc" || argv[1] == "-c") {
		return argv[2]
	}
	return strings.Join(argv, " ")
}

// diffCounts counts added/removed lines in a unified diff.
func diffCounts(diff string) (added, removed int) {
	for _, l := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(l, "+++"), strings.HasPrefix(l, "---"):
		case strings.HasPrefix(l, "+"):
			added++
		case strings.HasPrefix(l, "-"):
			removed++
		}
	}
	return
}

// maybeReportModel reports codex's model to the server once, so the viewer
// shows the real model instead of the model-less default a fresh session gets.
func (m *rolloutMapper) maybeReportModel(raw json.RawMessage) {
	if m.modelReported {
		return
	}
	var p struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(raw, &p) != nil || p.Model == "" {
		return
	}
	m.modelReported = true
	if m.reportModel != nil {
		m.reportModel(p.Model)
	}
}

// emitApplyPatch turns a codex apply_patch call into file_write + diff events,
// one per file the patch touches. Best-effort parse of the standard
// "*** Begin Patch / *** Update File: <path>" envelope.
func (m *rolloutMapper) emitApplyPatch(arguments string) {
	patch := extractPatch(arguments)
	if patch == "" {
		return
	}
	for _, fp := range parsePatchFiles(patch) {
		m.emit("file_write", map[string]any{
			"tool":    "apply_patch",
			"file":    fp.path,
			"diff":    clip(fp.diff, codexOutputCap),
			"added":   fp.added,
			"removed": fp.removed,
			"ts":      isoNow(),
		})
	}
}

// ── parsing helpers ───────────────────────────────────────────────────────────

func joinContent(content []struct {
	Type string `json:"type"`
	Text string `json:"text"`
}) string {
	var b strings.Builder
	for _, c := range content {
		if c.Text != "" {
			b.WriteString(c.Text)
		}
	}
	return strings.TrimSpace(b.String())
}

// extractCommand pulls the shell command out of a function_call's JSON
// arguments string. Codex exec_command uses "cmd"; others may use "command".
func extractCommand(arguments string) string {
	var a map[string]any
	if json.Unmarshal([]byte(arguments), &a) != nil {
		return strings.TrimSpace(arguments)
	}
	for _, k := range []string{"cmd", "command"} {
		if v, ok := a[k]; ok {
			switch t := v.(type) {
			case string:
				return t
			case []any:
				parts := make([]string, 0, len(t))
				for _, e := range t {
					parts = append(parts, fmt.Sprint(e))
				}
				return strings.Join(parts, " ")
			}
		}
	}
	return strings.TrimSpace(arguments)
}

// parseCodexOutput unwraps a function_call_output value. Codex wraps shell
// output as "Chunk ID: …\n…\nOutput:\n<actual>"; strip to the actual output.
// The value may be a JSON string or an object with an "output"/"content" field.
func parseCodexOutput(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		var o struct {
			Output  string `json:"output"`
			Content string `json:"content"`
		}
		if json.Unmarshal(raw, &o) == nil {
			s = firstNonEmptyStr(o.Output, o.Content)
		} else {
			s = string(raw)
		}
	}
	if i := strings.Index(s, "\nOutput:\n"); i >= 0 {
		s = s[i+len("\nOutput:\n"):]
	}
	return strings.TrimSpace(s)
}

func codexToolLabel(name string) string {
	switch name {
	case "exec_command", "local_shell", "shell", "container.exec":
		return "Shell"
	case "apply_patch":
		return "Edit"
	case "":
		return "Tool"
	default:
		return name
	}
}

// isCodexPreamble reports whether a user message is one of codex's injected
// scaffolding blocks rather than something the operator actually typed.
// codex keeps adding them (<multi_agent_mode>, <skills_instructions>…), so
// any message that is one whole <tag>…</tag> block counts, not a fixed list.
// The AGENTS.md injection ("# AGENTS.md instructions for <dir>") counts too;
// codex sends it in the same message as <environment_context>.
func isCodexPreamble(text string) bool {
	t := strings.TrimSpace(text)
	for _, tag := range []string{"# AGENTS.md instructions", "<environment_context>", "<permissions instructions>", "<user_instructions>"} {
		if strings.HasPrefix(t, tag) {
			return true
		}
	}
	m := codexPreambleRe.FindStringSubmatch(t)
	return m != nil && strings.HasSuffix(t, "</"+m[1]+">")
}

var codexPreambleRe = regexp.MustCompile(`^<([a-z_]+(?: [a-z_]+)*)>`)

// looksLikeChannelTurn reports whether the whole message is just a
// channel-wrapped viewer comment injected by the live channel MCP.
func looksLikeChannelTurn(text string) bool {
	t := strings.TrimSpace(text)
	return strings.HasPrefix(t, "<channel") && strings.Contains(t, "liveshortly") && strings.HasSuffix(t, "</channel>")
}

type patchedFile struct {
	path           string
	diff           string
	added, removed int
}

// extractPatch pulls the raw patch text out of apply_patch arguments, which may
// be a JSON object ({"input": "*** Begin Patch…"}) or the patch string itself.
func extractPatch(arguments string) string {
	var a map[string]any
	if json.Unmarshal([]byte(arguments), &a) == nil {
		for _, k := range []string{"input", "patch", "content"} {
			if v, ok := a[k].(string); ok && v != "" {
				return v
			}
		}
	}
	return arguments
}

// parsePatchFiles walks a standard apply_patch envelope and returns per-file
// diffs with +/- line counts.
func parsePatchFiles(patch string) []patchedFile {
	var out []patchedFile
	var cur *patchedFile
	flush := func() {
		if cur != nil {
			cur.diff = strings.TrimRight(cur.diff, "\n")
			out = append(out, *cur)
			cur = nil
		}
	}
	for _, line := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(line, "*** Update File: "),
			strings.HasPrefix(line, "*** Add File: "),
			strings.HasPrefix(line, "*** Delete File: "):
			flush()
			p := line[strings.Index(line, ": ")+2:]
			cur = &patchedFile{path: strings.TrimSpace(p)}
		case strings.HasPrefix(line, "*** End Patch"), strings.HasPrefix(line, "*** Begin Patch"):
			// envelope markers — ignore
		default:
			if cur == nil {
				continue
			}
			if strings.HasPrefix(line, "+") {
				cur.added++
			} else if strings.HasPrefix(line, "-") {
				cur.removed++
			}
			cur.diff += line + "\n"
		}
	}
	flush()
	return out
}

func firstNonEmptyStr(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func isoNow() string { return time.Now().UTC().Format(time.RFC3339) }

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// codexOutputCap bounds codex tool output, replies and diffs the same as the
// Claude hook path (sidecar maxCaptureBytes), so codex sessions aren't cut
// to a 300-char stub.
const codexOutputCap = 256 * 1024
