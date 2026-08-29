#!/usr/bin/env bash
# Build a .deb package for GUI installation (Software Center, GDebi, double-click).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

VERSION="${ALFAOS_VERSION:-1.0.0}"
REVISION="${ALFAOS_REVISION:-1}"
ARCH="${ALFAOS_ARCH:-amd64}"
PKG_NAME="alfaos_${VERSION}-${REVISION}_${ARCH}"
STAGE="$ROOT/build/deb/$PKG_NAME"
OUT="$ROOT/build/${PKG_NAME}.deb"

need() {
    command -v "$1" >/dev/null 2>&1 || {
        echo "Missing required tool: $1" >&2
        exit 1
    }
}

need go
need dpkg-deb
need install
need du

echo "==> Building ALFAOS binary..."
go mod tidy
CGO_ENABLED=0 go build -ldflags="-s -w" -o build/alfaos ./cmd/alfaos/

echo "==> Staging Debian package..."
rm -rf "$STAGE"
mkdir -p "$STAGE/DEBIAN"
mkdir -p "$STAGE/usr/bin"
mkdir -p "$STAGE/etc/alfaos"
mkdir -p "$STAGE/usr/share/applications"
mkdir -p "$STAGE/usr/share/doc/alfaos"

install -m 755 build/alfaos "$STAGE/usr/bin/alfaos"
install -m 644 configs/default.yaml "$STAGE/etc/alfaos/config.yaml"
install -m 644 debian/alfaos.desktop "$STAGE/usr/share/applications/alfaos.desktop"
install -m 644 debian/copyright "$STAGE/usr/share/doc/alfaos/copyright"
gzip -9 -c debian/changelog > "$STAGE/usr/share/doc/alfaos/changelog.gz"

for script in postinst prerm; do
    install -m 755 "debian/$script" "$STAGE/DEBIAN/$script"
done
install -m 644 debian/conffiles "$STAGE/DEBIAN/conffiles"

INSTALLED_SIZE="$(du -sk "$STAGE" | awk '{print $1}')"
cat > "$STAGE/DEBIAN/control" <<EOF
Package: alfaos
Version: ${VERSION}-${REVISION}
Section: admin
Priority: optional
Architecture: ${ARCH}
Maintainer: ALFAOS <alfaos@localhost>
Installed-Size: ${INSTALLED_SIZE}
Depends: sudo, policykit-1
Recommends: libvirt-clients, libvirt-daemon-system, qemu-kvm, virtinst, sshpass, freerdp2-x11 | rdesktop, wget, curl, openssh-client, genisoimage, bridge-utils, gdebi-core
Description: Automated Linux Framework for Alpha OS
 ALFAOS installs and configures a Debian XFCE desktop in a KVM/libvirt VM
 with xRDP remote access, custom theming, and automated verification.
EOF

echo "==> Building $OUT ..."
mkdir -p "$(dirname "$OUT")"
dpkg-deb --root-owner-group --build "$STAGE" "$OUT"

echo ""
echo "Package ready:"
echo "  $OUT"
echo ""
echo "Install without terminal:"
echo "  • Double-click the .deb file (Software Center / GDebi)"
echo "  • Or: sudo apt install ./build/${PKG_NAME}.deb"
echo ""
echo "Then launch 'ALFAOS Install' from the application menu,"
echo "or run: sudo alfaos install"
