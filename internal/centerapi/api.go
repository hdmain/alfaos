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
		RDPQuality:     QualityNameFromSize(cfg.RDP.Width, cfg.RDP.Height),
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
	wth, hgt := req.Width, req.Height
	if req.Quality != "" {
		pw, ph, ok := QualityPreset(req.Quality)
		if !ok {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "quality must be low|medium|high|ultra"})
			return
		}
		wth, hgt = pw, ph
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
	if err := config.Save(cfg, s.cfgPath); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}
	s.cfg = cfg

	if err := applyGuestResolution(cfg, wth, hgt); err != nil {
		logging.Warn("guest resolution update: %v", err)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":      true,
			"width":   wth,
			"height":  hgt,
			"quality": QualityNameFromSize(wth, hgt),
			"warning": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"width":   wth,
		"height":  hgt,
		"quality": QualityNameFromSize(wth, hgt),
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

func applyGuestResolution(cfg *config.Config, w, h int) error {
	vm := virtualization.New(cfg)
	if !vm.DomainExists() || !vm.DomainRunning() {
		return fmt.Errorf("VM not running — resolution saved for next session")
	}
	ip, err := vm.GetVMIP(30 * time.Second)
	if err != nil {
		return err
	}
	script := fmt.Sprintf(`sudo tee /etc/alfaos/rdp-resolution >/dev/null <<EOF
W=%d
H=%d
EOF
/home/alfaos/.local/bin/alfaos-set-resolution.sh >/tmp/alfaos-resolution.log 2>&1 || true
`, w, h)
	_, err = vm.RunSSH(ip, script)
	return err
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
