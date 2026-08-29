package install

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/alfaos/alfaos/internal/config"
	"github.com/alfaos/alfaos/internal/debian"
	"github.com/alfaos/alfaos/internal/desktop"
	"github.com/alfaos/alfaos/internal/host"
	"github.com/alfaos/alfaos/internal/logging"
	"github.com/alfaos/alfaos/internal/networking"
	"github.com/alfaos/alfaos/internal/rdp"
	"github.com/alfaos/alfaos/internal/verification"
	"github.com/alfaos/alfaos/internal/virtualization"
	"github.com/alfaos/alfaos/internal/wallpapers"
)

type Installer struct {
	cfg    *config.Config
	vm     *virtualization.Manager
	deb    *debian.Installer
	desk   *desktop.Configurator
	rdpCfg *rdp.Configurator
	wall   *wallpapers.Manager
}

func New(cfg *config.Config) *Installer {
	vm := virtualization.New(cfg)
	return &Installer{
		cfg:    cfg,
		vm:     vm,
		deb:    debian.New(cfg),
		desk:   desktop.New(cfg, vm),
		rdpCfg: rdp.New(cfg, vm),
		wall:   wallpapers.New(cfg, vm),
	}
}

func (i *Installer) Run(force bool) error {
	logging.Banner()

	const totalSteps = 12

	// Step 1: Host requirements
	logging.Step(1, totalSteps, "Checking host requirements")
	hostReq, err := host.CheckRequirements()
	if err != nil {
		return err
	}
	i.cfg.AdaptToHost(hostReq.MemoryMB, hostReq.DiskFreeGB)

	// Step 2: Load KVM module
	logging.Step(2, totalSteps, "Loading KVM kernel module")
	if err := host.LoadKVMModule(); err != nil {
		logging.Warn("KVM module load: %v", err)
	}

	// Step 3: Install host packages
	logging.Step(3, totalSteps, "Installing virtualization packages")
	if err := host.InstallHostPackages(hostReq.Distro.PackageManager); err != nil {
		return fmt.Errorf("host package install: %w", err)
	}

	// Step 4: Enable services
	logging.Step(4, totalSteps, "Enabling libvirt services")
	if err := host.EnableServices(); err != nil {
		logging.Warn("Service enable: %v", err)
	}

	// Step 5: Configure networking
	logging.Step(5, totalSteps, "Configuring libvirt networking")
	if err := networking.ConfigureLibvirt(); err != nil {
		return fmt.Errorf("network config: %w", err)
	}

	// Step 6: Ensure state directories
	if err := i.cfg.EnsureDirs(); err != nil {
		return err
	}
	if err := i.cfg.WriteSystemConfig(); err != nil {
		logging.Warn("Could not write /etc/alfaos/config.yaml: %v", err)
	}

	// Extract wallpapers to state dir early
	_ = i.wall.ExtractToStateDir()
	if err := wallpapers.CopyFromWorkspace(findAssetsDir(), i.cfg.Paths.StateDir); err != nil {
		logging.Warn("Could not copy workspace wallpapers: %v", err)
	}

	// Step 7: Download and verify ISO
	logging.Step(6, totalSteps, "Downloading Debian ISO")
	if err := i.deb.DownloadISO(); err != nil {
		return fmt.Errorf("ISO download: %w", err)
	}

	// Step 8: Generate preseed and create VM
	logging.Step(7, totalSteps, "Creating ALFAOS virtual machine")
	preseedPath, err := i.deb.GeneratePreseed()
	if err != nil {
		return fmt.Errorf("preseed generation: %w", err)
	}

	if force && i.vm.DomainExists() {
		logging.Info("Force mode: removing existing VM")
		_ = i.vm.UndefineVM()
	}

	needsInstall := !i.vm.DomainExists()
	if i.vm.DomainExists() && !i.vm.IsDebianInstalledOnDisk() {
		logging.Warn("VM exists but has no Debian installation — recreating VM")
		_ = i.vm.UndefineVM()
		needsInstall = true
	}

	if needsInstall {
		if err := i.vm.CreateVM(i.cfg.ISOPath(), preseedPath); err != nil {
			return fmt.Errorf("VM creation: %w", err)
		}

		logging.Step(8, totalSteps, "Waiting for automated Debian installation")
		if err := i.vm.WaitForShutdown(45 * time.Minute); err != nil {
			return fmt.Errorf("Debian install: %w", err)
		}

		if !i.vm.IsDebianInstalledOnDisk() {
			return fmt.Errorf("Debian installation did not complete — run: sudo /alfaos install --force")
		}
		logging.Success("Debian installation verified on disk")
	} else {
		logging.Info("VM already exists with Debian installed, skipping installation")
	}

	// Configure direct kernel boot (bypasses SeaBIOS/GRUB boot issues)
	if err := i.vm.SetupDirectKernelBoot(); err != nil {
		return fmt.Errorf("direct kernel boot setup: %w", err)
	}

	// Step 9: Start VM
	logging.Step(9, totalSteps, "Starting ALFAOS virtual machine")
	if err := i.vm.StartVM(); err != nil {
		return fmt.Errorf("start VM: %w", err)
	}

	// Wait for VM to boot and obtain DHCP lease
	time.Sleep(20 * time.Second)

	vmIP, err := i.vm.GetVMIP(10 * time.Minute)
	if err != nil {
		return fmt.Errorf("get VM IP: %w", err)
	}

	if err := i.vm.WaitForSSH(vmIP, 15*time.Minute); err != nil {
		logging.Warn("SSH failed, power-cycling VM and retrying...")
		_ = i.vm.StopVM()
		time.Sleep(5 * time.Second)
		if err := i.vm.StartVM(); err != nil {
			return fmt.Errorf("restart VM: %w", err)
		}
		time.Sleep(30 * time.Second)

		vmIP, err = i.vm.GetVMIP(5 * time.Minute)
		if err != nil {
			return fmt.Errorf("get VM IP after reboot: %w", err)
		}
		if err := i.vm.WaitForSSH(vmIP, 10*time.Minute); err != nil {
			return fmt.Errorf("SSH wait: %w", err)
		}
	}

	// Step 10: Install desktop
	logging.Step(10, totalSteps, "Installing ALFAOS desktop environment")
	if err := i.wall.Install(vmIP); err != nil {
		return fmt.Errorf("wallpapers: %w", err)
	}
	if err := i.desk.Install(vmIP); err != nil {
		return fmt.Errorf("desktop: %w", err)
	}

	// Step 11: Install RDP
	logging.Step(11, totalSteps, "Installing and configuring RDP server")
	if err := i.rdpCfg.Install(vmIP); err != nil {
		return fmt.Errorf("rdp: %w", err)
	}

	// Reboot VM to apply desktop/RDP config
	logging.Info("Rebooting VM to apply configuration...")
	_, _ = i.vm.RunSSH(vmIP, "sudo reboot")
	time.Sleep(30 * time.Second)

	vmIP, err = i.vm.GetVMIP(5 * time.Minute)
	if err != nil {
		logging.Warn("Could not refresh VM IP: %v", err)
	} else {
		_ = i.vm.WaitForSSH(vmIP, 5*time.Minute)
	}

	if i.cfg.RDP.Expose {
		if err := networking.ExposeRDP(i.cfg, vmIP); err != nil {
			logging.Warn("RDP port forward: %v", err)
		}
	}

	// Step 12: Verification
	logging.Step(12, totalSteps, "Running verification tests")
	verifier := verification.New(i.cfg, i.vm, vmIP)

	repairFuncs := i.buildRepairFuncs(vmIP)

	passed, err := verifier.RunWithRepair(hostReq, repairFuncs)
	if err != nil {
		return err
	}

	i.printFinalReport(vmIP, passed, verifier.FailedComponents())
	_ = os.WriteFile(filepath.Join(i.cfg.Paths.StateDir, "vm.ip"), []byte(vmIP), 0644)
	_ = os.WriteFile(filepath.Join(i.cfg.Paths.StateDir, "vm.name"), []byte(i.cfg.VM.Name), 0644)
	if i.cfg.RDP.Expose {
		rdpAddr := networking.RDPConnectAddress(i.cfg.RDP.Port, true, vmIP)
		_ = os.WriteFile(filepath.Join(i.cfg.Paths.StateDir, "rdp.address"), []byte(rdpAddr), 0644)
	}
	if !passed {
		return fmt.Errorf("ALFAOS installation completed with verification failures")
	}

	return nil
}

func (i *Installer) buildRepairFuncs(vmIP string) map[string]func() error {
	return map[string]func() error{
		"VM running": func() error {
			return i.vm.StartVM()
		},
		"libvirt working": func() error {
			_, err := host.RunCommand("systemctl", "restart", "libvirtd")
			return err
		},
		"RDP service running": func() error {
			_, err := i.vm.RunSSH(vmIP, "sudo systemctl restart xrdp")
			return err
		},
		"RDP server installed": func() error {
			return i.rdpCfg.Install(vmIP)
		},
		"XFCE installed": func() error {
			return i.desk.Install(vmIP)
		},
		"Desktop session working": func() error {
			return i.rdpCfg.Install(vmIP)
		},
		"RDP port reachable": func() error {
			if i.cfg.RDP.Expose {
				return networking.ExposeRDP(i.cfg, vmIP)
			}
			return nil
		},
	}
}

func (i *Installer) printFinalReport(vmIP string, passed bool, failed []string) {
	rdpHost := vmIP
	if i.cfg.RDP.Expose {
		if addr := networking.RDPConnectAddress(i.cfg.RDP.Port, true, vmIP); addr != "" {
			rdpHost = addr
		}
	}

	fmt.Println()
	fmt.Println("═══════════════════════════════════════════════════════════")
	if passed {
		fmt.Println("  ALFAOS INSTALLATION SUCCESSFUL")
	} else {
		fmt.Println("  ALFAOS INSTALLATION COMPLETED WITH ERRORS")
	}
	fmt.Println("═══════════════════════════════════════════════════════════")
	fmt.Println()
	fmt.Printf("  VM Name:     %s\n", i.cfg.VM.Name)
	fmt.Printf("  VM IP:       %s\n", vmIP)
	fmt.Printf("  RDP Address: %s:%d\n", rdpHost, i.cfg.RDP.Port)
	if i.cfg.RDP.Expose && rdpHost != vmIP {
		bind := i.cfg.RDP.BindHost
		if bind == "" {
			bind = "0.0.0.0"
		}
		fmt.Printf("  (forwarded from %s:%d on host)\n", bind, i.cfg.RDP.Port)
	}
	fmt.Printf("  Username:    %s\n", i.cfg.ALFAOS.Username)
	fmt.Printf("  Password:    %s\n", i.cfg.ALFAOS.Password)
	fmt.Println()
	if i.cfg.RDP.Expose && i.cfg.Power.WakeOnRDP {
		fmt.Println("  Power saving:")
		fmt.Println("    • Host keeps TCP 3389 open even when the VM is off")
		fmt.Println("    • Connecting with any RDP client starts the VM automatically")
		if i.cfg.Power.IdleShutdownMinutes > 0 {
			fmt.Printf("    • VM shuts down after %d minutes without RDP sessions\n", i.cfg.Power.IdleShutdownMinutes)
		}
		fmt.Println("    • First connect after idle may take 30–90s (VM boot)")
		fmt.Println()
	}
	fmt.Println("  Connect with any RDP client:")
	fmt.Printf("    alfaos connect                    # %s, recommended\n", i.cfg.RDPResolution())
	fmt.Printf("    xfreerdp /v:%s /u:%s /p:%s /size:%s\n", rdpHost, i.cfg.ALFAOS.Username, i.cfg.ALFAOS.Password, i.cfg.RDPResolution())
	fmt.Printf("    rdesktop %s -u %s -p %s -g %s\n", rdpHost, i.cfg.ALFAOS.Username, i.cfg.ALFAOS.Password, i.cfg.RDPResolution())
	fmt.Println()

	if !passed {
		fmt.Println("  Failed components:")
		for _, f := range failed {
			fmt.Printf("    - %s\n", f)
		}
		fmt.Println()
	}

	fmt.Println("  End-to-end path verified:")
	fmt.Println("    Host → KVM/libvirt → ALFAOS VM → Debian → XFCE → RDP")
	fmt.Println()
}

func findAssetsDir() string {
	candidates := []string{
		"assets",
		"/usr/share/alfaos/assets",
		filepath.Join(filepath.Dir(os.Args[0]), "..", "share", "alfaos", "assets"),
	}
	for _, c := range candidates {
		if _, err := os.Stat(filepath.Join(c, "alfa1.jpeg")); err == nil {
			return c
		}
	}
	return "assets"
}
