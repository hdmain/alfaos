package virtualization

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/alfaos/alfaos/internal/host"
	"github.com/alfaos/alfaos/internal/logging"
)

var reRootUUID = regexp.MustCompile(`UUID=([0-9a-f-]+)\s+/`)

// SetupDirectKernelBoot extracts the installed kernel/initrd from disk and
// configures libvirt to boot Linux directly, bypassing SeaBIOS/GRUB.
func (m *Manager) SetupDirectKernelBoot() error {
	if !m.DomainExists() {
		return fmt.Errorf("domain does not exist")
	}

	if m.DomainRunning() {
		if err := m.StopVM(); err != nil {
			return err
		}
	}

	bootDir := filepath.Join(m.cfg.Paths.StateDir, "boot")
	if err := os.MkdirAll(bootDir, 0755); err != nil {
		return err
	}

	rootUUID, err := m.getRootUUID()
	if err != nil {
		return fmt.Errorf("root UUID: %w", err)
	}

	vmlinuzGuest, initrdGuest, err := m.findBootFiles()
	if err != nil {
		return err
	}

	hostVmlinuz := filepath.Join(bootDir, "vmlinuz")
	hostInitrd := filepath.Join(bootDir, "initrd.img")

	if err := m.copyFromGuest(vmlinuzGuest, hostVmlinuz); err != nil {
		return fmt.Errorf("copy vmlinuz: %w", err)
	}
	if err := m.copyFromGuest(initrdGuest, hostInitrd); err != nil {
		return fmt.Errorf("copy initrd: %w", err)
	}

	// Fix network and passwordless sudo for automated remote setup scripts.
	if _, err := host.RunCommand("virt-customize", "-d", m.cfg.VM.Name,
		"--run-command", `printf "auto lo\niface lo inet loopback\n\nauto eth0\niface eth0 inet dhcp\n" > /etc/network/interfaces`,
		"--run-command", `echo "alfaos ALL=(ALL) NOPASSWD:ALL" > /etc/sudoers.d/alfaos && chmod 440 /etc/sudoers.d/alfaos`); err != nil {
		logging.Warn("VM customize (network/sudo): %v", err)
	}

	xml, err := host.RunCommand("virsh", "dumpxml", m.cfg.VM.Name)
	if err != nil {
		return err
	}

	cmdline := fmt.Sprintf(
		"root=UUID=%s ro ip=dhcp net.ifnames=0 biosdevname=0 console=ttyS0,115200n8",
		rootUUID,
	)

	updated := injectDirectKernelBoot(cleanInstallXML(xml), hostVmlinuz, hostInitrd, cmdline)

	xmlPath := filepath.Join(m.cfg.Paths.StateDir, m.cfg.VM.Name+".xml")
	if err := os.WriteFile(xmlPath, []byte(updated), 0644); err != nil {
		return err
	}

	if _, err := host.RunCommand("virsh", "define", xmlPath); err != nil {
		return fmt.Errorf("virsh define: %w", err)
	}

	logging.Success("VM configured for direct kernel boot (bypasses BIOS/GRUB)")
	return nil
}

func (m *Manager) getRootUUID() (string, error) {
	out, err := host.RunCommand("virt-cat", "-d", m.cfg.VM.Name, "/etc/fstab")
	if err != nil {
		return "", err
	}
	matches := reRootUUID.FindStringSubmatch(out)
	if len(matches) < 2 {
		return "", fmt.Errorf("root UUID not found in fstab")
	}
	return matches[1], nil
}

func (m *Manager) findBootFiles() (vmlinuz, initrd string, err error) {
	out, err := host.RunCommand("virt-ls", "-d", m.cfg.VM.Name, "/boot")
	if err != nil {
		return "", "", fmt.Errorf("virt-ls /boot: %w", err)
	}

	var vmlinuzFiles, initrdFiles []string
	for _, name := range strings.Fields(out) {
		if strings.HasPrefix(name, "vmlinuz-") {
			vmlinuzFiles = append(vmlinuzFiles, name)
		}
		if strings.HasPrefix(name, "initrd.img-") {
			initrdFiles = append(initrdFiles, name)
		}
	}

	if len(vmlinuzFiles) == 0 || len(initrdFiles) == 0 {
		return "", "", fmt.Errorf("kernel or initrd not found in /boot")
	}

	vmlinuz = "/boot/" + vmlinuzFiles[len(vmlinuzFiles)-1]
	initrd = "/boot/" + initrdFiles[len(initrdFiles)-1]
	logging.Info("Boot files: %s, %s", vmlinuz, initrd)
	return vmlinuz, initrd, nil
}

func (m *Manager) copyFromGuest(guestPath, hostPath string) error {
	tmpDir := filepath.Dir(hostPath)
	base := filepath.Base(guestPath)

	_, err := host.RunCommand("virt-copy-out", "-d", m.cfg.VM.Name, guestPath, tmpDir)
	if err != nil {
		return err
	}

	downloaded := filepath.Join(tmpDir, base)
	if err := os.Rename(downloaded, hostPath); err != nil {
		// If rename fails (cross-device), copy manually.
		data, readErr := os.ReadFile(downloaded)
		if readErr != nil {
			return fmt.Errorf("rename %s: %w", downloaded, err)
		}
		if writeErr := os.WriteFile(hostPath, data, 0644); writeErr != nil {
			return writeErr
		}
		_ = os.Remove(downloaded)
	}
	return nil
}

func injectDirectKernelBoot(xml, kernel, initrd, cmdline string) string {
	xml = reKernel.ReplaceAllString(xml, "\n")
	xml = reInitrd.ReplaceAllString(xml, "\n")
	xml = reCmdline.ReplaceAllString(xml, "\n")

	kernelBlock := fmt.Sprintf(
		"\n    <kernel>%s</kernel>\n    <initrd>%s</initrd>\n    <cmdline>%s</cmdline>",
		kernel, initrd, cmdline,
	)

	return reOSBlock.ReplaceAllStringFunc(xml, func(block string) string {
		parts := reOSBlock.FindStringSubmatch(block)
		if len(parts) < 4 {
			return block
		}
		inner := reBootDev.ReplaceAllString(parts[2], "")
		inner = strings.TrimSpace(inner)
		if !strings.Contains(inner, "machine=") {
			inner = "<type arch='x86_64' machine='pc-i440fx-8.2'>hvm</type>"
		}
		return parts[1] + "\n    " + inner + kernelBlock + "\n  " + parts[3]
	})
}
