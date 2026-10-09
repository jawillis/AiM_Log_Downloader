// SPDX-License-Identifier: GPL-3.0-or-later

package imports

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"mychron-sync/internal/vbo/vbotest"
)

func write(t *testing.T, root, rel string, b []byte) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestIndexFindsVBOFiles(t *testing.T) {
	root := t.TempDir()
	oval := vbotest.Oval("Track: Test Oval", true)
	write(t, root, "RaceChrono/session 1.vbo", oval)
	write(t, root, "top.VBO", vbotest.Oval("", false))
	write(t, root, ".mychron-sync/hidden.vbo", oval)  // hidden folder: skipped
	write(t, root, "raw/a_0001.vbo", oval)            // add-on's raw folder: skipped
	write(t, root, "broken.vbo", []byte("not a vbo")) // unreadable: skipped
	write(t, root, "a_0775_RCQMA.xrk", []byte("xrk")) // not a .vbo

	x := New(root, nil)
	x.Refresh()
	got := x.List()
	if len(got) != 2 {
		t.Fatalf("got %d entries: %+v", len(got), got)
	}
	e := got[0]
	if e.File != "RaceChrono/session 1.vbo" || e.Name != "session 1.vbo" || e.Source != SourceVBO || e.Status != "ok" {
		t.Errorf("entry %+v", e)
	}
	if e.Track != "Test Oval" || e.Laps != "6" || e.BestLapIndex != 4 || e.BestLapMS < 71632 || e.BestLapMS > 71634 {
		t.Errorf("summary %+v", e)
	}
	if got[1].File != "top.VBO" || got[1].BestLapMS != 0 || got[1].Laps != "0" {
		t.Errorf("untimed entry %+v", got[1])
	}
	if !x.Has("RaceChrono/session 1.vbo") || x.Has("broken.vbo") || x.Has(".mychron-sync/hidden.vbo") {
		t.Error("Has is wrong")
	}

	// No change: same generation. New, changed or removed files: new generation.
	g := x.Generation()
	x.Refresh()
	if x.Generation() != g {
		t.Error("generation changed without any change on disk")
	}
	write(t, root, "new.vbo", oval)
	x.Refresh()
	if x.Generation() == g || len(x.List()) != 3 {
		t.Errorf("new file not picked up: %d entries", len(x.List()))
	}
	g = x.Generation()
	os.Remove(filepath.Join(root, "top.VBO"))
	x.Refresh()
	if x.Generation() == g || len(x.List()) != 2 {
		t.Errorf("removed file still listed: %d entries", len(x.List()))
	}
	g = x.Generation()
	p := filepath.Join(root, "new.vbo")
	os.WriteFile(p, vbotest.Oval("Track: Renamed", true), 0o644)
	os.Chtimes(p, time.Now().Add(time.Minute), time.Now().Add(time.Minute))
	x.Refresh()
	if x.Generation() == g {
		t.Error("changed file not noticed")
	}
	for _, e := range x.List() {
		if e.File == "new.vbo" && e.Track != "Renamed" {
			t.Errorf("changed file not re-read: %q", e.Track)
		}
	}
}

func TestIndexMissingFolder(t *testing.T) {
	x := New(filepath.Join(t.TempDir(), "nope"), nil)
	x.Refresh()
	if len(x.List()) != 0 {
		t.Error("expected nothing")
	}
}
