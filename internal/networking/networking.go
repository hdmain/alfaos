package networking

import (
	"strings"

	hostpkg "github.com/alfaos/alfaos/internal/host"
	"github.com/alfaos/alfaos/internal/logging"
)

func ConfigureLibvirt() error {
	logging.Info("Configuring libvirt networking...")

	out, err := hostpkg.RunCommand("virsh", "net-list", "--all")
	if err != nil {
		return err
	}

	if !strings.Contains(out, "default") {
		logging.Info("Creating default libvirt network...")
		_, err = hostpkg.RunCommand("virsh", "net-define", "/usr/share/libvirt/networks/default.xml")
		if err != nil {
			_, err = hostpkg.RunCommand("virsh", "net-define", "/etc/libvirt/qemu/networks/default.xml")
			if err != nil {
				logging.Warn("Could not define default network: %v", err)
			}
		}
	}

	_, _ = hostpkg.RunCommand("virsh", "net-autostart", "default")
	_, _ = hostpkg.RunCommand("virsh", "net-start", "default")
	_, _ = hostpkg.RunCommand("sysctl", "-w", "net.ipv4.ip_forward=1")
	_, _ = hostpkg.RunCommand("systemctl", "restart", "libvirtd")

	out, err = hostpkg.RunCommand("virsh", "net-list")
	if err != nil {
		return err
	}
	if strings.Contains(out, "default") && strings.Contains(out, "active") {
		logging.Success("Libvirt default network is active")
	} else {
		logging.Warn("Default network may not be active")
	}

	return nil
}

func TestPort(addr, port string) bool {
	nc := "nc"
	if !hostpkg.CommandExists(nc) {
		nc = "ncat"
	}
	if !hostpkg.CommandExists(nc) {
		out, err := hostpkg.RunCommand("bash", "-c",
			"timeout 3 bash -c 'echo >/dev/tcp/"+addr+"/"+port+"' 2>/dev/null && echo ok")
		return err == nil && strings.Contains(out, "ok")
	}
	_, err := hostpkg.RunCommand(nc, "-z", "-w", "3", addr, port)
	return err == nil
}

func TestPing(addr string) bool {
	_, err := hostpkg.RunCommand("ping", "-c", "1", "-W", "3", addr)
	return err == nil
}
