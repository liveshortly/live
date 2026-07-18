#!/usr/bin/env bash
# Entrypoint for the agent sandbox.
#
# Credentials are NOT bind-mounted from the host home dir. The live-docker
# wrapper stages short-lived, refresh-token-stripped creds into a read-only
# /inject mount; here we copy them into the container's ephemeral tmpfs home
# ($HOME is tmpfs, so nothing here ever touches the host filesystem), then hand
# off to `live <agent>`. On exit the tmpfs — and every secret in it — is gone.
set -euo pipefail

AGENT="${1:-claude}"

if [[ ! -d /inject ]]; then
  echo "error: no /inject mount — run via deploy/docker/live-docker, not raw 'docker run'." >&2
  exit 1
fi

mkdir -p "$HOME/.liveshortly" "$HOME/.claude"

# liveshortly creds (access token only; api_url comes via LIVESHORTLY_API_URL).
if [[ ! -f /inject/live-credentials.json ]]; then
  echo "error: missing /inject/live-credentials.json" >&2
  exit 1
fi
install -m 600 /inject/live-credentials.json "$HOME/.liveshortly/credentials.json"

# Claude OAuth (access token only) + minimal account/onboarding state.
if [[ "$AGENT" == "claude" ]]; then
  if [[ ! -f /inject/claude-credentials.json ]]; then
    echo "error: missing /inject/claude-credentials.json" >&2
    exit 1
  fi
  install -m 600 /inject/claude-credentials.json "$HOME/.claude/.credentials.json"
  [[ -f /inject/claude.json ]] && install -m 600 /inject/claude.json "$HOME/.claude.json"
fi

exec live "$AGENT"
