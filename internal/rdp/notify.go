package rdp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/alfaos/alfaos/internal/logging"
)

// LoginNotifyScriptBody posts login events to the host Alfa Center API
// (host then sends Discord). Falls back to a local discord-webhook file.
func LoginNotifyScriptBody() string {
	return `#!/bin/bash
LOG=/tmp/alfaos-login-notify.log
{
echo "=== $(date -u +%Y-%m-%dT%H:%M:%SZ) args=$* ==="

LOCK=/tmp/alfaos-login-notify.lock
now=$(date +%s)
EVENT=${1:-login}
if [ "$EVENT" != "install-test" ] && [ -f "$LOCK" ]; then
  prev=$(cat "$LOCK" 2>/dev/null || echo 0)
  if [ "$prev" -gt 0 ] 2>/dev/null && [ $((now - prev)) -lt 45 ]; then
    echo "dedupe: skip (last ${prev})"
    exit 0
  fi
fi
[ "$EVENT" != "install-test" ] && echo "$now" > "$LOCK" 2>/dev/null || true

USER_NAME=${PAM_USER:-${USER:-alfaos}}
RHOST=${PAM_RHOST:-n/a}
HOST=$(hostname -f 2>/dev/null || hostname)
case "$EVENT" in
  pam|session) EVENT="login" ;;
  install-test) EVENT="install-test" ;;
esac

API_URL=""
TOKEN=""
if [ -r /etc/alfaos/center.conf ]; then
  # shellcheck disable=SC1091
  . /etc/alfaos/center.conf
fi

json_esc() { printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g; s/'"$(printf '\r')"'//g'; }

BODY=$(printf '{"event":"%s","user":"%s","host":"%s","from":"%s"}' \
  "$(json_esc "$EVENT")" "$(json_esc "$USER_NAME")" "$(json_esc "$HOST")" "$(json_esc "$RHOST")")

posted=0
if [ -n "${API_URL:-}" ] && [ -n "${TOKEN:-}" ]; then
  URL="${API_URL%/}/api/notify/login"
  echo "POST $URL"
  if command -v curl >/dev/null 2>&1; then
    if curl -fsS -m 10 -H "X-Alfa-Token: $TOKEN" -H 'Content-Type: application/json' -d "$BODY" "$URL"; then
      echo "host API ok"
      posted=1
    else
      echo "host API failed (curl exit $?)"
    fi
  fi
fi

# Fallback: direct Discord from guest (needs outbound internet)
if [ "$posted" != "1" ] && [ -s /etc/alfaos/discord-webhook ]; then
  WH=$(tr -d '\n\r ' < /etc/alfaos/discord-webhook)
  CONTENT=$(printf 'ALFAOS RDP %s | user=%s host=%s from=%s' "$EVENT" "$USER_NAME" "$HOST" "$RHOST")
  CONTENT_ESC=$(json_esc "$CONTENT")
  PAYLOAD=$(printf '{"content":"%s"}' "$CONTENT_ESC")
  echo "POST discord webhook fallback"
  curl -fsS -m 10 -H 'Content-Type: application/json' -d "$PAYLOAD" "$WH" && posted=1 || echo "discord fallback failed"
fi

[ "$posted" = "1" ] || echo "WARN: no notify delivered"
exit 0
} >>"$LOG" 2>&1
`
}

// InstallLoginNotifyBash installs the notify script, PAM hook, and session hooks.
func InstallLoginNotifyBash(webhookURL string) string {
	url := strings.TrimSpace(webhookURL)
	urlFileBlock := `sudo rm -f /etc/alfaos/discord-webhook 2>/dev/null || true`
	if url != "" {
		urlFileBlock = fmt.Sprintf(`sudo mkdir -p /etc/alfaos
printf '%%s\n' %q | sudo tee /etc/alfaos/discord-webhook >/dev/null
sudo chmod 640 /etc/alfaos/discord-webhook
sudo chown root:alfaos /etc/alfaos/discord-webhook 2>/dev/null || sudo chown root:root /etc/alfaos/discord-webhook
`, url)
	}

	return fmt.Sprintf(`set -euo pipefail
sudo mkdir -p /etc/alfaos /usr/local/bin
sudo apt-get install -y -qq curl ca-certificates 2>/dev/null || true

%s

cat | sudo tee /usr/local/bin/alfaos-login-notify.sh >/dev/null << 'NOTIFY'
%s
NOTIFY
sudo chmod 755 /usr/local/bin/alfaos-login-notify.sh

# PAM: fire on successful xrdp-sesman session
if [ -f /etc/pam.d/xrdp-sesman ]; then
  sudo sed -i '/alfaos-login-notify/d' /etc/pam.d/xrdp-sesman 2>/dev/null || true
  echo 'session optional pam_exec.so seteuid /usr/local/bin/alfaos-login-notify.sh pam' | sudo tee -a /etc/pam.d/xrdp-sesman >/dev/null
fi

# Always wire startwm / reconnectwm (idempotent)
if [ -f /etc/xrdp/startwm.sh ]; then
  sudo sed -i '/alfaos-login-notify/d' /etc/xrdp/startwm.sh 2>/dev/null || true
  tmp=$(mktemp)
  awk '
    /exec startxfce4/ && !done {
      print "/usr/local/bin/alfaos-login-notify.sh session >/tmp/alfaos-login-notify.log 2>&1 &"
      done=1
    }
    { print }
  ' /etc/xrdp/startwm.sh > "$tmp"
  sudo mv "$tmp" /etc/xrdp/startwm.sh
  sudo chmod +x /etc/xrdp/startwm.sh
fi
if [ -f /etc/xrdp/reconnectwm.sh ]; then
  sudo sed -i '/alfaos-login-notify/d' /etc/xrdp/reconnectwm.sh 2>/dev/null || true
  echo '/usr/local/bin/alfaos-login-notify.sh reconnect >/tmp/alfaos-login-notify.log 2>&1 &' | sudo tee -a /etc/xrdp/reconnectwm.sh >/dev/null
  sudo chmod +x /etc/xrdp/reconnectwm.sh
fi

# Smoke-test notify path (non-fatal)
sudo -u alfaos /usr/local/bin/alfaos-login-notify.sh install-test >/tmp/alfaos-login-notify.log 2>&1 || true
echo LOGIN_NOTIFY_OK
cat /tmp/alfaos-login-notify.log 2>/dev/null | tail -n 20 || true
`, urlFileBlock, LoginNotifyScriptBody())
}

// InstallLoginNotify pushes Discord login webhook hooks to a running guest.
func (r *Configurator) InstallLoginNotify(ip string) error {
	url := strings.TrimSpace(r.cfg.Notify.DiscordWebhook)
	logging.Info("Installing RDP login Discord notify on VM...")
	body := InstallLoginNotifyBash(url)
	local := filepath.Join(r.cfg.Paths.StateDir, "rdp-login-notify.sh")
	if err := os.WriteFile(local, []byte("#!/bin/bash\n"+body), 0755); err != nil {
		return fmt.Errorf("write login notify script: %w", err)
	}
	remote := "/tmp/alfaos-rdp-login-notify.sh"
	if err := r.vm.CopyFile(ip, local, remote); err != nil {
		return err
	}
	out, err := r.vm.RunSSH(ip, "chmod +x "+remote+" && bash "+remote)
	if strings.TrimSpace(out) != "" {
		logging.Info("%s", strings.TrimSpace(out))
	}
	if err != nil {
		return fmt.Errorf("login notify install: %w\n%s", err, out)
	}
	if url == "" {
		logging.Info("Discord webhook empty — login notify installed but Discord disabled on host")
	} else {
		logging.Success("Discord login notify installed (guest → host API → Discord)")
	}
	return nil
}
