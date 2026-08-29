package desktop

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

func (d *Configurator) InstallScript() string {
	theme := d.cfg.ALFAOS.Theme
	gtkTheme, wmTheme := themeNames(theme)
	iconTheme := d.cfg.ALFAOS.Icons
	if iconTheme == "" {
		iconTheme = "Papirus-Dark"
	}
	wallpaper := d.cfg.ALFAOS.Wallpaper
	if wallpaper == "" {
		wallpaper = "alfa2.jpeg"
	}
	wallPath := "/usr/share/backgrounds/alfaos/" + wallpaper
	themePackages := themePackages(theme)
	terminalPkg := terminalPackage(d.cfg.ALFAOS.Terminal)

	tilixConfig := ""
	if strings.ToLower(d.cfg.ALFAOS.Terminal) != "xfce4-terminal" && d.cfg.ALFAOS.Terminal != "xfce" {
		tilixConfig = `
mkdir -p /home/alfaos/.config/tilix
cat > /home/alfaos/.config/tilix/tilix.dconf << 'TILIX'
[com/gexperts/Tilix/Settings]
theme-variant='dark'
TILIX`
	}

	browserInstall := `echo "==> Installing Firefox..."
sudo apt-get install -y -qq firefox-esr || sudo apt-get install -y -qq firefox`
	if !d.cfg.ALFAOS.Browser {
		browserInstall = ""
	}

	plankInstall := ""
	plankConfig := ""
	panelConfig := panelXML(false)
	if d.cfg.ALFAOS.Plank {
		plankInstall = "plank"
		panelConfig = panelXML(true)
		plankConfig = `
echo "==> Configuring Plank dock..."
mkdir -p /home/alfaos/.config/plank/dock1/launchers
mkdir -p /home/alfaos/.local/share/applications

# Single Firefox launcher — avoid duplicate dock icon (WM_CLASS mismatch)
if [ -f /usr/share/applications/firefox-esr.desktop ]; then
  grep -v '^StartupWMClass=' /usr/share/applications/firefox-esr.desktop \
    > /home/alfaos/.local/share/applications/firefox-esr.desktop
  echo 'StartupWMClass=firefox-esr' >> /home/alfaos/.local/share/applications/firefox-esr.desktop
fi

cat > /home/alfaos/.config/plank/dock1/settings << 'PLANKCFG'
[PlankDockPreferences]
Theme=Gtk+
IconSize=32
HideMode=0
Position=3
Alignment=0
ZoomEnabled=true
PLANKCFG

cat > /home/alfaos/.config/plank/dock1/launchers/firefox-esr.dockitem << 'ITEM'
[PlankDockItem]
Launcher=file:///home/alfaos/.local/share/applications/firefox-esr.desktop
ITEM

cat > /home/alfaos/.config/plank/dock1/launchers/tilix.dockitem << 'ITEM'
[PlankDockItem]
Launcher=file:///usr/share/applications/com.gexperts.Tilix.desktop
ITEM

cat > /home/alfaos/.config/plank/dock1/launchers/thunar.dockitem << 'ITEM'
[PlankDockItem]
Launcher=file:///usr/share/applications/thunar.desktop
ITEM

cat > /home/alfaos/.config/autostart/plank.desktop << 'PLANK'
[Desktop Entry]
Type=Application
Name=Plank
Exec=plank
Hidden=false
X-GNOME-Autostart-enabled=true
PLANK
`
	}

	return fmt.Sprintf(`#!/bin/bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive

echo "==> Updating package lists..."
sudo apt-get update -qq

echo "==> Installing XFCE desktop environment (minimal)..."
sudo apt-get install -y -qq \
    xfce4 xfce4-whiskermenu-plugin \
    %s \
    papirus-icon-theme \
    %s \
    %s \
    mousepad galculator btop gdebi \
    lightdm \
    dbus-x11 \
    net-tools

%s

echo "==> Configuring .deb installer (GDebi)..."
mkdir -p /home/alfaos/.config
cat > /home/alfaos/.config/mimeapps.list << 'MIME'
[Default Applications]
application/vnd.debian.binary-package=gdebi.desktop
MIME
sudo -u alfaos xdg-mime default gdebi.desktop application/vnd.debian.binary-package 2>/dev/null || true

echo "==> Removing XFCE bloat (goodies meta-package and unused plugins)..."
sudo apt-get purge -y -qq \
    xfce4-goodies \
    xfburn ristretto \
    xfce4-notes xfce4-notes-plugin xfce4-dict \
    xfce4-mailwatch-plugin xfce4-weather-plugin xfce4-wavelan-plugin \
    xfce4-timer-plugin xfce4-battery-plugin xfce4-fsguard-plugin \
    xfce4-genmon-plugin xfce4-smartbookmark-plugin xfce4-netload-plugin \
    xfce4-systemload-plugin xfce4-cpugraph-plugin xfce4-diskperf-plugin \
    xfce4-sensors-plugin xfce4-verve-plugin xfce4-screenshooter \
    xfce4-taskmanager thunar-archive-plugin thunar-media-tags-plugin \
    2>/dev/null || true
sudo apt-get autoremove -y -qq

echo "==> Configuring default terminal: %s..."
mkdir -p /home/alfaos/.config/xfce4
cat > /home/alfaos/.config/xfce4/helpers.rc << 'HELPERS'
TerminalEmulator=%s
FileManager=thunar
WebBrowser=firefox-esr
HELPERS

%s

echo "==> Installing wallpapers..."
sudo mkdir -p /usr/share/backgrounds/alfaos
sudo cp /tmp/alfa1.jpeg /usr/share/backgrounds/alfaos/alfa1.jpeg
sudo cp /tmp/alfa2.jpeg /usr/share/backgrounds/alfaos/alfa2.jpeg

echo "==> Writing XFCE configuration..."
mkdir -p /home/alfaos/.config/{xfce4/xfconf/xfce-perchannel-xml,gtk-3.0,autostart}

%s

cat > /home/alfaos/.config/xfce4/xfconf/xfce-perchannel-xml/xfce4-panel.xml << 'PANEL'
%s
PANEL

mkdir -p /home/alfaos/.config/xfce4/panel
cat > /home/alfaos/.config/xfce4/panel/whiskermenu-1.rc << 'WHISKER'
button-icon=start-here-debian
button-title=ALFAOS
show-button-title=false
WHISKER

cat > /home/alfaos/.config/xfce4/xfconf/xfce-perchannel-xml/xsettings.xml << 'XSETTINGS'
<?xml version="1.0" encoding="UTF-8"?>
<channel name="xsettings" version="1.0">
  <property name="Net" type="empty">
    <property name="ThemeName" type="string" value="%s"/>
    <property name="IconThemeName" type="string" value="%s"/>
  </property>
  <property name="Gtk" type="empty">
    <property name="FontName" type="string" value="Sans 10"/>
    <property name="CursorThemeName" type="string" value="Adwaita"/>
    <property name="DecorationLayout" type="string" value="icon,menu:minimize,maximize,close"/>
  </property>
</channel>
XSETTINGS

cat > /home/alfaos/.config/xfce4/xfconf/xfce-perchannel-xml/xfwm4.xml << 'XFWM4'
<?xml version="1.0" encoding="UTF-8"?>
<channel name="xfwm4" version="1.0">
  <property name="general" type="empty">
    <property name="theme" type="string" value="%s"/>
    <property name="title_font" type="string" value="Sans Bold 9"/>
    <property name="button_layout" type="string" value="O|SHMC"/>
    <property name="use_compositing" type="bool" value="true"/>
    <property name="box_move" type="bool" value="false"/>
    <property name="box_resize" type="bool" value="false"/>
  </property>
</channel>
XFWM4

cat > /home/alfaos/.config/xfce4/xfconf/xfce-perchannel-xml/xfce4-desktop.xml << 'DESKTOP'
<?xml version="1.0" encoding="UTF-8"?>
<channel name="xfce4-desktop" version="1.0">
  <property name="backdrop" type="empty">
    <property name="screen0" type="empty">
      <property name="monitor0" type="empty">
        <property name="image-style" type="int" value="5"/>
        <property name="image-show" type="bool" value="true"/>
        <property name="last-image" type="string" value="%s"/>
        <property name="workspace0" type="empty">
          <property name="last-image" type="string" value="%s"/>
          <property name="image-style" type="int" value="5"/>
        </property>
      </property>
    </property>
  </property>
  <property name="desktop-icons" type="empty">
    <property name="style" type="int" value="2"/>
  </property>
</channel>
DESKTOP

mkdir -p /home/alfaos/.config/gtk-3.0 /home/alfaos/.config/gtk-2.0
cat > /home/alfaos/.config/gtk-3.0/settings.ini << 'GTK3'
[Settings]
gtk-theme-name=%s
gtk-icon-theme-name=%s
gtk-font-name=Sans 10
gtk-cursor-theme-name=Adwaita
gtk-application-prefer-dark-theme=1
GTK3

cat > /home/alfaos/.config/gtk-2.0/gtkrc << 'GTK2'
gtk-theme-name="%s"
gtk-icon-theme-name="%s"
gtk-font-name="Sans 10"
GTK2

mkdir -p /home/alfaos/.icons/default
cat > /home/alfaos/.icons/default/index.theme << 'ICONS'
[Icon Theme]
Inherits=%s
ICONS

cat > /home/alfaos/.config/autostart/alfaos-desktop-apply.desktop << 'AUTOSTART'
[Desktop Entry]
Type=Application
Name=ALFAOS Desktop Apply
Exec=/home/alfaos/.local/bin/alfaos-apply-desktop.sh
X-GNOME-Autostart-enabled=true
X-GNOME-Autostart-Delay=3
OnlyShowIn=XFCE;
AUTOSTART

mkdir -p /home/alfaos/.local/bin
cat > /home/alfaos/.local/bin/alfaos-apply-desktop.sh << 'APPLY'
#!/bin/bash
LOCK=/tmp/alfaos-apply.lock
exec 9>"$LOCK" || exit 0
flock -n 9 || exit 0

GTK_THEME="%s"
WM_THEME="%s"
ICON_THEME="%s"
WALL="%s"

# Wait for XFCE session (dbus + panel) before touching settings
for _ in $(seq 1 30); do
  xfconf-query -c xfce4-panel -p /panels 2>/dev/null && break
  sleep 1
done

xfconf-query -c xsettings -p /Net/ThemeName -s "$GTK_THEME" 2>/dev/null || \
  xfconf-query -c xsettings -p /Net/ThemeName -n -t string -s "$GTK_THEME"
xfconf-query -c xsettings -p /Net/IconThemeName -s "$ICON_THEME" 2>/dev/null || \
  xfconf-query -c xsettings -p /Net/IconThemeName -n -t string -s "$ICON_THEME"
xfconf-query -c xfwm4 -p /general/theme -s "$WM_THEME" 2>/dev/null || \
  xfconf-query -c xfwm4 -p /general/theme -n -t string -s "$WM_THEME"
xfconf-query -c xfce4-desktop -p /desktop-icons/style -s 2 2>/dev/null || \
  xfconf-query -c xfce4-desktop -p /desktop-icons/style -n -t int -s 2 2>/dev/null || true

apply_wallpaper() {
  local base="$1"
  xfconf-query -c xfce4-desktop -p "${base}/workspace0/last-image" -s "$WALL" 2>/dev/null || \
    xfconf-query -c xfce4-desktop -p "${base}/workspace0/last-image" -n -t string -s "$WALL" 2>/dev/null || return 1
  xfconf-query -c xfce4-desktop -p "${base}/image-show" -s true 2>/dev/null || \
    xfconf-query -c xfce4-desktop -p "${base}/image-show" -n -t bool -s true 2>/dev/null || true
  xfconf-query -c xfce4-desktop -p "${base}/image-style" -s 5 2>/dev/null || \
    xfconf-query -c xfce4-desktop -p "${base}/image-style" -n -t int -s 5 2>/dev/null || true
  xfconf-query -c xfce4-desktop -p "${base}/last-image" -s "$WALL" 2>/dev/null || \
    xfconf-query -c xfce4-desktop -p "${base}/last-image" -n -t string -s "$WALL" 2>/dev/null || true
}

if [ -f "$WALL" ]; then
  for _ in $(seq 1 30); do
    if xrandr --query >/dev/null 2>&1; then
      break
    fi
    sleep 1
  done

  while IFS= read -r output; do
    [ -n "$output" ] || continue
    apply_wallpaper "/backdrop/screen0/monitor${output}"
  done < <(xrandr --query 2>/dev/null | awk '/ connected/{print $1}')

  while IFS= read -r prop; do
    xfconf-query -c xfce4-desktop -p "$prop" -s "$WALL" 2>/dev/null || true
  done < <(xfconf-query -c xfce4-desktop -l 2>/dev/null | grep '/last-image$' || true)

  xfdesktop --reload 2>/dev/null || true
fi
APPLY
chmod +x /home/alfaos/.local/bin/alfaos-apply-desktop.sh

echo "==> Configuring LightDM for auto-login..."
sudo mkdir -p /etc/lightdm/lightdm.conf.d
cat | sudo tee /etc/lightdm/lightdm.conf.d/alfaos-autologin.conf > /dev/null << 'LIGHTDM'
[Seat:*]
autologin-user=alfaos
autologin-user-timeout=0
user-session=xfce
LIGHTDM

sudo chown -R alfaos:alfaos /home/alfaos/.config /home/alfaos/.local

echo "==> ALFAOS desktop configuration complete."
`, strings.Join(themePackages, " "), terminalPkg, plankInstall, browserInstall,
		terminalPkg, terminalPkg, tilixConfig, plankConfig, panelConfig,
		gtkTheme, iconTheme, wmTheme, wallPath, wallPath,
		gtkTheme, iconTheme, gtkTheme, iconTheme, iconTheme,
		gtkTheme, wmTheme, iconTheme, wallPath)
}

func themeNames(theme string) (gtk, wm string) {
	switch strings.ToLower(theme) {
	case "arc":
		return "Arc-Dark", "Arc-Dark"
	case "dracula":
		return "Dracula", "Dracula"
	case "gruvbox":
		return "Gruvbox-GTK-Theme", "Gruvbox-GTK-Theme"
	default:
		return "Arc-Dark", "Arc-Dark"
	}
}

func terminalPackage(name string) string {
	switch strings.ToLower(name) {
	case "xfce4-terminal", "xfce":
		return "xfce4-terminal"
	default:
		return "tilix"
	}
}

func themePackages(theme string) []string {
	switch strings.ToLower(theme) {
	case "arc":
		return []string{"arc-theme"}
	case "dracula":
		return []string{"dracula-theme"}
	case "gruvbox":
		return []string{"gruvbox-gtk-theme"}
	default:
		return []string{"arc-theme"}
	}
}

func panelXML(plankEnabled bool) string {
	if plankEnabled {
		// Plank shows pinned/running apps — skip tasklist to avoid duplicate icons.
		return `<?xml version="1.0" encoding="UTF-8"?>
<channel name="xfce4-panel" version="1.0">
  <property name="panels" type="array">
    <value type="int" value="1"/>
    <property name="panel-1" type="empty">
      <property name="position" type="string" value="p=10;x=0;y=0"/>
      <property name="length" type="uint" value="100"/>
      <property name="size" type="uint" value="32"/>
      <property name="background-style" type="uint" value="0"/>
      <property name="background-alpha" type="uint" value="100"/>
      <property name="plugin-ids" type="array">
        <value type="int" value="1"/>
        <value type="int" value="2"/>
        <value type="int" value="3"/>
      </property>
    </property>
  </property>
  <property name="plugins" type="empty">
    <property name="plugin-1" type="string" value="whiskermenu"/>
    <property name="plugin-2" type="string" value="separator"/>
    <property name="plugin-3" type="string" value="systray"/>
  </property>
</channel>`
	}

	return `<?xml version="1.0" encoding="UTF-8"?>
<channel name="xfce4-panel" version="1.0">
  <property name="panels" type="array">
    <value type="int" value="1"/>
    <property name="panel-1" type="empty">
      <property name="position" type="string" value="p=10;x=0;y=0"/>
      <property name="length" type="uint" value="100"/>
      <property name="size" type="uint" value="32"/>
      <property name="background-style" type="uint" value="0"/>
      <property name="background-alpha" type="uint" value="100"/>
      <property name="plugin-ids" type="array">
        <value type="int" value="1"/>
        <value type="int" value="2"/>
        <value type="int" value="3"/>
        <value type="int" value="4"/>
      </property>
    </property>
  </property>
  <property name="plugins" type="empty">
    <property name="plugin-1" type="string" value="whiskermenu"/>
    <property name="plugin-2" type="string" value="separator"/>
    <property name="plugin-3" type="string" value="tasklist">
      <property name="grouping" type="uint" value="1"/>
    </property>
    <property name="plugin-4" type="string" value="systray"/>
  </property>
</channel>`
}

func (d *Configurator) Install(ip string) error {
	logging.Info("Installing ALFAOS desktop environment on VM...")

	scriptPath := "/tmp/alfaos-desktop-setup.sh"
	localScript := d.cfg.Paths.StateDir + "/desktop-setup.sh"

	if err := os.WriteFile(localScript, []byte(d.InstallScript()), 0755); err != nil {
		return fmt.Errorf("write desktop script: %w", err)
	}

	if err := d.vm.CopyFile(ip, localScript, scriptPath); err != nil {
		return fmt.Errorf("copy desktop script: %w", err)
	}

	out, err := d.vm.RunSSH(ip, "chmod +x "+scriptPath+" && bash "+scriptPath)
	if err != nil {
		return fmt.Errorf("desktop install failed: %w\n%s", err, out)
	}

	logging.Success("Desktop environment installed")
	return nil
}

func (d *Configurator) CheckPackage(ip, pkg string) bool {
	out, err := d.vm.RunSSH(ip, "dpkg -l "+pkg+" 2>/dev/null | grep ^ii")
	return err == nil && strings.Contains(out, "ii")
}
