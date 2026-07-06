# live

**Put `live` in front of your coding agent and people can watch it work.**

```bash
live claude        # Claude Code — the full story: prompts, tools, diffs, replies
live gemini        # some other agent? still works — we capture the terminal
live npm run dev   # honestly, run whatever you want through it
```

That's the whole idea. You type `live claude` instead of `claude`, and now
there's a shareable link where friends (or your team, or the internet) can
watch the session unfold live — every prompt, every tool call, every file
diff — and message you while it's happening. When you're done, the link turns
into a replay.

It's one small Go binary. It started as a weekend "wouldn't it be cool if…"
and it turned out to be pretty cool, so here we are. 🙂

And nothing gets captured unless you actually typed `live`. Plain `claude` is
still plain `claude`.

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
