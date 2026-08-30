package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/alfaos/alfaos/internal/logging"
	"gopkg.in/yaml.v3"
)

type Config struct {
	VM struct {
		Name     string `yaml:"name"`
		CPU      int    `yaml:"cpu"`
		RAM      int    `yaml:"ram_mb"`
		Disk     int    `yaml:"disk_gb"`
		Network  string `yaml:"network"`
		Graphics string `yaml:"graphics"`
	} `yaml:"vm"`

	Debian struct {
		Version     string `yaml:"version"`      // empty or "current" = auto-discover latest
		Arch        string `yaml:"arch"`
		ISOBaseURL  string `yaml:"iso_base_url"` // directory containing ISO and SHA256SUMS
		ISOURL      string `yaml:"iso_url"`      // optional override; auto-resolved if empty or stale
		ISOFilename string `yaml:"-"`            // resolved at runtime
		ISOHashURL  string `yaml:"iso_hash_url"`
		Mirror      string `yaml:"mirror"`
	} `yaml:"debian"`

	ALFAOS struct {
		Username  string `yaml:"username"`
		Password  string `yaml:"password"`
		Hostname  string `yaml:"hostname"`
		Theme     string `yaml:"theme"`
		Icons     string `yaml:"icons"`
		Wallpaper string `yaml:"wallpaper"`
		Terminal  string `yaml:"terminal"`
		Browser   bool   `yaml:"browser"`
		Plank     bool   `yaml:"plank"`
	} `yaml:"alfaos"`

	RDP struct {
		Port     int    `yaml:"port"`
		Width    int    `yaml:"width"`
		Height   int    `yaml:"height"`
		Expose   bool   `yaml:"expose"`    // forward host port to VM (for VPS remote access)
		BindHost string `yaml:"bind_host"` // host listen address, default 0.0.0.0
	} `yaml:"rdp"`

	// Power controls idle shutdown and wake-on-incoming-RDP (host listens even when VM is off).
	Power struct {
		IdleShutdownMinutes int  `yaml:"idle_shutdown_minutes"` // 0 = disabled; shutdown VM after this many idle minutes
		WakeOnRDP           bool `yaml:"wake_on_rdp"`           // start VM when a client connects to the host RDP port
	} `yaml:"power"`

	// DNS upstream for the VM (default: AdGuard — blocks ads/trackers, privacy-focused).
	DNS struct {
		Servers []string `yaml:"servers"` // empty = use AdGuard default blocking DNS
	} `yaml:"dns"`

	// Onioning routes all VM outbound TCP/DNS through Tor on the host.
	// RDP stays direct (host proxy → guest). Persisted by `alfaos onioning on|off`.
	Onioning bool `yaml:"onioning"`

	Paths struct {
		ISOCache   string `yaml:"iso_cache"`
		PreseedDir string `yaml:"preseed_dir"`
		StateDir   string `yaml:"state_dir"`
	} `yaml:"paths"`
}

func Default() *Config {
	c := &Config{}
	c.VM.Name = "alfaos"
	c.VM.CPU = 2
	c.VM.RAM = 4096
	c.VM.Disk = 32
	c.VM.Network = "default"
	c.VM.Graphics = "none"

	c.Debian.Version = "" // auto-discover latest from cdimage.debian.org
	c.Debian.Arch = "amd64"
	c.Debian.ISOBaseURL = "https://cdimage.debian.org/debian-cd/current/amd64/iso-cd"
	c.Debian.ISOURL = ""
	c.Debian.ISOHashURL = "https://cdimage.debian.org/debian-cd/current/amd64/iso-cd/SHA256SUMS"
	c.Debian.Mirror = "http://deb.debian.org/debian"

	c.ALFAOS.Username = "alfaos"
	c.ALFAOS.Password = "alfaos"
	c.ALFAOS.Hostname = "alfaos"
	c.ALFAOS.Theme = "Alfa"
	c.ALFAOS.Icons = "Papirus-Dark"
	c.ALFAOS.Wallpaper = "alfa2.jpeg"
	c.ALFAOS.Terminal = "tilix"
	c.ALFAOS.Browser = true
	c.ALFAOS.Plank = true

	c.RDP.Port = 3389
	c.RDP.Width = 1920
	c.RDP.Height = 1080
	c.RDP.Expose = true
	c.RDP.BindHost = "0.0.0.0"

	c.Power.IdleShutdownMinutes = 15
	c.Power.WakeOnRDP = true

	// AdGuard DNS + Quad9 backup (blocks malware/DNSSEC)
	c.DNS.Servers = []string{"94.140.14.14", "94.140.15.15", "9.9.9.11"}

	c.Onioning = false

	c.Paths.ISOCache = "/var/lib/alfaos/iso"
	c.Paths.PreseedDir = "/var/lib/alfaos/preseed"
	c.Paths.StateDir = "/var/lib/alfaos/state"
	return c
}

func Load(path string) (*Config, error) {
	cfg := Default()
	if path == "" {
		return cfg, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return cfg, nil
}

// ResolvePath returns the config file path from an explicit flag or common locations.
func ResolvePath(flag string) string {
	if flag != "" {
		return flag
	}
	for _, c := range []string{"/etc/alfaos/config.yaml", "configs/default.yaml"} {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return "/etc/alfaos/config.yaml"
}

// Save writes the config to path with restrictive permissions (contains password).
func Save(cfg *Config, path string) error {
	if path == "" {
		path = ResolvePath("")
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0640)
}

func (c *Config) ISOPath() string {
	name := c.Debian.ISOFilename
	if name == "" {
		if c.Debian.Version != "" {
			name = fmt.Sprintf("debian-%s-%s-netinst.iso", c.Debian.Version, c.Debian.Arch)
		} else {
			name = fmt.Sprintf("debian-current-%s-netinst.iso", c.Debian.Arch)
		}
	}
	return filepath.Join(c.Paths.ISOCache, name)
}

func (c *Config) ISOHashURLResolved() string {
	if c.Debian.ISOHashURL != "" {
		return c.Debian.ISOHashURL
	}
	return strings.TrimRight(c.Debian.ISOBaseURL, "/") + "/SHA256SUMS"
}

func (c *Config) OSVariant() string {
	major := debianMajorVersion(c.Debian.Version)
	if major >= 13 {
		return "debian13"
	}
	if major >= 12 {
		return "debian12"
	}
	if major >= 11 {
		return "debian11"
	}
	return "debian12"
}

func debianMajorVersion(version string) int {
	var major int
	fmt.Sscanf(version, "%d", &major)
	return major
}

func (c *Config) RDPResolution() string {
	w, h := c.RDP.Width, c.RDP.Height
	if w <= 0 {
		w = 1920
	}
	if h <= 0 {
		h = 1080
	}
	return fmt.Sprintf("%dx%d", w, h)
}

// DNSServers returns configured DNS servers or AdGuard defaults when unset.
func (c *Config) DNSServers() []string {
	if len(c.DNS.Servers) > 0 {
		return append([]string(nil), c.DNS.Servers...)
	}
	return []string{"94.140.14.14", "94.140.15.15", "9.9.9.11"}
}

func (c *Config) EnsureDirs() error {
	dirs := []string{c.Paths.ISOCache, c.Paths.PreseedDir, c.Paths.StateDir}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			return fmt.Errorf("create dir %s: %w", d, err)
		}
	}
	return nil
}

// WriteSystemConfig writes the active config to /etc/alfaos/config.yaml when missing or outdated keys needed.
func (c *Config) WriteSystemConfig() error {
	dir := "/etc/alfaos"
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	path := filepath.Join(dir, "config.yaml")
	if _, err := os.Stat(path); err == nil {
		// Keep admin customizations; only create if absent.
		return nil
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// AdaptToHost lowers VM resource requests when the host is tight on RAM or disk.
func (c *Config) AdaptToHost(hostRAMMB, hostDiskGB int64) {
	const (
		hostReserveMB   = 768  // leave RAM for the host OS and libvirt
		minHostRAMMB    = 3072 // minimum host RAM to attempt install
		minVMRAMMB      = 2048 // minimum VM RAM after tuning
		hostReserveGB   = 8    // leave disk for host OS, ISO cache, images
		minVMDiskGB     = 16
		absoluteMinDisk = 12
	)

	if hostRAMMB > 0 && hostRAMMB < minHostRAMMB {
		return
	}

	if hostRAMMB > 0 && c.VM.RAM > 0 {
		available := int(hostRAMMB - hostReserveMB)
		if available < minVMRAMMB {
			available = minVMRAMMB
		}
		available = (available / 256) * 256
		if c.VM.RAM > available {
			logging.Warn("Host RAM is %d MB — reducing VM RAM from %d MB to %d MB", hostRAMMB, c.VM.RAM, available)
			c.VM.RAM = available
		}
	}

	if hostDiskGB > 0 && c.VM.Disk > 0 {
		available := int(hostDiskGB - hostReserveGB)
		if available < minVMDiskGB {
			available = minVMDiskGB
		}
		if available < absoluteMinDisk {
			available = absoluteMinDisk
		}
		if c.VM.Disk > available {
			logging.Warn("Host disk free is %d GB — reducing VM disk from %d GB to %d GB", hostDiskGB, c.VM.Disk, available)
			c.VM.Disk = available
		}
	}
}
