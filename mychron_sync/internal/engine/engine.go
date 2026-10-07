// SPDX-License-Identifier: GPL-3.0-or-later

// Package engine decides when the logger is reachable and downloads any
// sessions that haven't been saved yet.
package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mychron-sync/internal/aim"
	"mychron-sync/internal/store"
)

// Engine states, shown in the UI and published to Home Assistant.
const (
	StateUnconfigured = "unconfigured"
	StatePaused       = "paused"
	StateOffline      = "offline"
	StateWaiting      = "waiting"
	StateSyncing      = "syncing"
	StateIdle         = "idle"
	StateError        = "error"
)

// Device is the part of aim.Client the engine needs (and tests can fake).
type Device interface {
	ListSessions() ([]aim.Session, error)
	ReadFile(path string, progress func(done, total int)) ([]byte, error)
	Close() error
}

// Notifier receives state changes and download summaries (e.g. Home Assistant).
type Notifier interface {
	StatusChanged(Status)
	Downloaded([]store.Entry)
}

// Config controls the engine.
type Config struct {
	Host           string
	PollInterval   time.Duration // how often to check whether the logger is on the network
	ResyncInterval time.Duration // while it stays on the network, re-list this often
	Settle         time.Duration // grace period after the logger appears
	BackoffBase    time.Duration // retry delay after a failed sync (doubles, capped)
	BackoffMax     time.Duration
	OutputDir      string
	KeepRaw        bool // also keep the compressed download under raw/

	Dial    func(ctx context.Context, host string) (Device, error)
	ProbeFn func(ctx context.Context, host string) bool
	Notify  Notifier
	Log     *slog.Logger
}

func (c *Config) defaults() {
	if c.PollInterval <= 0 {
		c.PollInterval = 15 * time.Second
	}
	if c.ResyncInterval <= 0 {
		c.ResyncInterval = 5 * time.Minute
	}
	if c.BackoffBase <= 0 {
		c.BackoffBase = 30 * time.Second
	}
	if c.BackoffMax <= 0 {
		c.BackoffMax = 10 * time.Minute
	}
	if c.Log == nil {
		c.Log = slog.Default()
	}
}

// Progress describes the download in flight.
type Progress struct {
	Name  string `json:"name"`
	Index int    `json:"index"` // 1-based position in this run
	Count int    `json:"count"` // sessions to download in this run
	Done  int    `json:"done"`  // bytes of the current file
	Total int    `json:"total"`
}

// Status is a snapshot for the UI.
type Status struct {
	State     string     `json:"state"`
	Message   string     `json:"message"`
	Host      string     `json:"host"`
	Online    bool       `json:"online"`
	Paused    bool       `json:"paused"`
	LastSeen  *time.Time `json:"last_seen,omitempty"`
	LastSync  *time.Time `json:"last_sync,omitempty"`
	NextSync  *time.Time `json:"next_sync,omitempty"`
	LastNew   int        `json:"last_new"`   // sessions saved by the most recent sync
	Total     int        `json:"total"`      // sessions saved overall
	LastError string     `json:"last_error"` // cleared by the next successful sync
	Progress  *Progress  `json:"progress,omitempty"`
}

func (s Status) clone() Status {
	if s.Progress != nil {
		p := *s.Progress
		s.Progress = &p
	}
	return s
}

// Engine runs the poll/sync loop.
type Engine struct {
	cfg Config
	man *store.Manifest

	mu       sync.Mutex
	st       Status
	notified Status

	kick   chan struct{}
	paused atomic.Bool
}

// New creates an engine. Stale temp files from an interrupted run are removed.
func New(cfg Config, man *store.Manifest) *Engine {
	cfg.defaults()
	e := &Engine{cfg: cfg, man: man, kick: make(chan struct{}, 1)}
	e.st = Status{State: StateOffline, Host: cfg.Host, Total: man.Len()}
	e.notified = e.st
	e.notified.State = "" // force the first notification
	if tmps, _ := filepath.Glob(filepath.Join(cfg.OutputDir, ".tmp-*")); len(tmps) > 0 {
		for _, t := range tmps {
			os.Remove(t)
		}
	}
	return e
}

// Status returns the current status.
func (e *Engine) Status() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.st.clone()
}

// SyncNow asks for a sync as soon as possible (no-op while paused).
func (e *Engine) SyncNow() {
	select {
	case e.kick <- struct{}{}:
	default:
	}
}

// SetPaused stops (or resumes) all contact with the logger. Pausing lets
// Race Studio use the logger's single connection.
func (e *Engine) SetPaused(p bool) {
	e.paused.Store(p)
	if p {
		e.update(func(s *Status) {
			s.Paused = true
			s.State = StatePaused
			s.Message = "Not contacting the logger, so Race Studio can use it."
			s.Progress = nil
		})
	} else {
		e.update(func(s *Status) { s.Paused = false; s.State = StateOffline; s.Message = "Checking for the logger…" })
		e.SyncNow()
	}
}

func (e *Engine) update(f func(*Status)) {
	e.mu.Lock()
	f(&e.st)
	if e.paused.Load() { // pausing always wins over a late status update
		e.st.State, e.st.Paused, e.st.Progress = StatePaused, true, nil
		e.st.Message = "Not contacting the logger, so Race Studio can use it."
	}
	snap := e.st.clone()
	changed := snap.State != e.notified.State || snap.Total != e.notified.Total ||
		snap.LastError != e.notified.LastError || snap.Online != e.notified.Online
	if changed {
		e.notified = snap
	}
	e.mu.Unlock()
	if changed && e.cfg.Notify != nil {
		e.cfg.Notify.StatusChanged(snap)
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// Run blocks until ctx is cancelled.
func (e *Engine) Run(ctx context.Context) {
	log := e.cfg.Log
	if e.cfg.Host == "" {
		e.update(func(s *Status) {
			s.State = StateUnconfigured
			s.Message = "Set logger_host in the add-on configuration, then restart the add-on."
		})
		<-ctx.Done()
		return
	}
	var (
		wasOnline bool
		nextSync  time.Time
		failures  int
		wait      time.Duration // first poll happens immediately
	)
	for {
		forced := false
		select {
		case <-ctx.Done():
			return
		case <-e.kick:
			forced = true
		case <-time.After(wait):
		}
		wait = e.cfg.PollInterval

		if e.paused.Load() {
			wasOnline = false
			continue
		}
		online := e.cfg.ProbeFn(ctx, e.cfg.Host)
		if ctx.Err() != nil {
			return
		}
		now := time.Now()
		if !online {
			wasOnline, failures = false, 0
			e.update(func(s *Status) {
				s.Online, s.State, s.Progress, s.NextSync = false, StateOffline, nil, nil
				s.Message = "Logger is not on the network. It reconnects when you're back in the pits."
			})
			continue
		}
		e.update(func(s *Status) { s.Online = true; s.LastSeen = &now })

		first := !wasOnline
		wasOnline = true
		if !(forced || first || !now.Before(nextSync)) {
			continue
		}
		if first && !forced && e.cfg.Settle > 0 {
			log.Info("logger appeared; letting it settle", "wait", e.cfg.Settle)
			e.update(func(s *Status) { s.State = StateWaiting; s.Message = "Giving it a moment to settle before connecting." })
			if !sleepCtx(ctx, e.cfg.Settle) {
				return
			}
			if e.paused.Load() {
				continue
			}
		}

		done, err := e.syncOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		done2 := time.Now()
		if len(done) > 0 && e.cfg.Notify != nil {
			e.cfg.Notify.Downloaded(done)
		}
		if err != nil {
			failures++
			delay := e.backoff(failures)
			next := done2.Add(delay)
			nextSync = next
			log.Warn("sync failed", "err", err, "saved_this_run", len(done), "retry_in", delay.Round(time.Second))
			e.update(func(s *Status) {
				s.State, s.Progress, s.NextSync = StateError, nil, &next
				s.LastError = err.Error()
				s.LastNew = len(done)
				s.Total = e.man.Len()
				s.Message = "Will try again in " + humanDur(delay) + "."
			})
			continue
		}
		failures = 0
		nextSync = done2.Add(e.cfg.ResyncInterval)
		next := nextSync
		log.Info("sync complete", "new", len(done), "total", e.man.Len())
		e.update(func(s *Status) {
			s.State, s.Progress, s.NextSync = StateIdle, nil, &next
			s.LastError = ""
			s.LastSync = &done2
			s.LastNew = len(done)
			s.Total = e.man.Len()
			switch len(done) {
			case 0:
				s.Message = "Nothing new on the logger."
			case 1:
				s.Message = "Saved 1 new session."
			default:
				s.Message = fmt.Sprintf("Saved %d new sessions.", len(done))
			}
		})
	}
}

func humanDur(d time.Duration) string {
	if d < 90*time.Second {
		return fmt.Sprintf("%d seconds", int(d.Round(time.Second).Seconds()))
	}
	return fmt.Sprintf("%d minutes", int(d.Round(time.Minute).Minutes()))
}

func (e *Engine) backoff(failures int) time.Duration {
	d := e.cfg.BackoffBase
	for i := 1; i < failures && d < e.cfg.BackoffMax; i++ {
		d *= 2
	}
	return min(d, e.cfg.BackoffMax)
}

// syncOnce connects, lists, and downloads everything not yet saved, newest
// first. Sessions saved before an error are kept (and reported in done).
func (e *Engine) syncOnce(ctx context.Context) (done []store.Entry, err error) {
	log := e.cfg.Log
	if e.paused.Load() {
		return nil, nil
	}
	e.update(func(s *Status) { s.State = StateSyncing; s.Message = "Connecting to the logger…"; s.Progress = nil })
	dev, err := e.cfg.Dial(ctx, e.cfg.Host)
	if err != nil {
		return nil, err
	}
	defer dev.Close()

	list, err := dev.ListSessions()
	if err != nil {
		return nil, fmt.Errorf("listing sessions: %w", err)
	}
	// Bring sessions saved by an earlier version up to date from this list.
	bySession := make(map[string]aim.Session, len(list))
	for _, s := range list {
		bySession[fmt.Sprintf("%s|%d", s.Name, s.Size)] = s
	}
	if n, err := e.man.Update(func(en *store.Entry) bool {
		s, ok := bySession[fmt.Sprintf("%s|%d", en.Name, en.DeviceSize)]
		if !ok || en.MetaVersion >= metaVersion {
			return false
		}
		applyMeta(en, s)
		return true
	}); err != nil {
		log.Warn("could not update saved session details", "err", err)
	} else if n > 0 {
		log.Info("filled in details for saved sessions", "count", n)
	}

	var todo []aim.Session
	for _, s := range list {
		if !e.man.Has(s.Name, s.Size) {
			todo = append(todo, s)
		}
	}
	sort.SliceStable(todo, func(i, j int) bool { return todo[i].ID > todo[j].ID })
	log.Info("session list", "on_logger", len(list), "to_download", len(todo))

	skipped := 0
	for i, s := range todo {
		if ctx.Err() != nil {
			return done, ctx.Err()
		}
		if e.paused.Load() {
			return done, nil
		}
		e.update(func(st *Status) {
			st.Progress = &Progress{Name: s.Name, Index: i + 1, Count: len(todo), Total: int(s.Size)}
			st.Message = fmt.Sprintf("Downloading %s (%d of %d)", s.Name, i+1, len(todo))
		})
		raw, err := dev.ReadFile("1:/mem/"+s.Name, func(d, t int) {
			e.mu.Lock()
			if e.st.Progress != nil {
				e.st.Progress.Done, e.st.Progress.Total = d, t
			}
			e.mu.Unlock()
		})
		if err != nil {
			if errors.Is(err, aim.ErrProtocol) { // this file is bad; keep going with the rest
				log.Warn("skipping session after protocol error", "name", s.Name, "err", err)
				skipped++
				continue
			}
			return done, fmt.Errorf("downloading %s: %w", s.Name, err)
		}
		entry, err := e.save(s, raw)
		if err != nil {
			return done, fmt.Errorf("saving %s: %w", s.Name, err)
		}
		log.Info("saved session", "name", s.Name, "file", entry.File, "status", entry.Status, "bytes", entry.Bytes)
		done = append(done, entry)
		e.update(func(st *Status) { st.Total = e.man.Len() })
	}
	if skipped > 0 {
		return done, fmt.Errorf("%d session(s) failed integrity checks and will be retried", skipped)
	}
	return done, nil
}

// save writes a download to disk (atomically) and records it.
func (e *Engine) save(s aim.Session, raw []byte) (store.Entry, error) {
	version := e.man.CountName(s.Name) + 1
	stem := fileStem(s, version)
	entry := store.Entry{Name: s.Name, ID: s.ID, DeviceSize: s.Size, DownloadedAt: time.Now().UTC()}
	applyMeta(&entry, s)
	inflated, ierr := aim.Inflate(raw)
	if ierr == nil {
		name, err := writeUnique(e.cfg.OutputDir, stem+".xrk", inflated)
		if err != nil {
			return entry, err
		}
		entry.File, entry.Bytes, entry.Status = name, int64(len(inflated)), store.StatusOK
		if !aim.LooksLikeXRK(inflated) {
			entry.Note = "Saved, but the data doesn't start with the usual XRK header."
		}
		if e.cfg.KeepRaw {
			if _, err := writeUnique(filepath.Join(e.cfg.OutputDir, "raw"), stem+"."+extOr(s.Ext), raw); err != nil {
				return entry, err
			}
		}
	} else {
		name, err := writeUnique(e.cfg.OutputDir, stem+"."+extOr(s.Ext), raw)
		if err != nil {
			return entry, err
		}
		entry.File, entry.Bytes, entry.Status = name, int64(len(raw)), store.StatusRawOnly
		entry.Note = "Saved exactly as received; it could not be decompressed (" + ierr.Error() + ")."
	}
	if err := e.man.Add(entry); err != nil {
		return entry, err
	}
	return entry, nil
}

// metaVersion is bumped when we start keeping more list columns, so existing
// entries get filled in from the logger's list on the next sync.
const metaVersion = 1

// Columns that have their own Entry field; everything else non-empty goes in Extra.
var mappedColumns = map[string]bool{
	"": true, "name": true, "size": true, "date": true, "hour": true, "nlap": true, "nbest": true,
	"best": true, "pilota": true, "track_name": true, "veicolo": true, "campionato": true,
	"test_dur": true, "track_lat": true, "track_lon": true, "device": true,
}

// applyMeta copies everything the session list tells us onto an entry.
func applyMeta(e *store.Entry, s aim.Session) {
	e.Date, e.Hour, e.DateSuspect = s.Date, s.Hour, dateSuspect(s.Date)
	e.Laps, e.Track = s.Laps, s.Track
	e.Driver, e.Vehicle, e.Championship, e.Device = s.Driver, s.Vehicle, s.Championship, s.Device
	e.BestLapMS, e.BestLapIndex, e.DurationMS = s.BestMS, s.BestLap, s.DurationMS
	e.Lat, e.Lon = s.Lat, s.Lon
	e.Extra = nil
	for k, v := range s.Fields {
		if !mappedColumns[k] && v != "" {
			if e.Extra == nil {
				e.Extra = map[string]string{}
			}
			e.Extra[k] = v
		}
	}
	e.MetaVersion = metaVersion
}

func extOr(ext string) string {
	if ext == "" {
		return "bin"
	}
	return ext
}

// dateSuspect flags dates from an unset logger clock (it reports 29/11/2015).
func dateSuspect(date string) bool {
	parts := strings.Split(date, "/")
	if len(parts) != 3 {
		return true
	}
	var y int
	if _, err := fmt.Sscanf(parts[2], "%d", &y); err != nil {
		return true
	}
	return y < 2020
}

func sanitize(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range s {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_'
		if ok {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.TrimRight(b.String(), "-")
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}

// fileStem builds e.g. "a_0775_RCQMA" (or "a_0775_RCQMA_v2" if the logger
// reported a different file under a name we already saved).
func fileStem(s aim.Session, version int) string {
	stem := sanitize(strings.TrimSuffix(s.Name, filepath.Ext(s.Name)))
	if stem == "" {
		stem = "session"
	}
	if t := sanitize(s.Track); t != "" {
		stem += "_" + t
	}
	if version > 1 {
		stem += fmt.Sprintf("_v%d", version)
	}
	return stem
}

// writeUnique writes data to dir/name via a temp file and rename, never
// overwriting an existing file. It returns the name actually used.
func writeUnique(dir, name string, data []byte) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return "", err
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	final := name
	for n := 2; ; n++ {
		if _, err := os.Stat(filepath.Join(dir, final)); errors.Is(err, os.ErrNotExist) {
			break
		}
		final = fmt.Sprintf("%s_dup%d%s", base, n, ext)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, final)); err != nil {
		return "", err
	}
	rel := final
	if filepath.Base(dir) == "raw" {
		rel = "raw/" + final
	}
	return rel, nil
}
