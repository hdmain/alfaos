package guestsetup

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/alfaos/alfaos/internal/centerapi"
	"github.com/alfaos/alfaos/internal/config"
	"github.com/alfaos/alfaos/internal/logging"
	"github.com/alfaos/alfaos/internal/rdp"
	"github.com/alfaos/alfaos/internal/virtualization"
)

const (
	defaultGitHubRepo = "hdmain/alfaos"
	defaultBranch     = "main"
	minBinaryBytes    = 1024 * 100 // reject tiny HTML/error responses
)

// InstallAlfaCenter copies the Alfa Center GUI into the guest and writes API credentials.
// When refresh is true (alfaos center-install), always re-download from GitHub so the
// guest is not stuck on a stale /var/lib/alfaos/state/alfa-center cache.
func InstallAlfaCenter(cfg *config.Config, vm *virtualization.Manager, ip string, refresh bool) error {
	logging.Info("Installing Alfa Center (guest settings UI)...")

	token, err := centerapi.EnsureToken(cfg.Paths.StateDir)
	if err != nil {
		return fmt.Errorf("center token: %w", err)
	}
	apiURL := centerapi.APIURLForGuest(cfg)

	binLocal, err := resolveAlfaCenterBinary(cfg.Paths.StateDir, refresh)
	if err != nil {
		return fmt.Errorf("Alfa Center binary: %w", err)
	}
	if st, err := os.Stat(binLocal); err == nil {
		logging.Info("Binary: %s (%d bytes, mtime %s)", binLocal, st.Size(), st.ModTime().UTC().Format(time.RFC3339))
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
# Stop any running UI so the new binary is used on next launch
pkill -x alfa-center 2>/dev/null || true
sleep 0.5

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

echo "Alfa Center installed:"
ls -la /usr/local/bin/alfa-center
sha256sum /usr/local/bin/alfa-center | awk '{print "sha256:", $1}'
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
	if strings.TrimSpace(out) != "" {
		logging.Info("%s", strings.TrimSpace(out))
	}

	if err := rdp.New(cfg, vm).InstallQualityHooks(ip); err != nil {
		logging.Warn("quality persistence hooks: %v", err)
	}

	logging.Success("Alfa Center installed on guest desktop (API %s)", apiURL)
	logging.Info("Close Alfa Center if it is open, then launch it again from the Desktop")
	return nil
}

func resolveAlfaCenterBinary(stateDir string, refresh bool) (string, error) {
	if refresh {
		logging.Info("Refreshing Alfa Center binary from GitHub...")
		path, err := downloadAlfaCenter(stateDir)
		if err == nil {
			return path, nil
		}
		logging.Warn("Download failed (%v) — falling back to local binary", err)
	}

	if path, ok := findLocalAlfaCenter(stateDir); ok {
		logging.Info("Using local Alfa Center binary: %s", path)
		return path, nil
	}

	// Try building on host when cargo + sources are available (dev machines).
	if runtime.GOOS == "linux" {
		if crate := findAlfaCenterCrate(); crate != "" {
			logging.Info("Building Alfa Center from source (%s)...", crate)
			cmd := exec.Command("cargo", "build", "--release")
			cmd.Dir = crate
			cmd.Env = append(os.Environ(), "CARGO_TERM_COLOR=never")
			if out, err := cmd.CombinedOutput(); err != nil {
				logging.Warn("cargo build failed: %v", err)
				_ = out
			} else {
				built := filepath.Join(crate, "target/release/alfa-center")
				stateBin := filepath.Join(stateDir, "alfa-center")
				_ = os.MkdirAll(stateDir, 0755)
				if err := copyFileLocal(built, stateBin); err == nil {
					return stateBin, nil
				}
				return built, nil
			}
		}
	}

	return downloadAlfaCenter(stateDir)
}

func findLocalAlfaCenter(stateDir string) (string, bool) {
	candidates := []string{
		filepath.Join(stateDir, "alfa-center"),
		"/usr/share/alfaos/alfa-center",
		"/usr/local/share/alfaos/alfa-center",
		"guest/alfa-center/dist/alfa-center",
		"guest/alfa-center/target/release/alfa-center",
	}
	if exe, err := os.Executable(); err == nil {
		root := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(root, "guest/alfa-center/dist/alfa-center"),
			filepath.Join(root, "..", "guest/alfa-center/dist/alfa-center"),
			filepath.Join(root, "alfa-center"),
		)
	}
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
		if st, err := os.Stat(c); err == nil && !st.IsDir() && st.Size() >= minBinaryBytes {
			if isELF(c) {
				return c, true
			}
		}
	}
	return "", false
}

func downloadAlfaCenter(stateDir string) (string, error) {
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return "", err
	}
	dest := filepath.Join(stateDir, "alfa-center")
	repo := strings.TrimSpace(os.Getenv("ALFAOS_GITHUB"))
	if repo == "" {
		repo = defaultGitHubRepo
	}
	branch := strings.TrimSpace(os.Getenv("ALFAOS_BRANCH"))
	if branch == "" {
		branch = defaultBranch
	}
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" {
		return "", fmt.Errorf("invalid ALFAOS_GITHUB %q (want owner/repo)", repo)
	}

	urls := []string{
		// Prefer raw main (always latest commit) — Pages can lag behind CI deploy.
		fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/guest/alfa-center/dist/alfa-center?t=%d", repo, branch, time.Now().Unix()),
		fmt.Sprintf("https://github.com/%s/raw/%s/guest/alfa-center/dist/alfa-center?t=%d", repo, branch, time.Now().Unix()),
		fmt.Sprintf("https://%s.github.io/%s/alfa-center?t=%d", owner, name, time.Now().Unix()),
	}

	var lastErr error
	for _, url := range urls {
		logging.Info("Downloading Alfa Center from %s ...", url)
		if err := downloadBinary(url, dest); err != nil {
			logging.Warn("download failed: %v", err)
			lastErr = err
			continue
		}
		logging.Success("Alfa Center downloaded to %s", dest)
		return dest, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no download URLs tried")
	}
	return "", fmt.Errorf("could not download Alfa Center: %w", lastErr)
}

func downloadBinary(url, dest string) error {
	client := &http.Client{Timeout: 5 * time.Minute}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "alfaos-center-install")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	tmp := dest + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, resp.Body)
	closeErr := f.Close()
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return closeErr
	}
	if n < minBinaryBytes {
		_ = os.Remove(tmp)
		return fmt.Errorf("file too small (%d bytes) — not a binary", n)
	}
	if !isELF(tmp) {
		_ = os.Remove(tmp)
		return fmt.Errorf("downloaded file is not an ELF binary")
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	_ = os.Chmod(dest, 0755)
	return nil
}

func isELF(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var magic [4]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil {
		return false
	}
	return magic[0] == 0x7f && magic[1] == 'E' && magic[2] == 'L' && magic[3] == 'F'
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
