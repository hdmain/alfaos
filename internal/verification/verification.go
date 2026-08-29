package verification

import (
	"fmt"
	"strings"

	"github.com/alfaos/alfaos/internal/config"
	"github.com/alfaos/alfaos/internal/host"
	"github.com/alfaos/alfaos/internal/logging"
	"github.com/alfaos/alfaos/internal/networking"
	"github.com/alfaos/alfaos/internal/virtualization"
)

type Result struct {
	Name    string
	Passed  bool
	Detail  string
	Repair  func() error
}

type Verifier struct {
	cfg    *config.Config
	vm     *virtualization.Manager
	vmIP   string
	results []Result
}

func New(cfg *config.Config, vm *virtualization.Manager, vmIP string) *Verifier {
	return &Verifier{cfg: cfg, vm: vm, vmIP: vmIP}
}

func (v *Verifier) RunAll(hostReq *host.Requirements) (bool, error) {
	v.results = nil

	v.add("Virtualization available", hostReq.HasVirtualization, "")
	v.add("KVM installed", v.vm.IsKVMInstalled(), "")
	v.add("QEMU installed", v.vm.IsQEMUInstalled(), "")
	v.add("libvirt working", v.vm.IsLibvirtWorking(), "")
	v.add("Virtual machine created", v.vm.DomainExists(), v.cfg.VM.Name)
	v.add("VM running", v.vm.DomainRunning(), "")

	if v.vmIP != "" {
		v.checkRemote("Debian installed", "test -f /etc/debian_version && echo ok")
		v.checkRemote("XFCE installed", "dpkg -l xfce4 2>/dev/null | grep -q ^ii && echo ok")
		v.checkRemote("Whisker Menu installed", "dpkg -l xfce4-whiskermenu-plugin 2>/dev/null | grep -q ^ii && echo ok")
		v.checkRemote("Theme installed", v.themeCheck())
		v.checkRemote("Papirus icons installed", "dpkg -l papirus-icon-theme 2>/dev/null | grep -q ^ii && echo ok")
		if v.cfg.ALFAOS.Plank {
			v.checkRemote("Plank installed", "dpkg -l plank 2>/dev/null | grep -q ^ii && echo ok")
		} else {
			v.add("Plank installed", true, "skipped (disabled)")
		}
		v.checkRemote("Wallpaper installed", "test -f /usr/share/backgrounds/alfaos/alfa1.jpeg && echo ok")
		v.checkRemote("RDP server installed", "dpkg -l xrdp 2>/dev/null | grep -q ^ii && echo ok")
		v.checkRemote("RDP service running", "systemctl is-active xrdp 2>/dev/null | grep -q active && echo ok")
		v.add("Network connectivity", networking.TestPing(v.vmIP), v.vmIP)
		v.add("RDP port reachable", networking.TestPort(v.vmIP, fmt.Sprintf("%d", v.cfg.RDP.Port)), fmt.Sprintf(":%d", v.cfg.RDP.Port))
		if v.cfg.RDP.Expose {
			hostAddr := "127.0.0.1"
			v.add("RDP exposed on host", networking.TestPort(hostAddr, fmt.Sprintf("%d", v.cfg.RDP.Port)), fmt.Sprintf("%s:%d", v.cfg.RDP.BindHost, v.cfg.RDP.Port))
		}
		v.checkRemote("Desktop session working", "test -x /home/alfaos/.xsession && grep -q xfce /home/alfaos/.xsession && echo ok")
	}

	allPassed := true
	fmt.Println()
	logging.Info("Verification Results:")
	fmt.Println()
	for _, r := range v.results {
		logging.Check(r.Name, r.Passed, r.Detail)
		if !r.Passed {
			allPassed = false
		}
	}
	fmt.Println()

	return allPassed, nil
}

func (v *Verifier) RunWithRepair(hostReq *host.Requirements, repairFuncs map[string]func() error) (bool, error) {
	maxAttempts := 3
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		passed, err := v.RunAll(hostReq)
		if err != nil {
			return false, err
		}
		if passed {
			return true, nil
		}

		if attempt == maxAttempts {
			break
		}

		logging.Warn("Some checks failed — attempting automatic repair (attempt %d/%d)...", attempt, maxAttempts)
		repaired := false
		for _, r := range v.results {
			if r.Passed {
				continue
			}
			if fn, ok := repairFuncs[r.Name]; ok && fn != nil {
				logging.Info("Repairing: %s", r.Name)
				if err := fn(); err != nil {
					logging.Error("Repair failed for %s: %v", r.Name, err)
				} else {
					repaired = true
				}
			}
		}
		if !repaired {
			logging.Error("No automatic repair available for failed components")
			break
		}
	}

	return false, nil
}

func (v *Verifier) FailedComponents() []string {
	var failed []string
	for _, r := range v.results {
		if !r.Passed {
			failed = append(failed, r.Name)
		}
	}
	return failed
}

func (v *Verifier) add(name string, passed bool, detail string) {
	v.results = append(v.results, Result{Name: name, Passed: passed, Detail: detail})
}

func (v *Verifier) checkRemote(name, cmd string) {
	out, err := v.vm.RunSSH(v.vmIP, cmd)
	passed := err == nil && strings.Contains(out, "ok")
	v.add(name, passed, "")
}

func (v *Verifier) themeCheck() string {
	theme := strings.ToLower(v.cfg.ALFAOS.Theme)
	switch theme {
	case "alfa", "arc":
		return "dpkg -l arc-theme 2>/dev/null | grep -q ^ii && echo ok"
	case "dracula":
		return "dpkg -l dracula-theme 2>/dev/null | grep -q ^ii && echo ok"
	case "gruvbox":
		return "dpkg -l gruvbox-gtk-theme 2>/dev/null | grep -q ^ii && echo ok"
	default:
		return "dpkg -l arc-theme 2>/dev/null | grep -q ^ii && echo ok"
	}
}
