package guestsetup

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/alfaos/alfaos/internal/config"
	"github.com/alfaos/alfaos/internal/desktop"
	"github.com/alfaos/alfaos/internal/logging"
	"github.com/alfaos/alfaos/internal/rdp"
	"github.com/alfaos/alfaos/internal/virtualization"
	"github.com/alfaos/alfaos/internal/wallpapers"
)

// Install configures the guest in one SSH session (wallpapers + desktop + RDP).
func Install(cfg *config.Config, vm *virtualization.Manager, wall *wallpapers.Manager, desk *desktop.Configurator, rdpCfg *rdp.Configurator, ip string) error {
	logging.Info("Configuring ALFAOS guest (fonts, desktop, RDP)...")

	if err := wall.ExtractToStateDir(); err != nil {
		return err
	}
	for _, name := range []string{"alfa1.jpeg", "alfa2.jpeg", "alfaos3.png"} {
		local := filepath.Join(cfg.Paths.StateDir, name)
		if err := vm.CopyFile(ip, local, "/tmp/"+name); err != nil {
			return fmt.Errorf("copy wallpaper %s: %w", name, err)
		}
	}

	script := desk.InstallScript(rdpCfg.ConfigScript())
	localScript := filepath.Join(cfg.Paths.StateDir, "guest-setup.sh")
	remoteScript := "/tmp/alfaos-guest-setup.sh"

	if err := os.WriteFile(localScript, []byte(script), 0755); err != nil {
		return fmt.Errorf("write guest script: %w", err)
	}
	if err := vm.CopyFile(ip, localScript, remoteScript); err != nil {
		return fmt.Errorf("copy guest script: %w", err)
	}

	out, err := vm.RunSSH(ip, "chmod +x "+remoteScript+" && bash "+remoteScript)
	if err != nil {
		return fmt.Errorf("guest setup failed: %w\n%s", err, out)
	}

	logging.Success("Guest configured (desktop, fonts, RDP)")
	return nil
}
