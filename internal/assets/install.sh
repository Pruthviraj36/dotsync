#!/usr/bin/env bash
#
# dotsync installer
#
# Downloads the prebuilt release binary for your OS/arch, verifies it
# against the published SHA-256 checksum, and installs it to /usr/local/bin
# (falling back to ~/.local/bin if that's not writable and sudo isn't
# available). This exists specifically so `dotsync` ends up somewhere
# that's on your $PATH by default — unlike `go install`, which puts the
# binary in $(go env GOPATH)/bin (usually ~/go/bin), a directory most
# fresh Linux installs don't have on PATH at all.
#
# Usage:
#   curl -fsSL https://dotsync.onrender.com/install.sh | bash
#
# Override the install location:
#   DOTSYNC_INSTALL_DIR=/opt/bin curl -fsSL https://dotsync.onrender.com/install.sh | bash

set -euo pipefail

REPO="Pruthviraj36/dotsync"
BIN_NAME="dotsync"
INSTALL_DIR="${DOTSYNC_INSTALL_DIR:-/usr/local/bin}"

say() { printf '%s\n' "$*"; }
die() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

# ── Detect OS / arch, matching the goos/goarch names GoReleaser builds for ──
os_raw="$(uname -s)"
arch_raw="$(uname -m)"

case "$os_raw" in
  Linux) goos="linux" ;;
  Darwin) goos="darwin" ;;
  *) die "unsupported OS: $os_raw — grab a binary manually from https://github.com/${REPO}/releases" ;;
esac

case "$arch_raw" in
  x86_64 | amd64) goarch="amd64" ;;
  arm64 | aarch64) goarch="arm64" ;;
  *) die "unsupported architecture: $arch_raw" ;;
esac

asset="${BIN_NAME}-${goos}-${goarch}.tar.gz"

command -v curl >/dev/null 2>&1 || die "curl is required but not found"
command -v tar >/dev/null 2>&1 || die "tar is required but not found"

# ── Resolve the latest release tag ──────────────────────────────────────────
say "==> Checking latest release..."
release_json="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest")" \
  || die "failed to reach GitHub — check your network connection"

tag="$(printf '%s' "$release_json" | grep -m1 '"tag_name"' | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')"
[ -n "$tag" ] || die "could not determine the latest release tag"
say "==> Latest version: ${tag}"

base_url="https://github.com/${REPO}/releases/download/${tag}"

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

# ── Download the archive and its checksums ──────────────────────────────────
say "==> Downloading ${asset}..."
curl -fsSL "${base_url}/${asset}" -o "${tmp_dir}/${asset}" \
  || die "failed to download ${asset} — does a release exist for ${goos}/${goarch}?"
curl -fsSL "${base_url}/checksums.txt" -o "${tmp_dir}/checksums.txt" \
  || die "failed to download checksums.txt"

# ── Verify before we trust anything inside the archive ──────────────────────
say "==> Verifying checksum..."
expected="$(grep " ${asset}\$" "${tmp_dir}/checksums.txt" | awk '{print $1}')"
[ -n "$expected" ] || die "no checksum entry for ${asset} — refusing to install an unverifiable binary"

if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "${tmp_dir}/${asset}" | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then
  actual="$(shasum -a 256 "${tmp_dir}/${asset}" | awk '{print $1}')"
else
  die "neither sha256sum nor shasum is available — cannot verify the download"
fi

if [ "$expected" != "$actual" ]; then
  die "checksum mismatch for ${asset}
    expected: ${expected}
    got:      ${actual}
  This could mean the download was corrupted or tampered with in transit.
  Nothing has been installed. Please try again, and if this persists,
  report it: https://github.com/${REPO}/issues"
fi
say "==> Checksum verified"

# ── Extract and install ──────────────────────────────────────────────────────
say "==> Extracting..."
tar -xzf "${tmp_dir}/${asset}" -C "${tmp_dir}"
[ -f "${tmp_dir}/${BIN_NAME}" ] || die "binary not found inside ${asset}"
chmod +x "${tmp_dir}/${BIN_NAME}"

say "==> Installing to ${INSTALL_DIR}/${BIN_NAME}"
if [ -w "$INSTALL_DIR" ] || mkdir -p "$INSTALL_DIR" 2>/dev/null && [ -w "$INSTALL_DIR" ]; then
  mv "${tmp_dir}/${BIN_NAME}" "${INSTALL_DIR}/${BIN_NAME}"
elif command -v sudo >/dev/null 2>&1; then
  say "    ${INSTALL_DIR} isn't writable without elevated privileges — using sudo"
  sudo mkdir -p "$INSTALL_DIR"
  sudo mv "${tmp_dir}/${BIN_NAME}" "${INSTALL_DIR}/${BIN_NAME}"
else
  fallback_dir="${HOME}/.local/bin"
  mkdir -p "$fallback_dir"
  mv "${tmp_dir}/${BIN_NAME}" "${fallback_dir}/${BIN_NAME}"
  INSTALL_DIR="$fallback_dir"
  say ""
  say "⚠️  Couldn't write to /usr/local/bin and sudo isn't available, so"
  say "    dotsync was installed to ${fallback_dir} instead."
  say "    Add it to your PATH by adding this to ~/.bashrc or ~/.zshrc:"
  say ""
  say "      export PATH=\"\$PATH:${fallback_dir}\""
  say ""
fi

say ""
say "✅ dotsync ${tag} installed to ${INSTALL_DIR}/${BIN_NAME}"
"${INSTALL_DIR}/${BIN_NAME}" --version 2>/dev/null || true
say ""
say "Run 'dotsync login' to get started."
