package vdisk

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Offline files (Android wave 3): disk paths, files or folders, kept on
// this device. Every file under a pin is downloaded (hash-checked, like
// any download) by SyncPins and never pruned from the cache; a file
// that changes elsewhere is fetched again under its new hash on the
// next SyncPins, and its old copy becomes an ordinary cache file. A pin
// whose path is gone stays (a folder renamed back, a manifest read
// half-way); it simply holds nothing. The list lives in CacheDir.

func (d *Disk) pinsFile() string { return filepath.Join(d.CacheDir, "pins.json") }

// loadPinsLocked reads the list once. d.mu held.
func (d *Disk) loadPinsLocked() {
	if d.pins != nil {
		return
	}
	d.pins = map[string]bool{}
	b, err := os.ReadFile(d.pinsFile())
	if err != nil {
		return
	}
	var l []string
	if json.Unmarshal(b, &l) == nil {
		for _, p := range l {
			if p = Clean(p); p != "" {
				d.pins[p] = true
			}
		}
	}
}

// Pins returns the pinned paths, sorted.
func (d *Disk) Pins() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.loadPinsLocked()
	out := make([]string, 0, len(d.pins))
	for p := range d.pins {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// SetPin keeps a file or folder offline (on) or lets it go back to the
// ordinary cache (off). Pinning needs the path to exist; unpinning
// never does.
func (d *Disk) SetPin(p string, on bool) error {
	if p = Clean(p); p == "" {
		return ErrNotExist
	}
	if on {
		if _, err := d.Stat(p); err != nil {
			return err
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.loadPinsLocked()
	if d.pins[p] == on {
		return nil
	}
	l := make([]string, 0, len(d.pins)+1)
	for q := range d.pins {
		if q != p {
			l = append(l, q)
		}
	}
	if on {
		l = append(l, p)
	}
	sort.Strings(l)
	b, _ := json.Marshal(l)
	if err := os.MkdirAll(d.CacheDir, 0o700); err != nil {
		return err
	}
	tmp := d.pinsFile() + ".part"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, d.pinsFile()); err != nil {
		os.Remove(tmp)
		return err
	}
	if on {
		d.pins[p] = true
	} else {
		delete(d.pins, p)
	}
	return nil
}

// pinnedLocked tells whether p is a pin or lies under one. d.mu held.
func (d *Disk) pinnedLocked(p string) bool {
	d.loadPinsLocked()
	for q := range d.pins {
		if p == q || strings.HasPrefix(p, q+"/") {
			return true
		}
	}
	return false
}

// pinnedHashes returns the content hashes the pins hold in the manifest
// in hand, and false when there are pins but no manifest yet (then
// nothing may be pruned: what they hold is unknown).
func (d *Disk) pinnedHashes() (map[string]bool, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.loadPinsLocked()
	out := map[string]bool{}
	if len(d.pins) == 0 {
		return out, true
	}
	if d.remote == nil {
		return out, false
	}
	for p, e := range d.remote.Files {
		if e.H != "" && d.pinnedLocked(p) {
			out[e.H] = true
		}
	}
	return out, true
}

// PinStatus is what SyncPins did.
type PinStatus struct {
	Files  int    `json:"files"`  // files the pins hold
	Bytes  int64  `json:"bytes"`  // their size
	Got    int    `json:"got"`    // downloaded now
	Failed int    `json:"failed"` // could not be downloaded (tried again next time)
	Error  string `json:"error,omitempty"`
}

// SyncPins reads the manifest again and downloads every pinned file
// that is not on this device yet. One failed file does not stop the
// others.
func (d *Disk) SyncPins() (PinStatus, error) {
	var st PinStatus
	if len(d.Pins()) == 0 {
		return st, nil
	}
	if err := d.Refresh(true); err != nil {
		return st, err
	}
	d.mu.Lock()
	var todo []string
	for p, e := range d.remote.Files {
		if d.pinnedLocked(p) {
			todo = append(todo, p)
			n, _ := e.Size()
			st.Files++
			st.Bytes += n
		}
	}
	d.mu.Unlock()
	sort.Strings(todo)
	for _, p := range todo {
		d.mu.Lock()
		e, ok := d.remote.Files[p]
		d.mu.Unlock()
		if !ok || d.cached(e.H) {
			continue
		}
		if _, err := d.fetchEntry(e); err != nil {
			st.Failed++
			if st.Error == "" {
				st.Error = err.Error()
			}
			continue
		}
		st.Got++
	}
	return st, nil
}
