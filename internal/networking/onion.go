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

const (
	torTransPort = 9040
	torDNSPort   = 5353
	onionChain   = "ALFAOS_ONION"
	torrcDropIn  = "/etc/tor/torrc.d/alfaos.conf"
	onionUnit    = "/etc/systemd/system/alfaos-onion.service"
)

// ConfigureOnioning enables or disables transparent Tor for all VM outbound traffic.
// RDP stays reachable: host→VM traffic is not redirected; only guest→internet goes through Tor.
func ConfigureOnioning(enabled bool, stateDir, libvirtNetwork string) error {
	if libvirtNetwork == "" {
		libvirtNetwork = "default"
	}
	if stateDir == "" {
		stateDir = "/var/lib/alfaos/state"
	}
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return err
	}

	bridge := libvirtBridge(libvirtNetwork)
	subnet := libvirtSubnet(libvirtNetwork)

	if !enabled {
		return disableOnioning(stateDir)
	}
	return enableOnioning(stateDir, bridge, subnet)
}

func enableOnioning(stateDir, bridge, subnet string) error {
	logging.Info("Enabling onioning — VM outbound traffic via Tor (RDP remains direct)")

	if err := ensureTorInstalled(); err != nil {
		return err
	}
	if err := writeTorrc(); err != nil {
		return err
	}
	if err := restartTor(); err != nil {
		return err
	}
	if err := waitTorBootstrap(2 * time.Minute); err != nil {
		logging.Warn("Tor bootstrap: %v (continuing — may still work)", err)
	}

	scriptPath := filepath.Join(stateDir, "apply-onion.sh")
	if err := writeOnionApplyScript(scriptPath, bridge, subnet); err != nil {
		return err
	}
	if err := installOnionService(scriptPath); err != nil {
		return err
	}

	if _, err := hostpkg.RunCommand("bash", scriptPath); err != nil {
		return fmt.Errorf("apply onion iptables: %w", err)
	}

	logging.Success("Onioning ON — guest TCP/DNS → Tor; host RDP proxy → VM stays clear")
	logging.Info("Check: from inside VM open https://check.torproject.org")
	return nil
}

func disableOnioning(stateDir string) error {
	logging.Info("Disabling onioning...")
	flushOnionRules()
	_, _ = hostpkg.RunCommand("systemctl", "disable", "--now", "alfaos-onion.service")
	_ = os.Remove(onionUnit)
	_, _ = hostpkg.RunCommand("systemctl", "daemon-reload")
	_ = os.Remove(filepath.Join(stateDir, "apply-onion.sh"))
	// Keep tor package; only remove our drop-in so Tor can stay for other uses.
	_ = os.Remove(torrcDropIn)
	_, _ = hostpkg.RunCommand("systemctl", "reload", "tor")
	logging.Success("Onioning OFF — VM uses normal NAT routing")
	return nil
}

func ensureTorInstalled() error {
	if hostpkg.CommandExists("tor") {
		return nil
	}
	logging.Info("Installing Tor...")
	if _, err := hostpkg.RunCommand("apt-get", "install", "-y", "-qq", "--no-install-recommends", "tor"); err != nil {
		_, err = hostpkg.RunCommand("apt-get", "-o", "Dir::Etc::sourceparts=/dev/null",
			"install", "-y", "-qq", "--no-install-recommends", "tor")
		if err != nil {
			return fmt.Errorf("install tor: %w", err)
		}
	}
	return nil
}

func writeTorrc() error {
	if err := os.MkdirAll("/etc/tor/torrc.d", 0755); err != nil {
		return err
	}
	// Ensure main torrc includes drop-ins (Debian usually has #%include /etc/tor/torrc.d/*.conf).
	ensureTorInclude()

	conf := fmt.Sprintf(`# Managed by ALFAOS onioning — do not edit by hand
# Transparent proxy for libvirt VMs; RDP is not torified (host→guest only).
VirtualAddrNetworkIPv4 10.192.0.0/10
AutomapHostsOnResolve 1
TransPort 127.0.0.1:%d IsolateClientAddr IsolateDestAddr IsolateDestPort
DNSPort 127.0.0.1:%d
SocksPort 127.0.0.1:9050
`, torTransPort, torDNSPort)

	return os.WriteFile(torrcDropIn, []byte(conf), 0644)
}

func ensureTorInclude() {
	const main = "/etc/tor/torrc"
	data, err := os.ReadFile(main)
	if err != nil {
		return
	}
	s := string(data)
	if strings.Contains(s, "/etc/tor/torrc.d") {
		// Uncomment include if disabled
		if strings.Contains(s, "#%include /etc/tor/torrc.d/*.conf") {
			s = strings.Replace(s, "#%include /etc/tor/torrc.d/*.conf", "%include /etc/tor/torrc.d/*.conf", 1)
			_ = os.WriteFile(main, []byte(s), 0644)
		}
		return
	}
	_ = os.WriteFile(main, append(data, []byte("\n%include /etc/tor/torrc.d/*.conf\n")...), 0644)
}

func restartTor() error {
	_, _ = hostpkg.RunCommand("systemctl", "enable", "tor")
	if _, err := hostpkg.RunCommand("systemctl", "restart", "tor"); err != nil {
		return fmt.Errorf("restart tor: %w", err)
	}
	return nil
}

func waitTorBootstrap(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if TestPort("127.0.0.1", fmt.Sprintf("%d", torTransPort)) {
			logging.Success("Tor TransPort is listening on 127.0.0.1:%d", torTransPort)
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("Tor TransPort :%d not ready within %v", torTransPort, timeout)
}

func writeOnionApplyScript(path, bridge, subnet string) error {
	script := fmt.Sprintf(`#!/bin/bash
# ALFAOS onioning — redirect libvirt guest traffic into Tor TransPort/DNSPort.
# Does NOT touch host→guest RDP (that is not PREROUTING from %s outbound).
set -euo pipefail
BRIDGE=%q
SUBNET=%q
TRANSP=%d
DNSPORT=%d
CHAIN=%q

flush() {
  iptables -t nat -D PREROUTING -i "$BRIDGE" -j "$CHAIN" 2>/dev/null || true
  iptables -t nat -F "$CHAIN" 2>/dev/null || true
  iptables -t nat -X "$CHAIN" 2>/dev/null || true
}

flush
iptables -t nat -N "$CHAIN"

# Keep guest↔host and guest↔guest clear (DHCP, SSH, RDP replies stay local)
iptables -t nat -A "$CHAIN" -d "$SUBNET" -j RETURN
iptables -t nat -A "$CHAIN" -d 127.0.0.0/8 -j RETURN

# DNS through Tor
iptables -t nat -A "$CHAIN" -p udp --dport 53 -j REDIRECT --to-ports "$DNSPORT"
iptables -t nat -A "$CHAIN" -p tcp --dport 53 -j REDIRECT --to-ports "$DNSPORT"

# All other TCP SYN from guests → Tor transparent proxy
iptables -t nat -A "$CHAIN" -p tcp --syn -j REDIRECT --to-ports "$TRANSP"

iptables -t nat -A PREROUTING -i "$BRIDGE" -j "$CHAIN"
`, bridge, bridge, subnet, torTransPort, torDNSPort, onionChain)

	return os.WriteFile(path, []byte(script), 0755)
}

func installOnionService(scriptPath string) error {
	unit := fmt.Sprintf(`[Unit]
Description=ALFAOS onioning (Tor transparent proxy for libvirt VMs)
After=network-online.target libvirtd.service tor.service
Wants=network-online.target
Requires=tor.service

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=%s
ExecStop=/bin/bash -c 'for br in virbr0; do iptables -t nat -D PREROUTING -i $$br -j ALFAOS_ONION 2>/dev/null || true; done; iptables -t nat -F ALFAOS_ONION 2>/dev/null || true; iptables -t nat -X ALFAOS_ONION 2>/dev/null || true'

[Install]
WantedBy=multi-user.target
`, scriptPath)

	if err := os.WriteFile(onionUnit, []byte(unit), 0644); err != nil {
		return err
	}
	_, _ = hostpkg.RunCommand("systemctl", "daemon-reload")
	_, _ = hostpkg.RunCommand("systemctl", "enable", "alfaos-onion.service")
	_, _ = hostpkg.RunCommand("systemctl", "restart", "alfaos-onion.service")
	return nil
}

func flushOnionRules() {
	// Try common bridges
	for _, br := range []string{"virbr0"} {
		_, _ = hostpkg.RunCommand("iptables", "-t", "nat", "-D", "PREROUTING", "-i", br, "-j", onionChain)
	}
	_, _ = hostpkg.RunCommand("iptables", "-t", "nat", "-F", onionChain)
	_, _ = hostpkg.RunCommand("iptables", "-t", "nat", "-X", onionChain)
}

func libvirtBridge(network string) string {
	out, err := hostpkg.RunCommand("virsh", "net-info", network)
	if err == nil {
		for _, line := range strings.Split(out, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "Bridge:") {
				parts := strings.Fields(line)
				if len(parts) >= 2 {
					return parts[1]
				}
			}
		}
	}
	return "virbr0"
}

func libvirtSubnet(network string) string {
	out, err := hostpkg.RunCommand("virsh", "net-dumpxml", network)
	if err != nil {
		return "192.168.122.0/24"
	}
	// <ip address='192.168.122.1' netmask='255.255.255.0'>
	addr := ""
	mask := ""
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "<ip ") {
			continue
		}
		if i := strings.Index(line, "address='"); i >= 0 {
			rest := line[i+len("address='"):]
			if j := strings.Index(rest, "'"); j >= 0 {
				addr = rest[:j]
			}
		}
		if i := strings.Index(line, "netmask='"); i >= 0 {
			rest := line[i+len("netmask='"):]
			if j := strings.Index(rest, "'"); j >= 0 {
				mask = rest[:j]
			}
		}
	}
	if addr == "" {
		return "192.168.122.0/24"
	}
	if mask == "255.255.255.0" {
		parts := strings.Split(addr, ".")
		if len(parts) == 4 {
			return fmt.Sprintf("%s.%s.%s.0/24", parts[0], parts[1], parts[2])
		}
	}
	return "192.168.122.0/24"
}

// OnioningActive reports whether the ALFAOS onion iptables chain is installed.
func OnioningActive() bool {
	_, err := hostpkg.RunCommand("iptables", "-t", "nat", "-n", "-L", onionChain)
	return err == nil
}
