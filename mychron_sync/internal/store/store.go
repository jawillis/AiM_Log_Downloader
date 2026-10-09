// SPDX-License-Identifier: GPL-3.0-or-later

// Package store keeps the record of which sessions have been downloaded.
// It lives next to the downloaded files so the two travel together (e.g. when
// the share is backed up to a NAS).
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Entry describes one downloaded session.
type Entry struct {
	Name         string  `json:"name"`        // as listed on the logger, e.g. a_0775.xrz
	ID           int     `json:"id"`          // numeric part of Name
	DeviceSize   int64   `json:"device_size"` // size reported by the logger (compressed)
	Date         string  `json:"date"`        // as reported by the logger
	Hour         string  `json:"hour"`
	DateSuspect  bool    `json:"date_suspect"` // logger clock looked unset (e.g. 2015)
	Laps         string  `json:"laps"`
	Track        string  `json:"track"`
	Driver       string  `json:"driver,omitempty"`
	Vehicle      string  `json:"vehicle,omitempty"`
	Championship string  `json:"championship,omitempty"`
	Device       string  `json:"device,omitempty"`
	BestLapMS    int64   `json:"best_lap_ms,omitempty"` // 0: the logger had no lap time
	BestLapIndex int     `json:"best_lap_index,omitempty"`
	DurationMS   int64   `json:"duration_ms,omitempty"`
	Lat          float64 `json:"lat,omitempty"`
	Lon          float64 `json:"lon,omitempty"`
	// Extra holds any other non-empty column the logger reported (max speed
	// and so on), kept so we can use it later without re-reading the logger.
	Extra map[string]string `json:"extra,omitempty"`
	// MetaVersion says which set of list columns this entry was filled from;
	// older entries are brought up to date from the logger's list, without
	// downloading anything again.
	MetaVersion  int       `json:"meta_version"`
	File         string    `json:"file"`   // relative to the output directory
	Bytes        int64     `json:"bytes"`  // size of the saved file
	Status       string    `json:"status"` // "ok" or "raw_only"
	Note         string    `json:"note,omitempty"`
	DownloadedAt time.Time `json:"downloaded_at"`
	// Source is empty for sessions downloaded from the logger, or "vbo" for
	// .vbo files found in the output folder (those are never in the manifest).
	Source string `json:"source,omitempty"`
}

const (
	StatusOK      = "ok"
	StatusRawOnly = "raw_only" // saved exactly as received because inflating failed
)

type file struct {
	Version int     `json:"version"`
	Entries []Entry `json:"entries"`
}

// Manifest is a small JSON-backed set of Entry values. Safe for concurrent use.
type Manifest struct {
	path    string
	mu      sync.RWMutex
	entries []Entry
	seen    map[string]bool
}

func key(name string, size int64) string { return fmt.Sprintf("%s|%d", name, size) }

// Open loads the manifest at path, or starts an empty one if it doesn't exist.
func Open(path string) (*Manifest, error) {
	m := &Manifest{path: path, seen: map[string]bool{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	var f file
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	m.entries = f.Entries
	for _, e := range m.entries {
		m.seen[key(e.Name, e.DeviceSize)] = true
	}
	return m, nil
}

// Has reports whether this exact session (name and size) was already saved.
func (m *Manifest) Has(name string, size int64) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.seen[key(name, size)]
}

// CountName returns how many saved entries share a logger file name.
func (m *Manifest) CountName(name string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	n := 0
	for _, e := range m.entries {
		if e.Name == name {
			n++
		}
	}
	return n
}

// Len returns the number of saved sessions.
func (m *Manifest) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.entries)
}

// List returns a copy of all entries in download order.
func (m *Manifest) List() []Entry {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]Entry(nil), m.entries...)
}

// Update applies fn to every entry and saves once if any reported a change.
// It returns how many entries changed.
func (m *Manifest) Update(fn func(e *Entry) bool) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	next := append([]Entry(nil), m.entries...)
	changed := 0
	for i := range next {
		if fn(&next[i]) {
			changed++
		}
	}
	if changed == 0 {
		return 0, nil
	}
	if err := m.save(next); err != nil {
		return 0, err
	}
	m.entries = next
	return changed, nil
}

// Add records an entry and persists the manifest atomically.
func (m *Manifest) Add(e Entry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	next := append(append([]Entry(nil), m.entries...), e)
	if err := m.save(next); err != nil {
		return err
	}
	m.entries = next
	m.seen[key(e.Name, e.DeviceSize)] = true
	return nil
}

func (m *Manifest) save(entries []Entry) error {
	b, err := json.MarshalIndent(file{Version: 1, Entries: entries}, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(m.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".manifest-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), m.path)
}
