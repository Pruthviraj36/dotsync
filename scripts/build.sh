#!/usr/bin/env bash
# scripts/build.sh — build the DotSync server binary and CLI for all platforms
set -euo pipefail

VERSION="${VERSION:-v0.1.0-beta}"
LDFLAGS="-s -w -X main.Version=${VERSION}"

echo "🔨 Building DotSync ${VERSION}"
echo ""

# ── Server ────────────────────────────────────────────────────────────────────
echo "→ Server binary (linux/amd64)..."
GOOS=linux GOARCH=amd64 go build -ldflags="${LDFLAGS}" -o dist/dotsync-server-linux-amd64 ./cmd/dotsync

echo "→ Server binary (current platform)..."
go build -ldflags="${LDFLAGS}" -o dist/dotsync-server ./cmd/dotsync

# ── CLI ───────────────────────────────────────────────────────────────────────
echo ""
echo "→ CLI (linux/amd64)..."
GOOS=linux GOARCH=amd64 go build -ldflags="${LDFLAGS}" -o dist/dotsync-linux-amd64 ./cli

echo "→ CLI (darwin/amd64)..."
GOOS=darwin GOARCH=amd64 go build -ldflags="${LDFLAGS}" -o dist/dotsync-darwin-amd64 ./cli

echo "→ CLI (darwin/arm64 — Apple Silicon)..."
GOOS=darwin GOARCH=arm64 go build -ldflags="${LDFLAGS}" -o dist/dotsync-darwin-arm64 ./cli

echo "→ CLI (windows/amd64)..."
GOOS=windows GOARCH=amd64 go build -ldflags="${LDFLAGS}" -o dist/dotsync-windows-amd64.exe ./cli

echo ""
echo "✅ Build complete. Artifacts in ./dist/"
ls -lh dist/
