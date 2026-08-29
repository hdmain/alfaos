package virtualization

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/alfaos/alfaos/internal/config"
	"github.com/alfaos/alfaos/internal/host"
	"github.com/alfaos/alfaos/internal/logging"
)

type Manager struct {
	cfg *config.Config
}

func New(cfg *config.Config) *Manager {
	return &Manager{cfg: cfg}
}

func (m *Manager) runVirsh(args ...string) (string, error) {
	virshArgs := append([]string{"-c", libvirtSystemURI}, args...)
	out, err := host.RunCommand("virsh", virshArgs...)
	if err == nil {
		return out, nil
	}
	if out, err := host.RunCommand("sudo", append([]string{"-n", "virsh"}, virshArgs...)...); err == nil {
		return out, nil
	}
	return host.RunCommand("sudo", append([]string{"virsh"}, virshArgs...)...)
}

func (m *Manager) DomainExists() bool {
	out, err := m.runVirsh("dominfo", m.cfg.VM.Name)
	if err != nil {
		return false
	}
	return strings.Contains(out, "Name:")
}

func (m *Manager) DomainRunning() bool {
	out, err := m.runVirsh("domstate", m.cfg.VM.Name)
	return err == nil && strings.TrimSpace(out) == "running"
}

func (m *Manager) StartVM() error {
	if !m.DomainExists() {
		return fmt.Errorf("VM %q does not exist — run: sudo alfaos install", m.cfg.VM.Name)
	}
	if m.DomainRunning() {
		logging.Info("VM %s is already running", m.cfg.VM.Name)
		return nil
	}
	_, err := m.runVirsh("start", m.cfg.VM.Name)
	if err != nil {
		return err
	}
	logging.Success("VM %s started", m.cfg.VM.Name)
	return nil
}

func (m *Manager) ShutdownVM(timeout time.Duration) error {
	if !m.DomainExists() {
		return fmt.Errorf("VM %q does not exist — run: sudo alfaos install", m.cfg.VM.Name)
	}
	if !m.DomainRunning() {
		logging.Info("VM %s is already stopped", m.cfg.VM.Name)
		return nil
	}
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}

	logging.Info("Shutting down VM %s...", m.cfg.VM.Name)
	if _, err := m.runVirsh("shutdown", m.cfg.VM.Name); err != nil {
		return err
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !m.DomainRunning() {
			logging.Success("VM %s shut down", m.cfg.VM.Name)
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("VM %s did not shut down within %v", m.cfg.VM.Name, timeout)
}

func (m *Manager) RebootVM() error {
	if !m.DomainExists() {
		return fmt.Errorf("VM %q does not exist — run: sudo alfaos install", m.cfg.VM.Name)
	}
	if !m.DomainRunning() {
		logging.Info("VM %s is stopped, starting...", m.cfg.VM.Name)
		return m.StartVM()
	}

	logging.Info("Rebooting VM %s...", m.cfg.VM.Name)
	if _, err := m.runVirsh("reboot", m.cfg.VM.Name); err != nil {
		return err
	}
	logging.Success("VM %s reboot initiated", m.cfg.VM.Name)
	return nil
}

func (m *Manager) StopVM() error {
	if !m.DomainExists() {
		return nil
	}
	_, err := m.runVirsh("destroy", m.cfg.VM.Name)
	return err
}

func (m *Manager) UndefineVM() error {
	if !m.DomainExists() {
		return nil
	}
	if m.DomainRunning() {
		_ = m.StopVM()
	}
	_, err := host.RunCommand("virsh", "undefine", m.cfg.VM.Name, "--remove-all-storage")
	return err
}

func (m *Manager) CreateVM(isoPath, preseedPath string) error {
	if m.DomainExists() {
		logging.Info("VM %s already exists, skipping creation", m.cfg.VM.Name)
		return nil
	}

	diskPath := fmt.Sprintf("/var/lib/libvirt/images/%s.qcow2", m.cfg.VM.Name)

	location := fmt.Sprintf(
		"%s,kernel=/install.amd/vmlinuz,initrd=/install.amd/initrd.gz",
		isoPath,
	)

	kernelArgs := fmt.Sprintf(
		"auto=true priority=critical preseed/file=/preseed.cfg "+
			"debian-installer/locale=en_US.UTF-8 keyboard-configuration/xkb-keymap=us "+
			"netcfg/choose_interface=auto netcfg/get_hostname=%s netcfg/get_domain=local "+
			"hw-detect/load_firmware=false",
		m.cfg.ALFAOS.Hostname,
	)

	// VirtIO disk (/dev/vda) + VirtIO NIC — much faster than IDE in the guest.
	args := []string{
		"--name", m.cfg.VM.Name,
		"--machine", "pc-i440fx-8.2",
		"--ram", fmt.Sprintf("%d", m.cfg.VM.RAM),
		"--vcpus", fmt.Sprintf("%d", m.cfg.VM.CPU),
		"--disk", fmt.Sprintf("path=%s,size=%d,format=qcow2,bus=virtio", diskPath, m.cfg.VM.Disk),
		"--network", fmt.Sprintf("network=%s,model=virtio", m.cfg.VM.Network),
		"--graphics", m.cfg.VM.Graphics,
		"--console", "pty,target_type=serial",
		"--location", location,
		"--initrd-inject", preseedPath,
		"--extra-args", kernelArgs,
		"--osinfo", "detect=on,name="+m.cfg.OSVariant(),
		"--boot", "hd",
		"--noautoconsole",
		"--check", "path_in_use=off",
		"--events", "on_reboot=restart",
	}

	logging.Info("Creating VM %s with virt-install...", m.cfg.VM.Name)
	cmd := exec.Command("virt-install", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("virt-install failed: %w\n%s", err, out)
	}
	logging.Success("VM %s created", m.cfg.VM.Name)
	return nil
}

func (m *Manager) WaitForShutdown(timeout time.Duration) error {
	logging.Info("Waiting for Debian installation to complete (VM will reboot/shutdown)...")
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !m.DomainRunning() {
			logging.Success("VM shut down — installation likely complete")
			return nil
		}
		time.Sleep(15 * time.Second)
	}
	return fmt.Errorf("timed out waiting for VM installation after %v", timeout)
}

func (m *Manager) RunSSH(ip, command string) (string, error) {
	args := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "ConnectTimeout=10",
		fmt.Sprintf("%s@%s", m.cfg.ALFAOS.Username, ip),
		command,
	}
	cmd := exec.Command("sshpass", append([]string{"-p", m.cfg.ALFAOS.Password, "ssh"}, args...)...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (m *Manager) WaitForSSH(ip string, timeout time.Duration) error {
	logging.Info("Waiting for SSH on %s...", ip)

	if err := m.WaitForPort(ip, "22", minDuration(timeout/2, 5*time.Minute)); err != nil {
		logging.Warn("SSH port not open yet: %v", err)
	}

	deadline := time.Now().Add(timeout)
	attempt := 0
	var lastErr error
	for time.Now().Before(deadline) {
		attempt++
		out, err := m.RunSSH(ip, "echo ready")
		if err == nil && strings.Contains(out, "ready") {
			logging.Success("SSH available on %s", ip)
			return nil
		}
		lastErr = err
		if attempt%6 == 0 {
			logging.Info("SSH not ready on %s (attempt %d)", ip, attempt)
			if !m.isIPReachable(ip) {
				logging.Warn("VM IP %s is not responding to ping", ip)
			}
		}
		time.Sleep(10 * time.Second)
	}

	m.logSSHDiagnostics(ip, lastErr)
	return fmt.Errorf("SSH not available on %s after %v", ip, timeout)
}

func (m *Manager) logSSHDiagnostics(ip string, lastErr error) {
	logging.Warn("SSH connection failed — diagnostics:")
	if lastErr != nil {
		logging.Warn("  last error: %v", lastErr)
	}
	logging.Warn("  ping: %v", m.isIPReachable(ip))
	if out, err := host.RunCommand("nc", "-z", "-w", "3", ip, "22"); err != nil {
		logging.Warn("  port 22: closed (%v)", err)
	} else {
		logging.Warn("  port 22: open")
		_ = out
	}
	if m.IsDebianInstalledOnDisk() {
		logging.Warn("  Debian is installed on disk — VM may still be booting")
	} else {
		logging.Warn("  Debian NOT found on disk — re-run with: sudo /alfaos install --force")
	}
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func (m *Manager) CopyFile(ip, localPath, remotePath string) error {
	args := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		localPath,
		fmt.Sprintf("%s@%s:%s", m.cfg.ALFAOS.Username, ip, remotePath),
	}
	cmd := exec.Command("sshpass", append([]string{"-p", m.cfg.ALFAOS.Password, "scp"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("scp failed: %w\n%s", err, out)
	}
	return nil
}

func (m *Manager) IsKVMInstalled() bool {
	return host.CommandExists("kvm-ok") || host.CommandExists("qemu-system-x86_64")
}

func (m *Manager) IsQEMUInstalled() bool {
	return host.CommandExists("qemu-system-x86_64") || host.CommandExists("qemu-system-aarch64")
}

func (m *Manager) IsLibvirtWorking() bool {
	_, err := host.RunCommand("virsh", "list", "--all")
	return err == nil
}
