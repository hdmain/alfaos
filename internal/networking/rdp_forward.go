package networking

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alfaos/alfaos/internal/config"
	hostpkg "github.com/alfaos/alfaos/internal/host"
	"github.com/alfaos/alfaos/internal/logging"
)

// ExposeRDP installs a host systemd service that listens on the RDP port,
// wakes the VM on connect (when configured), and proxies to guest xRDP.
func ExposeRDP(cfg *config.Config, vmIP string) error {
	hostPort := cfg.RDP.Port
	if hostPort <= 0 {
		hostPort = 3389
	}
	vmPort := hostPort
	bindAddr := cfg.RDP.BindHost
	if bindAddr == "" {
		bindAddr = "0.0.0.0"
	}

	logging.Info("Exposing RDP on %s:%d → VM (wake_on_rdp=%v, idle=%dm)",
		bindAddr, hostPort, cfg.Power.WakeOnRDP, cfg.Power.IdleShutdownMinutes)

	removeLegacyIPTables(hostPort, vmIP, vmPort)
	allowHostPort(hostPort)

	if vmIP != "" {
		if TestPort(vmIP, fmt.Sprintf("%d", vmPort)) {
			logging.Info("VM RDP reachable at %s:%d", vmIP, vmPort)
		} else {
			logging.Warn("VM RDP at %s:%d is not reachable from host yet (OK if VM is stopped)", vmIP, vmPort)
		}
	}

	if err := installProxyService(cfg, bindAddr, hostPort); err != nil {
		return fmt.Errorf("install RDP proxy service: %w", err)
	}

	time.Sleep(500 * time.Millisecond)
	if TestPort("127.0.0.1", fmt.Sprintf("%d", hostPort)) {
		logging.Success("RDP proxy listening on %s:%d", bindAddr, hostPort)
	} else {
		logging.Warn("RDP proxy may not be listening — check: systemctl status alfaos-rdp-forward")
	}

	printExposureHints(hostPort, cfg)
	return nil
}

func allowHostPort(port int) {
	portStr := fmt.Sprintf("%d", port)
	if hostpkg.CommandExists("ufw") {
		out, _ := hostpkg.RunCommand("ufw", "status")
		if strings.Contains(strings.ToLower(out), "active") {
			_, _ = hostpkg.RunCommand("ufw", "allow", portStr+"/tcp")
			logging.Info("Opened port %s/tcp in ufw", portStr)
		}
	}
	iptablesEnsure("filter", "INPUT", "-p", "tcp", "--dport", portStr, "-j", "ACCEPT")
}

func removeLegacyIPTables(hostPort int, vmIP string, vmPort int) {
	if vmIP == "" {
		return
	}
	portStr := fmt.Sprintf("%d", hostPort)
	vmPortStr := fmt.Sprintf("%d", vmPort)
	vmDest := fmt.Sprintf("%s:%s", vmIP, vmPortStr)

	legacy := [][]string{
		{"nat", "PREROUTING", "-p", "tcp", "--dport", portStr, "-j", "DNAT", "--to-destination", vmDest},
		{"nat", "OUTPUT", "-p", "tcp", "--dport", portStr, "-m", "addrtype", "--dst-type", "LOCAL", "-j", "DNAT", "--to-destination", vmDest},
		{"nat", "POSTROUTING", "-p", "tcp", "-d", vmIP, "--dport", vmPortStr, "-j", "MASQUERADE"},
		{"filter", "FORWARD", "-p", "tcp", "-d", vmIP, "--dport", vmPortStr, "-m", "state", "--state", "NEW,ESTABLISHED,RELATED", "-j", "ACCEPT"},
		{"filter", "FORWARD", "-p", "tcp", "-s", vmIP, "--sport", vmPortStr, "-m", "state", "--state", "ESTABLISHED,RELATED", "-j", "ACCEPT"},
	}
	for _, rule := range legacy {
		iptablesDelete(rule[0], rule[1], rule[2:]...)
	}
}

func iptablesEnsure(table, chain string, args ...string) {
	checkArgs := append([]string{"-t", table, "-C", chain}, args...)
	if _, err := hostpkg.RunCommand("iptables", checkArgs...); err == nil {
		return
	}
	addArgs := append([]string{"-t", table, "-A", chain}, args...)
	_, _ = hostpkg.RunCommand("iptables", addArgs...)
}

func iptablesDelete(table, chain string, args ...string) {
	delArgs := append([]string{"-t", table, "-D", chain}, args...)
	_, _ = hostpkg.RunCommand("iptables", delArgs...)
}

func installProxyService(cfg *config.Config, bindAddr string, hostPort int) error {
	bin := resolveAlfaosBinary()
	cfgPath := resolveConfigPath()
	unitPath := "/etc/systemd/system/alfaos-rdp-forward.service"

	// Drop legacy socat helper if present.
	_ = os.Remove(filepath.Join(cfg.Paths.StateDir, "run-rdp-proxy.sh"))

	execStart := fmt.Sprintf("%s rdp-proxy", bin)
	if cfgPath != "" {
		execStart = fmt.Sprintf("%s rdp-proxy --config %s", bin, cfgPath)
	}

	unit := fmt.Sprintf(`[Unit]
Description=ALFAOS RDP proxy (wake-on-connect + idle shutdown)
After=network-online.target libvirtd.service
Wants=network-online.target
Requires=libvirtd.service

[Service]
Type=simple
Restart=always
RestartSec=3
ExecStart=%s
# Keep listening on host port %d (bind %s) even when the VM is stopped.

[Install]
WantedBy=multi-user.target
`, execStart, hostPort, bindAddr)

	if err := os.WriteFile(unitPath, []byte(unit), 0644); err != nil {
		return err
	}
	_, _ = hostpkg.RunCommand("systemctl", "daemon-reload")
	_, _ = hostpkg.RunCommand("systemctl", "enable", "alfaos-rdp-forward.service")
	_, _ = hostpkg.RunCommand("systemctl", "restart", "alfaos-rdp-forward.service")
	return nil
}

func resolveAlfaosBinary() string {
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		if abs, err := filepath.Abs(exe); err == nil {
			return abs
		}
		return exe
	}
	for _, p := range []string{"/usr/local/bin/alfaos", "/alfaos", "/usr/bin/alfaos"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "alfaos"
}

func resolveConfigPath() string {
	for _, c := range []string{"/etc/alfaos/config.yaml", "configs/default.yaml"} {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

func printExposureHints(hostPort int, cfg *config.Config) {
	if ip := GetHostPrimaryIPv4(); ip != "" {
		user := "alfaos"
		pass := "alfaos"
		res := "1920x1080"
		if cfg != nil {
			user = cfg.ALFAOS.Username
			pass = cfg.ALFAOS.Password
			res = cfg.RDPResolution()
		}
		logging.Info("Connect from outside: rdesktop %s -u %s -p %s -g %s", ip, user, pass, res)
	}
	logging.Info("Verify on VPS: ss -tlnp | grep %d", hostPort)
	logging.Info("If external connect still fails, open TCP %d in your VPS provider firewall/panel", hostPort)
	if cfg != nil && cfg.Power.WakeOnRDP {
		logging.Info("Wake-on-RDP: host keeps port %d open; connecting starts the VM if it was shut down", hostPort)
	}
	if cfg != nil && cfg.Power.IdleShutdownMinutes > 0 {
		logging.Info("Idle shutdown: VM stops after %d minutes without RDP sessions", cfg.Power.IdleShutdownMinutes)
	}
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
