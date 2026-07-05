# Live shim — design

## Problem

The LiveShortly plugin model has two structural limits:

1. **Return path.** Hooks are ephemeral processes with 8–30s timeouts; they
   can only *pull* viewer messages at the moments they happen to fire. The
   plugin papered over this with an experimental MCP channel sidecar polling
   every 3s — Claude-only, fragile, and dependent on a shared state file.
2. **Activation & distribution.** Always-on once installed, one marketplace
   per agent, capture decided by state files instead of the user.

## Shape of the solution

`live <agent>` is a launcher that owns the whole session from one long-lived
process. The key decision: **the shim does not replace hooks for capture — it
provisions them.** Parsing a TUI's terminal output can never yield semantic
events (tool names, diffs); lifecycle hooks can. What hooks can't do is hold
a connection — the shim's persistent process does that.

```
┌───────────────────────── live (single process) ─────────────────────────┐
│ session lifecycle (create → stop)         auth (device flow + refresh)  │
│                                                                          │
│  sidecar                                                                 │
│   ├── event queue → POST /events    ordered, 3× retry, drop-on-overflow │
│   ├── comment receiver              SSE /agent/stream → poll fallback   │
│   └── hook HTTP server              127.0.0.1:<rand>, shared token      │
│                        ▲                                                 │
│                        │ POST /hook/<event> (stdin JSON relayed)         │
│   child: claude ── fires hooks ──► `live hook <event>` (same binary)     │
└──────────────────────────────────────────────────────────────────────────┘
```

## Decisions & rationale

**Parent process = sidecar.** No daemon management, no socket files, no
stale-state cleanup: the sidecar lives exactly as long as the session because
it *is* the session process. Hooks find it via `LIVE_SIDECAR` / `LIVE_TOKEN`
env vars inherited by the child agent.

**Hooks are dumb relays.** `live hook <event>` forwards stdin to the sidecar
and prints the response. All logic (event mapping, diffing, comment
injection, model detection) lives in one place, in Go, in the parent. Hook
latency budget: one localhost round trip.

**Comment delivery = queue + wake.** The server's pending queue
(`/comments/pending`, drain = destructive read) remains the source of truth.
The agent SSE stream (server change in flight, see SERVER-PROMPT.md) is only
a *wake signal*: on any pushed message the sidecar drains the queue. Exactly
-once delivery without ack machinery, and losing the socket loses nothing.
Until the server ships the stream, a 3s poll does the same job (the client
detects 404 and falls back automatically).

**Injection at hook boundaries only.** Comments become `additionalContext`
at `UserPromptSubmit` and `PostToolUse`. We deliberately do not type into the
agent's stdin: it races with the user and with turn state. (A future opt-in
`--interactive-viewers` may inject via PTY when the agent is verifiably
idle.) Consequence: a comment that arrives after the last tool call of the
final turn is not seen by the agent — it is still in the feed and delivered
next turn.

**Double-capture guard.** The temp settings file sets
`"enabledPlugins": {"liveshortly@liveshortly": false}` so the legacy plugin
does not open a second session for the same run. Verified working.

**PTY tier for everything else.** Any command runs under a pseudo-terminal;
output is mirrored untouched to the user and teed into `output` events
(ANSI-stripped, batched at 2s/8KB, 2000-char cap). Viewer comments overlay
in the operator's terminal. This is honest universality: view-only fidelity
for agents we don't have adapters for, semantic fidelity where we do.

**Event payloads mirror the plugin.** Same event types
(`prompt`, `pre_tool`, `tool_call`, `file_write`, `response`,
`input_requested`, `stream_end`) and same payload fields, so the web viewer
renders shim sessions and plugin sessions identically. Diffs are LCS-based
with the same 120-line cap.

**Failure posture: the agent always wins.** Every hook exits 0 with valid
JSON no matter what; the event queue drops rather than blocks; upload
failures are logged to `~/.liveshortly/live.log`, never printed over the
TUI; a dead API degrades the session to "not captured", never to "agent
broken".

## Server dependencies

Works against today's API. Upgrades automatically when the server adds
(see docs/SERVER-PROMPT.md):

| Server feature | Client behavior now | After |
|---|---|---|
| `GET /api/sessions/{id}/agent/stream` | 404 → 3s polling | real-time wake, <100ms comment latency |
| presence + reaper | crash leaves session "live" | agent-gone sessions auto-archived |
| `agent`/`capture_mode` fields | sent, ignored by server | shown in UI |

## Adapter roadmap

Gemini CLI (v0.26+) and Codex both ship Claude-style lifecycle hook systems;
their adapters are config-injection variants of the claude adapter (different
settings-file location and payload field names), mapping onto the same
sidecar hook handlers. Until then they run in the PTY tier.
