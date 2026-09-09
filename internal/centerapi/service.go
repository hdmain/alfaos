package centerapi

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/alfaos/alfaos/internal/config"
	hostpkg "github.com/alfaos/alfaos/internal/host"
	"github.com/alfaos/alfaos/internal/logging"
)

const unitPath = "/etc/systemd/system/alfaos-center-api.service"

// InstallService writes and enables the systemd unit for the Center API.
func InstallService(cfg *config.Config) error {
	bin := resolveAlfaosBinary()
	cfgPath := resolveConfigPath()
	execStart := fmt.Sprintf("%s center-api", bin)
	if cfgPath != "" {
		execStart = fmt.Sprintf("%s center-api --config %s", bin, cfgPath)
	}

	unit := fmt.Sprintf(`[Unit]
Description=ALFAOS Alfa Center API (guest settings UI)
After=network-online.target libvirtd.service
Wants=network-online.target
Requires=libvirtd.service

[Service]
Type=simple
Restart=always
RestartSec=3
ExecStart=%s

[Install]
WantedBy=multi-user.target
`, execStart)

	if err := os.WriteFile(unitPath, []byte(unit), 0644); err != nil {
		return err
	}
	_, _ = hostpkg.RunCommand("systemctl", "daemon-reload")
	_, _ = hostpkg.RunCommand("systemctl", "enable", "alfaos-center-api.service")
	_, _ = hostpkg.RunCommand("systemctl", "restart", "alfaos-center-api.service")
	logging.Success("Alfa Center API service installed (alfaos-center-api)")
	return nil
}

func resolveAlfaosBinary() string {
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		if abs, err := filepath.Abs(exe); err == nil {
			return abs
		}
		return exe
	}
	for _, p := range []string{"/usr/local/bin/alfaos", "/alfaos", "/usr/bin/alfaos"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "alfaos"
}

func resolveConfigPath() string {
	for _, c := range []string{"/etc/alfaos/config.yaml", "configs/default.yaml"} {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}
