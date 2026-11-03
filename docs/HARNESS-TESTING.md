# Harness testing runbook (LSH-00)

How to check every agent harness `live` supports (claude, codex in its three
modes, agy, gemini, ollama, and terminal agents like aider, cursor-agent,
hermes, qwen) against a **local** LiveShortly stack. Also how to record fixtures
so harness changes can be verified without live agent accounts.

Two layers:

1. **Golden replays** run in `go test ./...` with no accounts, network or
   server. They cover the capture → feed-event mapping for every harness. See
   `testdata/README.md`.
2. **Live runs** (this document) cover what replays can't: the server, the web
   UI, approvals, comments, the daemon and handoff. Results go in
   `docs/HARNESS-MATRIX.md`.

Ground rules:

- **Never against production.** The CLI reads `~/.liveshortly/host.json`
  before `LIVESHORTLY_API_URL` (`internal/hostcfg/hostcfg.go`), and the real
  one points at liveshortly.com. Always run with a throwaway `HOME` (§2).
- Each live run uses that agent's own account or quota. Only ollama with a
  local model is free. Ask the owner before you run the paid ones.
- Use **http://localhost:8080** (nginx, SSE unbuffered). `:3000`/`:8000`
  buffer SSE, so live events never show up there.

## 1. Bring up the local stack

From `liveshortly-server/`. Pick one option.

**A. The owner's dev stack** (`docker-compose.yml` + `docker-compose.local.yml`):

```bash
cd liveshortly-server
# The api reads Google OAuth + SESSION_SECRET from .env.auth (env_file in
# docker-compose.yml). --env-file .env.local only feeds compose interpolation
# (POSTGRES_PASSWORD). Google client redirect: http://localhost:8080/auth/google/callback
docker compose --env-file .env.local -f docker-compose.yml -f docker-compose.local.yml up -d --build
curl -fsS http://localhost:8080/health
```

**B. A throwaway stack that leaves A alone.** This uses `compose.selfhost.yml`
in a temp copy, with its own project name, network, volumes and port:

```bash
T=$(mktemp -d) && git -C liveshortly-server archive origin/main | tar -x -C "$T" && cd "$T"
cat > .env.lsh <<EOF
POSTGRES_PASSWORD=lsh-test
GOOGLE_CLIENT_ID=<owner's dev OAuth client id>
GOOGLE_CLIENT_SECRET=<owner's dev OAuth client secret>
SESSION_SECRET=$(openssl rand -hex 32)
PUBLIC_ORIGIN=http://localhost:18080
HTTP_PORT=18080
EOF
docker compose -p lsh00 --env-file .env.lsh -f compose.selfhost.yml up -d --build
# …test against http://localhost:18080 (add it to the OAuth client's redirect URIs)…
docker compose -p lsh00 -f compose.selfhost.yml down -v && rm -rf "$T"
```

`live login` is a Google device-flow sign-in, so the owner has to approve it
once per stack. Without real OAuth credentials the stack still boots, but
nobody can sign in.

## 2. Isolate the CLI

```bash
cd live && make build                  # ./bin/live from this checkout
export HOME=$(mktemp -d)               # never the real ~/.liveshortly
make use-local                         # host.json → http://localhost:8080 (LOCAL_ORIGIN=… for option B)
./bin/live login                       # owner approves the Google device prompt
./bin/live whoami
```

Logs: `$HOME/.liveshortly/live.log`. Agents still find their own logins under
the real home only if you point them there (e.g. `CODEX_HOME`,
`CLAUDE_CONFIG_DIR`). Decide per agent, and never copy credentials into the
repo or into fixtures.

## 3. Scratch repo and prompt

```bash
mkdir -p /tmp/lsh-repo && cd /tmp/lsh-repo && git init -q && printf '# lsh-repo\n' > README.md && git add . && git commit -qm init
```

Give every agent the same prompt:

> Read README.md, add a line 'hello from <agent>', then run `ls`.

It exercises a read, an edit and a shell command, and asks for permission
on the edit or shell step.

## 4. The matrix

Run each row and record ✅ / partial (reason) / ❌ in `docs/HARNESS-MATRIX.md`,
with the agent version and date. Link every real ❌/⚠ to its LSH card.

Rows: claude (baseline) · codex (tmux default) · codex `LIVE_TMUX=0` · codex
`LIVE_CODEX_APPSERVER=1` · agy · gemini · ollama (`ollama serve` + a small model,
e.g. `phi4-mini`) · aider · cursor-agent · hermes · qwen.

### Automated: steps 1, 2, 8

```bash
LIVE=$PWD/bin/live scripts/harness-matrix.sh claude     # prints one matrix row
LIVE_TMUX=0 scripts/harness-matrix.sh codex
LSH_CMD="hermes chat -q \"…\"" scripts/harness-matrix.sh hermes   # any agent without a default argv
```

The script refuses to run unless `$HOME/.liveshortly/host.json` points at
localhost. Agents run headless with their own prompts bypassed, inside the
scratch repo only.

### Manual checklist (interactive: `cd /tmp/lsh-repo && ../live/bin/live <agent>`)

Open the session page next to the terminal. For each agent:

1. **Live within 5 s**: the session appears on the web within 5 s of the
   first prompt, and its title comes from the prompt.
2. **Structured rows**: the prompt, the reply, each tool step and the README
   edit (with a diff) show as rows, not raw terminal text.
3. **Status**: "working" stays on through a slow tool (e.g. ask for
   `sleep 20`) and clears when the turn ends. No early "quiet = done".
4. **Comments**: post a viewer comment mid-turn ("also run `ls -a`"). It
   reaches the agent and changes its next step. Post two comments at once:
   both arrive word for word.
5. **Approvals**: the permission row on the web shows the full command. Allow
   runs it, Deny blocks it, and "Always allow" (where offered) stops the next
   identical ask. Fail-closed (LSH-07): when the hook can't reach the sidecar
   (e.g. `echo '{}' | LIVE_SIDECAR=127.0.0.1:1 live hook pre-tool-use --gated`),
   it must deny, never allow.
6. **Model**: the session shows the right model. Where a switch is offered,
   switching works, and agents without one reject it (LSH-01).
7. **Daemon**: run `./bin/live daemon --dir /tmp/lsh-repo`, then start the
   same agent from the web "New session". It runs and is gated unless bypass
   is on, and bypass really bypasses (codex: LSH-02). Check that no first-run
   dialog parks the pane (`tmux -L liveshortly attach -t live-<id8>`).
8. **Ending**: `/exit` or Ctrl-C gives an ended status. Web "End session"
   stops the agent. Afterwards `tmux -L liveshortly ls` and `ps` show no
   leftover pane or process.
9. **Handoff**: `live handoff <id>` prints a command for the **same** agent,
   and the digest names the files touched (LSH-06).
10. **PocketAI**: the iPhone/Mac viewer renders the same session (LSI cards).

## 5. Recording fixtures

Save each run's raw capture under `testdata/<agent>/<version>/`, using the
same file names as the existing folders. Generate the golden, read it, and
update the provenance table in `testdata/README.md`. Before committing, strip
anything private: tokens, emails, real paths outside `/tmp/lsh-repo`.

- **claude / agy hooks** (`hooks.jsonl`): register a recording hook
  next to (or instead of) `live hook`. Each hook command appends
  `jq -c '{hook:"<live event name>",payload:.}' >> /tmp/lsh-capture/hooks.jsonl`
  and prints `{}`. For claude, pass these in a `--settings` file with the
  same matchers as `internal/adapter/claude.go`. For agy, use a plugin
  `hooks.json` shaped like `internal/adapter/agy.go`. Copy the agent
  transcript the payloads point at to `transcript.jsonl` and rewrite the
  path to `$FIXTURE/transcript.jsonl`. Insert `{"comment":…}` and
  `{"verdict":…}` lines where a viewer commented or the owner answered.
- **codex rollout** (`rollout.jsonl`): after the run, copy the newest
  `~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl` (or under `$CODEX_HOME`).
- **codex app-server** (`jsonrpc.jsonl`): put a shim named `codex` first on
  `PATH` that runs the real binary and tees its stdout:
  `exec /path/to/real/codex "$@" | tee -a /tmp/lsh-capture/jsonrpc.jsonl`.
- **PTY agents** (`pty.jsonl`): `scripts/record-pty.py`, e.g.
  `scripts/record-pty.py testdata/ollama/<v>/pty.jsonl --prompt '>>> ' --input 'Say hello.\r' --input '/bye\r' -- ollama run phi4-mini`.
  It records output only, with arrival times. The replay depends on those
  times: the feed releases whole lines after 600 ms of idle (`adapter.Coalescer`).

Then:

```bash
go test ./... -run Replay -update   # writes events.golden.json
git diff testdata/                  # review: is this what a viewer should see?
go test ./...
```

## 6. Cleanup

Stop the agents, `live daemon` and the stack (option B: `down -v`). Delete
the temp `HOME` and `/tmp/lsh-repo`.
