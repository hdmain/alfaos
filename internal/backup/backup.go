package backup

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/alfaos/alfaos/internal/config"
	"github.com/alfaos/alfaos/internal/host"
	"github.com/alfaos/alfaos/internal/logging"
	"github.com/alfaos/alfaos/internal/networking"
	"github.com/alfaos/alfaos/internal/virtualization"
)

const (
	archiveConfig = "config.yaml"
	archiveDomain = "domain.xml"
	archiveDisk   = "disk.qcow2"
	archiveMeta   = "meta.json"
	archiveBootVmlinuz = "boot/vmlinuz"
	archiveBootInitrd  = "boot/initrd.img"
)

type Meta struct {
	CreatedAt string `json:"created_at"`
	VMName    string `json:"vm_name"`
	DiskGB    int    `json:"disk_gb"`
	Version   string `json:"version"`
}

// Export writes a .tar.gz with config + domain XML + qcow2 disk (+ optional kernel boot files).
func Export(cfg *config.Config, dest string) error {
	if dest == "" {
		return fmt.Errorf("destination path required")
	}
	if !strings.HasSuffix(strings.ToLower(dest), ".tar.gz") && !strings.HasSuffix(strings.ToLower(dest), ".tgz") {
		dest += ".tar.gz"
	}

	vm := virtualization.New(cfg)
	if !vm.DomainExists() {
		return fmt.Errorf("VM %q does not exist — run: sudo alfaos install", cfg.VM.Name)
	}

	if vm.DomainRunning() {
		logging.Info("Shutting down VM for consistent disk export...")
		if err := vm.ShutdownVM(3 * time.Minute); err != nil {
			logging.Warn("Graceful shutdown failed (%v) — forcing power off", err)
			_ = vm.StopVM()
		}
	}

	diskPath := DiskPath(cfg)
	if _, err := os.Stat(diskPath); err != nil {
		return fmt.Errorf("VM disk not found at %s: %w", diskPath, err)
	}

	xml, err := host.RunCommand("virsh", "dumpxml", cfg.VM.Name)
	if err != nil {
		return fmt.Errorf("dumpxml: %w", err)
	}

	tmpCfg := filepath.Join(os.TempDir(), "alfaos-export-config.yaml")
	if err := config.Save(cfg, tmpCfg); err != nil {
		return err
	}
	defer os.Remove(tmpCfg)

	meta := Meta{
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		VMName:    cfg.VM.Name,
		DiskGB:    cfg.VM.Disk,
		Version:   "1",
	}
	metaBytes, _ := json.MarshalIndent(meta, "", "  ")
	tmpMeta := filepath.Join(os.TempDir(), "alfaos-export-meta.json")
	if err := os.WriteFile(tmpMeta, metaBytes, 0644); err != nil {
		return err
	}
	defer os.Remove(tmpMeta)

	tmpXML := filepath.Join(os.TempDir(), "alfaos-export-domain.xml")
	if err := os.WriteFile(tmpXML, []byte(xml), 0644); err != nil {
		return err
	}
	defer os.Remove(tmpXML)

	logging.Info("Creating archive %s (config + disk — may take a while)...", dest)
	if dir := filepath.Dir(dest); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}

	f, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("create %s: %w", dest, err)
	}
	defer f.Close()

	gw := gzip.NewWriter(f)
	defer gw.Close()
	tw := tar.NewWriter(gw)
	defer tw.Close()

	files := []struct {
		name string
		path string
	}{
		{archiveConfig, tmpCfg},
		{archiveDomain, tmpXML},
		{archiveMeta, tmpMeta},
		{archiveDisk, diskPath},
	}

	bootVmlinuz := filepath.Join(cfg.Paths.StateDir, "boot", "vmlinuz")
	bootInitrd := filepath.Join(cfg.Paths.StateDir, "boot", "initrd.img")
	if _, err := os.Stat(bootVmlinuz); err == nil {
		files = append(files, struct{ name, path string }{archiveBootVmlinuz, bootVmlinuz})
	}
	if _, err := os.Stat(bootInitrd); err == nil {
		files = append(files, struct{ name, path string }{archiveBootInitrd, bootInitrd})
	}

	for _, item := range files {
		logging.Info("Adding %s...", item.name)
		if err := addFileToTar(tw, item.name, item.path); err != nil {
			return err
		}
	}

	logging.Success("Exported ALFAOS backup to %s", dest)
	return nil
}

// Import restores config + VM disk from a .tar.gz created by Export.
func Import(cfgPath, archive string, force bool) error {
	if archive == "" {
		return fmt.Errorf("archive path required")
	}
	if _, err := os.Stat(archive); err != nil {
		return fmt.Errorf("archive: %w", err)
	}

	tmpDir, err := os.MkdirTemp("", "alfaos-import-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	logging.Info("Extracting %s...", archive)
	if err := extractTarGz(archive, tmpDir); err != nil {
		return err
	}

	cfgFile := filepath.Join(tmpDir, archiveConfig)
	diskFile := filepath.Join(tmpDir, archiveDisk)
	domainFile := filepath.Join(tmpDir, archiveDomain)
	for _, p := range []string{cfgFile, diskFile, domainFile} {
		if _, err := os.Stat(p); err != nil {
			return fmt.Errorf("invalid backup — missing %s", filepath.Base(p))
		}
	}

	cfg, err := config.Load(cfgFile)
	if err != nil {
		return fmt.Errorf("load backup config: %w", err)
	}
	if err := cfg.EnsureDirs(); err != nil {
		return err
	}

	vm := virtualization.New(cfg)
	if vm.DomainExists() {
		if !force {
			return fmt.Errorf("VM %q already exists — re-run with: sudo alfaos import %s --force", cfg.VM.Name, archive)
		}
		logging.Warn("Force: removing existing VM %s", cfg.VM.Name)
		if err := vm.UndefineVM(); err != nil {
			return fmt.Errorf("undefine existing VM: %w", err)
		}
	}

	destDisk := DiskPath(cfg)
	logging.Info("Restoring disk to %s...", destDisk)
	if err := os.MkdirAll(filepath.Dir(destDisk), 0755); err != nil {
		return err
	}
	_ = os.Remove(destDisk)
	if err := copyFile(diskFile, destDisk); err != nil {
		return fmt.Errorf("restore disk: %w", err)
	}
	_ = os.Chmod(destDisk, 0600)

	bootDir := filepath.Join(cfg.Paths.StateDir, "boot")
	_ = os.MkdirAll(bootDir, 0755)
	if src := filepath.Join(tmpDir, archiveBootVmlinuz); fileExists(src) {
		_ = copyFile(src, filepath.Join(bootDir, "vmlinuz"))
	}
	if src := filepath.Join(tmpDir, archiveBootInitrd); fileExists(src) {
		_ = copyFile(src, filepath.Join(bootDir, "initrd.img"))
	}

	xmlBytes, err := os.ReadFile(domainFile)
	if err != nil {
		return err
	}
	xml := rewriteDomainPaths(string(xmlBytes), cfg, destDisk)

	tmpXML := filepath.Join(tmpDir, "import-domain.xml")
	if err := os.WriteFile(tmpXML, []byte(xml), 0644); err != nil {
		return err
	}
	if _, err := host.RunCommand("virsh", "define", tmpXML); err != nil {
		return fmt.Errorf("virsh define: %w", err)
	}

	if cfgPath == "" {
		cfgPath = config.ResolvePath("")
	}
	if err := config.Save(cfg, cfgPath); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	logging.Success("Config restored to %s", cfgPath)

	if cfg.Onioning {
		if err := networking.ConfigureOnioning(true, cfg.Paths.StateDir, cfg.VM.Network); err != nil {
			logging.Warn("Onioning restore: %v", err)
		}
	}

	if cfg.RDP.Expose {
		if err := networking.ExposeRDP(cfg, ""); err != nil {
			logging.Warn("RDP proxy: %v", err)
		}
	}

	logging.Success("Imported VM %q — start with: sudo alfaos start", cfg.VM.Name)
	return nil
}

// DiskPath returns the default libvirt qcow2 path for the configured VM.
func DiskPath(cfg *config.Config) string {
	return fmt.Sprintf("/var/lib/libvirt/images/%s.qcow2", cfg.VM.Name)
}

func rewriteDomainPaths(xml string, cfg *config.Config, diskPath string) string {
	// Point disk source at local path.
	reDisk := regexp.MustCompile(`(<source\s+file=')[^']+('\s*/>)`)
	xml = reDisk.ReplaceAllString(xml, "${1}"+diskPath+"${2}")

	bootDir := filepath.Join(cfg.Paths.StateDir, "boot")
	reKernel := regexp.MustCompile(`(<kernel>)[^<]*(</kernel>)`)
	reInitrd := regexp.MustCompile(`(<initrd>)[^<]*(</initrd>)`)
	xml = reKernel.ReplaceAllString(xml, "${1}"+filepath.Join(bootDir, "vmlinuz")+"${2}")
	xml = reInitrd.ReplaceAllString(xml, "${1}"+filepath.Join(bootDir, "initrd.img")+"${2}")
	return xml
}

func addFileToTar(tw *tar.Writer, name, path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	hdr, err := tar.FileInfoHeader(fi, "")
	if err != nil {
		return err
	}
	hdr.Name = name
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(tw, f)
	return err
}

func extractTarGz(archive, destDir string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip: %w (need a .tar.gz from alfaos export)", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		// Prevent path traversal
		clean := filepath.Clean(hdr.Name)
		if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			return fmt.Errorf("unsafe path in archive: %s", hdr.Name)
		}
		target := filepath.Join(destDir, clean)
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			return err
		}
		out.Close()
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
