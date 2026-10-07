// SPDX-License-Identifier: GPL-3.0-or-later

package engine

import (
	"context"
	"net"
	"strconv"
	"time"

	"mychron-sync/internal/aim"
)

// Probe modes.
const (
	ProbeAuto = "auto" // UDP keepalive first, TCP connect as a fallback
	ProbeUDP  = "udp"  // least intrusive: doesn't touch the logger's single TCP socket
	ProbeTCP  = "tcp"  // connect and immediately close
)

// NewProbe returns a function that reports whether the logger is reachable.
func NewProbe(mode string) func(ctx context.Context, host string) bool {
	switch mode {
	case ProbeUDP:
		return probeUDP
	case ProbeTCP:
		return probeTCP
	default:
		return func(ctx context.Context, host string) bool {
			return probeUDP(ctx, host) || probeTCP(ctx, host)
		}
	}
}

func probeUDP(ctx context.Context, host string) bool {
	d := net.Dialer{Timeout: 2 * time.Second}
	c, err := d.DialContext(ctx, "udp", net.JoinHostPort(host, strconv.Itoa(aim.DefaultUDPPort)))
	if err != nil {
		return false
	}
	defer c.Close()
	if _, err := c.Write([]byte("aim-ka")); err != nil {
		return false
	}
	_ = c.SetReadDeadline(time.Now().Add(1500 * time.Millisecond))
	_, err = c.Read(make([]byte, 512))
	return err == nil
}

func probeTCP(ctx context.Context, host string) bool {
	d := net.Dialer{Timeout: 3 * time.Second}
	c, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(aim.DefaultTCPPort)))
	if err != nil {
		return false
	}
	c.Close()
	return true
}
