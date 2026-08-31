package power

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alfaos/alfaos/internal/config"
	"github.com/alfaos/alfaos/internal/logging"
	"github.com/alfaos/alfaos/internal/networking"
	"github.com/alfaos/alfaos/internal/virtualization"
)

// Proxy listens on the host RDP port, wakes the VM on incoming connections,
// forwards TCP to the guest xRDP, and shuts the VM down after idle time.
type Proxy struct {
	cfg *config.Config
	vm  *virtualization.Manager

	mu        sync.Mutex
	active    atomic.Int32
	idleTimer *time.Timer
	wakeMu    sync.Mutex
}

// Run starts the RDP proxy daemon (blocking).
func Run(cfg *config.Config) error {
	p := &Proxy{
		cfg: cfg,
		vm:  virtualization.New(cfg),
	}
	return p.listenAndServe()
}

func (p *Proxy) listenAndServe() error {
	bind := p.cfg.RDP.BindHost
	if bind == "" {
		bind = "0.0.0.0"
	}
	port := p.cfg.RDP.Port
	if port <= 0 {
		port = 3389
	}
	addr := fmt.Sprintf("%s:%d", bind, port)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	defer ln.Close()

	logging.Info("RDP proxy listening on %s (wake_on_rdp=%v, idle_shutdown=%dm)",
		addr, p.cfg.Power.WakeOnRDP, p.cfg.Power.IdleShutdownMinutes)
	if p.cfg.Power.IdleShutdownMinutes > 0 {
		p.scheduleIdle()
	}

	for {
		conn, err := ln.Accept()
		if err != nil {
			return fmt.Errorf("accept: %w", err)
		}
		go p.handle(conn)
	}
}

func (p *Proxy) handle(client net.Conn) {
	defer client.Close()
	remote := client.RemoteAddr().String()
	logging.Info("RDP client connected from %s", remote)

	p.cancelIdle()
	p.active.Add(1)
	defer func() {
		p.active.Add(-1)
		p.scheduleIdle()
		logging.Info("RDP client disconnected: %s (active=%d)", remote, p.active.Load())
	}()

	vmIP, err := p.ensureVMReady(5 * time.Minute)
	if err != nil {
		logging.Error("Wake/ready failed for %s: %v", remote, err)
		return
	}

	vmPort := p.cfg.RDP.Port
	if vmPort <= 0 {
		vmPort = 3389
	}
	backend, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", vmIP, vmPort), 15*time.Second)
	if err != nil {
		logging.Error("Dial VM RDP %s:%d: %v", vmIP, vmPort, err)
		return
	}
	defer backend.Close()

	_ = client.SetDeadline(time.Time{})
	_ = backend.SetDeadline(time.Time{})
	setTCPNoDelay(client)
	setTCPNoDelay(backend)

	bufSize := 256 << 10
	bufClient := make([]byte, bufSize)
	bufBackend := make([]byte, bufSize)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_, _ = io.CopyBuffer(backend, client, bufClient)
		cancel()
	}()
	go func() {
		_, _ = io.CopyBuffer(client, backend, bufBackend)
		cancel()
	}()
	<-ctx.Done()
}

func setTCPNoDelay(conn net.Conn) {
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true)
	}
}

func (p *Proxy) ensureVMReady(timeout time.Duration) (string, error) {
	p.wakeMu.Lock()
	defer p.wakeMu.Unlock()

	deadline := time.Now().Add(timeout)

	if !p.vm.DomainExists() {
		return "", fmt.Errorf("VM %q does not exist — run: sudo alfaos install", p.cfg.VM.Name)
	}

	if !p.vm.DomainRunning() {
		if !p.cfg.Power.WakeOnRDP {
			return "", fmt.Errorf("VM is not running and wake_on_rdp is disabled")
		}
		logging.Info("VM is stopped — starting for incoming RDP...")
		if err := p.vm.StartVM(); err != nil {
			return "", fmt.Errorf("start VM: %w", err)
		}
	}

	remaining := time.Until(deadline)
	if remaining < 30*time.Second {
		remaining = 30 * time.Second
	}
	vmIP, err := p.vm.GetVMIP(remaining)
	if err != nil {
		return "", fmt.Errorf("VM IP: %w", err)
	}
	_ = os.WriteFile(filepath.Join(p.cfg.Paths.StateDir, "vm.ip"), []byte(vmIP), 0644)

	port := fmt.Sprintf("%d", p.cfg.RDP.Port)
	if p.cfg.RDP.Port <= 0 {
		port = "3389"
	}
	logging.Info("Waiting for xRDP on %s:%s...", vmIP, port)
	for time.Now().Before(deadline) {
		if networking.TestPort(vmIP, port) {
			logging.Success("VM RDP ready at %s:%s", vmIP, port)
			return vmIP, nil
		}
		time.Sleep(2 * time.Second)
	}
	return "", fmt.Errorf("RDP port %s not open on %s within timeout", port, vmIP)
}

func (p *Proxy) cancelIdle() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.idleTimer != nil {
		p.idleTimer.Stop()
		p.idleTimer = nil
	}
}

func (p *Proxy) scheduleIdle() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.scheduleIdleLocked()
}

func (p *Proxy) scheduleIdleLocked() {
	mins := p.cfg.Power.IdleShutdownMinutes
	if mins <= 0 {
		return
	}
	if p.active.Load() > 0 {
		return
	}
	if p.idleTimer != nil {
		p.idleTimer.Stop()
	}
	d := time.Duration(mins) * time.Minute
	logging.Info("No RDP sessions — will shut down VM in %v if still idle", d)
	p.idleTimer = time.AfterFunc(d, p.idleShutdown)
}

func (p *Proxy) idleShutdown() {
	p.mu.Lock()
	p.idleTimer = nil
	p.mu.Unlock()

	if p.active.Load() > 0 {
		return
	}

	p.wakeMu.Lock()
	defer p.wakeMu.Unlock()

	if p.active.Load() > 0 {
		return
	}
	if !p.vm.DomainRunning() {
		logging.Info("Idle shutdown skipped — VM already stopped")
		return
	}

	logging.Info("Idle timeout reached — shutting down VM to free host resources")
	if err := p.vm.ShutdownVM(2 * time.Minute); err != nil {
		logging.Warn("Graceful shutdown failed (%v) — forcing power off", err)
		_ = p.vm.StopVM()
	}
	logging.Success("VM shut down after idle (connect via RDP to wake)")
}
