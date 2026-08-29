package connect

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/alfaos/alfaos/internal/config"
)

func Run(cfg *config.Config) error {
	ip, err := ResolveVMIP(cfg)
	if err != nil {
		return err
	}

	res := cfg.RDPResolution()
	user := cfg.ALFAOS.Username
	pass := cfg.ALFAOS.Password

	fmt.Fprintf(os.Stderr, "Connecting to %s at %s...\n", ip, res)

	for _, client := range []struct {
		bin  string
		run  func(string) *exec.Cmd
	}{
		{"xfreerdp", func(b string) *exec.Cmd {
			return exec.Command(b, "/v:"+ip, "/u:"+user, "/p:"+pass, "/size:"+res, "/cert:ignore", "+clipboard")
		}},
		{"xfreerdp3", func(b string) *exec.Cmd {
			return exec.Command(b, "/v:"+ip, "/u:"+user, "/p:"+pass, "/size:"+res, "/cert:ignore", "+clipboard")
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
			ip,
		)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}

	return fmt.Errorf("no RDP client found — install: sudo apt install rdesktop freerdp3-x11")
}

func ResolveVMIP(cfg *config.Config) (string, error) {
	ipFile := filepath.Join(cfg.Paths.StateDir, "vm.ip")
	if data, err := os.ReadFile(ipFile); err == nil {
		if ip := strings.TrimSpace(string(data)); ip != "" {
			return ip, nil
		}
	}

	out, err := runVirsh("domifaddr", cfg.VM.Name)
	if err != nil {
		return "", fmt.Errorf("VM IP not found — run: sudo alfaos install (or save IP to %s)", ipFile)
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
