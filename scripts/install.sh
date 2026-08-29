#!/usr/bin/env bash
# ALFAOS one-line installer — https://github.com/hdmain/alfaos
set -euo pipefail

GITHUB_REPO="${ALFAOS_GITHUB:-hdmain/alfaos}"
BRANCH="${ALFAOS_BRANCH:-main}"
RUN_FULL=false

usage() {
  cat <<EOF
ALFAOS installer

Usage:
  curl -fsSL https://raw.githubusercontent.com/hdmain/alfaos/main/scripts/install.sh | sudo bash
  curl -fsSL ... | sudo bash -s -- --full

Options:
  --full, --install   Install CLI and run: alfaos install
  --cli-only          Install CLI only (default)
  -h, --help          Show this help

Environment:
  ALFAOS_GITHUB   GitHub repo (default: hdmain/alfaos)
  ALFAOS_BRANCH   Branch with dist/ binaries (default: main)
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
    exec sudo env ALFAOS_GITHUB="$GITHUB_REPO" ALFAOS_BRANCH="$BRANCH" bash -c \
      "$(curl -fsSL "https://raw.githubusercontent.com/${GITHUB_REPO}/${BRANCH}/scripts/install.sh")" "$@"
  fi
  echo "error: run as root or with sudo" >&2
  exit 1
fi

need() {
  command -v "$1" >/dev/null 2>&1
}

install_deps() {
  if need curl; then
    return
  fi
  echo "==> Installing curl..."
  if need apt-get; then
    apt-get update -qq
    apt-get install -y -qq curl ca-certificates
  elif need dnf; then
    dnf install -y curl ca-certificates
  elif need pacman; then
    pacman -Sy --noconfirm curl ca-certificates
  else
    echo "error: install curl, then re-run" >&2
    exit 1
  fi
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64) echo amd64 ;;
    aarch64|arm64) echo arm64 ;;
    *)
      echo "error: unsupported architecture: $(uname -m)" >&2
      exit 1
      ;;
  esac
}

install_binary() {
  local arch bin url tmp
  arch="$(detect_arch)"
  bin="alfaos-linux-${arch}"
  url="https://raw.githubusercontent.com/${GITHUB_REPO}/${BRANCH}/dist/${bin}"
  tmp="$(mktemp)"

  echo "==> Downloading ${bin}..."
  if ! curl -fsSL "$url" -o "$tmp"; then
    echo "error: failed to download ${url}" >&2
    exit 1
  fi

  install -m 755 "$tmp" /usr/local/bin/alfaos
  rm -f "$tmp"
  ln -sf /usr/local/bin/alfaos /alfaos
}

install_config() {
  local raw_base="https://raw.githubusercontent.com/${GITHUB_REPO}/${BRANCH}"
  mkdir -p /etc/alfaos /usr/share/alfaos/assets
  curl -fsSL "${raw_base}/configs/default.yaml" -o /etc/alfaos/config.yaml
  curl -fsSL "${raw_base}/assets/alfa1.jpeg" -o /usr/share/alfaos/assets/alfa1.jpeg 2>/dev/null || true
  curl -fsSL "${raw_base}/assets/alfa2.jpeg" -o /usr/share/alfaos/assets/alfa2.jpeg 2>/dev/null || true
}

echo "==> ALFAOS one-line installer"
echo "    github: ${GITHUB_REPO}"
echo "    branch: ${BRANCH}"

install_deps
install_binary
install_config

if [ "$RUN_FULL" = true ]; then
  echo ""
  echo "==> Running full ALFAOS installation (KVM VM + desktop + RDP)..."
  alfaos install
else
  echo ""
  echo "ALFAOS CLI installed."
  echo "  alfaos version: $(alfaos version)"
  echo "  next step:      sudo alfaos install"
fi
