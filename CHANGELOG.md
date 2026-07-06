# Changelog

All notable changes to `live` are documented here. Versions follow
[SemVer](https://semver.org); each release is a git tag (`vX.Y.Z`) that the
Homebrew formula and `live version` report.

## Unreleased

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

- Experimental `LIVE_TMUX=1` — clean CLI permission prompts via tmux. Runs
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
