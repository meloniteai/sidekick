#!/usr/bin/env bash
# sidekick installer.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/meloniteai/sidekick/main/install.sh | bash
#
# Environment overrides:
#   SIDEKICK_REPO         override the GitHub repo (default: meloniteai/sidekick)
#   SIDEKICK_VERSION      install a specific version, e.g. 0.2 (default: latest release)
#   SIDEKICK_INSTALL_DIR  install the binary into this directory instead of the auto-picked one
#   SIDEKICK_SKIP_AGENTS  if set, do not run `sidekick install` after dropping the binary
#   NO_COLOR         if set, suppress ANSI colours
#
# After the binary lands in $PATH we chain into `sidekick install` which writes
# the sidekick skill, registers the MCP server with any detected agent (Claude
# Code, Codex), and merges the PostToolUse write hook into their settings.
# Re-running this script is safe and idempotent — it's also the recommended
# way to refresh integrations after an upgrade.

set -euo pipefail

REPO="${SIDEKICK_REPO:-meloniteai/sidekick}"
BIN="sidekick"

# ---- Palette (matches the TUI brand from internal/sidekick/landing.go) ----
# Coral #E84B30 — headings / banner
# Coral-soft #FF7A55 — paths, versions
# 84 — ✓ done
# 9  — ✗ error
# 245 — dim help text
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ] && [ "${TERM:-}" != "dumb" ]; then
  CORAL=$'\033[38;2;232;75;48m'
  CORAL_SOFT=$'\033[38;2;255;122;85m'
  OK=$'\033[38;5;84m'
  ERR=$'\033[38;5;9m'
  DIM=$'\033[38;5;245m'
  BOLD=$'\033[1m'
  RESET=$'\033[0m'
else
  CORAL=""; CORAL_SOFT=""; OK=""; ERR=""; DIM=""; BOLD=""; RESET=""
fi

banner() {
  printf '\n%s%s   sidekick — agentic-coding compass%s\n'        "$BOLD" "$CORAL"      "$RESET" >&2
  printf '%s   github.com/%s%s\n\n'                          "$DIM"   "$REPO"      "$RESET" >&2
}
step() { printf '%s•%s %s\n'   "$CORAL_SOFT" "$RESET" "$*" >&2; }
ok()   { printf '%s✓%s %s\n'   "$OK"         "$RESET" "$*" >&2; }
warn() { printf '%s⚠%s %s\n'   "$CORAL_SOFT" "$RESET" "$*" >&2; }
fail() { printf '%s✗%s %s\n'   "$ERR"        "$RESET" "$*" >&2; exit 1; }
info() { printf '%s  %s%s\n'   "$DIM"        "$*"     "$RESET" >&2; }

need() { command -v "$1" >/dev/null 2>&1 || fail "missing dependency: $1"; }
need curl
need tar
need uname
need install
need mktemp

banner

case "$(uname -s)" in
  Darwin) OS=darwin ;;
  Linux)  OS=linux  ;;
  MINGW*|MSYS*|CYGWIN*)
    fail "Windows is not yet supported by install.sh. Download the windows zip from https://github.com/${REPO}/releases/latest"
    ;;
  *) fail "unsupported OS: $(uname -s)" ;;
esac

case "$(uname -m)" in
  x86_64|amd64)   ARCH=amd64 ;;
  arm64|aarch64)  ARCH=arm64 ;;
  *) fail "unsupported architecture: $(uname -m)" ;;
esac

# Resolve version
if [ -n "${SIDEKICK_VERSION:-}" ]; then
  VERSION="${SIDEKICK_VERSION#v}"
  TAG="v${VERSION}"
  step "Using pinned version ${CORAL_SOFT}${VERSION}${RESET}"
else
  step "Resolving latest release from github.com/${REPO}"
  # Capture the full API response before parsing instead of piping curl
  # straight into `grep -m1`. With curl 8.5 (the version ubuntu 24.04 ships)
  # `set -o pipefail` promotes grep's early-close into a curl write error
  # (exit 23) and the whole script aborts — even though the tag was already
  # received. Reading into a variable first buys atomicity without losing
  # the safety nets.
  LATEST_JSON="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest")" \
    || fail "could not fetch latest release info"
  TAG="$(printf '%s\n' "$LATEST_JSON" | grep -m1 '"tag_name":' | sed -E 's/.*"tag_name":[[:space:]]*"([^"]+)".*/\1/')"
  [ -n "$TAG" ] || fail "could not determine latest release tag"
  VERSION="${TAG#v}"
fi

ASSET="${BIN}_${VERSION}_${OS}_${ARCH}.tar.gz"
ASSET_URL="https://github.com/${REPO}/releases/download/${TAG}/${ASSET}"
CHECKSUMS_URL="https://github.com/${REPO}/releases/download/${TAG}/checksums.txt"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

step "Downloading ${CORAL_SOFT}${ASSET}${RESET} (${TAG})"
curl -fsSL "$ASSET_URL"       -o "$TMP/$ASSET"         || fail "download failed: $ASSET_URL"
curl -fsSL "$CHECKSUMS_URL"   -o "$TMP/checksums.txt"  || fail "download failed: $CHECKSUMS_URL"

# Verify checksum
EXPECTED="$(grep " ${ASSET}\$" "$TMP/checksums.txt" | awk '{print $1}')"
[ -n "$EXPECTED" ] || fail "no checksum entry for ${ASSET} in checksums.txt"

if command -v sha256sum >/dev/null 2>&1; then
  ACTUAL="$(sha256sum "$TMP/$ASSET" | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then
  ACTUAL="$(shasum -a 256 "$TMP/$ASSET" | awk '{print $1}')"
else
  fail "missing dependency: sha256sum or shasum"
fi
[ "$EXPECTED" = "$ACTUAL" ] || fail "checksum mismatch for ${ASSET}: expected $EXPECTED, got $ACTUAL"
ok "checksum verified"

tar -xzf "$TMP/$ASSET" -C "$TMP"
[ -x "$TMP/$BIN" ] || fail "archive did not contain executable: ${BIN}"

# Pick install dir
if [ -n "${SIDEKICK_INSTALL_DIR:-}" ]; then
  DEST="$SIDEKICK_INSTALL_DIR"
  mkdir -p "$DEST"
elif [ -w /usr/local/bin ] 2>/dev/null; then
  DEST="/usr/local/bin"
else
  DEST="$HOME/.local/bin"
  mkdir -p "$DEST"
fi

install -m 755 "$TMP/$BIN" "$DEST/$BIN"
ok "installed ${CORAL_SOFT}${BIN} ${VERSION}${RESET} -> ${CORAL_SOFT}${DEST}/${BIN}${RESET}"

case ":${PATH}:" in
  *":${DEST}:"*) ;;
  *)
    warn "${DEST} is not on your PATH"
    info "add to your shell profile:  export PATH=\"${DEST}:\$PATH\""
    ;;
esac

# ---- Chain into `sidekick install` for agent integration ----
if [ -n "${SIDEKICK_SKIP_AGENTS:-}" ]; then
  info "skipping agent integration (SIDEKICK_SKIP_AGENTS set). Run \`sidekick install\` later to wire up Claude / Codex."
elif ! "$DEST/$BIN" install --help >/dev/null 2>&1; then
  warn "this sidekick version does not have \`sidekick install\` yet — re-run \`curl … | bash\` after the next release, or wire up Claude/Codex manually (see README)."
else
  printf '\n%s%s   Agent integration%s\n' "$BOLD" "$CORAL" "$RESET" >&2
  # Always pass --yes here. When stdin is a tty (user invoked install.sh
  # directly, not piped from curl) we want to skip prompting because they
  # already opted in by running the installer. The user can re-run
  # `sidekick install` manually for fine-grained control.
  if ! "$DEST/$BIN" install --yes; then
    warn "agent integration finished with warnings — re-run \`sidekick install\` to retry."
  fi
fi

printf '\n%s%s   Done.%s  Run %s%ssidekick%s in a repo to launch the daemon.\n\n' \
  "$BOLD" "$OK" "$RESET" "$CORAL_SOFT" "$BOLD" "$RESET" >&2
