// SPDX-License-Identifier: GPL-3.0-or-later

package aim

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- known-good frames from the spec's appendix ----

func TestFramesMatchSpec(t *testing.T) {
	if got, want := hex.EncodeToString(helloFrame), "3c685354435008000000003e00000000060800003c535443500e003e"; got != want {
		t.Fatalf("HELLO\n got %s\nwant %s", got, want)
	}
	if got, want := hex.EncodeToString(buildAck(0)), "3c685354435004000000003e000000003c5354435000003e"; got != want {
		t.Fatalf("ACK 0\n got %s\nwant %s", got, want)
	}
}

// ---- fake logger ----

type fakeLogger struct {
	files        map[string][]byte
	listing      []byte
	stallAfter   int  // stop answering after this many chunks (-1: never)
	corruptChunk bool // send a chunk with a bad checksum
	tcp          net.Listener
	udp          net.PacketConn
	silentUDP    bool
	once         sync.Once
}

func newFakeLogger(t *testing.T) *fakeLogger {
	t.Helper()
	f := &fakeLogger{files: map[string][]byte{}, stallAfter: -1}
	var err error
	if f.tcp, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	if f.udp, err = net.ListenPacket("udp", "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.tcp.Close(); f.udp.Close() })
	return f
}

func (f *fakeLogger) opts() Options {
	// Start serving only once the test has finished configuring the fake.
	f.once.Do(func() { go f.serveUDP(); go f.serveTCP() })
	return Options{
		Host:           "127.0.0.1",
		TCPPort:        f.tcp.Addr().(*net.TCPAddr).Port,
		UDPPort:        f.udp.LocalAddr().(*net.UDPAddr).Port,
		ReadTimeout:    400 * time.Millisecond,
		ConnectTimeout: time.Second,
		Keepalive:      true,
	}
}

func (f *fakeLogger) serveUDP() {
	buf := make([]byte, 64)
	for {
		_, addr, err := f.udp.ReadFrom(buf)
		if err != nil {
			return
		}
		if !f.silentUDP {
			status := make([]byte, 236)
			status[0] = 0xec
			f.udp.WriteTo(status, addr)
		}
	}
}

func readFrame(r *bufio.Reader) (tag string, payload []byte, err error) {
	hdr := make([]byte, 12)
	if _, err = io.ReadFull(r, hdr); err != nil {
		return
	}
	tag = string(hdr[2:6])
	payload = make([]byte, binary.LittleEndian.Uint32(hdr[6:10]))
	if _, err = io.ReadFull(r, payload); err != nil {
		return
	}
	_, err = io.ReadFull(r, make([]byte, 8))
	return
}

func (f *fakeLogger) serveTCP() {
	for {
		c, err := f.tcp.Accept()
		if err != nil {
			return
		}
		go f.handle(c)
	}
}

func (f *fakeLogger) handle(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	for {
		tag, p, err := readFrame(r)
		if err != nil {
			return
		}
		switch tag {
		case "STCP": // HELLO
			c.Write(buildFrame("STCP", []byte{0, 0, 0, 0, 0x06, 0x09, 0, 0}))
		case "STNC":
			op, sub := binary.LittleEndian.Uint16(p[8:]), binary.LittleEndian.Uint16(p[10:])
			path := strings.TrimRight(string(p[32:]), "\x00")
			var data []byte
			switch {
			case op == opList && sub == subList:
				data = f.listing
			case op == opRead && sub == subRead:
				d, ok := f.files[path]
				if !ok {
					return
				}
				data = d
			default:
				return
			}
			h := make([]byte, 64)
			c.Write(buildFrame("STCP", h))
			binary.LittleEndian.PutUint32(h[16:], uint32(len(data)))
			c.Write(buildFrame("STCP", h))
			for sent, n := 0, 0; sent < len(data); n++ {
				if _, ap, err := readFrame(r); err != nil || binary.LittleEndian.Uint32(ap) != uint32(sent) {
					return
				}
				if f.stallAfter >= 0 && n >= f.stallAfter {
					time.Sleep(5 * time.Second) // go silent, no FIN/RST
					return
				}
				end := min(sent+maxChunk, len(data))
				body := binary.LittleEndian.AppendUint32(nil, uint32(sent))
				body = append(body, data[sent:end]...)
				fr := buildFrame("STCP", body)
				if f.corruptChunk {
					fr[len(fr)-3] ^= 0xFF // damage the checksum
				}
				c.Write(fr)
				sent = end
			}
		}
	}
}

const maxChunk = 65472

const listing = "garbage\x00\x00name,size,date,hour,nlap,nbest,best,pilota,track_name,veicolo\r\n" +
	"a_0775.xrz,2621422,06/10/2026,15:02:48,1,1,60000,,RCQMA,\r\n" +
	"a_0751.xrz,3414801,29/11/2015,19:00:07,11,3,59000,,Hallett,\r\n" +
	"a_0694.hrz,3901753,29/11/2015,19:00:07,1,1,61000,,Ranch Long,\r\n" +
	"a_0729.xrz,1513251,31/08/2026,20:46:09,1,1,61000,,,\r\n"

func compressed(t *testing.T, payload []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	b.WriteString("<hXXX") // some leading bytes before the zlib stream
	w := zlib.NewWriter(&b)
	w.Write(payload)
	w.Close()
	return b.Bytes()
}

// ---- tests ----

func TestListSessions(t *testing.T) {
	f := newFakeLogger(t)
	f.listing = []byte(listing)
	c, err := Dial(context.Background(), f.opts())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	got, err := c.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("got %d sessions", len(got))
	}
	s := got[0]
	if s.Name != "a_0775.xrz" || s.ID != 775 || s.Ext != "xrz" || s.Size != 2621422 ||
		s.Date != "06/10/2026" || s.Hour != "15:02:48" || s.Track != "RCQMA" || s.Laps != "1" {
		t.Fatalf("unexpected parse: %+v", s)
	}
	if got[2].Ext != "hrz" || got[2].ID != 694 || got[2].Track != "Ranch Long" {
		t.Fatalf("hrz row: %+v", got[2])
	}
	if got[3].Track != "" {
		t.Fatalf("blank track should stay blank: %+v", got[3])
	}
}

func TestMultiChunkReadAndInflate(t *testing.T) {
	f := newFakeLogger(t)
	orig := make([]byte, 300_000) // incompressible => several 64 KB chunks
	rand.Read(orig)
	orig = append([]byte("<hCNF"), orig...)
	f.files["1:/mem/a_0001.xrz"] = compressed(t, orig)
	c, err := Dial(context.Background(), f.opts())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var lastDone, lastTotal int
	raw, err := c.ReadFile("1:/mem/a_0001.xrz", func(d, tot int) { lastDone, lastTotal = d, tot })
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, f.files["1:/mem/a_0001.xrz"]) {
		t.Fatal("raw bytes differ")
	}
	if lastDone != lastTotal || lastTotal != len(raw) {
		t.Fatalf("progress ended at %d/%d (len %d)", lastDone, lastTotal, len(raw))
	}
	out, err := Inflate(raw)
	if err != nil || !bytes.Equal(out, orig) || !LooksLikeXRK(out) {
		t.Fatalf("inflate: err=%v equal=%v", err, bytes.Equal(out, orig))
	}
}

func TestHangMidTransferIsDetected(t *testing.T) {
	f := newFakeLogger(t)
	big := make([]byte, 300_000)
	rand.Read(big)
	f.files["1:/mem/a_0002.xrz"] = big
	f.stallAfter = 1
	o := f.opts()
	o.Keepalive = false // exercise the read-timeout path on its own
	c, err := Dial(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	start := time.Now()
	_, err = c.ReadFile("1:/mem/a_0002.xrz", nil)
	if !errors.Is(err, ErrHung) {
		t.Fatalf("want ErrHung, got %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("took too long to give up: %s", time.Since(start))
	}
}

func TestKeepaliveLossAbortsBlockedRead(t *testing.T) {
	f := newFakeLogger(t)
	big := make([]byte, 300_000)
	rand.Read(big)
	f.files["1:/mem/a_0003.xrz"] = big
	f.stallAfter = 1
	f.silentUDP = true
	o := f.opts()
	o.ReadTimeout = 30 * time.Second // only the watchdog can rescue us
	c, err := Dial(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	start := time.Now()
	_, err = c.ReadFile("1:/mem/a_0003.xrz", nil)
	if !errors.Is(err, ErrHung) {
		t.Fatalf("want ErrHung, got %v", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("watchdog too slow: %s", d)
	}
}

func TestBadChunkChecksumIsRejected(t *testing.T) {
	f := newFakeLogger(t)
	f.files["1:/mem/a_0004.xrz"] = []byte("hello world, this is a small file")
	f.corruptChunk = true
	c, err := Dial(context.Background(), f.opts())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err = c.ReadFile("1:/mem/a_0004.xrz", nil); !errors.Is(err, ErrProtocol) {
		t.Fatalf("want ErrProtocol, got %v", err)
	}
}

func TestUnreachable(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	_, err := Dial(context.Background(), Options{Host: "127.0.0.1", TCPPort: port, ConnectTimeout: time.Second})
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("want ErrUnreachable, got %v", err)
	}
}

func TestInflateRejectsGarbage(t *testing.T) {
	if _, err := Inflate([]byte("definitely not compressed")); !errors.Is(err, ErrInflate) {
		t.Fatalf("want ErrInflate, got %v", err)
	}
	trunc := compressed(t, bytes.Repeat([]byte("abc"), 5000))
	if _, err := Inflate(trunc[:len(trunc)/2]); !errors.Is(err, ErrInflate) {
		t.Fatalf("truncated stream: want ErrInflate, got %v", err)
	}
}
