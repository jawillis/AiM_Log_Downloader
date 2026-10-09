// SPDX-License-Identifier: GPL-3.0-or-later

package vbo_test

import (
	"bytes"
	"math"
	"strings"
	"testing"

	"mychron-sync/internal/vbo"
	"mychron-sync/internal/vbo/vbotest"
)

func TestSummarizeMatchesLapline(t *testing.T) {
	s, err := vbo.Summarize(bytes.NewReader(vbotest.Oval("Track: Test Oval\r\nDriver: Jason", true)))
	if err != nil {
		t.Fatal(err)
	}
	// Expected values are what Lapline's js/vbo.js reports for the same file:
	// laps out 7.020, 71.714, 79.670, 71.633, 79.591, in 7.372; best is lap 4.
	if s.Laps != 6 || s.BestLapNo != 4 {
		t.Errorf("laps=%d best lap no=%d, want 6 and 4", s.Laps, s.BestLapNo)
	}
	if d := s.BestLapMS - 71633; d < -1 || d > 1 {
		t.Errorf("best lap %d ms, want 71633", s.BestLapMS)
	}
	if s.DurationMS != 317000 {
		t.Errorf("duration %d, want 317000", s.DurationMS)
	}
	if s.Date != "12/05/2026" || s.Hour != "14:03:07" {
		t.Errorf("date %q %q", s.Date, s.Hour)
	}
	if s.Track != "Test Oval" || s.Driver != "Jason" || s.Vehicle != "" {
		t.Errorf("track %q driver %q vehicle %q", s.Track, s.Driver, s.Vehicle)
	}
	if math.Abs(s.Lat-30.1328) > 0.01 || math.Abs(s.Lon+97.6411) > 0.01 {
		t.Errorf("position %v,%v", s.Lat, s.Lon)
	}
}

func TestSummarizeWithoutStartLine(t *testing.T) {
	s, err := vbo.Summarize(bytes.NewReader(vbotest.Oval("", false)))
	if err != nil {
		t.Fatal(err)
	}
	if s.Laps != 0 || s.BestLapMS != 0 {
		t.Errorf("laps=%d best=%d, want no timed laps", s.Laps, s.BestLapMS)
	}
	if s.DurationMS != 317000 || s.Track != "" {
		t.Errorf("duration %d track %q", s.DurationMS, s.Track)
	}
}

func TestSummarizeRejectsOtherFiles(t *testing.T) {
	for _, in := range []string{"", "hello\nworld\n", "[column names]\nsats time\n"} {
		if _, err := vbo.Summarize(strings.NewReader(in)); err == nil {
			t.Errorf("%q: expected an error", in)
		}
	}
}
