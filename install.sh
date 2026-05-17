#!/usr/bin/env bash
# hud installer.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/meloniteai/kikaite-hud/main/install.sh | bash
#
# Environment overrides:
#   HUD_REPO         override the GitHub repo (default: meloniteai/kikaite-hud)
#   HUD_VERSION      install a specific version, e.g. 0.2 (default: latest release)
#   HUD_INSTALL_DIR  install into this directory instead of the auto-picked one
#
# The binary lazily creates ~/.hud on first use; the installer only drops the
# `hud` executable into your PATH.

set -euo pipefail

REPO="${HUD_REPO:-meloniteai/kikaite-hud}"
BIN="hud"

log()  { printf '%s\n' "$*" >&2; }
fail() { log "error: $*"; exit 1; }

need() { command -v "$1" >/dev/null 2>&1 || fail "missing dependency: $1"; }
need curl
need tar
need uname
need install
need mktemp

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
if [ -n "${HUD_VERSION:-}" ]; then
  VERSION="${HUD_VERSION#v}"
  TAG="v${VERSION}"
else
  log "Resolving latest release from github.com/${REPO}..."
  TAG="$(
    curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
      | grep -m1 '"tag_name":' \
      | sed -E 's/.*"tag_name":[[:space:]]*"([^"]+)".*/\1/'
  )"
  [ -n "$TAG" ] || fail "could not determine latest release tag"
  VERSION="${TAG#v}"
fi

ASSET="${BIN}_${VERSION}_${OS}_${ARCH}.tar.gz"
ASSET_URL="https://github.com/${REPO}/releases/download/${TAG}/${ASSET}"
CHECKSUMS_URL="https://github.com/${REPO}/releases/download/${TAG}/checksums.txt"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

log "Downloading ${ASSET} (${TAG})..."
curl -fsSL "$ASSET_URL"     -o "$TMP/$ASSET"     || fail "download failed: $ASSET_URL"
curl -fsSL "$CHECKSUMS_URL" -o "$TMP/checksums.txt" || fail "download failed: $CHECKSUMS_URL"

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

tar -xzf "$TMP/$ASSET" -C "$TMP"
[ -x "$TMP/$BIN" ] || fail "archive did not contain executable: ${BIN}"

# Pick install dir
if [ -n "${HUD_INSTALL_DIR:-}" ]; then
  DEST="$HUD_INSTALL_DIR"
  mkdir -p "$DEST"
elif [ -w /usr/local/bin ] 2>/dev/null; then
  DEST="/usr/local/bin"
else
  DEST="$HOME/.local/bin"
  mkdir -p "$DEST"
fi

install -m 755 "$TMP/$BIN" "$DEST/$BIN"

log "Installed ${BIN} ${VERSION} -> ${DEST}/${BIN}"

case ":${PATH}:" in
  *":${DEST}:"*) ;;
  *)
    log ""
    log "${DEST} is not on your PATH. Add this line to your shell profile:"
    log "    export PATH=\"${DEST}:\$PATH\""
    ;;
esac
