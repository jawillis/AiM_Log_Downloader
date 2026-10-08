// SPDX-License-Identifier: GPL-3.0-or-later

// Package web serves the status page and its small JSON API.
//
// The page is designed for Home Assistant ingress: every URL it uses is
// relative, and (because the add-on runs on the host network) requests are only
// accepted from the Supervisor's ingress gateway unless AllowAny is set.
package web

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"mychron-sync/internal/engine"
	"mychron-sync/internal/store"
)

//go:embed static/index.html
var indexHTML []byte

//go:embed static/lapline-open.js
var laplineOpenJS []byte

// ingressGateway is the address Home Assistant's ingress proxy connects from.
const ingressGateway = "172.30.32.2"

// Deps are the server's collaborators.
type Deps struct {
	Engine   *engine.Engine
	Manifest *store.Manifest
	Version  string
	// AllowAny disables the ingress-gateway check. Only for local development.
	AllowAny bool
	// FilesDir is where saved sessions live. Only files listed in the
	// manifest are served, read-only, at /files/<name>.
	FilesDir string
	// LaplineDir is a checkout of the Lapline viewer. It is served at /lapline/
	// when it contains an index.html; empty or missing disables it.
	LaplineDir string
}

func (d Deps) laplineEnabled() bool {
	if d.LaplineDir == "" {
		return false
	}
	st, err := os.Stat(filepath.Join(d.LaplineDir, "index.html"))
	return err == nil && !st.IsDir()
}

// New returns the HTTP handler.
func New(d Deps) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(indexHTML)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"version": d.Version, "status": d.Engine.Status(), "lapline": d.laplineEnabled()})
	})
	mux.HandleFunc("GET /api/sessions", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"sessions": enrich(d.Manifest.List())})
	})
	mux.HandleFunc("POST /api/sync", func(w http.ResponseWriter, r *http.Request) {
		d.Engine.SyncNow()
		writeJSON(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /api/pause", func(w http.ResponseWriter, r *http.Request) {
		d.Engine.SetPaused(true)
		writeJSON(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /api/resume", func(w http.ResponseWriter, r *http.Request) {
		d.Engine.SetPaused(false)
		writeJSON(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("GET /files/{name}", d.serveSession)
	if d.laplineEnabled() {
		// Relative redirect: an absolute one would drop Home Assistant's ingress prefix.
		mux.HandleFunc("GET /lapline", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", "lapline/")
			w.WriteHeader(http.StatusFound)
		})
		mux.HandleFunc("GET /lapline/{$}", d.laplineIndex)
		mux.HandleFunc("GET /lapline/lapline-open.js", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.Write(laplineOpenJS)
		})
		static := http.StripPrefix("/lapline/", http.FileServer(http.Dir(d.LaplineDir)))
		mux.HandleFunc("GET /lapline/", func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/") { // no directory listings
				http.NotFound(w, r)
				return
			}
			static.ServeHTTP(w, r)
		})
	}
	return guard(d.AllowAny, mux)
}

// laplineIndex serves Lapline's own index.html with our helper script added.
func (d Deps) laplineIndex(w http.ResponseWriter, r *http.Request) {
	b, err := os.ReadFile(filepath.Join(d.LaplineDir, "index.html"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	tag := []byte(`<script src="lapline-open.js"></script>`)
	if i := bytes.LastIndex(b, []byte("</body>")); i >= 0 {
		b = bytes.Join([][]byte{b[:i], tag, b[i:]}, nil)
	} else {
		b = append(b, tag...)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(b)
}

// serveSession serves one saved session, but only if the manifest lists it.
func (d Deps) serveSession(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if d.FilesDir == "" || name == "" || filepath.Base(name) != name || strings.HasPrefix(name, ".") {
		http.NotFound(w, r)
		return
	}
	known := false
	for _, e := range d.Manifest.List() {
		if e.File == name {
			known = true
			break
		}
	}
	if !known {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(filepath.Join(d.FilesDir, name))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "private, max-age=3600") // saved sessions never change
	if r.URL.Query().Get("download") != "" {
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	}
	http.ServeContent(w, r, name, st.ModTime(), f)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

// guard rejects anything that didn't come through Home Assistant (or loopback,
// for health checks and local development).
func guard(allowAny bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowAny && !trusted(r.RemoteAddr) {
			http.Error(w, "forbidden: open this add-on from the Home Assistant sidebar", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func trusted(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.Equal(net.ParseIP(ingressGateway)))
}
