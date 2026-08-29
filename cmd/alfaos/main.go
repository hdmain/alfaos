package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alfaos/alfaos/internal/config"
	"github.com/alfaos/alfaos/internal/connect"
	"github.com/alfaos/alfaos/internal/install"
	"github.com/alfaos/alfaos/internal/logging"
	"github.com/alfaos/alfaos/internal/networking"
	"github.com/alfaos/alfaos/internal/passwd"
	"github.com/alfaos/alfaos/internal/power"
	"github.com/alfaos/alfaos/internal/virtualization"
	"github.com/spf13/cobra"
)

var (
	cfgFile     string
	force       bool
	newPassword string
	version     = "dev"
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "alfaos",
		Short: "ALFAOS — Automated Linux Framework for Alpha OS",
		Long:  "Automatically build, install, configure, and test the ALFAOS desktop system on KVM.",
	}

	installCmd := &cobra.Command{
		Use:   "install",
		Short: "Install and configure ALFAOS on this host and inside a KVM virtual machine",
		Long: `Perform a fully automated ALFAOS installation:

  • Verify host requirements and hardware virtualization
  • Install KVM, QEMU, libvirt, and related tools
  • Download and verify Debian ISO
  • Create and provision the ALFAOS virtual machine
  • Install XFCE desktop with themes, icons, and wallpapers
  • Configure RDP for remote desktop access
  • Run end-to-end verification tests`,
		RunE: runInstall,
	}

	installCmd.Flags().StringVarP(&cfgFile, "config", "c", "", "Path to config file (default: configs/default.yaml)")
	installCmd.Flags().BoolVar(&force, "force", false, "Recreate VM if it already exists")

	connectCmd := &cobra.Command{
		Use:   "connect",
		Short: "Connect to ALFAOS VM via RDP at configured resolution (default 1920x1080)",
		RunE:  runConnect,
	}
	connectCmd.Flags().StringVarP(&cfgFile, "config", "c", "", "Path to config file (default: configs/default.yaml)")

	exposeCmd := &cobra.Command{
		Use:   "expose-rdp",
		Short: "Forward host RDP port (0.0.0.0:3389) to the ALFAOS VM (wake-on-connect)",
		RunE:  runExposeRDP,
	}
	exposeCmd.Flags().StringVarP(&cfgFile, "config", "c", "", "Path to config file (default: configs/default.yaml)")

	rdpProxyCmd := &cobra.Command{
		Use:    "rdp-proxy",
		Short:  "Run host RDP proxy daemon (wake-on-connect + idle shutdown)",
		Hidden: true,
		RunE:   runRDPProxy,
	}
	rdpProxyCmd.Flags().StringVarP(&cfgFile, "config", "c", "", "Path to config file")

	startCmd := vmCommand("start", "Start the ALFAOS VM", func(vm *virtualization.Manager) error {
		return vm.StartVM()
	})
	shutdownCmd := vmCommand("shutdown", "Gracefully shut down the ALFAOS VM", func(vm *virtualization.Manager) error {
		return vm.ShutdownVM(0)
	})
	rebootCmd := vmCommand("reboot", "Reboot the ALFAOS VM", func(vm *virtualization.Manager) error {
		return vm.RebootVM()
	})

	passwdCmd := &cobra.Command{
		Use:   "passwd",
		Short: "Change ALFAOS VM user password (config and guest)",
		Long: `Change the password for the ALFAOS desktop user.

Updates /etc/alfaos/config.yaml and, when the VM is installed, changes the
password inside the guest via SSH (starts the VM if it is stopped).`,
		RunE: runPasswd,
	}
	passwdCmd.Flags().StringVarP(&cfgFile, "config", "c", "", "Path to config file (default: /etc/alfaos/config.yaml)")
	passwdCmd.Flags().StringVar(&newPassword, "password", "", "New password (non-interactive; avoid on shared shells)")

	versionCmd := &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("alfaos version %s\n", version)
		},
	}

	rootCmd.AddCommand(installCmd, connectCmd, exposeCmd, rdpProxyCmd, startCmd, shutdownCmd, rebootCmd, passwdCmd, versionCmd)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func runPasswd(cmd *cobra.Command, args []string) error {
	if err := virtualization.EnsureLibvirtAccess(); err != nil {
		return err
	}

	cfgPath := config.ResolvePath(cfgFile)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}

	pass, err := passwd.PromptNewPassword(newPassword)
	if err != nil {
		return err
	}
	return passwd.Change(cfg, cfgPath, pass)
}

func runConnect(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	return connect.Run(cfg)
}

func runRDPProxy(cmd *cobra.Command, args []string) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("alfaos rdp-proxy must be run as root")
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	return power.Run(cfg)
}

func runExposeRDP(cmd *cobra.Command, args []string) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("alfaos expose-rdp must be run as root: sudo alfaos expose-rdp")
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	vm := virtualization.New(cfg)
	vmIP, _ := readVMIP(cfg, vm) // optional — proxy can wake a stopped VM
	if err := networking.ExposeRDP(cfg, vmIP); err != nil {
		return err
	}
	bind := cfg.RDP.BindHost
	if bind == "" {
		bind = "0.0.0.0"
	}
	fmt.Printf("RDP proxy: %s:%d → VM (wake_on_rdp=%v, idle=%dm)\n",
		bind, cfg.RDP.Port, cfg.Power.WakeOnRDP, cfg.Power.IdleShutdownMinutes)
	if networking.TestPort("127.0.0.1", fmt.Sprintf("%d", cfg.RDP.Port)) {
		fmt.Printf("Local check OK: port %d is listening on host\n", cfg.RDP.Port)
	} else {
		fmt.Printf("WARNING: port %d not listening — run: systemctl status alfaos-rdp-forward\n", cfg.RDP.Port)
	}
	host := networking.RDPConnectAddress(cfg.RDP.Port, true, vmIP)
	if host != "" {
		fmt.Printf("Connect from outside: rdesktop %s -u %s -p %s -g %s\n", host, cfg.ALFAOS.Username, cfg.ALFAOS.Password, cfg.RDPResolution())
		fmt.Printf("If that fails, open TCP %d in your VPS provider firewall\n", cfg.RDP.Port)
	}
	if cfg.Power.WakeOnRDP {
		fmt.Println("Tip: VM may be shut down while idle — your RDP client will wake it (first connect can take 30–90s)")
	}
	return nil
}

func readVMIP(cfg *config.Config, vm *virtualization.Manager) (string, error) {
	ipFile := filepath.Join(cfg.Paths.StateDir, "vm.ip")
	if data, err := os.ReadFile(ipFile); err == nil {
		if ip := strings.TrimSpace(string(data)); ip != "" {
			return ip, nil
		}
	}
	if !vm.DomainRunning() {
		return "", fmt.Errorf("VM is not running")
	}
	vmIP, err := vm.GetVMIP(2 * time.Minute)
	if err != nil {
		return "", fmt.Errorf("VM IP: %w", err)
	}
	return vmIP, nil
}

func vmCommand(use, short string, fn func(*virtualization.Manager) error) *cobra.Command {
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := virtualization.EnsureLibvirtAccess(); err != nil {
				return err
			}
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			return fn(virtualization.New(cfg))
		},
	}
	cmd.Flags().StringVarP(&cfgFile, "config", "c", "", "Path to config file (default: configs/default.yaml)")
	return cmd
}

func loadConfig() (*config.Config, error) {
	cfgPath := cfgFile
	if cfgPath == "" {
		cfgPath = findConfig()
	}
	return config.Load(cfgPath)
}

func runInstall(cmd *cobra.Command, args []string) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("alfaos install must be run as root: sudo alfaos install")
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	installer := install.New(cfg)
	if err := installer.Run(force); err != nil {
		logging.Error("%v", err)
		return err
	}

	logging.Success("ALFAOS is ready!")
	return nil
}

func findConfig() string {
	candidates := []string{
		"configs/default.yaml",
		"/etc/alfaos/config.yaml",
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}
