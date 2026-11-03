# Harness support matrix

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
