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
	"github.com/alfaos/alfaos/internal/virtualization"
	"github.com/spf13/cobra"
)

var (
	cfgFile string
	force   bool
	version = "dev"
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
		Short: "Forward host RDP port (0.0.0.0:3389) to the ALFAOS VM",
		RunE:  runExposeRDP,
	}
	exposeCmd.Flags().StringVarP(&cfgFile, "config", "c", "", "Path to config file (default: configs/default.yaml)")

	startCmd := vmCommand("start", "Start the ALFAOS VM", func(vm *virtualization.Manager) error {
		return vm.StartVM()
	})
	shutdownCmd := vmCommand("shutdown", "Gracefully shut down the ALFAOS VM", func(vm *virtualization.Manager) error {
		return vm.ShutdownVM(0)
	})
	rebootCmd := vmCommand("reboot", "Reboot the ALFAOS VM", func(vm *virtualization.Manager) error {
		return vm.RebootVM()
	})

	versionCmd := &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("alfaos version %s\n", version)
		},
	}

	rootCmd.AddCommand(installCmd, connectCmd, exposeCmd, startCmd, shutdownCmd, rebootCmd, versionCmd)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func runConnect(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	return connect.Run(cfg)
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
	vmIP, err := readVMIP(cfg, vm)
	if err != nil {
		return err
	}
	bind := cfg.RDP.BindHost
	if bind == "" {
		bind = "0.0.0.0"
	}
	if err := networking.ExposeRDP(cfg.Paths.StateDir, bind, cfg.RDP.Port, vmIP, cfg.RDP.Port); err != nil {
		return err
	}
	host := networking.RDPConnectAddress(cfg.RDP.Port, true, vmIP)
	fmt.Printf("RDP proxy: %s:%d → VM %s:%d\n", bind, cfg.RDP.Port, vmIP, cfg.RDP.Port)
	if networking.TestPort("127.0.0.1", fmt.Sprintf("%d", cfg.RDP.Port)) {
		fmt.Printf("Local check OK: port %d is listening on host\n", cfg.RDP.Port)
	} else {
		fmt.Printf("WARNING: port %d not listening — run: systemctl status alfaos-rdp-forward\n", cfg.RDP.Port)
	}
	if host != "" && host != vmIP {
		fmt.Printf("Connect from outside: rdesktop %s -u %s -p %s -g %s\n", host, cfg.ALFAOS.Username, cfg.ALFAOS.Password, cfg.RDPResolution())
		fmt.Printf("If that fails, open TCP %d in your VPS provider firewall\n", cfg.RDP.Port)
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
