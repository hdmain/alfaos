use std::process::Command;

/// Map quality preset → (width, height, bpp).
pub fn quality_geometry(quality: &str) -> Option<(u32, u32, u32)> {
    match quality.to_lowercase().as_str() {
        "low" => Some((1280, 720, 16)),
        "medium" | "med" => Some((1600, 900, 24)),
        "high" => Some((1920, 1080, 32)),
        "ultra" => Some((2560, 1440, 32)),
        _ => None,
    }
}

/// Apply resolution on the *current* graphical session (Alfa Center has DISPLAY).
/// Also persists files + xrdp max_bpp so Display settings / next login match.
pub fn apply_local(quality: &str, width: u32, height: u32, bpp: u32) -> Result<String, String> {
    let display = std::env::var("DISPLAY").unwrap_or_else(|_| ":0".into());
    let script = format!(
        r#"set -euo pipefail
export DISPLAY={display}
LOG=/tmp/alfaos-quality-local.log
: > "$LOG"
echo "DISPLAY=$DISPLAY quality={quality} target={width}x{height} bpp={bpp}" >> "$LOG"

sudo mkdir -p /etc/alfaos
sudo tee /etc/alfaos/rdp-resolution >/dev/null <<EOF
W={width}
H={height}
EOF
sudo tee /etc/alfaos/rdp-quality >/dev/null <<EOF
QUALITY={quality}
BPP={bpp}
EOF

if [ -f /etc/xrdp/xrdp.ini ]; then
  if grep -q '^max_bpp=' /etc/xrdp/xrdp.ini; then
    sudo sed -i 's/^max_bpp=.*/max_bpp={bpp}/' /etc/xrdp/xrdp.ini
  else
    sudo sed -i '/^\[Globals\]/a max_bpp={bpp}' /etc/xrdp/xrdp.ini
  fi
fi

# Prefer the connected output (xorgxrdp: often rdp0 / VNC-0)
OUTPUT=$(xrandr 2>/dev/null | awk '/ connected/{{print $1; exit}}')
if [ -z "$OUTPUT" ]; then
  echo "no connected output" >> "$LOG"
  xrandr >> "$LOG" 2>&1 || true
  exit 3
fi
echo "output=$OUTPUT" >> "$LOG"

set_mode() {{
  xrandr --output "$OUTPUT" --mode "$1" >>"$LOG" 2>&1
}}

ok=0
# Shrink framebuffer first so Display settings cannot stay at 1920
xrandr --fb {width}x{height} >>"$LOG" 2>&1 || true

if set_mode "{width}x{height}"; then
  ok=1
else
  # Existing mode name containing WxH
  while IFS= read -r mode; do
    if set_mode "$mode"; then ok=1; break; fi
  done < <(xrandr 2>/dev/null | awk -v w="{width}" -v h="{height}" '$0 ~ w"x"h {{print $1}}')
fi

if [ "$ok" -eq 0 ] && command -v cvt >/dev/null 2>&1; then
  MODELINE=$(cvt {width} {height} 60 2>/dev/null | awk '/Modeline/{{sub(/^Modeline /,""); print}}')
  if [ -n "$MODELINE" ]; then
    MODE=$(echo "$MODELINE" | awk '{{print $1}}' | tr -d '"')
    xrandr --newmode $MODELINE >>"$LOG" 2>&1 || true
    xrandr --addmode "$OUTPUT" "$MODE" >>"$LOG" 2>&1 || true
    if set_mode "$MODE"; then ok=1; fi
  fi
fi

# Force again after mode add
xrandr --output "$OUTPUT" --mode "{width}x{height}" >>"$LOG" 2>&1 || true
xrandr --fb {width}x{height} >>"$LOG" 2>&1 || true

# Sync XFCE display panel / desktop geometry hints
if command -v xfconf-query >/dev/null 2>&1; then
  xfconf-query -c displays -p /Default/"$OUTPUT"/Resolution -n -t string -s "{width}x{height}" >>"$LOG" 2>&1 || true
  xfconf-query -c displays -p /Default/"$OUTPUT"/ActiveResolution -n -t string -s "{width}x{height}" >>"$LOG" 2>&1 || true
fi

CURRENT=$(xrandr 2>/dev/null | awk '/\*/{{print $1; exit}}')
echo "current=$CURRENT ok=$ok" >> "$LOG"

if [ "$ok" -ne 1 ]; then
  echo "FAILED to set {width}x{height}" >> "$LOG"
  exit 2
fi

# Confirm
if echo "$CURRENT" | grep -q "{width}x{height}"; then
  echo "OK $CURRENT"
  exit 0
fi
# Mode may be named differently (e.g. 1280x720_60.00)
if xrandr 2>/dev/null | awk '/\*/{{print; exit}}' | grep -q "{width}x{height}"; then
  echo "OK $CURRENT"
  exit 0
fi
echo "OK $CURRENT (set attempted)"
"#,
        display = shell_quote(&display),
        quality = quality,
        width = width,
        height = height,
        bpp = bpp,
    );

    let out = Command::new("bash")
        .arg("-lc")
        .arg(&script)
        .output()
        .map_err(|e| format!("failed to run display script: {e}"))?;

    let stdout = String::from_utf8_lossy(&out.stdout).trim().to_string();
    let stderr = String::from_utf8_lossy(&out.stderr).trim().to_string();
    if !out.status.success() {
        let log = std::fs::read_to_string("/tmp/alfaos-quality-local.log").unwrap_or_default();
        return Err(format!(
            "local display apply failed (exit {:?}): {stderr}\n{stdout}\n{log}",
            out.status.code()
        ));
    }

    let current = current_resolution().unwrap_or_else(|_| "unknown".into());
    Ok(format!(
        "Display is now {current} (wanted {width}×{height}, {bpp}-bit). {}",
        if stdout.is_empty() { String::new() } else { stdout }
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
