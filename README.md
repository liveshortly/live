<h1 align="center">live</h1>

<p align="center">
  <em>Put <code>live</code> in front of your coding agent — and people can watch it work.</em>
</p>

<p align="center">
  <img src="https://img.shields.io/github/stars/liveshortly/live?style=flat-square&labelColor=1a1a1a&color=1f7a4d&label=stars" alt="Stars">
  <img src="https://img.shields.io/badge/works_with-claude_%C2%B7_gemini_%C2%B7_codex_%C2%B7_anything-1f7a4d?style=flat-square&labelColor=1a1a1a" alt="Works with any agent">
  <img src="https://img.shields.io/badge/built_with-Go-1f7a4d?style=flat-square&labelColor=1a1a1a&logo=go&logoColor=white" alt="Built with Go">
  <a href="https://liveshortly.com"><img src="https://img.shields.io/badge/watch-liveshortly.com-1f7a4d?style=flat-square&labelColor=1a1a1a" alt="liveshortly.com"></a>
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
  <code>curl -fsSL https://liveshortly.com/i.sh | bash</code>
</p>

---

```bash
live claude        # Claude Code — the full story: prompts, tools, diffs, replies
live agy           # Antigravity CLI — full story: prompts, tools, diffs, replies
live gemini        # some other agent? still works — we capture the terminal
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

## Getting it

### The easy way

```bash
curl -fsSL https://liveshortly.com/i.sh | bash
```

Downloads a prebuilt binary for your platform, checks it against its published
SHA-256, drops `live` on your PATH, and pops open the browser to sign in. Run it
again whenever you want to update. **No Go toolchain, no source checkout, no
build** — it's one static binary.

Binaries are published on every release for macOS and Linux, amd64 and arm64:
[liveshortly/live-dist releases](https://github.com/liveshortly/live-dist/releases),
mirrored at [liveshortly.com/install/](https://liveshortly.com/install/).

If you'd rather not pipe curl into bash, grab the binary and its `.sha256` from
either link, verify, `chmod +x`, and move it onto your PATH.

### Homebrew, if that's your thing

```bash
brew tap liveshortly/tap git@github.com:liveshortly/homebrew-tap.git
brew install liveshortly/tap/live
live login
```

Installs the same prebuilt binary as the curl installer — no Go, no compile. (The
tap repo is still private, so your SSH key needs access; once it's public this is
just `brew tap liveshortly/tap`.) Update later with `brew upgrade liveshortly/tap/live`.

### Keeping it current

Once it's installed, it updates itself:

```bash
live update                     # grab the latest
live update --version v0.1.1    # or pin a specific one (yes, downgrades too)
```

Under the hood it pulls checksum-verified binaries from
`https://liveshortly.com/install/` (one per platform, plus a `latest.txt`).
Every release stays up there, so nothing you've pinned ever disappears. The
installer takes the same pin if you prefer:

```bash
curl -fsSL https://liveshortly.com/i.sh | bash -s -- --version v1.0.1
```

### Or build it yourself

The source repo is private; this needs access.

```bash
git clone git@github.com:liveshortly/live.git && cd live
make install          # → /usr/local/bin/live
live login            # browser sign-in
live whoami           # make sure it took
```

Your login lives at `~/.liveshortly/credentials.json` (locked down to 0600)
and refreshes on its own.

### Point `live` at a local server

`live` reads its endpoint from `~/.liveshortly/host.json`. Two make targets flip
it:

```bash
make use-local        # → http://localhost:8080  (then: live login)
make use-prod         # → https://liveshortly.com (then: live login)
```

`use-local` targets the **single-origin nginx** stack (`docker-compose.local.yml`
in liveshortly-server, `:8080`) — not the base `:3000`/`:8000` split, whose
Next.js proxy buffers SSE and never streams live events. Override the origin with
`make use-local LOCAL_ORIGIN=http://host:port` if needed.

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
LIVE_WEB_PERMS=0 live claude # turn it off — plain native behavior
LIVE_TMUX=0      live claude # keep gating, but answer only from the web (no popup)
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
● live streaming → https://liveshortly.com/session/<id>
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

| Command | How it's captured | What shows up | Messages back |
|---|---|---|---|
| `live claude` | Claude Code hooks | prompts, tool calls, file diffs, replies, "waiting for you" pings, model auto-detect | straight into the agent |
| `live gemini` | terminal recording (proper hooks planned — Gemini CLI has a hook system now) | terminal output, cleaned up | shown to you |
| `live codex` | terminal recording (hooks planned — Codex has hooks too) | terminal output | shown to you |
| `live <anything>` | terminal recording | terminal output | shown to you |

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

For non-Claude agents there's no hook system to lean on, so we run them inside
a pseudo-terminal and record what scrolls by — mirroring it to you untouched.

Want the deeper dive? [docs/DESIGN.md](docs/DESIGN.md). And the server side
this client already talks to: [docs/SERVER-PROMPT.md](docs/SERVER-PROMPT.md).

## Knobs & files

| Thing | Where it lives |
|---|---|
| Which server | `~/.liveshortly/host.json` `{"api_url": "...", "web_url": "..."}`, or the `LIVESHORTLY_API_URL` env var, else localhost |
| Your login | `~/.liveshortly/credentials.json` (0600) — point elsewhere with `LIVESHORTLY_CRED_PATH` |
| Logs | `~/.liveshortly/live.log` (we never scribble over the agent's screen) |
| Idle messages | On by default for `live claude` — a per-session channel lets viewer messages reach Claude even while it's idle. First run, Claude Code shows a one-time "load development channels?" prompt — just say yes. Set `LIVE_CHANNEL=0` to turn it off. Channel logs at `~/.liveshortly/channel.log` |
| Approve tools from the web | `live --web-permissions claude` (or `LIVE_WEB_PERMS=1`). Off by default. When on, Claude pauses on each tool and waits for you to tap Approve/Deny on the session page instead of the keyboard — handy when you've stepped away. Only *you* (the owner) can answer; viewers just watch. Heads-up: while it's on, it gates every tool, so your local allowlist doesn't apply, and Claude sits paused until you answer (or it denies after a few minutes). |

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

- [ ] Proper hooks for Gemini CLI (real events instead of terminal scraping)
- [ ] Same for Codex
- [x] Real-time viewer comments (live push, no more polling)
- [x] Idle-viewer messages that reach Claude while it's waiting
- [x] Homebrew tap
- [x] Self-updating releases (`live update`)
- [ ] Let viewers answer Claude's "can I run this?" prompts from the web
- [ ] Visual asciinema-style replay alongside the event feed

It's early and moving fast — if you try it and something's janky, that's
useful to hear. 🚀

---

<p align="center">
  <sub>Made with 💚 by <a href="https://x.com/ironfisto/">@ironfisto</a> &middot; <a href="https://x.com/sec_r0">@sec_r0</a> &nbsp;·&nbsp; <a href="https://liveshortly.com">liveshortly.com</a></sub>
</p>
