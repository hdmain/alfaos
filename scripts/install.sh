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
  ALFAOS_BRANCH   Git branch for config/assets (default: main)
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
  local missing=()
  need curl || missing+=(curl)
  [ "${#missing[@]}" -eq 0 ] && return 0

  echo "==> Installing: ${missing[*]}..."
  if need apt-get; then
    # Skip apt-get update — broken third-party repos (e.g. Docker) must not block install
    apt-get install -y -qq --no-install-recommends ca-certificates "${missing[@]}" || {
      echo "error: could not install ${missing[*]} (fix apt sources or install manually)" >&2
      exit 1
    }
  elif need dnf; then
    dnf install -y ca-certificates "${missing[@]}"
  elif need pacman; then
    pacman -Sy --noconfirm ca-certificates "${missing[@]}"
  else
    echo "error: install ${missing[*]}, then re-run" >&2
    exit 1
  fi
}

ensure_unzip() {
  need unzip && return 0
  echo "==> Installing: unzip..."
  if need apt-get; then
    apt-get install -y -qq --no-install-recommends unzip || {
      echo "error: install unzip manually: apt-get install -y unzip" >&2
      exit 1
    }
  elif need dnf; then
    dnf install -y unzip
  elif need pacman; then
    pacman -Sy --noconfirm unzip
  else
    echo "error: unzip required for fallback download" >&2
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

github_user() {
  echo "${GITHUB_REPO%%/*}"
}

github_repo_name() {
  echo "${GITHUB_REPO#*/}"
}

install_binary_from_pages() {
  local arch bin url tmp owner repo sha
  arch="$(detect_arch)"
  bin="alfaos-linux-${arch}"
  owner="$(github_user)"
  repo="$(github_repo_name)"
  sha="$(curl -fsSL "https://api.github.com/repos/${GITHUB_REPO}/commits/${BRANCH}" | sed -n 's/.*"sha": "\([a-f0-9]*\)".*/\1/p' | head -1)"
  url="https://${owner}.github.io/${repo}/${bin}"
  [ -n "$sha" ] && url="${url}?v=${sha}"
  tmp="$(mktemp)"

  echo "==> Downloading ${bin} from GitHub Pages (${sha:-latest})..."
  curl -fsSL "$url" -o "$tmp"
  install -m 755 "$tmp" /usr/local/bin/alfaos
  rm -f "$tmp"
  ln -sf /usr/local/bin/alfaos /alfaos
}

install_binary_from_actions() {
  local arch bin artifact url tmpdir
  ensure_unzip
  arch="$(detect_arch)"
  bin="alfaos-linux-${arch}"
  artifact="${bin}"
  url="https://nightly.link/${GITHUB_REPO}/workflows/build.yml/refs/heads/${BRANCH}/${artifact}.zip"
  tmpdir="$(mktemp -d)"

  echo "==> Downloading ${bin} from GitHub Actions..."
  curl -fsSL "$url" -o "${tmpdir}/artifact.zip"
  unzip -q "${tmpdir}/artifact.zip" -d "${tmpdir}"

  if [ -f "${tmpdir}/${bin}" ]; then
    install -m 755 "${tmpdir}/${bin}" /usr/local/bin/alfaos
  else
    echo "error: ${bin} not found in workflow artifact" >&2
    exit 1
  fi
  rm -rf "$tmpdir"
  ln -sf /usr/local/bin/alfaos /alfaos
}

install_binary() {
  if install_binary_from_pages; then
    return 0
  fi
  echo "==> GitHub Pages failed, trying Actions artifact..."
  install_binary_from_actions
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
