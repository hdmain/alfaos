package networking

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	hostpkg "github.com/alfaos/alfaos/internal/host"
	"github.com/alfaos/alfaos/internal/logging"
)

// ExposeRDP forwards hostPort on bindAddr to the VM RDP port via iptables NAT.
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

	logging.Info("Exposing RDP on %s:%d → VM %s:%d", bindAddr, hostPort, vmIP, vmPort)

	_, _ = hostpkg.RunCommand("sysctl", "-w", "net.ipv4.ip_forward=1")

	if hostpkg.CommandExists("ufw") {
		_, _ = hostpkg.RunCommand("ufw", "allow", fmt.Sprintf("%d/tcp", hostPort))
	}

	applyRules(bindAddr, hostPort, vmIP, vmPort)

	if err := writeForwardScripts(stateDir, bindAddr, hostPort, vmPort); err != nil {
		logging.Warn("Could not persist RDP forward rules: %v", err)
	} else if err := installForwardService(stateDir); err != nil {
		logging.Warn("Could not install RDP forward service: %v", err)
	}

	logging.Success("RDP exposed on %s:%d (connect from outside with host IP)", bindAddr, hostPort)
	return nil
}

func applyRules(bindAddr string, hostPort int, vmIP string, vmPort int) {
	vmDest := fmt.Sprintf("%s:%d", vmIP, vmPort)
	portStr := fmt.Sprintf("%d", hostPort)

	// Incoming connections to the host (external clients).
	iptablesEnsure("nat", "PREROUTING",
		"-p", "tcp", "--dport", portStr,
		"-j", "DNAT", "--to-destination", vmDest)

	// Connections from the host itself (alfaos connect via localhost/public IP).
	if bindAddr == "0.0.0.0" || bindAddr == "" {
		iptablesEnsure("nat", "OUTPUT",
			"-p", "tcp", "--dport", portStr,
			"-m", "addrtype", "--dst-type", "LOCAL",
			"-j", "DNAT", "--to-destination", vmDest)
	} else {
		iptablesEnsure("nat", "OUTPUT",
			"-p", "tcp", "-d", bindAddr, "--dport", portStr,
			"-j", "DNAT", "--to-destination", vmDest)
	}

	iptablesEnsure("nat", "POSTROUTING",
		"-p", "tcp", "-d", vmIP, "--dport", fmt.Sprintf("%d", vmPort),
		"-j", "MASQUERADE")

	iptablesEnsure("filter", "FORWARD",
		"-p", "tcp", "-d", vmIP, "--dport", fmt.Sprintf("%d", vmPort),
		"-m", "state", "--state", "NEW,ESTABLISHED,RELATED", "-j", "ACCEPT")
	iptablesEnsure("filter", "FORWARD",
		"-p", "tcp", "-s", vmIP, "--sport", fmt.Sprintf("%d", vmPort),
		"-m", "state", "--state", "ESTABLISHED,RELATED", "-j", "ACCEPT")
}

func iptablesEnsure(table, chain string, args ...string) {
	checkArgs := append([]string{"-t", table, "-C", chain}, args...)
	if _, err := hostpkg.RunCommand("iptables", checkArgs...); err == nil {
		return
	}
	addArgs := append([]string{"-t", table, "-A", chain}, args...)
	_, _ = hostpkg.RunCommand("iptables", addArgs...)
}

func writeForwardScripts(stateDir, bindAddr string, hostPort, vmPort int) error {
	applyPath := filepath.Join(stateDir, "apply-rdp-forward.sh")
	removePath := filepath.Join(stateDir, "remove-rdp-forward.sh")
	ipFile := filepath.Join(stateDir, "vm.ip")
	vmNameFile := filepath.Join(stateDir, "vm.name")

	vmName := "alfaos"
	if data, err := os.ReadFile(vmNameFile); err == nil {
		if n := strings.TrimSpace(string(data)); n != "" {
			vmName = n
		}
	}

	applyScript := fmt.Sprintf(`#!/bin/bash
set -euo pipefail
VM_IP=""
if [ -f %q ]; then
  VM_IP=$(tr -d '[:space:]' < %q)
fi
if [ -z "$VM_IP" ]; then
  VM_IP=$(virsh domifaddr %q 2>/dev/null | awk '/ipv4/ {gsub(/\/.*/,"",$4); print $4; exit}')
fi
if [ -z "$VM_IP" ]; then
  echo "ALFAOS: could not resolve VM IP for RDP forward" >&2
  exit 1
fi
sysctl -w net.ipv4.ip_forward=1 >/dev/null
%s
`, ipFile, ipFile, vmName, forwardRuleShell(bindAddr, hostPort, vmPort, "$VM_IP"))

	removeScript := fmt.Sprintf(`#!/bin/bash
set -euo pipefail
VM_IP=""
if [ -f %q ]; then
  VM_IP=$(tr -d '[:space:]' < %q)
fi
if [ -z "$VM_IP" ]; then
  VM_IP=$(virsh domifaddr %q 2>/dev/null | awk '/ipv4/ {gsub(/\/.*/,"",$4); print $4; exit}')
fi
[ -n "$VM_IP" ] || exit 0
%s
`, ipFile, ipFile, vmName, removeRuleShell(bindAddr, hostPort, vmPort, "$VM_IP"))

	if err := os.WriteFile(applyPath, []byte(applyScript), 0755); err != nil {
		return err
	}
	return os.WriteFile(removePath, []byte(removeScript), 0755)
}

func forwardRuleShell(bindAddr string, hostPort, vmPort int, vmIPVar string) string {
	portStr := fmt.Sprintf("%d", hostPort)
	vmPortStr := fmt.Sprintf("%d", vmPort)
	lines := []string{
		fmt.Sprintf(`iptables -t nat -C PREROUTING -p tcp --dport %s -j DNAT --to-destination %s:%s 2>/dev/null || \
iptables -t nat -A PREROUTING -p tcp --dport %s -j DNAT --to-destination %s:%s`, portStr, vmIPVar, vmPortStr, portStr, vmIPVar, vmPortStr),
	}
	if bindAddr == "0.0.0.0" || bindAddr == "" {
		lines = append(lines,
			fmt.Sprintf(`iptables -t nat -C OUTPUT -p tcp --dport %s -m addrtype --dst-type LOCAL -j DNAT --to-destination %s:%s 2>/dev/null || \
iptables -t nat -A OUTPUT -p tcp --dport %s -m addrtype --dst-type LOCAL -j DNAT --to-destination %s:%s`, portStr, vmIPVar, vmPortStr, portStr, vmIPVar, vmPortStr),
		)
	} else {
		lines = append(lines,
			fmt.Sprintf(`iptables -t nat -C OUTPUT -p tcp -d %s --dport %s -j DNAT --to-destination %s:%s 2>/dev/null || \
iptables -t nat -A OUTPUT -p tcp -d %s --dport %s -j DNAT --to-destination %s:%s`, bindAddr, portStr, vmIPVar, vmPortStr, bindAddr, portStr, vmIPVar, vmPortStr),
		)
	}
	lines = append(lines,
		fmt.Sprintf(`iptables -t nat -C POSTROUTING -p tcp -d %s --dport %s -j MASQUERADE 2>/dev/null || \
iptables -t nat -A POSTROUTING -p tcp -d %s --dport %s -j MASQUERADE`, vmIPVar, vmPortStr, vmIPVar, vmPortStr),
		fmt.Sprintf(`iptables -C FORWARD -p tcp -d %s --dport %s -m state --state NEW,ESTABLISHED,RELATED -j ACCEPT 2>/dev/null || \
iptables -A FORWARD -p tcp -d %s --dport %s -m state --state NEW,ESTABLISHED,RELATED -j ACCEPT`, vmIPVar, vmPortStr, vmIPVar, vmPortStr),
		fmt.Sprintf(`iptables -C FORWARD -p tcp -s %s --sport %s -m state --state ESTABLISHED,RELATED -j ACCEPT 2>/dev/null || \
iptables -A FORWARD -p tcp -s %s --sport %s -m state --state ESTABLISHED,RELATED -j ACCEPT`, vmIPVar, vmPortStr, vmIPVar, vmPortStr),
	)
	return strings.Join(lines, "\n")
}

func removeRuleShell(bindAddr string, hostPort, vmPort int, vmIPVar string) string {
	portStr := fmt.Sprintf("%d", hostPort)
	vmPortStr := fmt.Sprintf("%d", vmPort)
	lines := []string{
		fmt.Sprintf(`iptables -t nat -D PREROUTING -p tcp --dport %s -j DNAT --to-destination %s:%s 2>/dev/null || true`, portStr, vmIPVar, vmPortStr),
	}
	if bindAddr == "0.0.0.0" || bindAddr == "" {
		lines = append(lines,
			fmt.Sprintf(`iptables -t nat -D OUTPUT -p tcp --dport %s -m addrtype --dst-type LOCAL -j DNAT --to-destination %s:%s 2>/dev/null || true`, portStr, vmIPVar, vmPortStr),
		)
	} else {
		lines = append(lines,
			fmt.Sprintf(`iptables -t nat -D OUTPUT -p tcp -d %s --dport %s -j DNAT --to-destination %s:%s 2>/dev/null || true`, bindAddr, portStr, vmIPVar, vmPortStr),
		)
	}
	lines = append(lines,
		fmt.Sprintf(`iptables -t nat -D POSTROUTING -p tcp -d %s --dport %s -j MASQUERADE 2>/dev/null || true`, vmIPVar, vmPortStr),
		fmt.Sprintf(`iptables -D FORWARD -p tcp -d %s --dport %s -m state --state NEW,ESTABLISHED,RELATED -j ACCEPT 2>/dev/null || true`, vmIPVar, vmPortStr),
		fmt.Sprintf(`iptables -D FORWARD -p tcp -s %s --sport %s -m state --state ESTABLISHED,RELATED -j ACCEPT 2>/dev/null || true`, vmIPVar, vmPortStr),
	)
	return strings.Join(lines, "\n")
}

func installForwardService(stateDir string) error {
	unitPath := "/etc/systemd/system/alfaos-rdp-forward.service"
	applyPath := filepath.Join(stateDir, "apply-rdp-forward.sh")
	unit := fmt.Sprintf(`[Unit]
Description=ALFAOS RDP port forward to VM
After=network-online.target libvirtd.service
Wants=network-online.target

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=%s
ExecStop=%s

[Install]
WantedBy=multi-user.target
`, applyPath, filepath.Join(stateDir, "remove-rdp-forward.sh"))

	if err := os.WriteFile(unitPath, []byte(unit), 0644); err != nil {
		return err
	}
	_, _ = hostpkg.RunCommand("systemctl", "daemon-reload")
	_, _ = hostpkg.RunCommand("systemctl", "enable", "alfaos-rdp-forward.service")
	_, _ = hostpkg.RunCommand("systemctl", "restart", "alfaos-rdp-forward.service")
	return nil
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
