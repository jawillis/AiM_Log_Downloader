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
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"mychron-sync/internal/engine"
	"mychron-sync/internal/imports"
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
	// manifest or in Imports are served, read-only, at /files/<path>.
	FilesDir string
	// Imports lists .vbo files copied into FilesDir by hand. Optional.
	Imports *imports.Index
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
		writeJSON(w, map[string]any{"version": d.Version, "status": d.Engine.Status(), "lapline": d.laplineEnabled(), "imports": d.importsGeneration()})
	})
	mux.HandleFunc("GET /api/sessions", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"sessions": enrich(d.allSessions())})
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
	mux.HandleFunc("GET /files/{path...}", d.serveSession)
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

// allSessions is the manifest plus any imported files.
func (d Deps) allSessions() []store.Entry {
	all := d.Manifest.List()
	if d.Imports != nil {
		all = append(all, d.Imports.List()...)
	}
	return all
}

// importsGeneration changes whenever imported files are added, changed or
// removed, so the page knows to reload the list.
func (d Deps) importsGeneration() int {
	if d.Imports == nil {
		return 0
	}
	return d.Imports.Generation()
}

// known reports whether rel (relative to FilesDir, forward slashes) is a
// session the page lists. Nothing else on the share is ever served.
func (d Deps) known(rel string) bool {
	for _, e := range d.Manifest.List() {
		if e.File == rel {
			return true
		}
	}
	return d.Imports != nil && d.Imports.Has(rel)
}

// safeRel rejects anything that isn't a plain relative path inside FilesDir.
func safeRel(rel string) bool {
	if rel == "" || strings.HasPrefix(rel, "/") || strings.Contains(rel, "\\") {
		return false
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == "" || strings.HasPrefix(seg, ".") {
			return false
		}
	}
	return true
}

// serveSession serves one saved or imported session, and nothing else.
func (d Deps) serveSession(w http.ResponseWriter, r *http.Request) {
	rel := r.PathValue("path")
	if d.FilesDir == "" || !safeRel(rel) || !d.known(rel) {
		http.NotFound(w, r)
		return
	}
	name := filepath.Base(filepath.FromSlash(rel))
	f, err := os.Open(filepath.Join(d.FilesDir, filepath.FromSlash(rel)))
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
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
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
