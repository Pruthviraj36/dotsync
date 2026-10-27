#!/usr/bin/env bash
# scripts/build.sh — build the DotSync server binary and CLI for all platforms
set -euo pipefail

VERSION="${VERSION:-v0.1.0-beta}"
LDFLAGS="-s -w -X github.com/Pruthviraj36/dotsync/cli/cmd.Version=${VERSION}"

echo "🔨 Building DotSync ${VERSION}"
echo ""

mkdir -p dist/tmp

# ── Server ────────────────────────────────────────────────────────────────────
echo "→ Server binary (linux/amd64)..."
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="${LDFLAGS}" -o dist/dotsync-server-linux-amd64 ./cmd/dotsync

echo "→ Server binary (linux/arm64)..."
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="${LDFLAGS}" -o dist/dotsync-server-linux-arm64 ./cmd/dotsync

echo "→ Server binary (current platform)..."
CGO_ENABLED=0 go build -ldflags="${LDFLAGS}" -o dist/dotsync-server ./cmd/dotsync

# ── CLI ───────────────────────────────────────────────────────────────────────
echo ""
build_unix() {
  local goos="$1" goarch="$2"
  echo "→ CLI (${goos}/${goarch})..."
  GOOS="${goos}" GOARCH="${goarch}" CGO_ENABLED=0 go build -ldflags="${LDFLAGS}" -o dist/tmp/dotsync .
  tar -czf "dist/dotsync-${goos}-${goarch}.tar.gz" -C dist/tmp dotsync
  rm dist/tmp/dotsync
}

build_windows() {
  local goarch="$1"
  echo "→ CLI (windows/${goarch})..."
  GOOS=windows GOARCH="${goarch}" CGO_ENABLED=0 go build -ldflags="${LDFLAGS}" -o dist/tmp/dotsync.exe .
  # -j strips directory names, -q is quiet; store the exe at the archive root.
  ( cd dist/tmp && zip -jq "../dotsync-windows-${goarch}.zip" dotsync.exe )
  rm dist/tmp/dotsync.exe
}

build_unix linux amd64
build_unix linux arm64
build_unix darwin amd64
build_unix darwin arm64
build_windows amd64
build_windows arm64

rmdir dist/tmp

echo ""
echo "→ Generating checksums.txt..."
if command -v sha256sum >/dev/null 2>&1; then
  ( cd dist && sha256sum dotsync-* > checksums.txt )
elif command -v shasum >/dev/null 2>&1; then
  ( cd dist && shasum -a 256 dotsync-* > checksums.txt )
else
  echo "ERROR: neither sha256sum nor shasum is available" >&2
  exit 1
fi

echo ""
echo "✅ Build complete. Artifacts in ./dist/"
ls -lh dist/
