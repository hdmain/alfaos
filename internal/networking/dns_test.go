package networking

import (
	"strings"
	"testing"
)

func TestInjectLibvirtDNS(t *testing.T) {
	xml := `<network>
  <name>default</name>
  <bridge name='virbr0'/>
  <forward mode='nat'/>
  <ip address='192.168.122.1' netmask='255.255.255.0'>
    <dhcp>
      <range start='192.168.122.2' end='192.168.122.254'/>
    </dhcp>
  </ip>
</network>`

	servers := []string{"94.140.14.14", "94.140.15.15"}
	out := injectLibvirtDNS(xml, servers)

	if !strings.Contains(out, "<forwarder addr='94.140.14.14'/>") || !strings.Contains(out, "<forwarder addr='94.140.15.15'/>") {
		t.Fatalf("missing forwarders:\n%s", out)
	}
	if strings.Index(out, "<dns>") < 0 || strings.Index(out, "<ip ") < 0 {
		t.Fatalf("missing blocks:\n%s", out)
	}
	if strings.Index(out, "<dns>") > strings.Index(out, "<ip ") {
		t.Fatalf("dns block should precede ip block:\n%s", out)
	}

	again := injectLibvirtDNS(out, servers)
	if again != out {
		t.Fatal("expected idempotent inject")
	}
}

func TestGuestDHCPDNSLine(t *testing.T) {
	line := GuestDHCPDNSLine([]string{"94.140.14.14", "94.140.15.15"})
	want := "supersede domain-name-servers 94.140.14.14, 94.140.15.15;"
	if line != want {
		t.Fatalf("got %q want %q", line, want)
	}
}
