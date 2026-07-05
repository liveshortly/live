# live

**Stream any coding-agent session to [LiveShortly](https://liveshortly.com) — live, replayable, shareable — by prefixing one word.**

```bash
live claude        # Claude Code, full semantic capture (prompts, tools, diffs, responses)
live gemini        # any other agent CLI — terminal-output capture
live npm run dev   # works for any command at all
```

`live` is a single Go binary that replaces the LiveShortly Claude-Code plugin
with an explicit, per-session, agent-agnostic launcher. Nothing is captured
unless you asked for it by typing `live`.

---

## Why a shim instead of a plugin?

| | plugin (hooks-only) | `live` shim |
|---|---|---|
| Activation | always-on once installed | explicit, per session |
| Install | per-agent marketplace | one binary, every agent |
| Viewer → agent delivery | polling inside short-lived hook processes | persistent process: SSE push (or 3s polling fallback), injected at the next hook boundary |
| Session end detection | inferred from hooks that sometimes don't fire | process exit — always correct |
| Non-Claude agents | not supported | PTY capture for anything |

The shim still uses Claude Code's lifecycle hooks for *capture* (they're the
only way to get structured tool events), but it injects them itself via a
temporary `--settings` file and runs a persistent in-process sidecar that owns
auth, upload retries, and the viewer-comment return path.

## Install

### From source (currently the only channel — repo is private)

```bash
git clone git@github.com:resapce/live.git
cd live
make install          # builds and copies to /usr/local/bin/live
# or: go build -o ~/bin/live ./cmd/live
```

Requires Go 1.22+.

### Sign in

```bash
live login            # opens the browser device-flow approval page
live whoami           # verify
```

Credentials are stored at `~/.liveshortly/credentials.json` — the same file
the LiveShortly plugin uses, so **an existing plugin login works as-is** and
vice versa.

## Usage

```bash
live claude                       # interactive Claude Code, captured
live claude -p "fix the tests"    # print-mode runs are captured too
live --title "demo run" claude    # custom session title
live gemini                       # PTY capture (see support matrix)
live -- ./scripts/deploy.sh       # any command; `--` ends live's own flags
```

On launch the shim prints the share link:

```
● live streaming → https://liveshortly.com/session/<id>
```

On exit it stops and archives the session and prints the replay link.
The agent's exit code is preserved.

### What viewers can do

Viewers on the session page see the event feed live (prompts, tool calls,
file diffs, responses). Anyone with comment permission can message the
session; the shim delivers messages to the agent:

- **claude (hooks mode):** injected as context at the next prompt or
  tool-use boundary — Claude reads them mid-task and can respond inline.
- **PTY mode:** shown to *you* as a yellow `[live] 💬` overlay in the
  terminal (arbitrary agents can't accept injected input safely).

## Agent support matrix

| Command | Capture | Events | Viewer messages |
|---|---|---|---|
| `live claude` | lifecycle hooks | prompt, pre_tool, tool_call, file_write (with diffs), response, input_requested, stream_end + model auto-detect | injected into the agent |
| `live gemini` | PTY (hooks adapter planned — Gemini CLI v0.26+ has a hooks system) | output (ANSI-stripped, batched) | operator overlay |
| `live codex` | PTY (hooks adapter planned — Codex supports hooks.json) | output | operator overlay |
| `live <anything>` | PTY | output | operator overlay |

## How it works

```
live claude
 ├─ device-flow auth (~/.liveshortly/credentials.json, auto-refresh)
 ├─ POST /api/sessions            → session id + share link
 ├─ start in-process sidecar
 │    ├─ event queue → POST /events (ordered, retried, batched)
 │    ├─ viewer comments: SSE /agent/stream when the server supports it,
 │    │  3s polling of /comments/pending otherwise (drain = ack)
 │    └─ localhost hook endpoint (random port + shared token)
 ├─ write temp settings: every Claude hook = `live hook <event>`
 │  (also disables the legacy liveshortly plugin to avoid double capture)
 ├─ exec claude --settings <tmp> (inherits the real terminal)
 └─ on exit: flush events → POST /stop → print replay link
```

`live hook <event>` is the same binary in relay mode: it forwards the hook's
stdin JSON to the sidecar over localhost and prints the sidecar's hook
response (which is how viewer comments become `additionalContext`). It always
exits 0 in ≤6s so a broken sidecar can never wedge the agent.

Full architecture and design decisions: [docs/DESIGN.md](docs/DESIGN.md).
Server-side changes this client is ready for: [docs/SERVER-PROMPT.md](docs/SERVER-PROMPT.md).

## Configuration

| What | Where |
|---|---|
| API endpoint | `~/.liveshortly/host.json` `{"api_url": "...", "web_url": "..."}`, else `LIVESHORTLY_API_URL` env, else `http://localhost:8000` |
| Credentials | `~/.liveshortly/credentials.json` (0600), override with `LIVESHORTLY_CRED_PATH` |
| Diagnostics log | `~/.liveshortly/live.log` (the shim never prints over the agent's TUI) |

## Troubleshooting

- **"not signed in"** — `live login`. If it loops, delete
  `~/.liveshortly/credentials.json` and retry.
- **No events in the feed** — check `~/.liveshortly/live.log`; every upload
  failure is logged there with the reason.
- **Duplicate sessions when running `live claude`** — an old liveshortly
  plugin is still enabled under a non-standard marketplace name. `live`
  disables `liveshortly@liveshortly` automatically; disable others with
  `claude /plugin` or uninstall the plugin.
- **Session stuck "live" after a crash** — the server reaps agent-silent
  sessions once the presence feature (docs/SERVER-PROMPT.md) is deployed;
  until then it ends on the next `/stop` or archive pass.

## Development

```bash
make build      # ./bin/live
make vet
go test ./...
```

Layout:

```
cmd/live/            CLI entry: run / login / whoami / hook relay
internal/hostcfg/    endpoint + credential path resolution (plugin-compatible)
internal/auth/       device flow, token refresh, credentials.json
internal/api/        LiveShortly REST + agent SSE client
internal/sidecar/    event queue, comment receiver, hook handlers, diffs
internal/adapter/    claude (--settings hook injection) and PTY runners
```

## Roadmap

- [ ] Gemini CLI hooks adapter (semantic events instead of PTY)
- [ ] Codex hooks adapter
- [ ] Real-time comment push via `/agent/stream` (client ready; server change in flight)
- [ ] `live --interactive-viewers`: opt-in PTY stdin injection when the agent is idle
- [ ] Homebrew tap + release binaries
- [ ] asciinema-style visual replay upload alongside semantic events
