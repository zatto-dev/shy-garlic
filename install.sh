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

blad() { printf '\n  %s\n\n' "$*" >&2; exit 1; }
info() { printf '  %s\n' "$*"; }

for narzedzie in curl tar uname; do
  command -v "$narzedzie" >/dev/null 2>&1 || blad "$narzedzie is required but not installed."
done

# ── suma kontrolna ───────────────────────────────────────────────────────────
# Bez niej instalator jest tylko wygodniejszym sposobem na uruchomienie
# cudzego pliku, więc brak narzędzia jest błędem, nie ostrzeżeniem.
if command -v sha256sum >/dev/null 2>&1; then
  policz_sume() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then
  policz_sume() { shasum -a 256 "$1" | cut -d' ' -f1; }
else
  blad "Neither sha256sum nor shasum found. Cannot verify the download."
fi

# ── system i architektura ────────────────────────────────────────────────────
case "$(uname -s)" in
  Linux)  SYSTEM=linux ;;
  Darwin) SYSTEM=darwin ;;
  *)      blad "Unsupported system: $(uname -s). Build from source: go install github.com/${REPO}/cmd/shyg@latest" ;;
esac

case "$(uname -m)" in
  x86_64|amd64)  ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  armv7l|armv6l) ARCH=arm ;;
  *)             blad "Unsupported architecture: $(uname -m)." ;;
esac

[ "$SYSTEM" = darwin ] && [ "$ARCH" = arm ] && blad "There is no 32-bit ARM build for macOS."

# ── wersja ───────────────────────────────────────────────────────────────────
if [ -n "${SHYG_VERSION:-}" ]; then
  WERSJA="$SHYG_VERSION"
else
  info "Looking up the latest release..."
  WERSJA=$(curl -fsSL "$API" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1)
  [ -n "$WERSJA" ] || blad "Could not read the latest version from GitHub. Set SHYG_VERSION to pick one yourself."
fi
GOLA_WERSJA="${WERSJA#v}"

ARCHIWUM="shyg_${GOLA_WERSJA}_${SYSTEM}_${ARCH}.tar.gz"
KATALOG=$(mktemp -d)
# Sprzątamy także po błędzie — inaczej każda nieudana próba zostawia śmieci.
trap 'rm -rf "$KATALOG"' EXIT INT TERM

info "Downloading ${ARCHIWUM} (${WERSJA})..."
curl -fsSL -o "${KATALOG}/${ARCHIWUM}" "${DL}/${WERSJA}/${ARCHIWUM}" \
  || blad "Download failed. Does ${WERSJA} have a build for ${SYSTEM}/${ARCH}?"
curl -fsSL -o "${KATALOG}/checksums.txt" "${DL}/${WERSJA}/checksums.txt" \
  || blad "Could not download the checksums file. Refusing to install unverified code."

OCZEKIWANA=$(grep " ${ARCHIWUM}\$" "${KATALOG}/checksums.txt" | cut -d' ' -f1)
[ -n "$OCZEKIWANA" ] || blad "${ARCHIWUM} is not listed in checksums.txt. Refusing to install."
POLICZONA=$(policz_sume "${KATALOG}/${ARCHIWUM}")
if [ "$OCZEKIWANA" != "$POLICZONA" ]; then
  blad "Checksum mismatch.
  expected: ${OCZEKIWANA}
  got:      ${POLICZONA}
  Nothing was installed."
fi
info "Checksum verified."

tar -xzf "${KATALOG}/${ARCHIWUM}" -C "$KATALOG" || blad "Could not unpack the archive."
[ -f "${KATALOG}/shyg" ] || blad "The archive does not contain the shyg binary."
chmod +x "${KATALOG}/shyg"

# ── miejsce instalacji ───────────────────────────────────────────────────────
if [ -n "${SHYG_BIN_DIR:-}" ]; then
  KATALOG_BIN="$SHYG_BIN_DIR"
elif [ -w /usr/local/bin ] 2>/dev/null; then
  KATALOG_BIN=/usr/local/bin
elif [ "$(id -u)" = 0 ]; then
  KATALOG_BIN=/usr/local/bin
else
  KATALOG_BIN="$HOME/.local/bin"
fi
mkdir -p "$KATALOG_BIN" || blad "Cannot create ${KATALOG_BIN}."

if [ -w "$KATALOG_BIN" ]; then
  mv "${KATALOG}/shyg" "${KATALOG_BIN}/shyg"
elif command -v sudo >/dev/null 2>&1; then
  info "${KATALOG_BIN} needs elevated rights — calling sudo."
  sudo mv "${KATALOG}/shyg" "${KATALOG_BIN}/shyg"
else
  blad "${KATALOG_BIN} is not writable and sudo is not available. Set SHYG_BIN_DIR to a directory you own."
fi

printf '\n  Shy Garlic %s installed in %s\n' "$WERSJA" "$KATALOG_BIN"
case ":${PATH}:" in
  *":${KATALOG_BIN}:"*) ;;
  *) printf '  ! %s is not in your PATH.\n' "$KATALOG_BIN" ;;
esac
printf '\n  Next:  shyg serve -config shygarlic.yaml\n'
printf '  Docs:  https://github.com/%s\n\n' "$REPO"
