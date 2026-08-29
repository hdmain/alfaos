package debian

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"

	"github.com/alfaos/alfaos/internal/config"
	"github.com/alfaos/alfaos/internal/host"
	"github.com/alfaos/alfaos/internal/logging"
)

type Installer struct {
	cfg *config.Config
}

func New(cfg *config.Config) *Installer {
	return &Installer{cfg: cfg}
}

func (d *Installer) ResolveRelease() error {
	base := strings.TrimRight(d.cfg.Debian.ISOBaseURL, "/")
	if base == "" {
		base = fmt.Sprintf("https://cdimage.debian.org/debian-cd/current/%s/iso-cd", d.cfg.Debian.Arch)
		d.cfg.Debian.ISOBaseURL = base
	}
	hashURL := d.cfg.ISOHashURLResolved()

	// Honor an explicit ISO URL if it is reachable.
	if d.cfg.Debian.ISOURL != "" {
		if ok, _ := urlReachable(d.cfg.Debian.ISOURL); ok {
			d.cfg.Debian.ISOFilename = filepath.Base(d.cfg.Debian.ISOURL)
			if d.cfg.Debian.Version == "" {
				d.cfg.Debian.Version = versionFromFilename(d.cfg.Debian.ISOFilename)
			}
			logging.Info("Using configured ISO: %s", d.cfg.Debian.ISOFilename)
			return nil
		}
		logging.Warn("Configured ISO URL unavailable, discovering current release...")
	}

	var filename, version string
	var err error

	if d.cfg.Debian.Version != "" && d.cfg.Debian.Version != "current" {
		filename, version, err = d.findISOInSums(hashURL, d.cfg.Debian.Version)
		if err != nil {
			logging.Warn("Requested version %s not found, using latest: %v", d.cfg.Debian.Version, err)
			filename, version, err = d.discoverNetinstISO(hashURL)
		}
	} else {
		filename, version, err = d.discoverNetinstISO(hashURL)
	}
	if err != nil {
		return err
	}

	d.cfg.Debian.ISOFilename = filename
	d.cfg.Debian.Version = version
	d.cfg.Debian.ISOURL = base + "/" + filename
	d.cfg.Debian.ISOHashURL = hashURL

	logging.Info("Resolved Debian release: %s (%s)", version, filename)
	return nil
}

func (d *Installer) discoverNetinstISO(hashURL string) (filename, version string, err error) {
	resp, err := http.Get(hashURL)
	if err != nil {
		return "", "", fmt.Errorf("fetch SHA256SUMS: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("SHA256SUMS HTTP %d", resp.StatusCode)
	}

	arch := regexp.QuoteMeta(d.cfg.Debian.Arch)
	pattern := regexp.MustCompile(`^debian-(\d+\.\d+\.\d+)-` + arch + `-netinst\.iso$`)

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		parts := strings.Fields(scanner.Text())
		if len(parts) < 2 {
			continue
		}
		name := parts[1]
		if pattern.MatchString(name) {
			m := pattern.FindStringSubmatch(name)
			return name, m[1], nil
		}
	}
	return "", "", fmt.Errorf("no netinst ISO found in SHA256SUMS at %s", hashURL)
}

func (d *Installer) findISOInSums(hashURL, version string) (filename, ver string, err error) {
	want := fmt.Sprintf("debian-%s-%s-netinst.iso", version, d.cfg.Debian.Arch)
	resp, err := http.Get(hashURL)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		parts := strings.Fields(scanner.Text())
		if len(parts) >= 2 && parts[1] == want {
			return want, version, nil
		}
	}
	return "", "", fmt.Errorf("version %s not found", version)
}

func versionFromFilename(name string) string {
	m := regexp.MustCompile(`debian-(\d+\.\d+\.\d+)-`).FindStringSubmatch(name)
	if len(m) >= 2 {
		return m[1]
	}
	return ""
}

func urlReachable(url string) (bool, error) {
	req, err := http.NewRequest(http.MethodHead, url, nil)
	if err != nil {
		return false, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK, nil
}

func (d *Installer) DownloadISO() error {
	if err := d.ResolveRelease(); err != nil {
		return fmt.Errorf("resolve Debian release: %w", err)
	}

	isoPath := d.cfg.ISOPath()
	if _, err := os.Stat(isoPath); err == nil {
		logging.Info("ISO already cached at %s", isoPath)
		return d.VerifyISO()
	}

	logging.Info("Downloading Debian ISO from %s", d.cfg.Debian.ISOURL)
	if err := downloadFile(d.cfg.Debian.ISOURL, isoPath); err != nil {
		return err
	}
	logging.Success("ISO downloaded to %s", isoPath)
	return d.VerifyISO()
}

func (d *Installer) VerifyISO() error {
	isoPath := d.cfg.ISOPath()
	logging.Info("Verifying ISO integrity...")

	expected, err := d.fetchExpectedHash(filepath.Base(isoPath))
	if err != nil {
		logging.Warn("Could not fetch SHA256SUMS: %v — skipping verification", err)
		return nil
	}

	actual, err := fileSHA256(isoPath)
	if err != nil {
		return err
	}

	if actual != expected {
		_ = os.Remove(isoPath)
		return fmt.Errorf("ISO checksum mismatch: expected %s, got %s", expected, actual)
	}

	logging.Success("ISO integrity verified (SHA256: %s...)", actual[:16])
	return nil
}

func (d *Installer) fetchExpectedHash(filename string) (string, error) {
	resp, err := http.Get(d.cfg.ISOHashURLResolved())
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.Fields(line)
		if len(parts) >= 2 && parts[1] == filename {
			return parts[0], nil
		}
	}
	return "", fmt.Errorf("hash for %s not found in SHA256SUMS", filename)
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func downloadFile(url, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return err
	}

	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d for %s", resp.StatusCode, url)
	}

	tmp := dest + ".partial"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}

	written, err := io.Copy(f, resp.Body)
	f.Close()
	if err != nil {
		os.Remove(tmp)
		return err
	}

	logging.Info("Downloaded %d bytes", written)
	return os.Rename(tmp, dest)
}

const preseedTemplate = `# ALFAOS Debian Preseed — fully automated installation
d-i debconf/priority select critical

d-i keyboard-configuration/xkb-keymap select us
d-i keyboard-configuration/layoutcode string us

d-i netcfg/choose_interface select auto
d-i netcfg/get_hostname string {{.Hostname}}
d-i netcfg/get_domain string local

d-i mirror/country string manual
d-i mirror/http/hostname string deb.debian.org
d-i mirror/http/directory string /debian
d-i mirror/http/proxy string

d-i clock-setup/utc boolean true
d-i time/zone string UTC
d-i clock-setup/ntp boolean true

d-i partman-auto/method string regular
d-i partman-auto/choose_recipe select atomic
d-i partman-auto/disk string /dev/vda
d-i partman/confirm_write_new_label boolean true
d-i partman/choose_partition select finish
d-i partman/confirm boolean true
d-i partman/confirm_nooverwrite boolean true

d-i passwd/root-login boolean false
d-i passwd/user-fullname string ALFAOS User
d-i passwd/username string {{.Username}}
d-i passwd/user-password password {{.Password}}
d-i passwd/user-password-again password {{.Password}}
d-i user-setup/allow-password-weak boolean true
d-i user-setup/encrypt-user-password boolean false

tasksel tasksel/first multiselect standard, ssh-server
d-i pkgsel/include string openssh-server sudo curl wget qemu-guest-agent
d-i pkgsel/upgrade select full-upgrade
d-i pkgsel/update-policy select none

d-i grub-installer/only_debian boolean true
d-i grub-installer/with_other_os boolean true
d-i grub-installer/bootdev string /dev/vda

d-i finish-install/reboot_in_progress note
d-i preseed/late_command string \
    in-target apt-get install -y openssh-server; \
    in-target systemctl enable ssh; \
    in-target systemctl enable qemu-guest-agent; \
    in-target sed -i 's/#PermitRootLogin.*/PermitRootLogin no/' /etc/ssh/sshd_config; \
    in-target sed -i 's/#PasswordAuthentication.*/PasswordAuthentication yes/' /etc/ssh/sshd_config; \
    in-target sed -i 's/PasswordAuthentication no/PasswordAuthentication yes/' /etc/ssh/sshd_config; \
    in-target sed -i 's/#KbdInteractiveAuthentication.*/KbdInteractiveAuthentication yes/' /etc/ssh/sshd_config; \
    in-target sh -c 'printf "auto lo\niface lo inet loopback\n\nauto eth0\niface eth0 inet dhcp\n" > /etc/network/interfaces'; \
    in-target sh -c 'echo "alfaos ALL=(ALL) NOPASSWD:ALL" > /etc/sudoers.d/alfaos && chmod 440 /etc/sudoers.d/alfaos'; \
    in-target systemctl restart ssh
`

func (d *Installer) GeneratePreseed() (string, error) {
	preseedPath := filepath.Join(d.cfg.Paths.PreseedDir, "preseed.cfg")
	tmpl, err := template.New("preseed").Parse(preseedTemplate)
	if err != nil {
		return "", err
	}

	f, err := os.Create(preseedPath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	data := struct {
		Hostname string
		Username string
		Password string
	}{
		d.cfg.ALFAOS.Hostname,
		d.cfg.ALFAOS.Username,
		d.cfg.ALFAOS.Password,
	}

	if err := tmpl.Execute(f, data); err != nil {
		return "", err
	}

	logging.Success("Preseed file generated at %s", preseedPath)
	return preseedPath, nil
}

func (d *Installer) BuildPreseedISO(preseedPath string) (string, error) {
	isoPath := filepath.Join(d.cfg.Paths.PreseedDir, "preseed.iso")
	labelDir := filepath.Join(d.cfg.Paths.PreseedDir, "iso-root")
	if err := os.MkdirAll(labelDir, 0755); err != nil {
		return "", err
	}

	dest := filepath.Join(labelDir, "preseed.cfg")
	data, err := os.ReadFile(preseedPath)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(dest, data, 0644); err != nil {
		return "", err
	}

	os.Remove(isoPath)
	_, err = host.RunCommand("genisoimage", "-o", isoPath,
		"-V", "PRESEED", "-r", "-J", labelDir)
	if err != nil {
		// Try mkisofs on some systems
		_, err = host.RunCommand("mkisofs", "-o", isoPath,
			"-V", "PRESEED", "-r", "-J", labelDir)
		if err != nil {
			return "", fmt.Errorf("create preseed ISO: %w", err)
		}
	}

	logging.Success("Preseed ISO created at %s", isoPath)
	return isoPath, nil
}
