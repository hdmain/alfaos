package networking

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	hostpkg "github.com/alfaos/alfaos/internal/host"
	"github.com/alfaos/alfaos/internal/logging"
)

// ExposeRDP listens on bindAddr:hostPort and proxies TCP to the VM RDP port.
func ExposeRDP(stateDir, bindAddr string, hostPort int, vmIP string, vmPort int) error {
	if hostPort <= 0 {
		hostPort = 3389
	}
	if vmPort <= 0 {
		vmPort = 3389
	}
	if bindAddr == "" {
		bindAddr = "0.0.0.0"
	}

	logging.Info("Exposing RDP on %s:%d → VM %s:%d (socat proxy)", bindAddr, hostPort, vmIP, vmPort)

	if err := ensureSocat(); err != nil {
		return err
	}

	removeLegacyIPTables(hostPort, vmIP, vmPort)
	allowHostPort(hostPort)

	if !TestPort(vmIP, fmt.Sprintf("%d", vmPort)) {
		logging.Warn("VM RDP at %s:%d is not reachable from host yet", vmIP, vmPort)
	} else {
		logging.Info("VM RDP reachable at %s:%d", vmIP, vmPort)
	}

	if err := writeSocatScripts(stateDir, bindAddr, hostPort, vmPort); err != nil {
		return fmt.Errorf("write socat scripts: %w", err)
	}
	if err := installSocatService(stateDir, bindAddr, hostPort); err != nil {
		return fmt.Errorf("install socat service: %w", err)
	}

	time.Sleep(500 * time.Millisecond)
	if TestPort("127.0.0.1", fmt.Sprintf("%d", hostPort)) {
		logging.Success("RDP proxy listening on %s:%d", bindAddr, hostPort)
	} else {
		logging.Warn("RDP proxy may not be listening — check: systemctl status alfaos-rdp-forward")
	}

	printExposureHints(hostPort)
	return nil
}

func ensureSocat() error {
	if hostpkg.CommandExists("socat") {
		return nil
	}
	logging.Info("Installing socat for RDP proxy...")
	if _, err := hostpkg.RunCommand("apt-get", "install", "-y", "-qq", "--no-install-recommends", "socat"); err != nil {
		_, err = hostpkg.RunCommand("apt-get", "-o", "Dir::Etc::sourceparts=/dev/null", "install", "-y", "-qq", "--no-install-recommends", "socat")
		if err != nil {
			return fmt.Errorf("install socat: %w", err)
		}
	}
	return nil
}

func allowHostPort(port int) {
	portStr := fmt.Sprintf("%d", port)
	if hostpkg.CommandExists("ufw") {
		out, _ := hostpkg.RunCommand("ufw", "status")
		if strings.Contains(strings.ToLower(out), "active") {
			_, _ = hostpkg.RunCommand("ufw", "allow", portStr+"/tcp")
			logging.Info("Opened port %s/tcp in ufw", portStr)
		}
	}
	iptablesEnsure("filter", "INPUT", "-p", "tcp", "--dport", portStr, "-j", "ACCEPT")
}

func removeLegacyIPTables(hostPort int, vmIP string, vmPort int) {
	portStr := fmt.Sprintf("%d", hostPort)
	vmPortStr := fmt.Sprintf("%d", vmPort)
	vmDest := fmt.Sprintf("%s:%s", vmIP, vmPortStr)

	legacy := [][]string{
		{"nat", "PREROUTING", "-p", "tcp", "--dport", portStr, "-j", "DNAT", "--to-destination", vmDest},
		{"nat", "OUTPUT", "-p", "tcp", "--dport", portStr, "-m", "addrtype", "--dst-type", "LOCAL", "-j", "DNAT", "--to-destination", vmDest},
		{"nat", "POSTROUTING", "-p", "tcp", "-d", vmIP, "--dport", vmPortStr, "-j", "MASQUERADE"},
		{"filter", "FORWARD", "-p", "tcp", "-d", vmIP, "--dport", vmPortStr, "-m", "state", "--state", "NEW,ESTABLISHED,RELATED", "-j", "ACCEPT"},
		{"filter", "FORWARD", "-p", "tcp", "-s", vmIP, "--sport", vmPortStr, "-m", "state", "--state", "ESTABLISHED,RELATED", "-j", "ACCEPT"},
	}
	for _, rule := range legacy {
		iptablesDelete(rule[0], rule[1], rule[2:]...)
	}
}

func iptablesEnsure(table, chain string, args ...string) {
	checkArgs := append([]string{"-t", table, "-C", chain}, args...)
	if _, err := hostpkg.RunCommand("iptables", checkArgs...); err == nil {
		return
	}
	addArgs := append([]string{"-t", table, "-A", chain}, args...)
	_, _ = hostpkg.RunCommand("iptables", addArgs...)
}

func iptablesDelete(table, chain string, args ...string) {
	delArgs := append([]string{"-t", table, "-D", chain}, args...)
	_, _ = hostpkg.RunCommand("iptables", delArgs...)
}

func writeSocatScripts(stateDir, bindAddr string, hostPort, vmPort int) error {
	runPath := filepath.Join(stateDir, "run-rdp-proxy.sh")
	ipFile := filepath.Join(stateDir, "vm.ip")
	vmNameFile := filepath.Join(stateDir, "vm.name")

	vmName := "alfaos"
	if data, err := os.ReadFile(vmNameFile); err == nil {
		if n := strings.TrimSpace(string(data)); n != "" {
			vmName = n
		}
	}

	bindOpt := ""
	if bindAddr != "" && bindAddr != "0.0.0.0" {
		bindOpt = fmt.Sprintf(",bind=%s", bindAddr)
	}

	script := fmt.Sprintf(`#!/bin/bash
set -euo pipefail
VM_IP=""
if [ -f %q ]; then
  VM_IP=$(tr -d '[:space:]' < %q)
fi
if [ -z "$VM_IP" ]; then
  VM_IP=$(virsh domifaddr %q 2>/dev/null | awk '/ipv4/ {gsub(/\/.*/,"",$4); print $4; exit}')
fi
if [ -z "$VM_IP" ]; then
  echo "ALFAOS: could not resolve VM IP for RDP proxy" >&2
  exit 1
fi
exec /usr/bin/socat TCP-LISTEN:%d,reuseaddr,fork%s TCP:${VM_IP}:%d
`, ipFile, ipFile, vmName, hostPort, bindOpt, vmPort)

	return os.WriteFile(runPath, []byte(script), 0755)
}

func installSocatService(stateDir, bindAddr string, hostPort int) error {
	unitPath := "/etc/systemd/system/alfaos-rdp-forward.service"
	runPath := filepath.Join(stateDir, "run-rdp-proxy.sh")

	unit := fmt.Sprintf(`[Unit]
Description=ALFAOS RDP TCP proxy to VM (socat)
After=network-online.target libvirtd.service
Wants=network-online.target

[Service]
Type=simple
Restart=on-failure
RestartSec=3
ExecStart=%s

[Install]
WantedBy=multi-user.target
`, runPath)

	if err := os.WriteFile(unitPath, []byte(unit), 0644); err != nil {
		return err
	}
	_, _ = hostpkg.RunCommand("systemctl", "daemon-reload")
	_, _ = hostpkg.RunCommand("systemctl", "enable", "alfaos-rdp-forward.service")
	_, _ = hostpkg.RunCommand("systemctl", "restart", "alfaos-rdp-forward.service")
	return nil
}

func printExposureHints(hostPort int) {
	if ip := GetHostPrimaryIPv4(); ip != "" {
		logging.Info("Connect from outside: rdesktop %s -u alfaos -p alfaos -g 1920x1080", ip)
	}
	logging.Info("Verify on VPS: ss -tlnp | grep %d", hostPort)
	logging.Info("If external connect still fails, open TCP %d in your VPS provider firewall/panel", hostPort)
}

// GetHostPrimaryIPv4 returns the primary outbound IPv4 address of this host.
func GetHostPrimaryIPv4() string {
	out, err := hostpkg.RunCommand("bash", "-c",
		`ip -4 route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src") {print $(i+1); exit}}'`)
	if err == nil {
		if ip := strings.TrimSpace(out); ip != "" && ip != "127.0.0.1" {
			return ip
		}
	}

	out, err = hostpkg.RunCommand("hostname", "-I")
	if err == nil {
		for _, ip := range strings.Fields(out) {
			if !strings.HasPrefix(ip, "127.") && !strings.HasPrefix(ip, "192.168.122.") {
				return ip
			}
		}
	}
	return ""
}

// RDPConnectAddress returns the address clients should use for RDP.
func RDPConnectAddress(cfgHostPort int, exposed bool, vmIP string) string {
	if !exposed {
		return vmIP
	}
	if ip := GetHostPrimaryIPv4(); ip != "" {
		return ip
	}
	return vmIP
}
