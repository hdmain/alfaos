#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

echo "==> Building ALFAOS installer..."
go mod tidy
CGO_ENABLED=0 go build -ldflags="-s -w" -o build/alfaos ./cmd/alfaos/

echo "==> Installing to /usr/local/bin/alfaos..."
sudo install -m 755 build/alfaos /usr/local/bin/alfaos

echo "==> Installing assets and config..."
sudo mkdir -p /usr/share/alfaos/assets /etc/alfaos
sudo cp -r assets/* /usr/share/alfaos/assets/ 2>/dev/null || true
sudo cp configs/default.yaml /etc/alfaos/config.yaml 2>/dev/null || true

echo "==> Creating /alfaos symlink..."
sudo ln -sf /usr/local/bin/alfaos /alfaos

echo ""
echo "ALFAOS installer ready. Run:"
echo "  sudo /alfaos install"
