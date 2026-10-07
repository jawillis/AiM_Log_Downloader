// SPDX-License-Identifier: GPL-3.0-or-later

package ha

import (
	"reflect"
	"testing"

	"mychron-sync/internal/store"
)

func TestEventDataWithLapTimes(t *testing.T) {
	d := eventData([]store.Entry{
		{Name: "a_0767.xrz", Track: "Hallett", BestLapMS: 109549},
		{Name: "a_0768.xrz", Track: "Hallett", BestLapMS: 111000},
		{Name: "a_0769.xrz", Track: "Ranch Long"}, // no lap time
	})
	if d["count"] != 3 || !reflect.DeepEqual(d["tracks"], []string{"Hallett", "Ranch Long"}) {
		t.Fatalf("%v", d)
	}
	if d["best_lap"] != "1:49.549" || d["best_lap_track"] != "Hallett" || d["best_lap_session"] != "a_0767.xrz" || d["best_lap_ms"] != int64(109549) {
		t.Fatalf("%v", d)
	}
}

func TestEventDataWithoutLapTimesOmitsBestLap(t *testing.T) {
	d := eventData([]store.Entry{{Name: "a_0775.xrz", Track: "RCQMA"}, {Name: "a_0774.xrz"}})
	if _, ok := d["best_lap"]; ok {
		t.Fatalf("best_lap should be absent: %v", d)
	}
	if d["count"] != 2 || !reflect.DeepEqual(d["tracks"], []string{"RCQMA"}) {
		t.Fatalf("%v", d)
	}
}
