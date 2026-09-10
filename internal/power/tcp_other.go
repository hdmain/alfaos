//go:build !linux

package power

import "net"

func setTCPQuickAck(_ *net.TCPConn) {}
