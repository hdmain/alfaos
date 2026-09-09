package virtualization

import (
	"fmt"
	"strings"

	"github.com/alfaos/alfaos/internal/host"
	"github.com/alfaos/alfaos/internal/logging"
)

// TunePerformance applies KVM/QEMU options that make the guest snappier
// (host CPU passthrough, faster disk I/O, virtio balloon/rng/agent).
// Safe to re-run. May briefly restart the VM so libvirt reloads the domain.
func (m *Manager) TunePerformance() error {
	if !m.DomainExists() {
		return fmt.Errorf("VM %q does not exist", m.cfg.VM.Name)
	}
	if !host.CommandExists("virt-xml") {
		return fmt.Errorf("virt-xml not found — install virtinst")
	}

	name := m.cfg.VM.Name
	wasRunning := m.DomainRunning()
	logging.Info("Tuning KVM performance for %s...", name)

	if wasRunning {
		logging.Info("Shutting down VM to apply CPU/disk tuning...")
		if err := m.ShutdownVM(0); err != nil {
			logging.Warn("Graceful shutdown failed (%v) — forcing", err)
			_ = m.StopVM()
		}
	}

	steps := []struct {
		desc string
		args []string
	}{
		{
			"CPU host-passthrough",
			[]string{name, "--edit", "--cpu", "host-passthrough,cache.mode=passthrough"},
		},
		{
			"disk writeback + threads + discard",
			[]string{name, "--edit", "--disk", "target=vda,cache=writeback,io=threads,discard=unmap"},
		},
		{
			"virtio memballoon",
			[]string{name, "--edit", "--memballoon", "model=virtio"},
		},
		{
			"virtio-scsi controller (if needed)",
			[]string{name, "--add-device", "--controller", "scsi,model=virtio-scsi"},
		},
		{
			"virtio RNG",
			[]string{name, "--add-device", "--rng", "/dev/urandom,model=virtio"},
		},
		{
			"qemu guest agent channel",
			[]string{name, "--add-device", "--channel", "unix,target_type=virtio,name=org.qemu.guest_agent.0"},
		},
	}

	cpus := m.cfg.VM.CPU
	if cpus < 1 {
		cpus = 2
	}
	steps = append(steps, struct {
		desc string
		args []string
	}{
		"virtio-net multi-queue",
		[]string{name, "--edit", "--network", fmt.Sprintf("type=network,driver.queues=%d", cpus)},
	})

	for _, s := range steps {
		out, err := host.RunCommand("virt-xml", s.args...)
		if err != nil {
			// Many "add-device" calls fail when the device already exists — OK.
			msg := strings.ToLower(out + err.Error())
			if strings.Contains(msg, "already") ||
				strings.Contains(msg, "exists") ||
				strings.Contains(msg, "duplicate") ||
				strings.Contains(msg, "no such") {
				logging.Info("  skip %s (%v)", s.desc, summarizeVirtXML(out, err))
				continue
			}
			logging.Warn("  %s: %v", s.desc, summarizeVirtXML(out, err))
			continue
		}
		logging.Success("  %s", s.desc)
	}

	// Host timers: catch-up RTC helps when host is busy
	if out, err := host.RunCommand("virt-xml", name, "--edit", "--clock", "offset=utc"); err != nil {
		logging.Warn("  clock: %v", summarizeVirtXML(out, err))
	}
	for _, t := range []string{
		"name=rtc,tickpolicy=catchup",
		"name=pit,tickpolicy=delay",
		"name=hpet,present=no",
	} {
		if out, err := host.RunCommand("virt-xml", name, "--edit", "--timer", t); err != nil {
			logging.Warn("  timer %s: %v", t, summarizeVirtXML(out, err))
		}
	}
	logging.Success("  KVM clock timers")


	if wasRunning {
		logging.Info("Starting VM after tune...")
		if err := m.StartVM(); err != nil {
			return fmt.Errorf("start after tune: %w", err)
		}
	}

	logging.Success("KVM performance tuning applied to %s", name)
	return nil
}

func summarizeVirtXML(out string, err error) string {
	s := strings.TrimSpace(out)
	if s == "" && err != nil {
		return err.Error()
	}
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	if err != nil && s != "" {
		return fmt.Sprintf("%v (%s)", err, s)
	}
	if err != nil {
		return err.Error()
	}
	return s
}
