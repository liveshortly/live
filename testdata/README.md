# Harness fixtures (LSH-00)

One folder per harness and version: `testdata/<agent>/<version>/`. Each holds
the raw capture a harness hands to `live`, and `events.golden.json`, the feed
events `live` makes from it. Replay tests push every capture through the real
mapper and compare the result with the golden:

| Capture file | Harness path | Replayed through | Test |
|---|---|---|---|
| `hooks.jsonl` (+ `transcript.jsonl`) | claude, agy, gemini hooks | `sidecar.handleHook` (`internal/sidecar/hooks.go`) | `internal/sidecar/harness_replay_test.go` |
| `rollout.jsonl` | codex rollout tail (tmux default, `LIVE_TMUX=0`) | `rolloutMapper` (`cmd/live/rollout.go`) | `cmd/live/harness_replay_test.go` |
| `jsonrpc.jsonl` | codex app-server (`LIVE_CODEX_APPSERVER=1`) | `codexApp.readLoop` (`cmd/live/appserver*.go`) | same |
| `pty.jsonl` | ollama, gemini < 0.26, any terminal agent | idle batching + `adapter.StripANSI` (mirrors `runPTYMode`) | same |

A golden pins what the code does **today**, gaps included (see Findings). A
fix that changes what viewers see shows up as a golden diff to review:

```bash
go test ./... -run Replay -update && git diff testdata/
```

When a harness is upgraded, add a new version folder; keep the old one.
Formats are described at the top of each test file. PTY captures are recorded
with `scripts/record-pty.py`; everything else is described in
`docs/HARNESS-TESTING.md` §5.

## Provenance

**Real** = captured from the harness on this machine. **Synthetic** = written by
hand from the named source, because a real run needs the owner's account or
quota. Replace a synthetic fixture with a real capture when someone runs the
matrix (docs/HARNESS-TESTING.md); keep the folder name.

| Fixture | Kind | Source |
|---|---|---|
| `ollama/0.30.10/pty.jsonl` | **real** | `scripts/record-pty.py … -- ollama run phi4-mini`, local model, no network, 2026-11-03. The machine was heavily loaded (load avg ~20), so tokens arrived ~15 s apart. |
| `claude/2.1.316/hooks.jsonl` | synthetic | Claude Code hook stdin as documented (SessionStart, UserPromptSubmit, PreToolUse, PostToolUse, Notification, Stop) and the matchers in `internal/adapter/claude.go`. A real run costs the owner's Claude quota. |
| `agy/1.4.9/hooks.jsonl`, `transcript.jsonl` | synthetic | Hook envelope (`toolCall`, `stepIdx`, `transcriptPath`, `modelName`, `fullyIdle`) from `internal/sidecar/hooks.go`. Tool names, arg keys and the transcript step shape (`GENERIC` step, `Command output:`) copied from local agy 1.4.9 transcripts (`~/.gemini/antigravity-cli/brain/*/.system_generated/logs/transcript.jsonl`). Unverified: whether hook args are JSON-encoded the way the transcript stores them (`"\"ls\""`). |
| `codex/0.142.5/rollout.jsonl` | synthetic | The 0.142.5 shapes already used by `rollout_test.go` (`live/CODEX.md`). Kept as the regression baseline for the old `function_call exec_command` / `apply_patch` format. |
| `codex/0.166.0/rollout.jsonl` | synthetic | Line types and keys copied from local codex-tui 0.163–0.165 rollout files (`~/.codex/sessions/**`): `custom_tool_call` `exec` (code mode), `event_msg/item_completed` with `CommandExecution` / `FileChange` / `AgentMessage`, `function_call` `wait`. No 0.166.0 rollout existed locally. |
| `codex-appserver/0.166.0/jsonrpc.jsonl` | synthetic | Checked against `codex app-server generate-json-schema` from the installed codex-cli 0.166.0 (`ThreadItem` variants, `FileUpdateChange` array, server-request method names). |
| `gemini/0.65.0/hooks.jsonl` | synthetic | Hook stdin as documented in gemini-cli 0.65.0 `docs/hooks/reference.md` (base fields, BeforeAgent `prompt`, BeforeModel `llm_request.model`, Before/AfterTool `tool_name`/`tool_input`/`tool_response`, AfterAgent `prompt_response`, Notification `ToolPermission`) with argument keys from `docs/reference/tools.md`. The AfterAgent retry carrying viewer messages through BeforeAgent follows the 0.65.0 source. Checked live on gemini 0.66.0 only that the extension's SessionStart hook reaches `live hook`. A real run costs the owner's Google quota. |
| `gemini-pty/0.65.0/pty.jsonl` | synthetic | Modelled on the gemini-cli TUI (banner, input box, spinner, tool rows, approval dialog). Gemini 0.26+ now runs on hooks; this pins the PTY fallback for older gemini. |
| `aider/0.82.3/pty.jsonl` | synthetic | aider's line REPL (banner, `>` prompt, SEARCH/REPLACE block, y/n confirmations). |
| `cursor-agent/2026.10.01/pty.jsonl` | synthetic | cursor-agent's full-screen TUI (alternate screen, cursor-addressed redraws). |
| `hermes/0.21.5/pty.jsonl` | synthetic | hermes' line REPL with streamed reply fragments. |

No qwen fixture: `qwen` on this Mac is a local wrapper script (`~/bin/qwen`) for
a remote box, not Qwen Code. It takes the same PTY path as aider.

## Findings the goldens pin (link each fix to its card)

Already fixed by #27, and pinned here as fixed: agy gets every comment's
text, asks and receipts name the real agent, and app-server `file_write`
carries the diff.

- **codex ≥ 0.163 (LSH-02):** the rollout mapper only knows `function_call`
  `exec_command`/`apply_patch`. Current codex runs shell and patches inside
  `custom_tool_call` `exec` and reports them as `item_completed`
  `CommandExecution`/`FileChange`, so `codex/0.166.0` produces **no tool or
  file rows**, and the `wait` helper call becomes a bogus `tool_call` named
  `wait`. (0.142.5 also shows the apply_patch result as a stray `output` row.)
- **codex app-server (LSH-02):** no model is reported, and
  `item/tool/requestUserInput` is answered with `{"decision":"denied"}`,
  which is not that request's response shape.
- **agy 1.4.9 (LSH-03):** the newer tool names `read_file`, `edit_file`,
  `write_file` fall through to the generic branch. No `file_write` row, and
  approvals read "Antigravity wants to run edit_file: edit_file". Only the old
  `replace_file_content`/`write_to_file` names produce diffs. `run_command`
  output keeps the `Created At…/Command output:` header because
  `cleanToolOutput` looks for `Output:\n`. Every PreInvocation re-emits the
  transcript's last user prompt, so the prompt row repeats each model step.
- **PTY agents (LSH-04/05):** the real ollama capture shows a slow model split
  into one `output` event per token (600 ms idle cut). The REPL's grey
  placeholder text ("Send a message (/? for help)") glues onto the typed
  prompt, because cursor-back/erase sequences are dropped, not replayed. A
  wrapped-line redraw duplicates a word ("be be of"). Full-screen TUIs
  (gemini, cursor-agent) arrive as box-drawing text, with prompt and reply
  mixed into one block.
