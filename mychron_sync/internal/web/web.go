// SPDX-License-Identifier: GPL-3.0-or-later

// Package web serves the status page and its small JSON API.
//
// The page is designed for Home Assistant ingress: every URL it uses is
// relative, and (because the add-on runs on the host network) requests are only
// accepted from the Supervisor's ingress gateway unless AllowAny is set.
package web

import (
	_ "embed"
	"encoding/json"
	"net"
	"net/http"

	"mychron-sync/internal/engine"
	"mychron-sync/internal/store"
)

//go:embed static/index.html
var indexHTML []byte

// ingressGateway is the address Home Assistant's ingress proxy connects from.
const ingressGateway = "172.30.32.2"

// Deps are the server's collaborators.
type Deps struct {
	Engine   *engine.Engine
	Manifest *store.Manifest
	Version  string
	// AllowAny disables the ingress-gateway check. Only for local development.
	AllowAny bool
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
		writeJSON(w, map[string]any{"version": d.Version, "status": d.Engine.Status()})
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
	return guard(d.AllowAny, mux)
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
