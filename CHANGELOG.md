# Changelog

All notable changes to `live` are documented here. Versions follow
[SemVer](https://semver.org); each release is a git tag (`vX.Y.Z`) that the
Homebrew formula and `live version` report.

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
