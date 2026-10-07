// SPDX-License-Identifier: GPL-3.0-or-later

package web

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"mychron-sync/internal/engine"
	"mychron-sync/internal/store"
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
