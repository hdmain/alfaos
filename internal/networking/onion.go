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
	nftTable     = "alfaos_onion"
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
	gateway := libvirtGateway(libvirtNetwork, bridge)

	if !enabled {
		return disableOnioning(stateDir)
	}
	return enableOnioning(stateDir, bridge, subnet, gateway)
}

func enableOnioning(stateDir, bridge, subnet, gateway string) error {
	logging.Info("Enabling onioning — VM outbound traffic via Tor (RDP remains direct)")
	logging.Info("Bridge %s subnet %s gateway %s", bridge, subnet, gateway)

	if err := ensureTorInstalled(); err != nil {
		return err
	}
	if err := writeTorrc(gateway); err != nil {
		return err
	}
	if err := restartTor(); err != nil {
		return err
	}
	if err := waitTorBootstrap(2*time.Minute, gateway); err != nil {
		logging.Warn("Tor bootstrap: %v (continuing — may still work)", err)
	}

	scriptPath := filepath.Join(stateDir, "apply-onion.sh")
	if err := writeOnionApplyScript(scriptPath, bridge, subnet, gateway); err != nil {
		return err
	}
	if err := installOnionService(scriptPath, bridge); err != nil {
		return err
	}

	if out, err := hostpkg.RunCommand("bash", scriptPath); err != nil {
		return fmt.Errorf("apply onion rules: %w\n%s", err, out)
	}

	logging.Success("Onioning ON — guest TCP/DNS → Tor; host RDP proxy → VM stays clear")
	logging.Info("From inside VM open https://check.torproject.org (use a NEW browser tab)")
	logging.Info("If IP still leaks: sudo systemctl restart alfaos-onion && sudo alfaos onioning status")
	return nil
}

func disableOnioning(stateDir string) error {
	logging.Info("Disabling onioning...")
	flushOnionRules("virbr0")
	_, _ = hostpkg.RunCommand("systemctl", "disable", "--now", "alfaos-onion.service")
	_ = os.Remove(onionUnit)
	_, _ = hostpkg.RunCommand("systemctl", "daemon-reload")
	_ = os.Remove(filepath.Join(stateDir, "apply-onion.sh"))
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

func writeTorrc(gateway string) error {
	if err := os.MkdirAll("/etc/tor/torrc.d", 0755); err != nil {
		return err
	}
	ensureTorInclude()

	// REDIRECT from virbr0 rewrites dest to the bridge IP (e.g. 192.168.122.1),
	// NOT 127.0.0.1 — Tor must listen on the gateway address (and localhost).
	gwLine := ""
	if gateway != "" && gateway != "127.0.0.1" {
		gwLine = fmt.Sprintf("TransPort %s:%d IsolateClientAddr IsolateDestAddr IsolateDestPort\nDNSPort %s:%d\n",
			gateway, torTransPort, gateway, torDNSPort)
	}

	conf := fmt.Sprintf(`# Managed by ALFAOS onioning — do not edit by hand
# Transparent proxy for libvirt VMs; RDP is not torified (host→guest only).
# IMPORTANT: REDIRECT to virbr0 targets the bridge IP, so TransPort must bind there.
VirtualAddrNetworkIPv4 10.192.0.0/10
AutomapHostsOnResolve 1
TransPort 127.0.0.1:%d IsolateClientAddr IsolateDestAddr IsolateDestPort
DNSPort 127.0.0.1:%d
%sSocksPort 127.0.0.1:9050
`, torTransPort, torDNSPort, gwLine)

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

func waitTorBootstrap(timeout time.Duration, gateway string) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		okLocal := TestPort("127.0.0.1", fmt.Sprintf("%d", torTransPort))
		okGW := gateway == "" || gateway == "127.0.0.1" || TestPort(gateway, fmt.Sprintf("%d", torTransPort))
		if okLocal && okGW {
			logging.Success("Tor TransPort ready (127.0.0.1 + %s):%d", gateway, torTransPort)
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("Tor TransPort :%d not ready within %v", torTransPort, timeout)
}

func writeOnionApplyScript(path, bridge, subnet, gateway string) error {
	script := fmt.Sprintf(`#!/bin/bash
# ALFAOS onioning — redirect libvirt guest traffic into Tor.
# REDIRECT from $BRIDGE rewrites destination to the bridge IP (%s), so Tor listens there.
set -euo pipefail
BRIDGE=%q
SUBNET=%q
GATEWAY=%q
TRANSP=%d
DNSPORT=%d
CHAIN=%q
NFTTABLE=%q

flush_iptables() {
  iptables -t nat -D PREROUTING -i "$BRIDGE" -j "$CHAIN" 2>/dev/null || true
  iptables -t nat -F "$CHAIN" 2>/dev/null || true
  iptables -t nat -X "$CHAIN" 2>/dev/null || true
}

flush_nft() {
  if command -v nft >/dev/null 2>&1; then
    nft delete table ip "$NFTTABLE" 2>/dev/null || true
  fi
}

flush_ip6() {
  if command -v ip6tables >/dev/null 2>&1; then
    ip6tables -D FORWARD -i "$BRIDGE" -j DROP 2>/dev/null || true
    ip6tables -D FORWARD -o "$BRIDGE" -j DROP 2>/dev/null || true
  fi
}

flush_iptables
flush_nft
flush_ip6

# Allow DNAT to localhost if used elsewhere
sysctl -w net.ipv4.conf.all.route_localnet=1 >/dev/null
sysctl -w "net.ipv4.conf.${BRIDGE}.route_localnet=1" >/dev/null 2>/dev/null || true

# Prefer nftables (libvirt on modern Debian uses nft; iptables-only can be ignored)
if command -v nft >/dev/null 2>&1; then
  nft -f - <<EOF
table ip ${NFTTABLE} {
  chain prerouting {
    type nat hook prerouting priority -110; policy accept;
    iifname "${BRIDGE}" ip daddr ${SUBNET} return
    iifname "${BRIDGE}" ip daddr 127.0.0.0/8 return
    iifname "${BRIDGE}" udp dport 53 redirect to :${DNSPORT}
    iifname "${BRIDGE}" tcp dport 53 redirect to :${DNSPORT}
    iifname "${BRIDGE}" tcp flags syn / syn,rst redirect to :${TRANSP}
  }
}
EOF
  echo "alfaos-onion: nftables table ${NFTTABLE} installed"
else
  iptables -t nat -N "$CHAIN"
  iptables -t nat -A "$CHAIN" -d "$SUBNET" -j RETURN
  iptables -t nat -A "$CHAIN" -d 127.0.0.0/8 -j RETURN
  iptables -t nat -A "$CHAIN" -p udp --dport 53 -j REDIRECT --to-ports "$DNSPORT"
  iptables -t nat -A "$CHAIN" -p tcp --dport 53 -j REDIRECT --to-ports "$DNSPORT"
  iptables -t nat -A "$CHAIN" -p tcp --syn -j REDIRECT --to-ports "$TRANSP"
  iptables -t nat -A PREROUTING -i "$BRIDGE" -j "$CHAIN"
  echo "alfaos-onion: iptables chain ${CHAIN} installed"
fi

# Block IPv6 from/to the VM bridge (Tor transparent proxy is IPv4-only → IPv6 would leak)
if command -v ip6tables >/dev/null 2>&1; then
  ip6tables -I FORWARD -i "$BRIDGE" -j DROP
  ip6tables -I FORWARD -o "$BRIDGE" -j DROP
fi

# Quick sanity: Tor must accept connections on the gateway IP
if ! ss -tln | grep -q ":${TRANSP}"; then
  echo "WARNING: nothing listening on :${TRANSP}" >&2
fi
if [ -n "$GATEWAY" ] && [ "$GATEWAY" != "127.0.0.1" ]; then
  if ! ss -tln | grep -qE "${GATEWAY}:${TRANSP}|\\*:${TRANSP}|0\\.0\\.0\\.0:${TRANSP}"; then
    echo "WARNING: Tor may not be listening on ${GATEWAY}:${TRANSP} (REDIRECT target)" >&2
    ss -tlnp | grep "${TRANSP}" || true
  fi
fi
`, gateway, bridge, subnet, gateway, torTransPort, torDNSPort, onionChain, nftTable)

	return os.WriteFile(path, []byte(script), 0755)
}

func installOnionService(scriptPath, bridge string) error {
	unit := fmt.Sprintf(`[Unit]
Description=ALFAOS onioning (Tor transparent proxy for libvirt VMs)
After=network-online.target libvirtd.service tor.service
Wants=network-online.target
Requires=tor.service

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=%s
ExecStop=/bin/bash -c 'nft delete table ip alfaos_onion 2>/dev/null || true; iptables -t nat -D PREROUTING -i %s -j ALFAOS_ONION 2>/dev/null || true; iptables -t nat -F ALFAOS_ONION 2>/dev/null || true; iptables -t nat -X ALFAOS_ONION 2>/dev/null || true; ip6tables -D FORWARD -i %s -j DROP 2>/dev/null || true; ip6tables -D FORWARD -o %s -j DROP 2>/dev/null || true; true'

[Install]
WantedBy=multi-user.target
`, scriptPath, bridge, bridge, bridge)

	if err := os.WriteFile(onionUnit, []byte(unit), 0644); err != nil {
		return err
	}
	_, _ = hostpkg.RunCommand("systemctl", "daemon-reload")
	_, _ = hostpkg.RunCommand("systemctl", "enable", "alfaos-onion.service")
	_, _ = hostpkg.RunCommand("systemctl", "restart", "alfaos-onion.service")
	return nil
}

func flushOnionRules(bridge string) {
	if bridge == "" {
		bridge = "virbr0"
	}
	_, _ = hostpkg.RunCommand("nft", "delete", "table", "ip", nftTable)
	_, _ = hostpkg.RunCommand("iptables", "-t", "nat", "-D", "PREROUTING", "-i", bridge, "-j", onionChain)
	_, _ = hostpkg.RunCommand("iptables", "-t", "nat", "-F", onionChain)
	_, _ = hostpkg.RunCommand("iptables", "-t", "nat", "-X", onionChain)
	_, _ = hostpkg.RunCommand("ip6tables", "-D", "FORWARD", "-i", bridge, "-j", "DROP")
	_, _ = hostpkg.RunCommand("ip6tables", "-D", "FORWARD", "-o", bridge, "-j", "DROP")
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

func libvirtGateway(network, bridge string) string {
	out, err := hostpkg.RunCommand("virsh", "net-dumpxml", network)
	if err == nil {
		for _, line := range strings.Split(out, "\n") {
			if !strings.Contains(line, "<ip ") {
				continue
			}
			if i := strings.Index(line, "address='"); i >= 0 {
				rest := line[i+len("address='"):]
				if j := strings.Index(rest, "'"); j >= 0 {
					return rest[:j]
				}
			}
		}
	}
	if bridge != "" {
		out, err := hostpkg.RunCommand("bash", "-c",
			fmt.Sprintf(`ip -4 -o addr show %s 2>/dev/null | awk '{print $4}' | cut -d/ -f1 | head -1`, bridge))
		if err == nil {
			if ip := strings.TrimSpace(out); ip != "" {
				return ip
			}
		}
	}
	return "192.168.122.1"
}

func libvirtSubnet(network string) string {
	out, err := hostpkg.RunCommand("virsh", "net-dumpxml", network)
	if err != nil {
		return "192.168.122.0/24"
	}
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

// OnioningActive reports whether onion redirect rules are installed.
func OnioningActive() bool {
	if _, err := hostpkg.RunCommand("nft", "list", "table", "ip", nftTable); err == nil {
		return true
	}
	_, err := hostpkg.RunCommand("iptables", "-t", "nat", "-n", "-L", onionChain)
	return err == nil
}

// OnioningDiagnostics returns a short human-readable status for `alfaos onioning status`.
func OnioningDiagnostics(libvirtNetwork string) string {
	bridge := libvirtBridge(libvirtNetwork)
	gw := libvirtGateway(libvirtNetwork, bridge)
	var b strings.Builder

	fmt.Fprintf(&b, "bridge: %s  gateway: %s\n", bridge, gw)

	if OnioningActive() {
		b.WriteString("rules: present\n")
	} else {
		b.WriteString("rules: MISSING\n")
	}

	ssOut, _ := hostpkg.RunCommand("ss", "-tln")
	if strings.Contains(ssOut, fmt.Sprintf(":%d", torTransPort)) {
		fmt.Fprintf(&b, "tor TransPort :%d: listening\n", torTransPort)
	} else {
		fmt.Fprintf(&b, "tor TransPort :%d: NOT listening\n", torTransPort)
	}
	if gw != "" && TestPort(gw, fmt.Sprintf("%d", torTransPort)) {
		fmt.Fprintf(&b, "tor on gateway %s:%d: OK\n", gw, torTransPort)
	} else if gw != "" {
		fmt.Fprintf(&b, "tor on gateway %s:%d: FAIL (this caused IP leaks)\n", gw, torTransPort)
	}

	if _, err := hostpkg.RunCommand("nft", "list", "table", "ip", nftTable); err == nil {
		b.WriteString("backend: nftables\n")
	} else {
		b.WriteString("backend: iptables\n")
	}
	return b.String()
}
