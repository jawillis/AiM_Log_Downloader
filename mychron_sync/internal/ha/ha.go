// SPDX-License-Identifier: GPL-3.0-or-later

// Package ha publishes status to Home Assistant through the Supervisor's
// core API proxy. It needs `homeassistant_api: true` in the add-on config and
// does nothing when run outside Home Assistant.
//
// Entities created this way don't survive a Home Assistant restart on their
// own, so the add-on re-publishes them whenever its state changes.
package ha

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"time"

	"mychron-sync/internal/aim"
	"mychron-sync/internal/engine"
	"mychron-sync/internal/store"
)

const (
	StatusEntity   = "sensor.mychron_sync_status"
	SessionsEntity = "sensor.mychron_sync_sessions"
	// EventNewSessions fires after a sync saves at least one session.
	EventNewSessions = "mychron_sync_new_sessions"
)

// Client implements engine.Notifier.
type Client struct {
	base  string
	token string
	http  *http.Client
	log   *slog.Logger
	queue chan func(context.Context) error
}

// New returns nil unless running under the Supervisor (SUPERVISOR_TOKEN set).
func New(log *slog.Logger) *Client {
	tok := os.Getenv("SUPERVISOR_TOKEN")
	if tok == "" {
		return nil
	}
	c := &Client{
		base: "http://supervisor/core/api", token: tok, log: log,
		http:  &http.Client{Timeout: 5 * time.Second},
		queue: make(chan func(context.Context) error, 32),
	}
	go func() { // one worker keeps updates in order and off the sync path
		for job := range c.queue {
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			if err := job(ctx); err != nil {
				log.Debug("home assistant update failed", "err", err)
			}
			cancel()
		}
	}()
	return c
}

func (c *Client) post(ctx context.Context, path string, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("POST %s: %s", path, resp.Status)
	}
	return nil
}

func (c *Client) enqueue(job func(context.Context) error) {
	select {
	case c.queue <- job:
	default: // HA is slow or down; the next change will carry fresher state
	}
}

// StatusChanged publishes the two sensors.
func (c *Client) StatusChanged(s engine.Status) {
	attrs := map[string]any{
		"friendly_name": "Logger sync",
		"icon":          "mdi:cloud-sync-outline",
		"message":       s.Message,
		"logger_online": s.Online,
		"last_error":    s.LastError,
	}
	if s.LastSeen != nil {
		attrs["logger_last_seen"] = s.LastSeen.Format(time.RFC3339)
	}
	if s.LastSync != nil {
		attrs["last_sync"] = s.LastSync.Format(time.RFC3339)
	}
	c.enqueue(func(ctx context.Context) error {
		return c.post(ctx, "/states/"+StatusEntity, map[string]any{"state": s.State, "attributes": attrs})
	})
	c.enqueue(func(ctx context.Context) error {
		return c.post(ctx, "/states/"+SessionsEntity, map[string]any{
			"state": s.Total,
			"attributes": map[string]any{
				"friendly_name":       "Sessions downloaded",
				"icon":                "mdi:database-check-outline",
				"unit_of_measurement": "sessions",
				"state_class":         "total_increasing",
			},
		})
	})
}

// Downloaded fires an event so automations can notify your phone.
func (c *Client) Downloaded(entries []store.Entry) {
	data := eventData(entries)
	c.enqueue(func(ctx context.Context) error { return c.post(ctx, "/events/"+EventNewSessions, data) })
}

// eventData summarises a batch of saved sessions. best_lap* are only present
// when at least one session in the batch has a lap time.
func eventData(entries []store.Entry) map[string]any {
	names := make([]string, 0, len(entries))
	tracks := map[string]bool{}
	var best *store.Entry
	for i, e := range entries {
		names = append(names, e.Name)
		if e.Track != "" {
			tracks[e.Track] = true
		}
		if e.BestLapMS > 0 && (best == nil || e.BestLapMS < best.BestLapMS) {
			best = &entries[i]
		}
	}
	trackList := make([]string, 0, len(tracks))
	for t := range tracks {
		trackList = append(trackList, t)
	}
	sort.Strings(trackList)
	data := map[string]any{"count": len(entries), "sessions": names, "tracks": trackList}
	if best != nil {
		data["best_lap"] = aim.FormatLap(best.BestLapMS)
		data["best_lap_ms"] = best.BestLapMS
		data["best_lap_track"] = best.Track
		data["best_lap_session"] = best.Name
	}
	return data
}
