package networking

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	hostpkg "github.com/alfaos/alfaos/internal/host"
	"github.com/alfaos/alfaos/internal/logging"
)

var reLibvirtDNS = regexp.MustCompile(`(?s)\s*<dns>.*?</dns>\s*`)

// ConfigureLibvirtDNS sets upstream DNS forwarders on the libvirt NAT network (dnsmasq).
func ConfigureLibvirtDNS(network string, servers []string) error {
	if network == "" {
		network = "default"
	}
	if len(servers) == 0 {
		return nil
	}

	out, err := hostpkg.RunCommand("virsh", "net-list", "--all")
	if err != nil {
		return err
	}
	if !strings.Contains(out, network) {
		logging.Warn("Libvirt network %q not found — skipping DNS config", network)
		return nil
	}

	xml, err := hostpkg.RunCommand("virsh", "net-dumpxml", network)
	if err != nil {
		return fmt.Errorf("net-dumpxml %s: %w", network, err)
	}

	allPresent := true
	for _, s := range servers {
		if !strings.Contains(xml, "addr='"+s+"'") {
			allPresent = false
			break
		}
	}
	if allPresent && strings.Contains(xml, "<dns>") {
		logging.Info("Libvirt network %s already uses configured DNS", network)
		return nil
	}

	updated := injectLibvirtDNS(xml, servers)

	tmp := filepath.Join(os.TempDir(), fmt.Sprintf("alfaos-net-%s.xml", network))
	if err := os.WriteFile(tmp, []byte(updated), 0644); err != nil {
		return err
	}
	defer os.Remove(tmp)

	wasActive := strings.Contains(out, network) && isNetworkActive(network)
	if wasActive {
		logging.Info("Updating DNS on libvirt network %s (brief reconnect)...", network)
		_, _ = hostpkg.RunCommand("virsh", "net-destroy", network)
	}

	if _, err := hostpkg.RunCommand("virsh", "net-define", tmp); err != nil {
		if wasActive {
			_, _ = hostpkg.RunCommand("virsh", "net-start", network)
		}
		return fmt.Errorf("net-define %s: %w", network, err)
	}

	_, _ = hostpkg.RunCommand("virsh", "net-autostart", network)
	if wasActive {
		if _, err := hostpkg.RunCommand("virsh", "net-start", network); err != nil {
			return fmt.Errorf("net-start %s: %w", network, err)
		}
	}

	logging.Success("Libvirt DNS: %s → %s", network, strings.Join(servers, ", "))
	return nil
}

func isNetworkActive(name string) bool {
	out, err := hostpkg.RunCommand("virsh", "net-info", name)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Active:") {
			return strings.Contains(strings.ToLower(line), "yes")
		}
	}
	return false
}

func injectLibvirtDNS(xml string, servers []string) string {
	if libvirtDNSConfigured(xml, servers) {
		return xml
	}

	xml = reLibvirtDNS.ReplaceAllString(xml, "\n")
	block := "\n" + buildLibvirtDNSBlock(servers)
	if loc := regexp.MustCompile(`\s*<ip `).FindStringIndex(xml); loc != nil {
		return xml[:loc[0]] + block + xml[loc[0]:]
	}
	return strings.Replace(xml, "</network>", block+"\n</network>", 1)
}

func libvirtDNSConfigured(xml string, servers []string) bool {
	block := reLibvirtDNS.FindString(xml)
	if block == "" {
		return false
	}
	for _, s := range servers {
		if !strings.Contains(block, fmt.Sprintf("addr='%s'", s)) {
			return false
		}
	}
	return true
}

func buildLibvirtDNSBlock(servers []string) string {
	var b strings.Builder
	b.WriteString("  <dns>\n")
	for _, s := range servers {
		b.WriteString(fmt.Sprintf("    <forwarder addr='%s'/>\n", s))
	}
	b.WriteString("  </dns>")
	return b.String()
}

// GuestDHCPDNSLine returns a dhclient supersede line for the guest.
func GuestDHCPDNSLine(servers []string) string {
	if len(servers) == 0 {
		return ""
	}
	return "supersede domain-name-servers " + strings.Join(servers, ", ") + ";"
}

// GuestResolvConf returns resolv.conf content for the guest.
func GuestResolvConf(servers []string) string {
	var b strings.Builder
	for _, s := range servers {
		b.WriteString("nameserver ")
		b.WriteString(s)
		b.WriteByte('\n')
	}
	return b.String()
}

// GuestInterfacesEth0 returns a classic ifupdown stanza for eth0 with DNS.
func GuestInterfacesEth0(servers []string) string {
	dns := strings.Join(servers, " ")
	return fmt.Sprintf("auto lo\niface lo inet loopback\n\nauto eth0\niface eth0 inet dhcp\n    dns-nameservers %s\n", dns)
}
