#!/usr/bin/env bash
#
# Installs the sf binary from a GitHub release.
#
#   curl -fsSL https://raw.githubusercontent.com/xseman/superfaktura-cli/master/scripts/install.sh | bash
#
# Environment:
#   VERSION  release tag to install (default: latest)
#   PREFIX   install into $PREFIX/bin (default: /usr/local as root, $HOME/.local otherwise)
#
set -euo pipefail

REPO="xseman/superfaktura-cli"
BINARY="sf"

die() { echo "Error: $*" >&2; exit 1; }

case "$(uname -s)" in
  Darwin*) PLATFORM="darwin" ;;
  Linux*)  PLATFORM="linux" ;;
  *)       die "unsupported operating system $(uname -s). On Windows download sf-windows-amd64.exe from https://github.com/$REPO/releases" ;;
esac

case "$(uname -m)" in
  x86_64|amd64)  ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *)             die "unsupported architecture $(uname -m)" ;;
esac

if command -v curl >/dev/null 2>&1; then
  fetch() { curl -fsSL "$1" -o "$2"; }
elif command -v wget >/dev/null 2>&1; then
  fetch() { wget -qO "$2" "$1"; }
else
  die "neither curl nor wget is available"
fi

VERSION="${VERSION:-latest}"
if [ "$VERSION" = "latest" ]; then
  BASE="https://github.com/$REPO/releases/latest/download"
else
  case "$VERSION" in v*) TAG="$VERSION" ;; *) TAG="v$VERSION" ;; esac
  BASE="https://github.com/$REPO/releases/download/$TAG"
fi

# The release carries bare binaries named sf-<os>-<arch>, plus CHECKSUMS.txt.
ASSET="${BINARY}-${PLATFORM}-${ARCH}"
TMP="$(mktemp -d)"
trap 'rm -rf -- "$TMP"' EXIT

echo "Downloading $ASSET ($VERSION)..."
fetch "$BASE/$ASSET" "$TMP/$ASSET" || die "download failed: $BASE/$ASSET"

# Verify the download when checksums are published and a hashing tool exists.
if fetch "$BASE/CHECKSUMS.txt" "$TMP/CHECKSUMS.txt" 2>/dev/null; then
  if command -v sha256sum >/dev/null 2>&1; then
    (cd "$TMP" && sha256sum -c --ignore-missing CHECKSUMS.txt >/dev/null) \
      || die "checksum verification failed"
    echo "Checksum verified."
  elif command -v shasum >/dev/null 2>&1; then
    (cd "$TMP" && shasum -a 256 -c --ignore-missing CHECKSUMS.txt >/dev/null) \
      || die "checksum verification failed"
    echo "Checksum verified."
  else
    echo "Warning: no sha256sum or shasum found, skipping verification." >&2
  fi
fi

if [ "$(id -u)" -eq 0 ]; then
  PREFIX="${PREFIX:-/usr/local}"
else
  PREFIX="${PREFIX:-$HOME/.local}"
fi
INSTALL_DIR="$PREFIX/bin"

mkdir -p "$INSTALL_DIR" \
  || die "cannot create $INSTALL_DIR — rerun with sudo, or set PREFIX to a directory you own"

install -m 0755 "$TMP/$ASSET" "$INSTALL_DIR/$BINARY"
echo "Installed $INSTALL_DIR/$BINARY"

# `sf` is also the Salesforce CLI. Say so rather than let the collision surprise
# somebody later.
EXISTING="$(command -v "$BINARY" 2>/dev/null || true)"
if [ -n "$EXISTING" ] && [ "$EXISTING" != "$INSTALL_DIR/$BINARY" ]; then
  echo
  echo "Notice: another '$BINARY' is earlier on your PATH at $EXISTING."
  echo "        Reorder your PATH, or call $INSTALL_DIR/$BINARY directly."
elif [ -z "$EXISTING" ]; then
  echo
  echo "Notice: $INSTALL_DIR is not on your PATH. Add it with:"
  echo "  export PATH=\"$INSTALL_DIR:\$PATH\""
fi
