#!/usr/bin/env bash
# Rebuild dist/ binaries — run before pushing code changes.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

VERSION="$(git rev-parse --short HEAD 2>/dev/null || echo dev)"
LDFLAGS="-s -w -X main.version=${VERSION}"

mkdir -p dist
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -ldflags="${LDFLAGS}" -o dist/alfaos-linux-amd64 ./cmd/alfaos/
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go build -ldflags="${LDFLAGS}" -o dist/alfaos-linux-arm64 ./cmd/alfaos/
sha256sum dist/alfaos-linux-* > dist/SHA256SUMS
ls -lh dist/alfaos-linux-* dist/SHA256SUMS
