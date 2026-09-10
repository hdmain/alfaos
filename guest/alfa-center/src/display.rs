use std::process::Command;

/// Link profile → (preferred_w, preferred_h, bpp, compress_flag, light_desktop).
/// Resolution is a soft preference for clients; we do NOT fail if Display stays the same.
pub fn quality_geometry(quality: &str) -> Option<(u32, u32, u32)> {
    let p = link_profile(quality)?;
    Some((p.width, p.height, p.bpp))
}

pub struct LinkProfile {
    pub name: &'static str,
    pub width: u32,
    pub height: u32,
    pub bpp: u32,
    pub bulk_compression: bool,
    pub bitmap_compression: bool,
    pub crypt_level: &'static str, // low | medium | high
    pub light_desktop: bool,       // fewer animations / cheaper wallpaper
}

pub fn link_profile(quality: &str) -> Option<LinkProfile> {
    match quality.to_lowercase().as_str() {
        "low" | "slow" => Some(LinkProfile {
            name: "slow",
            width: 1280,
            height: 720,
            bpp: 16,
            bulk_compression: true,
            bitmap_compression: true,
            crypt_level: "low",
            light_desktop: true,
        }),
        "medium" | "med" | "balanced" => Some(LinkProfile {
            name: "balanced",
            width: 1600,
            height: 900,
            bpp: 24,
            bulk_compression: true,
            bitmap_compression: true,
            crypt_level: "medium",
            light_desktop: false,
        }),
        "high" | "lan" => Some(LinkProfile {
            name: "lan",
            width: 1920,
            height: 1080,
            bpp: 32,
            bulk_compression: false,
            bitmap_compression: false,
            crypt_level: "low",
            light_desktop: false,
        }),
        "ultra" | "max" => Some(LinkProfile {
            name: "max",
            width: 2560,
            height: 1440,
            bpp: 32,
            bulk_compression: false,
            bitmap_compression: false,
            crypt_level: "low",
            light_desktop: false,
        }),
        _ => None,
    }
}

/// Tune xRDP + XFCE for the link profile. Soft-try xrandr; Display may stay the same.
pub fn apply_local(quality: &str, width: u32, height: u32, bpp: u32) -> Result<String, String> {
    let profile = link_profile(quality).ok_or_else(|| "unknown profile".to_string())?;
    let compress = if profile.bulk_compression { "true" } else { "false" };
    let bitmap_compress = if profile.bitmap_compression {
        "true"
    } else {
        "false"
    };
    let light = if profile.light_desktop { "1" } else { "0" };
    let display = std::env::var("DISPLAY").unwrap_or_else(|_| ":10".into());

    let script = format!(
        r#"set -euo pipefail
export DISPLAY={display}
LOG=/tmp/alfaos-quality-local.log
: > "$LOG"
echo "profile={pname} DISPLAY=$DISPLAY bpp={bpp} compress={compress} bitmap={bitmap_compress} light={light}" >> "$LOG"

sudo mkdir -p /etc/alfaos
sudo tee /etc/alfaos/rdp-quality >/dev/null <<EOF
QUALITY={quality}
PROFILE={pname}
BPP={bpp}
COMPRESS={compress}
BITMAP_COMPRESS={bitmap_compress}
LIGHT={light}
W={width}
H={height}
EOF
sudo tee /etc/alfaos/rdp-resolution >/dev/null <<EOF
W={width}
H={height}
EOF

# --- xRDP server: critical for slow links ---
if [ -f /etc/xrdp/xrdp.ini ]; then
  set_ini() {{
    local key="$1" val="$2"
    if grep -q "^${{key}}=" /etc/xrdp/xrdp.ini; then
      sudo sed -i "s/^${{key}}=.*/${{key}}=${{val}}/" /etc/xrdp/xrdp.ini
    else
      sudo sed -i "/^\[Globals\]/a ${{key}}=${{val}}" /etc/xrdp/xrdp.ini
    fi
  }}
  set_ini max_bpp {bpp}
  set_ini bulk_compression {compress}
  set_ini tcp_nodelay true
  set_ini tcp_keepalive true
  set_ini crypt_level {crypt}
  set_ini use_fastpath both
  set_ini new_cursors true
  set_ini bitmap_cache true
  set_ini bitmap_compression {bitmap_compress}
  set_ini pointer_cache_size 32
  echo "xrdp.ini tuned" >> "$LOG"
fi

# Soft resize (optional) — OK if RDP client keeps its size
OUTPUT=$(xrandr 2>/dev/null | awk '/ connected/{{print $1; exit}}' || true)
if [ -n "$OUTPUT" ]; then
  xrandr --output "$OUTPUT" --mode "{width}x{height}" >>"$LOG" 2>&1 || \
    (command -v cvt >/dev/null && MODELINE=$(cvt {width} {height} 60 2>/dev/null | awk '/Modeline/{{sub(/^Modeline /,""); print}}') && \
     MODE=$(echo "$MODELINE" | awk '{{print $1}}' | tr -d '"') && \
     xrandr --newmode $MODELINE >>"$LOG" 2>&1 || true && \
     xrandr --addmode "$OUTPUT" "$MODE" >>"$LOG" 2>&1 || true && \
     xrandr --output "$OUTPUT" --mode "$MODE" >>"$LOG" 2>&1 || true) || true
fi

# --- Light desktop for slow links (lite wallpaper = cheaper to encode over RDP) ---
LITE_WALL=/usr/share/backgrounds/alfaos/alfaoslite.jpg
FULL_WALL=/usr/share/backgrounds/alfaos/alfaos3.png
if [ "{light}" = "1" ]; then
  xfconf-query -c xfwm4 -p /general/use_compositing -s false 2>/dev/null || true
  xfconf-query -c xfwm4 -p /general/workspace_count -s 1 2>/dev/null || true
  xfconf-query -c xfce4-desktop -p /desktop-icons/style -s 0 2>/dev/null || true
  WALL="$LITE_WALL"
  if [ ! -f "$WALL" ]; then
    echo "missing $LITE_WALL — keeping current wallpaper" >> "$LOG"
    WALL=""
  fi
  if [ -n "$WALL" ]; then
    for prop in $(xfconf-query -c xfce4-desktop -l 2>/dev/null | grep '/last-image$' || true); do
      xfconf-query -c xfce4-desktop -p "$prop" -s "$WALL" 2>/dev/null || \
        xfconf-query -c xfce4-desktop -p "$prop" -n -t string -s "$WALL" 2>/dev/null || true
    done
    for prop in $(xfconf-query -c xfce4-desktop -l 2>/dev/null | grep '/image-style$' || true); do
      xfconf-query -c xfce4-desktop -p "$prop" -s 5 2>/dev/null || true  # scaled
    done
    echo "lite wallpaper: $WALL" >> "$LOG"
  fi
  xfconf-query -c xfce4-panel -p /panels/panel-1/background-style -s 0 2>/dev/null || true
  echo "light desktop applied" >> "$LOG"
else
  xfconf-query -c xfce4-desktop -p /desktop-icons/style -s 2 2>/dev/null || true
  if [ -f "$FULL_WALL" ]; then
    for prop in $(xfconf-query -c xfce4-desktop -l 2>/dev/null | grep '/last-image$' || true); do
      xfconf-query -c xfce4-desktop -p "$prop" -s "$FULL_WALL" 2>/dev/null || true
    done
    echo "restored wallpaper: $FULL_WALL" >> "$LOG"
  fi
fi

# --- Persist forever: re-apply on every XFCE login / RDP reconnect ---
mkdir -p /home/alfaos/.local/bin /home/alfaos/.config/autostart
cat > /home/alfaos/.local/bin/alfaos-apply-quality.sh << 'QSCRIPT'
#!/bin/bash
[ -f /etc/alfaos/rdp-quality ] || exit 0
. /etc/alfaos/rdp-quality
QUALITY=${{QUALITY:-high}}
BPP=${{BPP:-32}}
COMPRESS=${{COMPRESS:-false}}
BITMAP_COMPRESS=${{BITMAP_COMPRESS:-false}}
LIGHT=${{LIGHT:-0}}
Q=$(echo "$QUALITY" | tr 'A-Z' 'a-z')
CRYPT=${{CRYPT:-low}}
case "$Q" in
  low|slow) CRYPT=low; BPP=${{BPP:-16}}; COMPRESS=true; BITMAP_COMPRESS=true; LIGHT=${{LIGHT:-1}} ;;
  medium|med|balanced) CRYPT=medium; BPP=${{BPP:-24}}; COMPRESS=true; BITMAP_COMPRESS=true ;;
  ultra|max|high|lan) CRYPT=low; BPP=${{BPP:-32}}; COMPRESS=false; BITMAP_COMPRESS=false ;;
esac
set_ini() {{
  [ -f /etc/xrdp/xrdp.ini ] || return 0
  local key="$1" val="$2"
  if grep -q "^${{key}}=" /etc/xrdp/xrdp.ini 2>/dev/null; then
    sudo sed -i "s/^${{key}}=.*/${{key}}=${{val}}/" /etc/xrdp/xrdp.ini
  else
    sudo sed -i "/^\[Globals\]/a ${{key}}=${{val}}" /etc/xrdp/xrdp.ini
  fi
}}
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
[ -n "${{DISPLAY:-}}" ] || exit 0
command -v xfconf-query >/dev/null 2>&1 || exit 0
LITE_WALL=/usr/share/backgrounds/alfaos/alfaoslite.jpg
FULL_WALL=/usr/share/backgrounds/alfaos/alfaos3.png
if [ "$LIGHT" = "1" ]; then WALL="$LITE_WALL"; ICON_STYLE=0
  xfconf-query -c xfwm4 -p /general/use_compositing -s false 2>/dev/null || true
else WALL="$FULL_WALL"; ICON_STYLE=2; fi
[ -f "$WALL" ] || WALL=""
xfconf-query -c xfce4-desktop -p /desktop-icons/style -s "$ICON_STYLE" 2>/dev/null || true
if [ -n "$WALL" ]; then
  for prop in $(xfconf-query -c xfce4-desktop -l 2>/dev/null | grep '/last-image$' || true); do
    xfconf-query -c xfce4-desktop -p "$prop" -s "$WALL" 2>/dev/null || true
  done
  xfdesktop --reload 2>/dev/null || true
fi
QSCRIPT
chmod +x /home/alfaos/.local/bin/alfaos-apply-quality.sh

cat > /home/alfaos/.config/autostart/alfaos-quality-apply.desktop << 'AUTO'
[Desktop Entry]
Type=Application
Name=ALFAOS Quality Persist
Exec=/home/alfaos/.local/bin/alfaos-apply-quality.sh
X-GNOME-Autostart-enabled=true
X-GNOME-Autostart-Delay=5
OnlyShowIn=XFCE;
AUTO

if [ -f /home/alfaos/.local/bin/alfaos-apply-desktop.sh ] && \
   ! grep -q 'alfaos-apply-quality.sh' /home/alfaos/.local/bin/alfaos-apply-desktop.sh; then
  printf '\n[ -x /home/alfaos/.local/bin/alfaos-apply-quality.sh ] && /home/alfaos/.local/bin/alfaos-apply-quality.sh >/tmp/alfaos-quality.log 2>&1 || true\n' \
    >> /home/alfaos/.local/bin/alfaos-apply-desktop.sh
fi

sudo tee /etc/xrdp/startwm.sh >/dev/null << 'STARTWM'
#!/bin/sh
if [ -r /etc/default/locale ]; then . /etc/default/locale; export LANG LANGUAGE; fi
if [ -r /etc/profile ]; then . /etc/profile; fi
unset DBUS_SESSION_BUS_ADDRESS
unset XDG_RUNTIME_DIR
/home/alfaos/.local/bin/alfaos-set-resolution.sh >/tmp/alfaos-resolution.log 2>&1 || true
/home/alfaos/.local/bin/alfaos-apply-quality.sh >/tmp/alfaos-quality.log 2>&1 || true
exec startxfce4
STARTWM
sudo chmod +x /etc/xrdp/startwm.sh
sudo tee /etc/xrdp/reconnectwm.sh >/dev/null << 'RECONNECT'
#!/bin/sh
/home/alfaos/.local/bin/alfaos-apply-quality.sh >/tmp/alfaos-quality.log 2>&1 || true
/home/alfaos/.local/bin/alfaos-set-resolution.sh >/tmp/alfaos-resolution.log 2>&1 || true
RECONNECT
sudo chmod +x /etc/xrdp/reconnectwm.sh
echo "persistence hooks installed" >> "$LOG"

CURRENT=$(xrandr 2>/dev/null | awk '/\*/{{print $1; exit}}' || echo unknown)
echo "OK profile={pname} bpp={bpp} compress={compress} display=$CURRENT"
"#,
        display = shell_quote(&display),
        quality = quality,
        pname = profile.name,
        width = width,
        height = height,
        bpp = bpp,
        compress = compress,
        bitmap_compress = bitmap_compress,
        crypt = profile.crypt_level,
        light = light,
    );

    let out = Command::new("bash")
        .arg("-lc")
        .arg(&script)
        .output()
        .map_err(|e| format!("failed to run link profile script: {e}"))?;

    let stdout = String::from_utf8_lossy(&out.stdout).trim().to_string();
    let stderr = String::from_utf8_lossy(&out.stderr).trim().to_string();
    if !out.status.success() {
        let log = std::fs::read_to_string("/tmp/alfaos-quality-local.log").unwrap_or_default();
        return Err(format!(
            "link profile apply failed (exit {:?}): {stderr}\n{stdout}\n{log}",
            out.status.code()
        ));
    }

    Ok(format!(
        "Profile '{}' applied permanently ({}-bit, compression={}, light_desktop={}). \
         Survives reconnect and reboot. Slow link uses alfaoslite.jpg.",
        profile.name, bpp, compress, profile.light_desktop
    ))
}

pub fn current_resolution() -> Result<String, String> {
    let out = Command::new("bash")
        .arg("-lc")
        .arg(r#"xrandr 2>/dev/null | awk '/\*/{print $1; exit}'"#)
        .output()
        .map_err(|e| e.to_string())?;
    let s = String::from_utf8_lossy(&out.stdout).trim().to_string();
    if s.is_empty() {
        Err("could not read current resolution".into())
    } else {
        Ok(s)
    }
}

fn shell_quote(s: &str) -> String {
    format!("'{}'", s.replace('\'', r#"'\''"#))
}
