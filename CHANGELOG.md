# Changelog

All notable changes to `live` are documented here. Versions follow
[SemVer](https://semver.org); each release is a git tag (`vX.Y.Z`) that the
Homebrew formula and `live version` report.

## v2.3.2 — 2026-11-03

### Added
- **Gemini CLI runs on its hooks, not a raw terminal (LSH-04).** `live gemini`
  (Gemini CLI 0.26+) now streams prompts, replies, tool steps, file edits with
  diffs, permission pings, the model and turn ends as structured events, like
  `live claude`. Viewer comments reach gemini at tool and turn boundaries (a
  comment that arrives after the last tool re-opens the turn once). With
  permission control on, gemini runs with `--approval-mode=yolo` and live's
  BeforeTool hook is the gate: the owner approves from the web or the tmux
  popup, "always allow" is remembered, and the gate fails closed like LSH-07.
  Read-only tools (read, grep, glob, search…) aren't gated; MCP and unknown
  tools are. A web-started gemini is gated and drivable the same way.
  - Hooks ship as a `liveshortly` extension in `~/.gemini/extensions` (Gemini
    has no per-run settings flag, and it ignores a system-settings override
    outside a root-owned directory). It is inert outside live: each hook runs
    `true` unless `live` set `LIVE_BIN`. `live uninstall` removes it.
  - Live only takes over approvals when its hook is certain to fire. If hooks
    or extensions are disabled in any gemini settings scope, an admin
    extension allowlist is set, the extension is disabled, a settings file
    can't be parsed, or the run is under GitHub Actions (hook env stripped),
    gemini's own prompt stays in charge and live says why. Passing
    `--yolo`/`--approval-mode` yourself also leaves approvals to gemini.
  - Known gaps: in an untrusted folder gemini refuses yolo, so each gated
    tool is approved twice (web, then gemini's own prompt). With
    gemini's env redaction on, `LIVE_TOKEN` is stripped from hooks, so capture
    is off and gated tools are denied. Comments wait for the next boundary
    while gemini is idle (no channel like Claude's). No model switch. Gemini
    older than 0.26 keeps raw terminal capture.
  - `testdata/gemini/0.65.0/hooks.jsonl` is a golden replay built from
    gemini's documented hook payloads (synthetic, see `testdata/README.md`);
    the old PTY fixture moved to `testdata/gemini-pty/0.65.0/`.
- **One support matrix (LSH-08).** What live supports per agent (capture,
  approvals, comments, model switch, handoff, min version, golden replays,
  pocket.ai approvals, known gaps) now lives in one table,
  `internal/harness`, which the CLI also reads for its own decisions (display
  names, `/model` switch, typed comments, pocket.ai approval status).
  `go run ./scripts/harnessmatrix` writes it into the README,
  `docs/HARNESS-MATRIX.md` and `docs/harness-matrix.json` (for the web docs and
  new-session picker); `-check` and `go test ./...` fail when a copy drifts or
  a fixture folder is added without regenerating. Stale README lines (codex as
  "terminal recording", unchecked roadmap items) are gone.

### Fixed
- **codex app-server sends `initialized` (LSH-12).** `LIVE_CODEX_APPSERVER=1`
  went straight from the `initialize` response to `thread/start` without the
  `initialized` notification the protocol requires. Both app-server clients
  (`live codex` and the pocket.ai codex driver) now share one handshake:
  initialize → initialized → thread/start, covered by a test.
- **Terminal agents stream as readable rows (LSH-11).** PTY capture (ollama,
  aider, hermes, gemini < 0.26, any `live <cmd>`) now releases whole lines once
  output idles for 600 ms and holds the line still being written until its
  newline, so a slow model no longer produces one feed row per token. A line
  that keeps streaming is released at a word boundary every 45 s; operator
  input and agent exit flush everything at once. Cursor motion and erase
  sequences (`CSI D/C/G/K`) are now replayed instead of stripped, so ollama's
  grey "Send a message" placeholder no longer glues onto the typed prompt and a
  soft-wrapped line no longer repeats the word that moved down ("be be of").
- **agy 1.4.9 sessions read correctly (LSH-10).** The newer tool names
  `edit_file`, `write_file` and `read_file` now produce Edit/Write/View rows
  (with the diff for edits and writes) and approvals name the file, instead of
  "wants to run edit_file: edit_file". The old `replace_file_content` /
  `write_to_file` / `view_file` names keep working. Command output no longer
  starts with agy's `Created At… / Command output:` header (only that exact
  leading header is removed), and the prompt row appears once per user turn
  instead of on every model step.
- **codex ≥ 0.163 sessions show tool and file rows again (LSH-09).** Newer
  codex runs shell commands and patches inside a code-mode `exec` call and
  records them as `item_completed` `CommandExecution` / `FileChange` items,
  which the rollout mapper ignored, so viewers saw no commands or edits and a
  bogus `wait` row instead. Those items now become `Shell` tool rows and
  `file_write` rows with diffs; the `wait` helper is hidden, and older codex
  (0.142) no longer shows the apply_patch result as a stray output row.
- **codex app-server (`LIVE_CODEX_APPSERVER=1`) reports the model and answers
  codex in its own schema.** The model comes from the thread start. Command and
  file approvals are answered `accept`/`decline` (the legacy methods keep
  `approved`/`denied`), permission requests get a granted-permissions reply,
  `item/tool/requestUserInput` gets an empty `answers` map, MCP elicitations
  are declined, and unsupported requests get a JSON-RPC error instead of a
  `{"decision":"denied"}` codex cannot parse.

### Security
- **Approvals fail closed (LSH-07).** In web-permissions mode the agent's own
  prompt is skipped and `live hook pre-tool-use` is the only gate, yet it
  answered `allow` whenever the sidecar was unreachable, timed out, returned a
  non-200, or sent an unreadable reply, so a crashed sidecar let every gated
  tool run unreviewed (Claude, codex, agy). The gated hook is now installed as
  `live hook pre-tool-use --gated` and denies in all of those cases, telling the
  agent approvals are offline and printing the cause to stderr. Sessions where
  the owner opted into bypass (`--dangerously-skip-permissions`,
  `bypass_permissions`, `LIVE_CODEX_PERMS=0`) behave as before. Sessions already
  running keep their old hook until restarted.

## v2.3.1 — 2026-11-02

### Fixed
- **A fresh `live` reaches liveshortly.com again.** v2.3.0 defaulted to
  `http://localhost:8080`, so `live login` on a new install could not reach the
  hosted service. With no `~/.liveshortly/host.json` and no
  `LIVESHORTLY_API_URL`, the CLI now uses `https://liveshortly.com` (hosted
  first, self-host first-class — D131). Resolution order is unchanged:
  host.json, then the env var, then the hosted default; existing self-host
  configurations keep working. `make use-prod` points back at liveshortly.com.
- Installer copy no longer says no hosted account is involved; it points at
  `live login` for liveshortly.com with a one-line self-host pointer. Installer
  behaviour (downloads, checksum verification, paths) is unchanged.

## v2.3.0 — 2026-10-28

### Added
- `live daemon` offers **every agent harness installed on the machine**, not a
  fixed list of six. The builtin probe widens to the harnesses worth guessing
  at (`copilot`, `opencode`, `crush`, `goose`, `amp`, `qwen`, `pi`, `hermes`
  alongside the existing set), and anything else goes in
  `~/.liveshortly/agents.json` — a different executable name, an absolute path,
  or a fixed argv prefix:

  ```json
  [{"name": "hermes"},
   {"name": "pi", "command": "pi.dev", "args": ["chat"]}]
  ```

  Same schema the Pocket `--agents-file` adapters already used, so one file
  covers both modes, and `--agents-file` is no longer `--server`-only. An agent
  live has no native capture for runs through the generic pty path — exactly as
  `live <that harness>` already did when typed by hand.
- Web spawns can pick a **claude model alias**, and `/model` switches the model
  of a running session.

### Fixed
- `agy` and `antigravity` were probed and registered by the daemon but silently
  dropped by the server, so they never appeared in the picker.

### Security
- The daemon's agent allowlist is now the set **it** built at startup from this
  machine's PATH and the operator's own adapter file, rather than a hardcoded
  list — the same shape as the `--dir` allowlist. A spawn still carries only a
  NAME; the argv is still built locally, and a name the daemon did not register
  is refused.

## v2.2.1 — 2026-10-23

### Fixed
- Web-spawned sessions with `bypass_permissions` ended the instant they were
  created: `--no-permissions` also dropped tmux mode, so the headless child ran
  claude with no terminal and it exited immediately. Headless runs now keep the
  tmux pane regardless of permission gating, and the claude settings follow the
  resolved gating (no permission hook installed on a bypassed run).

## v2.2.0 — 2026-10-23

### Added
- Web-spawned sessions can run **unattended**: the spawn command's new
  `bypass_permissions` boolean makes the daemon start the agent with
  `live --no-permissions` plus the agent's own bypass flag
  (`claude --dangerously-skip-permissions`, `gemini --yolo`), so an
  owner-started session raises no permission probes. The boolean is mapped to
  these fixed flags in the daemon — nothing from the wire reaches the argv.

- Document self-hosted Pocket daemon and browser-platform setup separately.
- Install and update from GitHub Releases without a website mirror or automatic login.
- Default new CLI configurations to the local single-origin web stack; preserve explicit host settings.
- Remove the hosted website mirror/deployment from CLI releases.

## v2.1.3 — 2026-10-22

- **Fix: web-spawned / headless sessions ended instantly with no agent attached.**
  `tmuxPaneStatus` parsed `tmux list-panes -F "#{pane_dead} #{pane_exit_status}"`
  assuming two fields, but a live pane reports an empty `#{pane_exit_status}`
  (one field, `"0 "`). That was treated as an error, so `attachOrWait` tore the
  session down the moment it started. Only the headless path (`live daemon`
  web-started sessions) was affected — interactive runs use `tmux attach-session`
  and never hit this. Parse `pane_dead` first; read the exit code only once the
  pane has actually exited.

## v2.1.2 — 2026-08-17

- **`live agy` single permission gate parity with Claude & ephemeral plugin scoping**:
  - Automatically pass `--dangerously-skip-permissions` to AGY when LiveShortly's web
    permissions are ON so the Web UI / popup is the single source of truth (no double CLI prompt).
  - Auto-detect `--dangerously-skip-permissions` on the CLI to disable LiveShortly's hook broker.
  - Make `live hook` always output safe `allow` decisions when `LIVE_SIDECAR` is unset,
    preventing non-live AGY sessions from ever being blocked.
  - Scope AGY plugin lifecycle to the session by automatically running `agy plugin uninstall`
    on session cleanup.

## v2.1.1 — 2026-08-17

- **Fixed: input/comments from the web were not delivered into `live agy`.**
  - Wire `sc.OnComment` in tmux mode to paste and submit viewer comments directly into
    AGY's composer when idle, prompting AGY to process them immediately.
  - Automatically queue comments for `Stop` hook continuation (`decision: "continue"`)
    and `PreInvocation` injection if AGY is mid-turn.
  - Remove standard MCP tool siphoning that previously emptied the hook queue for AGY.

## v2.1.0 — 2026-08-17

- **`live agy` — full parity with `live claude` for Google Antigravity (AGY)**:
  - Deep lifecycle hook capture for Antigravity CLI (`agy` / `antigravity`) via
    an ephemeral managed plugin (`PreInvocation`, `PreToolUse`, `PostToolUse`, `Stop`).
  - Structured live feed: captures prompts, replies, tool executions (`run_command`),
    file diffs (`write_to_file`, `replace_file_content`), and web fetching.
  - Permission control: answer AGY tool prompts from the web or in-terminal tmux popups
    (`allow`, `deny`, `always-allow`).
  - Viewer comments: delivered into running AGY sessions via the `liveshortly-channel`
    MCP server and hook-boundary injection (`injectSteps` / `Stop` continuation).
  - Session forks: continue any session with `live agy --session <id>` or `live agy --handoff <code>`.
  - Web daemon: `live daemon` can now spawn `agy` sessions directly from the web.

## v2.0.2 — 2026-08-14

CLI audit follow-up: correctness fixes across join, exit codes, session lookup,
and UTF-8 handling.

- **Fixed: `live version` lied on `go run` builds.** The fallback was stuck at
  `1.0.1` while releases were on `2.x`. It now reads the checked-in `VERSION`
  file when not set via `-ldflags`.
- **Fixed: tmux and app-server modes always exited 0.** Agent failures were
  invisible to shells and CI. Tmux paths now propagate the pane exit status;
  app-server mode returns the codex process exit code (130 on Ctrl-C).
- **Fixed: `live join` called `os.Exit` when a session ended.** That skipped
  terminal cleanup and raced with raw-mode input. Session end now unblocks the
  main loop cleanly.
- **Fixed: live join stream dedup used hidden events.** `baseSeq` came from the
  last raw event (including `stream_start`), which could duplicate or skip live
  events. It now tracks the max seq of visible events only.
- **Fixed: `live ls -n 0` fetched every session.** `-n` now requires a positive
  count; use `--all` for the full list.
- **Fixed: short session handles missed on large accounts.** `resolveID` only
  scanned the first 100 sessions; it now paginates through all of them.
- **Fixed: invalid session URLs were forwarded to the API.** Pasted links now
  validate the UUID after `/session/`.
- **Fixed: UTF-8 truncation in viewer comments, PTY output, codex instructions,
  and rollout clips.** Byte cuts could split multi-byte runes; all are now
  rune-safe.
- **Fixed: desktop notifications always opened liveshortly.com.** They now use
  the configured `WebURL` from host.json.
- **Fixed: sidecar shutdown race.** The hook HTTP server could still POST while
  the event queue was closed; HTTP now shuts down before the queue.
- **Fixed: app-server JSON-RPC hang on write failure.** A failed write left a
  pending entry that blocked forever; pending entries are now cleaned up.

## v2.0.1 — 2026-08-11

Follow-up to v2.0.0: a live "is working" indicator, and a codex capture bug bad
enough to justify the release on its own.

- **Fixed: `live codex` captured nothing unless you typed within 30 seconds.**
  The rollout watcher waited 30s from launch for codex to open its session file
  and then gave up for good — but codex writes that file when you send your
  FIRST PROMPT, not when it starts. Any session where the first message came
  later was recorded as an empty feed, which is most of them, and certain
  whenever codex opens with an update prompt. The timer bought nothing: the
  goroutine already exits with the session's context, which is the correct
  bound. It now waits as long as the session lives, polling briskly at first and
  easing off once it is clearly waiting on a human. Verified by idling 45s past
  launch — the window that used to kill capture — and getting a full transcript.
- **The prompt says when the agent is working.** A spinner and "<agent> is
  working…" sit in the status chip for as long as the turn runs, so a watcher
  can tell a thinking agent from a hung one. The rule differs by capture because
  the captures differ: a hooks agent brackets a turn (`prompt` … `response`) and
  emits *nothing* in between, so an opening event holds the indicator until the
  turn closes — the web's silence-based rule would drop it a few seconds into
  every turn, which is precisely the stretch worth reporting. A pty agent has no
  prompt/response at all, only `output` and a `stream_start` heartbeat, so
  silence is the only signal and the window is set wider than the flush interval
  to stop it strobing between the batches of one reply. Taking the later of the
  two deadlines covers both without branching, and an open turn is capped so a
  dead agent stops claiming to work. `live watch` carries the same indicator on
  a status line, without taking the keyboard.
- **The indicator names what is actually running**, most specific first: the
  model when the session has reported one, else the agent that was launched —
  `gpt-5.5`, `claude-opus-5`, `codex`, `gemma4`. It used to say "Claude", which
  is a brand rather than the thing doing the work, and would have been an
  outright lie beside a codex or ollama session. The transcript's own speaker
  label now resolves through the same function; the two disagreed before, one
  saying "Claude" while the turn above it said "claude-opus-5".
- **Fixed: the status chip rendered faint.** The prompt wrapped the whole status
  in `\033[2m`, so dim was still in effect when a colour inside it began and the
  terminal drew dim green — applied to the two things on that line most needing
  to be seen, the working indicator and the BLOCKED permission chip. The status
  owns its own colours now, and the indicator is bright rather than normal green:
  a terminal's normal green is a body-text colour, not a signal.
- **A message you send lights the indicator on keypress**, not when the round
  trip finishes. Sending is itself a network call, so the terminal used to admit
  nothing was happening for two round trips. The optimistic window is short and
  expires by itself, so a message queued against an idle agent does not leave a
  lying indicator behind.

## v2.0.0 — 2026-08-11

The terminal stops being write-only. Until now `live` could *start* a session
and nothing else: to see what you had running, who it was shared with, or what
someone else's agent was doing, you opened the website. This release moves that
whole surface into the CLI, and adds the thing the website could never do —
letting two developers hold a conversation through one agent, terminal to
terminal.

- **`live ls` / `live shared` — your sessions, in the terminal.** One line each —
  status, title, handle, link, agent — and the ten most recent by default, since
  a listing is a glance rather than an archive; `--all` pages through the rest
  and `-n <limit>` picks a count. Filters: `--mine` / `--shared`, `--live` /
  `--ended`, `-q <text>`, and `--json` for piping. `live shared` is
  `live ls --shared`. The link is never truncated to make a row fit: the title
  column gives way first, and the agent column is dropped before the title stops
  naming anything.
- **`live join <id>` — attach a second terminal to a running session.** It
  prints recent history for context, streams the session as it happens, and
  sends anything you type to the running agent as a viewer message — the same
  path the web composer uses, so it reaches the agent mid-task. The person who
  owns the session sees your message arrive in their agent; you see the reply
  stream back. `live watch <id>` is the read-only half. `/info`, `/url`,
  `/fork`, `/stop` and `/quit` work while joined.
- **The joined view reads like the web viewer, not like a log.** Every turn is
  a block that names who produced it — the owner (marked *you* when it is your
  own session), the model that replied, or the viewer who spoke — with its text
  wrapped and shown in full instead of clipped to one line. Work steps render
  as compact rows: the tool and its target, captured output under a `⎿`, and
  file writes with a coloured diff and its `+`/`−` counts. Permission and
  input waits surface as their own notice, a command flagged dangerous is
  marked, and long paths keep the tail that identifies the file.
- **The input line holds its ground.** `live join` draws its own prompt in raw
  mode — carrying the session's uptime and live watcher count — and prints
  arriving events *above* it, so an event landing mid-sentence no longer
  splices itself into what you are typing. `↑`/`↓` recall what you sent,
  `Ctrl-U` clears, `Ctrl-W` deletes a word, `Ctrl-A`/`Ctrl-E` and the arrows
  move the cursor. A non-terminal stdin falls back to plain line reads, so
  `echo "ship it" | live join <id>` still works, and the join exits by itself
  when the session ends.
- **A joined terminal can answer the agent's permission prompts.** A session
  used to stall whenever its owner walked away from the tool prompt. Now the
  request surfaces in the joined view with `/allow`, `/deny` and `/always`, the
  prompt turns into a `BLOCKED` chip until it is answered, and joining an
  already-stuck session says so on arrival. It posts to the same `/decision`
  endpoint the web viewer uses and carries the same comment-level
  authorization, so the running shim — which polls for a verdict every second —
  resumes just as it would from the browser, and the decision is emitted as an
  event so every watcher sees who answered.
- **`live share <id> <email>` — grant access without leaving the shell.**
  Defaults to `commenter` (the role a CLI joiner needs to talk back);
  `--role viewer` is watch-only, and `--list` shows who has access.
- **`live info <id>`** prints one session in full — lineage, machine, repo and
  branch, whether the agent stream is currently attached — plus its last events.
- **`live stop`, `live open`, `live hosts`** end a running session from
  anywhere, open its page, and list the machines currently running
  `live daemon`.
- Every command that takes an `<id>` accepts the eight-character handle the
  listings print, a full session id, or a pasted session link.

No server change was needed for any of it: the listing, streaming, comment and
share endpoints already existed for the web client.

## v2.3.2 — 2026-11-03

### Fixed
- **A fresh `live` reaches liveshortly.com again.** v2.3.0 defaulted to
  `http://localhost:8080`, so `live login` on a new install could not reach the
  hosted service. With no `~/.liveshortly/host.json` and no
  `LIVESHORTLY_API_URL`, the CLI now uses `https://liveshortly.com` (hosted
  first, self-host first-class — D131). Resolution order is unchanged:
  host.json, then the env var, then the hosted default; existing self-host
  configurations keep working. `make use-prod` points back at liveshortly.com.
- Installer copy no longer says no hosted account is involved; it points at
  `live login` for liveshortly.com with a one-line self-host pointer. Installer
  behaviour (downloads, checksum verification, paths) is unchanged.

- **Fixed: a session whose capture process died no longer sits "live" forever.**
  A spawned `live --attach` child runs in its own process group so a session
  outlives the daemon that started it. The flip side went unhandled: kill that
  child — reboot, `kill -9`, OOM — and its tmux pane survives with no sidecar
  behind it. The hook relay fails silently by design (`live hook` exits 0 on a
  connection error, so a dead sidecar can never wedge the agent), so nothing is
  captured and nobody ever calls stop. `live daemon` now sweeps at startup and
  every 5 minutes: for each `live-*` pane on its tmux socket it probes the
  sidecar address recorded in that pane's environment, and archives the session
  when nothing answers. The pane is deliberately left running — that socket also
  carries `live claude` runs you started by hand, and killing an agent mid-task
  to tidy a feed is not a trade the daemon gets to make for you; it stays
  attachable, it just stops pretending to be captured.

## v1.3.1 — 2026-07-29

- **Fixed: `~/.liveshortly/host.json` wins over `LIVESHORTLY_API_URL` again.**
  v1.3.0 inverted this. `LIVESHORTLY_API_URL` is routinely left exported in a
  shell profile from earlier debugging, so a stale
  `export LIVESHORTLY_API_URL=http://localhost:8000` silently redirected every
  `live` command away from the production host the user had configured with
  `make use-prod` — sessions, login, and token refresh alike. host.json is the
  explicit, tool-managed switch and is authoritative; the env var remains the
  way to point at a stack when no host.json exists.
- **Fixed: a token refresh against the wrong host no longer deletes your
  credentials.** `Refresh` treated any 401 as "this token was revoked" and
  removed `credentials.json`. Refreshing against a host that never issued the
  token also 401s, so the combination above could destroy a perfectly good
  production login. A 401 now only deletes when it comes from the issuing host;
  otherwise it reports the mismatch and leaves the file alone.

## v1.3.0 — 2026-07-29

- **Start a session from the browser: `live daemon`.** Running `live <agent>` in
  a terminal was the only way to open a session, so every session cost a context
  switch to a shell. `live daemon` registers this machine with the server, holds
  a command stream open, and starts the ordinary capture path when the web asks
  for a session — same hooks, same permission gating, same feed. Take one over
  locally at any time with `tmux -L liveshortly attach -t live-<id8>`.
  A spawn command is remote code execution on your machine, so the daemon trusts
  the server for nothing: the server sends an agent NAME, never a command line,
  and the daemon re-checks it against its own allowlist and the directory
  against the list passed to `--dir`. Nothing reaches a shell. Needs a server
  with the hosts API.
- **Fixed: `curl … | bash` now really does install onto your PATH and sign you
  in.** Both interactivity checks tested `[ -t 0 ]`, but under the documented
  one-liner stdin *is* the script pipe — so for everyone who followed the README
  it was false, and the installer quietly skipped `/usr/local/bin` (landing in
  `~/.local/bin`, off PATH) and skipped sign-in. It now tests the controlling
  terminal, which also keeps CI and cron from hanging on a device flow nobody
  can approve.
- **Fixed: a viewer comment was answered twice.** Every comment was pushed to
  the channel *and* unconditionally queued for hook-boundary injection, on the
  reasoning that a buffered send is not proof the agent consumed it. Nothing
  ever acked, so with a healthy channel the duplicate was not rare — it was
  every message. Comments now route to the channel first and fall back to hooks
  only when no subscriber took them; delivery stays at-least-once, acked per
  hop.
- **Fixed: the second session on a long-lived tmux server inherited the first
  one's environment.** tmux seeds a new session from the *server's* environment
  for anything outside `update-environment`, which lists neither `HOME` nor
  `PATH`. The agent could therefore read a different `HOME`, find no config and
  restart its first-run wizard, while hooks posted to whatever API URL the tmux
  server was born with. The load-bearing variables are now pinned per session.
- **Fixed: `LIVESHORTLY_API_URL` now wins over `~/.liveshortly/host.json`.**
  With the file taking precedence, pointing `live` at a local stack meant
  overriding `HOME` — which relocates `~/.claude` and caused exactly the
  first-run wizard above.
- The "Loading development channels" confirmation fires on *every* launch, not
  once as the message claimed. A web-started session answers it, since no
  operator is there to.

## v1.2.0 — 2026-07-10

- **Storage-quota aware.** When the server auto-ends a session because its owner
  hit their storage quota, `live` now reacts to the crossing signal on the
  event-emit response: it stops capturing and prints where to pick up — the
  downloadable session summary and the `live claude --handoff <code>` command to
  continue after freeing space.
- **Capture is lossless up to 256 KB per event.** Prompt, assistant-reply and
  tool-output caps were raised to match the server's per-event belt cap;
  truncation is now purely a display concern handled when fanning out to
  viewers, so the stored feed (which the storage quota meters) is complete.

## v1.1.0 — 2026-07-09

- **Forking a session into `codex` now carries the prior context.** codex has no
  `--append-system-prompt-file`, so a fork printed the digest and then tmux
  repainted over it. The digest is now passed as `-c developer_instructions=…`,
  which orients the agent without submitting an opening turn. Clipped to 64 KiB
  so a long fork's launch command stays under `ARG_MAX`; the full text always
  remains at the digest path.
- Fixed: a trailing `-c key=value` was mistaken for the opening prompt and
  submitted to the model. `live` injects those itself, so the scan that looks
  backwards for the positional prompt now skips `-c`/`--config` values.
- **Releases are cut by CI from a push to `main`,** gated by the new `VERSION`
  file: a push releases only if the tag it names does not exist yet. One run
  tags the repo, publishes the binaries to `live-dist`, mirrors them (and the
  installer) to `liveshortly.com`, and bumps the Homebrew formula.
- The Homebrew formula installs the prebuilt binary instead of compiling from
  source, so `brew install` no longer needs Go or access to the private repo.
- Everything moved to the `liveshortly` GitHub org. The Go module path is now
  `github.com/liveshortly/live`. Old URLs redirect, so nothing you have pinned
  breaks.

## v1.0.1 — 2026-07-09

- **`live` now installs as a prebuilt binary.** `curl -fsSL https://liveshortly.com/i.sh | bash`
  downloads a signed-off, checksum-verified static binary for your platform and
  puts it on your PATH. No Go toolchain, no source checkout, no build step, and
  no repo access required. Installs went from ~30 s to ~2 s.
- Release artifacts are published on every `v*` tag to the public
  [resapce/live-dist](https://github.com/resapce/live-dist/releases) repo by CI,
  and mirrored at `https://liveshortly.com/install/`. The installer prefers
  GitHub and falls back to the mirror; `live update` is unchanged.
- The installer verifies the SHA-256 **and** runs the staged binary to confirm it
  reports the expected version before replacing anything. The final move is an
  atomic rename, so a failed install can never leave a partial binary on PATH,
  and a running `live` is never truncated under itself.
- The whole script is wrapped in `main()`, invoked on the last line: a truncated
  download now runs nothing at all instead of half an installer.
- Rerunning the installer upgrades `live` in place wherever it already lives,
  rather than dropping a second copy that shadows the first on PATH.
- New installer flags: `--bin-dir`, `--no-login`, `--no-modify-path`,
  `--from-source`. Non-interactive installs (piped, no TTY) no longer hang on the
  device-flow sign-in — they print `live login` and exit cleanly.
- `install.sh` still works and is byte-identical to `i.sh`.

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
