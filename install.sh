#!/usr/bin/env bash
# live — one-command installer.
#
#     curl -fsSL https://liveshortly.com/install.sh | bash
#
# (Cloning the private source repo still needs your GitHub SSH key or gh
# auth. Equivalent while you have a checkout: bash install.sh)
#
# Does the setup end to end: fetches/updates the source, builds, installs the
# binary onto your PATH, and signs you in. Idempotent — rerun it any time to
# update. It never touches your Go installation — Go 1.21+ is a prerequisite
# (https://go.dev/dl/) and the script stops with instructions if it's missing.

# Piped through plain `sh` on some systems? Everything below assumes bash.
if [ -z "${BASH_VERSION:-}" ]; then
  echo "[live-install] please pipe to bash:  curl -fsSL https://liveshortly.com/install.sh | bash" >&2
  exit 1
fi
set -euo pipefail

# Optional version pin: `bash install.sh --version v0.1.1`, or when piped:
#   curl -fsSL https://liveshortly.com/install.sh | bash -s -- --version v0.1.1
# (env LIVE_VERSION=v0.1.1 works too). Default: latest main.
REQ_VERSION="${LIVE_VERSION:-}"
while [ $# -gt 0 ]; do
  case "$1" in
    --version) REQ_VERSION="${2:-}"; shift 2 ;;
    *) echo "[live-install] unknown flag: $1" >&2; exit 2 ;;
  esac
done
[ -n "$REQ_VERSION" ] && REQ_VERSION="v${REQ_VERSION#v}"

REPO_SSH="git@github.com:resapce/live.git"
REPO_HTTPS="https://github.com/resapce/live.git"
SRC_DIR="${LIVE_SRC_DIR:-$HOME/.liveshortly/src/live}"
BIN_DIR="${LIVE_BIN_DIR:-}"

say()  { printf '\033[32m[live-install]\033[0m %s\n' "$*"; }
fail() { printf '\033[31m[live-install]\033[0m %s\n' "$*" >&2; exit 1; }

command -v git >/dev/null 2>&1 || fail "git is required — install it and rerun."

# ── 1. Fetch or update the source ─────────────────────────────────────────────
# If this script is being run from inside a checkout, build that checkout.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd || true)"
if [ -n "$SCRIPT_DIR" ] && [ -f "$SCRIPT_DIR/go.mod" ] && grep -q '^module github.com/resapce/live$' "$SCRIPT_DIR/go.mod" 2>/dev/null; then
  SRC_DIR="$SCRIPT_DIR"
  say "using existing checkout: $SRC_DIR"
  if [ -n "$REQ_VERSION" ]; then
    fail "--version only works on the managed copy (it moves HEAD). In your own checkout run: git checkout $REQ_VERSION && bash install.sh"
  fi
elif [ -d "$SRC_DIR/.git" ]; then
  say "updating $SRC_DIR"
  git -C "$SRC_DIR" fetch --tags --quiet 2>/dev/null || true
  if [ -z "$REQ_VERSION" ]; then
    # A previous pinned install may have left the clone detached at a tag.
    git -C "$SRC_DIR" checkout --quiet main 2>/dev/null || true
    git -C "$SRC_DIR" pull --ff-only --quiet || say "warning: could not fast-forward; building what's there"
  fi
else
  mkdir -p "$(dirname "$SRC_DIR")"
  say "cloning resapce/live → $SRC_DIR"
  git clone --quiet "$REPO_SSH" "$SRC_DIR" 2>/dev/null \
    || git clone --quiet "$REPO_HTTPS" "$SRC_DIR" \
    || fail "clone failed — check your GitHub SSH key or credentials for resapce/live."
fi

# ── 2. Check for a Go toolchain (never installed by this script) ─────────────
# Any Go >= 1.21 auto-downloads the exact toolchain go.mod asks for
# (GOTOOLCHAIN=auto), so whatever Go you already have is sufficient.
if ! command -v go >/dev/null 2>&1; then
  fail "Go is required but not installed. Install it first (https://go.dev/dl/,
              or 'brew install go' / your package manager), then rerun this script."
fi

# ── 3. Pin the requested version, then build ─────────────────────────────────
if [ -n "$REQ_VERSION" ]; then
  git -C "$SRC_DIR" checkout --quiet "$REQ_VERSION" \
    || fail "version $REQ_VERSION not found — see https://liveshortly.com/install/ for published versions"
  say "pinned to $REQ_VERSION"
fi
VERSION="$(git -C "$SRC_DIR" describe --tags --always --dirty 2>/dev/null || echo 0.2.0)"
say "building live $VERSION"
(cd "$SRC_DIR" && GOTOOLCHAIN=auto go build -ldflags "-X main.version=$VERSION" -o bin/live ./cmd/live)

# ── 4. Install onto PATH ──────────────────────────────────────────────────────
if [ -z "$BIN_DIR" ]; then
  if [ -w /usr/local/bin ]; then
    BIN_DIR=/usr/local/bin
  elif [ -t 0 ] && command -v sudo >/dev/null 2>&1; then
    BIN_DIR=/usr/local/bin
  else
    BIN_DIR="$HOME/.local/bin"
  fi
fi
mkdir -p "$BIN_DIR" 2>/dev/null || true
if [ -w "$BIN_DIR" ]; then
  install -m 0755 "$SRC_DIR/bin/live" "$BIN_DIR/live"
else
  say "installing to $BIN_DIR (needs sudo)"
  sudo install -m 0755 "$SRC_DIR/bin/live" "$BIN_DIR/live"
fi
say "installed → $BIN_DIR/live"

case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *)
    RC="$HOME/.profile"
    case "${SHELL:-}" in */zsh) RC="$HOME/.zshrc" ;; */bash) RC="$HOME/.bashrc" ;; esac
    if ! grep -qs "$BIN_DIR" "$RC" 2>/dev/null; then
      printf '\nexport PATH="%s:$PATH"\n' "$BIN_DIR" >> "$RC"
      say "added $BIN_DIR to PATH in $RC (open a new terminal to pick it up)"
    fi
    export PATH="$BIN_DIR:$PATH"
    ;;
esac

# ── 5. Sign in (device flow opens your browser; no typing needed) ────────────
if "$BIN_DIR/live" whoami >/dev/null 2>&1; then
  say "already signed in: $("$BIN_DIR/live" whoami 2>/dev/null | head -1)"
else
  say "signing in — approve the request in your browser"
  "$BIN_DIR/live" login
fi

say "done. Try:  live claude"
