package virtualization

import (
	"strings"

	"github.com/alfaos/alfaos/internal/host"
	"github.com/alfaos/alfaos/internal/logging"
)

// IsDebianInstalledOnDisk inspects the VM disk offline to verify Debian was installed.
func (m *Manager) IsDebianInstalledOnDisk() bool {
	if !m.DomainExists() {
		return false
	}

	wasRunning := m.DomainRunning()
	if wasRunning {
		if err := m.StopVM(); err != nil {
			logging.Warn("Could not stop VM for disk inspection: %v", err)
		}
		defer func() {
			if wasRunning {
				_ = m.StartVM()
			}
		}()
	}

	checks := []struct {
		name string
		args []string
	}{
		{"virt-cat", []string{"virt-cat", "-d", m.cfg.VM.Name, "/etc/debian_version"}},
		{"guestfish", []string{"guestfish", "--ro", "-d", m.cfg.VM.Name, "-i", "cat", "/etc/debian_version"}},
	}

	for _, check := range checks {
		if !host.CommandExists(check.args[0]) {
			continue
		}
		out, err := host.RunCommand(check.args[0], check.args[1:]...)
		if err == nil && strings.TrimSpace(out) != "" {
			logging.Info("Debian %s found on VM disk (via %s)", strings.TrimSpace(out), check.name)
			return true
		}
	}

	logging.Warn("No Debian installation found on VM disk")
	return false
}

func (m *Manager) IsSSHConfiguredOnDisk() bool {
	if !m.DomainExists() {
		return false
	}

	out, err := host.RunCommand("virt-cat", "-d", m.cfg.VM.Name, "/etc/ssh/sshd_config")
	if err != nil {
		return false
	}
	return strings.Contains(out, "openssh-server") ||
		strings.Contains(strings.ToLower(out), "passwordauthentication yes")
}
