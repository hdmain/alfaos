#!/usr/bin/env bash
# ALFAOS one-line installer — https://github.com/hdmain/alfaos
set -euo pipefail

REPO="${ALFAOS_REPO:-https://github.com/hdmain/alfaos.git}"
BRANCH="${ALFAOS_BRANCH:-main}"
INSTALL_DIR="${ALFAOS_INSTALL_DIR:-/tmp/alfaos-src-$$}"
RUN_FULL=false

usage() {
  cat <<EOF
ALFAOS installer

Usage:
  curl -fsSL https://raw.githubusercontent.com/hdmain/alfaos/main/scripts/install.sh | sudo bash
  curl -fsSL ... | sudo bash -s -- --full

Options:
  --full, --install   Build CLI and run: alfaos install
  --cli-only          Build CLI only (default)
  -h, --help          Show this help
EOF
}

for arg in "$@"; do
  case "$arg" in
    --full|--install) RUN_FULL=true ;;
    --cli-only) RUN_FULL=false ;;
    -h|--help) usage; exit 0 ;;
  esac
done

if [ "$(id -u)" -ne 0 ]; then
  if command -v sudo >/dev/null 2>&1; then
    exec sudo env ALFAOS_REPO="$REPO" ALFAOS_BRANCH="$BRANCH" bash -c \
      "$(curl -fsSL "${REPO%.git}/raw/${BRANCH}/scripts/install.sh")" "$@"
  fi
  echo "error: run as root or with sudo" >&2
  exit 1
fi

need() {
  command -v "$1" >/dev/null 2>&1
}

install_build_deps() {
  if need git && need go; then
    return
  fi
  echo "==> Installing build dependencies (git, golang)..."
  if need apt-get; then
    apt-get update -qq
    apt-get install -y -qq git golang-go curl ca-certificates
  elif need dnf; then
    dnf install -y git golang curl ca-certificates
  elif need pacman; then
    pacman -Sy --noconfirm git go curl ca-certificates
  else
    echo "error: install git and go, then re-run" >&2
    exit 1
  fi
}

echo "==> ALFAOS one-line installer"
echo "    repo:   $REPO"
echo "    branch: $BRANCH"

install_build_deps

rm -rf "$INSTALL_DIR"
git clone --depth 1 --branch "$BRANCH" "$REPO" "$INSTALL_DIR"

cd "$INSTALL_DIR"
export DEBIAN_FRONTEND=noninteractive
./scripts/build.sh

if [ "$RUN_FULL" = true ]; then
  echo ""
  echo "==> Running full ALFAOS installation (KVM VM + desktop + RDP)..."
  alfaos install
else
  echo ""
  echo "ALFAOS CLI installed. Next step:"
  echo "  sudo alfaos install"
fi

rm -rf "$INSTALL_DIR"
