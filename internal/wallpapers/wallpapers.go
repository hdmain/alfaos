package wallpapers

import (
	"embed"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/alfaos/alfaos/internal/config"
	"github.com/alfaos/alfaos/internal/logging"
	"github.com/alfaos/alfaos/internal/virtualization"
)

//go:embed assets/alfa1.jpeg assets/alfa2.jpeg
var embeddedFS embed.FS

type Manager struct {
	cfg *config.Config
	vm  *virtualization.Manager
}

func New(cfg *config.Config, vm *virtualization.Manager) *Manager {
	return &Manager{cfg: cfg, vm: vm}
}

func (w *Manager) ExtractToStateDir() error {
	names := []string{"alfa1.jpeg", "alfa2.jpeg"}
	for _, name := range names {
		data, err := embeddedFS.ReadFile("assets/" + name)
		if err != nil {
			// Fallback to filesystem assets
			alt := filepath.Join("assets", name)
			data, err = os.ReadFile(alt)
			if err != nil {
				return fmt.Errorf("read wallpaper %s: %w", name, err)
			}
		}
		dest := filepath.Join(w.cfg.Paths.StateDir, name)
		if err := os.WriteFile(dest, data, 0644); err != nil {
			return err
		}
	}
	return nil
}

func (w *Manager) Install(ip string) error {
	logging.Info("Installing ALFAOS wallpapers on VM...")

	if err := w.ExtractToStateDir(); err != nil {
		return err
	}

	for _, name := range []string{"alfa1.jpeg", "alfa2.jpeg"} {
		local := filepath.Join(w.cfg.Paths.StateDir, name)
		remote := "/tmp/" + name
		if err := w.vm.CopyFile(ip, local, remote); err != nil {
			return fmt.Errorf("copy wallpaper %s: %w", name, err)
		}
	}

	logging.Success("Wallpapers copied to VM")
	return nil
}

func (w *Manager) Verify(ip string) bool {
	out, err := w.vm.RunSSH(ip, "test -f /usr/share/backgrounds/alfaos/alfa1.jpeg && echo ok")
	return err == nil && len(out) > 0
}

// CopyFromWorkspace copies wallpapers from project assets if embed fails at build time.
func CopyFromWorkspace(srcDir, destDir string) error {
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return err
	}
	for _, name := range []string{"alfa1.jpeg", "alfa2.jpeg"} {
		src := filepath.Join(srcDir, name)
		dst := filepath.Join(destDir, name)
		if err := copyFile(src, dst); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}
