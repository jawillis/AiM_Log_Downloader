// SPDX-License-Identifier: GPL-3.0-or-later

package web

import (
	"math"
	"sort"

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

// enrich returns entries newest first, with track names filled in where the
// position makes it unambiguous, and the fastest lap per track marked.
func enrich(entries []store.Entry) []sessionView {
	sorted := append([]store.Entry(nil), entries...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].ID > sorted[j].ID })

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
