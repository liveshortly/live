<h1 align="center">live</h1>

<p align="center">
  <em>Put <code>live</code> in front of your coding agent — and people can watch it work.</em>
</p>

<p align="center">
  <img src="https://img.shields.io/github/stars/resapce/live?style=flat-square&labelColor=1a1a1a&color=1f7a4d&label=stars" alt="Stars">
  <img src="https://img.shields.io/badge/works_with-claude_%C2%B7_gemini_%C2%B7_codex_%C2%B7_anything-1f7a4d?style=flat-square&labelColor=1a1a1a" alt="Works with any agent">
  <img src="https://img.shields.io/badge/built_with-Go-1f7a4d?style=flat-square&labelColor=1a1a1a&logo=go&logoColor=white" alt="Built with Go">
  <a href="https://liveshortly.com"><img src="https://img.shields.io/badge/watch-liveshortly.com-1f7a4d?style=flat-square&labelColor=1a1a1a" alt="liveshortly.com"></a>
</p>

<p align="center">
  <video src="https://github.com/resapce/Live/raw/main/docs/demo.mp4" poster="docs/demo-poster.jpg" width="100%" autoplay loop muted playsinline controls>
    <img src="docs/demo.gif" width="100%" alt="live demo — run `live claude`, a shareable URL is generated, open it in the browser to watch the session stream live, and viewers talk back into the running session" />
  </video>
</p>

<p align="center">
  <strong>Any agent &middot; live in one word &middot; watch it, replay it, talk back to it.</strong><br>
  <sub>Type <code>live claude</code> instead of <code>claude</code> and a shareable link appears — friends, your team, or the internet watch every prompt, tool call and file diff as it happens, and message the session while it runs. When it ends, the link becomes a replay. One small Go binary; nothing is captured unless you typed <code>live</code>.</sub>
</p>

<p align="center">
  <code>curl -fsSL https://liveshortly.com/install.sh | bash</code>
</p>

---

```bash
live claude        # Claude Code — the full story: prompts, tools, diffs, replies
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
curl -fsSL https://liveshortly.com/install.sh | bash
```

Fetches the source, builds it, drops `live` on your PATH, and pops open the
browser to sign in. Run it again whenever you want to update. You'll need
Go 1.21+ ([go.dev/dl](https://go.dev/dl/) or `brew install go`) — the script
won't touch your Go setup, it just checks it's there and tells you if it
isn't.

Heads up: the source repo is private for now, so the clone step wants your
GitHub SSH key (or `gh` auth). If you'd rather not pipe curl into bash:

```bash
git clone git@github.com:resapce/live.git ~/.liveshortly/src/live && bash ~/.liveshortly/src/live/install.sh
# or, with the GitHub CLI:
gh api -H "Accept: application/vnd.github.raw" repos/resapce/live/contents/install.sh | bash
```

### Homebrew, if that's your thing

```bash
brew tap resapce/tap git@github.com:resapce/homebrew-tap.git
brew install resapce/tap/live
live login
```

Builds the latest tagged release. (Repo's private, so again your SSH key needs
access — once it's public this is just `brew tap resapce/tap`.) Update later
with `brew upgrade resapce/tap/live`.

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
curl -fsSL https://liveshortly.com/install.sh | bash -s -- --version v0.1.1
```

### Or just build it yourself

```bash
git clone git@github.com:resapce/live.git && cd live
make install          # → /usr/local/bin/live
live login            # browser sign-in
live whoami           # make sure it took
```

Your login lives at `~/.liveshortly/credentials.json` (locked down to 0600)
and refreshes on its own.

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
