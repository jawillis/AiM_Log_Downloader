// SPDX-License-Identifier: GPL-3.0-or-later

package aim

import (
	"bytes"
	"compress/zlib"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// ErrInflate means a download could not be decompressed.
var ErrInflate = errors.New("could not inflate session")

// Session is one row of the logger's session list.
type Session struct {
	Name  string // e.g. a_0775.xrz (older firmware: a_0694.hrz)
	ID    int    // numeric part of Name; 0 if the name is unrecognised
	Ext   string // xrz / hrz
	Size  int64  // compressed size on the logger
	Date  string // DD/MM/YYYY as reported (often a bogus 2015 date if the clock was unset)
	Hour  string // HH:MM:SS
	Laps  string
	Track string

	Driver       string  // pilota
	Vehicle      string  // veicolo (your kart number)
	Championship string  // campionato
	Device       string  // the logger's own name
	BestMS       int64   // best lap in milliseconds; 0 when the logger has no lap time
	BestLap      int     // nbest: which lap was best, as the logger numbers it
	DurationMS   int64   // test_dur: session length in milliseconds
	Lat, Lon     float64 // track position in degrees; 0,0 when absent

	Fields map[string]string // every column the logger sent, trimmed
}

var nameRe = regexp.MustCompile(`^a_(\d+)\.([A-Za-z0-9]+)$`)

func parseSessions(body []byte) ([]Session, error) {
	var sb strings.Builder
	for _, b := range body { // the list is Latin-1
		sb.WriteRune(rune(b))
	}
	text := sb.String()
	idx := strings.Index(text, "name,size,")
	if idx < 0 {
		return nil, fmt.Errorf("%w: session list header not found", ErrProtocol)
	}
	r := csv.NewReader(strings.NewReader(text[idx:]))
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("%w: session list: %v", ErrProtocol, err)
	}
	for i := range header {
		header[i] = clean(header[i])
	}
	var out []Session
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: session list: %v", ErrProtocol, err)
		}
		f := map[string]string{}
		for i, v := range rec {
			if i < len(header) {
				f[header[i]] = clean(v)
			}
		}
		if f["name"] == "" {
			continue
		}
		s := Session{
			Name: f["name"], Date: f["date"], Hour: f["hour"],
			Laps: f["nlap"], Track: f["track_name"], Fields: f,
		}
		s.Size, _ = strconv.ParseInt(f["size"], 10, 64)
		s.Driver, s.Vehicle, s.Championship, s.Device = f["pilota"], f["veicolo"], f["campionato"], f["device"]
		s.BestMS, s.BestLap, s.DurationMS = nonNeg(f["best"]), int(nonNeg(f["nbest"])), nonNeg(f["test_dur"])
		s.Lat, s.Lon = coord(f["track_lat"]), coord(f["track_lon"])
		if m := nameRe.FindStringSubmatch(s.Name); m != nil {
			s.ID, _ = strconv.Atoi(m[1])
			s.Ext = strings.ToLower(m[2])
		}
		out = append(out, s)
	}
	return out, nil
}

func nonNeg(v string) int64 {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// coord converts the logger's degrees-times-10^7 integers to degrees.
func coord(v string) float64 {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0
	}
	d := float64(n) / 1e7
	if d < -180 || d > 180 {
		return 0
	}
	return d
}

// FormatLap renders a lap time in milliseconds as 1:49.549 (or 59.123).
func FormatLap(ms int64) string {
	if ms <= 0 {
		return ""
	}
	m, rest := ms/60000, ms%60000
	if m == 0 {
		return fmt.Sprintf("%d.%03d", rest/1000, rest%1000)
	}
	return fmt.Sprintf("%d:%02d.%03d", m, rest/1000, rest%1000)
}

func clean(s string) string { return strings.Trim(s, "\x00 \t\r\n") }

// Inflate extracts the zlib stream from a downloaded .xrz/.hrz payload.
func Inflate(raw []byte) ([]byte, error) {
	lastErr := errors.New("no zlib stream found")
	tried := 0
	for i := 0; i+1 < len(raw) && tried < 32; i++ {
		if raw[i] != 0x78 {
			continue
		}
		if b := raw[i+1]; b != 0x01 && b != 0x5e && b != 0x9c && b != 0xda {
			continue
		}
		tried++
		zr, err := zlib.NewReader(bytes.NewReader(raw[i:]))
		if err != nil {
			lastErr = err
			continue
		}
		out, err := io.ReadAll(zr)
		zr.Close()
		if err != nil {
			lastErr = err
			continue
		}
		return out, nil
	}
	return nil, fmt.Errorf("%w: %v", ErrInflate, lastErr)
}

// LooksLikeXRK reports whether data starts with the XRK container header.
func LooksLikeXRK(b []byte) bool { return bytes.HasPrefix(b, []byte("<hCNF")) }
