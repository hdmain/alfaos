package centerapi

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/alfaos/alfaos/internal/config"
	hostpkg "github.com/alfaos/alfaos/internal/host"
	"github.com/alfaos/alfaos/internal/logging"
	"github.com/alfaos/alfaos/internal/networking"
	"github.com/alfaos/alfaos/internal/passwd"
	"github.com/alfaos/alfaos/internal/virtualization"
)

const (
	DefaultPort = 7391
	TokenFile   = "center.token"
)

// Server is the host-side HTTP API for Alfa Center (guest UI).
type Server struct {
	cfg     *config.Config
	cfgPath string
	token   string
	port    int
}

type statusResponse struct {
	OK              bool   `json:"ok"`
	Version         string `json:"version"`
	VMName          string `json:"vm_name"`
	VMRunning       bool   `json:"vm_running"`
	Onioning        bool   `json:"onioning"`
	OnioningStable  bool   `json:"onioning_stable"`
	OnioningActive  bool   `json:"onioning_active"`
	RDPWidth        int    `json:"rdp_width"`
	RDPHeight       int    `json:"rdp_height"`
	RDPQuality      string `json:"rdp_quality"`
	IdleMinutes     int    `json:"idle_shutdown_minutes"`
	WakeOnRDP       bool   `json:"wake_on_rdp"`
	DNS             []string `json:"dns"`
}

type passwordRequest struct {
	Current string `json:"current"`
	New     string `json:"new"`
}

type qualityRequest struct {
	Quality string `json:"quality"` // low | medium | high | ultra
	Width   int    `json:"width"`
	Height  int    `json:"height"`
}

type onioningRequest struct {
	Enabled bool `json:"enabled"`
	Stable  bool `json:"stable"`
}

type powerRequest struct {
	IdleShutdownMinutes *int  `json:"idle_shutdown_minutes"`
	WakeOnRDP           *bool `json:"wake_on_rdp"`
}

type apiError struct {
	Error string `json:"error"`
}

// EnsureToken creates or loads the shared API token used by Alfa Center.
func EnsureToken(stateDir string) (string, error) {
	if stateDir == "" {
		stateDir = "/var/lib/alfaos/state"
	}
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return "", err
	}
	path := filepath.Join(stateDir, TokenFile)
	if data, err := os.ReadFile(path); err == nil {
		if t := strings.TrimSpace(string(data)); t != "" {
			return t, nil
		}
	}
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)
	if err := os.WriteFile(path, []byte(token+"\n"), 0600); err != nil {
		return "", err
	}
	return token, nil
}

// QualityPreset maps a named quality to width/height.
func QualityPreset(name string) (w, h int, ok bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "low":
		return 1280, 720, true
	case "medium", "med":
		return 1600, 900, true
	case "high":
		return 1920, 1080, true
	case "ultra":
		return 2560, 1440, true
	default:
		return 0, 0, false
	}
}

// QualityNameFromSize returns the closest preset name for width×height.
func QualityNameFromSize(w, h int) string {
	for _, name := range []string{"low", "medium", "high", "ultra"} {
		pw, ph, _ := QualityPreset(name)
		if pw == w && ph == h {
			return name
		}
	}
	return "custom"
}

// Run starts the HTTP API (blocking).
func Run(cfg *config.Config, cfgPath string) error {
	token, err := EnsureToken(cfg.Paths.StateDir)
	if err != nil {
		return fmt.Errorf("api token: %w", err)
	}
	s := &Server{cfg: cfg, cfgPath: cfgPath, token: token, port: DefaultPort}
	return s.listenAndServe()
}

func (s *Server) listenAndServe() error {
	bridge := networking.LibvirtBridge(s.cfg.VM.Network)
	gw := networking.LibvirtGateway(s.cfg.VM.Network, bridge)
	addr := fmt.Sprintf("%s:%d", gw, s.port)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", s.handleHealth)
	mux.HandleFunc("/api/status", s.auth(s.handleStatus))
	mux.HandleFunc("/api/password", s.auth(s.handlePassword))
	mux.HandleFunc("/api/rdp/quality", s.auth(s.handleQuality))
	mux.HandleFunc("/api/onioning", s.auth(s.handleOnioning))
	mux.HandleFunc("/api/power", s.auth(s.handlePower))

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		// Fallback: bind all interfaces on the API port (still only useful from guest NAT).
		addr = fmt.Sprintf("0.0.0.0:%d", s.port)
		ln, err = net.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("listen %s: %w", addr, err)
		}
	}
	logging.Info("Alfa Center API listening on http://%s (guest → host)", ln.Addr())

	srv := &http.Server{
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 60 * time.Second,
	}
	return srv.Serve(ln)
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("X-Alfa-Token")
		if got == "" {
			if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
				got = strings.TrimPrefix(auth, "Bearer ")
			}
		}
		if got == "" || got != s.token {
			writeJSON(w, http.StatusUnauthorized, apiError{Error: "unauthorized"})
			return
		}
		next(w, r)
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, apiError{Error: "method not allowed"})
		return
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}
	s.cfg = cfg
	vm := virtualization.New(cfg)
	writeJSON(w, http.StatusOK, statusResponse{
		OK:             true,
		Version:        "1",
		VMName:         cfg.VM.Name,
		VMRunning:      vm.DomainExists() && vm.DomainRunning(),
		Onioning:       cfg.Onioning,
		OnioningStable: cfg.OnioningStable,
		OnioningActive: networking.OnioningActive(),
		RDPWidth:       cfg.RDP.Width,
		RDPHeight:      cfg.RDP.Height,
		RDPQuality:     effectiveQuality(cfg),
		IdleMinutes:    cfg.Power.IdleShutdownMinutes,
		WakeOnRDP:      cfg.Power.WakeOnRDP,
		DNS:            cfg.DNSServers(),
	})
}

func (s *Server) handlePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, apiError{Error: "method not allowed"})
		return
	}
	var req passwordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid json"})
		return
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}
	if req.Current != cfg.ALFAOS.Password {
		writeJSON(w, http.StatusForbidden, apiError{Error: "current password is incorrect"})
		return
	}
	if strings.TrimSpace(req.New) == "" {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "new password cannot be empty"})
		return
	}
	if req.New == req.Current {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "new password must differ from current"})
		return
	}
	if err := passwd.Change(cfg, s.cfgPath, req.New); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}
	s.cfg = cfg
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleQuality(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, apiError{Error: "method not allowed"})
		return
	}
	var req qualityRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid json"})
		return
	}

	quality := strings.ToLower(strings.TrimSpace(req.Quality))
	wth, hgt := req.Width, req.Height
	if quality != "" {
		pw, ph, ok := QualityPreset(quality)
		if !ok {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "quality must be low|medium|high|ultra"})
			return
		}
		wth, hgt = pw, ph
	} else {
		quality = QualityNameFromSize(wth, hgt)
		if quality == "custom" {
			quality = "high"
		}
	}
	if wth < 800 || hgt < 600 || wth > 3840 || hgt > 2160 {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "resolution out of range"})
		return
	}

	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}
	cfg.RDP.Width = wth
	cfg.RDP.Height = hgt
	cfg.RDP.Quality = quality
	if err := config.Save(cfg, s.cfgPath); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}
	s.cfg = cfg

	applied, err := applyGuestQuality(cfg, quality, wth, hgt)
	if err != nil {
		logging.Warn("guest quality update: %v", err)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":      true,
			"width":   wth,
			"height":  hgt,
			"quality": quality,
			"bpp":     qualityBPP(quality),
			"applied": false,
			"warning": err.Error(),
			"hint":    "Saved on host. Disconnect and reconnect RDP to apply.",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"width":   wth,
		"height":  hgt,
		"quality": quality,
		"bpp":     qualityBPP(quality),
		"applied": applied,
		"hint":    "Resolution applied to the live session. Reconnect RDP once for color-depth (bpp) to fully update.",
	})
}

func (s *Server) handleOnioning(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, apiError{Error: "method not allowed"})
		return
	}
	var req onioningRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid json"})
		return
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}
	if err := networking.ConfigureOnioning(req.Enabled, req.Stable && req.Enabled, cfg.Paths.StateDir, cfg.VM.Network); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}
	cfg.Onioning = req.Enabled
	cfg.OnioningStable = req.Enabled && req.Stable
	if err := config.Save(cfg, s.cfgPath); err != nil {
		logging.Warn("save config: %v", err)
	}
	s.cfg = cfg
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":               true,
		"onioning":         cfg.Onioning,
		"onioning_stable":  cfg.OnioningStable,
		"onioning_active":  networking.OnioningActive(),
	})
}

func (s *Server) handlePower(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, apiError{Error: "method not allowed"})
		return
	}
	var req powerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid json"})
		return
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}
	if req.IdleShutdownMinutes != nil {
		if *req.IdleShutdownMinutes < 0 || *req.IdleShutdownMinutes > 24*60 {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "idle_shutdown_minutes out of range"})
			return
		}
		cfg.Power.IdleShutdownMinutes = *req.IdleShutdownMinutes
	}
	if req.WakeOnRDP != nil {
		cfg.Power.WakeOnRDP = *req.WakeOnRDP
	}
	if err := config.Save(cfg, s.cfgPath); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}
	s.cfg = cfg
	// Restart RDP proxy so idle timer picks up new config.
	_, _ = runSystemctl("restart", "alfaos-rdp-forward.service")
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                      true,
		"idle_shutdown_minutes":   cfg.Power.IdleShutdownMinutes,
		"wake_on_rdp":             cfg.Power.WakeOnRDP,
	})
}

func applyGuestQuality(cfg *config.Config, quality string, w, h int) (bool, error) {
	vm := virtualization.New(cfg)
	if !vm.DomainExists() || !vm.DomainRunning() {
		return false, fmt.Errorf("VM not running — quality saved for next session")
	}
	ip, err := vm.GetVMIP(30 * time.Second)
	if err != nil {
		return false, err
	}
	bpp := qualityBPP(quality)
	// Install/update the apply helper, then run it against the live Xrdp session(s).
	script := fmt.Sprintf(`set -euo pipefail
sudo mkdir -p /etc/alfaos /home/alfaos/.local/bin
sudo tee /etc/alfaos/rdp-resolution >/dev/null <<EOF
W=%d
H=%d
EOF
sudo tee /etc/alfaos/rdp-quality >/dev/null <<EOF
QUALITY=%s
BPP=%d
EOF

# Persist xRDP color depth (takes effect on next RDP connect)
if [ -f /etc/xrdp/xrdp.ini ]; then
  if grep -q '^max_bpp=' /etc/xrdp/xrdp.ini; then
    sudo sed -i 's/^max_bpp=.*/max_bpp=%d/' /etc/xrdp/xrdp.ini
  else
    sudo sed -i '/^\[Globals\]/a max_bpp=%d' /etc/xrdp/xrdp.ini
  fi
fi

sudo tee /home/alfaos/.local/bin/alfaos-apply-quality.sh >/dev/null << 'APPLYSCRIPT'
#!/bin/bash
set -euo pipefail
[ -f /etc/alfaos/rdp-resolution ] && . /etc/alfaos/rdp-resolution
W=${W:-1920}
H=${H:-1080}
LOG=/tmp/alfaos-quality.log
: > "$LOG"
echo "target ${W}x${H}" >> "$LOG"

set_mode_on_display() {
  local display="$1" xauth="$2" output mode modeline
  export DISPLAY="$display"
  if [ -n "$xauth" ] && [ -f "$xauth" ]; then
    export XAUTHORITY="$xauth"
  else
    unset XAUTHORITY || true
  fi
  echo "try DISPLAY=$display XAUTHORITY=${XAUTHORITY:-}" >> "$LOG"
  for _ in $(seq 1 10); do
    output=$(xrandr 2>/dev/null | awk '/ connected/{print $1; exit}')
    [ -n "$output" ] && break
    sleep 0.3
  done
  [ -n "$output" ] || { echo "no output on $display" >> "$LOG"; return 1; }

  if xrandr --output "$output" --mode "${W}x${H}" >>"$LOG" 2>&1; then
    echo "set ${W}x${H} on $output ($display)" >> "$LOG"
    return 0
  fi
  while IFS= read -r mode; do
    if xrandr --output "$output" --mode "$mode" >>"$LOG" 2>&1; then
      echo "set $mode on $output ($display)" >> "$LOG"
      return 0
    fi
  done < <(xrandr 2>/dev/null | awk -v w="$W" -v h="$H" '$0 ~ w"x"h {print $1}')

  if command -v cvt >/dev/null 2>&1; then
    modeline=$(cvt "$W" "$H" 60 2>/dev/null | awk '/Modeline/{sub(/^Modeline /,""); print}')
    [ -n "$modeline" ] || return 1
    mode=$(echo "$modeline" | awk '{print $1}' | tr -d '"')
    xrandr --newmode $modeline >>"$LOG" 2>&1 || true
    xrandr --addmode "$output" "$mode" >>"$LOG" 2>&1 || true
    if xrandr --output "$output" --mode "$mode" >>"$LOG" 2>&1; then
      echo "created+set $mode on $output ($display)" >> "$LOG"
      return 0
    fi
  fi
  return 1
}

ok=0
# Prefer live Xorg sessions owned by alfaos (xrdp)
for pid in $(pgrep -u alfaos -x Xorg 2>/dev/null; pgrep -u alfaos -x Xorg.bin 2>/dev/null); do
  envfile="/proc/$pid/environ"
  [ -r "$envfile" ] || continue
  display=$(tr '\0' '\n' < "$envfile" | sed -n 's/^DISPLAY=//p' | head -1)
  xauth=$(tr '\0' '\n' < "$envfile" | sed -n 's/^XAUTHORITY=//p' | head -1)
  [ -n "$display" ] || continue
  if set_mode_on_display "$display" "$xauth"; then
    ok=1
  fi
done

# Fallback: scan sockets :10-:30 (typical xrdp range)
if [ "$ok" -eq 0 ]; then
  for n in $(seq 10 30); do
    sock="/tmp/.X11-unix/X$n"
    [ -S "$sock" ] || continue
    if set_mode_on_display ":$n" "/home/alfaos/.Xauthority"; then
      ok=1
      break
    fi
    # xorgxrdp sometimes stores auth under /run
    for auth in /run/xrdp/sockdir/* /var/run/xrdp/*; do
      [ -f "$auth" ] || continue
      if set_mode_on_display ":$n" "$auth"; then
        ok=1
        break 2
      fi
    done
  done
fi

if [ "$ok" -eq 1 ]; then
  echo APPLIED
  exit 0
fi
echo "FAILED — no live X display resized (will apply on next RDP login)" >> "$LOG"
exit 2
APPLYSCRIPT
sudo chmod +x /home/alfaos/.local/bin/alfaos-apply-quality.sh
sudo chown alfaos:alfaos /home/alfaos/.local/bin/alfaos-apply-quality.sh

# Keep legacy name working for startwm/reconnectwm
sudo ln -sf /home/alfaos/.local/bin/alfaos-apply-quality.sh /home/alfaos/.local/bin/alfaos-set-resolution.sh

# Also refresh reconnect/start hooks to call the apply script
if [ -f /etc/xrdp/reconnectwm.sh ]; then
  sudo tee /etc/xrdp/reconnectwm.sh >/dev/null << 'RECONNECT'
#!/bin/sh
/home/alfaos/.local/bin/alfaos-apply-quality.sh >/tmp/alfaos-quality.log 2>&1 || true
RECONNECT
  sudo chmod +x /etc/xrdp/reconnectwm.sh
fi

sudo -u alfaos /home/alfaos/.local/bin/alfaos-apply-quality.sh
`, w, h, quality, bpp, bpp, bpp)

	out, err := vm.RunSSH(ip, "bash -lc "+strconv.Quote(script))
	if err != nil {
		// exit 2 = saved but live resize failed — still partially OK
		if strings.Contains(out, "FAILED") || strings.Contains(err.Error(), "exit status 2") {
			return false, fmt.Errorf("live resize failed — reconnect RDP (log: /tmp/alfaos-quality.log)")
		}
		return false, fmt.Errorf("%w\n%s", err, out)
	}
	return strings.Contains(out, "APPLIED"), nil
}

func qualityBPP(quality string) int {
	switch strings.ToLower(quality) {
	case "low":
		return 16
	case "medium":
		return 24
	default:
		return 32
	}
}

func effectiveQuality(cfg *config.Config) string {
	if q := cfg.RDPQualityName(); q != "" {
		return q
	}
	return QualityNameFromSize(cfg.RDP.Width, cfg.RDP.Height)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func runSystemctl(args ...string) (string, error) {
	full := append([]string{"systemctl"}, args...)
	return hostpkg.RunCommand(full[0], full[1:]...)
}

// GuestConfigContent returns the config file Alfa Center reads inside the VM.
func GuestConfigContent(apiURL, token string) string {
	return fmt.Sprintf("API_URL=%s\nTOKEN=%s\n", apiURL, token)
}

// APIURLForGuest builds the URL guests should use (libvirt gateway).
func APIURLForGuest(cfg *config.Config) string {
	bridge := networking.LibvirtBridge(cfg.VM.Network)
	gw := networking.LibvirtGateway(cfg.VM.Network, bridge)
	return fmt.Sprintf("http://%s:%d", gw, DefaultPort)
}

// PortString returns the default API port as string.
func PortString() string {
	return strconv.Itoa(DefaultPort)
}
