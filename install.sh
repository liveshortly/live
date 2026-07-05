#!/usr/bin/env bash
# live — one-command installer.
#
#     curl -fsSL https://liveshortly.com/install.sh | bash
#
# (Cloning the private source repo still needs your GitHub SSH key or gh
# auth. Equivalent while you have a checkout: bash install.sh)
#
# Does everything: fetches/updates the source, ensures a Go toolchain, builds,
# installs the binary onto your PATH, and signs you in. Idempotent — rerun it
# any time to update.

# Piped through plain `sh` on some systems? Everything below assumes bash.
if [ -z "${BASH_VERSION:-}" ]; then
  echo "[live-install] please pipe to bash:  curl -fsSL https://liveshortly.com/install.sh | bash" >&2
  exit 1
fi
set -euo pipefail

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
elif [ -d "$SRC_DIR/.git" ]; then
  say "updating $SRC_DIR"
  git -C "$SRC_DIR" pull --ff-only --quiet || say "warning: could not fast-forward; building what's there"
else
  mkdir -p "$(dirname "$SRC_DIR")"
  say "cloning resapce/live → $SRC_DIR"
  git clone --quiet "$REPO_SSH" "$SRC_DIR" 2>/dev/null \
    || git clone --quiet "$REPO_HTTPS" "$SRC_DIR" \
    || fail "clone failed — check your GitHub SSH key or credentials for resapce/live."
fi

# ── 2. Ensure a Go toolchain ──────────────────────────────────────────────────
# Any Go >= 1.21 auto-downloads the exact toolchain go.mod asks for
# (GOTOOLCHAIN=auto), so a package-manager Go is always sufficient.
if ! command -v go >/dev/null 2>&1; then
  say "Go not found — installing it"
  if [ "$(uname -s)" = "Darwin" ] && command -v brew >/dev/null 2>&1; then
    brew install go
  elif command -v apt-get >/dev/null 2>&1; then
    sudo apt-get update -qq && sudo apt-get install -y -qq golang-go
  elif command -v dnf >/dev/null 2>&1; then
    sudo dnf install -y -q golang
  elif command -v pacman >/dev/null 2>&1; then
    sudo pacman -S --noconfirm --quiet go
  else
    fail "could not install Go automatically — install Go from https://go.dev/dl/ and rerun."
  fi
fi

# ── 3. Build ──────────────────────────────────────────────────────────────────
VERSION="$(git -C "$SRC_DIR" describe --tags --always --dirty 2>/dev/null || echo 0.1.0)"
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
