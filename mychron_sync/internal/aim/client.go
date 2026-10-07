// SPDX-License-Identifier: GPL-3.0-or-later
//
// Behaviour ported from mychron_wifi.py in github.com/TheAngryRaven/mychron-wifi-spec
// (Copyright (C) 2026 TheAngryRaven, GPL-3.0-or-later).

package aim

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"sync/atomic"
	"time"
)

var (
	// ErrUnreachable means the TCP connection could not be opened.
	ErrUnreachable = errors.New("logger unreachable")
	// ErrHung means the logger stopped answering (the ESP32 stack does this).
	// Reconnect, or wait and retry later.
	ErrHung = errors.New("logger stopped responding")
	// ErrProtocol means the logger answered, but not in a way we accept.
	ErrProtocol = errors.New("protocol error")

	errTimeout = errors.New("read timeout")
)

const trailerWait = 500 * time.Millisecond

// Options configures a connection.
type Options struct {
	Host           string
	TCPPort        int
	UDPPort        int
	ConnectTimeout time.Duration
	ReadTimeout    time.Duration // per read; the device can go silent without a FIN/RST
	Keepalive      bool          // UDP keepalive + hang watchdog (recommended)
	Log            *slog.Logger
}

func (o *Options) defaults() {
	if o.TCPPort == 0 {
		o.TCPPort = DefaultTCPPort
	}
	if o.UDPPort == 0 {
		o.UDPPort = DefaultUDPPort
	}
	if o.ConnectTimeout == 0 {
		o.ConnectTimeout = 5 * time.Second
	}
	if o.ReadTimeout == 0 {
		o.ReadTimeout = 10 * time.Second
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
}

// Client is one connection to a logger. It is not safe for concurrent use.
type Client struct {
	opts   Options
	conn   net.Conn
	rx     []byte
	buf    []byte
	hung   atomic.Bool
	stop   context.CancelFunc
	kaDone chan struct{}
}

// Dial connects and performs the HELLO handshake. The device allows a single
// TCP connection, so callers should Close promptly (Race Studio needs it too).
// Cancelling ctx aborts any in-flight operation.
func Dial(ctx context.Context, opts Options) (*Client, error) {
	opts.defaults()
	d := net.Dialer{Timeout: opts.ConnectTimeout}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(opts.Host, strconv.Itoa(opts.TCPPort)))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	cctx, cancel := context.WithCancel(ctx)
	context.AfterFunc(cctx, func() { conn.Close() })
	c := &Client{opts: opts, conn: conn, buf: make([]byte, 64*1024), stop: cancel}

	if _, err := conn.Write(helloFrame); err != nil {
		c.Close()
		return nil, fmt.Errorf("%w: sending hello: %v", ErrHung, err)
	}
	if _, err := c.nextFrame(); err != nil {
		c.Close()
		return nil, fmt.Errorf("hello: %w", err)
	}
	if opts.Keepalive {
		c.kaDone = make(chan struct{})
		go c.keepalive(cctx)
	}
	return c, nil
}

// Close releases the connection.
func (c *Client) Close() error {
	c.stop()
	if c.kaDone != nil {
		<-c.kaDone
	}
	return nil
}

// keepalive sends "aim-ka" over UDP about once a second. Three consecutive
// unanswered keepalives mean the device has hung; we then close the TCP
// connection so any blocked read returns immediately.
func (c *Client) keepalive(ctx context.Context) {
	defer close(c.kaDone)
	u, err := net.Dial("udp", net.JoinHostPort(c.opts.Host, strconv.Itoa(c.opts.UDPPort)))
	if err != nil {
		return
	}
	defer u.Close()
	buf := make([]byte, 2048)
	misses := 0
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		_, _ = u.Write(keepaliveMsg)
		_ = u.SetReadDeadline(time.Now().Add(800 * time.Millisecond))
		if _, err := u.Read(buf); err != nil {
			misses++
			if misses >= 3 && c.hung.CompareAndSwap(false, true) {
				c.opts.Log.Warn("logger stopped answering keepalives")
				c.conn.Close()
			}
		} else {
			misses = 0
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// fill reads more bytes into the receive buffer.
func (c *Client) fill(wait time.Duration) error {
	if c.hung.Load() {
		return fmt.Errorf("%w: keepalive lost", ErrHung)
	}
	_ = c.conn.SetReadDeadline(time.Now().Add(wait))
	n, err := c.conn.Read(c.buf)
	if n > 0 {
		c.rx = append(c.rx, c.buf[:n]...)
		return nil
	}
	if err == nil {
		return nil
	}
	if c.hung.Load() {
		return fmt.Errorf("%w: keepalive lost", ErrHung)
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return errTimeout
	}
	return fmt.Errorf("%w: %v", ErrHung, err)
}

// need blocks until at least n bytes are buffered; it returns errTimeout if
// wait elapses with no progress.
func (c *Client) need(n int, wait time.Duration) error {
	for len(c.rx) < n {
		if err := c.fill(wait); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) mustNeed(n int) error {
	err := c.need(n, c.opts.ReadTimeout)
	if errors.Is(err, errTimeout) {
		return fmt.Errorf("%w: no data for %s", ErrHung, c.opts.ReadTimeout)
	}
	return err
}

// nextFrame reads exactly one wire frame, resyncing on stray bytes.
func (c *Client) nextFrame() (frame, error) {
	for {
		if err := c.mustNeed(12); err != nil {
			return frame{}, err
		}
		if c.rx[0] != '<' || c.rx[1] != 'h' {
			if i := bytes.Index(c.rx, []byte("<h")); i >= 0 {
				c.rx = c.rx[i:]
			} else if c.rx[len(c.rx)-1] == '<' {
				c.rx = c.rx[len(c.rx)-1:]
			} else {
				c.rx = c.rx[:0]
			}
			continue
		}
		tag := string(c.rx[2:6])
		ln := int(binary.LittleEndian.Uint32(c.rx[6:10]))
		if ln < 0 || ln > maxFramePayload {
			return frame{}, fmt.Errorf("%w: frame length %d", ErrProtocol, ln)
		}
		if err := c.mustNeed(12 + ln); err != nil {
			return frame{}, err
		}
		f := frame{tag: tag, payload: append([]byte(nil), c.rx[12:12+ln]...)}
		end := 12 + ln
		// The trailer is part of the envelope, but tolerate its absence.
		if err := c.need(end+8, trailerWait); err != nil && !errors.Is(err, errTimeout) {
			return frame{}, err
		}
		if len(c.rx) >= end+8 && c.rx[end] == '<' && string(c.rx[end+1:end+5]) == tag && c.rx[end+7] == '>' {
			f.checksumOK = binary.LittleEndian.Uint16(c.rx[end+5:end+7]) == checksum(f.payload)
			end += 8
		}
		c.rx = c.rx[end:]
		return f, nil
	}
}

func (c *Client) send(b []byte) error {
	if c.hung.Load() {
		return fmt.Errorf("%w: keepalive lost", ErrHung)
	}
	_ = c.conn.SetWriteDeadline(time.Now().Add(c.opts.ReadTimeout))
	if _, err := c.conn.Write(b); err != nil {
		return fmt.Errorf("%w: write: %v", ErrHung, err)
	}
	return nil
}

// transfer runs the universal chunked read used for file reads and the
// session list: command, two header frames, then ACK/chunk until complete.
func (c *Client) transfer(cmd []byte, progress func(done, total int)) ([]byte, error) {
	if err := c.send(cmd); err != nil {
		return nil, err
	}
	if _, err := c.nextFrame(); err != nil { // header #1 (echoes the command)
		return nil, err
	}
	h2, err := c.nextFrame() // header #2: total size at payload[16:20]
	if err != nil {
		return nil, err
	}
	if len(h2.payload) < 20 {
		return nil, fmt.Errorf("%w: short transfer header (%d bytes)", ErrProtocol, len(h2.payload))
	}
	total := int(binary.LittleEndian.Uint32(h2.payload[16:20]))
	if total < 0 || total > maxTransfer {
		return nil, fmt.Errorf("%w: implausible transfer size %d", ErrProtocol, total)
	}
	data := make([]byte, 0, total)
	for len(data) < total {
		if err := c.send(buildAck(uint32(len(data)))); err != nil { // ACK = running offset
			return nil, err
		}
		f, err := c.nextFrame()
		if err != nil {
			return nil, err
		}
		if len(f.payload) <= 4 {
			return nil, fmt.Errorf("%w: empty chunk at offset %d", ErrProtocol, len(data))
		}
		if off := int(binary.LittleEndian.Uint32(f.payload[:4])); off != len(data) {
			return nil, fmt.Errorf("%w: chunk offset %d, expected %d", ErrProtocol, off, len(data))
		}
		if !f.checksumOK {
			return nil, fmt.Errorf("%w: bad checksum on chunk at offset %d", ErrProtocol, len(data))
		}
		chunk := f.payload[4:]
		if len(data)+len(chunk) > total {
			return nil, fmt.Errorf("%w: chunk overruns announced size", ErrProtocol)
		}
		data = append(data, chunk...)
		if progress != nil {
			progress(len(data), total)
		}
	}
	return data, nil
}

// ReadFile reads any file by "volume:/path", e.g. "1:/mem/a_0775.xrz".
func (c *Client) ReadFile(path string, progress func(done, total int)) ([]byte, error) {
	cmd, err := buildSTNC(opRead, subRead, path, 0)
	if err != nil {
		return nil, err
	}
	return c.transfer(cmd, progress)
}

// ListSessions returns the recorded sessions on the logger.
func (c *Client) ListSessions() ([]Session, error) {
	cmd, err := buildSTNC(opList, subList, "", 0)
	if err != nil {
		return nil, err
	}
	body, err := c.transfer(cmd, nil)
	if err != nil {
		return nil, err
	}
	return parseSessions(body)
}
