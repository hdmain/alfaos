# ALFAOS Installer

Automated Linux Framework for Alpha OS — a Go-based CLI that installs and configures a complete Debian desktop environment inside a KVM virtual machine.

## One-line install

Downloads a pre-built binary from **GitHub Actions** (published to GitHub Pages on every push to `main`):

```bash
curl -fsSL https://raw.githubusercontent.com/hdmain/alfaos/main/scripts/install.sh | sudo bash
```

Full automated install (CLI + KVM VM + XFCE desktop + RDP):

```bash
curl -fsSL https://raw.githubusercontent.com/hdmain/alfaos/main/scripts/install.sh | sudo bash -s -- --full
```

Binaries: `https://hdmain.github.io/alfaos/alfaos-linux-amd64` (auto-built by CI — no local `dist/` commit needed).

**One-time repo setup:** GitHub → Settings → Pages → Source: **GitHub Actions**.

Then connect:

```bash
alfaos connect
```

## Quick Start (from source)

```bash
git clone https://github.com/hdmain/alfaos.git
cd alfaos
./scripts/build.sh
sudo alfaos install
```

## What It Does

1. Detects Linux distribution and verifies host requirements
2. Checks hardware virtualization (Intel VT-x / AMD-V)
3. Installs KVM, QEMU, libvirt, virt-install, and dependencies
4. Configures libvirt networking
5. Downloads Debian netinst ISO with SHA256 verification
6. Creates a KVM VM with automated preseed installation
7. Installs minimal XFCE, Whisker Menu, Tilix terminal, Arc/Dracula/Gruvbox theme, Papirus icons, Plank
8. Installs custom ALFAOS wallpapers
9. Configures xRDP for remote desktop access
10. Runs comprehensive verification with automatic repair
11. Reports RDP connection details

## Project Structure

```
alfaos/
├── cmd/alfaos/          # CLI entry point
├── internal/
│   ├── host/            # Host detection and package install
│   ├── virtualization/  # KVM/libvirt VM management
│   ├── debian/          # ISO download, preseed, verification
│   ├── networking/      # libvirt network configuration
│   ├── desktop/         # XFCE desktop setup
│   ├── rdp/             # xRDP configuration
│   ├── wallpapers/      # Embedded wallpaper assets
│   ├── verification/    # End-to-end verification tests
│   ├── logging/         # Structured logging and progress
│   ├── config/          # Configuration management
│   └── install/         # Installation orchestration
├── assets/              # Wallpaper files
├── configs/             # Default configuration
└── scripts/build.sh     # Build and install script
```

## Configuration

Edit `configs/default.yaml` or `/etc/alfaos/config.yaml`:

```yaml
vm:
  cpu: 2
  ram_mb: 4096
  disk_gb: 32

alfaos:
  theme: Arc    # Arc, Dracula, or Gruvbox
  terminal: tilix
  browser: true
  plank: true

power:
  idle_shutdown_minutes: 15   # shut down VM when no RDP sessions (0 = never)
  wake_on_rdp: true           # start VM when any RDP client connects to the host
```

### Power saving (wake-on-RDP)

When `rdp.expose` is enabled, the host service `alfaos-rdp-forward` always listens on the RDP port (default 3389), even if the VM is powered off.

1. You connect from another PC with any RDP client to the VPS IP.
2. The proxy starts the VM, waits for xRDP, then forwards the session.
3. After `idle_shutdown_minutes` with no RDP sessions, the VM is shut down and host RAM is freed.

First connect after idle can take **30–90 seconds** (VM boot). If your client times out, wait a minute and connect again — the VM may already be up.

```bash
sudo systemctl status alfaos-rdp-forward
sudo journalctl -u alfaos-rdp-forward -f
```

Apply config changes with:

```bash
sudo alfaos expose-rdp
```

## Requirements

- Linux host (Debian/Ubuntu/Fedora/Arch)
- Root access (`sudo`)
- Hardware virtualization enabled in BIOS
- At least 3 GB RAM and 12 GB free disk space (VM RAM/disk auto-tuned to fit your host)
- Internet connection for ISO download

## Connect via RDP

After installation completes (default resolution: 1920x1080):

```
alfaos connect
xfreerdp /v:<VM_IP> /u:alfaos /p:alfaos /size:1920x1080
rdesktop <VM_IP> -u alfaos -p alfaos -g 1920x1080
```

Resolution is configured in `configs/default.yaml` under `rdp.width` / `rdp.height`.

## VM control

```bash
alfaos start      # start VM
alfaos shutdown   # graceful ACPI shutdown
alfaos reboot     # reboot running VM (or start if stopped)
```

These commands use libvirt (`qemu:///system`). If your user is not in the `libvirt` group, `alfaos` will prompt for `sudo` automatically.

## CLI commands

| Command | Description |
|---------|-------------|
| `alfaos install` | Full automated install (requires root) |
| `alfaos connect` | Open RDP session to the VM |
| `alfaos expose-rdp` | Install/restart host RDP proxy (wake-on-connect) |
| `alfaos start` | Start the VM |
| `alfaos shutdown` | Graceful shutdown |
| `alfaos reboot` | Reboot (or start if stopped) |
| `alfaos version` | Print version |

## Desktop slimming

ALFAOS installs a **minimal XFCE** — no `xfce4-goodies` meta-package. The installer also purges leftover bloat if present.

### Default terminal: Tilix

Dark GTK terminal with tabs and splits, matched to Arc-Dark. Set `alfaos.terminal: xfce4-terminal` in config to use the stock XFCE terminal instead.

### Installed by default (lightweight essentials)

| Program | Package | Purpose |
|---------|---------|---------|
| Firefox | `firefox-esr` | Web browser |
| Notepad | `mousepad` | Text editor |
| Calculator | `galculator` | Calculator |
| .deb installer | `gdebi` | Install local `.deb` packages (double-click in Thunar) |
| System monitor | `btop` | Terminal resource monitor |
| Terminal | `tilix` | Terminal emulator |
| Files | `thunar` | File manager |

Set `alfaos.browser: false` in config to skip Firefox.

### Not installed by default (optional)

| Package | Size | Notes |
|---------|------|-------|
| `xfce4-goodies` | ~50 MB meta | never — use minimal XFCE |

### Removed automatically (`xfce4-goodies` contents)

These are **not needed** on a remote desktop VM and are purged during install:

| Package | What it is |
|---------|------------|
| `xfburn` | CD/DVD burner |
| `ristretto` | Image viewer |
| `xfce4-notes` / `-plugin` | Sticky notes |
| `xfce4-dict` | Dictionary |
| `xfce4-mailwatch-plugin` | Mail checker |
| `xfce4-weather-plugin` | Weather widget |
| `xfce4-wavelan-plugin` | WiFi signal (useless in VM) |
| `xfce4-timer-plugin` | Timer |
| `xfce4-battery-plugin` | Laptop battery (useless in VM) |
| `xfce4-fsguard-plugin` | Disk space monitor |
| `xfce4-genmon-plugin` | Generic monitor |
| `xfce4-smartbookmark-plugin` | Smart bookmarks |
| `xfce4-netload-plugin` | Network graph |
| `xfce4-systemload-plugin` | CPU/RAM graph |
| `xfce4-cpugraph-plugin` | CPU graph |
| `xfce4-diskperf-plugin` | Disk perf graph |
| `xfce4-sensors-plugin` | Hardware sensors |
| `xfce4-verve-plugin` | Command line on desktop |
| `xfce4-screenshooter` | Screenshot tool |
| `xfce4-taskmanager` | Task manager |
| `thunar-archive-plugin` | Archive plugin for file manager |
| `thunar-media-tags-plugin` | MP3 tags in file manager |

### Kept (core desktop)

`xfce4-session`, `xfwm4`, `xfdesktop4`, `xfce4-panel`, `xfce4-settings`, `thunar`, `xfce4-whiskermenu-plugin`, `xfce4-notifyd`, `xfce4-power-manager`, `tilix`, `mousepad`, `galculator`, `gdebi`, `btop`, `firefox-esr`, `plank`, themes, wallpapers.

### Manual purge on existing VM

```bash
sudo apt purge xfce4-goodies xfburn ristretto xfce4-notes xfce4-notes-plugin \
  xfce4-dict xfce4-mailwatch-plugin xfce4-weather-plugin xfce4-wavelan-plugin \
  xfce4-timer-plugin xfce4-battery-plugin xfce4-fsguard-plugin xfce4-genmon-plugin \
  xfce4-smartbookmark-plugin xfce4-netload-plugin xfce4-systemload-plugin \
  xfce4-cpugraph-plugin xfce4-diskperf-plugin xfce4-sensors-plugin xfce4-verve-plugin \
  xfce4-screenshooter xfce4-taskmanager thunar-archive-plugin thunar-media-tags-plugin
sudo apt autoremove -y
sudo apt install -y tilix
```

## License

MIT
