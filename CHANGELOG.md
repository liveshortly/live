# Changelog

All notable changes to `live` are documented here. Versions follow
[SemVer](https://semver.org); each release is a git tag (`vX.Y.Z`) that the
Homebrew formula and `live version` report.

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
