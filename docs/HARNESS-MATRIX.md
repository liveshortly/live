# Harness support matrix

## What live supports (generated)

The single support table, also in the README and published as
`docs/harness-matrix.json` for the web docs and the new-session picker. Edit
`internal/harness/harness.go`, then run `go run ./scripts/harnessmatrix`
(`-check` and `go test ./internal/harness` fail when this copy drifts). Adding
a `testdata/<agent>/<version>` fixture changes the "Golden replays" column, so
regenerate after recording one.

<!-- harness-matrix:start -->
| Agent | Capture | Approvals | Viewer comments | First message wakes idle agent | Model choice at start | Model switch | Handoff | Min version | Golden replays | pocket.ai approvals | Known gaps |
|---|---|---|---|---|---|---|---|---|---|---|---|
| `claude` | ✅ Claude Code hooks | ✅ gated from the web or tmux popup; fails closed | ✅ injected (channel, hook boundaries, Stop) | ✅ channel push; held until the channel connects | ✅ `--model` (opus, sonnet, haiku, your default) | ✅ `/model` from the session page | ✅ seed prompt injected | — | claude 2.1.316 | supported | — |
| `codex` | ✅ rollout file, codex in tmux | ✅ gated (PreToolUse hook); fails closed. Off with `--no-permissions`, `--yolo`, `-a never` | ✅ typed into codex's composer | ✅ typed into the composer once it is drawn (waits up to 20 s; session notice if it never is) | ✅ `--model` (codex's model list) | ❌ | ✅ seed prompt injected | — | codex 0.142.5, codex 0.166.0 | supported | no mid-session model switch |
| `codex` (`LIVE_TMUX=0`) | ✅ rollout file, codex in a PTY | ✅ gated (PreToolUse hook); fails closed | ⚠ codex pulls them via the `poll_viewer_messages` channel tool (`LIVE_CHANNEL=0` turns it off) | ❌ waits for codex to call `poll_viewer_messages`; an idle codex never does | ✅ `--model` (codex's model list) | ❌ | ✅ seed prompt injected | — | codex 0.142.5, codex 0.166.0 | supported | comments wait until codex calls the channel tool; no model switch |
| `codex` (`LIVE_CODEX_APPSERVER=1`) | ✅ app-server JSON-RPC (headless) | ✅ answered from the web; deny on timeout | ✅ steer the running turn, or start a new one | ✅ starts a turn (held on the server until the thread is open) | ✅ `model` on thread/start | ❌ | ✅ developer instructions | — | codex-appserver 0.166.0 | supported | file approvals say only "apply a file change"; no terminal UI; codex questions get no answers; no model switch |
| `agy`, `antigravity` | ✅ hooks (`liveshortly` plugin) | ✅ gated; fails closed; live exits if the plugin can't install | ✅ every comment in full, at the next model step | ✅ typed in once the composer is ready (waits up to 20 s, confirms, retries once; notice names trust/sign-in) | ✅ `--model` (`agy models`) | ❌ | ⚠ prior context shown in the pane and saved as a digest; agy has no system-prompt flag and `-i` would start a turn, so it isn't given to agy | — | agy 1.4.9 | supported | reply arrives when the turn ends; no model switch |
| `gemini` | ✅ hooks (`liveshortly` extension) | ⚠ gated, fails closed — but gemini's own prompt stays in charge when hooks may not fire (hooks/extensions disabled, allowlist, GitHub Actions, your own `--yolo`/`--approval-mode`) | ⚠ at tool and turn boundaries only | ❌ queued for the next hook; an idle gemini fires none until someone types in the terminal | ✅ `--model` (shipped list + your default) | ❌ | ⚠ paste the printed seed prompt | 0.26 | gemini 0.65.0 | unsupported | untrusted folder: approve twice; env redaction strips `LIVE_TOKEN` (capture off, gated tools denied); comments wait while idle; no model switch |
| `gemini` (gemini < 0.26) | ⚠ terminal recording | ❌ gemini's own prompt, in the terminal only | ⚠ shown to the operator only | ❌ shown to the operator only | ✅ `--model` (shipped list + your default) | ❌ | ⚠ paste the printed seed prompt | — | gemini-pty 0.65.0 | unsupported | no structure; upgrade to 0.26+ |
| `ollama` | ⚠ terminal recording, whole lines | — no tools | ✅ typed in as one line | ✅ typed in as one line; an early one waits in the terminal's input buffer | ✅ `ollama run <model>` (pulled models; required) | ❌ | ⚠ paste the printed seed prompt | — | ollama 0.30.10 | not-needed | no prompt/tool rows, no stream_end, no model |
| `any ACP agent` (`LIVE_ACP=1` or `"acp": true` in agents.json) | ✅ Agent Client Protocol over stdio (headless) | ✅ answered from the web; fails closed | ✅ sent as the next turn (queued while one runs) | ✅ sent as the first turn (held on the server until the ACP session is open) | ⚠ only via `models` + `model_flag` in agents.json | ❌ | ✅ digest prepended to the first turn | ACP protocol v1 | acp 1 | unsupported | no terminal UI; comments wait for the running turn; no model switch |
| `aider`, `cursor-agent`, `hermes`, `copilot`, `opencode`, `crush`, `goose`, `amp`, `qwen`, `pi` and any other command | ⚠ terminal recording (raw lines, no structure) | ❌ the agent's own prompt, in the terminal only | ⚠ shown to the operator only (opt in to typing with `type_comments` in agents.json or `LIVE_TYPE_COMMENTS=1`) | ❌ shown to the operator only; ⚠ with `type_comments` it is typed in, ready or not | ⚠ `cursor-agent` (`--model`), or any agent given `models` + `model_flag` in agents.json | ❌ | ⚠ paste the printed seed prompt | — | aider 0.82.3, cursor-agent 2026.10.01, hermes 0.21.5 | unsupported | no prompt/reply/tool rows, no stream_end, no model. Speaks ACP? Run it in ACP mode (row above) |

✅ works · ⚠ partial (reason given) · ❌ not supported. Generated from `internal/harness` by `go run ./scripts/harnessmatrix`; do not edit by hand.
<!-- harness-matrix:end -->

Steps 1–10 are defined in `docs/HARNESS-TESTING.md` §4.

## Live runs (local stack)

Fill this in from real runs: `scripts/harness-matrix.sh` covers steps 1, 2 and
8, and the checklist covers the rest. Record the agent version and date. A
blank cell means not run yet.

| Agent | Version | Date | 1 live+title | 2 structured | 3 status | 4 comments | 5 approvals | 6 model | 7 daemon | 8 ending | 9 handoff | 10 PocketAI |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| claude | 2.1.316 | | | | | | | | | | | |
| codex (tmux) | 0.166.0 | | | | | | | | | | | |
| codex `LIVE_TMUX=0` | 0.166.0 | | | | | | | | | | | |
| codex app-server | 0.166.0 | | | | | | | | | | | |
| agy | 1.4.9 | | | | | | | | | | | |
| gemini | 0.65.0 | | | | | | | | | | | |
| ollama | 0.30.10 | | | | | | | | | | | |
| aider | 0.82.3 | | | | | | | | | | | |
| cursor-agent | 2026.10.01 | | | | | | | | | | | |
| hermes | 0.21.5 | | | | | | | | | | | |
| qwen | — | | | | | | | | | | | |

Not run yet (2026-11-03): every live run needs the owner's Google device
approval for `live login` on the local stack, and every agent except ollama
spends the owner's quota.

## From the golden replays (`go test ./...`, no accounts)

What the capture → feed mapping produces today, per `testdata/`. "synthetic"
fixtures are hand-built from the sources listed in `testdata/README.md`, so
re-check them with a real capture.

| Agent (fixture) | Prompt row | Reply row | Tool rows | File edit + diff | Approval ask text | Comments injected | stream_end | Model | Card |
|---|---|---|---|---|---|---|---|---|---|
| claude 2.1.316 (synthetic) | ✅ | ✅ | ✅ | ✅ | ✅ full command | ✅ text, at hook boundary and Stop | ✅ | ✅ from transcript | baseline |
| agy 1.4.9 (synthetic) | ⚠ repeated on every PreInvocation | ⚠ only at Stop | ⚠ `read_file`/`edit_file` generic; `run_command` output keeps the `Created At…` header | ❌ for `edit_file`/`write_file` (✅ only for old `replace_file_content`) | ⚠ "Antigravity wants to run edit_file: edit_file" | ✅ full text of every comment | ✅ | ✅ | LSH-03 |
| codex 0.142.5 rollout (synthetic) | ✅ | ✅ | ✅ "Shell" | ✅ | (hook, as claude) | (channel / tmux) | ✅ | ✅ | LSH-09 ✅ |
| codex 0.166.0 rollout (synthetic, real shapes) | ✅ | ✅ | ✅ "Shell" from `item_completed CommandExecution` | ✅ from `FileChange` | (hook, as claude) | (channel / tmux) | ✅ | ✅ | LSH-09 ✅ |
| codex 0.166.0 app-server (synthetic, schema-checked) | ✅ | ✅ | ✅ "Shell" | ✅ | ⚠ "apply a file change" | — | ✅ | ✅ from `thread/started` / `thread/start` | LSH-09 ✅ |
| ollama 0.30.10 (**real**) | ❌ (raw output) | ✅ whole lines (a very slow model: a row per ~45 s, cut at a word) | ❌ | ❌ | ❌ | typed line (ollama only) | ❌ | ❌ | LSH-05, LSH-11 ✅ |
| gemini 0.65.0 hooks (synthetic) | ✅ | ✅ | ✅ shell as Bash; others by gemini name | ✅ `replace`/`write_file` | ✅ full command | ✅ text, at tool boundary and AfterAgent | ✅ | ✅ from BeforeModel | LSH-04 |
| gemini < 0.26 PTY (`gemini-pty`, synthetic) | ❌ raw TUI | ❌ raw TUI | ❌ | ❌ | ❌ | ❌ overlay | ❌ | ❌ | LSH-04 |
| aider / cursor-agent / hermes (synthetic) | ❌ raw | ❌ raw | ❌ | ❌ | ❌ | ❌ overlay | ❌ | ❌ | LSH-05 |
