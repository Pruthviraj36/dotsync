#!/usr/bin/env bash
#
# DotSync installer
#
# Downloads the prebuilt release binary for your OS/arch, verifies it
# against the published SHA-256 checksum, and installs to /usr/local/bin
# (falls back to ~/.local/bin if not writable and sudo unavailable).
#
# Usage:
#   curl -fsSL https://<your-server>/install.sh | bash
#
# Override install location:
#   DOTSYNC_INSTALL_DIR=/opt/bin curl -fsSL https://<your-server>/install.sh | bash
#
# After install, point the CLI at your server:
#   export DOTSYNC_SERVER=https://<your-server>
#   dotsync login

set -euo pipefail

REPO="Pruthviraj36/dotsync"
BIN_NAME="dotsync"
INSTALL_DIR="${DOTSYNC_INSTALL_DIR:-/usr/local/bin}"

say() { printf '%s\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

os_raw="$(uname -s)"
arch_raw="$(uname -m)"

case "$os_raw" in
  Linux)  goos="linux"  ;;
  Darwin) goos="darwin" ;;
  *) die "unsupported OS: $os_raw — grab a binary from https://github.com/${REPO}/releases" ;;
esac

case "$arch_raw" in
  x86_64 | amd64)  goarch="amd64" ;;
  arm64 | aarch64) goarch="arm64" ;;
  *) die "unsupported architecture: $arch_raw" ;;
esac

asset="${BIN_NAME}-${goos}-${goarch}.tar.gz"

command -v curl >/dev/null 2>&1 || die "curl is required but not found"
command -v tar  >/dev/null 2>&1 || die "tar is required but not found"

say "==> Checking latest release..."
release_json="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest")" \
  || die "failed to reach GitHub — check your network connection"

tag="$(printf '%s' "$release_json" | grep -m1 '"tag_name"' | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')"
[ -n "$tag" ] || die "could not determine the latest release tag"
say "==> Latest version: ${tag}"

base_url="https://github.com/${REPO}/releases/download/${tag}"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

say "==> Downloading ${asset}..."
curl -fsSL "${base_url}/${asset}"       -o "${tmp_dir}/${asset}"       || die "failed to download ${asset}"
curl -fsSL "${base_url}/checksums.txt"  -o "${tmp_dir}/checksums.txt"  || die "failed to download checksums.txt"

say "==> Verifying checksum..."
expected="$(grep " ${asset}$" "${tmp_dir}/checksums.txt" | awk '{print $1}')"
[ -n "$expected" ] || die "no checksum entry for ${asset} — refusing to install"

if   command -v sha256sum >/dev/null 2>&1; then actual="$(sha256sum "${tmp_dir}/${asset}" | awk '{print $1}')"
elif command -v shasum    >/dev/null 2>&1; then actual="$(shasum -a 256 "${tmp_dir}/${asset}" | awk '{print $1}')"
else die "neither sha256sum nor shasum available — cannot verify download"
fi

[ "$expected" = "$actual" ] || die "checksum mismatch — download may be corrupted or tampered with"
say "==> Checksum verified"

say "==> Extracting..."
tar -xzf "${tmp_dir}/${asset}" -C "${tmp_dir}"
[ -f "${tmp_dir}/${BIN_NAME}" ] || die "binary not found inside ${asset}"
chmod +x "${tmp_dir}/${BIN_NAME}"

say "==> Installing to ${INSTALL_DIR}/${BIN_NAME}"
if [ -w "$INSTALL_DIR" ] || mkdir -p "$INSTALL_DIR" 2>/dev/null && [ -w "$INSTALL_DIR" ]; then
  mv "${tmp_dir}/${BIN_NAME}" "${INSTALL_DIR}/${BIN_NAME}"
elif command -v sudo >/dev/null 2>&1; then
  say "    ${INSTALL_DIR} not writable — using sudo"
  sudo mkdir -p "$INSTALL_DIR"
  sudo mv "${tmp_dir}/${BIN_NAME}" "${INSTALL_DIR}/${BIN_NAME}"
else
  fallback="${HOME}/.local/bin"
  mkdir -p "$fallback"
  mv "${tmp_dir}/${BIN_NAME}" "${fallback}/${BIN_NAME}"
  INSTALL_DIR="$fallback"
  say ""
  say "  Installed to ${fallback} (add to PATH if needed):"
  say "    export PATH=\"\$PATH:${fallback}\""
  say ""
fi

say ""
say "dotsync ${tag} installed to ${INSTALL_DIR}/${BIN_NAME}"
"${INSTALL_DIR}/${BIN_NAME}" --version 2>/dev/null || true
say ""
say "Next: set your server and log in:"
say "  export DOTSYNC_SERVER=https://<your-server>"
say "  dotsync login"
