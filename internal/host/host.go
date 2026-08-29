package host

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/alfaos/alfaos/internal/logging"
)

type Distro struct {
	ID             string
	IDLike         string
	VersionID      string
	PrettyName     string
	PackageManager string // apt, dnf, pacman, zypper
}

type Requirements struct {
	Distro            Distro
	HasVirtualization bool
	IsRoot            bool
	CPUArch           string
	MemoryMB          int64
	DiskFreeGB        int64
}

func CheckRequirements() (*Requirements, error) {
	req := &Requirements{
		IsRoot:  os.Geteuid() == 0,
		CPUArch: runtime.GOARCH,
	}

	distro, err := detectDistro()
	if err != nil {
		return nil, err
	}
	req.Distro = distro

	req.HasVirtualization = checkVirtualization()
	req.MemoryMB = readMemoryMB()
	req.DiskFreeGB = readDiskFreeGB("/")

	return req, validateRequirements(req)
}

func detectDistro() (Distro, error) {
	f, err := os.Open("/etc/os-release")
	if err != nil {
		return Distro{}, fmt.Errorf("cannot read /etc/os-release: %w", err)
	}
	defer f.Close()

	d := Distro{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key, val := parts[0], strings.Trim(parts[1], `"`)
		switch key {
		case "ID":
			d.ID = val
		case "ID_LIKE":
			d.IDLike = val
		case "VERSION_ID":
			d.VersionID = val
		case "PRETTY_NAME":
			d.PrettyName = val
		}
	}

	switch {
	case d.ID == "debian" || d.ID == "ubuntu" || strings.Contains(d.IDLike, "debian"):
		d.PackageManager = "apt"
	case d.ID == "fedora" || strings.Contains(d.IDLike, "fedora") || d.ID == "rhel" || d.ID == "centos":
		d.PackageManager = "dnf"
	case d.ID == "arch" || d.ID == "manjaro":
		d.PackageManager = "pacman"
	case d.ID == "opensuse-leap" || d.ID == "suse":
		d.PackageManager = "zypper"
	default:
		d.PackageManager = "apt"
	}

	if d.PrettyName == "" {
		d.PrettyName = d.ID
	}
	return d, nil
}

func checkVirtualization() bool {
	data, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return false
	}
	content := string(data)
	hasVMX := strings.Contains(content, "vmx") || strings.Contains(content, "svm")
	if !hasVMX {
		return false
	}

	if _, err := os.Stat("/dev/kvm"); err != nil {
		// KVM module may not be loaded yet
		out, _ := exec.Command("lsmod").Output()
		if !strings.Contains(string(out), "kvm") {
			logging.Warn("KVM module not loaded; will attempt to load during install")
		}
	}
	return true
}

func readMemoryMB() int64 {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			var kb int64
			fmt.Sscanf(line, "MemTotal: %d kB", &kb)
			return kb / 1024
		}
	}
	return 0
}

func readDiskFreeGB(path string) int64 {
	out, err := exec.Command("df", "-BG", "--output=avail", path).Output()
	if err != nil {
		return 0
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return 0
	}
	var gb int64
	fmt.Sscanf(strings.TrimSpace(lines[1]), "%dG", &gb)
	return gb
}

func validateRequirements(req *Requirements) error {
	var errs []string

	if !req.IsRoot {
		errs = append(errs, "must run as root (use sudo)")
	}

	if req.CPUArch != "amd64" && req.CPUArch != "arm64" {
		errs = append(errs, fmt.Sprintf("unsupported architecture: %s (need amd64 or arm64)", req.CPUArch))
	}

	if !req.HasVirtualization {
		errs = append(errs, "hardware virtualization (Intel VT-x / AMD-V) not available")
	}

	if req.MemoryMB > 0 && req.MemoryMB < 3072 {
		errs = append(errs, fmt.Sprintf("insufficient RAM: %d MB (need at least 3072 MB)", req.MemoryMB))
	}

	if req.DiskFreeGB > 0 && req.DiskFreeGB < 12 {
		errs = append(errs, fmt.Sprintf("insufficient disk space: %d GB free (need at least 12 GB)", req.DiskFreeGB))
	}

	if len(errs) > 0 {
		return fmt.Errorf("host requirements not met:\n  - %s", strings.Join(errs, "\n  - "))
	}

	logging.Info("Host: %s (%s)", req.Distro.PrettyName, req.Distro.PackageManager)
	logging.Info("Virtualization: available")
	logging.Info("RAM: %d MB, Disk free: %d GB", req.MemoryMB, req.DiskFreeGB)
	return nil
}

func RunCommand(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %v: %w\n%s", name, args, err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

func CommandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func InstallHostPackages(pm string) error {
	packages := hostPackagesForPM(pm)
	if len(packages) == 0 {
		return fmt.Errorf("unsupported package manager: %s", pm)
	}

	logging.Info("Installing host packages: %s", strings.Join(packages, ", "))

	switch pm {
	case "apt":
		if _, err := RunCommand("apt-get", "update", "-qq"); err != nil {
			logging.Warn("apt-get update failed (broken third-party repos?) — continuing: %v", err)
		}
		args := append([]string{"install", "-y", "-qq"}, packages...)
		_, err := RunCommand("apt-get", args...)
		return err
	case "dnf":
		args := append([]string{"install", "-y", "-q"}, packages...)
		_, err := RunCommand("dnf", args...)
		return err
	case "pacman":
		args := append([]string{"-S", "--noconfirm", "--needed"}, packages...)
		_, err := RunCommand("pacman", args...)
		return err
	case "zypper":
		args := append([]string{"install", "-y", "-q"}, packages...)
		_, err := RunCommand("zypper", args...)
		return err
	default:
		return fmt.Errorf("unsupported package manager: %s", pm)
	}
}

func hostPackagesForPM(pm string) []string {
	common := []string{
		"qemu-kvm", "libvirt-daemon-system", "libvirt-clients",
		"virtinst", "bridge-utils", "genisoimage", "wget", "curl",
		"openssh-client", "sshpass",
	}
	switch pm {
	case "apt":
		return append(common,
			"qemu-system-x86", "libvirt-daemon", "libvirt-daemon-driver-qemu",
			"libguestfs-tools", "dnsmasq-base", "iproute2", "netcat-openbsd",
			"rdesktop", "freerdp2-x11",
		)
	case "dnf":
		return []string{
			"qemu-kvm", "qemu-img", "libvirt", "libvirt-daemon-kvm",
			"virt-install", "bridge-utils", "genisoimage", "wget", "curl",
			"openssh-clients", "sshpass", "libguestfs-tools", "dnsmasq",
			"iproute", "nmap-ncat",
		}
	case "pacman":
		return []string{
			"qemu-full", "libvirt", "virt-install", "bridge-utils", "cdrtools",
			"wget", "curl", "openssh", "sshpass", "libguestfs", "dnsmasq",
			"iproute2", "gnu-netcat",
		}
	default:
		return common
	}
}

func EnableServices() error {
	services := []string{"libvirtd", "virtlogd"}
	for _, svc := range services {
		if _, err := RunCommand("systemctl", "enable", "--now", svc); err != nil {
			logging.Warn("Could not enable %s: %v", svc, err)
		}
	}
	// Add current user to libvirt group if not root session user
	user := os.Getenv("SUDO_USER")
	if user != "" {
		if _, err := RunCommand("usermod", "-aG", "libvirt", user); err != nil {
			logging.Warn("Could not add %s to libvirt group: %v", user, err)
		}
		if _, err := RunCommand("usermod", "-aG", "kvm", user); err != nil {
			logging.Warn("Could not add %s to kvm group: %v", user, err)
		}
	}
	return nil
}

func LoadKVMModule() error {
	out, _ := exec.Command("lsmod").Output()
	if strings.Contains(string(out), "kvm") {
		return nil
	}
	_, err := RunCommand("modprobe", "kvm")
	if err != nil {
		return err
	}
	// Try Intel then AMD
	if _, err := RunCommand("modprobe", "kvm_intel"); err != nil {
		_, _ = RunCommand("modprobe", "kvm_amd")
	}
	return nil
}
