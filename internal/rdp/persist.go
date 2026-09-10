package rdp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/alfaos/alfaos/internal/config"
	"github.com/alfaos/alfaos/internal/logging"
)

// QualityProfileParams returns xRDP/desktop knobs for a named quality preset.
// bulkCompress / bitmapCompress trade bandwidth for encode latency — off on LAN profiles.
func QualityProfileParams(quality string) (bpp int, bulkCompress, bitmapCompress, crypt, light string) {
	bpp = 32
	bulkCompress = "false"
	bitmapCompress = "false"
	crypt = "medium"
	light = "0"
	switch strings.ToLower(strings.TrimSpace(quality)) {
	case "low", "slow":
		bpp = 16
		bulkCompress = "true"
		bitmapCompress = "true"
		crypt = "low"
		light = "1"
	case "medium", "med", "balanced":
		bpp = 24
		bulkCompress = "true"
		bitmapCompress = "true"
		crypt = "medium"
	case "ultra", "max":
		crypt = "low" // less PDU crypto overhead on LAN
	case "high", "lan":
		crypt = "low"
	default:
		// high / unknown → low-latency LAN defaults above
	}
	return bpp, bulkCompress, bitmapCompress, crypt, light
}

// ApplyQualityScriptBody is the guest script that re-applies /etc/alfaos/rdp-quality
// on login / RDP reconnect (wallpaper, light desktop, xrdp.ini).
func ApplyQualityScriptBody() string {
	return `#!/bin/bash
# Persist Alfa Center RDP profile across login / reconnect.
[ -f /etc/alfaos/rdp-quality ] || exit 0
# shellcheck disable=SC1091
. /etc/alfaos/rdp-quality

QUALITY=${QUALITY:-high}
BPP=${BPP:-32}
COMPRESS=${COMPRESS:-false}
BITMAP_COMPRESS=${BITMAP_COMPRESS:-false}
LIGHT=${LIGHT:-0}
Q=$(echo "$QUALITY" | tr 'A-Z' 'a-z')
CRYPT=${CRYPT:-low}
case "$Q" in
  low|slow)
    CRYPT=low
    BPP=${BPP:-16}
    COMPRESS=true
    BITMAP_COMPRESS=true
    LIGHT=${LIGHT:-1}
    ;;
  medium|med|balanced)
    CRYPT=medium
    BPP=${BPP:-24}
    COMPRESS=true
    BITMAP_COMPRESS=true
    ;;
  ultra|max|high|lan)
    CRYPT=low
    BPP=${BPP:-32}
    COMPRESS=false
    BITMAP_COMPRESS=false
    ;;
esac

set_ini() {
  [ -f /etc/xrdp/xrdp.ini ] || return 0
  local key="$1" val="$2"
  if grep -q "^${key}=" /etc/xrdp/xrdp.ini 2>/dev/null; then
    sudo sed -i "s/^${key}=.*/${key}=${val}/" /etc/xrdp/xrdp.ini
  else
    sudo sed -i "/^\[Globals\]/a ${key}=${val}" /etc/xrdp/xrdp.ini
  fi
}

set_ini max_bpp "$BPP"
set_ini bulk_compression "$COMPRESS"
set_ini tcp_nodelay true
set_ini tcp_keepalive true
set_ini crypt_level "$CRYPT"
set_ini use_fastpath both
set_ini new_cursors true
set_ini bitmap_cache true
set_ini bitmap_compression "$BITMAP_COMPRESS"
set_ini pointer_cache_size 32

# Desktop tweaks need an X session (autostart / reconnect).
[ -n "${DISPLAY:-}" ] || exit 0
command -v xfconf-query >/dev/null 2>&1 || exit 0

LITE_WALL=/usr/share/backgrounds/alfaos/alfaoslite.jpg
FULL_WALL=/usr/share/backgrounds/alfaos/alfaos3.png
if [ "$LIGHT" = "1" ]; then
  WALL="$LITE_WALL"
  ICON_STYLE=0
  xfconf-query -c xfwm4 -p /general/use_compositing -s false 2>/dev/null || true
  xfconf-query -c xfwm4 -p /general/workspace_count -s 1 2>/dev/null || true
else
  WALL="$FULL_WALL"
  ICON_STYLE=2
fi
[ -f "$WALL" ] || WALL=""

xfconf-query -c xfce4-desktop -p /desktop-icons/style -s "$ICON_STYLE" 2>/dev/null || \
  xfconf-query -c xfce4-desktop -p /desktop-icons/style -n -t int -s "$ICON_STYLE" 2>/dev/null || true

if [ -n "$WALL" ]; then
  for prop in $(xfconf-query -c xfce4-desktop -l 2>/dev/null | grep '/last-image$' || true); do
    xfconf-query -c xfce4-desktop -p "$prop" -s "$WALL" 2>/dev/null || \
      xfconf-query -c xfce4-desktop -p "$prop" -n -t string -s "$WALL" 2>/dev/null || true
  done
  for prop in $(xfconf-query -c xfce4-desktop -l 2>/dev/null | grep '/image-style$' || true); do
    xfconf-query -c xfce4-desktop -p "$prop" -s 5 2>/dev/null || true
  done
  xfdesktop --reload 2>/dev/null || true
fi
`
}

// InstallQualityHooksBash installs persistence scripts + session hooks on the guest.
// Safe to re-run (idempotent).
func InstallQualityHooksBash(seedQuality string, width, height int) string {
	if width <= 0 {
		width = 1920
	}
	if height <= 0 {
		height = 1080
	}
	q := strings.ToLower(strings.TrimSpace(seedQuality))
	if q == "" {
		q = "low"
	}
	bpp, compress, bitmapCompress, _, light := QualityProfileParams(q)

	return fmt.Sprintf(`set -euo pipefail
sudo mkdir -p /etc/alfaos /home/alfaos/.local/bin /home/alfaos/.config/autostart

# Seed profile file if missing (keep user Apply choice if present)
if [ ! -f /etc/alfaos/rdp-quality ]; then
  sudo tee /etc/alfaos/rdp-quality >/dev/null << 'QCFG'
QUALITY=%s
PROFILE=%s
BPP=%d
COMPRESS=%s
BITMAP_COMPRESS=%s
LIGHT=%s
W=%d
H=%d
QCFG
fi
if [ ! -f /etc/alfaos/rdp-resolution ]; then
  sudo tee /etc/alfaos/rdp-resolution >/dev/null << 'RESCFG'
W=%d
H=%d
RESCFG
fi

cat > /home/alfaos/.local/bin/alfaos-apply-quality.sh << 'QSCRIPT'
%s
QSCRIPT
chmod +x /home/alfaos/.local/bin/alfaos-apply-quality.sh
sudo chown alfaos:alfaos /home/alfaos/.local/bin/alfaos-apply-quality.sh

# Autostart after desktop apply — restores Slow link wallpaper etc.
cat > /home/alfaos/.config/autostart/alfaos-quality-apply.desktop << 'AUTO'
[Desktop Entry]
Type=Application
Name=ALFAOS Quality Persist
Exec=/home/alfaos/.local/bin/alfaos-apply-quality.sh
X-GNOME-Autostart-enabled=true
X-GNOME-Autostart-Delay=5
OnlyShowIn=XFCE;
AUTO
sudo chown -R alfaos:alfaos /home/alfaos/.config/autostart

# Always run resolution + quality (do not short-circuit with ||)
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
/home/alfaos/.local/bin/alfaos-apply-quality.sh >/tmp/alfaos-quality.log 2>&1 || true
exec startxfce4
STARTWM
sudo chmod +x /etc/xrdp/startwm.sh

cat | sudo tee /etc/xrdp/reconnectwm.sh > /dev/null << 'RECONNECT'
#!/bin/sh
/home/alfaos/.local/bin/alfaos-apply-quality.sh >/tmp/alfaos-quality.log 2>&1 || true
/home/alfaos/.local/bin/alfaos-set-resolution.sh >/tmp/alfaos-resolution.log 2>&1 || true
RECONNECT
sudo chmod +x /etc/xrdp/reconnectwm.sh

# Make desktop apply honor LIGHT (and call quality script at end)
if [ -f /home/alfaos/.local/bin/alfaos-apply-desktop.sh ]; then
  if ! grep -q 'alfaos-apply-quality.sh' /home/alfaos/.local/bin/alfaos-apply-desktop.sh; then
    printf '\n# Persist Alfa Center link profile\n[ -x /home/alfaos/.local/bin/alfaos-apply-quality.sh ] && /home/alfaos/.local/bin/alfaos-apply-quality.sh >/tmp/alfaos-quality.log 2>&1 || true\n' \
      >> /home/alfaos/.local/bin/alfaos-apply-desktop.sh
  fi
fi

echo QUALITY_HOOKS_OK
`, q, q, bpp, compress, bitmapCompress, light, width, height, width, height, ApplyQualityScriptBody())
}

// InstallQualityHooks pushes persistence hooks to a running guest.
func (r *Configurator) InstallQualityHooks(ip string) error {
	logging.Info("Installing RDP quality persistence hooks on VM...")
	q := r.cfg.RDPQualityName()
	if q == "" {
		q = "low"
	}
	body := InstallQualityHooksBash(q, r.cfg.RDP.Width, r.cfg.RDP.Height)
	local := filepath.Join(r.cfg.Paths.StateDir, "rdp-quality-hooks.sh")
	if err := os.WriteFile(local, []byte("#!/bin/bash\n"+body), 0755); err != nil {
		return fmt.Errorf("write quality hooks: %w", err)
	}
	remote := "/tmp/alfaos-rdp-quality-hooks.sh"
	if err := r.vm.CopyFile(ip, local, remote); err != nil {
		return err
	}
	out, err := r.vm.RunSSH(ip, "chmod +x "+remote+" && bash "+remote)
	if err != nil {
		return fmt.Errorf("quality hooks: %w\n%s", err, out)
	}
	logging.Success("RDP quality settings will persist across reconnect/reboot")
	return nil
}

// Ensure guest quality file matches host config (used after Apply / center-install).
func SeedQualityFileBash(cfg *config.Config) string {
	q := cfg.RDPQualityName()
	if q == "" {
		q = "low"
	}
	w, h := cfg.RDP.Width, cfg.RDP.Height
	if w <= 0 {
		w = 1920
	}
	if h <= 0 {
		h = 1080
	}
	bpp, compress, bitmapCompress, _, light := QualityProfileParams(q)
	return fmt.Sprintf(`sudo mkdir -p /etc/alfaos
sudo tee /etc/alfaos/rdp-quality >/dev/null << EOF
QUALITY=%s
PROFILE=%s
BPP=%d
COMPRESS=%s
BITMAP_COMPRESS=%s
LIGHT=%s
W=%d
H=%d
EOF
sudo tee /etc/alfaos/rdp-resolution >/dev/null << EOF
W=%d
H=%d
EOF
`, q, q, bpp, compress, bitmapCompress, light, w, h, w, h)
}
