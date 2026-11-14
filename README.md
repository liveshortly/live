# live

`live` is a small Go CLI that you put in front of a coding agent. Type
`live claude` instead of `claude` and you get a link where people can watch the
session as it happens (prompts, tool calls, file diffs, replies), send messages
into the running agent, and approve its tool runs. When the session ends the
link becomes a replay. It works with Claude Code, Codex, Antigravity (`agy`),
Gemini CLI, Ollama, pi and any other command. Sessions stream to a LiveShortly
server: the hosted one at [liveshortly.com](https://liveshortly.com) by default,
or your own copy of [liveshortly-server](https://github.com/liveshortly/liveshortly-server).
`live daemon` lets the web app start sessions on your machine, and
`live daemon --server` lets the PocketAI apps for Mac and iPhone run agents on
your machine directly, without any server. All of these projects live under
[github.com/liveshortly](https://github.com/liveshortly): see [PocketAI.Mac](https://github.com/liveshortly/PocketAI.Mac), [PocketAI.Iphone](https://github.com/liveshortly/PocketAI.Iphone) and their shared [PocketAI.Core](https://github.com/liveshortly/PocketAI.Core).

<p align="center">
  <a href="https://github.com/liveshortly/live/raw/main/docs/demo.mp4">
    <img src="docs/demo.gif" width="100%" alt="Running live claude prints a session link; the browser shows the session streaming live and a viewer sending a message into it" />
  </a>
</p>

Nothing is captured unless you typed `live`. If the server is down or you are
offline, the agent runs normally and the agent's exit code passes through.

## Contents

- [Quick start](#quick-start)
- [Install](#install)
- [Using it](#using-it)
- [Run locally (development)](#run-locally-development)
- [Self-host](#self-host)
- [Build from source](#build-from-source)
- [Tests](#tests)
- [Repository layout](#repository-layout)
- [Reference](#reference)
- [Contributing](#contributing)
- [License](#license)

## Quick start

```bash
curl -fsSL https://raw.githubusercontent.com/liveshortly/live-dist/main/install.sh | bash
live login          # signs in to liveshortly.com, or to your server (see Self-host)
live claude         # prints the session link, then runs Claude Code as usual
```

## Install

Installer (macOS and Linux, arm64 and amd64, checksum-verified binary from
[GitHub Releases](https://github.com/liveshortly/live-dist/releases)):

```bash
curl -fsSL https://raw.githubusercontent.com/liveshortly/live-dist/main/install.sh | bash
```

The installer accepts `--version`, `--bin-dir`, `--no-modify-path`,
`--from-source` and `--login`. To read it first, download the URL to a file
and run `bash install.sh`.

Homebrew:

```bash
brew install liveshortly/tap/live
```

Or [build from source](#build-from-source).

### Updates

```bash
live update
live update --version v2.1.2          # a specific release
brew upgrade liveshortly/tap/live     # Homebrew installs
```

`live update` downloads from GitHub Releases, independent of the session
server you use. To serve releases from your own mirror, set
`LIVE_INSTALL_BASE` to a flat directory holding `latest.txt`, the `.gz`
binaries and their `.sha256` files (the layout `make release` produces), or set
`LIVE_GH_BASE` and `LIVE_LATEST_URL` for a GitHub-compatible release host.

### tmux

Install tmux (`brew install tmux` / `apt install tmux`). `live codex` needs it
to type viewer messages into Codex's composer. `live claude` uses it for the
in-terminal approval popup; without it, approvals still work from the web.

## Using it

```bash
live claude                         # Claude Code, streamed
live claude -p "fix the tests"      # one-shot runs too
live --title "friday demo" claude   # name the session
live codex                          # any supported agent
live -- ./scripts/deploy.sh         # any command; `--` ends live's own flags
live --no-permissions claude        # turn off web approvals for this run
```

On start, `live` prints the session link; on exit it prints the replay link.

Viewers with comment access can message the session. For hook-integrated
agents (Claude, Codex, Antigravity, Gemini) the message reaches the agent at
its next pause, including while it is idle. For plain terminal capture it is
typed in when the agent is idle at its prompt, or shown to you as a
`[live]` line to relay.

Tool approvals are on by default for `live claude`, `live codex`, `live agy`
and `live gemini`: each tool run waits for Allow / Deny / Allow always from the
session page or a tmux popup. Only the owner, or someone joined with commenter
access, can answer. Unanswered requests are denied after about 8 minutes.

### From the terminal

```bash
live ls                     # recent sessions (--all, --live, --ended, -q <text>, --json)
live shared                 # sessions shared with you
live info <id>              # one session and its last events
live join <id>              # stream a session and talk to its agent
live watch <id>             # same, read-only
live share <id> them@example.com [--role viewer]
live stop <id>
live open <id>
live hosts                  # your machines running `live daemon`
live agents                 # agents this machine can run
```

`<id>` is the short handle a listing prints, a full session id, or a pasted
session link. While joined, `/allow`, `/deny` and `/always` answer a pending
approval; `/help` lists the other slash commands.

### Starting sessions from the web: `live daemon`

```bash
live daemon --dir /absolute/path/to/project
```

The machine appears under **New session** in the web app. The server can only
pick from the agents this machine found on its `PATH` (plus your
`~/.liveshortly/agents.json`) and the directories you passed with `--dir`
(repeatable); it cannot send a command line.

### PocketAI apps: `live daemon --server`

For the PocketAI apps you do not need a LiveShortly server or account. Run the
daemon in server mode on the machine that has your agent CLIs installed and
signed in:

```bash
export LIVE_POCKET_KEY="$(openssl rand -hex 32)"
live daemon --server --dir /absolute/path/to/project
```

It listens on `:8787` on all interfaces (`--listen` to change it). In the app,
choose **LiveShortly**, enter `http://<machine>:8787` and the key, then **Test
connection**. `--key` works instead of `LIVE_POCKET_KEY`; `--name`,
`--state-dir`, `--agents-file`, `--tls-cert` and `--tls-key` are also
accepted. Over plain HTTP the key is sent unencrypted, so use TLS or a private
network, and run the daemon as a dedicated user if others hold the key. Details
are in [Reference: daemon server mode](#daemon-server-mode).

## Run locally (development)

To develop `live` against a local server, start
[liveshortly-server](https://github.com/liveshortly/liveshortly-server) with its
`docker-compose.local.yml` stack (one origin on `http://localhost:8080`, needed
for sign-in and streaming), then:

```bash
make build                                   # ./bin/live
./bin/live host set http://localhost:8080    # or: make use-local
./bin/live login
./bin/live claude
```

`./bin/live host reset` (or deleting `~/.liveshortly/host.json`) switches back
to liveshortly.com. Logs go to `~/.liveshortly/live.log`.

## Self-host

`live` needs no server of its own beyond one of these:

- **Your own LiveShortly server** for the web app, accounts, sharing and
  replays. Deploy it with liveshortly-server's
  [self-hosting guide](https://github.com/liveshortly/liveshortly-server/blob/main/web/content/docs/self-hosting.md),
  then on each agent machine:

  ```bash
  live host set https://live.example.com
  live login
  live whoami
  ```

  Use the same origin users open in the browser; the API and web app share it.
  `live host` prints the server in use and where the setting came from.
  Credentials (`~/.liveshortly/credentials.json`) belong to one server, so run
  `live logout && live login` after switching. Accounts and data are not
  shared between liveshortly.com and your server.

- **No server at all**, for the PocketAI apps: `live daemon --server` (see
  [above](#pocketai-apps-live-daemon---server)).

How `live` picks the server, first match wins:

1. `~/.liveshortly/host.json` (`{"api_url": "...", "web_url": "..."}`), written by `live host set`
2. `LIVESHORTLY_API_URL` (and `LIVESHORTLY_WEB_URL` if the web origin differs)
3. `https://liveshortly.com`

From a checkout, `make use-server SERVER_ORIGIN=https://live.example.com`
writes the same `host.json`.

## Build from source

Install the Go version in `go.mod` (or let Go download it automatically):

```bash
git clone https://github.com/liveshortly/live.git
cd live
make build                 # ./bin/live, version from git describe
make install               # copies to /usr/local/bin (PREFIX=... to change)
```

Without make: `go build -o "$HOME/.local/bin/live" ./cmd/live`.

`make release` cross-compiles stripped binaries for darwin and linux
(arm64/amd64) into `dist/`, with `.sha256` files, and refuses to run on a dirty
tree unless you pass `VERSION=vX.Y.Z`.

A container that runs a streamed Claude Code session in isolation is in
[`deploy/docker/`](deploy/docker/README.md).

## Tests

```bash
make vet
make test                         # go test ./...
go test -race ./cmd/live
go run ./scripts/harnessmatrix -check   # README/docs agent matrix is up to date
```

CI also checks the installer: `shellcheck -S error install.sh`, `bash -n
install.sh` and `python3 scripts/test_install.py`. Per-agent recorded replays
live in `testdata/`; see [docs/HARNESS-TESTING.md](docs/HARNESS-TESTING.md).

## Repository layout

```
cmd/live/            the CLI: run, login, host, daemon, join, ls, update, hook relay, ...
internal/hostcfg/    server selection and credential path
internal/auth/       sign-in, token refresh, credentials file
internal/api/        LiveShortly API client (REST and live stream)
internal/sidecar/    event queue, comment listener, hook handlers, diffs
internal/adapter/    per-agent integration and terminal capture
internal/harness/    agent support matrix (source of the table below)
internal/perm/       approval gating
scripts/             matrix generator, installer test, formula bump
deploy/docker/       agent sandbox image
docs/                design notes, harness matrix and testing guide
testdata/            recorded agent sessions for replay tests
install.sh           installer
```

## Reference

### Files and environment

| Setting | Where |
|---|---|
| Server | `~/.liveshortly/host.json`, else `LIVESHORTLY_API_URL` / `LIVESHORTLY_WEB_URL`, else liveshortly.com |
| Login | `~/.liveshortly/credentials.json` (0600); override with `LIVESHORTLY_CRED_PATH` |
| Logs | `~/.liveshortly/live.log`, `~/.liveshortly/channel.log` |
| Custom agents | `~/.liveshortly/agents.json` |
| Idle messages to Claude | On by default via a per-session channel; Claude Code asks once to load development channels. `LIVE_CHANNEL=0` turns it off |
| tmux | `LIVE_TMUX=0` runs without tmux |
| ACP agents | `LIVE_ACP=1 live <agent's ACP command>` |
| Type messages into any command | `LIVE_TYPE_COMMENTS=1` |
| Codex app-server mode | `LIVE_CODEX_APPSERVER=1` |

### Who works with what

Run any of these as `live <agent>`. The table and a live-run checklist are also
in [docs/HARNESS-MATRIX.md](docs/HARNESS-MATRIX.md).

<!-- harness-matrix:start -->
| Agent | Capture | Approvals | Viewer comments | First message wakes idle agent | Model choice at start | Model switch | Handoff | Min version | Golden replays | pocket.ai approvals | Known gaps |
|---|---|---|---|---|---|---|---|---|---|---|---|
| `claude` | ✅ Claude Code hooks | ✅ gated from the web or tmux popup; fails closed | ✅ injected (channel, hook boundaries, Stop) | ✅ channel push; held until the channel connects | ✅ `--model` (opus, sonnet, haiku, your default) | ✅ `/model` from the session page | ✅ seed prompt injected | — | claude 2.1.316 | supported | — |
| `codex` | ✅ rollout file, codex in tmux | ✅ gated (PreToolUse hook); fails closed. Off with `--no-permissions`, `--yolo`, `-a never` | ✅ typed into codex's composer | ✅ typed into the composer once it is drawn (waits up to 20 s; session notice if it never is) | ✅ `--model` (codex's model list) | ❌ | ✅ seed prompt injected | — | codex 0.142.5, codex 0.166.0 | supported | no mid-session model switch |
| `codex` (`LIVE_TMUX=0`) | ✅ rollout file, codex in a PTY | ✅ gated (PreToolUse hook); fails closed | ⚠ codex pulls them via the `poll_viewer_messages` channel tool (`LIVE_CHANNEL=0` turns it off) | ❌ waits for codex to call `poll_viewer_messages`; an idle codex never does | ✅ `--model` (codex's model list) | ❌ | ✅ seed prompt injected | — | codex 0.142.5, codex 0.166.0 | supported | comments wait until codex calls the channel tool; no model switch |
| `codex` (`LIVE_CODEX_APPSERVER=1`) | ✅ app-server JSON-RPC (headless) | ✅ answered from the web; deny on timeout | ✅ steer the running turn, or start a new one | ✅ starts a turn (held on the server until the thread is open) | ✅ `model` on thread/start | ❌ | ✅ developer instructions | — | codex-appserver 0.166.0 | supported | file approvals say only "apply a file change"; no terminal UI; codex questions get no answers; no model switch |
| `agy`, `antigravity` | ✅ hooks (`liveshortly` plugin) | ✅ gated; fails closed; live exits if the plugin can't install | ✅ every comment in full, at the next model step | ✅ typed in once the composer is ready (waits up to 20 s, confirms, retries once; notice names trust/sign-in) | ✅ `--model` (`agy models`) | ❌ | ⚠ prior context shown in the pane and saved as a digest; agy has no system-prompt flag and `-i` would start a turn, so it isn't given to agy | — | agy 1.4.9 | supported | reply arrives when the turn ends; no model switch |
| `gemini` | ✅ hooks (`liveshortly` extension) | ⚠ gated, fails closed — but gemini's own prompt stays in charge when hooks may not fire (hooks/extensions disabled, allowlist, GitHub Actions, your own `--yolo`/`--approval-mode`) | ✅ typed into the composer while idle; at tool and turn boundaries while busy (tmux runs; without tmux, boundaries only) | ✅ typed in once gemini is idle at its composer (waits up to 20 s, confirms, retries once; notice names trust/sign-in); mid-turn it waits for the next hook | ✅ `--model` (shipped list + your default) | ❌ | ⚠ not given to the agent — prior context shown in the terminal and saved as a digest file | 0.26 | gemini 0.65.0 | unsupported | untrusted folder: approve twice; env redaction strips `LIVE_TOKEN` (capture off, gated tools denied); without tmux comments wait while idle; no model switch |
| `gemini` (gemini < 0.26) | ⚠ terminal recording | ❌ gemini's own prompt, in the terminal only | ⚠ shown to the operator only | ❌ shown to the operator only | ✅ `--model` (shipped list + your default) | ❌ | ⚠ not given to the agent — prior context shown in the terminal and saved as a digest file | — | gemini-pty 0.65.0 | unsupported | no structure; upgrade to 0.26+ |
| `ollama` | ⚠ terminal recording, whole lines | — no tools | ✅ typed in as one line | ✅ typed in as one line once ollama is idle at its prompt; an early one waits for it | ✅ `ollama run <model>` (pulled models; required) | ❌ | ⚠ not given to the agent — prior context shown in the terminal and saved as a digest file | — | ollama 0.30.10 | not-needed | no prompt/tool rows, no stream_end, no model |
| `any ACP agent` (`LIVE_ACP=1` or `"acp": true` in agents.json) | ✅ Agent Client Protocol over stdio (headless) | ✅ answered from the web; fails closed | ✅ sent as the next turn (queued while one runs) | ✅ sent as the first turn (held on the server until the ACP session is open) | ⚠ only via `models` + `model_flag` in agents.json | ❌ | ✅ digest prepended to the first turn | ACP protocol v1 | acp 1 | unsupported | no terminal UI; comments wait for the running turn; no model switch |
| `pi` | ✅ pi RPC protocol over stdio (`pi --mode rpc`) | ✅ answered from the web (confirm/allow dialogs); fails closed | ✅ sent as the next turn, or steered into the running one | ✅ sent as the first turn (held on the server until pi is up) | ✅ `--model provider/id` (pi's `--list-models`) | ❌ | ✅ digest prepended to the first turn | 1.5 | — | unsupported | multi-choice questions are shown but not yet answered from the web; no mid-session model switch yet |
| `aider`, `cursor-agent`, `hermes`, `copilot`, `opencode`, `crush`, `goose`, `amp`, `qwen` and any other command | ⚠ terminal recording (raw lines, no structure) | ❌ the agent's own prompt, in the terminal only | ⚠ typed in at the agent's prompt when it is idle (the named agents; any other command only with `type_comments` in agents.json or `LIVE_TYPE_COMMENTS=1`) | ⚠ typed in once output is quiet and no dialog is on screen (echo confirmed, Enter retried once; session notice after 20 s). A heuristic, not yet checked per agent; `ready_pattern` in agents.json pins the prompt | ⚠ `cursor-agent` (`--model`), or any agent given `models` + `model_flag` in agents.json | ❌ | ⚠ not given to the agent — prior context shown in the terminal and saved as a digest file | — | aider 0.82.3, cursor-agent 2026.10.01, hermes 0.21.5 | unsupported | no prompt/reply/tool rows, no stream_end, no model. Speaks ACP? Run it in ACP mode (row above) |

✅ works · ⚠ partial (reason given) · ❌ not supported. Generated from `internal/harness` by `go run ./scripts/harnessmatrix`; do not edit by hand.
<!-- harness-matrix:end -->

### Custom and ACP agents

An agent that speaks the [Agent Client Protocol](https://agentclientprotocol.com)
gets a structured feed, web approvals and message delivery:

```bash
LIVE_ACP=1 live goose acp
```

For web-started sessions, describe agents in `~/.liveshortly/agents.json`:

```json
[
  { "name": "goose", "command": "goose", "args": ["acp"], "acp": true },
  { "name": "aider", "ready_pattern": "(?m)^\\S*> ?$" },
  { "name": "hermes", "models": ["nous/hermes-4-405b"], "model_flag": "-m" }
]
```

`ready_pattern` pins the regexp for an agent's idle prompt, `type_comments:
true` allows typing messages into an unknown command, and `models` +
`model_flag` list the models the web may start it with. `live agents` shows
what the daemon found and where.

### Daemon server mode

`live daemon --server` serves a small HTTP API for the PocketAI apps. Every
route requires `Authorization: Bearer <key>`, and the key grants access to
every session on the daemon.

| Method | Route | Purpose |
|---|---|---|
| GET | `/v1/info` | Server name, agents, models, workspaces, statuses |
| GET | `/v1/sessions` | Session summaries, newest first |
| POST | `/v1/sessions` | Create with `{"agent":"codex","directory":"/allowed/path"}`; Ollama also needs `"model"` |
| GET | `/v1/sessions/{id}` | Status and conversation |
| POST | `/v1/sessions/{id}/messages` | Send `{"text":"..."}`; returns 202 while working |
| POST | `/v1/sessions/{id}/stop` | Cancel the current turn |
| POST | `/v1/sessions/{id}/permissions/{requestId}` | `{"decision":"allow"}` or `{"decision":"deny"}` |
| DELETE | `/v1/sessions/{id}` | Delete an idle session |

Limits: one running turn per session and four in total, 200 sessions, 50 turns
per session, 32,000-byte messages, 256 KiB output, 15-minute turn timeout. The
last 96 KB of context is replayed to a fresh agent process each turn. Sessions
are stored in `~/.liveshortly/pocket/liveshortly-pocket` (change with
`--state-dir`) and survive restarts.

`--agents-file` adds agents for this mode: each gets the prompt on stdin (or in
an argument containing `{prompt}`) and prints its answer to stdout. No shell is
used, and clients cannot supply commands or arguments.

Phone approvals are supported for Claude, Codex and Antigravity; each tool run
waits for **Approve once** or **Deny** and is denied after 8 minutes, on relay
failure, or when the turn stops. Other agents keep their own permission
behaviour; Ollama runs no tools.

### How it works

```
live claude
 ├─ sign in (~/.liveshortly/credentials.json, auto-refreshes)
 ├─ open a session          → print the share link
 ├─ start a sidecar for the life of the session
 │    ├─ ships events to the server (ordered, retried)
 │    ├─ listens for viewer messages (push, polling as fallback)
 │    └─ serves a localhost endpoint for the hooks
 ├─ give Claude a temporary settings file wiring every hook to `live hook <event>`
 ├─ run claude in your terminal as normal
 └─ on exit: flush, close the session, print the replay link
```

Gemini and Antigravity use their own hook systems, Codex is read from its
rollout file (or driven over its app-server protocol), and anything else runs
in a pseudo-terminal whose output is recorded. More in
[docs/DESIGN.md](docs/DESIGN.md).

### Troubleshooting

- **"not signed in"**: run `live login`. If it loops, delete
  `~/.liveshortly/credentials.json` and try again.
- **Wrong server**: `live host` shows which server is in use and why.
- **Nothing in the feed**: check `~/.liveshortly/live.log`; failed uploads are
  logged there.
- **Session stuck on live after a crash**: the server ends sessions whose agent
  has gone quiet.

## Contributing

Issues and pull requests are welcome on
[github.com/liveshortly/live](https://github.com/liveshortly/live). Run
`make vet` and `make test` before opening a pull request. If you change agent
support in `internal/harness`, regenerate the matrix with
`go run ./scripts/harnessmatrix`. Release notes go in [CHANGELOG.md](CHANGELOG.md).
<!-- TODO(owner): add CONTRIBUTING.md / code of conduct / security contact if wanted. -->

## License

The `live` CLI is released under the [Apache License 2.0](LICENSE). The LiveShortly server and web app are a separate project under AGPL-3.0.
