package main

import (
	"fmt"
	"os"

	"github.com/alfaos/alfaos/internal/config"
	"github.com/alfaos/alfaos/internal/connect"
	"github.com/alfaos/alfaos/internal/install"
	"github.com/alfaos/alfaos/internal/logging"
	"github.com/alfaos/alfaos/internal/virtualization"
	"github.com/spf13/cobra"
)

var (
	cfgFile string
	force   bool
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
			fmt.Println("alfaos version 1.0.0")
		},
	}

	rootCmd.AddCommand(installCmd, connectCmd, startCmd, shutdownCmd, rebootCmd, versionCmd)

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
