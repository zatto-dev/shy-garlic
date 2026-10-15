#!/bin/sh
# Installs a pinned Helm into $HOME/.local/bin and verifies its SHA-256.
# A script instead of a third-party action: the organisation only allows
# GitHub-owned actions pinned by full SHA, and one dependency fewer to audit.
# To bump: change both values below (sum from https://get.helm.sh/helm-<ver>-linux-amd64.tar.gz.sha256sum).
set -eu
HELM_VERSION="v3.22.1"
HELM_SHA256="896cf63684a42a723d61c980369d6e588330927deb13676ca0d85006dd6da0ea"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
curl -fsSL -o "$tmp/helm.tgz" "https://get.helm.sh/helm-${HELM_VERSION}-linux-amd64.tar.gz"
echo "${HELM_SHA256}  $tmp/helm.tgz" | sha256sum -c -
tar -xzf "$tmp/helm.tgz" -C "$tmp" linux-amd64/helm
mkdir -p "$HOME/.local/bin"
install -m755 "$tmp/linux-amd64/helm" "$HOME/.local/bin/helm"
[ -z "${GITHUB_PATH:-}" ] || echo "$HOME/.local/bin" >> "$GITHUB_PATH"
"$HOME/.local/bin/helm" version --short
