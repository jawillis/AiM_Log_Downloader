// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOldManifestStillLoads(t *testing.T) {
	// A manifest written by version 0.1: none of the newer fields exist.
	old := `{"version":1,"entries":[{"name":"a_0001.xrz","id":1,"device_size":100,"date":"01/10/2026",
	"hour":"10:00:00","date_suspect":false,"laps":"3","track":"Hallett","file":"a_0001_Hallett.xrk",
	"bytes":500,"status":"ok","downloaded_at":"2026-10-01T10:00:00Z"}]}`
	path := filepath.Join(t.TempDir(), "m.json")
	os.WriteFile(path, []byte(old), 0o644)
	m, err := Open(path)
	if err != nil || m.Len() != 1 || !m.Has("a_0001.xrz", 100) {
		t.Fatalf("err=%v len=%d", err, m.Len())
	}
	if e := m.List()[0]; e.MetaVersion != 0 || e.BestLapMS != 0 || e.Track != "Hallett" {
		t.Fatalf("%+v", e)
	}
}

func TestUpdateSavesOnlyWhenSomethingChanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.json")
	m, _ := Open(path)
	m.Add(Entry{Name: "a_0001.xrz", DeviceSize: 1})
	m.Add(Entry{Name: "a_0002.xrz", DeviceSize: 2})
	before, _ := os.Stat(path)

	n, err := m.Update(func(e *Entry) bool { return false })
	if err != nil || n != 0 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if after, _ := os.Stat(path); !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("no-op update rewrote the file")
	}
	n, err = m.Update(func(e *Entry) bool {
		if e.Name == "a_0002.xrz" {
			e.BestLapMS, e.MetaVersion = 109549, 1
			return true
		}
		return false
	})
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	m2, _ := Open(path) // persisted
	if e := m2.List()[1]; e.BestLapMS != 109549 || e.MetaVersion != 1 {
		t.Fatalf("not persisted: %+v", e)
	}
}
