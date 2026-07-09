# Changelog

All notable changes to `live` are documented here. Versions follow
[SemVer](https://semver.org); each release is a git tag (`vX.Y.Z`) that the
Homebrew formula and `live version` report.

## v1.0.0 — 2026-07-09

- **`live codex` is now interactive and real-time at the same time.** Codex runs
  in an isolated tmux session (private `-L liveshortly` socket) — you drive the
  real codex TUI as usual — and viewer comments are **typed straight into its
  composer**. This closes the long-standing gap where a comment posted from the
  web never reached an **idle** codex.

  Why it was broken: codex is a standard MCP client, so it only saw viewer
  comments when it chose to call the `poll_viewer_messages` tool — which an idle
  agent never does. Delivery was pull; it needed to be push. tmux gives a
  scriptable handle on the pane, so a comment lands the moment it arrives,
  whether codex is idle or mid-turn (mid-turn comments queue and run next).

  - Comments are delivered via a tmux **paste buffer** with bracketed paste, so
    arbitrary text (`;`, leading `-`, embedded newlines) can't be misread as tmux
    arguments or submit a turn early.
  - A **modal guard** refuses to type when codex is showing a dialog (directory
    trust, approval prompt), rather than silently answering the wrong question.
  - Permission prompts keep the existing `display-popup` + web race.
  - Feed events still come from the structured rollout tailer — the screen is
    never scraped for content.
  - Falls back to the previous PTY rollout mode when tmux is absent, and is
    skipped for headless `-p` runs. `LIVE_TMUX=0` opts out.

- **tmux is now required for the full `live codex` experience** (it was only
  "nice to have", for Claude's permission popups). Without it, codex silently
  falls back to the mode where idle comments never arrive.

- Fixed: a viewer comment injected into codex was emitted to the feed **twice** —
  once as `viewer_comment`, once as the `prompt` codex appeared to receive. The
  rollout mapper now skips viewer-injected turns.

## v0.6.1 — 2026-07-08

- **Real-time `live codex` via the app-server protocol** (opt-in,
  `LIVE_CODEX_APPSERVER=1`). Drives a headless `codex app-server` over JSON-RPC
  instead of the native-TUI sidecar, for true real-time parity with Claude:
  - **Viewer comments push instantly** as real turns (`turn/steer` mid-turn,
    `turn/start` when idle) — no polling.
  - **Permission prompts answered from the browser.** Codex's approval requests
    (command / file-change) are gated through the same web allow/deny flow as
    Claude and mapped to codex's decision.
  - Streamed items map to the existing feed rows (prompt / response / tool_call
    / file_write / stream_end).
  - Codex is headless in this mode — you watch, steer, and approve from the
    session page. The default remains the native-TUI rollout mode.
- Also shows the **prior-context digest in the terminal on fork** (from the
  interim build) — `LIVE_NO_CONTEXT=1` to suppress.

## v0.6.0 — 2026-07-08

- **`live codex` — full parity with `live claude`** (codex-cli 0.142.5). See
  `CODEX.md`.
  - **Structured capture.** New `rollout` capture mode tails codex's own
    session file (`~/.codex/sessions/**/rollout-*.jsonl`) and maps its events
    to the same feed rows Claude produces — prompts, replies, tool calls, and
    file diffs — instead of scraping raw terminal text.
  - **Handoff parity.** `live codex --handoff <code>` / `--session <id>` now
    inject the orient prompt as codex's initial prompt automatically (no more
    paste banner). Applies to any agent that takes an initial prompt.
  - **Two-way viewer comments.** The `live channel` MCP loads into codex
    per-session (ephemeral `-c mcp_servers.*` overrides, nothing persisted) and
    exposes a standard `poll_viewer_messages` tool so codex can pull viewer
    messages as real turns.
  - **Web-gated permissions** (opt-in, `LIVE_CODEX_PERMS=1`). A session-scoped
    codex `PreToolUse` hook gates tool runs through the same browser
    allow/deny flow as Claude — codex's hook payload/decision format matches
    Claude's, so the server side is reused unchanged.
- **Prior context is shown in the terminal on fork.** A fork/handoff now prints
  a compact, bounded view of the source session's digest (the most recent
  turns) so you can see what you continued from without opening the file or the
  web. `LIVE_NO_CONTEXT=1` suppresses it.
- **Fix: `--handoff` no longer exits instantly.** The viewer-channel flags
  ended on a variadic flag that swallowed the injected handoff prompt, so the
  forked agent started with no prompt and quit. The channel flags now end on a
  boolean so the prompt survives.

## v0.5.0 — 2026-07-07

- **Session handoff / fork.** Continue any session you can read — live or
  archived, yours or someone else's — as a brand-new session **you own**, with
  any agent:
  - `live claude --session <id>` forks from a session's latest state.
  - `live claude --handoff <code>` forks from a pinned handoff snapshot.
  - The original session is untouched; the server increments its fork count.
    On launch, the new agent is seeded with the prior context: the digest is
    written to `~/.liveshortly/handoffs/<id>.md` and Claude opens with a prompt
    to read it, restate the state, and continue. PTY agents (gemini/codex/…)
    get the file plus a paste-in banner. A private, unshared source prints a
    clean "you don't have access to fork this session".
  - `--pre-digest` (off by default) pre-compresses the context with a headless
    `claude -p` pass before the interactive launch.
- **`live handoff <session-id>`** prints the copyable command to continue a
  session (mints a handoff code server-side).

## v0.4.0 — 2026-07-06

- **Permission control is now ON by default** for interactive `live claude`.
  You approve tool runs from a tmux popup (when tmux is installed) or the
  session page — allow / deny / allow-always, owner only. Turn it off per run
  with `--no-permissions` / `LIVE_WEB_PERMS=0`; disable just the tmux popup
  with `LIVE_TMUX=0`. When tmux is absent, a plain `live claude` falls back to
  Claude's native terminal prompts (install tmux for the in-terminal popup),
  and headless `-p`/`--print` runs are never gated.

## v0.3.0 — 2026-07-06

Permission control — answer Claude's tool prompts from the web, or a tmux
popup. (Opt-in in v0.3.0; on by default from v0.4.0.)

- New `--web-permissions` mode (`LIVE_WEB_PERMS=1`), opt-in, owner-only:
  answer Claude's tool-permission prompts from the web instead of the
  keyboard. The PreToolUse hook blocks and long-polls the `/decision`
  endpoint; your Approve/Deny tap on the session page becomes Claude's
  permission decision, pre-empting the local prompt. Verified end to end
  (allow → tool runs, deny → tool blocked, no keyboard prompt either way).
  While on, it gates every matched tool, so the local allowlist is bypassed;
  falls back to deny if unanswered within ~8 minutes.
- `--web-permissions` gains **Allow always**: tapping ✓✓ ALLOW ALWAYS on the
  session page allows the tool now and remembers it for the rest of the
  session, so matching calls auto-allow without re-prompting — Claude Code's
  "don't ask again", owner-driven. Memory is keyed by the leading command word
  for Bash (`git`, `npm`, …), the tool for Edit/Write/MultiEdit, and the host
  for WebFetch. Verified end to end: after one "always" on `touch`, a second
  `touch` auto-ran with no further prompt; deny still blocks.

- `LIVE_TMUX=1` — clean CLI permission prompts via tmux (**install tmux for
  the best experience**). Runs
  Claude inside an isolated tmux session (dedicated socket) so a permission
  request appears in a `tmux display-popup` (`[y] allow / [a] allow always /
  [n] deny`) **without corrupting Claude's TUI** — tmux composites them in
  separate regions. Dual-source: answer from the popup keyboard OR the web,
  first responder wins (via the broker + a local `/perm` endpoint and a
  `live perm-prompt` popup subcommand). Requires tmux; falls back to web-only
  if absent. Verified e2e: both the popup-keyboard and web answers run the
  tool. Caveat: you're wrapped in tmux (Ctrl-B prefix; nested-tmux quirks),
  and it shows our allow/deny/always, not Claude's exact native menu.

## v0.2.2 — 2026-07-06

- Fix: viewer comments delivered to an idle Claude over the channel were
  echoed into the feed a second time as a raw `prompt` event, showing the
  `<channel …>…</channel>` wrapper around the message. These synthetic
  channel turns are now skipped — the clean `viewer_comment` event already
  represents them.

## v0.2.1 — 2026-07-06

- Idle-viewer channel delivery is now **on by default** for interactive
  `live claude`: a per-session channel MCP server pushes viewer messages to
  Claude even while it sits idle (previously opt-in behind `LIVE_CHANNEL=1`).
  Verified end to end — an idle Claude surfaces and answers a viewer comment.
  The first launch shows Claude Code's one-time "Loading development channels"
  confirmation (accept it once). Set `LIVE_CHANNEL=0` to force pure
  hook-boundary delivery; skipped automatically in `-p`/headless mode.

## v0.2.0 — 2026-07-05

- New `live update` command: self-updates from the release binaries served
  at `https://liveshortly.com/install/` (sha256-verified, staged + atomically
  swapped, sanity-run before replacing). `live update --version vX.Y.Z`
  installs any published version, downgrades included.
- `install.sh` accepts a version pin: `--version vX.Y.Z` flag or
  `LIVE_VERSION` env (piped form: `curl … | bash -s -- --version vX.Y.Z`).
- New `make release` / `make publish` targets: cross-compile stripped
  binaries for darwin/linux × arm64/amd64 and publish them (gz + sha256 +
  latest.txt) into the LiveShortly web app's `/install/` folder. The folder
  is cumulative — every released version stays installable.

## v0.1.1 — 2026-07-05

- `install.sh` no longer installs or modifies Go. Go 1.21+ is now a stated
  prerequisite: the script checks for it and exits with instructions
  (go.dev/dl or your package manager) when missing. Everything else —
  fetch/update source, build, PATH install, sign-in — is unchanged.

## v0.1.0 — 2026-07-05

Initial release.

- `live claude` — full semantic capture via injected lifecycle hooks
  (prompt, pre_tool, tool_call, file_write with diffs, response,
  input_requested, stream_end) plus model auto-detect.
- `live <any command>` — universal PTY capture (ANSI-stripped, batched
  `output` events) with viewer-comment overlay.
- Two-way viewer messaging: comments injected as context at prompt /
  post-tool boundaries; SSE `agent/stream` client with automatic 3s-polling
  fallback for servers that predate it.
- Browser device-flow auth (`live login` / `logout` / `whoami`) sharing
  `~/.liveshortly/credentials.json`.
- One-command installer (`install.sh`), Makefile, Homebrew tap
  (`resapce/homebrew-tap`).
