// Package cloudfiles shows the orOS disk (a vdisk.Disk) in a folder of
// Windows Explorer with the Cloud Files API, the placeholder system
// OneDrive uses (wave 6). Every file of the disk is a placeholder:
// listed with its name, size and date, its content downloaded when a
// program first reads it. Files written in the folder are uploaded.
//
// The engine here is the same on every system and is tested with a
// fake folder; cfapi_windows.go is the Windows side (placeholders and
// the callbacks Windows makes when a file is read, deleted or renamed).
//
// It follows the rules of vdisk and the mirror:
//   - an upload names the version it started from, so a file changed
//     in orOS meanwhile is kept beside it as a conflict copy (FILES-V);
//   - a file changed here is never replaced or removed: only
//     placeholders Windows marks "in sync" are updated or deleted, and
//     a deleted file that holds downloaded content goes to the trash;
//   - an empty or reset vault (no manifest) deletes nothing (VD-1), and
//     more than MaxDeletes deletions at once wait;
//   - lock, swap and temp files (mirror.Ignored) stay in the folder.
package cloudfiles

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/koulaxizis/oros-drive/mirror"
	"github.com/koulaxizis/oros-drive/vdisk"
)

var (
	// ErrBusy: a program has the file open for writing; try again later.
	ErrBusy = errors.New("file is open in another program")
	// ErrChanged: the file changed since it was looked at; nothing done.
	ErrChanged = errors.New("file changed meanwhile")
)

// Entry is one file or folder found in the local folder.
type Entry struct {
	Dir         bool
	Placeholder bool // made by the engine (or converted after an upload)
	InSync      bool // Windows says it is unchanged since then
	Size        int64
	MTime       time.Time
}

// Local is the folder side. Paths are relative, with "/".
type Local interface {
	// Scan lists everything in the folder (it errors when the folder
	// itself is gone: an unplugged disk deletes nothing).
	Scan() (map[string]Entry, error)
	// Identity is the content hash a placeholder was made with.
	Identity(rel string) (string, error)
	// CreateFile makes a placeholder (no content, in sync) for i.
	CreateFile(rel string, i vdisk.Info) error
	// CreateDir makes a folder placeholder (in sync).
	CreateDir(rel string) error
	// Update gives an in-sync placeholder new content from orOS (its
	// local content is dropped); ErrChanged when it is not in sync.
	Update(rel string, i vdisk.Info) error
	// Snapshot copies the file's content to tmp for an upload.
	Snapshot(rel, tmp string) error
	// MarkInSync makes the file an in-sync placeholder of hash, only
	// when its content still hashes to it (else ErrChanged).
	MarkInSync(rel, hash string) error
	// MarkDirInSync makes a folder a placeholder.
	MarkDirInSync(rel string) error
	// Remove deletes an in-sync file (to the trash when its content
	// was downloaded) or an empty folder; ErrChanged when it changed.
	Remove(rel string, dir bool) error
}

// State values for folders: made here ("/") or seen in orOS ("/r").
const (
	dirLocal  = "/"
	dirRemote = "/r"
)

// Engine keeps a Local folder and a Disk the same.
type Engine struct {
	Disk       *vdisk.Disk
	Local      Local
	StatePath  string        // what both sides last agreed on; "" = memory only
	MaxDeletes int           // deletions carried out in one pass; 0 = 50
	Settle     time.Duration // a file changed this recently is still being written; 0 = 2 s
	Grace      time.Duration // a synced file missing this long without a delete callback was deleted; 0 = 30 s
	GOOS       string        // names the folder can hold; "" = runtime.GOOS
	Now        func() time.Time
	Logf       func(kind, rel, detail string)

	mu      sync.Mutex
	loaded  bool
	dirty   bool
	state   map[string]string    // rel → content hash, or dirLocal / dirRemote
	missing map[string]time.Time // synced, gone here, no delete callback yet
	leaving map[string]time.Time // told orOS of its deletion or move; Windows has not finished it
	tooBig  map[string]string    // rel → size/mtime that was refused
}

// Report says what a pass could not do.
type Report struct {
	Held      int      // deletions waiting (more than MaxDeletes at once)
	LocalOnly []string // files too big for the vault
	Skipped   []string // names the folder cannot hold, or file/folder clashes
	Errors    []string
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Engine) maxDeletes() int {
	if e.MaxDeletes > 0 {
		return e.MaxDeletes
	}
	return 50
}

func (e *Engine) settle() time.Duration {
	if e.Settle > 0 {
		return e.Settle
	}
	return 2 * time.Second
}

func (e *Engine) grace() time.Duration {
	if e.Grace > 0 {
		return e.Grace
	}
	return 30 * time.Second
}

func (e *Engine) goos() string {
	if e.GOOS != "" {
		return e.GOOS
	}
	return runtime.GOOS
}

func (e *Engine) logf(kind, rel, detail string) {
	if e.Logf != nil {
		e.Logf(kind, rel, detail)
	}
}

func diskPath(rel string) string { return vdisk.Root + "/" + rel }

// syncable: a name both orOS and this folder can hold, not a temp file.
func (e *Engine) syncable(rel string) bool {
	if rel == "" || !mirror.RelOK(rel, e.goos()) {
		return false
	}
	for _, seg := range strings.Split(rel, "/") {
		if mirror.Ignored(seg) {
			return false
		}
	}
	return vdisk.Clean(diskPath(rel)) != ""
}

// ---------- state ----------

type stateFile struct {
	Files map[string]string `json:"files"`
}

func (e *Engine) load() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.loaded {
		return nil
	}
	e.state = map[string]string{}
	e.missing = map[string]time.Time{}
	e.leaving = map[string]time.Time{}
	e.tooBig = map[string]string{}
	if e.StatePath != "" {
		b, err := os.ReadFile(e.StatePath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil {
			var sf stateFile
			if err := json.Unmarshal(b, &sf); err != nil {
				return fmt.Errorf("%s: %w", e.StatePath, err)
			}
			for k, v := range sf.Files {
				e.state[k] = v
			}
		}
	}
	e.loaded = true
	return nil
}

// Save writes the state when it changed.
func (e *Engine) Save() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.dirty || e.StatePath == "" {
		return nil
	}
	b, err := json.Marshal(stateFile{Files: e.state})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(e.StatePath), 0o700); err != nil {
		return err
	}
	tmp := e.StatePath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, e.StatePath); err != nil {
		return err
	}
	e.dirty = false
	return nil
}

func (e *Engine) set(rel, v string) {
	e.mu.Lock()
	if e.state[rel] != v {
		e.state[rel] = v
		e.dirty = true
	}
	delete(e.missing, rel)
	e.mu.Unlock()
}

// forget drops rel and everything under it.
func (e *Engine) forget(rel string) {
	e.mu.Lock()
	for k := range e.state {
		if k == rel || strings.HasPrefix(k, rel+"/") {
			delete(e.state, k)
			delete(e.missing, k)
			e.dirty = true
		}
	}
	e.mu.Unlock()
}

func (e *Engine) snapshotState() map[string]string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make(map[string]string, len(e.state))
	for k, v := range e.state {
		out[k] = v
	}
	return out
}

// Synced returns what both sides last agreed on (for tests and status).
func (e *Engine) Synced() map[string]string {
	if e.load() != nil {
		return nil
	}
	return e.snapshotState()
}

// ---------- the pass ----------

type removal struct {
	rel string
	dir bool
}

// Pass brings both sides together once.
func (e *Engine) Pass() (Report, error) {
	var rep Report
	if err := e.load(); err != nil {
		return rep, err
	}
	tree, exists, err := e.Disk.Tree(false)
	if err != nil {
		return rep, err
	}
	local, err := e.Local.Scan()
	if err != nil {
		return rep, err
	}
	remote := map[string]vdisk.Info{}
	for p, i := range tree {
		rel := strings.TrimPrefix(p, vdisk.Root+"/")
		if e.syncable(rel) {
			remote[rel] = i
		} else if !mirror.Ignored(path.Base(rel)) {
			rep.Skipped = append(rep.Skipped, rel)
		}
	}
	e.mu.Lock()
	for rel := range local {
		if !e.syncable(rel) {
			delete(local, rel)
		}
		delete(e.missing, rel) // here again
	}
	for rel, since := range e.leaving {
		if _, ok := local[rel]; !ok || e.now().Sub(since) > 2*time.Minute {
			delete(e.leaving, rel) // done (or refused by something else)
		}
	}
	leaving := make([]string, 0, len(e.leaving))
	for rel := range e.leaving {
		leaving = append(leaving, rel)
	}
	e.mu.Unlock()
	isLeaving := func(rel string) bool {
		for _, k := range leaving {
			if rel == k || strings.HasPrefix(rel, k+"/") {
				return true
			}
		}
		return false
	}
	state := e.snapshotState()
	all := map[string]bool{}
	for rel := range local {
		all[rel] = true
	}
	for rel := range remote {
		all[rel] = true
	}
	for rel := range state {
		all[rel] = true
	}
	names := make([]string, 0, len(all))
	for rel := range all {
		names = append(names, rel)
	}
	sort.Strings(names) // a folder before what is in it

	var gone []removal        // deleted in orOS: remove here
	var lost []string         // deleted here while nobody listened: remove in orOS
	made := map[string]bool{} // folders created in this pass
	now := e.now()

	fail := func(rel string, err error) {
		rep.Errors = append(rep.Errors, rel+": "+err.Error())
		e.logf("error", rel, err.Error())
	}
	parents := func(rel string) error {
		for _, dir := range parentsOf(rel) {
			if _, ok := local[dir]; ok || made[dir] {
				continue
			}
			if err := e.Local.CreateDir(dir); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			made[dir] = true
			e.set(dir, dirRemote)
		}
		return nil
	}

	for _, rel := range names {
		l, lok := local[rel]
		r, rok := remote[rel]
		s, sok := state[rel]
		if lok && isLeaving(rel) {
			// Being deleted or moved: orOS already knows. Uploading it
			// now would bring it back.
			continue
		}
		switch {
		case lok && rok && l.Dir != r.Dir:
			rep.Skipped = append(rep.Skipped, rel)

		case lok && l.Dir:
			switch {
			case !l.Placeholder:
				// A folder made here.
				if err := e.Disk.Mkdir(diskPath(rel)); err != nil && !errors.Is(err, vdisk.ErrExist) {
					fail(rel, err)
					continue
				}
				if err := e.Local.MarkDirInSync(rel); err != nil {
					fail(rel, err)
					continue
				}
				e.set(rel, map[bool]string{true: dirRemote, false: dirLocal}[rok])
			case rok:
				if s != dirRemote {
					e.set(rel, dirRemote)
				}
			case s == dirRemote && exists:
				gone = append(gone, removal{rel, true}) // deleted in orOS
			default:
				// Empty folders live on this device only (the manifest
				// holds files): show it in orOS again after a restart.
				if err := e.Disk.Mkdir(diskPath(rel)); err != nil && !errors.Is(err, vdisk.ErrExist) {
					fail(rel, err)
				}
				if !sok {
					e.set(rel, dirLocal)
				}
			}

		case lok:
			e.passFile(rel, l, r, rok, s, sok, exists, now, &rep, &gone)

		case rok && r.Dir:
			if sok && (s == dirRemote || s == dirLocal) && !made[rel] {
				// Deleted here: its files go with it (each one below);
				// the folder itself is only a name in orOS.
				continue
			}
			if made[rel] {
				continue
			}
			if err := parents(rel); err != nil {
				fail(rel, err)
				continue
			}
			if err := e.Local.CreateDir(rel); err != nil && !errors.Is(err, os.ErrExist) {
				fail(rel, err)
				continue
			}
			made[rel] = true
			e.set(rel, dirRemote)

		case rok:
			if sok && s == r.Hash {
				// Synced, gone here, and no delete callback carried it
				// (the app was not running, or a program moved it to a
				// temp name and has not put the new version back yet).
				e.mu.Lock()
				since, ok := e.missing[rel]
				if !ok {
					e.missing[rel] = now
					since = now
				}
				e.mu.Unlock()
				if now.Sub(since) >= e.grace() {
					lost = append(lost, rel)
				}
				continue
			}
			// New in orOS, or changed there after it was deleted here
			// (an edit beats a deletion).
			if err := parents(rel); err != nil {
				fail(rel, err)
				continue
			}
			if err := e.Local.CreateFile(rel, r); err != nil {
				if errors.Is(err, os.ErrExist) {
					rep.Skipped = append(rep.Skipped, rel) // "A.txt" beside "a.txt"
				} else {
					fail(rel, err)
				}
				continue
			}
			e.set(rel, r.Hash)

		case sok:
			e.forget(rel)
		}
	}

	// Deletions, with the brakes the mirror has.
	if len(gone) > e.maxDeletes() {
		rep.Held += len(gone)
		e.logf("held", "", fmt.Sprintf("%d files deleted in orOS", len(gone)))
		gone = nil
	}
	if len(lost) > e.maxDeletes() {
		rep.Held += len(lost)
		e.logf("held", "", fmt.Sprintf("%d files missing here", len(lost)))
		lost = nil
	}
	// Files first, then folders from the deepest.
	sort.Slice(gone, func(a, b int) bool {
		if gone[a].dir != gone[b].dir {
			return !gone[a].dir
		}
		return gone[a].rel > gone[b].rel
	})
	for _, g := range gone {
		err := e.Local.Remove(g.rel, g.dir)
		switch {
		case err == nil:
			e.forget(g.rel)
			e.logf("deleted-here", g.rel, "")
		case errors.Is(err, ErrChanged), errors.Is(err, ErrBusy):
			// Changed here meanwhile: it is uploaded on the next pass.
		case g.dir:
			// Not empty: something new here keeps it.
		default:
			fail(g.rel, err)
		}
	}
	for _, rel := range lost {
		err := e.Disk.Remove(diskPath(rel), false)
		if err != nil && !errors.Is(err, vdisk.ErrNotExist) {
			fail(rel, err)
			continue
		}
		e.forget(rel)
		e.logf("deleted-orOS", rel, "")
	}
	if err := e.Save(); err != nil {
		fail("", err)
	}
	return rep, nil
}

func (e *Engine) passFile(rel string, l Entry, r vdisk.Info, rok bool, s string, sok, exists bool, now time.Time, rep *Report, gone *[]removal) {
	if sok && (s == dirLocal || s == dirRemote) {
		sok, s = false, "" // was a folder
	}
	upload := !l.Placeholder || !l.InSync
	base := s
	if !upload && !sok {
		// A placeholder at a name we never synced (moved here under a
		// temp name, or state lost): adopt it when it matches.
		id, err := e.Local.Identity(rel)
		if err == nil && rok && id == r.Hash {
			e.set(rel, id)
			return
		}
		upload, base = true, ""
	}
	if upload {
		if now.Sub(l.MTime) < e.settle() {
			return // still being written
		}
		key := fmt.Sprintf("%d/%d", l.Size, l.MTime.UnixNano())
		e.mu.Lock()
		refused := e.tooBig[rel] == key
		e.mu.Unlock()
		if refused {
			rep.LocalOnly = append(rep.LocalOnly, rel)
			return
		}
		e.upload(rel, base, key, rep)
		return
	}
	switch {
	case !rok:
		if exists {
			*gone = append(*gone, removal{rel, false})
		}
	case r.Dir:
		rep.Skipped = append(rep.Skipped, rel)
	case r.Hash != s:
		// Changed in orOS; unchanged here.
		if err := e.Local.Update(rel, r); err != nil {
			if !errors.Is(err, ErrChanged) && !errors.Is(err, ErrBusy) {
				rep.Errors = append(rep.Errors, rel+": "+err.Error())
				e.logf("error", rel, err.Error())
			}
			return
		}
		e.set(rel, r.Hash)
		e.logf("updated-here", rel, "")
	}
}

func (e *Engine) upload(rel, base, key string, rep *Report) {
	f, err := e.Disk.TempFile()
	if err != nil {
		rep.Errors = append(rep.Errors, rel+": "+err.Error())
		return
	}
	tmp := f.Name()
	f.Close()
	defer os.Remove(tmp)
	if err := e.Local.Snapshot(rel, tmp); err != nil {
		if !errors.Is(err, ErrBusy) && !errors.Is(err, os.ErrNotExist) {
			rep.Errors = append(rep.Errors, rel+": "+err.Error())
			e.logf("error", rel, err.Error())
		}
		return
	}
	info, err := e.Disk.Put(diskPath(rel), tmp, base)
	switch {
	case errors.Is(err, vdisk.ErrTooBig):
		e.mu.Lock()
		e.tooBig[rel] = key
		e.mu.Unlock()
		rep.LocalOnly = append(rep.LocalOnly, rel)
		return
	case errors.Is(err, vdisk.ErrBadName), errors.Is(err, vdisk.ErrIsDir):
		rep.Skipped = append(rep.Skipped, rel)
		return
	case err != nil:
		rep.Errors = append(rep.Errors, rel+": "+err.Error())
		e.logf("error", rel, err.Error())
		return
	}
	// orOS now has this version, whatever happens here next.
	e.set(rel, info.Hash)
	e.logf("uploaded", rel, "")
	if err := e.Local.MarkInSync(rel, info.Hash); err != nil && !errors.Is(err, ErrChanged) && !errors.Is(err, ErrBusy) {
		rep.Errors = append(rep.Errors, rel+": "+err.Error())
	}
}

func parentsOf(rel string) []string {
	var out []string
	for dir := path.Dir(rel); dir != "." && dir != "/"; dir = path.Dir(dir) {
		out = append([]string{dir}, out...)
	}
	return out
}

// ---------- what Windows tells the engine ----------

// Fetch returns a local copy of the content a placeholder was made
// with (its identity), for Windows to fill the file from.
func (e *Engine) Fetch(identity string) (string, error) {
	return e.Disk.FetchHash(identity)
}

// OnDelete: a program deletes a placeholder. The deletion goes to
// orOS first; when that fails the program's deletion fails too, so a
// deleted file never comes back on its own.
func (e *Engine) OnDelete(rel string) error {
	if err := e.load(); err != nil {
		return err
	}
	if !e.syncable(rel) {
		return nil
	}
	err := e.Disk.Remove(diskPath(rel), true)
	if err != nil && !errors.Is(err, vdisk.ErrNotExist) {
		return err
	}
	e.forget(rel)
	e.goingAway(rel)
	e.logf("deleted-orOS", rel, "")
	return e.Save()
}

// goingAway marks rel as told to orOS while Windows still carries the
// deletion or move out (a pass may run in between).
func (e *Engine) goingAway(rel string) {
	e.mu.Lock()
	e.leaving[rel] = e.now()
	e.mu.Unlock()
}

// OnRename: a program renames or moves a placeholder. in says whether
// each side is inside the folder.
func (e *Engine) OnRename(from, to string, fromIn, toIn bool) error {
	if err := e.load(); err != nil {
		return err
	}
	fromOK := fromIn && e.syncable(from)
	toOK := toIn && e.syncable(to)
	switch {
	case fromOK && toOK:
		err := e.Disk.Rename(diskPath(from), diskPath(to))
		if errors.Is(err, vdisk.ErrNotExist) {
			return nil // not in orOS yet: the next pass uploads it
		}
		if err != nil {
			return err
		}
		e.mu.Lock()
		for k, v := range e.state {
			if k == from || strings.HasPrefix(k, from+"/") {
				delete(e.state, k)
				e.state[to+strings.TrimPrefix(k, from)] = v
			}
		}
		for k := range e.state {
			if k == to || strings.HasPrefix(k, to+"/") {
				delete(e.missing, k)
			}
		}
		e.dirty = true
		e.leaving[from] = e.now()
		e.mu.Unlock()
		e.logf("renamed-orOS", from, to)
		return e.Save()
	case fromOK && !toIn:
		return e.OnDelete(from) // moved out of the folder (or to the Recycle Bin)
	}
	// Renamed to a temp name (a program saving "write new, swap"), or
	// from one: orOS keeps the file; the version that lands back under
	// the real name is uploaded against it.
	return nil
}
