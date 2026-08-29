package virtualization

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/alfaos/alfaos/internal/host"
	"github.com/alfaos/alfaos/internal/logging"
)

var (
	reKernel  = regexp.MustCompile(`(?s)\s*<kernel[^>]*>.*?</kernel>\s*`)
	reInitrd  = regexp.MustCompile(`(?s)\s*<initrd[^>]*>.*?</initrd>\s*`)
	reCmdline = regexp.MustCompile(`(?s)\s*<cmdline[^>]*/>\s*|\s*<cmdline[^>]*>.*?</cmdline>\s*`)
	reCdrom   = regexp.MustCompile(`(?s)\s*<disk\s+[^>]*device=['"]cdrom['"][^>]*/>\s*|\s*<disk\s+[^>]*device=['"]cdrom['"][^>]*>.*?</disk>\s*`)
	reBootDev = regexp.MustCompile(`(?s)\s*<boot dev=['"][^'"]*['"]/>\s*`)
	reOSBlock = regexp.MustCompile(`(?s)(<os>)(.*?)(</os>)`)
	reMAC     = regexp.MustCompile(`(?i)([0-9a-f]{2}(:[0-9a-f]{2}){5})`)
	reBootMenu = regexp.MustCompile(`(?s)\s*<bootmenu[^>]*/>\s*`)
	reLease   = regexp.MustCompile(`(?i)([0-9a-f]{2}(:[0-9a-f]{2}){5})\s+ipv4\s+([0-9.]+)`)
)

// FinalizeAfterInstall removes installer kernel/initrd from the domain XML and
// sets persistent boot order to disk. Required after virt-install --location.
func (m *Manager) FinalizeAfterInstall() error {
	if !m.DomainExists() {
		return nil
	}

	if m.DomainRunning() {
		if err := m.StopVM(); err != nil {
			logging.Warn("Stop VM before finalize: %v", err)
		}
	}

	xml, err := host.RunCommand("virsh", "dumpxml", m.cfg.VM.Name)
	if err != nil {
		return fmt.Errorf("dumpxml: %w", err)
	}

	cleaned := cleanInstallXML(xml)
	if cleaned == xml {
		logging.Info("Domain XML already finalized for disk boot")
	} else {
		logging.Info("Removing installer kernel/initrd and setting disk boot...")
	}

	xmlPath := filepath.Join(m.cfg.Paths.StateDir, m.cfg.VM.Name+".xml")
	if err := os.WriteFile(xmlPath, []byte(cleaned), 0644); err != nil {
		return fmt.Errorf("write domain xml: %w", err)
	}

	if _, err := host.RunCommand("virsh", "define", xmlPath); err != nil {
		return fmt.Errorf("virsh define: %w", err)
	}

	logging.Success("VM configured to boot from installed disk")
	return nil
}

func cleanInstallXML(xml string) string {
	xml = reKernel.ReplaceAllString(xml, "\n")
	xml = reInitrd.ReplaceAllString(xml, "\n")
	xml = reCmdline.ReplaceAllString(xml, "\n")
	xml = reCdrom.ReplaceAllString(xml, "\n")

	// Remove UEFI loader/NVRAM — Debian is installed for legacy BIOS boot.
	xml = regexp.MustCompile(`(?s)\s*<loader[^>]*>.*?</loader>\s*`).ReplaceAllString(xml, "\n")
	xml = regexp.MustCompile(`(?s)\s*<loader[^>]*/>\s*`).ReplaceAllString(xml, "\n")
	xml = regexp.MustCompile(`(?s)\s*<nvram[^>]*/>\s*`).ReplaceAllString(xml, "\n")
	xml = regexp.MustCompile(`(?s)\s*<nvram[^>]*>.*?</nvram>\s*`).ReplaceAllString(xml, "\n")
	xml = reBootMenu.ReplaceAllString(xml, "\n")

	xml = reOSBlock.ReplaceAllStringFunc(xml, func(block string) string {
		parts := reOSBlock.FindStringSubmatch(block)
		if len(parts) < 4 {
			return block
		}
		inner := reBootDev.ReplaceAllString(parts[2], "")
		inner = strings.TrimSpace(inner)

		// Force BIOS firmware and i440fx for reliable SeaBIOS boot from SATA.
		if !strings.Contains(inner, "machine=") {
			inner = "<type arch='x86_64' machine='pc-i440fx-8.2'>hvm</type>"
		} else {
			inner = regexp.MustCompile(`machine='[^']*'`).ReplaceAllString(inner, "machine='pc-i440fx-8.2'")
		}

		return parts[1] + "\n    " + inner + "\n    <boot dev='hd'/>\n  " + parts[3]
	})

	return xml
}

// EjectInstallMedia is kept for compatibility; delegates to FinalizeAfterInstall.
func (m *Manager) EjectInstallMedia() error {
	return m.FinalizeAfterInstall()
}

func (m *Manager) GetVMIP(timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	attempt := 0
	for time.Now().Before(deadline) {
		attempt++
		ip, method, err := m.tryGetIP()
		if err == nil && ip != "" && isValidVMIP(ip) && m.isIPReachable(ip) {
			logging.Success("VM IP address: %s (via %s)", ip, method)
			return ip, nil
		}
		if ip != "" && !m.isIPReachable(ip) {
			logging.Debug("Ignoring unreachable lease IP %s", ip)
		}
		if attempt%3 == 0 {
			logging.Info("Still waiting for VM IP (attempt %d)...", attempt)
		}
		time.Sleep(10 * time.Second)
	}

	m.logNetworkDiagnostics()
	return "", fmt.Errorf("could not determine VM IP after %v", timeout)
}

func (m *Manager) tryGetIP() (string, string, error) {
	mac, _ := m.getVMMAC()
	var candidates []string
	add := func(ips ...string) {
		for _, ip := range ips {
			if isValidVMIP(ip) && !contains(candidates, ip) {
				candidates = append(candidates, ip)
			}
		}
	}

	for _, source := range []string{"agent", "lease", "arp"} {
		out, err := host.RunCommand("virsh", "domifaddr", m.cfg.VM.Name, "--source", source)
		if err == nil {
			add(parseAllDomifaddr(out)...)
		}
	}

	if mac != "" {
		out, err := host.RunCommand("virsh", "net-dhcp-leases", m.cfg.VM.Network)
		if err == nil {
			add(parseAllDHCPLeasesByMAC(out, mac)...)
		}
		add(m.parseDnsmasqLeaseFile(mac))
	}

	out, err := host.RunCommand("virsh", "net-dhcp-leases", m.cfg.VM.Network)
	if err == nil {
		add(parseAllDHCPLeases(out)...)
	}

	out, _ = host.RunCommand("ip", "neigh", "show")
	add(parseNeighborTable(out, mac))

	// Prefer libvirt subnet, then any reachable VM IP.
	for _, ip := range candidates {
		if strings.HasPrefix(ip, "192.168.122.") && m.isIPReachable(ip) {
			return ip, "reachable", nil
		}
	}
	for _, ip := range candidates {
		if m.isIPReachable(ip) {
			return ip, "reachable", nil
		}
	}
	if len(candidates) > 0 {
		// Return last lease candidate (usually newest) if not reachable yet.
		return candidates[len(candidates)-1], "lease", nil
	}

	return "", "", fmt.Errorf("IP not found")
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

func parseAllDomifaddr(out string) []string {
	var ips []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "-") || strings.HasPrefix(line, "Name") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 4 && fields[2] == "ipv4" {
			ip := strings.Split(fields[3], "/")[0]
			if isValidIPv4(ip) {
				ips = append(ips, ip)
			}
		}
	}
	return ips
}

func parseAllDHCPLeasesByMAC(out, mac string) []string {
	var ips []string
	mac = strings.ToLower(mac)
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(strings.ToLower(line), mac) {
			continue
		}
		if m := reLease.FindStringSubmatch(line); len(m) >= 4 && strings.EqualFold(m[1], mac) {
			ips = append(ips, m[3])
		}
	}
	return ips
}

func parseAllDHCPLeases(out string) []string {
	var ips []string
	for _, line := range strings.Split(out, "\n") {
		if m := reLease.FindStringSubmatch(line); len(m) >= 4 {
			ips = append(ips, m[3])
		}
	}
	return ips
}

func parseDomifaddr(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "-") || strings.HasPrefix(line, "Name") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 4 && fields[2] == "ipv4" {
			ip := strings.Split(fields[3], "/")[0]
			if isValidIPv4(ip) {
				return ip
			}
		}
	}
	return ""
}

func parseDHCPLeasesByMAC(out, mac string) string {
	mac = strings.ToLower(mac)
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(strings.ToLower(line), mac) {
			continue
		}
		if m := reLease.FindStringSubmatch(line); len(m) >= 4 {
			if strings.EqualFold(m[1], mac) && isValidIPv4(m[3]) {
				return m[3]
			}
		}
	}
	return ""
}

func parseAnyDHCPLease(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if m := reLease.FindStringSubmatch(line); len(m) >= 4 && isValidIPv4(m[3]) {
			return m[3]
		}
	}
	return ""
}

func (m *Manager) parseDnsmasqLeaseFile(mac string) string {
	paths := []string{
		fmt.Sprintf("/var/lib/libvirt/dnsmasq/%s.leases", "default"),
		fmt.Sprintf("/var/lib/libvirt/dnsmasq/%s.leases", "default.xml"),
	}
	if m.cfg.VM.Network != "" && m.cfg.VM.Network != "default" {
		paths = append([]string{
			fmt.Sprintf("/var/lib/libvirt/dnsmasq/%s.leases", m.cfg.VM.Network),
		}, paths...)
	}

	mac = strings.ToLower(mac)
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 3 && strings.EqualFold(fields[1], mac) && isValidIPv4(fields[2]) {
				return fields[2]
			}
		}
	}
	return ""
}

func parseNeighborTable(out, mac string) string {
	mac = strings.ToLower(mac)
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "192.168.122.") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 1 || !isValidIPv4(fields[0]) {
			continue
		}
		if mac == "" || strings.Contains(strings.ToLower(line), mac) {
			if strings.Contains(line, "REACHABLE") || strings.Contains(line, "STALE") || strings.Contains(line, "DELAY") {
				return fields[0]
			}
		}
	}
	return ""
}

func (m *Manager) getVMMAC() (string, error) {
	out, err := host.RunCommand("virsh", "domiflist", m.cfg.VM.Name)
	if err != nil {
		return "", err
	}
	return m.parseMACFromDomiflist(out)
}

func (m *Manager) parseMACFromDomiflist(out string) (string, error) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "-") || strings.HasPrefix(line, "Interface") {
			continue
		}
		fields := strings.Fields(line)
		// Interface Type Source Model MAC
		if len(fields) >= 5 && fields[1] == "network" {
			return fields[4], nil
		}
		if len(fields) >= 2 {
			if m := reMAC.FindString(fields[len(fields)-1]); m != "" {
				return m, nil
			}
		}
	}
	return "", fmt.Errorf("MAC not found")
}

func (m *Manager) logNetworkDiagnostics() {
	logging.Warn("Could not determine VM IP — diagnostics:")
	if out, err := host.RunCommand("virsh", "domstate", m.cfg.VM.Name); err == nil {
		logging.Warn("  domstate: %s", strings.TrimSpace(out))
	}
	if out, err := host.RunCommand("virsh", "domiflist", m.cfg.VM.Name); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			logging.Warn("  domiflist: %s", line)
		}
	}
	if out, err := host.RunCommand("virsh", "net-dhcp-leases", m.cfg.VM.Network); err == nil {
		if strings.TrimSpace(out) == "" {
			logging.Warn("  net-dhcp-leases: (empty)")
		}
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			logging.Warn("  net-dhcp-leases: %s", line)
		}
	}
	if out, err := host.RunCommand("virsh", "domifaddr", m.cfg.VM.Name); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			logging.Warn("  domifaddr: %s", line)
		}
	}
}

func isValidIPv4(ip string) bool {
	parts := strings.Split(ip, ".")
	if len(parts) != 4 {
		return false
	}
	for _, p := range parts {
		if len(p) == 0 || len(p) > 3 {
			return false
		}
	}
	return ip != "0.0.0.0"
}

// isValidVMIP rejects loopback and invalid addresses unsuitable for VM SSH.
func isValidVMIP(ip string) bool {
	if !isValidIPv4(ip) {
		return false
	}
	if strings.HasPrefix(ip, "127.") {
		return false
	}
	return true
}

func (m *Manager) isIPReachable(ip string) bool {
	if !isValidVMIP(ip) {
		return false
	}
	_, err := host.RunCommand("ping", "-c", "1", "-W", "2", ip)
	return err == nil
}

func (m *Manager) WaitForPort(ip, port string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if host.CommandExists("nc") {
			_, err := host.RunCommand("nc", "-z", "-w", "2", ip, port)
			if err == nil {
				return nil
			}
		} else {
			out, err := host.RunCommand("bash", "-c",
				"timeout 2 bash -c 'echo >/dev/tcp/"+ip+"/"+port+"' 2>/dev/null && echo ok")
			if err == nil && strings.Contains(out, "ok") {
				return nil
			}
		}
		time.Sleep(5 * time.Second)
	}
	return fmt.Errorf("port %s not open on %s after %v", port, ip, timeout)
}
