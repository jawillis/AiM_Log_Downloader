// SPDX-License-Identifier: GPL-3.0-or-later

package engine

import (
	"bytes"
	"compress/zlib"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mychron-sync/internal/aim"
	"mychron-sync/internal/store"
)

type fakeDev struct {
	sessions []aim.Session
	files    map[string][]byte
	reads    []string // order of ReadFile calls
	failOn   string   // ReadFile of this path returns failErr
	failErr  error
}

func (d *fakeDev) ListSessions() ([]aim.Session, error) { return d.sessions, nil }
func (d *fakeDev) Close() error                         { return nil }
func (d *fakeDev) ReadFile(p string, prog func(int, int)) ([]byte, error) {
	d.reads = append(d.reads, p)
	if p == d.failOn {
		return nil, d.failErr
	}
	b, ok := d.files[p]
	if !ok {
		return nil, errors.New("no such file")
	}
	if prog != nil {
		prog(len(b), len(b))
	}
	return b, nil
}

func xrz(payload string) []byte {
	var b bytes.Buffer
	b.WriteString("hdr")
	w := zlib.NewWriter(&b)
	w.Write([]byte("<hCNF" + payload))
	w.Close()
	return b.Bytes()
}

func sess(name string, id int, ext string, size int64, date, track string) aim.Session {
	return aim.Session{Name: name, ID: id, Ext: ext, Size: size, Date: date, Hour: "10:00:00", Laps: "3", Track: track}
}

func newTestEngine(t *testing.T, dev *fakeDev) (*Engine, *store.Manifest, string) {
	t.Helper()
	dir := t.TempDir()
	man, err := store.Open(filepath.Join(dir, ".mychron-sync", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	e := New(Config{
		Host: "test", OutputDir: dir,
		Dial: func(context.Context, string) (Device, error) { return dev, nil },
	}, man)
	return e, man, dir
}

func TestFirstRunDownloadsEverythingNewestFirst(t *testing.T) {
	a, b, c := xrz("a"), xrz("b"), xrz("c")
	dev := &fakeDev{
		sessions: []aim.Session{
			sess("a_0694.hrz", 694, "hrz", int64(len(a)), "29/11/2015", "Ranch Long"),
			sess("a_0775.xrz", 775, "xrz", int64(len(b)), "06/10/2026", "RCQMA"),
			sess("a_0729.xrz", 729, "xrz", int64(len(c)), "31/08/2026", ""),
		},
		files: map[string][]byte{"1:/mem/a_0694.hrz": a, "1:/mem/a_0775.xrz": b, "1:/mem/a_0729.xrz": c},
	}
	e, man, dir := newTestEngine(t, dev)
	done, err := e.syncOnce(context.Background())
	if err != nil || len(done) != 3 {
		t.Fatalf("done=%d err=%v", len(done), err)
	}
	want := []string{"1:/mem/a_0775.xrz", "1:/mem/a_0729.xrz", "1:/mem/a_0694.hrz"}
	for i, p := range want {
		if dev.reads[i] != p {
			t.Fatalf("read order %v, want %v", dev.reads, want)
		}
	}
	for _, f := range []string{"a_0775_RCQMA.xrk", "a_0729.xrk", "a_0694_Ranch-Long.xrk"} {
		if b, err := os.ReadFile(filepath.Join(dir, f)); err != nil || !bytes.HasPrefix(b, []byte("<hCNF")) {
			t.Fatalf("%s: err=%v", f, err)
		}
	}
	got := map[string]store.Entry{}
	for _, en := range man.List() {
		got[en.Name] = en
	}
	if !got["a_0694.hrz"].DateSuspect || got["a_0775.xrz"].DateSuspect {
		t.Fatalf("date_suspect wrong: %+v", got)
	}
	// Nothing is left behind except the files and the manifest.
	if tmp, _ := filepath.Glob(filepath.Join(dir, ".tmp-*")); len(tmp) != 0 {
		t.Fatalf("temp files left: %v", tmp)
	}
}

func TestSecondRunOnlyFetchesNewSessions(t *testing.T) {
	a := xrz("a")
	dev := &fakeDev{
		sessions: []aim.Session{sess("a_0001.xrz", 1, "xrz", int64(len(a)), "01/10/2026", "T")},
		files:    map[string][]byte{"1:/mem/a_0001.xrz": a},
	}
	e, man, _ := newTestEngine(t, dev)
	if _, err := e.syncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	dev.reads = nil
	if done, err := e.syncOnce(context.Background()); err != nil || len(done) != 0 || len(dev.reads) != 0 {
		t.Fatalf("expected no-op, got done=%d reads=%v err=%v", len(done), dev.reads, err)
	}
	n := xrz("new")
	dev.sessions = append(dev.sessions, sess("a_0002.xrz", 2, "xrz", int64(len(n)), "02/10/2026", "T"))
	dev.files["1:/mem/a_0002.xrz"] = n
	done, err := e.syncOnce(context.Background())
	if err != nil || len(done) != 1 || done[0].Name != "a_0002.xrz" || man.Len() != 2 {
		t.Fatalf("done=%v err=%v len=%d", done, err, man.Len())
	}
}

func TestManifestSurvivesRestart(t *testing.T) {
	a := xrz("a")
	dev := &fakeDev{
		sessions: []aim.Session{sess("a_0001.xrz", 1, "xrz", int64(len(a)), "01/10/2026", "T")},
		files:    map[string][]byte{"1:/mem/a_0001.xrz": a},
	}
	e, _, dir := newTestEngine(t, dev)
	e.syncOnce(context.Background())
	man2, err := store.Open(filepath.Join(dir, ".mychron-sync", "manifest.json"))
	if err != nil || !man2.Has("a_0001.xrz", int64(len(a))) {
		t.Fatalf("reopened manifest lost the entry: %v", err)
	}
}

func TestUninflatablePayloadIsKeptAsReceived(t *testing.T) {
	junk := []byte("this is not a zlib stream at all")
	dev := &fakeDev{
		sessions: []aim.Session{sess("a_0700.hrz", 700, "hrz", int64(len(junk)), "01/10/2026", "T")},
		files:    map[string][]byte{"1:/mem/a_0700.hrz": junk},
	}
	e, man, dir := newTestEngine(t, dev)
	done, err := e.syncOnce(context.Background())
	if err != nil || len(done) != 1 {
		t.Fatalf("done=%d err=%v", len(done), err)
	}
	if done[0].Status != store.StatusRawOnly || done[0].File != "a_0700_T.hrz" {
		t.Fatalf("entry: %+v", done[0])
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "a_0700_T.hrz")); !bytes.Equal(b, junk) {
		t.Fatal("raw bytes not preserved")
	}
	if !man.Has("a_0700.hrz", int64(len(junk))) {
		t.Fatal("raw-only sessions must still count as downloaded")
	}
}

func TestSameNameDifferentSizeIsKeptAsNewVersion(t *testing.T) {
	v1, v2 := xrz("one"), xrz("two-longer")
	dev := &fakeDev{
		sessions: []aim.Session{sess("a_0001.xrz", 1, "xrz", int64(len(v1)), "01/10/2026", "T")},
		files:    map[string][]byte{"1:/mem/a_0001.xrz": v1},
	}
	e, _, dir := newTestEngine(t, dev)
	e.syncOnce(context.Background())
	// e.g. the logger's memory was cleared and the counter restarted
	dev.sessions = []aim.Session{sess("a_0001.xrz", 1, "xrz", int64(len(v2)), "09/10/2026", "T")}
	dev.files["1:/mem/a_0001.xrz"] = v2
	if _, err := e.syncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"a_0001_T.xrk", "a_0001_T_v2.xrk"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatalf("missing %s", f)
		}
	}
}

func TestHangKeepsProgressAndResumes(t *testing.T) {
	a, b, c := xrz("a"), xrz("b"), xrz("c")
	dev := &fakeDev{
		sessions: []aim.Session{
			sess("a_0003.xrz", 3, "xrz", int64(len(a)), "03/10/2026", "T"),
			sess("a_0002.xrz", 2, "xrz", int64(len(b)), "02/10/2026", "T"),
			sess("a_0001.xrz", 1, "xrz", int64(len(c)), "01/10/2026", "T"),
		},
		files:   map[string][]byte{"1:/mem/a_0003.xrz": a, "1:/mem/a_0002.xrz": b, "1:/mem/a_0001.xrz": c},
		failOn:  "1:/mem/a_0002.xrz",
		failErr: aim.ErrHung,
	}
	e, man, _ := newTestEngine(t, dev)
	done, err := e.syncOnce(context.Background())
	if !errors.Is(err, aim.ErrHung) || len(done) != 1 || man.Len() != 1 {
		t.Fatalf("done=%d len=%d err=%v", len(done), man.Len(), err)
	}
	dev.failOn = ""
	dev.reads = nil
	done, err = e.syncOnce(context.Background())
	if err != nil || len(done) != 2 || man.Len() != 3 {
		t.Fatalf("resume: done=%d len=%d err=%v", len(done), man.Len(), err)
	}
	if len(dev.reads) != 2 || dev.reads[0] != "1:/mem/a_0002.xrz" {
		t.Fatalf("should not re-download a_0003: %v", dev.reads)
	}
}

func TestOneCorruptFileDoesNotBlockTheRest(t *testing.T) {
	a, b := xrz("a"), xrz("b")
	dev := &fakeDev{
		sessions: []aim.Session{
			sess("a_0002.xrz", 2, "xrz", int64(len(a)), "02/10/2026", "T"),
			sess("a_0001.xrz", 1, "xrz", int64(len(b)), "01/10/2026", "T"),
		},
		files:   map[string][]byte{"1:/mem/a_0002.xrz": a, "1:/mem/a_0001.xrz": b},
		failOn:  "1:/mem/a_0002.xrz",
		failErr: aim.ErrProtocol,
	}
	e, man, _ := newTestEngine(t, dev)
	done, err := e.syncOnce(context.Background())
	if err == nil || len(done) != 1 || done[0].Name != "a_0001.xrz" || man.Has("a_0002.xrz", int64(len(a))) {
		t.Fatalf("done=%v err=%v", done, err)
	}
}

func TestPauseStopsDownloads(t *testing.T) {
	a := xrz("a")
	dev := &fakeDev{
		sessions: []aim.Session{sess("a_0001.xrz", 1, "xrz", int64(len(a)), "01/10/2026", "T")},
		files:    map[string][]byte{"1:/mem/a_0001.xrz": a},
	}
	e, man, _ := newTestEngine(t, dev)
	e.SetPaused(true)
	if done, err := e.syncOnce(context.Background()); err != nil || len(done) != 0 || man.Len() != 0 {
		t.Fatalf("paused sync should do nothing: %v %v", done, err)
	}
	if e.Status().State != StatePaused {
		t.Fatalf("state = %s", e.Status().State)
	}
}

func TestRunLoopSyncsWhenLoggerAppearsAndStaysQuietWhenGone(t *testing.T) {
	a := xrz("a")
	dev := &fakeDev{
		sessions: []aim.Session{sess("a_0001.xrz", 1, "xrz", int64(len(a)), "01/10/2026", "T")},
		files:    map[string][]byte{"1:/mem/a_0001.xrz": a},
	}
	e, man, _ := newTestEngine(t, dev)
	online := make(chan bool, 1)
	online <- false
	cur := false
	e.cfg.PollInterval = 20 * time.Millisecond
	e.cfg.Settle = 10 * time.Millisecond
	e.cfg.ProbeFn = func(context.Context, string) bool {
		select {
		case v := <-online:
			cur = v
		default:
		}
		return cur
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	time.Sleep(100 * time.Millisecond)
	if man.Len() != 0 || e.Status().State != StateOffline {
		t.Fatalf("should be offline and idle: len=%d state=%s", man.Len(), e.Status().State)
	}
	online <- true
	deadline := time.Now().Add(2 * time.Second)
	for man.Len() != 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if man.Len() != 1 {
		t.Fatalf("session was not downloaded after the logger appeared (state=%s msg=%q)", e.Status().State, e.Status().Message)
	}
	time.Sleep(60 * time.Millisecond)
	if s := e.Status(); s.State != StateIdle || s.LastNew != 1 || s.Total != 1 {
		t.Fatalf("status: %+v", s)
	}
}

func TestFileNames(t *testing.T) {
	cases := []struct {
		s    aim.Session
		v    int
		want string
	}{
		{aim.Session{Name: "a_0775.xrz", Track: "RCQMA"}, 1, "a_0775_RCQMA"},
		{aim.Session{Name: "a_0772.xrz", Track: "Ranch Long"}, 1, "a_0772_Ranch-Long"},
		{aim.Session{Name: "a_0729.xrz", Track: ""}, 1, "a_0729"},
		{aim.Session{Name: "a_0001.xrz", Track: "../../etc/passwd"}, 2, "a_0001_etc-passwd_v2"},
	}
	for _, c := range cases {
		if got := fileStem(c.s, c.v); got != c.want {
			t.Errorf("fileStem(%+v, %d) = %q, want %q", c.s, c.v, got, c.want)
		}
	}
}

func TestBackoff(t *testing.T) {
	e := New(Config{BackoffBase: 30 * time.Second, BackoffMax: 10 * time.Minute, OutputDir: t.TempDir()}, mustOpen(t))
	want := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 10 * time.Minute, 10 * time.Minute}
	for i, w := range want {
		if got := e.backoff(i + 1); got != w {
			t.Errorf("backoff(%d) = %s, want %s", i+1, got, w)
		}
	}
}

func mustOpen(t *testing.T) *store.Manifest {
	m, err := store.Open(filepath.Join(t.TempDir(), "m.json"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestNewDownloadsKeepAllListColumns(t *testing.T) {
	a := xrz("a")
	ss := sess("a_0767.xrz", 767, "xrz", int64(len(a)), "04/10/2026", "Hallett")
	ss.Driver, ss.Vehicle, ss.Device = "Jason Willis", "25", "RatWag"
	ss.BestMS, ss.BestLap, ss.DurationMS, ss.Lat, ss.Lon = 109549, 2, 1301512, 36.220685, -96.59038
	ss.Fields = map[string]string{"name": ss.Name, "best": "109549", "maxvel": "-2046820352", "mode": "speed", "venue_type": ""}
	dev := &fakeDev{sessions: []aim.Session{ss}, files: map[string][]byte{"1:/mem/a_0767.xrz": a}}
	e, man, _ := newTestEngine(t, dev)
	if _, err := e.syncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := man.List()[0]
	if got.BestLapMS != 109549 || got.BestLapIndex != 2 || got.DurationMS != 1301512 || got.Driver != "Jason Willis" ||
		got.Vehicle != "25" || got.Lat != 36.220685 || got.MetaVersion != metaVersion {
		t.Fatalf("%+v", got)
	}
	if got.Extra["maxvel"] != "-2046820352" || got.Extra["mode"] != "speed" || len(got.Extra) != 2 {
		t.Fatalf("extra should hold only the unmapped, non-empty columns: %v", got.Extra)
	}
}

func TestEarlierVersionsSessionsAreFilledInWithoutRedownloading(t *testing.T) {
	a := xrz("a")
	dev := &fakeDev{files: map[string][]byte{"1:/mem/a_0767.xrz": a}}
	e, man, _ := newTestEngine(t, dev)
	// Saved by the previous version: no list columns, MetaVersion 0.
	man.Add(store.Entry{Name: "a_0767.xrz", ID: 767, DeviceSize: int64(len(a)), Track: "Hallett", File: "a_0767_Hallett.xrk", Status: store.StatusOK})

	ss := sess("a_0767.xrz", 767, "xrz", int64(len(a)), "04/10/2026", "Hallett")
	ss.BestMS, ss.DurationMS = 109549, 1301512
	dev.sessions = []aim.Session{ss}

	done, err := e.syncOnce(context.Background())
	if err != nil || len(done) != 0 || len(dev.reads) != 0 {
		t.Fatalf("must not download again: done=%d reads=%v err=%v", len(done), dev.reads, err)
	}
	got := man.List()[0]
	if got.BestLapMS != 109549 || got.DurationMS != 1301512 || got.MetaVersion != metaVersion {
		t.Fatalf("not filled in: %+v", got)
	}
	if got.File != "a_0767_Hallett.xrk" || got.Status != store.StatusOK {
		t.Fatalf("must not touch the saved file details: %+v", got)
	}
	// And the second pass changes nothing.
	if n, _ := man.Update(func(en *store.Entry) bool { return false }); n != 0 {
		t.Fatal("unexpected change")
	}
}
