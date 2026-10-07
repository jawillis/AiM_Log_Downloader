// SPDX-License-Identifier: GPL-3.0-or-later

package aim

import (
	"math"
	"testing"
)

// Rows copied from a real Solo 2 DL (header and two of the rows it returned).
const realList = "name,size,date,hour,nlap,nbest,best,pilota,track_name,veicolo,campionato,venue_type,mode,trk_type,motivolap,maxvel,device,track_lat,track_lon,test_dur,pname,ptype,ptime,pdist,pmaxv,valid,\r\n" +
	"a_0775.xrz,2621422,06/10/2026,15:02:48,1,,,Jason Willis,RCQMA,25,,,speed,closed,stop,-2046820352,RatWag,303926421,-976695726,1076359,,,,,,,\r\n" +
	"a_0767.xrz,2933512,04/10/2026,07:45:46,4,2,109549,Jason Willis,Hallett,25,,,speed,closed,stop,-2046820352,RatWag,362206850,-965903800,1301512,,,,,,,\r\n"

func TestParseRealListRows(t *testing.T) {
	got, err := parseSessions([]byte(realList))
	if err != nil || len(got) != 2 {
		t.Fatalf("err=%v n=%d", err, len(got))
	}
	a := got[0] // a session with no lap time
	if a.BestMS != 0 || a.BestLap != 0 || a.Laps != "1" || a.DurationMS != 1076359 ||
		a.Driver != "Jason Willis" || a.Vehicle != "25" || a.Device != "RatWag" {
		t.Fatalf("a_0775: %+v", a)
	}
	if math.Abs(a.Lat-30.3926421) > 1e-9 || math.Abs(a.Lon+97.6695726) > 1e-9 {
		t.Fatalf("coordinates: %v, %v", a.Lat, a.Lon)
	}
	b := got[1] // a session with timed laps
	if b.BestMS != 109549 || b.BestLap != 2 || b.Laps != "4" || b.Track != "Hallett" {
		t.Fatalf("a_0767: %+v", b)
	}
	if b.Fields["maxvel"] != "-2046820352" || b.Fields["mode"] != "speed" {
		t.Fatalf("raw fields not kept: %v", b.Fields)
	}
}

func TestFormatLap(t *testing.T) {
	cases := map[int64]string{91234: "1:31.234", 109549: "1:49.549", 59123: "59.123", 60000: "1:00.000", 600001: "10:00.001", 0: "", -5: ""}
	for ms, want := range cases {
		if got := FormatLap(ms); got != want {
			t.Errorf("FormatLap(%d) = %q, want %q", ms, got, want)
		}
	}
}

func TestOddNumbersDontBreakParsing(t *testing.T) {
	list := "name,size,best,test_dur,track_lat,track_lon\r\na_0001.xrz,10,-1,abc,99999999999,\r\n"
	got, err := parseSessions([]byte(list))
	if err != nil || len(got) != 1 {
		t.Fatal(err)
	}
	if s := got[0]; s.BestMS != 0 || s.DurationMS != 0 || s.Lat != 0 || s.Lon != 0 {
		t.Fatalf("expected zeros for junk values: %+v", s)
	}
}
