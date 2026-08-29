#!/usr/bin/env bash
# Publish ALFAOS to GitHub (run once after: gh auth login)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

gh auth status

REPO_SLUG="${ALFAOS_GH_REPO:-hdmain/alfaos}"
DESC="Automated KVM/Debian/XFCE/RDP installer — ALFAOS"

if git remote get-url origin >/dev/null 2>&1; then
  echo "==> Remote origin already set, pushing..."
  git push -u origin main
  gh repo view "$REPO_SLUG" --web 2>/dev/null || true
  exit 0
fi

echo "==> Creating public GitHub repo: $REPO_SLUG"
if gh repo create "$REPO_SLUG" --public --source=. --remote=origin --push --description "$DESC"; then
  echo ""
  echo "Published: https://github.com/$REPO_SLUG"
  echo ""
  echo "One-line install:"
  echo "  curl -fsSL https://raw.githubusercontent.com/$REPO_SLUG/main/scripts/install.sh | sudo bash"
  exit 0
fi

USER="$(gh api user -q .login)"
FALLBACK="$USER/alfaos"
echo "==> Could not create $REPO_SLUG (org may not exist). Trying $FALLBACK ..."
gh repo create "$FALLBACK" --public --source=. --remote=origin --push --description "$DESC"
echo ""
echo "Published: https://github.com/$FALLBACK"
echo "Update ALFAOS_REPO if you use a fork:"
echo "  export ALFAOS_REPO=https://github.com/$FALLBACK.git"
