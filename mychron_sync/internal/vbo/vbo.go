// SPDX-License-Identifier: GPL-3.0-or-later

// Package vbo reads the session summary (date, length, laps, best lap, track)
// from a Racelogic .vbo file, as written by VBOX loggers and exported by apps
// such as RaceChrono, Harry's LapTimer and TrackAddict.
//
// Lap timing follows Lapline's reader (js/vbo.js) so the best lap shown on the
// sessions page is the same one Lapline shows when the file is opened.
package vbo

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Summary is what the sessions page needs from one file.
type Summary struct {
	Date       string // DD/MM/YYYY from "File created on ...", empty if missing
	Hour       string // HH:MM:SS
	DurationMS int64
	Laps       int   // laps as Lapline numbers them (out and in laps included); 0 if the file can't be timed
	BestLapMS  int64 // 0 when there is no timed lap
	BestLapNo  int   // Lapline's lap number of the best lap
	Track      string
	Vehicle    string
	Driver     string
	Lat, Lon   float64 // first GPS fix, degrees (east positive)
}

const day = 86400000.0

var (
	reSection = regexp.MustCompile(`^\[(.+)\]$`)
	reCreated = regexp.MustCompile(`(\d{1,2})/(\d{1,2})/(\d{4})(?:\s*@\s*(\d+):(\d+):(\d+))?`)
	reTrack   = regexp.MustCompile(`(?i)^(?:track|circuit|venue)\s*[:=]\s*(.+)$`)
	reVehicle = regexp.MustCompile(`(?i)^(?:vehicle|car)\s*[:=]\s*(.+)$`)
	reDriver  = regexp.MustCompile(`(?i)^(?:driver|racer)\s*[:=]\s*(.+)$`)
	reLine    = regexp.MustCompile(`(?i)^(start|finish)\s+([-+\d.\s]+)`)
)

func fields(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == '\t' || r == ',' })
}

// latin1 turns Latin-1 bytes into a string (the "¬" in [laptiming] lines isn't UTF-8).
func latin1(b []byte) string {
	r := make([]rune, len(b))
	for i, c := range b {
		r[i] = rune(c)
	}
	return string(r)
}

// hmsToMs turns "HHMMSS.SS" (UTC) into ms since midnight.
func hmsToMs(x float64) float64 {
	hh := math.Floor(x / 10000)
	mm := math.Mod(math.Floor(x/100), 100)
	ss := x - hh*10000 - mm*100
	return (hh*3600 + mm*60 + ss) * 1000
}

// Summarize reads a whole .vbo file. Only the time and GPS columns are parsed.
func Summarize(r io.Reader) (Summary, error) {
	var s Summary
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)

	sec := map[string][]string{}
	cur := "_pre"
	inData := false
	for !inData && sc.Scan() {
		ln := strings.TrimSpace(latin1(sc.Bytes()))
		if m := reSection.FindStringSubmatch(ln); m != nil {
			cur = strings.ToLower(strings.TrimSpace(m[1]))
			if _, ok := sec[cur]; !ok {
				sec[cur] = nil
			}
			inData = cur == "data"
			continue
		}
		if ln != "" {
			sec[cur] = append(sec[cur], ln)
		}
	}
	if err := sc.Err(); err != nil {
		return s, err
	}
	if !inData || sec["column names"] == nil {
		return s, errors.New("not a Racelogic .vbo file")
	}

	cols := fields(strings.Join(sec["column names"], " "))
	ci := func(name string) int {
		for i, c := range cols {
			if strings.EqualFold(c, name) {
				return i
			}
		}
		return -1
	}
	iT, iLat, iLon, iSat := ci("time"), ci("lat"), ci("long"), ci("sats")
	if iT < 0 {
		return s, errors.New("no time column")
	}
	num := func(tok []string, i int) float64 {
		if i < 0 {
			return math.NaN()
		}
		v, err := strconv.ParseFloat(tok[i], 64)
		if err != nil {
			return math.NaN()
		}
		return v
	}

	var t0, tEnd float64
	n := 0
	prevT, dayOff := math.Inf(-1), 0.0
	var gT, lat, lon []float64
	for sc.Scan() {
		tok := fields(latin1(sc.Bytes()))
		if len(tok) == 0 || len(tok) < len(cols) {
			continue
		}
		t := hmsToMs(num(tok, iT))
		if math.IsNaN(t) || math.IsInf(t, 0) {
			continue
		}
		t += dayOff
		if t < prevT-day/2 { // crossed midnight UTC
			dayOff += day
			t += day
		}
		if t <= prevT { // duplicate or out-of-order row
			continue
		}
		prevT = t
		if n == 0 {
			t0 = t
		}
		tEnd = t
		n++

		la, lo := num(tok, iLat)/60, -num(tok, iLon)/60 // minutes; VBO longitude is positive west
		if iSat >= 0 && int64(num(tok, iSat))&63 == 0 {
			continue
		}
		if la == 0 && lo == 0 {
			continue
		}
		gT, lat, lon = append(gT, t), append(lat, la), append(lon, lo)
	}
	if err := sc.Err(); err != nil {
		return s, err
	}
	if n < 2 {
		return s, errors.New("no data rows")
	}
	s.DurationMS = int64(math.Round(math.Max(1, tEnd-t0)))

	for k := range lat {
		if !math.IsNaN(lat[k]) && !math.IsNaN(lon[k]) {
			s.Lat, s.Lon = lat[k], lon[k]
			break
		}
	}

	// Laps, numbered the way Lapline numbers them.
	type lap struct {
		ms   float64
		kind string
	}
	var laps []lap
	start, finish, mode := findCrossings(sec["laptiming"], gT, lat, lon)
	switch {
	case mode == "circuit" && len(start) > 0:
		if start[0]-t0 > 1000 {
			laps = append(laps, lap{start[0] - t0, "out"})
		}
		for k := 0; k+1 < len(start); k++ {
			laps = append(laps, lap{start[k+1] - start[k], "lap"})
		}
		if tEnd-start[len(start)-1] > 1000 {
			laps = append(laps, lap{tEnd - start[len(start)-1], "in"})
		}
	case mode == "sprint":
		type ev struct {
			t     float64
			start bool
		}
		var evs []ev
		for _, t := range start {
			evs = append(evs, ev{t, true})
		}
		for _, t := range finish {
			evs = append(evs, ev{t, false})
		}
		sort.SliceStable(evs, func(i, j int) bool { return evs[i].t < evs[j].t })
		from := math.NaN()
		for _, e := range evs {
			if e.start {
				from = e.t
			} else if !math.IsNaN(from) {
				laps = append(laps, lap{e.t - from, "lap"})
				from = math.NaN()
			}
		}
	}
	// Without a start/finish line Lapline shows the whole log as one lap; that
	// isn't a lap time, so the list reports no laps for it.
	timed := false
	for _, l := range laps {
		if l.kind == "lap" {
			timed = true
		}
	}
	if timed {
		s.Laps = len(laps)
		best := math.Inf(1)
		for i, l := range laps {
			if l.kind == "lap" && l.ms < best {
				best, s.BestLapNo = l.ms, i+1
			}
		}
		s.BestLapMS = int64(math.Round(best))
	}

	// "File created on 29/07/2019 @ 10:23:45" (day/month/year, the writer's local time)
	if m := reCreated.FindStringSubmatch(strings.Join(sec["_pre"], " ")); m != nil {
		d, _ := strconv.Atoi(m[1])
		mo, _ := strconv.Atoi(m[2])
		s.Date = fmt.Sprintf("%02d/%02d/%s", d, mo, m[3])
		if m[4] != "" {
			h, _ := strconv.Atoi(m[4])
			mi, _ := strconv.Atoi(m[5])
			se, _ := strconv.Atoi(m[6])
			s.Hour = fmt.Sprintf("%02d:%02d:%02d", h, mi, se)
		}
	}

	note := func(re *regexp.Regexp) string {
		for _, ln := range sec["comments"] {
			if m := re.FindStringSubmatch(ln); m != nil {
				return strings.TrimSpace(m[1])
			}
		}
		return ""
	}
	s.Track, s.Vehicle, s.Driver = note(reTrack), note(reVehicle), note(reDriver)
	return s, nil
}

// crossU intersects segment p1-p2 with q1-q2 and returns the fraction along p1-p2, or -1.
func crossU(p1x, p1y, p2x, p2y, q1x, q1y, q2x, q2y float64) float64 {
	rx, ry, sx, sy := p2x-p1x, p2y-p1y, q2x-q1x, q2y-q1y
	den := rx*sy - ry*sx
	if den == 0 {
		return -1
	}
	u := ((q1x-p1x)*sy - (q1y-p1y)*sx) / den
	w := ((q1x-p1x)*ry - (q1y-p1y)*rx) / den
	if u >= 0 && u < 1 && w >= -0.1 && w <= 1.1 { // a little slack on the line ends
		return u
	}
	return -1
}

// findCrossings reads the [laptiming] start (and finish) lines and returns the
// times the GPS track crosses them. Writers disagree on lat/long order and the
// longitude sign, so every reading is tried and the one on the driven path wins.
func findCrossings(lt []string, T, lat, lon []float64) (start, finish []float64, mode string) {
	if len(T) == 0 {
		return nil, nil, "none"
	}
	lines := map[string][]float64{}
	for _, ln := range lt {
		m := reLine.FindStringSubmatch(ln)
		if m == nil {
			continue
		}
		var n []float64
		for _, f := range strings.Fields(m[2]) {
			v, err := strconv.ParseFloat(f, 64)
			if err != nil {
				v = math.NaN()
			}
			n = append(n, v)
		}
		if len(n) >= 4 && !anyNaN(n[:4]) {
			lines[strings.ToLower(m[1])] = n[:4]
		}
	}
	if lines["start"] == nil {
		return nil, nil, "none"
	}

	lat0, lon0 := lat[0], lon[0]
	if math.IsNaN(lat0) || math.IsNaN(lon0) {
		return nil, nil, "none"
	}
	kx, ky := 111320*math.Cos(lat0*math.Pi/180), 110540.0
	X, Y := make([]float64, len(T)), make([]float64, len(T))
	for k := range T {
		X[k], Y[k] = (lon[k]-lon0)*kx, (lat[k]-lat0)*ky
	}
	toXY := func(la, lo float64) (float64, float64) { return (lo - lon0) * kx, (la - lat0) * ky }

	type seg struct{ px, py, qx, qy float64 }
	readLine := func(n []float64) *seg {
		var best *seg
		bestD := math.Inf(1)
		for _, lonFirst := range []bool{true, false} {
			for _, sgn := range []float64{-1, 1} {
				a, b, c, d := n[0], n[1], n[2], n[3]
				la1, lo1, la2, lo2 := a/60, sgn*b/60, c/60, sgn*d/60
				if lonFirst {
					la1, lo1, la2, lo2 = b/60, sgn*a/60, d/60, sgn*c/60
				}
				px, py := toXY(la1, lo1)
				qx, qy := toXY(la2, lo2)
				mx, my := (px+qx)/2, (py+qy)/2
				dmin := math.Inf(1)
				for k := 0; k < len(X); k += 2 {
					if dd := math.Hypot(X[k]-mx, Y[k]-my); dd < dmin {
						dmin = dd
					}
				}
				if dmin < bestD {
					bestD, best = dmin, &seg{px, py, qx, qy}
				}
			}
		}
		if bestD < 200 {
			return best
		}
		return nil
	}
	crossings := func(L *seg) []float64 {
		var out []float64
		if L == nil {
			return out
		}
		for k := 1; k < len(T); k++ {
			if T[k]-T[k-1] > 2000 { // don't bridge GPS dropouts
				continue
			}
			u := crossU(X[k-1], Y[k-1], X[k], Y[k], L.px, L.py, L.qx, L.qy)
			if u < 0 {
				continue
			}
			t := T[k-1] + u*(T[k]-T[k-1])
			if len(out) > 0 && t-out[len(out)-1] < 5000 { // debounce wobble around the line
				continue
			}
			out = append(out, t)
		}
		return out
	}

	start = crossings(readLine(lines["start"]))
	if f := lines["finish"]; f != nil && !equal4(f, lines["start"]) {
		if fin := crossings(readLine(f)); len(fin) > 0 {
			return start, fin, "sprint"
		}
	}
	return start, nil, "circuit"
}

func anyNaN(v []float64) bool {
	for _, x := range v {
		if math.IsNaN(x) {
			return true
		}
	}
	return false
}

func equal4(a, b []float64) bool {
	for i := 0; i < 4; i++ {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
