package debian

import (
	"testing"

	"github.com/alfaos/alfaos/internal/config"
)

func TestResolveReleaseDiscoversCurrentISO(t *testing.T) {
	cfg := config.Default()
	inst := New(cfg)

	if err := inst.ResolveRelease(); err != nil {
		t.Fatalf("ResolveRelease: %v", err)
	}

	if cfg.Debian.ISOFilename == "" {
		t.Fatal("expected ISOFilename to be set")
	}
	if cfg.Debian.Version == "" {
		t.Fatal("expected Version to be set")
	}
	if cfg.Debian.ISOURL == "" {
		t.Fatal("expected ISOURL to be set")
	}
	t.Logf("resolved %s -> %s", cfg.Debian.Version, cfg.Debian.ISOURL)
}

func TestResolveReleaseFallsBackFromStaleURL(t *testing.T) {
	cfg := config.Default()
	cfg.Debian.ISOURL = "https://cdimage.debian.org/debian-cd/current/amd64/iso-cd/debian-12.9.0-amd64-netinst.iso"
	inst := New(cfg)

	if err := inst.ResolveRelease(); err != nil {
		t.Fatalf("ResolveRelease: %v", err)
	}
	if cfg.Debian.ISOFilename == "debian-12.9.0-amd64-netinst.iso" {
		t.Fatal("expected fallback from stale 12.9.0 URL")
	}
	t.Logf("fallback resolved to %s", cfg.Debian.ISOFilename)
}
