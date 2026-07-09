#!/usr/bin/env bash
# live — one-command installer.
#
#     curl -fsSL https://liveshortly.com/i.sh | bash
#
# Downloads a prebuilt, checksum-verified binary for your platform, puts it on
# your PATH, and signs you in. No Go toolchain, no source checkout, no build.
# Idempotent — rerun it any time to update.
#
# Everything lives inside main(), invoked on the last line. A truncated download
# therefore runs nothing at all, rather than half an installer.

# Piped through plain `sh` on some systems? Everything below assumes bash.
if [ -z "${BASH_VERSION:-}" ]; then
  echo "[live-install] please pipe to bash:  curl -fsSL https://liveshortly.com/i.sh | bash" >&2
  exit 1
fi
set -euo pipefail

# Where release assets are served from. GitHub Releases is the origin; the
# LiveShortly web app mirrors the same filenames and is the fallback. The
# version pointer always comes from the mirror (no API rate limit).
GH_BASE="${LIVE_GH_BASE:-https://github.com/liveshortly/live-dist/releases/download}"
MIRROR_BASE="${LIVE_INSTALL_BASE:-https://liveshortly.com/install}"

say()  { printf '\033[32m[live-install]\033[0m %s\n' "$*"; }
warn() { printf '\033[33m[live-install]\033[0m %s\n' "$*" >&2; }
fail() { printf '\033[31m[live-install]\033[0m %s\n' "$*" >&2; exit 1; }

# fetch <url> [dest] — dest of "-" (default) means stdout.
fetch() {
  local url="$1" dest="${2:--}"
  if command -v curl >/dev/null 2>&1; then
    if [ "$dest" = "-" ]; then curl -fsSL --retry 3 "$url"
    else curl -fsSL --retry 3 -o "$dest" "$url"; fi
  elif command -v wget >/dev/null 2>&1; then
    if [ "$dest" = "-" ]; then wget -qO- "$url"
    else wget -qO "$dest" "$url"; fi
  else
    fail "need curl or wget to download."
  fi
}

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1" | awk '{print $1}'
  else fail "need sha256sum or shasum to verify the download."; fi
}

# Global, not local to main(): the EXIT trap fires after main() returns, by
# which point a `local` would be out of scope (and unbound under `set -u`).
LIVE_TMP=""
trap 'rm -rf "${LIVE_TMP:-}"' EXIT

main() {
  local REQ_VERSION="${LIVE_VERSION:-}"
  local BIN_DIR="${LIVE_BIN_DIR:-}"
  local NO_LOGIN="${LIVE_NO_LOGIN:-}"
  local NO_MODIFY_PATH="${LIVE_NO_MODIFY_PATH:-}"
  local FROM_SOURCE="${LIVE_FROM_SOURCE:-}"

  while [ $# -gt 0 ]; do
    case "$1" in
      --version) REQ_VERSION="${2:-}"; shift 2 ;;
      --bin-dir) BIN_DIR="${2:-}"; shift 2 ;;
      --no-login) NO_LOGIN=1; shift ;;
      --no-modify-path) NO_MODIFY_PATH=1; shift ;;
      --from-source) FROM_SOURCE=1; shift ;;
      *) fail "unknown flag: $1" ;;
    esac
  done
  [ -n "$REQ_VERSION" ] && REQ_VERSION="v${REQ_VERSION#v}"

  if [ -n "$FROM_SOURCE" ]; then
    build_from_source "$REQ_VERSION"
    return
  fi

  # ── 1. Detect platform ──────────────────────────────────────────────────────
  local os arch
  case "$(uname -s)" in
    Darwin) os=darwin ;;
    Linux)  os=linux ;;
    *) fail "unsupported OS: $(uname -s). Supported: macOS, Linux." ;;
  esac
  case "$(uname -m)" in
    x86_64|amd64)  arch=amd64 ;;
    arm64|aarch64) arch=arm64 ;;
    *) fail "unsupported architecture: $(uname -m). Supported: amd64, arm64." ;;
  esac
  # Prefer the native build when this shell is running under Rosetta.
  if [ "$os" = darwin ] && [ "$arch" = amd64 ] \
     && [ "$(sysctl -n sysctl.proc_translated 2>/dev/null || echo 0)" = "1" ]; then
    arch=arm64
    say "Rosetta detected — installing the native arm64 build"
  fi

  # ── 2. Resolve the version ──────────────────────────────────────────────────
  local version="$REQ_VERSION"
  if [ -z "$version" ]; then
    version="$(fetch "$MIRROR_BASE/latest.txt" - 2>/dev/null | tr -d '[:space:]')" \
      || fail "could not resolve the latest version — check your network."
    [ -n "$version" ] || fail "empty version pointer at $MIRROR_BASE/latest.txt"
  fi
  version="v${version#v}"

  # ── 3. Download + verify ────────────────────────────────────────────────────
  local asset="live-$version-$os-$arch"
  local tmp; tmp="$(mktemp -d)"; LIVE_TMP="$tmp"

  say "downloading live $version ($os/$arch)"
  local ok=""
  for base in "$GH_BASE/$version" "$MIRROR_BASE"; do
    if fetch "$base/$asset.sha256" "$tmp/sum" 2>/dev/null \
       && fetch "$base/$asset.gz" "$tmp/$asset.gz" 2>/dev/null; then
      ok=1; break
    fi
    warn "not available at $base — trying the next source"
  done
  [ -n "$ok" ] || fail "live $version is not published for $os/$arch.
              See https://liveshortly.com/install/ for published versions."

  gzip -dc "$tmp/$asset.gz" > "$tmp/live" || fail "corrupt archive — aborting."

  local want got
  want="$(awk '{print $1}' "$tmp/sum")"
  got="$(sha256_of "$tmp/live")"
  [ "$got" = "$want" ] || fail "checksum mismatch (got $got, want $want) — aborting."
  chmod 0755 "$tmp/live"

  # The staged binary must actually run and report the version we asked for.
  local reported
  reported="$("$tmp/live" version 2>/dev/null || true)"
  case "$reported" in
    *"${version#v}"*) ;;
    *) fail "staged binary failed verification (reported: ${reported:-nothing}) — aborting." ;;
  esac

  # ── 4. Install onto PATH ────────────────────────────────────────────────────
  # Upgrade in place when live is already installed, so we never leave a stale
  # copy shadowing the new one on PATH.
  if [ -z "$BIN_DIR" ]; then
    local existing; existing="$(command -v live 2>/dev/null || true)"
    if [ -n "$existing" ]; then
      BIN_DIR="$(dirname "$(readlink "$existing" 2>/dev/null || echo "$existing")")"
    elif [ -w /usr/local/bin ] || { [ -t 0 ] && command -v sudo >/dev/null 2>&1; }; then
      BIN_DIR=/usr/local/bin
    else
      BIN_DIR="$HOME/.local/bin"
    fi
  fi
  mkdir -p "$BIN_DIR" 2>/dev/null || true

  # Stage inside the destination so the final move is an atomic rename: a
  # running `live` is never truncated, and a failure never leaves a partial
  # binary on PATH.
  if [ -w "$BIN_DIR" ]; then
    mv "$tmp/live" "$BIN_DIR/.live.tmp.$$"
    mv "$BIN_DIR/.live.tmp.$$" "$BIN_DIR/live"
  else
    say "installing to $BIN_DIR (needs sudo)"
    sudo install -m 0755 "$tmp/live" "$BIN_DIR/live"
  fi
  say "installed → $BIN_DIR/live"

  if [ -z "$NO_MODIFY_PATH" ]; then
    case ":$PATH:" in
      *":$BIN_DIR:"*) ;;
      *)
        local rc="$HOME/.profile"
        case "${SHELL:-}" in */zsh) rc="$HOME/.zshrc" ;; */bash) rc="$HOME/.bashrc" ;; esac
        if ! grep -qs "$BIN_DIR" "$rc" 2>/dev/null; then
          printf '\nexport PATH="%s:$PATH"\n' "$BIN_DIR" >> "$rc"
          say "added $BIN_DIR to PATH in $rc (open a new terminal to pick it up)"
        fi
        export PATH="$BIN_DIR:$PATH"
        ;;
    esac
  fi

  # tmux is required for `live codex` (it drives codex's composer) and powers
  # the clean in-terminal permission popups for `live claude`.
  command -v tmux >/dev/null 2>&1 \
    || say "tip: install tmux — required for 'live codex', and it gives 'live claude' clean permission popups."

  # ── 5. Sign in (device flow opens your browser; no typing needed) ───────────
  if [ -n "$NO_LOGIN" ]; then
    say "skipping sign-in. Run: live login"
  elif "$BIN_DIR/live" whoami >/dev/null 2>&1; then
    say "already signed in: $("$BIN_DIR/live" whoami 2>/dev/null | head -1)"
  elif [ -t 0 ]; then
    say "signing in — approve the request in your browser"
    "$BIN_DIR/live" login
  else
    # Piped from curl with no terminal: a device flow nobody can approve would
    # just hang forever. Tell them what to run instead.
    say "non-interactive install — finish with: live login"
  fi

  say "done. Try:  live claude"
  say "permission control is on by default (tmux popup / session page) — disable with LIVE_WEB_PERMS=0"
}

# Developer escape hatch: build from a source checkout. Needs repo access.
build_from_source() {
  local req="$1"
  command -v git >/dev/null 2>&1 || fail "git is required — install it and rerun."
  command -v go  >/dev/null 2>&1 || fail "Go is required to build from source (https://go.dev/dl/)."

  local src="${LIVE_SRC_DIR:-$HOME/.liveshortly/src/live}"
  local script_dir; script_dir="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd || true)"
  if [ -n "$script_dir" ] && grep -qs '^module github.com/liveshortly/live$' "$script_dir/go.mod" 2>/dev/null; then
    src="$script_dir"
    say "using existing checkout: $src"
  elif [ -d "$src/.git" ]; then
    say "updating $src"
    git -C "$src" fetch --tags --quiet 2>/dev/null || true
    if [ -z "$req" ]; then git -C "$src" pull --ff-only --quiet 2>/dev/null || true; fi
  else
    mkdir -p "$(dirname "$src")"
    say "cloning liveshortly/live → $src"
    git clone --quiet git@github.com:liveshortly/live.git "$src" \
      || fail "clone failed — liveshortly/live is private; check your GitHub SSH key."
  fi
  [ -n "$req" ] && git -C "$src" checkout --quiet "$req"

  local v; v="$(git -C "$src" describe --tags --always --dirty 2>/dev/null || echo dev)"
  say "building live $v"
  (cd "$src" && GOTOOLCHAIN=auto go build -trimpath -ldflags "-s -w -X main.version=${v#v}" -o bin/live ./cmd/live)

  local bin_dir="${LIVE_BIN_DIR:-$HOME/.local/bin}"
  mkdir -p "$bin_dir"
  install -m 0755 "$src/bin/live" "$bin_dir/live"
  say "installed → $bin_dir/live"
}

main "$@"
