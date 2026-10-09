// SPDX-License-Identifier: GPL-3.0-or-later

package web

import (
	"math"
	"sort"
	"time"

	"mychron-sync/internal/store"
)

// sessionView is what the page receives: the saved entry plus a few values
// worked out across all sessions.
type sessionView struct {
	store.Entry
	TrackShown    string `json:"track_shown"`    // the track name, or one guessed from position
	TrackInferred bool   `json:"track_inferred"` // true when TrackShown was guessed
	TrackBest     bool   `json:"track_best"`     // fastest lap saved for this track
}

// Sessions recorded within this distance of a named session are assumed to be
// at the same track when the logger left the name blank.
const sameTrackMetres = 1000

func hasPosition(e store.Entry) bool { return e.Lat != 0 || e.Lon != 0 }

// distMetres is an equirectangular approximation, plenty accurate at track scale.
func distMetres(aLat, aLon, bLat, bLon float64) float64 {
	dy := (aLat - bLat) * 110540
	dx := (aLon - bLon) * 111320 * math.Cos((aLat+bLat)/2*math.Pi/180)
	return math.Hypot(dx, dy)
}

// recordedAt parses the DD/MM/YYYY date and HH:MM:SS hour; false if unknown.
func recordedAt(e store.Entry) (time.Time, bool) {
	if e.Date == "" || e.DateSuspect {
		return time.Time{}, false
	}
	hour := e.Hour
	if len(hour) > 8 {
		hour = hour[:8]
	}
	if t, err := time.Parse("02/01/2006 15:04:05", e.Date+" "+hour); err == nil {
		return t, true
	}
	if t, err := time.Parse("02/01/2006", e.Date); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// newestFirst orders logger sessions by session number (the logger's clock is
// often unset, but its numbering is reliable) and merges imported files in by
// the time they were recorded. A logger session with no usable date counts as
// recorded when the nearest older dated one was, which keeps it in place.
func newestFirst(entries []store.Entry) []store.Entry {
	var logger, imported []store.Entry
	for _, e := range entries {
		if e.Source == "" {
			logger = append(logger, e)
		} else {
			imported = append(imported, e)
		}
	}
	sort.SliceStable(logger, func(i, j int) bool { return logger[i].ID > logger[j].ID })

	when := func(e store.Entry) time.Time {
		if t, ok := recordedAt(e); ok {
			return t
		}
		return e.DownloadedAt // for an imported file, its modification time
	}
	loggerAt := make([]time.Time, len(logger))
	var carry time.Time
	for i := len(logger) - 1; i >= 0; i-- {
		if t, ok := recordedAt(logger[i]); ok {
			carry = t
		}
		loggerAt[i] = carry
	}
	sort.SliceStable(imported, func(i, j int) bool { return when(imported[i]).After(when(imported[j])) })

	out := make([]store.Entry, 0, len(entries))
	i, j := 0, 0
	for i < len(logger) || j < len(imported) {
		if j < len(imported) && (i >= len(logger) || when(imported[j]).After(loggerAt[i])) {
			out = append(out, imported[j])
			j++
		} else {
			out = append(out, logger[i])
			i++
		}
	}
	return out
}

// enrich returns entries newest first, with track names filled in where the
// position makes it unambiguous, and the fastest lap per track marked.
func enrich(entries []store.Entry) []sessionView {
	sorted := newestFirst(entries)

	out := make([]sessionView, len(sorted))
	for i, e := range sorted {
		v := sessionView{Entry: e, TrackShown: e.Track}
		if e.Track == "" && hasPosition(e) {
			best, bestD := "", math.MaxFloat64
			for _, o := range sorted {
				if o.Track == "" || !hasPosition(o) {
					continue
				}
				if d := distMetres(e.Lat, e.Lon, o.Lat, o.Lon); d < bestD {
					best, bestD = o.Track, d
				}
			}
			if best != "" && bestD <= sameTrackMetres {
				v.TrackShown, v.TrackInferred = best, true
			}
		}
		out[i] = v
	}

	fastest := map[string]int64{}
	for _, v := range out {
		if v.BestLapMS > 0 && v.TrackShown != "" {
			if cur, ok := fastest[v.TrackShown]; !ok || v.BestLapMS < cur {
				fastest[v.TrackShown] = v.BestLapMS
			}
		}
	}
	for i := range out {
		v := &out[i]
		v.TrackBest = v.BestLapMS > 0 && v.TrackShown != "" && v.BestLapMS == fastest[v.TrackShown]
	}
	return out
}
