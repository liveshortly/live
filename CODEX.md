# `live codex` — Codex support

Brings `live codex` to parity with `live claude`: structured live feed, session
sharing, handoff/fork, two-way viewer comments, and (opt-in) web-gated
permissions. Verified against **codex-cli 0.142.5**.

## Capture — rollout-file tailer (`captureMode: "rollout"`)

`profileFor("codex")` → `{framework: "codex", captureMode: "rollout"}`.
`runRolloutMode` (`cmd/live/rollout.go`) runs codex interactively in a PTY and,
in parallel, tails codex's own structured session file and maps its events to
the existing sidecar feed events — no server changes.

- **Source of truth:** `~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl` (honors
  `CODEX_HOME`). Discovered by snapshotting existing files, then picking the new
  one whose `session_meta.cwd` matches, appearing after launch.
- **Envelope:** each line is `{timestamp, type, payload}`; the semantic type is
  `payload.type`.
- **Mapping** (`rolloutMapper`):

  | codex `payload.type` | feed event |
  |---|---|
  | `message` role=user (minus `<environment_context>`/`<permissions>` preambles) | `prompt` |
  | `message` role=assistant | `response` |
  | `function_call` (exec_command/shell) + its `function_call_output` (paired by `call_id`) | `tool_call` |
  | `function_call` name=`apply_patch` | `file_write` + diff |
  | `event_msg` task_complete / turn_aborted | `stream_end` |
  | `turn_context`/`session_meta` `model` | reports the model once |

  Shell output is unwrapped from codex's `…Output:\n<actual>` envelope.

## Handoff parity — injected seed prompt

`agentProfile.acceptsInitialPrompt` is true for claude and codex. On
`--handoff`/`--session`, `seedHandoff` injects the orient prompt as codex's
initial `[prompt]` positional (`appendSeedPrompt`) instead of printing a paste
banner. Same flag-ordering care as the claude channel (see `6a75b17`).

## Viewer comments — channel MCP (standard-tool mode)

The `live channel` MCP is loaded into codex **per-session and ephemeral** via
`-c mcp_servers.liveshortly_channel.*` launch overrides — never `codex mcp add`
(nothing persists in `~/.codex/config.toml`). Because codex is a standard MCP
client and ignores Claude's `notifications/claude/channel` extension, the
channel server runs in **tool mode** (`LIVE_CHANNEL_TOOL=1`): it exposes a
`poll_viewer_messages` MCP tool that returns buffered viewer comments. Default
on; `LIVE_CHANNEL=0` disables.

## Web-gated permissions — codex hooks (opt-in: `LIVE_CODEX_PERMS=1`)

**Spike result: fully working.** Codex's hook system is a Claude-compatible
implementation:

- The `hooks` feature is **stable/enabled** (`plugin_hooks` is removed — hooks
  are config-based now).
- A **`PreToolUse`** hook set via ephemeral `-c hooks.PreToolUse=[…]` (no plugin
  install) **fires in both exec and interactive modes**. `--dangerously-bypass-hook-trust`
  skips the one-time trust prompt for this self-vetted hook.
- Codex **normalizes** hook payloads to Claude's schema — a shell call arrives
  as `tool_name:"Bash"`, `tool_input:{"command":…}`, with `session_id`,
  `transcript_path`, `hook_event_name` — so the existing sidecar
  `hookPreToolUse` handler is reused **unchanged**.
- The output wire format is identical to Claude's
  (`hookSpecificOutput.permissionDecision`/`permissionDecisionReason`), and a
  `deny` **actually blocks** the tool (verified: a denied `touch` never ran).

Opt-in (not default) because codex has no local-popup fallback like Claude's
tmux mode — a solo, unwatched session would block every tool until the grace
window denies. The matcher uses Claude tool names
(`Edit|Write|MultiEdit|Bash|WebFetch`).

## Web viewer

`frameworkLabel()` renders a "Codex" badge; the event shapes the tailer emits
(`prompt`/`response`/`tool_call`/`file_write`/`stream_end`) are already rendered
by `EventStream` identically to Claude — schema is shared, so no viewer changes
beyond the label.

## Real-time mode — the app-server client (opt-in: `LIVE_CODEX_APPSERVER=1`)

The default rollout mode is a *sidecar to the native codex TUI*: comments are
pulled (`poll_viewer_messages`) and permissions are hook-gated. For true
real-time — **push** comments and **browser-answered** permission prompts —
`LIVE_CODEX_APPSERVER=1` switches to driving a **headless `codex app-server`**
over its JSON-RPC protocol (`runAppServerMode`, `appserver*.go`):

- **Handshake:** `initialize` → `thread/start` (cwd, `approvalPolicy:on-request`,
  `sandbox:workspace-write`) → `turn/start` with the initial prompt / handoff
  seed.
- **Streaming → feed, live.** `item/completed` notifications map to feed events
  by `item.type`: `userMessage`→`prompt`, `agentMessage`→`response`,
  `commandExecution`→`tool_call`, `fileChange`→`file_write`; `turn/completed`→
  `stream_end`. (Same rows the web already renders.)
- **Permissions in the UI, real-time.** Codex's server→client approval requests
  (`item/commandExecution/requestApproval`, `item/fileChange/requestApproval`)
  are answered via `sidecar.RequestPermission` — the owner's browser allow/deny
  becomes codex's `ReviewDecision` (`approved`/`denied`). No hook, no poll.
- **Comments as push.** A viewer comment is injected as a real turn instantly —
  `turn/steer` while a turn is running, `turn/start` when idle.

Tradeoff: codex is **headless** here — no terminal TUI. The operator watches,
steers, and approves entirely from the session page; the terminal just holds the
process until Ctrl-C. Verified end-to-end against a real `codex app-server`
(`TestCodexAppServerIntegration`, gated by `LIVE_CODEX_IT=1`); the notification
mapping is hermetically unit-tested against captured 0.142.5 shapes.

Default stays the native-TUI rollout mode; app-server is opt-in until the web
gains a first-class "drive this session" input surface.

## Server

Zero changes. Create/fork already take `framework`+`captureMode`; the handoff
digest is agent-neutral. The app-server approval flow reuses the existing
web-permission path (`sidecar.RequestPermission`).
