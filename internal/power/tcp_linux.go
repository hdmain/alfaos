//go:build linux

package power

import (
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

func setTCPQuickAck(tc *net.TCPConn) {
	rc, err := tc.SyscallConn()
	if err != nil {
		return
	}
	_ = rc.Control(func(fd uintptr) {
		_ = unix.SetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_QUICKACK, 1)
		_ = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP, syscall.TCP_NODELAY, 1)
	})
}
