// SPDX-License-Identifier: GPL-3.0-or-later

package web

import (
	"testing"

	"mychron-sync/internal/store"
)

// Positions are the real ones from the logger.
var (
	rcqma  = [2]float64{30.3926421, -97.6695726}
	ranch  = [2]float64{32.5231780, -97.6151899}
	hallet = [2]float64{36.2206850, -96.5903800}
)

func ent(id int, track string, pos [2]float64, best int64) store.Entry {
	return store.Entry{Name: "a", ID: id, Track: track, Lat: pos[0], Lon: pos[1], BestLapMS: best}
}

func byID(vs []sessionView) map[int]sessionView {
	m := map[int]sessionView{}
	for _, v := range vs {
		m[v.ID] = v
	}
	return m
}

func TestNewestFirstAndTrackBest(t *testing.T) {
	vs := enrich([]store.Entry{
		ent(1, "Hallett", hallet, 112000),
		ent(2, "Hallett", hallet, 109549), // fastest Hallett lap
		ent(3, "Hallett", hallet, 0),      // no lap time: never a "best"
		ent(4, "RCQMA", rcqma, 61000),     // only timed lap at its track: a best
		ent(5, "Hallett", hallet, 109549), // ties count too
	})
	if vs[0].ID != 5 || vs[4].ID != 1 {
		t.Fatalf("not newest first: %v", []int{vs[0].ID, vs[1].ID, vs[2].ID, vs[3].ID, vs[4].ID})
	}
	m := byID(vs)
	want := map[int]bool{1: false, 2: true, 3: false, 4: true, 5: true}
	for id, w := range want {
		if m[id].TrackBest != w {
			t.Errorf("session %d TrackBest = %v, want %v", id, m[id].TrackBest, w)
		}
	}
}

func TestBlankTrackIsGuessedFromPosition(t *testing.T) {
	vs := enrich([]store.Entry{
		ent(1, "RCQMA", rcqma, 0),
		ent(2, "", [2]float64{rcqma[0] + 0.0005, rcqma[1]}, 70000), // about 55 m away
		ent(3, "Ranch Long", ranch, 0),
		ent(4, "", [2]float64{40.0, -100.0}, 0), // nowhere near anything we know
		ent(5, "", [2]float64{}, 0),             // no position at all
	})
	m := byID(vs)
	if v := m[2]; v.TrackShown != "RCQMA" || !v.TrackInferred {
		t.Fatalf("near RCQMA: %+v", v)
	}
	if v := m[1]; v.TrackShown != "RCQMA" || v.TrackInferred {
		t.Fatalf("a named track must not be marked as guessed: %+v", v)
	}
	if v := m[4]; v.TrackShown != "" || v.TrackInferred {
		t.Fatalf("far away should stay blank: %+v", v)
	}
	if v := m[5]; v.TrackShown != "" {
		t.Fatalf("no position should stay blank: %+v", v)
	}
	if !m[2].TrackBest {
		t.Fatal("a guessed track should still count for track best")
	}
}

func TestDistanceSanity(t *testing.T) {
	if d := distMetres(rcqma[0], rcqma[1], rcqma[0], rcqma[1]); d != 0 {
		t.Fatal(d)
	}
	// 0.001 degrees of latitude is about 110 m.
	if d := distMetres(30.0, -97.0, 30.001, -97.0); d < 105 || d > 115 {
		t.Fatalf("got %.1f m", d)
	}
	// The three real tracks are hundreds of km apart.
	if d := distMetres(rcqma[0], rcqma[1], hallet[0], hallet[1]); d < 500_000 {
		t.Fatalf("got %.0f m", d)
	}
}
