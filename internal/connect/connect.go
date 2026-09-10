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
		bin string
		run func(string) *exec.Cmd
	}{
		{"xfreerdp", func(b string) *exec.Cmd {
			return exec.Command(b, xfreerdpArgs(cfg, ip, user, pass, res)...)
		}},
		{"xfreerdp3", func(b string) *exec.Cmd {
			return exec.Command(b, xfreerdpArgs(cfg, ip, user, pass, res)...)
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
		exp := rdesktopExperience(cfg)
		cmd := exec.Command("rdesktop",
			"-g", res,
			"-u", user,
			"-p", pass,
			"-r", "clipboard:off",
			"-a", fmt.Sprintf("%d", rdpBPP(cfg)),
			"-x", exp,
			ip,
		)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}

	return fmt.Errorf("no RDP client found — install: sudo apt install freerdp3-x11")
}

// xfreerdpArgs picks compression/network profile from rdp.quality.
// LAN profiles prioritize low input lag (gfx/rfx, no modem buffering).
func xfreerdpArgs(cfg *config.Config, ip, user, pass, res string) []string {
	net, compress, bpp := freerdpProfile(cfg)
	args := []string{
		"/v:" + ip,
		"/u:" + user,
		"/p:" + pass,
		"/size:" + res,
		"/cert:ignore",
		"+clipboard",
		"/network:" + net,
		"/bpp:" + fmt.Sprintf("%d", bpp),
		"/compression-level:" + compress,
		"/sound:off",
		// Prefer GFX/RFX path — legacy bitmap + -gfx feels sluggish for mouse
		"/gfx",
		"/rfx",
	}
	q := strings.ToLower(strings.TrimSpace(cfg.RDP.Quality))
	switch q {
	case "low", "slow":
		// Bandwidth save without modem-tier update buffering
		args = append(args, "-wallpaper", "-themes", "-menu-anims", "-window-drag")
	case "medium", "med", "balanced":
		args = append(args, "-menu-anims", "-window-drag")
	}
	return args
}

func freerdpProfile(cfg *config.Config) (network, compressLevel string, bpp int) {
	switch strings.ToLower(strings.TrimSpace(cfg.RDP.Quality)) {
	case "low", "slow":
		// wan (not modem): modem adds noticeable mouse/update buffering
		return "wan", "2", 16
	case "medium", "balanced":
		return "wan", "1", 24
	case "ultra", "max":
		return "lan", "0", 32
	default: // high / lan
		return "lan", "0", 32
	}
}

func rdpBPP(cfg *config.Config) int {
	_, _, bpp := freerdpProfile(cfg)
	return bpp
}

func rdesktopExperience(cfg *config.Config) string {
	switch strings.ToLower(cfg.RDP.Quality) {
	case "low", "slow":
		return "broadband" // closer to wan; modem feels laggy
	case "medium", "balanced":
		return "broadband"
	default:
		return "lan"
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
