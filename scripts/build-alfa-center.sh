#!/usr/bin/env bash
# Build Alfa Center (Rust/egui) for Linux amd64 and copy to dist/.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CRATE="$ROOT/guest/alfa-center"
DIST="$CRATE/dist"

cd "$CRATE"
if ! command -v cargo >/dev/null 2>&1; then
  echo "cargo not found — install Rust: https://rustup.rs" >&2
  exit 1
fi

echo "==> Building alfa-center (release)..."
cargo build --release
mkdir -p "$DIST"
install -m 755 "$CRATE/target/release/alfa-center" "$DIST/alfa-center"
# Also stage for host guestsetup when installed from /usr
if [ -d /var/lib/alfaos/state ]; then
  install -m 755 "$DIST/alfa-center" /var/lib/alfaos/state/alfa-center || true
fi
echo "==> Done: $DIST/alfa-center"
ls -lh "$DIST/alfa-center"
