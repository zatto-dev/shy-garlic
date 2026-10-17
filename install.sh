#!/bin/sh
# Shy Garlic — installer.
#
#   curl -fsSL https://raw.githubusercontent.com/zatto-dev/shy-garlic/main/install.sh | sh
#
# Pins a version:            SHYG_VERSION=v0.11.0 sh install.sh
# Installs somewhere else:   SHYG_BIN_DIR=$HOME/.local/bin sh install.sh
#
# The script downloads a release archive, CHECKS ITS SHA-256 against the
# checksums file published with the same release, and only then installs it.
# A download that cannot be verified is a download that does not get installed.
set -eu

REPO="zatto-dev/shy-garlic"
API="https://api.github.com/repos/${REPO}/releases/latest"
DL="https://github.com/${REPO}/releases/download"

fail() { printf '\n  %s\n\n' "$*" >&2; exit 1; }
info() { printf '  %s\n' "$*"; }

for tool in curl tar uname; do
  command -v "$tool" >/dev/null 2>&1 || fail "$tool is required but not installed."
done

# ── checksum ─────────────────────────────────────────────────────────────────
# Without it the installer is just a more convenient way to run somebody
# else's file, so a missing tool is an error, not a warning.
if command -v sha256sum >/dev/null 2>&1; then
  compute_sum() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then
  compute_sum() { shasum -a 256 "$1" | cut -d' ' -f1; }
else
  fail "Neither sha256sum nor shasum found. Cannot verify the download."
fi

# ── system and architecture ──────────────────────────────────────────────────
case "$(uname -s)" in
  Linux)  SYSTEM=linux ;;
  Darwin) SYSTEM=darwin ;;
  *)      fail "Unsupported system: $(uname -s). Build from source: go install github.com/${REPO}/cmd/shyg@latest" ;;
esac

case "$(uname -m)" in
  x86_64|amd64)  ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  armv7l|armv6l) ARCH=arm ;;
  *)             fail "Unsupported architecture: $(uname -m)." ;;
esac

[ "$SYSTEM" = darwin ] && [ "$ARCH" = arm ] && fail "There is no 32-bit ARM build for macOS."

# ── version ──────────────────────────────────────────────────────────────────
if [ -n "${SHYG_VERSION:-}" ]; then
  VERSION="$SHYG_VERSION"
else
  info "Looking up the latest release..."
  VERSION=$(curl -fsSL "$API" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1)
  [ -n "$VERSION" ] || fail "Could not read the latest version from GitHub. Set SHYG_VERSION to pick one yourself."
fi
BARE_VERSION="${VERSION#v}"

ARCHIVE="shyg_${BARE_VERSION}_${SYSTEM}_${ARCH}.tar.gz"
WORKDIR=$(mktemp -d)
# Clean up after a failure too — otherwise every failed attempt leaves junk behind.
trap 'rm -rf "$WORKDIR"' EXIT INT TERM

info "Downloading ${ARCHIVE} (${VERSION})..."
curl -fsSL -o "${WORKDIR}/${ARCHIVE}" "${DL}/${VERSION}/${ARCHIVE}" \
  || fail "Download failed. Does ${VERSION} have a build for ${SYSTEM}/${ARCH}?"
curl -fsSL -o "${WORKDIR}/checksums.txt" "${DL}/${VERSION}/checksums.txt" \
  || fail "Could not download the checksums file. Refusing to install unverified code."

EXPECTED=$(grep " ${ARCHIVE}\$" "${WORKDIR}/checksums.txt" | cut -d' ' -f1)
[ -n "$EXPECTED" ] || fail "${ARCHIVE} is not listed in checksums.txt. Refusing to install."
ACTUAL=$(compute_sum "${WORKDIR}/${ARCHIVE}")
if [ "$EXPECTED" != "$ACTUAL" ]; then
  fail "Checksum mismatch.
  expected: ${EXPECTED}
  got:      ${ACTUAL}
  Nothing was installed."
fi
info "Checksum verified."

tar -xzf "${WORKDIR}/${ARCHIVE}" -C "$WORKDIR" || fail "Could not unpack the archive."
[ -f "${WORKDIR}/shyg" ] || fail "The archive does not contain the shyg binary."
chmod +x "${WORKDIR}/shyg"

# ── install location ─────────────────────────────────────────────────────────
if [ -n "${SHYG_BIN_DIR:-}" ]; then
  BIN_TARGET="$SHYG_BIN_DIR"
elif [ -w /usr/local/bin ] 2>/dev/null; then
  BIN_TARGET=/usr/local/bin
elif [ "$(id -u)" = 0 ]; then
  BIN_TARGET=/usr/local/bin
else
  BIN_TARGET="$HOME/.local/bin"
fi
mkdir -p "$BIN_TARGET" || fail "Cannot create ${BIN_TARGET}."

if [ -w "$BIN_TARGET" ]; then
  mv "${WORKDIR}/shyg" "${BIN_TARGET}/shyg"
elif command -v sudo >/dev/null 2>&1; then
  info "${BIN_TARGET} needs elevated rights — calling sudo."
  sudo mv "${WORKDIR}/shyg" "${BIN_TARGET}/shyg"
else
  fail "${BIN_TARGET} is not writable and sudo is not available. Set SHYG_BIN_DIR to a directory you own."
fi

printf '\n  Shy Garlic %s installed in %s\n' "$VERSION" "$BIN_TARGET"
case ":${PATH}:" in
  *":${BIN_TARGET}:"*) ;;
  *) printf '  ! %s is not in your PATH.\n' "$BIN_TARGET" ;;
esac
printf '\n  Next:  shyg serve -config shygarlic.yaml\n'
printf '  Docs:  https://github.com/%s\n\n' "$REPO"
