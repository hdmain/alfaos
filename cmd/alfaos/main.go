package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alfaos/alfaos/internal/backup"
	"github.com/alfaos/alfaos/internal/config"
	"github.com/alfaos/alfaos/internal/connect"
	"github.com/alfaos/alfaos/internal/install"
	"github.com/alfaos/alfaos/internal/logging"
	"github.com/alfaos/alfaos/internal/networking"
	"github.com/alfaos/alfaos/internal/passwd"
	"github.com/alfaos/alfaos/internal/power"
	"github.com/alfaos/alfaos/internal/rdp"
	"github.com/alfaos/alfaos/internal/virtualization"
	"github.com/spf13/cobra"
)

var (
	cfgFile         string
	force           bool
	newPassword     string
	onioningStable  bool
	version         = "dev"
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

	onioningCmd := &cobra.Command{
		Use:   "onioning [on|off|stable|status]",
		Short: "Route all VM internet through Tor with killswitch (or no net)",
		Long: `Enable or disable Tor-only networking for the ALFAOS VM.

  • on              — Tor + killswitch (privacy: new exit IP per destination/port)
  • on --stable     — Tor + killswitch with one shared exit IP (~10 min rotation)
  • stable on|off   — toggle stable IP while onioning stays on
  • off             — remove Tor redirect and killswitch; normal NAT again
  • status          — show whether onioning/killswitch is active

If Tor is down while onioning is on, the VM has no internet (fail-closed).
RDP keeps working: clients connect to the host, which proxies to the VM locally.

Examples:
  sudo alfaos onioning on
  sudo alfaos onioning on --stable
  sudo alfaos onioning stable on
  sudo alfaos onioning stable off
  sudo alfaos onioning off
  alfaos onioning status`,
		Args: cobra.MaximumNArgs(2),
		RunE: runOnioning,
	}
	onioningCmd.Flags().StringVarP(&cfgFile, "config", "c", "", "Path to config file")
	onioningCmd.Flags().BoolVar(&onioningStable, "stable", false, "Use one Tor exit IP for all traffic (~10 min rotation)")

	exportCmd := &cobra.Command{
		Use:   "export <file.tar.gz>",
		Short: "Export config + VM disk to a single .tar.gz backup",
		Long: `Shut down the VM (if running) and pack into one archive:

  • config.yaml
  • domain.xml (libvirt)
  • disk.qcow2
  • boot kernel/initrd (if present)

Example:
  sudo alfaos export /root/alfaos-backup.tar.gz`,
		Args: cobra.ExactArgs(1),
		RunE: runExport,
	}
	exportCmd.Flags().StringVarP(&cfgFile, "config", "c", "", "Path to config file")

	importCmd := &cobra.Command{
		Use:   "import <file.tar.gz>",
		Short: "Import config + VM disk from an alfaos export archive",
		Long: `Restore ALFAOS from a .tar.gz created by alfaos export.

Replaces /etc/alfaos/config.yaml, restores the qcow2 disk, and defines the
libvirt domain. Use --force if a VM with the same name already exists.

Example:
  sudo alfaos import /root/alfaos-backup.tar.gz
  sudo alfaos import /root/alfaos-backup.tar.gz --force`,
		Args: cobra.ExactArgs(1),
		RunE: runImport,
	}
	importCmd.Flags().StringVarP(&cfgFile, "config", "c", "", "Path to write config (default: /etc/alfaos/config.yaml)")
	importCmd.Flags().BoolVar(&force, "force", false, "Replace existing VM with the same name")

	versionCmd := &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("alfaos version %s\n", version)
		},
	}

	rootCmd.AddCommand(installCmd, connectCmd, exposeCmd, rdpProxyCmd, startCmd, shutdownCmd, rebootCmd, passwdCmd, onioningCmd, exportCmd, importCmd, versionCmd)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func runExport(cmd *cobra.Command, args []string) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("alfaos export must be run as root: sudo alfaos export <file.tar.gz>")
	}
	if err := virtualization.EnsureLibvirtAccess(); err != nil {
		return err
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	return backup.Export(cfg, args[0])
}

func runImport(cmd *cobra.Command, args []string) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("alfaos import must be run as root: sudo alfaos import <file.tar.gz>")
	}
	if err := virtualization.EnsureLibvirtAccess(); err != nil {
		return err
	}
	cfgPath := config.ResolvePath(cfgFile)
	return backup.Import(cfgPath, args[0], force)
}

func runOnioning(cmd *cobra.Command, args []string) error {
	cfgPath := config.ResolvePath(cfgFile)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}

	if len(args) > 0 && strings.ToLower(args[0]) == "stable" {
		return runOnioningStable(cfg, cfgPath, args[1:])
	}

	action := "status"
	if len(args) > 0 {
		action = strings.ToLower(strings.TrimSpace(args[0]))
	}

	switch action {
	case "status", "":
		active := networking.OnioningActive()
		fmt.Printf("onioning config: %v\n", cfg.Onioning)
		fmt.Printf("onioning stable: %v\n", cfg.OnioningStable)
		fmt.Printf("onioning active: %v\n", active)
		fmt.Print(networking.OnioningDiagnostics(cfg.VM.Network))
		if active {
			fmt.Println("VM outbound TCP/DNS → Tor; RDP host→VM remains direct")
			if cfg.OnioningStable {
				fmt.Println("stable IP mode — exit rotates about every 10 minutes")
			} else {
				fmt.Println("privacy mode — exit IP may change per destination/port")
			}
		}
		return nil
	case "on", "true", "enable", "1":
		if os.Geteuid() != 0 {
			return fmt.Errorf("alfaos onioning on must be run as root: sudo alfaos onioning on")
		}
		stable := onioningStable
		if err := networking.ConfigureOnioning(true, stable, cfg.Paths.StateDir, cfg.VM.Network); err != nil {
			return err
		}
		cfg.Onioning = true
		cfg.OnioningStable = stable
		if err := config.Save(cfg, cfgPath); err != nil {
			logging.Warn("Could not save config: %v", err)
		}
		return nil
	case "off", "false", "disable", "0":
		if os.Geteuid() != 0 {
			return fmt.Errorf("alfaos onioning off must be run as root: sudo alfaos onioning off")
		}
		if err := networking.ConfigureOnioning(false, false, cfg.Paths.StateDir, cfg.VM.Network); err != nil {
			return err
		}
		cfg.Onioning = false
		cfg.OnioningStable = false
		if err := config.Save(cfg, cfgPath); err != nil {
			logging.Warn("Could not save config: %v", err)
		}
		return nil
	default:
		return fmt.Errorf("usage: alfaos onioning [on|off|stable|status]")
	}
}

func runOnioningStable(cfg *config.Config, cfgPath string, args []string) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("alfaos onioning stable must be run as root: sudo alfaos onioning stable on|off")
	}

	action := "status"
	if len(args) > 0 {
		action = strings.ToLower(strings.TrimSpace(args[0]))
	}

	switch action {
	case "status", "":
		fmt.Printf("onioning stable config: %v\n", cfg.OnioningStable)
		if cfg.OnioningStable {
			fmt.Println("one shared Tor exit IP (~10 min rotation)")
		} else {
			fmt.Println("privacy mode — separate exit per destination/port")
		}
		return nil
	case "on", "true", "enable", "1":
		if !cfg.Onioning {
			cfg.Onioning = true
		}
		cfg.OnioningStable = true
		if err := networking.ConfigureOnioning(true, true, cfg.Paths.StateDir, cfg.VM.Network); err != nil {
			return err
		}
		if err := config.Save(cfg, cfgPath); err != nil {
			logging.Warn("Could not save config: %v", err)
		}
		return nil
	case "off", "false", "disable", "0":
		if !cfg.Onioning {
			return fmt.Errorf("onioning is off — run: sudo alfaos onioning on")
		}
		cfg.OnioningStable = false
		if err := networking.ConfigureOnioning(true, false, cfg.Paths.StateDir, cfg.VM.Network); err != nil {
			return err
		}
		if err := config.Save(cfg, cfgPath); err != nil {
			logging.Warn("Could not save config: %v", err)
		}
		return nil
	default:
		return fmt.Errorf("usage: alfaos onioning stable [on|off|status]")
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
	if vmIP != "" && vm.DomainRunning() {
		if err := rdp.New(cfg, vm).ApplyTuning(vmIP); err != nil {
			logging.Warn("RDP tune: %v (connect may feel laggy until VM is reconfigured)", err)
		}
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
	fmt.Println("Tip: for lowest latency on LAN, connect directly to the VM IP (alfaos connect does this when the VM is running)")
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
