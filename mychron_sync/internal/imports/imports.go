// SPDX-License-Identifier: GPL-3.0-or-later

// Package imports lists session files that weren't downloaded from the logger
// but were copied into the output folder by hand: currently Racelogic .vbo
// files (VBOX, RaceChrono, Harry's LapTimer, TrackAddict, ...).
//
// Nothing is written to disk. The folder is rescanned periodically and each
// file is read again only when its size or modification time changes.
package imports

import (
	"context"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"mychron-sync/internal/store"
	"mychron-sync/internal/vbo"
)

// SourceVBO marks entries that came from a .vbo file.
const SourceVBO = "vbo"

// maxDepth limits how far below the output folder files are looked for.
const maxDepth = 6

type cached struct {
	size  int64
	mod   time.Time
	entry store.Entry
	ok    bool // false: the file couldn't be read as a .vbo
}

// Index is the current set of .vbo files. Safe for concurrent use.
type Index struct {
	root string
	log  *slog.Logger

	mu    sync.RWMutex
	files map[string]cached // by path relative to root, with forward slashes
	gen   int
}

// New returns an empty index of root. Call Refresh or Run to fill it.
func New(root string, log *slog.Logger) *Index {
	if log == nil {
		log = slog.Default()
	}
	return &Index{root: root, log: log, files: map[string]cached{}}
}

// Run refreshes the index now and then every interval until ctx ends.
func (x *Index) Run(ctx context.Context, every time.Duration) {
	for {
		x.Refresh()
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

// Refresh rescans the folder. Hidden folders (such as .mychron-sync) and the
// add-on's raw folder are skipped.
func (x *Index) Refresh() {
	x.mu.RLock()
	prev := x.files
	x.mu.RUnlock()

	next := map[string]cached{}
	changed := false
	_ = filepath.WalkDir(x.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == x.root {
				return err
			}
			return nil // unreadable subfolder: skip it
		}
		rel, rerr := filepath.Rel(x.root, path)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if path == x.root {
				return nil
			}
			if strings.HasPrefix(d.Name(), ".") || (rel == "raw") || strings.Count(rel, "/") >= maxDepth {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || strings.HasPrefix(d.Name(), ".") || !strings.EqualFold(filepath.Ext(d.Name()), ".vbo") {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		if c, ok := prev[rel]; ok && c.size == info.Size() && c.mod.Equal(info.ModTime()) {
			next[rel] = c
			return nil
		}
		changed = true
		next[rel] = x.read(path, rel, info)
		return nil
	})
	if len(next) != len(prev) {
		changed = true
	}
	for k := range prev {
		if _, ok := next[k]; !ok {
			changed = true
		}
	}
	if !changed {
		return
	}
	x.mu.Lock()
	x.files = next
	x.gen++
	x.mu.Unlock()
}

func (x *Index) read(path, rel string, info fs.FileInfo) cached {
	c := cached{size: info.Size(), mod: info.ModTime()}
	f, err := os.Open(path)
	if err != nil {
		x.log.Warn("could not open .vbo file", "file", rel, "err", err)
		return c
	}
	defer f.Close()
	s, err := vbo.Summarize(f)
	if err != nil {
		x.log.Warn("skipping unreadable .vbo file", "file", rel, "err", err)
		return c
	}
	c.ok = true
	c.entry = store.Entry{
		Name:         filepath.Base(path),
		Date:         s.Date,
		Hour:         s.Hour,
		Laps:         strconv.Itoa(s.Laps),
		Track:        s.Track,
		Driver:       s.Driver,
		Vehicle:      s.Vehicle,
		BestLapMS:    s.BestLapMS,
		BestLapIndex: s.BestLapNo,
		DurationMS:   s.DurationMS,
		Lat:          s.Lat,
		Lon:          s.Lon,
		File:         rel,
		Bytes:        info.Size(),
		Status:       store.StatusOK,
		DownloadedAt: info.ModTime().UTC(),
		Source:       SourceVBO,
	}
	x.log.Debug("read .vbo file", "file", rel, "laps", s.Laps, "best_ms", s.BestLapMS, "track", s.Track)
	return c
}

// List returns the readable files, in path order.
func (x *Index) List() []store.Entry {
	x.mu.RLock()
	defer x.mu.RUnlock()
	out := make([]store.Entry, 0, len(x.files))
	for _, c := range x.files {
		if c.ok {
			out = append(out, c.entry)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].File < out[j].File })
	return out
}

// Has reports whether rel (relative to the output folder, forward slashes)
// is a readable file in the index.
func (x *Index) Has(rel string) bool {
	x.mu.RLock()
	defer x.mu.RUnlock()
	c, ok := x.files[rel]
	return ok && c.ok
}

// Generation changes whenever the set of files or any file's contents changes.
func (x *Index) Generation() int {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return x.gen
}
