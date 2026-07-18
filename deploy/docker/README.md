# Agent sandbox (Docker)

Run a live-streamed Claude Code session in an isolated container. The image is
**credential-free** — your Claude and liveshortly creds are mounted at run time,
never baked in. Anyone can pull the image and use their own login.

## Files
- `Dockerfile` — two-stage build: static `live` binary + node runtime with the
  Claude Code CLI and tmux.
- `entrypoint.sh` — verifies the cred mounts, then runs `live <agent>`.
- `live-docker` — host wrapper: exports Claude creds from the macOS Keychain and
  runs the container with a minimal permission set.

## Build
```bash
# from the repo root
docker build -f deploy/docker/Dockerfile -t ghcr.io/liveshortly/agent-sandbox:latest .
```

## Run
```bash
install -m 755 deploy/docker/live-docker /usr/local/bin/live-docker   # once
live-docker            # claude (default)
live-docker codex      # once the image bundles codex
```
Requires `live login` already done on the host (creates `~/.liveshortly`) and
Claude Code signed in on this Mac (Keychain entry `Claude Code-credentials`).

## Push to GHCR
```bash
echo "$GHCR_PAT" | docker login ghcr.io -u <github-user> --password-stdin
docker push ghcr.io/liveshortly/agent-sandbox:latest
```
The published image carries no secrets; sharing it just shares the tooling.

## Security model
- **No creds in the image.** Verify: `docker history --no-trunc ghcr.io/liveshortly/agent-sandbox:latest` shows no cred COPY.
- Runtime: `--cap-drop ALL`, `--security-opt no-new-privileges`, no docker
  socket, no host network, `--pids-limit`/`--memory`/`--cpus` caps, non-root uid 1000.
- Claude creds mounted read-only; `~/.liveshortly` mounted read-write so `live`
  can refresh its access token.

## Known ceilings (ponytail)
- **macOS only** — Keychain export uses `security`. On Linux the Claude creds are
  already a file (`~/.claude/.credentials.json`); mount it directly, skip the export.
- **No `--read-only` rootfs** — agents write under `$HOME`/`/tmp`. Add tmpfs maps
  to enable it when hardening.
- **No egress firewall** — container can reach any host. Restrict to
  `api.anthropic.com` + `liveshortly.com` with a network policy if untrusted.
- **claude-only** — codex needs its CLI added to the image and a creds path.
