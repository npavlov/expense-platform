#!/usr/bin/env bash
# Installs the pinned istioctl into .local/bin, verified by SHA-256.
# The hashes are pinned here, not downloaded next to the archive, so a
# tampered release asset cannot come with a matching checksum.
#
#   scripts/install-istioctl.sh        -> .local/bin/istioctl
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
source "$root/scripts/istio-version.sh"

case "$(uname -s)-$(uname -m)" in
  Linux-x86_64)  platform=linux-amd64; sha="$ISTIOCTL_SHA256_LINUX_AMD64" ;;
  Linux-aarch64) platform=linux-arm64; sha="$ISTIOCTL_SHA256_LINUX_ARM64" ;;
  Darwin-arm64)  platform=osx-arm64;   sha="$ISTIOCTL_SHA256_OSX_ARM64" ;;
  *) echo "unsupported platform $(uname -s)-$(uname -m)" >&2; exit 1 ;;
esac

bin="$root/.local/bin"; mkdir -p "$bin"
tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
archive="istioctl-$ISTIO_VERSION-$platform.tar.gz"
curl -fsSL -o "$tmp/$archive" "https://github.com/istio/istio/releases/download/$ISTIO_VERSION/$archive"
echo "$sha  $tmp/$archive" | shasum -a 256 -c - >/dev/null
tar -xzf "$tmp/$archive" -C "$tmp" istioctl
install -m 0755 "$tmp/istioctl" "$bin/istioctl"
echo "installed $("$bin/istioctl" version --remote=false 2>/dev/null) to $bin/istioctl"
