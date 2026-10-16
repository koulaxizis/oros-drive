// Package mirror keeps folders of this computer and folders of the
// orOS Vault the same, both ways, by the vault.js rules.
//
// A pass reads the cloud manifest once, scans every mirrored folder,
// decides each path with a three-way merge against what was synced
// last (decide.go), applies the cloud's changes here, then uploads
// this computer's changes and commits them in a manifest write that is
// conditional on the revision it read. A refused write means another
// device wrote in between: the pass starts again from a fresh read and
// never overwrites what it has not seen.
package mirror

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/koulaxizis/oros-drive/vault"
)

// DriveRoot is where mirrored folders live in the orOS disk. orOS
// lists these on demand (vault.js VD-OD): phones and browsers download
// a file only when it is opened.
const DriveRoot = vault.RootPath + "/Drive"

// Pair is one mirrored folder.
type Pair struct {
	ID     string `json:"id"`
	Local  string `json:"local"`  // absolute folder on this computer
	Remote string `json:"remote"` // "/internal/Drive/<device>/<name>"
	Paused bool   `json:"paused,omitempty"`
}

// Name is the folder's display name.
func (p Pair) Name() string { return path.Base(p.Remote) }

// Options tune the safety limits.
type Options struct {
	MaxFileSize int64         // bigger files stay on this computer only (default 500 MB)
	MaxDeletes  int           // more deletions than this in one folder wait for a yes (default 50)
	StableAfter time.Duration // a file changed more recently than this is still being written (default 3 s)
	BatchFiles  int           // uploads per manifest commit (default 100)
	BatchBytes  int64         // upload bytes per manifest commit (default 256 MB)
}

func (o Options) withDefaults() Options {
	if o.MaxFileSize <= 0 {
		o.MaxFileSize = 500 << 20
	}
	if o.MaxDeletes <= 0 {
		o.MaxDeletes = 50
	}
	if o.StableAfter == 0 {
		o.StableAfter = 3 * time.Second
	}
	if o.BatchFiles <= 0 {
		o.BatchFiles = 100
	}
	if o.BatchBytes <= 0 {
		o.BatchBytes = 256 << 20
	}
	return o
}

// Event is one thing the engine did, for the activity list.
type Event struct {
	Time   time.Time `json:"time"`
	Kind   string    `json:"kind"` // an Action name, "conflict-copy", "skip", "error"
	Folder string    `json:"folder"`
	Path   string    `json:"path"`
	Detail string    `json:"detail,omitempty"`
}

// Folder states.
const (
	StateOK      = "ok"
	StatePaused  = "paused"
	StateMissing = "missing" // the folder is gone (unplugged disk?): nothing is touched
	StateHeld    = "held"    // many deletions wait for the user's yes
	StateReset   = "reset"   // the vault is empty although this folder was synced
	StateError   = "error"
)

// FolderStatus is the outcome of a pass for one folder.
type FolderStatus struct {
	ID          string    `json:"id"`
	State       string    `json:"state"`
	Error       string    `json:"error,omitempty"`
	Files       int       `json:"files"`
	HeldDeletes int       `json:"heldDeletes,omitempty"`
	LocalOnly   []string  `json:"localOnly,omitempty"` // too big to upload
	Skipped     []string  `json:"skipped,omitempty"`   // cloud names this system cannot hold, case clashes, unreadable
	LastSync    time.Time `json:"lastSync,omitempty"`
}

// Result sums up a pass.
type Result struct {
	Uploaded      int                      `json:"uploaded"`
	Downloaded    int                      `json:"downloaded"`
	DeletedLocal  int                      `json:"deletedLocal"`
	DeletedRemote int                      `json:"deletedRemote"`
	Conflicts     int                      `json:"conflicts"`
	Failed        int                      `json:"failed"`
	Busy          bool                     `json:"busy"` // files were still being written: run again soon
	Folders       map[string]*FolderStatus `json:"folders"`
}

// Engine runs passes. One pass at a time.
type Engine struct {
	Vault    *vault.Vault
	StateDir string
	Trash    Trash
	Opt      Options
	Now      func() time.Time
	Log      func(Event)
	// CanCreate: allowed to write the first manifest of an empty vault.
	// The app sets it only when the vault key in hand belongs to the
	// key file in the cloud, so a reset vault is never rebuilt with an
	// old key behind orOS's back.
	CanCreate func() error

	mu sync.Mutex
}

var ErrTooManyConflicts = errors.New("the vault kept changing during the sync; will try again")

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Engine) log(kind, folder, p, detail string) {
	if e.Log != nil {
		e.Log(Event{Time: e.now(), Kind: kind, Folder: folder, Path: p, Detail: detail})
	}
}

// Sync runs one pass over pairs. approve holds the folders whose held
// deletions the user confirmed. Errors that stop the whole pass (no
// network, vault locked) are returned; per-folder trouble is in the
// result.
func (e *Engine) Sync(pairs []Pair, approve map[string]bool) (*Result, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for tries := 0; ; tries++ {
		remote, err := e.Vault.FetchManifest()
		if err != nil {
			return nil, err
		}
		res, err := e.pass(remote, pairs, approve)
		if errors.Is(err, vault.ErrConflict) {
			if tries < 2 {
				continue
			}
			return res, ErrTooManyConflicts
		}
		return res, err
	}
}

// work is one file to send up or one path to remove from the vault.
type work struct {
	pair   *Pair
	st     *State
	rel    string
	abs    string
	hash   string // expected content
	size   int64
	mtime  int64
	delete bool
	copyOf string // conflict copy of this path: its marker clears after the commit
}

func (e *Engine) pass(remote *vault.Remote, pairs []Pair, approve map[string]bool) (*Result, error) {
	opt := e.Opt.withDefaults()
	res := &Result{Folders: map[string]*FolderStatus{}}
	states := map[string]*State{}
	var todo []*work

	for i := range pairs {
		p := &pairs[i]
		fs := &FolderStatus{ID: p.ID, State: StateOK}
		res.Folders[p.ID] = fs
		if p.Paused {
			fs.State = StatePaused
			continue
		}
		st, err := LoadState(e.StateDir, p.ID)
		if err != nil {
			fs.State, fs.Error = StateError, err.Error()
			continue
		}
		if info, err := os.Stat(p.Local); err != nil || !info.IsDir() {
			fs.State = StateMissing
			continue
		}
		// VD-1 for the client: a vault with no manifest is empty, never
		// "everything was deleted". If this folder was synced before,
		// the vault was reset (or is being rebuilt): stop and ask.
		if !remote.Exists && st.synced() {
			fs.State = StateReset
			continue
		}
		states[p.ID] = st
		w, err := e.planFolder(p, st, remote, approve[p.ID], opt, res, fs)
		if err != nil {
			fs.State, fs.Error = StateError, err.Error()
		}
		if err := st.Save(e.StateDir, p.ID); err != nil {
			fs.State, fs.Error = StateError, err.Error()
			continue
		}
		todo = append(todo, w...)
	}

	if len(todo) > 0 && !remote.Exists && e.CanCreate != nil {
		if err := e.CanCreate(); err != nil {
			return res, err
		}
	}
	if err := e.commit(remote, todo, opt, res); err != nil {
		return res, err
	}
	for id, st := range states {
		if err := st.Save(e.StateDir, id); err != nil {
			res.Folders[id].State, res.Folders[id].Error = StateError, err.Error()
		}
		if res.Folders[id].State == StateOK || res.Folders[id].State == StateHeld {
			res.Folders[id].LastSync = e.now()
		}
	}
	return res, nil
}

func under(paths []string, rel string) bool {
	for _, u := range paths {
		if rel == u || strings.HasPrefix(rel, u+"/") {
			return true
		}
	}
	return false
}

// planFolder applies the cloud's changes to one folder and returns the
// uploads and vault removals it needs.
func (e *Engine) planFolder(p *Pair, st *State, remote *vault.Remote, approved bool, opt Options, res *Result, fs *FolderStatus) ([]*work, error) {
	sc, err := scan(p.Local, st, e.now(), opt.StableAfter)
	if err != nil {
		return nil, err
	}
	if sc.busy {
		res.Busy = true
	}
	fs.Skipped = append(fs.Skipped, sc.unread...)

	// The cloud side of this folder, as relative paths.
	prefix := p.Remote + "/"
	cloud := map[string]vault.Entry{}
	cloudFold := map[string]string{}
	for full, ent := range remote.Files {
		if !strings.HasPrefix(full, prefix) {
			continue
		}
		rel := full[len(prefix):]
		if ignoredRel(rel) {
			continue // a lock file (or one inside a folder the scan skips): never mirrored, never deleted
		}
		if !RelOK(rel, goos) {
			fs.Skipped = append(fs.Skipped, rel)
			continue
		}
		if caseFolds(goos) {
			k := strings.ToLower(rel)
			if other, ok := cloudFold[k]; ok {
				// Two cloud files that are one file here: keep the
				// first (sorted), skip the other.
				keep, skip := other, rel
				if rel < other {
					keep, skip = rel, other
					cloud[keep] = ent
					delete(cloud, skip)
					cloudFold[k] = keep
				}
				fs.Skipped = append(fs.Skipped, skip)
				continue
			}
			cloudFold[k] = rel
		}
		cloud[rel] = ent
	}

	all := map[string]bool{}
	for rel := range sc.files {
		all[rel] = true
	}
	for rel := range cloud {
		all[rel] = true
	}
	for rel := range st.Files {
		all[rel] = true
	}
	rels := make([]string, 0, len(all))
	for rel := range all {
		rels = append(rels, rel)
	}
	sort.Strings(rels)

	type step struct {
		rel string
		act Action
		lf  *localFile
		ent vault.Entry
	}
	var steps []step
	deletes := 0
	for _, rel := range rels {
		lf := sc.files[rel]
		if lf != nil && lf.Unknown {
			continue
		}
		if lf == nil && under(sc.unread, rel) {
			continue // an unreadable folder is not a deleted one
		}
		ent, inCloud := cloud[rel]
		if !inCloud && skippedHere(fs.Skipped, rel) {
			continue
		}
		local, rem, base := "", "", ""
		if lf != nil {
			local = lf.Hash
		}
		if inCloud {
			rem = ent.H
		}
		if r := st.Files[rel]; r != nil {
			base = r.Base
		}
		act := decide(base, local, rem)
		if act == Same {
			if local == "" {
				delete(st.Files, rel)
			} else {
				st.Files[rel] = &Record{Size: lf.Size, MTime: lf.MTime, Local: local, Base: local}
			}
			delete(st.Conflicts, rel)
			continue
		}
		if lf != nil {
			// Remember the hash, so an unchanged file is not read again.
			r := st.Files[rel]
			if r == nil {
				r = &Record{}
				st.Files[rel] = r
			}
			r.Size, r.MTime, r.Local = lf.Size, lf.MTime, lf.Hash
		}
		if act == DeleteLocal || act == DeleteRemote {
			deletes++
		}
		steps = append(steps, step{rel, act, lf, ent})
	}

	held := deletes > opt.MaxDeletes && !approved
	if held {
		fs.State, fs.HeldDeletes = StateHeld, deletes
	}

	var todo []*work
	for _, s := range steps {
		abs := filepath.Join(p.Local, filepath.FromSlash(s.rel))
		switch s.act {
		case Download:
			if other, ok := sc.fold[strings.ToLower(s.rel)]; ok && caseFolds(goos) && other != s.rel {
				fs.Skipped = append(fs.Skipped, s.rel)
				continue
			}
			if err := e.download(p, st, s.rel, abs, s.ent, s.lf); err != nil {
				res.Failed++
				e.log("error", p.Name(), s.rel, err.Error())
				continue
			}
			res.Downloaded++
			e.log("download", p.Name(), s.rel, "")
		case DeleteLocal:
			if held {
				continue
			}
			if err := e.removeLocal(p, s.rel, abs, s.lf); err != nil {
				res.Failed++
				e.log("error", p.Name(), s.rel, err.Error())
				continue
			}
			delete(st.Files, s.rel)
			res.DeletedLocal++
			e.log("delete-local", p.Name(), s.rel, "")
		case DeleteRemote:
			if held {
				continue
			}
			todo = append(todo, &work{pair: p, st: st, rel: s.rel, delete: true})
		case Upload:
			if s.lf.Size > opt.MaxFileSize {
				fs.LocalOnly = append(fs.LocalOnly, s.rel)
				continue
			}
			todo = append(todo, &work{pair: p, st: st, rel: s.rel, abs: abs, hash: s.lf.Hash, size: s.lf.Size, mtime: s.lf.MTime})
		case Conflict:
			if s.lf.Size > opt.MaxFileSize {
				fs.LocalOnly = append(fs.LocalOnly, s.rel)
				continue
			}
			w, err := e.conflictCopy(p, st, s.rel, s.ent, sc, cloud)
			if err != nil {
				res.Failed++
				e.log("error", p.Name(), s.rel, err.Error())
				continue
			}
			res.Conflicts++
			if w != nil {
				todo = append(todo, w)
			}
			todo = append(todo, &work{pair: p, st: st, rel: s.rel, abs: abs, hash: s.lf.Hash, size: s.lf.Size, mtime: s.lf.MTime})
		}
	}
	for rel, r := range st.Files {
		if r.Base == "" && sc.files[rel] == nil {
			delete(st.Files, rel)
		}
	}
	fs.Files = len(sc.files)
	return todo, nil
}

// ignoredRel: any segment is an ignored name. The scan skips ignored
// folders whole, so a cloud file inside one must be skipped too, or it
// would look deleted here right after its download.
func ignoredRel(rel string) bool {
	for _, seg := range strings.Split(rel, "/") {
		if Ignored(seg) {
			return true
		}
	}
	return false
}

func skippedHere(skipped []string, rel string) bool {
	for _, s := range skipped {
		if s == rel {
			return true
		}
	}
	return false
}

var errChanged = errors.New("changed on this computer while syncing; will sync next time")

// unchanged: the file is still what the scan saw (VD-4: a local change
// made during the pass is never overwritten or removed).
func unchanged(abs string, lf *localFile) bool {
	info, err := os.Lstat(abs)
	if lf == nil {
		return errors.Is(err, os.ErrNotExist)
	}
	return err == nil && info.Mode().IsRegular() && info.Size() == lf.Size && info.ModTime().UnixNano() == lf.MTime
}

func (e *Engine) download(p *Pair, st *State, rel, abs string, ent vault.Entry, lf *localFile) error {
	plain, err := e.Vault.Download(ent)
	if err != nil {
		return err
	}
	if !unchanged(abs, lf) {
		return errChanged
	}
	rec, err := writeAtomic(abs, plain, ent.H)
	if err != nil {
		return err
	}
	st.Files[rel] = rec
	return nil
}

// writeAtomic writes into a temp file beside abs and renames it over,
// so no reader ever sees half a file. The record it returns makes the
// next scan see the engine's own write as already synced.
func writeAtomic(abs string, data []byte, hash string) (*Record, error) {
	dir := filepath.Dir(abs)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	var rnd [6]byte
	rand.Read(rnd[:])
	tmp := filepath.Join(dir, tmpPrefix+hex.EncodeToString(rnd[:]))
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return nil, err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return nil, err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return nil, err
	}
	if err := os.Rename(tmp, abs); err != nil {
		os.Remove(tmp)
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	return &Record{Size: info.Size(), MTime: info.ModTime().UnixNano(), Local: hash, Base: hash}, nil
}

func (e *Engine) removeLocal(p *Pair, rel, abs string, lf *localFile) error {
	if !unchanged(abs, lf) {
		return errChanged
	}
	if e.Trash != nil {
		if err := e.Trash.Move(abs, p.Name(), rel); err != nil {
			return err
		}
	} else if err := os.Remove(abs); err != nil {
		return err
	}
	// Remove folders the deletion left empty, up to the mirrored folder.
	root := filepath.Clean(p.Local)
	for d := filepath.Dir(abs); d != root && strings.HasPrefix(d, root); d = filepath.Dir(d) {
		if os.Remove(d) != nil {
			break
		}
	}
	return nil
}

// conflictCopy writes the cloud's version beside the local file, once
// per cloud version, and returns the upload of the copy (its object is
// already in the vault, so only the manifest entry goes up).
func (e *Engine) conflictCopy(p *Pair, st *State, rel string, ent vault.Entry, sc *scanResult, cloud map[string]vault.Entry) (*work, error) {
	if st.Conflicts[rel] == ent.H {
		return nil, nil
	}
	plain, err := e.Vault.Download(ent)
	if err != nil {
		return nil, err
	}
	now := e.now()
	var crel string
	for n := 0; ; n++ {
		crel = ConflictName(rel, now, n)
		_, inScan := sc.files[crel]
		_, inCloud := cloud[crel]
		_, statErr := os.Lstat(filepath.Join(p.Local, filepath.FromSlash(crel)))
		if !inScan && !inCloud && errors.Is(statErr, os.ErrNotExist) {
			break
		}
	}
	cabs := filepath.Join(p.Local, filepath.FromSlash(crel))
	rec, err := writeAtomic(cabs, plain, ent.H)
	if err != nil {
		return nil, err
	}
	rec.Base = "" // not in the cloud under this name yet
	st.Files[crel] = rec
	st.Conflicts[rel] = ent.H
	e.log("conflict-copy", p.Name(), crel, rel)
	return &work{pair: p, st: st, rel: crel, abs: cabs, hash: ent.H, size: rec.Size, mtime: rec.MTime, copyOf: rel}, nil
}

func mimeOf(rel string) string {
	t := mime.TypeByExtension(strings.ToLower(path.Ext(rel)))
	if i := strings.IndexByte(t, ';'); i >= 0 {
		t = t[:i]
	}
	if t == "" {
		return "application/octet-stream"
	}
	return t
}

// commit uploads and removes in batches, one conditional manifest
// write per batch. State is updated only for batches the cloud took.
func (e *Engine) commit(remote *vault.Remote, todo []*work, opt Options, res *Result) error {
	for len(todo) > 0 {
		var batch []*work
		var bytes int64
		for len(todo) > 0 && len(batch) < opt.BatchFiles && (len(batch) == 0 || bytes+todo[0].size <= opt.BatchBytes) {
			bytes += todo[0].size
			batch, todo = append(batch, todo[0]), todo[1:]
		}
		var done []*work
		for _, w := range batch {
			full := w.pair.Remote + "/" + w.rel
			if w.delete {
				delete(remote.Files, full)
				done = append(done, w)
				continue
			}
			data, err := os.ReadFile(w.abs)
			if err != nil {
				res.Failed++
				e.log("error", w.pair.Name(), w.rel, err.Error())
				continue
			}
			if vault.SHA256Hex(data) != w.hash {
				res.Busy = true // changed since the scan: next pass
				continue
			}
			ent, err := e.Vault.Upload(remote, data, mimeOf(w.rel))
			if err != nil {
				return err
			}
			remote.Files[full] = ent
			done = append(done, w)
		}
		if len(done) == 0 {
			continue
		}
		if err := e.Vault.Commit(remote); err != nil {
			return err
		}
		for _, w := range done {
			if w.delete {
				delete(w.st.Files, w.rel)
				res.DeletedRemote++
				e.log("delete-remote", w.pair.Name(), w.rel, "")
				continue
			}
			w.st.Files[w.rel] = &Record{Size: w.size, MTime: w.mtime, Local: w.hash, Base: w.hash}
			if w.copyOf == "" {
				res.Uploaded++
				e.log("upload", w.pair.Name(), w.rel, "")
			}
		}
		saved := map[string]bool{}
		for _, w := range done {
			if w.copyOf == "" {
				delete(w.st.Conflicts, w.rel) // the conflict is over once the local version is up
			}
			if saved[w.pair.ID] {
				continue
			}
			saved[w.pair.ID] = true
			if err := w.st.Save(e.StateDir, w.pair.ID); err != nil {
				return fmt.Errorf("saving state: %w", err)
			}
		}
	}
	return nil
}
