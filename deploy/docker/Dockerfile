# LiveShortly agent sandbox — a credential-FREE image that bundles the `live`
# CLI, the Claude Code CLI, and tmux so a viewer can run a live-streamed agent
# session in an isolated container.
#
# The image contains NO credentials. Claude + liveshortly creds are bind-mounted
# at `docker run` time (see deploy/docker/live-docker). Anyone can pull this and
# supply their own creds — nothing personal is baked into a layer.
#
#   Build:  docker build -f deploy/docker/Dockerfile -t ghcr.io/liveshortly/agent-sandbox:latest .
#   Run:    deploy/docker/live-docker claude
#
# Two stages: build the static Go `live` binary, then a slim node runtime that
# also carries the Claude Code CLI (npm).

# --- stage 1: build the linux `live` binary from source ---
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# CGO off → a self-contained binary that runs on the slim runtime with no libc surprises.
RUN CGO_ENABLED=0 go build -trimpath -o /out/live ./cmd/live

# --- stage 2: runtime ---
FROM node:22-bookworm-slim

# tmux: `live` drives the agent inside a tmux session. git + ca-certificates:
# needed by claude-code and for TLS to api.anthropic.com / liveshortly.com.
RUN apt-get update \
 && apt-get install -y --no-install-recommends tmux git ca-certificates \
 && rm -rf /var/lib/apt/lists/*

# Claude Code CLI on PATH (live finds it via exec.LookPath("claude")).
RUN npm install -g @anthropic-ai/claude-code

COPY --from=build /out/live /usr/local/bin/live
COPY deploy/docker/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

# Non-root: reuse the `node` user (uid 1000) the base image already ships.
# Its home (/home/node) is where the mounted creds land.
#
# Pre-create ~/.claude owned by node: bind-mounting the creds FILE would otherwise
# make Docker create this parent dir as root, and claude (uid 1000) could not then
# mkdir session-env/cache inside it (EACCES on SessionStart).
RUN mkdir -p /home/node/.claude && chown -R node:node /home/node/.claude
USER node
WORKDIR /home/node

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
# Default agent; override with `docker run … <image> codex`.
CMD ["claude"]
