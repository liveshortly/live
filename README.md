# live

**Stream any coding-agent session to [LiveShortly](https://liveshortly.com) — live, replayable, shareable — by prefixing one word.**

```bash
live claude        # Claude Code, full semantic capture (prompts, tools, diffs, responses)
live gemini        # any other agent CLI — terminal-output capture
live npm run dev   # works for any command at all
```

`live` is a single Go binary. Run your agent through it and viewers get a
live event feed of the session — every prompt, tool call, file diff, and
response — plus a replay link when you're done. Viewers can message the
session while it runs, and the agent sees those messages mid-task.

Nothing is ever captured unless you asked for it by typing `live`.

## Features

- **One binary, every agent.** Deep integration for Claude Code; universal
  terminal capture for anything else you can run in a shell.
- **Explicit, per-session.** `live claude` streams; plain `claude` doesn't.
- **Two-way.** Viewers with comment permission talk back; messages are
  delivered into the agent's context at the next safe boundary.
- **Zero-config sharing.** The share link prints the moment the session
  starts; the replay link prints when it ends. Exit codes are preserved.
- **Never in the way.** Capture is fully best-effort: if the API is down,
  your agent runs exactly as it would have without `live`.

## Install

### One command (does everything)

```bash
curl -fsSL https://liveshortly.com/install.sh | bash
```

That's the whole setup: it fetches the source, builds, puts `live` on your
PATH, and opens the browser sign-in. Rerun the same command any time to
update. The only prerequisite is Go 1.21+ ([go.dev/dl](https://go.dev/dl/)
or `brew install go`) — the script never installs or modifies Go itself and
stops with clear instructions if it's missing.

While the source repo is private, the clone step inside the script needs
your GitHub SSH key (or gh auth) for `resapce/live`. No-curl equivalents:

```bash
git clone git@github.com:resapce/live.git ~/.liveshortly/src/live && bash ~/.liveshortly/src/live/install.sh
# or, with the GitHub CLI:
gh api -H "Accept: application/vnd.github.raw" repos/resapce/live/contents/install.sh | bash
```

### Homebrew

```bash
brew tap resapce/tap git@github.com:resapce/homebrew-tap.git
brew install resapce/tap/live
live login
```

Builds the latest tagged release from source (the tap and repo are private,
so your GitHub SSH key must have access; once public this becomes plain
`brew tap resapce/tap`). Upgrade with `brew upgrade resapce/tap/live`.

### Manual (if you prefer)

```bash
git clone git@github.com:resapce/live.git && cd live
make install          # builds and copies to /usr/local/bin/live
live login            # browser device-flow approval
live whoami           # verify
```

Requires Go 1.22+ (the module pins its exact toolchain; Go auto-downloads it).

Credentials are stored at `~/.liveshortly/credentials.json` (mode 0600) and
refresh automatically.

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
session; `live` delivers messages to the agent:

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
 ├─ exec claude --settings <tmp> (inherits the real terminal)
 └─ on exit: flush events → POST /stop → print replay link
```

For Claude Code, `live` injects lifecycle hooks through a temporary
`--settings` file — that's what yields structured, semantic events instead
of raw terminal text. `live hook <event>` is the same binary in relay mode:
it forwards the hook's stdin JSON to the sidecar over localhost and prints
the sidecar's hook response (which is how viewer comments become
`additionalContext`). It always exits 0 in ≤6s, so capture problems can
never wedge the agent.

For everything else, `live` runs the command inside a pseudo-terminal and
records its output, mirroring the terminal untouched for you.

Full architecture and design decisions: [docs/DESIGN.md](docs/DESIGN.md).
Upcoming server-side capabilities this client is ready for:
[docs/SERVER-PROMPT.md](docs/SERVER-PROMPT.md).

## Configuration

| What | Where |
|---|---|
| API endpoint | `~/.liveshortly/host.json` `{"api_url": "...", "web_url": "..."}`, else `LIVESHORTLY_API_URL` env, else `http://localhost:8000` |
| Credentials | `~/.liveshortly/credentials.json` (0600), override with `LIVESHORTLY_CRED_PATH` |
| Diagnostics log | `~/.liveshortly/live.log` (`live` never prints over the agent's TUI) |

## Troubleshooting

- **"not signed in"** — `live login`. If it loops, delete
  `~/.liveshortly/credentials.json` and retry.
- **No events in the feed** — check `~/.liveshortly/live.log`; every upload
  failure is logged there with the reason.
- **Session stuck "live" after a crash** — the server reaps agent-silent
  sessions once presence support (docs/SERVER-PROMPT.md) is deployed;
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
internal/hostcfg/    endpoint + credential path resolution
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
- [x] Homebrew tap (`resapce/homebrew-tap`) — release binaries still to come
- [ ] asciinema-style visual replay upload alongside semantic events
