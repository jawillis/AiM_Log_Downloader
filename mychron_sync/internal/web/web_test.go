// SPDX-License-Identifier: GPL-3.0-or-later

package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mychron-sync/internal/engine"
	"mychron-sync/internal/imports"
	"mychron-sync/internal/store"
	"mychron-sync/internal/vbo/vbotest"
)

func newHandler(t *testing.T, allowAny bool) http.Handler {
	t.Helper()
	dir := t.TempDir()
	man, err := store.Open(filepath.Join(dir, "m.json"))
	if err != nil {
		t.Fatal(err)
	}
	return New(Deps{Engine: engine.New(engine.Config{OutputDir: dir}, man), Manifest: man, Version: "test", AllowAny: allowAny})
}

func TestOnlyIngressGatewayAndLoopbackAreTrusted(t *testing.T) {
	h := newHandler(t, false)
	cases := map[string]int{
		"172.30.32.2:41000": 200, // Home Assistant ingress
		"127.0.0.1:5000":    200,
		"[::1]:5000":        200,
		"192.168.1.20:5000": 403, // someone else on the LAN
		"172.30.32.3:5000":  403, // another add-on
		"garbage":           403,
	}
	for addr, want := range cases {
		for _, path := range []string{"/", "/healthz", "/api/status"} {
			req := httptest.NewRequest("GET", path, nil)
			req.RemoteAddr = addr
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != want {
				t.Errorf("%s %s = %d, want %d", addr, path, rec.Code, want)
			}
		}
	}
	// Mutating endpoints are guarded too.
	req := httptest.NewRequest("POST", "/api/pause", nil)
	req.RemoteAddr = "192.168.1.20:5000"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Errorf("POST from LAN = %d, want 403", rec.Code)
	}
}

func TestAllowAnyForDevelopment(t *testing.T) {
	req := httptest.NewRequest("GET", "/healthz", nil)
	req.RemoteAddr = "192.168.1.20:5000"
	rec := httptest.NewRecorder()
	newHandler(t, true).ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("got %d", rec.Code)
	}
}

// ---- saved-session downloads and the Lapline viewer ----

func get(h http.Handler, path, remote string, hdr ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", path, nil)
	req.RemoteAddr = remote
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

const ha = "172.30.32.2:40000"

func sessionsFixture(t *testing.T, laplineDir string) (http.Handler, string) {
	t.Helper()
	dir := t.TempDir()
	man, err := store.Open(filepath.Join(dir, ".mychron-sync", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "a_0001_Hallett.xrk"), []byte("0123456789"), 0o644)
	os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("not a session"), 0o644)
	man.Add(store.Entry{Name: "a_0001.xrz", File: "a_0001_Hallett.xrk", Status: store.StatusOK})
	man.Add(store.Entry{Name: "a_0002.xrz", File: "gone.xrk", Status: store.StatusOK}) // listed, but the file is missing
	h := New(Deps{Engine: engine.New(engine.Config{OutputDir: dir}, man), Manifest: man, Version: "t", FilesDir: dir, LaplineDir: laplineDir})
	return h, dir
}

func TestFilesServeOnlyWhatTheManifestLists(t *testing.T) {
	h, _ := sessionsFixture(t, "")
	if r := get(h, "/files/a_0001_Hallett.xrk", ha); r.Code != 200 || r.Body.String() != "0123456789" {
		t.Fatalf("listed file: %d %q", r.Code, r.Body.String())
	}
	if r := get(h, "/files/a_0001_Hallett.xrk?download=1", ha); r.Header().Get("Content-Disposition") != "attachment; filename=a_0001_Hallett.xrk" {
		t.Fatalf("download header: %q", r.Header().Get("Content-Disposition"))
	}
	if r := get(h, "/files/a_0001_Hallett.xrk", ha, "Range", "bytes=2-4"); r.Code != 206 || r.Body.String() != "234" {
		t.Fatalf("range: %d %q", r.Code, r.Body.String())
	}
	for _, p := range []string{
		"/files/secret.txt",      // on the share, not in the manifest
		"/files/manifest.json",   // not a session
		"/files/gone.xrk",        // listed but missing on disk
		"/files/..%2Fsecret.txt", // traversal
		"/files/%2e%2e%2f%2e%2e%2fetc%2fpasswd",
		"/files/.mychron-sync", // hidden state folder
		"/files/",
	} {
		if r := get(h, p, ha); r.Code != 404 {
			t.Errorf("%s = %d, want 404", p, r.Code)
		}
	}
	if r := get(h, "/files/a_0001_Hallett.xrk", "192.168.1.20:5000"); r.Code != 403 {
		t.Errorf("from the LAN = %d, want 403", r.Code)
	}
}

func fakeLapline(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	os.MkdirAll(filepath.Join(d, "css"), 0o755)
	os.MkdirAll(filepath.Join(d, "js"), 0o755)
	os.WriteFile(filepath.Join(d, "index.html"), []byte("<html><body><div>app</div>\n<script src=\"js/app.js\"></script>\n</body></html>"), 0o644)
	os.WriteFile(filepath.Join(d, "css", "app.css"), []byte("body{}"), 0o644)
	os.WriteFile(filepath.Join(d, "js", "app.js"), []byte("1"), 0o644)
	os.WriteFile(filepath.Join(d, "README.md"), []byte("readme"), 0o644)
	return d
}

func TestLaplineIsServedWithTheHelperAdded(t *testing.T) {
	h, _ := sessionsFixture(t, fakeLapline(t))
	r := get(h, "/lapline/", ha)
	body := r.Body.String()
	i, j := strings.Index(body, `<script src="lapline-open.js"></script>`), strings.LastIndex(body, "</body>")
	if r.Code != 200 || i < 0 || i > j || strings.Count(body, "lapline-open.js") != 1 {
		t.Fatalf("index: %d helper at %d, </body> at %d\n%s", r.Code, i, j, body)
	}
	if r := get(h, "/lapline", ha); r.Code != 302 || r.Header().Get("Location") != "lapline/" {
		t.Fatalf("redirect must be relative to survive the ingress prefix: %d %q", r.Code, r.Header().Get("Location"))
	}
	if r := get(h, "/lapline/css/app.css", ha); r.Code != 200 || r.Body.String() != "body{}" {
		t.Fatalf("css: %d", r.Code)
	}
	if r := get(h, "/lapline/lapline-open.js", ha); r.Code != 200 || !strings.Contains(r.Header().Get("Content-Type"), "javascript") || !strings.Contains(r.Body.String(), "App.loadBuffer") {
		t.Fatalf("helper script: %d %q", r.Code, r.Header().Get("Content-Type"))
	}
	if r := get(h, "/lapline/js/", ha); r.Code != 404 {
		t.Errorf("directory listing = %d, want 404", r.Code)
	}
	if r := get(h, "/lapline/", "192.168.1.20:5000"); r.Code != 403 {
		t.Errorf("from the LAN = %d, want 403", r.Code)
	}
	st := get(h, "/api/status", ha).Body.String()
	if !strings.Contains(st, `"lapline":true`) {
		t.Fatalf("status should advertise the viewer: %s", st)
	}
}

func TestLaplineAbsentIsHandledQuietly(t *testing.T) {
	for _, dir := range []string{"", filepath.Join(t.TempDir(), "nope")} {
		h, _ := sessionsFixture(t, dir)
		if r := get(h, "/lapline/", ha); r.Code != 404 {
			t.Errorf("dir %q: /lapline/ = %d, want 404", dir, r.Code)
		}
		if st := get(h, "/api/status", ha).Body.String(); !strings.Contains(st, `"lapline":false`) {
			t.Errorf("dir %q: status %s", dir, st)
		}
	}
}

func TestImportedVBOFilesAreListedAndServed(t *testing.T) {
	dir := t.TempDir()
	man, err := store.Open(filepath.Join(dir, ".mychron-sync", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "a_0001_Hallett.xrk"), []byte("xrk"), 0o644)
	man.Add(store.Entry{Name: "a_0001.xrz", ID: 1, File: "a_0001_Hallett.xrk", Status: store.StatusOK})
	os.MkdirAll(filepath.Join(dir, "RaceChrono"), 0o755)
	oval := vbotest.Oval("Track: Test Oval", true)
	os.WriteFile(filepath.Join(dir, "RaceChrono", "Oval run #1.vbo"), oval, 0o644)
	os.WriteFile(filepath.Join(dir, "RaceChrono", "notes.txt"), []byte("private"), 0o644)
	idx := imports.New(dir, nil)
	idx.Refresh()
	h := New(Deps{Engine: engine.New(engine.Config{OutputDir: dir}, man), Manifest: man, Version: "t", FilesDir: dir, Imports: idx})

	body := get(h, "/api/sessions", ha).Body.String()
	for _, want := range []string{`"file":"RaceChrono/Oval run #1.vbo"`, `"source":"vbo"`, `"track_shown":"Test Oval"`, `"file":"a_0001_Hallett.xrk"`} {
		if !strings.Contains(body, want) {
			t.Errorf("sessions missing %s:\n%s", want, body)
		}
	}
	if st := get(h, "/api/status", ha).Body.String(); !strings.Contains(st, `"imports":1`) {
		t.Errorf("status should report the imports generation: %s", st)
	}

	// The page encodes each path segment; both that and an encoded slash work.
	for _, p := range []string{"/files/RaceChrono/Oval%20run%20%231.vbo", "/files/RaceChrono%2FOval%20run%20%231.vbo"} {
		r := get(h, p, ha)
		if r.Code != 200 || r.Body.Len() != len(oval) {
			t.Errorf("%s = %d (%d bytes)", p, r.Code, r.Body.Len())
		}
	}
	r := get(h, "/files/RaceChrono/Oval%20run%20%231.vbo?download=1", ha)
	if cd := r.Header().Get("Content-Disposition"); cd != `attachment; filename="Oval run #1.vbo"` {
		t.Errorf("download header %q", cd)
	}
	for _, p := range []string{
		"/files/RaceChrono/notes.txt", // next to an imported file, but not a session
		"/files/RaceChrono/../a_0001_Hallett.xrk",
		"/files/RaceChrono/..%2F..%2Fetc%2Fpasswd",
		"/files/RaceChrono/",
		"/files/.mychron-sync/manifest.json",
	} {
		if r := get(h, p, ha); r.Code != 404 && r.Code != 301 {
			t.Errorf("%s = %d, want 404", p, r.Code)
		}
	}
}
