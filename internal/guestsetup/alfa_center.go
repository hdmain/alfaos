package guestsetup

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/alfaos/alfaos/internal/centerapi"
	"github.com/alfaos/alfaos/internal/config"
	"github.com/alfaos/alfaos/internal/logging"
	"github.com/alfaos/alfaos/internal/virtualization"
)

// InstallAlfaCenter copies the Alfa Center GUI into the guest and writes API credentials.
func InstallAlfaCenter(cfg *config.Config, vm *virtualization.Manager, ip string) error {
	logging.Info("Installing Alfa Center (guest settings UI)...")

	token, err := centerapi.EnsureToken(cfg.Paths.StateDir)
	if err != nil {
		return fmt.Errorf("center token: %w", err)
	}
	apiURL := centerapi.APIURLForGuest(cfg)

	binLocal, err := resolveAlfaCenterBinary(cfg.Paths.StateDir)
	if err != nil {
		logging.Warn("Alfa Center binary not available: %v", err)
		logging.Warn("Build it with: scripts/build-alfa-center.sh (or cargo build --release in guest/alfa-center)")
		return nil
	}

	if err := vm.CopyFile(ip, binLocal, "/tmp/alfa-center"); err != nil {
		return fmt.Errorf("copy alfa-center: %w", err)
	}

	confLocal := filepath.Join(cfg.Paths.StateDir, "center.conf")
	if err := os.WriteFile(confLocal, []byte(centerapi.GuestConfigContent(apiURL, token)), 0640); err != nil {
		return err
	}
	if err := vm.CopyFile(ip, confLocal, "/tmp/alfaos-center.conf"); err != nil {
		return fmt.Errorf("copy center.conf: %w", err)
	}

	script := `#!/bin/bash
set -euo pipefail
sudo install -m 755 /tmp/alfa-center /usr/local/bin/alfa-center
sudo mkdir -p /etc/alfaos
sudo install -m 640 /tmp/alfaos-center.conf /etc/alfaos/center.conf
sudo chown root:alfaos /etc/alfaos/center.conf 2>/dev/null || sudo chown root:root /etc/alfaos/center.conf

# OpenGL / EGL for egui under xorgxrdp
sudo apt-get install -y -qq libegl1 libgl1-mesa-dri libglx-mesa0 mesa-utils 2>/dev/null || true

mkdir -p /home/alfaos/.local/share/applications /home/alfaos/Desktop

cat > /home/alfaos/.local/share/applications/alfa-center.desktop << 'EOF'
[Desktop Entry]
Version=1.0
Type=Application
Name=Alfa Center
Comment=ALFAOS settings — quality, password, privacy, power
Exec=/usr/local/bin/alfa-center
Icon=preferences-system
Terminal=false
Categories=Settings;System;
StartupNotify=true
EOF

cp /home/alfaos/.local/share/applications/alfa-center.desktop /home/alfaos/Desktop/alfa-center.desktop
chmod +x /home/alfaos/Desktop/alfa-center.desktop /home/alfaos/.local/share/applications/alfa-center.desktop

# Mark desktop icon as trusted (XFCE / gio)
if command -v gio >/dev/null 2>&1; then
  gio set /home/alfaos/Desktop/alfa-center.desktop metadata::trusted true 2>/dev/null || true
fi

# Optional Plank launcher
if [ -d /home/alfaos/.config/plank/dock1/launchers ]; then
  cat > /home/alfaos/.config/plank/dock1/launchers/alfa-center.dockitem << 'ITEM'
[PlankDockItem]
Launcher=file:///home/alfaos/.local/share/applications/alfa-center.desktop
ITEM
fi

sudo chown -R alfaos:alfaos /home/alfaos/Desktop /home/alfaos/.local/share/applications
sudo chown alfaos:alfaos /home/alfaos/.config/plank/dock1/launchers/alfa-center.dockitem 2>/dev/null || true
echo "Alfa Center installed"
`
	localScript := filepath.Join(cfg.Paths.StateDir, "alfa-center-install.sh")
	if err := os.WriteFile(localScript, []byte(script), 0755); err != nil {
		return err
	}
	remote := "/tmp/alfa-center-install.sh"
	if err := vm.CopyFile(ip, localScript, remote); err != nil {
		return err
	}
	out, err := vm.RunSSH(ip, "chmod +x "+remote+" && bash "+remote)
	if err != nil {
		return fmt.Errorf("alfa-center install: %w\n%s", err, out)
	}

	logging.Success("Alfa Center installed on guest desktop (API %s)", apiURL)
	return nil
}

func resolveAlfaCenterBinary(stateDir string) (string, error) {
	candidates := []string{
		filepath.Join(stateDir, "alfa-center"),
		"/usr/share/alfaos/alfa-center",
		"/usr/local/share/alfaos/alfa-center",
		"guest/alfa-center/dist/alfa-center",
		"guest/alfa-center/target/release/alfa-center",
	}
	// Next to the alfaos module root when running from repo
	if exe, err := os.Executable(); err == nil {
		root := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(root, "guest/alfa-center/dist/alfa-center"),
			filepath.Join(root, "..", "guest/alfa-center/dist/alfa-center"),
		)
	}
	// Walk up from cwd looking for the crate
	if wd, err := os.Getwd(); err == nil {
		dir := wd
		for i := 0; i < 6; i++ {
			candidates = append(candidates,
				filepath.Join(dir, "guest/alfa-center/dist/alfa-center"),
				filepath.Join(dir, "guest/alfa-center/target/release/alfa-center"),
			)
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}

	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() && st.Size() > 0 {
			return c, nil
		}
	}

	// Try building on host when cargo is available (Linux install hosts).
	if runtime.GOOS == "linux" {
		crate := findAlfaCenterCrate()
		if crate != "" {
			logging.Info("Building Alfa Center from source (%s)...", crate)
			cmd := exec.Command("cargo", "build", "--release")
			cmd.Dir = crate
			cmd.Env = append(os.Environ(), "CARGO_TERM_COLOR=never")
			if out, err := cmd.CombinedOutput(); err != nil {
				return "", fmt.Errorf("cargo build: %w\n%s", err, out)
			}
			built := filepath.Join(crate, "target/release/alfa-center")
			distDir := filepath.Join(crate, "dist")
			_ = os.MkdirAll(distDir, 0755)
			dist := filepath.Join(distDir, "alfa-center")
			_ = copyFileLocal(built, dist)
			_ = os.MkdirAll(stateDir, 0755)
			stateBin := filepath.Join(stateDir, "alfa-center")
			if err := copyFileLocal(built, stateBin); err == nil {
				return stateBin, nil
			}
			return built, nil
		}
	}

	return "", fmt.Errorf("binary not found (expected guest/alfa-center/dist/alfa-center)")
}

func findAlfaCenterCrate() string {
	if wd, err := os.Getwd(); err == nil {
		dir := wd
		for i := 0; i < 6; i++ {
			p := filepath.Join(dir, "guest/alfa-center/Cargo.toml")
			if _, err := os.Stat(p); err == nil {
				return filepath.Join(dir, "guest/alfa-center")
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	return ""
}

func copyFileLocal(src, dst string) error {
	in, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, in, 0755)
}
