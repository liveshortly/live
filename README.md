<h1 align="center">live</h1>

<p align="center">
  <em>Put <code>live</code> in front of your coding agent — and people can watch it work.</em>
</p>

<p align="center">
  <img src="https://img.shields.io/github/stars/liveshortly/live?style=flat-square&labelColor=1a1a1a&color=1f7a4d&label=stars" alt="Stars">
  <img src="https://img.shields.io/badge/works_with-claude_%C2%B7_gemini_%C2%B7_codex_%C2%B7_anything-1f7a4d?style=flat-square&labelColor=1a1a1a" alt="Works with any agent">
  <img src="https://img.shields.io/badge/built_with-Go-1f7a4d?style=flat-square&labelColor=1a1a1a&logo=go&logoColor=white" alt="Built with Go">
  <img src="https://img.shields.io/badge/deploy-self--hosted-1f7a4d?style=flat-square" alt="Self-hosted">
</p>

<p align="center">
  <a href="https://github.com/liveshortly/live/raw/main/docs/demo.mp4">
    <img src="docs/demo.gif" width="100%" alt="live demo — run `live claude`, a shareable URL is generated, open it in the browser to watch the session stream live, and viewers talk back into the running session" />
  </a>
</p>
<p align="center"><sub><a href="https://github.com/liveshortly/live/raw/main/docs/demo.mp4">▶ watch in HD (mp4)</a></sub></p>

<p align="center">
  <strong>Any agent &middot; live in one word &middot; watch it, replay it, talk back to it.</strong><br>
  <sub>Type <code>live claude</code> instead of <code>claude</code> and a shareable link appears — friends, your team, or the internet watch every prompt, tool call and file diff as it happens, and message the session while it runs. When it ends, the link becomes a replay. One small Go binary; nothing is captured unless you typed <code>live</code>.</sub>
</p>

<p align="center">
  <code>curl -fsSL https://raw.githubusercontent.com/liveshortly/live-dist/main/install.sh | bash</code>
</p>

---

After [configuring your server](#choose-your-server):

```bash
live claude        # Claude Code — the full story: prompts, tools, diffs, replies
live agy           # Antigravity CLI — full story: prompts, tools, diffs, replies
live gemini        # Gemini CLI — prompts, tools, diffs, replies via its hooks
live npm run dev   # honestly, run whatever you want through it
```

That's the whole idea: you type `live claude` instead of `claude`, and now
there's a shareable link where people watch the session unfold live — every
prompt, tool call and file diff — and message you while it's happening. When
you're done, it turns into a replay. It started as a weekend "wouldn't it be
cool if…" and turned out to be pretty cool. 🙂

## Why you might like it

- **One binary, works with everything.** Deep hooks for Claude Code, and a
  "just record the terminal" fallback for any other CLI on earth.
- **You opt in, every time.** No always-on daemon, no background magic —
  `live claude` streams, `claude` doesn't.
- **Viewers can talk back.** Anyone with comment access can message the
  session, and the agent actually sees it — even while it's sitting idle
  waiting for you.
- **The link just appears.** Share link when it starts, replay link when it
  ends. No dashboards to click through.
- **It stays out of your way.** If the server's down or you're offline, your
  agent runs exactly like normal. Capture is best-effort; your work is not.

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/liveshortly/live-dist/main/install.sh | bash
```

Installs a checksum-verified binary for macOS/Linux, arm64/amd64, from
[GitHub Releases](https://github.com/liveshortly/live-dist/releases). No Go
toolchain or automatic login. Then `live login` signs you in to
[liveshortly.com](https://liveshortly.com) (or [your own server](#choose-your-server)). To inspect the
installer first, download that URL to a file and run it with `bash install.sh`.

### Homebrew

```bash
brew tap liveshortly/tap
brew install liveshortly/tap/live
```

### Build from source

Install the Go version in `go.mod` (or use Go's automatic toolchain download):

```bash
git clone https://github.com/liveshortly/live.git
cd live
mkdir -p "$HOME/.local/bin"
go build -o "$HOME/.local/bin/live" ./cmd/live
export PATH="$HOME/.local/bin:$PATH"
```

Source builds use the current checkout; release downloads contain the most
recently published version. The public source repository is the place to report
issues and contribute changes. If the Homebrew tap is not yet publicly available,
use the installer or build from source.

### Updates

```bash
live update
live update --version v2.1.2
# Homebrew-managed installations:
brew upgrade liveshortly/tap/live
```

The updater uses GitHub Releases independently of your session server. A server
operator can set `LIVE_INSTALL_BASE=https://downloads.example.com/live` to use a
flat mirror containing `latest.txt`, the `.gz` assets, and their `.sha256` files.
For a GitHub-compatible release host, set `LIVE_GH_BASE` and `LIVE_LATEST_URL`.
The installer accepts `--version`, `--bin-dir`, `--no-modify-path`, and
`--from-source`. Login is opt-in with `--login`;
`--no-login` remains accepted for older scripts.

## Choose your server

By default `live` uses the hosted service at **https://liveshortly.com** —
`live login`, `live claude`, done. Self-hosting is first-class: deploy one of
these setups on infrastructure you control and point the CLI at it.

### pocket.ai: standalone daemon

The standalone daemon and GitHub-based `live update` changes are currently on
`main`; until the next binary release, [build from source](#build-from-source)
for these features. The installer itself already downloads directly from GitHub.

Install and sign in to your chosen agent CLIs on the server, then:

```bash
export LIVE_POCKET_KEY="$(openssl rand -hex 32)"
live daemon --server --dir /absolute/path/to/project
```

Keep the key for phone setup. In pocket.ai, select **LiveShortly**, enter the
server address (for example `http://your-server.local:8787`) and the same key,
then **Test connection**, select an agent/workspace, and save. The daemon listens
on all interfaces by default. Use HTTPS or a private network for remote access.
No web platform, Google login, Postgres, or Redis is needed for this mode.
See [Standalone server for pocket.ai](#pocketai-support-in-live-daemon) for TLS,
agent adapters, persistence, API routes and approval support.

### Browser sessions: self-host the web platform

Deploy [liveshortly-server](https://github.com/liveshortly/liveshortly-server)
using its [self-hosting guide](https://github.com/liveshortly/liveshortly-server/blob/main/deploy/DEPLOY.md).
It includes the web UI, API, Postgres, Redis, and a streaming-aware reverse proxy.
Configure your own Google OAuth client to enable browser and CLI login.

Then point the CLI at the same origin users open in their browser:

```bash
mkdir -p "$HOME/.liveshortly"
cat > "$HOME/.liveshortly/host.json" <<'EOF'
{"api_url":"https://live.example.com","web_url":"https://live.example.com"}
EOF
live login
live whoami
live claude
# Register a machine for the web UI's session launcher:
live daemon --dir /absolute/path/to/project
```

For the local Compose stack, use `http://localhost:8080` for both URLs. From a
checkout, `make use-local` or
`make use-server SERVER_ORIGIN=https://live.example.com` writes `host.json`.
Server resolution: `host.json` → `LIVESHORTLY_API_URL` / `LIVESHORTLY_WEB_URL`
→ `https://liveshortly.com`. Delete `host.json` (or `make use-prod`) to go back
to the hosted service. Credentials in `~/.liveshortly/credentials.json` belong
to one server; run `live logout && live login` after changing servers. Data and
accounts are not transferred between the hosted service and a self-hosted one.

### tmux 🪄

`brew install tmux` / `apt install tmux`.

**Required for `live codex`.** Codex runs inside a tmux session so viewer
comments can be typed into its composer — that is the only way a comment reaches
codex while it sits idle. Without tmux, `live codex` falls back to a mode where
web comments are delivered only if codex happens to poll for them.

**Recommended for `live claude`**, where it powers the clean CLI permission
popups (`LIVE_TMUX=1`, below). Without it, permission control still works from
the web.

## Answering Claude's permission prompts

**On by default** for interactive `live claude` (v0.4.0+). Instead of only the
terminal, you approve Claude's tool runs from a **tmux popup** (when tmux is
installed) or **the session page** — handy when you're watching from your
phone or streaming it. Answer from whichever, first one wins.

- **Allow / Deny / Allow-always** — owner only. "Allow always" remembers the
  command for the session so it stops asking (Claude's "don't ask again").
- With **tmux** installed, Claude runs in an isolated tmux session so the
  prompt shows in a `display-popup` without touching Claude's TUI. Without
  tmux, a plain `live claude` uses Claude's **native terminal prompts** (so
  install tmux for the in-terminal popup).
- While on, every write/execute/fetch tool is gated (your allowlist is
  bypassed) — use **allow-always** to quiet the repeats. Unanswered prompts
  deny after ~8 minutes.

```bash
live claude                 # permission control on (tmux popup if tmux present)
live --no-permissions claude  # turn it off for this run — plain native behavior
```

Headless `-p`/`--print` runs are never gated.

## Using it

```bash
live claude                       # normal interactive Claude Code, streamed
live claude -p "fix the tests"    # one-shot runs get captured too
live --title "friday demo" claude # give the session a name
live gemini                       # any other agent (terminal capture)
live -- ./scripts/deploy.sh       # anything at all; `--` ends live's flags
```

When it starts you'll see your link:

```
● live streaming → https://live.example.com/session/<id>
```

When it exits, it wraps up the session and prints the replay link. Your
agent's exit code passes straight through, so scripts don't care that `live`
was in the middle.

### What the people watching get

They see the feed roll by live — prompts, tool calls, file diffs, replies.
Anyone allowed to comment can send you a message, and `live` gets it to the
agent:

- **Claude:** it lands in Claude's context at the next natural pause (a
  prompt, a tool call, or when it's idle waiting on you), so it can actually
  react mid-task.
- **Everything else (terminal capture):** the message pops up for *you* as a
  yellow `[live] 💬` line in your terminal — random CLIs can't safely have
  text injected into them, so you relay it.

## Browsing and joining sessions from the terminal

Everything the website shows you, without the website:

```bash
live ls                     # the 10 most recent, one line each
live ls --all               # every one of them
live ls --live              # only the ones running right now
live ls -q auth --ended     # search titles
live shared                 # what other people have shared with you
live info <id>              # one session in full, plus its last events
live hosts                  # your machines running `live daemon`
```

`<id>` is the eight-character handle every listing prints — or a full session
id, or a pasted session link. Add `--json` to `ls` if you'd rather pipe it.

### Joining someone else's session

This is the part that makes two terminals talk to each other:

```bash
live join <id>              # watch it live AND talk to the agent
live watch <id>             # same, read-only
```

`live join` prints the last ~20 turns for context, then streams the session as
it happens. Anything you type goes to the running agent as a viewer message —
the same channel the web composer uses — so the person whose terminal it is
sees your message land in their agent, and you see the reply come back. Two
people, two CLIs, one agent in the middle.

It reads like the web viewer, not like a log:

```
 › Rohit Sehgal · you 08:31:32
   append a line 'v2 tui' to README.md, then run wc -l

 ⟳ Bash · running  08:31:35
   git status --short

 ▸ Bash  08:31:35
   git status --short
   ⎿ ?? README.md

 ✎ Edit  +1 −0  08:33:15
   …/scratchpad/demo/README.md
    hello
   +v2 tui

 ◆ claude-opus-5 · agent 08:33:17
   Status: `?? README.md` (untracked). Line `v2 tui` appended.

────────────────────────────────────────────────────────────────────────
[4m · 2◉] ❯ nice — now add a test
```

Every turn names who produced it, replies are shown in full rather than cut to
one line, diffs are coloured with their `+`/`−` counts, and the prompt at the
bottom carries the session's uptime and how many people are watching.

While the agent is mid-task the prompt says so, with a spinner:

```
[4m · 2◉] ⠹ Claude is working… ❯
```

It holds for the whole turn rather than a few seconds — a hooks-captured agent
emits nothing at all while the model thinks, and that silent stretch is exactly
when you want to know something is happening. It clears the moment the reply
lands, or when the agent stops to ask you something. `live watch` carries the
same indicator on its own status line.

Your input line stays put: an event arriving while you are halfway through a
sentence is printed above the prompt, not spliced into it. `↑`/`↓` recall what
you have sent, `Ctrl-U` clears the line, `Ctrl-W` deletes a word, `Ctrl-C`
leaves. Piping works too (`echo "ship it" | live join <id>`).

### Answering permission prompts from a join

When the agent stops to ask permission for a tool, whoever is joined can answer
it — not just whoever is sitting at the session's own terminal:

```
 ⌐ waiting for permission 09:12:21
   Claude wants to run Write: …/scratchpad/demo/perm2.txt
   ⏸ the agent is waiting — /allow, /deny or /always answers it from here

[BLOCKED — /allow or /deny] ❯ /allow
```

The prompt itself turns into a `BLOCKED` chip so a stalled session is obvious
even after the request scrolls away, and joining an already-stuck session says
so up front. The verdict goes through the same endpoint the web viewer uses and
carries the same authorization — commenter access, the same right that lets you
talk to the session — so the running agent picks it up within a second and
everybody watching sees who answered.

Slash commands while joined: `/allow`, `/deny`, `/always`, `/info`, `/who`,
`/tail`, `/url`, `/fork`, `/stop`, `/quit`. `/help` lists them.

Add `--full` to print the entire history instead of the last 20 turns, or
`-n <count>` to pick your own.

To let someone in, grant them access:

```bash
live share <id> them@example.com                 # commenter — they can talk
live share <id> them@example.com --role viewer   # watch only
live share <id> --list                           # who has access
```

And to wind one up from anywhere:

```bash
live stop <id>              # end a running session
live open <id>              # open its page in a browser
```

## Who works with what

Run any of these as `live <agent>`. The same table, with the live-run
checklist, is in [docs/HARNESS-MATRIX.md](docs/HARNESS-MATRIX.md).

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
| `aider`, `cursor-agent`, `hermes`, `copilot`, `opencode`, `crush`, `goose`, `amp`, `qwen`, `pi` and any other command | ⚠ terminal recording (raw lines, no structure) | ❌ the agent's own prompt, in the terminal only | ⚠ typed in at the agent's prompt when it is idle (the named agents; any other command only with `type_comments` in agents.json or `LIVE_TYPE_COMMENTS=1`) | ⚠ typed in once output is quiet and no dialog is on screen (echo confirmed, Enter retried once; session notice after 20 s). A heuristic, not yet checked per agent; `ready_pattern` in agents.json pins the prompt | ⚠ `cursor-agent` (`--model`), or any agent given `models` + `model_flag` in agents.json | ❌ | ⚠ not given to the agent — prior context shown in the terminal and saved as a digest file | — | aider 0.82.3, cursor-agent 2026.10.01, hermes 0.21.5 | unsupported | no prompt/reply/tool rows, no stream_end, no model. Speaks ACP? Run it in ACP mode (row above) |

✅ works · ⚠ partial (reason given) · ❌ not supported. Generated from `internal/harness` by `go run ./scripts/harnessmatrix`; do not edit by hand.
<!-- harness-matrix:end -->

### Any agent that speaks ACP

An agent live has never heard of, including one released after this binary,
gets a structured feed, web approvals and comment delivery if it speaks the
[Agent Client Protocol](https://agentclientprotocol.com). Run its ACP entry
point with `LIVE_ACP=1`:

```bash
LIVE_ACP=1 live goose acp
LIVE_ACP=1 live gemini --experimental-acp
```

For web-spawned sessions, mark it in `~/.liveshortly/agents.json`:

```json
[{ "name": "goose", "command": "goose", "args": ["acp"], "acp": true }]
```

The session is headless: send the first instruction from the session page.

### Messages to terminal agents

A viewer message to aider, cursor-agent, hermes, copilot, opencode, crush,
goose, amp, qwen or pi is typed into the agent once it is idle at its prompt:
output quiet for 1.5 s, no yes/no or trust dialog on screen. The message is
submitted only after the agent echoes it. If the agent's prompt is
recognisable, pin it with a regexp in agents.json:

```json
[{ "name": "aider", "ready_pattern": "(?m)^\\S*> ?$" }]
```

Any other command gets messages typed in only with `"type_comments": true`
(or `LIVE_TYPE_COMMENTS=1`), since it may be a shell.

### Choosing the model from the web

`live daemon` registers, for each agent, the models the web may start it with:
the agent's own listing where it has one (codex's model cache, `agy models`,
`cursor-agent --list-models`, `ollama list`), otherwise a short list shipped
with live, plus your configured default. The daemon only accepts a model from
that list and passes it as the agent's own `--model` flag. For an agents.json
adapter, list them yourself:

```json
[{ "name": "hermes", "models": ["nous/hermes-4-405b"], "model_flag": "-m" }]
```

## How it actually works

```
live claude
 ├─ sign in (~/.liveshortly/credentials.json, auto-refreshes)
 ├─ open a session          → get the share link
 ├─ spin up a little sidecar (lives as long as your session)
 │    ├─ ships events to the server (ordered, retried)
 │    ├─ listens for viewer comments (real-time push, polling as fallback)
 │    └─ a tiny localhost endpoint for the hooks to talk to
 ├─ hand Claude a temp settings file wiring every hook to `live hook <event>`
 ├─ run claude for real (it owns your terminal like normal)
 └─ on exit: flush everything → close the session → print the replay link
```

The trick for Claude is those lifecycle hooks — that's how we get real
structured events (this file changed, that command ran) instead of just
scraping terminal text. `live hook <event>` is the same binary wearing a
different hat: it just relays the hook's data to the sidecar and hands back
the response. It always bails out cleanly in a few seconds, so even if
something's broken on our end, Claude never hangs waiting on us.

Gemini and Antigravity get the same treatment through their own hook systems,
and codex is read from its structured rollout file (or driven over its
app-server protocol). Everything else runs inside a pseudo-terminal and we
record what scrolls by — mirroring it to you untouched.

Want the deeper dive? [docs/DESIGN.md](docs/DESIGN.md). And the server side
this client already talks to: [docs/SERVER-PROMPT.md](docs/SERVER-PROMPT.md).

## Knobs & files

| Thing | Where it lives |
|---|---|
| Which server | `~/.liveshortly/host.json` `{"api_url": "...", "web_url": "..."}`, or the `LIVESHORTLY_API_URL` env var, else https://liveshortly.com |
| Your login | `~/.liveshortly/credentials.json` (0600) — point elsewhere with `LIVESHORTLY_CRED_PATH` |
| Logs | `~/.liveshortly/live.log` (we never scribble over the agent's screen) |
| Idle messages | On by default for `live claude` — a per-session channel lets viewer messages reach Claude even while it's idle. First run, Claude Code shows a one-time "load development channels?" prompt — just say yes. Set `LIVE_CHANNEL=0` to turn it off. Channel logs at `~/.liveshortly/channel.log` |
| Approve tools from the web | On by default for `live claude`, `live agy`, `live gemini` and `live codex`: each tool waits for Approve/Deny from a tmux popup or the session page. Only *you* (the owner) can answer; viewers just watch. While on, it gates every tool, so your local allowlist doesn't apply; an unanswered prompt denies after ~8 minutes. `live --no-permissions <agent>` turns it off for a run. |

## When something's off

- **"not signed in"** — run `live login`. If it keeps looping, delete
  `~/.liveshortly/credentials.json` and try again.
- **Nothing showing up in the feed** — peek at `~/.liveshortly/live.log`,
  every failed upload logs why.
- **Session stuck on "live" after a crash** — the server cleans up sessions
  whose agent went quiet; worst case it wraps up on the next stop.

## Poking at the code

```bash
make build      # → ./bin/live
make vet
go test ./...
```

Roughly where things are:

```
cmd/live/            the CLI: run / login / whoami / hook relay
internal/hostcfg/    figuring out the server + where creds live
internal/auth/       sign-in, token refresh, the credentials file
internal/api/        talking to LiveShortly (REST + live stream)
internal/sidecar/    event queue, comment listener, hook handlers, diffs
internal/adapter/    Claude (hook injection) and the terminal-capture runner
```

## Where this is headed

- [x] Proper hooks for Gemini CLI (real events instead of terminal scraping)
- [x] Same for Codex (rollout file, app-server) and Antigravity (hooks)
- [x] Real-time viewer comments (live push, no more polling)
- [x] Idle-viewer messages that reach Claude while it's waiting
- [x] Homebrew tap
- [x] Self-updating releases (`live update`)
- [x] Let the owner answer "can I run this?" prompts from the web
- [ ] Visual asciinema-style replay alongside the event feed

It's early and moving fast — if you try it and something's janky, that's
useful to hear. 🚀

---

<p align="center">
  <sub>Made with 💚 by <a href="https://x.com/ironfisto/">@ironfisto</a> &middot; <a href="https://x.com/sec_r0">@sec_r0</a> &nbsp;·&nbsp; <a href="https://github.com/liveshortly/live">GitHub</a></sub>
</p>

## pocket.ai support in live daemon

The existing daemon can listen directly for **pocket.ai by LiveShortly** clients:

```sh
go build -o bin/live ./cmd/live
./bin/live daemon --server --key 'your-chosen-key' --dir /absolute/path/to/project
```

Pocket only needs a server URL and key. There is no application-name flag or
application header. Any non-empty key is accepted. `LIVE_POCKET_KEY` is an
alternative to `--key`. By default the daemon listens on **all interfaces** at
`:8787`; override with `--listen`. Repeat `--dir` for multiple workspaces. `--name`
optionally changes the displayed server name (otherwise the hostname is used).

Use `--tls-cert` and `--tls-key`, or a TLS reverse proxy, for HTTPS. LAN HTTP
sends the bearer key in plaintext. Every API route requires
`Authorization: Bearer <key>`. The key grants access to all sessions on this daemon.

Discovery checks PATH for Codex, Claude, Gemini, AGY, Antigravity, Ollama, Aider and
Cursor Agent. Test connection refreshes this list and enumerates locally pulled
Ollama models. Agent installation, active session count and last-turn status are
reported separately; discovery does not verify provider login or quota. Install
and configure the CLI on the daemon host before using it from Pocket. Agent
approval controls remain enabled (see Approvals below). Ollama sessions require a selected local model;
uninstalled models are rejected instead of being downloaded implicitly.

### Additional agents

Register other CLI agents through a local JSON file, then pass `--agents-file`:

```json
[
  {"name":"my-agent","command":"/path/to/my-agent","args":["--print"]}
]
```

Adapters receive the prompt on stdin and must print their answer to stdout.
An argument containing `{prompt}` receives the prompt as a literal argv value if
that CLI requires a command-line prompt. No shell is used. These commands are
configured only by the server operator; API clients cannot supply arbitrary
executables or arguments. Custom agents appear when their command is executable.

`live agents` lists every agent the daemon finds (with its path), the ones it
probed but did not find, and the agents.json entries. The daemon searches your
login shell's PATH plus the usual global-bin directories (`~/.local/bin`,
`~/.bun/bin`, `~/.npm-global/bin`, pnpm, volta, mise/asdf shims, `~/go/bin`,
`~/.cargo/bin`). A CLI that live does not know by name (for example `pig`) needs
an agents.json entry.

### API and lifecycle

| Method | Route | Purpose |
| --- | --- | --- |
| GET | `/v1/info` | Server name, installed agents, models, workspaces and agent statuses |
| GET | `/v1/sessions` | Session summaries, newest first |
| POST | `/v1/sessions` | Create with `{"agent":"codex","directory":"/allowed/path"}`; Ollama also requires `"model"` |
| GET | `/v1/sessions/{id}` | Status and complete conversation |
| POST | `/v1/sessions/{id}/messages` | Submit `{"text":"..."}`; returns 202 while working |
| POST | `/v1/sessions/{id}/stop` | Cancel a turn and its subprocess group |
| POST | `/v1/sessions/{id}/permissions/{requestId}` | Answer a pending request with `{"decision":"allow"}` or `{"decision":"deny"}` |
| DELETE | `/v1/sessions/{id}` | Delete an idle session |

Turns continue independently of client connections. Poll until `running` becomes
`idle`, `failed`, or `interrupted`. After an ambiguous connection failure, fetch
the session before resubmitting. Limits: one active turn per session, four total,
200 sessions, 50 turns per session, 32,000-byte messages, 256 KiB output and a
15-minute timeout. The most recent 96 KB of context is replayed to a fresh CLI
process each turn. Native CLI thread resumption is not implemented. Existing
website/tmux sessions are separate from these API sessions.

### Approvals in pocket.ai

Codex uses its app-server protocol with on-request approvals and the workspace-write
sandbox. Claude and AGY/Antigravity use PreToolUse hooks: each tool call waits for
an explicit decision in the phone's chat. The app shows the complete tool input,
**Approve once**, and **Deny**. Codex and Claude retain their native permission
checks. As in the existing LiveShortly web/tmux adapter, AGY delegates terminal
prompts to the installed, enabled PreToolUse gate using its
`--dangerously-skip-permissions` flag. The gate still requires a phone decision for
every tool; it denies on relay failures, timeout, cancellation, and invalid input.
AGY is not launched in this mode if hook installation or enabling fails.

While the agent waits, the session stays `running` and exposes a `permissions`
array containing each request's ID, tool, detail and expiry. Reconnecting retrieves
the same pending requests. Answers require the daemon key, apply only to that
request, and reject stale/replayed IDs. Requests expire after eight minutes;
stopping a turn or restarting the daemon denies outstanding requests. A Codex
permission-profile grant is limited to the current turn, as required by its native
protocol. The key holder can answer any session on this daemon.

Claude receives temporary settings. AGY installs a separate `liveshortly-pocket`
plugin under `~/.liveshortly/pocket-hook-plugin`; it is inert outside pocket-launched
processes and does not replace the existing `liveshortly` plugin. Hook traffic uses
a loopback-only listener and a random per-turn token. Failed relays deny the tool.

Agent discovery reports `approvals: supported`, `unsupported`, or `not-needed`
(the "pocket.ai approvals" column in [Who works with what](#who-works-with-what)).
Gemini, Cursor, Aider and custom command adapters currently have no phone approval
bridge; their existing CLI permission behavior remains in effect. Ollama's chat
adapter does not execute tools. The app displays this support status in connection
settings.

Session files remain at `~/.liveshortly/pocket/liveshortly-pocket` for compatibility
with existing installations; `--state-dir` overrides this. A lock prevents duplicate
daemons using the same store. History survives restarts. Interrupted turns are
marked accordingly. A launch-directory allowlist is not a general filesystem
sandbox: agents run as the daemon's OS user. Use a dedicated user/container for
untrusted collaborators. Restart with a new key to rotate access.

The original web-connected `live daemon --dir ...` mode is unchanged.

Validation: `go test -race ./cmd/live`, `go test ./...`, `go vet ./...`. The Pocket
repository also includes `python3 scripts/test_liveshortly.py` for cross-project
contract and persistence tests without provider calls.
