package virtualization

import (
	"fmt"
	"os"
	"regexp"
	"runtime"
	"strconv"
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
	// Ensure fully off before XML edits
	if m.DomainRunning() {
		_ = m.StopVM()
	}

	xml, _ := m.runVirsh("dumpxml", name)
	hostCPUs := hostCPUCount()

	// Cap vCPUs to what this cloud host can actually run (avoids KVM SMP warnings / failures).
	if hostCPUs > 0 {
		want := m.cfg.VM.CPU
		if want < 1 {
			want = 2
		}
		if want > hostCPUs {
			logging.Warn("Host has %d CPU(s) — capping VM from %d to %d vCPUs", hostCPUs, want, hostCPUs)
			want = hostCPUs
			m.cfg.VM.CPU = want
		}
		if out, err := host.RunCommand("virt-xml", name, "--edit", "--vcpus", strconv.Itoa(want)); err != nil {
			logging.Warn("  vcpus: %v", summarizeVirtXML(out, err))
		} else {
			logging.Success("  vCPUs = %d", want)
		}
	}

	edits := []struct {
		desc string
		args []string
	}{
		{"CPU host-passthrough", []string{name, "--edit", "--cpu", "host-passthrough,cache.mode=passthrough"}},
		{"disk writeback + threads + discard", []string{name, "--edit", "--disk", "target=vda,cache=writeback,io=threads,discard=unmap"}},
		{"virtio memballoon", []string{name, "--edit", "--memballoon", "model=virtio"}},
		{"virtio-net multi-queue", []string{name, "--edit", "--network", fmt.Sprintf("driver.queues=%d", max(1, m.cfg.VM.CPU))}},
		{"clock utc", []string{name, "--edit", "--clock", "offset=utc"}},
	}
	for _, s := range edits {
		out, err := host.RunCommand("virt-xml", s.args...)
		if err != nil {
			logging.Warn("  %s: %v", s.desc, summarizeVirtXML(out, err))
			continue
		}
		logging.Success("  %s", s.desc)
	}

	// Add-only devices — skip when already present in domain XML.
	if !strings.Contains(xml, "virtio-scsi") && !strings.Contains(xml, "model='virtio-scsi'") {
		if out, err := host.RunCommand("virt-xml", name, "--add-device", "--controller", "scsi,model=virtio-scsi"); err != nil {
			logging.Warn("  virtio-scsi: %v", summarizeVirtXML(out, err))
		} else {
			logging.Success("  virtio-scsi controller")
		}
	} else {
		logging.Info("  skip virtio-scsi (already present)")
	}

	if !strings.Contains(xml, "rng") && !strings.Contains(xml, "virtio-rng") {
		if out, err := host.RunCommand("virt-xml", name, "--add-device", "--rng", "/dev/urandom,model=virtio"); err != nil {
			logging.Warn("  virtio RNG: %v", summarizeVirtXML(out, err))
		} else {
			logging.Success("  virtio RNG")
		}
	} else {
		logging.Info("  skip virtio RNG (already present)")
	}

	// Guest agent: keep exactly one channel. Previous tune could add duplicates and break start.
	if err := m.ensureSingleGuestAgentChannel(); err != nil {
		logging.Warn("  guest agent channel: %v", err)
	} else {
		logging.Success("  qemu guest agent channel (single)")
	}

	if wasRunning {
		logging.Info("Starting VM after tune...")
		if err := m.StartVM(); err != nil {
			// One more repair pass for duplicate channel, then retry.
			_ = m.ensureSingleGuestAgentChannel()
			if err2 := m.StartVM(); err2 != nil {
				return fmt.Errorf("start after tune: %w", err2)
			}
		}
	}

	logging.Success("KVM performance tuning applied to %s", name)
	return nil
}

// ensureSingleGuestAgentChannel removes duplicate org.qemu.guest_agent.0 channels
// and adds one if missing.
func (m *Manager) ensureSingleGuestAgentChannel() error {
	name := m.cfg.VM.Name
	xml, err := m.runVirsh("dumpxml", name)
	if err != nil {
		return err
	}

	const marker = "org.qemu.guest_agent.0"
	count := strings.Count(xml, marker)
	if count == 1 {
		return nil
	}

	if count > 1 {
		logging.Warn("Removing %d duplicate guest-agent channels...", count-1)
		cleaned := removeDuplicateGuestAgentChannels(xml)
		tmp, err := os.CreateTemp("", "alfaos-domain-*.xml")
		if err != nil {
			return err
		}
		path := tmp.Name()
		if _, err := tmp.WriteString(cleaned); err != nil {
			tmp.Close()
			_ = os.Remove(path)
			return err
		}
		tmp.Close()
		defer os.Remove(path)
		if _, err := m.runVirsh("define", path); err != nil {
			return fmt.Errorf("redefine after channel cleanup: %w", err)
		}
		xml = cleaned
		count = strings.Count(xml, marker)
	}

	if count == 0 {
		out, err := host.RunCommand("virt-xml", name, "--add-device", "--channel",
			"unix,target_type=virtio,name=org.qemu.guest_agent.0")
		if err != nil {
			return fmt.Errorf("add guest agent: %v", summarizeVirtXML(out, err))
		}
	}
	return nil
}

// removeDuplicateGuestAgentChannels keeps the first <channel>…guest_agent…</channel> block.
func removeDuplicateGuestAgentChannels(xml string) string {
	re := regexp.MustCompile(`(?s)<channel[^>]*>.*?org\.qemu\.guest_agent\.0.*?</channel>\s*`)
	matches := re.FindAllString(xml, -1)
	if len(matches) <= 1 {
		return xml
	}
	// Remove all, then re-insert the first once at the first match position.
	first := matches[0]
	without := re.ReplaceAllString(xml, "")
	// Prefer putting it back near other channels / devices.
	if i := strings.Index(without, "</devices>"); i >= 0 {
		return without[:i] + first + without[i:]
	}
	return without + first
}

func hostCPUCount() int {
	// Prefer online CPU count from sysfs / nproc.
	if out, err := host.RunCommand("nproc"); err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(out)); err == nil && n > 0 {
			return n
		}
	}
	n := runtime.NumCPU()
	if n < 1 {
		return 1
	}
	return n
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func summarizeVirtXML(out string, err error) string {
	s := strings.TrimSpace(out)
	if s == "" && err != nil {
		return err.Error()
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	if err != nil && s != "" {
		return fmt.Sprintf("%v (%s)", err, s)
	}
	if err != nil {
		return err.Error()
	}
	return s
}
