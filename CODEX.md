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

## Server

Zero changes. Create/fork already take `framework`+`captureMode`; the handoff
digest is agent-neutral.
