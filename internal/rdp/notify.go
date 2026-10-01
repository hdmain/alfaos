package rdp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/alfaos/alfaos/internal/logging"
)

// LoginNotifyScriptBody is installed on the guest and posts to Discord on RDP login.
// No-ops when /etc/alfaos/discord-webhook is missing/empty. Dedupes within 45s.
func LoginNotifyScriptBody() string {
	return `#!/bin/bash
URL_FILE=/etc/alfaos/discord-webhook
[ -s "$URL_FILE" ] || exit 0
URL=$(tr -d '\n\r ' < "$URL_FILE")
[ -n "$URL" ] || exit 0

LOCK=/tmp/alfaos-login-notify.lock
now=$(date +%s)
if [ -f "$LOCK" ]; then
  prev=$(cat "$LOCK" 2>/dev/null || echo 0)
  if [ "$prev" -gt 0 ] 2>/dev/null && [ $((now - prev)) -lt 45 ]; then
    exit 0
  fi
fi
echo "$now" > "$LOCK" 2>/dev/null || true

USER_NAME=${PAM_USER:-${USER:-alfaos}}
RHOST=${PAM_RHOST:-n/a}
HOST=$(hostname -f 2>/dev/null || hostname)
WHEN=$(date -u +"%Y-%m-%d %H:%M:%S UTC")
EVENT=${1:-login}
case "$EVENT" in
  pam|session) EVENT="login" ;;
  reconnect) EVENT="reconnect" ;;
esac

# Minimal JSON escaping for Discord content
esc() { printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g'; }
CONTENT=$(printf 'ALFAOS RDP %s\n• user: %s\n• host: %s\n• from: %s\n• time: %s' \
  "$(esc "$EVENT")" "$(esc "$USER_NAME")" "$(esc "$HOST")" "$(esc "$RHOST")" "$(esc "$WHEN")")
PAYLOAD=$(printf '{"content":"%s"}' "$CONTENT")

if command -v curl >/dev/null 2>&1; then
  curl -fsS -m 8 -H 'Content-Type: application/json' -d "$PAYLOAD" "$URL" >/dev/null 2>&1 || true
elif command -v wget >/dev/null 2>&1; then
  wget -q -T 8 -O /dev/null --header='Content-Type: application/json' --post-data="$PAYLOAD" "$URL" 2>/dev/null || true
fi
`
}

// InstallLoginNotifyBash writes webhook URL, notify script, PAM + session hooks.
func InstallLoginNotifyBash(webhookURL string) string {
	url := strings.TrimSpace(webhookURL)
	urlFileBlock := `sudo rm -f /etc/alfaos/discord-webhook 2>/dev/null || true`
	if url != "" {
		// Write URL via printf to avoid heredoc issues with special chars
		urlFileBlock = fmt.Sprintf(`sudo mkdir -p /etc/alfaos
printf '%%s\n' %q | sudo tee /etc/alfaos/discord-webhook >/dev/null
sudo chmod 640 /etc/alfaos/discord-webhook
sudo chown root:alfaos /etc/alfaos/discord-webhook 2>/dev/null || sudo chown root:root /etc/alfaos/discord-webhook
`, url)
	}

	return fmt.Sprintf(`set -euo pipefail
sudo mkdir -p /etc/alfaos /usr/local/bin

%s

cat | sudo tee /usr/local/bin/alfaos-login-notify.sh >/dev/null << 'NOTIFY'
%s
NOTIFY
sudo chmod 755 /usr/local/bin/alfaos-login-notify.sh

# PAM: fire on successful xrdp-sesman session open
if [ -f /etc/pam.d/xrdp-sesman ]; then
  if ! grep -q 'alfaos-login-notify' /etc/pam.d/xrdp-sesman 2>/dev/null; then
    echo 'session optional pam_exec.so seteuid /usr/local/bin/alfaos-login-notify.sh pam' | sudo tee -a /etc/pam.d/xrdp-sesman >/dev/null
  fi
fi

echo LOGIN_NOTIFY_OK
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
	if err != nil {
		return fmt.Errorf("login notify install: %w\n%s", err, out)
	}
	if url == "" {
		logging.Info("Discord webhook empty — login notify installed but disabled")
	} else {
		logging.Success("Discord webhook enabled for successful RDP logins")
	}
	return nil
}
