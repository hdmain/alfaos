package connect

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/alfaos/alfaos/internal/config"
	"github.com/alfaos/alfaos/internal/logging"
	"github.com/alfaos/alfaos/internal/networking"
)

func Run(cfg *config.Config) error {
	ip, viaProxy, err := ResolveVMIP(cfg)
	if err != nil {
		return err
	}

	res := cfg.RDPResolution()
	user := cfg.ALFAOS.Username
	pass := cfg.ALFAOS.Password

	if viaProxy {
		fmt.Fprintf(os.Stderr, "Connecting via host proxy %s at %s (VM waking/stopped — higher latency)\n", ip, res)
	} else {
		fmt.Fprintf(os.Stderr, "Connecting directly to VM %s at %s...\n", ip, res)
	}

	for _, client := range []struct {
		bin  string
		run  func(string) *exec.Cmd
	}{
		{"xfreerdp", func(b string) *exec.Cmd {
			return exec.Command(b, xfreerdpArgs(ip, user, pass, res)...)
		}},
		{"xfreerdp3", func(b string) *exec.Cmd {
			return exec.Command(b, xfreerdpArgs(ip, user, pass, res)...)
		}},
	} {
		if _, err := exec.LookPath(client.bin); err == nil {
			cmd := client.run(client.bin)
			cmd.Stdin = os.Stdin
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			return cmd.Run()
		}
	}

	if _, err := exec.LookPath("rdesktop"); err == nil {
		cmd := exec.Command("rdesktop",
			"-g", res,
			"-u", user,
			"-p", pass,
			"-r", "clipboard:off",
			"-a", "16",
			"-x", "lan",
			ip,
		)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}

	return fmt.Errorf("no RDP client found — install: sudo apt install freerdp3-x11")
}

// xfreerdpArgs returns low-latency flags for LAN/homelab use.
func xfreerdpArgs(ip, user, pass, res string) []string {
	return []string{
		"/v:" + ip,
		"/u:" + user,
		"/p:" + pass,
		"/size:" + res,
		"/cert:ignore",
		"+clipboard",
		"/network:lan",
		"/gfx",
		"/rfx",
		"/compression-level:2",
		"/sound:off",
	}
}

// ResolveVMIP returns the RDP target and whether traffic goes through the host proxy.
// Direct VM IP is preferred when the guest is up — the userspace proxy adds noticeable lag.
func ResolveVMIP(cfg *config.Config) (ip string, viaProxy bool, err error) {
	port := cfg.RDP.Port
	if port <= 0 {
		port = 3389
	}
	portStr := fmt.Sprintf("%d", port)

	if vmIP, err := lookupVMIP(cfg); err == nil {
		if networking.TestPort(vmIP, portStr) {
			return vmIP, false, nil
		}
		logging.Info("VM at %s:%d not reachable — using host proxy (wake-on-RDP)", vmIP, port)
	}

	if cfg.RDP.Expose {
		if networking.TestPort("127.0.0.1", portStr) {
			return "127.0.0.1", true, nil
		}
		rdpFile := filepath.Join(cfg.Paths.StateDir, "rdp.address")
		if data, err := os.ReadFile(rdpFile); err == nil {
			if host := strings.TrimSpace(string(data)); host != "" {
				return host, true, nil
			}
		}
		if host := networking.GetHostPrimaryIPv4(); host != "" {
			return host, true, nil
		}
	}

	if vmIP, err := lookupVMIP(cfg); err == nil {
		return vmIP, false, nil
	}

	return "", false, fmt.Errorf("VM IP not found — run: sudo alfaos install (or start VM for direct RDP)")
}

func lookupVMIP(cfg *config.Config) (string, error) {
	ipFile := filepath.Join(cfg.Paths.StateDir, "vm.ip")
	if data, err := os.ReadFile(ipFile); err == nil {
		if ip := strings.TrimSpace(string(data)); ip != "" {
			return ip, nil
		}
	}

	out, err := runVirsh("domifaddr", cfg.VM.Name)
	if err != nil {
		return "", err
	}

	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 4 && strings.Contains(fields[3], ".") && !strings.HasPrefix(fields[3], "127.") {
			return strings.TrimSuffix(fields[3], "/24"), nil
		}
	}

	return "", fmt.Errorf("could not determine VM IP for %q", cfg.VM.Name)
}

func runVirsh(args ...string) (string, error) {
	cmd := exec.Command("virsh", args...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), nil
	}

	cmd = exec.Command("sudo", append([]string{"virsh"}, args...)...)
	out, err = cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("virsh %v: %w\n%s", args, err, out)
	}
	return string(out), nil
}
