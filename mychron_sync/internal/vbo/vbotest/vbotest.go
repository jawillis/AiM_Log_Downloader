// SPDX-License-Identifier: GPL-3.0-or-later

// Package vbotest makes synthetic .vbo files for tests.
package vbotest

import (
	"bytes"
	"fmt"
	"math"
)

// Oval writes a synthetic RaceChrono-style .vbo: 10 Hz laps of a 300 m
// circle with the start/finish line on its east side, an out lap, four timed
// laps of alternating pace and an in lap. comments is added to [comments].
// laptiming false leaves the [laptiming] section out.
func Oval(comments string, laptiming bool) []byte {
	lat0, lon0, R := 30.1328, -97.6411, 300.0
	ky, kx := 110540.0, 111320.0*math.Cos(lat0*math.Pi/180)
	ll := func(x, y float64) (float64, float64) { return lat0 + y/ky, lon0 + x/kx }
	var b bytes.Buffer
	b.WriteString("File created on 12/05/2026 @ 14:03:07\r\n\r\n[header]\r\nsatellites\r\ntime\r\nlatitude\r\nlongitude\r\nvelocity kmh\r\nheading\r\nheight\r\n\r\n")
	b.WriteString("[comments]\r\nExported by test\r\n")
	if comments != "" {
		b.WriteString(comments + "\r\n")
	}
	if laptiming {
		a1, o1 := ll(R-15, 0)
		a2, o2 := ll(R+15, 0)
		fmt.Fprintf(&b, "[laptiming]\r\nStart   %+012.5f %+012.5f %+012.5f %+012.5f \xac Start / Finish\r\n\r\n", -o1*60, a1*60, -o2*60, a2*60)
	}
	b.WriteString("[column names]\r\nsats time lat long velocity heading height\r\n\r\n[data]\r\n")
	t, ang, dt := 14*3600+3*60+10.0, -0.6, 0.1
	for {
		lapn := (ang + 0.6) / (2 * math.Pi)
		v := 25 + 2*math.Sin(lapn*3)
		ang += v * dt / R
		la, lo := ll(R*math.Cos(ang), R*math.Sin(ang))
		hh, mm, ss := int(t/3600), int(math.Mod(t, 3600)/60), math.Mod(t, 60)
		fmt.Fprintf(&b, "009 %02d%02d%05.2f %+012.5f %+012.5f %07.3f %06.2f %+09.2f\r\n", hh, mm, ss, la*60, -lo*60, v*3.6, 0.0, 150.0)
		t += dt
		if ang > 0.6+2*math.Pi*4 {
			break
		}
	}
	return b.Bytes()
}
