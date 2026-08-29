package rdp

import (
	"fmt"
	"os"
	"strings"

	"github.com/alfaos/alfaos/internal/config"
	"github.com/alfaos/alfaos/internal/logging"
	"github.com/alfaos/alfaos/internal/virtualization"
)

type Configurator struct {
	cfg *config.Config
	vm  *virtualization.Manager
}

func New(cfg *config.Config, vm *virtualization.Manager) *Configurator {
	return &Configurator{cfg: cfg, vm: vm}
}

func (r *Configurator) InstallScript() string {
	width := r.cfg.RDP.Width
	height := r.cfg.RDP.Height
	port := r.cfg.RDP.Port
	if width <= 0 {
		width = 1920
	}
	if height <= 0 {
		height = 1080
	}
	if port <= 0 {
		port = 3389
	}

	return fmt.Sprintf(`#!/bin/bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive

echo "==> Installing xRDP..."
sudo apt-get update -qq
sudo apt-get install -y -qq xrdp xorgxrdp x11-xserver-utils

echo "==> Configuring xRDP for XFCE..."
echo "xfce4-session" > /home/alfaos/.xsession
chmod +x /home/alfaos/.xsession
sudo chown alfaos:alfaos /home/alfaos/.xsession

sudo adduser xrdp ssl-cert 2>/dev/null || true

sudo mkdir -p /etc/alfaos
cat | sudo tee /etc/alfaos/rdp-resolution > /dev/null << 'RESCFG'
W=%d
H=%d
RESCFG

mkdir -p /home/alfaos/.local/bin
cat > /home/alfaos/.local/bin/alfaos-set-resolution.sh << 'RESSCRIPT'
#!/bin/bash
[ -f /etc/alfaos/rdp-resolution ] && . /etc/alfaos/rdp-resolution
W=${W:-1920}
H=${H:-1080}

for _ in $(seq 1 20); do
  OUTPUT=$(xrandr 2>/dev/null | awk '/ connected/{print $1; exit}')
  [ -n "$OUTPUT" ] && break
  sleep 0.5
done
[ -n "$OUTPUT" ] || exit 0

set_mode() {
  xrandr --output "$OUTPUT" --mode "$1" 2>/dev/null
}

if set_mode "${W}x${H}"; then exit 0; fi

while IFS= read -r mode; do
  set_mode "$mode" && exit 0
done < <(xrandr 2>/dev/null | awk -v w="$W" -v h="$H" '$0 ~ w"x"h {print $1}')

if command -v cvt >/dev/null 2>&1; then
  MODELINE=$(cvt "$W" "$H" 60 2>/dev/null | awk '/Modeline/{sub(/^Modeline /,""); print}')
  [ -n "$MODELINE" ] || exit 0
  MODE=$(echo "$MODELINE" | awk '{print $1}' | tr -d '"')
  xrandr --newmode $MODELINE 2>/dev/null || true
  xrandr --addmode "$OUTPUT" "$MODE" 2>/dev/null || true
  set_mode "$MODE" || true
fi
RESSCRIPT
chmod +x /home/alfaos/.local/bin/alfaos-set-resolution.sh
sudo chown alfaos:alfaos /home/alfaos/.local/bin/alfaos-set-resolution.sh

# rdesktop does not support drdynvc — keep it disabled for compatibility
sudo sed -i 's/^drdynvc=.*/drdynvc=false/' /etc/xrdp/xrdp.ini 2>/dev/null || true

# Reuse existing user session instead of spawning broken parallel displays
sudo sed -i 's/^Policy=.*/Policy=UBD/' /etc/xrdp/sesman.ini 2>/dev/null || true
sudo sed -i 's/^KillDisconnected=.*/KillDisconnected=true/' /etc/xrdp/sesman.ini 2>/dev/null || true
sudo sed -i 's/^DisconnectedTimeLimit=.*/DisconnectedTimeLimit=60/' /etc/xrdp/sesman.ini 2>/dev/null || true

cat | sudo tee /etc/xrdp/startwm.sh > /dev/null << 'STARTWM'
#!/bin/sh
if [ -r /etc/default/locale ]; then
  . /etc/default/locale
  export LANG LANGUAGE
fi
if [ -r /etc/profile ]; then
  . /etc/profile
fi
unset DBUS_SESSION_BUS_ADDRESS
unset XDG_RUNTIME_DIR
/home/alfaos/.local/bin/alfaos-set-resolution.sh >/tmp/alfaos-resolution.log 2>&1 || true
exec startxfce4
STARTWM
sudo chmod +x /etc/xrdp/startwm.sh

cat | sudo tee /etc/xrdp/reconnectwm.sh > /dev/null << 'RECONNECT'
#!/bin/sh
/home/alfaos/.local/bin/alfaos-set-resolution.sh >/tmp/alfaos-resolution.log 2>&1 || true
RECONNECT
sudo chmod +x /etc/xrdp/reconnectwm.sh

echo "==> Configuring firewall for RDP port %d..."
if command -v ufw >/dev/null 2>&1; then
    sudo ufw allow %d/tcp 2>/dev/null || true
fi

echo "==> Enabling and starting xRDP..."
sudo systemctl enable xrdp
sudo systemctl restart xrdp

echo "==> RDP configured (default resolution %dx%d — run: alfaos connect)."
`, width, height, port, port, width, height)
}

func (r *Configurator) Install(ip string) error {
	logging.Info("Installing and configuring RDP server on VM...")

	localScript := r.cfg.Paths.StateDir + "/rdp-setup.sh"
	if err := os.WriteFile(localScript, []byte(r.InstallScript()), 0755); err != nil {
		return fmt.Errorf("write rdp script: %w", err)
	}

	remoteScript := "/tmp/alfaos-rdp-setup.sh"
	if err := r.vm.CopyFile(ip, localScript, remoteScript); err != nil {
		return fmt.Errorf("copy rdp script: %w", err)
	}

	out, err := r.vm.RunSSH(ip, "chmod +x "+remoteScript+" && bash "+remoteScript)
	if err != nil {
		return fmt.Errorf("rdp install failed: %w\n%s", err, out)
	}

	logging.Success("RDP server installed and configured")
	return nil
}

func (r *Configurator) IsServiceRunning(ip string) bool {
	out, err := r.vm.RunSSH(ip, "systemctl is-active xrdp")
	return err == nil && strings.TrimSpace(out) == "active"
}

func (r *Configurator) IsPortListening(ip string) bool {
	out, err := r.vm.RunSSH(ip, fmt.Sprintf("ss -tln | grep ':%d '", r.cfg.RDP.Port))
	return err == nil && len(out) > 0
}
