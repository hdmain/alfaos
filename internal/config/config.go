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
		Port   int `yaml:"port"`
		Width  int `yaml:"width"`
		Height int `yaml:"height"`
	} `yaml:"rdp"`

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
	c.ALFAOS.Theme = "Arc"
	c.ALFAOS.Icons = "Papirus-Dark"
	c.ALFAOS.Wallpaper = "alfa2.jpeg"
	c.ALFAOS.Terminal = "tilix"
	c.ALFAOS.Browser = true
	c.ALFAOS.Plank = true

	c.RDP.Port = 3389
	c.RDP.Width = 1920
	c.RDP.Height = 1080

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

func (c *Config) EnsureDirs() error {
	dirs := []string{c.Paths.ISOCache, c.Paths.PreseedDir, c.Paths.StateDir}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			return fmt.Errorf("create dir %s: %w", d, err)
		}
	}
	return nil
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
