package virtualization

import (
	"strings"
	"testing"
)

func TestCleanInstallXMLRemovesUEFIAndSetsBIOS(t *testing.T) {
	xml := `<domain>
  <os>
    <type arch='x86_64' machine='pc-q35-noble'>hvm</type>
    <loader readonly='yes' type='pflash'>/path/OVMF.fd</loader>
    <nvram>/path/nvram</nvram>
    <boot dev='cdrom'/>
    <kernel>/tmp/vmlinuz</kernel>
  </os>
</domain>`

	cleaned := cleanInstallXML(xml)
	if strings.Contains(cleaned, "OVMF") || strings.Contains(cleaned, "<kernel>") {
		t.Fatalf("UEFI/kernel not removed:\n%s", cleaned)
	}
	if !strings.Contains(cleaned, "pc-i440fx") {
		t.Fatalf("expected i440fx machine")
	}
	if !strings.Contains(cleaned, "<boot dev='hd'/>") {
		t.Fatalf("expected hd boot")
	}
}

func TestCleanInstallXMLRemovesInstallerKernel(t *testing.T) {
	xml := `<domain>
  <os>
    <type>hvm</type>
    <boot dev='cdrom'/>
    <boot dev='hd'/>
    <kernel>/var/lib/alfaos/iso/debian.iso</kernel>
    <initrd>/var/lib/alfaos/iso/initrd.gz</initrd>
    <cmdline>auto=true</cmdline>
  </os>
  <devices>
    <disk type='file' device='cdrom'/>
    <disk type='file' device='disk'/>
  </devices>
</domain>`

	cleaned := cleanInstallXML(xml)
	if strings.Contains(cleaned, "<kernel>") || strings.Contains(cleaned, "<initrd>") || strings.Contains(cleaned, "<cmdline>") {
		t.Fatalf("installer boot elements not removed:\n%s", cleaned)
	}
	if strings.Contains(cleaned, "device='cdrom'") {
		t.Fatalf("cdrom disk not removed")
	}
	if !strings.Contains(cleaned, "<boot dev='hd'/>") {
		t.Fatalf("hd boot not set")
	}
	if strings.Contains(cleaned, "<boot dev='cdrom'/>") {
		t.Fatalf("cdrom boot still present")
	}
}

func TestParseDomifaddr(t *testing.T) {
	out := ` Name       MAC address          Protocol     Address
-------------------------------------------------------------------------------
 vnet0      52:54:00:12:34:56    ipv4         192.168.122.87/24
`
	ip := parseDomifaddr(out)
	if ip != "192.168.122.87" {
		t.Fatalf("got %q", ip)
	}
}

func TestParseDHCPLeasesByMAC(t *testing.T) {
	out := ` Expiry Time          MAC address        Protocol  IP address        Hostname
 2026-08-29 10:00:00   52:54:00:12:34:56  ipv4      192.168.122.87    alfaos
`
	ip := parseDHCPLeasesByMAC(out, "52:54:00:12:34:56")
	if ip != "192.168.122.87" {
		t.Fatalf("got %q", ip)
	}
}

func TestGetVMMACParsing(t *testing.T) {
	m := &Manager{}
	out := ` Interface  Type       Source    Model    MAC
-----------------------------------------------------------
 vnet0      network    default   virtio   52:54:00:ab:cd:ef
`
	mac, err := m.parseMACFromDomiflist(out)
	if err != nil || mac != "52:54:00:ab:cd:ef" {
		t.Fatalf("mac=%q err=%v", mac, err)
	}
}
