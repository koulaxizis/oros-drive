// Package vdisk shows the whole orOS disk as a file system (wave 5):
// the tree comes from the vault manifest, a file is downloaded when it
// is opened and kept in a local cache, and a write is uploaded and
// committed when the file is closed. FUSE on Linux (vdisk/fuse) and the
// Android DocumentsProvider (mobile/) sit on top of it.
//
// It follows the same rules as the mirror:
//   - every manifest write is conditional on the revision read; a
//     refused write reads the manifest again and re-applies the change;
//   - a file changed elsewhere while it was open here keeps both: this
//     write takes the name, the other version becomes a conflict copy
//     (FILES-V, VD-4);
//   - a deletion of a file that changed elsewhere since it was listed
//     does nothing (an edit beats a deletion);
//   - downloads are hash-checked (VD-H) and cached by content hash, so a
//     cached file is never stale;
//   - lock, swap and temp files (mirror.Ignored names) stay on this
//     device: programs that save through "write a .tmp, rename it over
//     the file" work, and only the final file reaches the vault.
package vdisk

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/koulaxizis/oros-drive/mirror"
	"github.com/koulaxizis/oros-drive/vault"
)

var (
	ErrNotExist = fs.ErrNotExist
	ErrExist    = fs.ErrExist
	ErrNotEmpty = errors.New("folder not empty")
	ErrIsDir    = errors.New("is a folder")
	ErrNotDir   = errors.New("not a folder")
	ErrTooBig   = errors.New("file too big for the vault")
	ErrBadName  = errors.New("name orOS cannot hold")
)

// Root is the disk's root as the manifest names it.
const Root = vault.RootPath

// Info describes one file or folder.
type Info struct {
	Path   string    `json:"path"` // disk path, "/internal/Docs/a.txt"
	Name   string    `json:"name"`
	Dir    bool      `json:"dir,omitempty"`
	Size   int64     `json:"size"`
	MTime  time.Time `json:"mtime"`
	Hash   string    `json:"hash,omitempty"`
	Mime   string    `json:"mime,omitempty"`
	Cached bool      `json:"cached,omitempty"` // downloaded on this device
	Pinned bool      `json:"pinned,omitempty"` // kept offline (a pin or under one)
}

// Disk is the vault seen as a file system. Safe for concurrent use.
type Disk struct {
	Vault    *vault.Vault
	CacheDir string        // downloaded files (by hash) and files being written
	MaxCache int64         // bytes kept in the cache; 0 = 2 GB
	MaxFile  int64         // largest file written; 0 = 500 MB
	TTL      time.Duration // how long a read manifest counts as current; 0 = 30 s

	mu      sync.Mutex
	remote  *vault.Remote
	fetched time.Time
	dirs    map[string]bool // folders made here that hold no file yet
	scratch map[string]scratchFile
	pins    map[string]bool // offline paths (pins.go); nil until read
	fetchMu sync.Mutex      // one download at a time per disk
	now     func() time.Time
}

func (d *Disk) clock() time.Time {
	if d.now != nil {
		return d.now()
	}
	return time.Now()
}

func (d *Disk) maxCache() int64 {
	if d.MaxCache > 0 {
		return d.MaxCache
	}
	return 2 << 30
}

func (d *Disk) maxFile() int64 {
	if d.MaxFile > 0 {
		return d.MaxFile
	}
	return 500 << 20
}

func (d *Disk) ttl() time.Duration {
	if d.TTL > 0 {
		return d.TTL
	}
	return 30 * time.Second
}

// scratchFile is a temp or lock file kept on this device only.
type scratchFile struct {
	local string
	mtime time.Time
}

// isScratch: a file whose name is never synced (mirror.Ignored).
func isScratch(p string) bool {
	return p != Root && mirror.Ignored(path.Base(p)) && nameOK(path.Dir(p))
}

func (d *Disk) scratchInfo(p string, s scratchFile) Info {
	var size int64
	if fi, err := os.Stat(s.local); err == nil {
		size = fi.Size()
	}
	return Info{Path: p, Name: path.Base(p), Size: size, MTime: s.mtime, Cached: true}
}

// putScratch keeps a temp file's content on this device.
func (d *Disk) putScratch(p, local string) (Info, error) {
	sum := sha256.Sum256([]byte(p))
	dst := filepath.Join(d.CacheDir, "scratch", hex.EncodeToString(sum[:16]))
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return Info{}, err
	}
	if err := copyFile(local, dst); err != nil {
		return Info{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.scratch == nil {
		d.scratch = map[string]scratchFile{}
	}
	s := scratchFile{local: dst, mtime: d.clock()}
	d.scratch[p] = s
	return d.scratchInfo(p, s), nil
}

func copyFile(src, dst string) error {
	if src == dst {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".part"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, dst)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// Clean turns any user or disk path into the disk form, or "" when it
// is not one (outside the disk, "..", empty segments).
func Clean(p string) string {
	p = vault.DiskPath(p)
	if p == Root {
		return p
	}
	if vault.CheckPath(p) != nil {
		return ""
	}
	return p
}

// Refresh reads the manifest again when the copy in hand is older than
// TTL (or always, with force).
func (d *Disk) Refresh(force bool) error {
	d.mu.Lock()
	fresh := d.remote != nil && !force && d.clock().Sub(d.fetched) < d.ttl()
	d.mu.Unlock()
	if fresh {
		return nil
	}
	r, err := d.Vault.FetchManifest()
	if err != nil {
		return err
	}
	d.setRemote(r)
	return nil
}

func (d *Disk) setRemote(r *vault.Remote) {
	d.mu.Lock()
	d.remote, d.fetched = r, d.clock()
	// A folder made here is shown until a file lands in it.
	for dir := range d.dirs {
		if hasPrefix(r.Files, dir) {
			delete(d.dirs, dir)
		}
	}
	d.mu.Unlock()
}

func hasPrefix(files map[string]vault.Entry, dir string) bool {
	for p := range files {
		if strings.HasPrefix(p, dir+"/") {
			return true
		}
	}
	return false
}

// snapshot returns the current manifest (refreshing it when stale).
func (d *Disk) snapshot() (*vault.Remote, error) {
	if err := d.Refresh(false); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.remote, nil
}

func (d *Disk) isDir(r *vault.Remote, p string) bool {
	if p == Root || d.dirs[p] {
		return true
	}
	return hasPrefix(r.Files, p)
}

func (d *Disk) info(p string, e vault.Entry) Info {
	size, _ := e.Size()
	return Info{Path: p, Name: path.Base(p), Size: size, MTime: e.ModTime(), Hash: e.H, Mime: e.Mime,
		Cached: d.cached(e.H)}
}

// Stat describes one path.
func (d *Disk) Stat(p string) (Info, error) {
	if p = Clean(p); p == "" {
		return Info{}, ErrNotExist
	}
	r, err := d.snapshot()
	if err != nil {
		return Info{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if s, ok := d.scratch[p]; ok {
		return d.scratchInfo(p, s), nil
	}
	if e, ok := r.Files[p]; ok {
		i := d.info(p, e)
		i.Pinned = d.pinnedLocked(p)
		return i, nil
	}
	if d.isDir(r, p) {
		return Info{Path: p, Name: path.Base(p), Dir: true, Pinned: d.pinnedLocked(p)}, nil
	}
	return Info{}, ErrNotExist
}

// List returns a folder's entries, folders first, then by name.
func (d *Disk) List(dir string) ([]Info, error) {
	if dir = Clean(dir); dir == "" {
		return nil, ErrNotExist
	}
	r, err := d.snapshot()
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := r.Files[dir]; ok {
		return nil, ErrNotDir
	}
	if !d.isDir(r, dir) {
		return nil, ErrNotExist
	}
	seen := map[string]Info{}
	add := func(p string, e *vault.Entry) {
		rest := strings.TrimPrefix(p, dir+"/")
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			name := rest[:i]
			if _, ok := seen[name]; !ok {
				seen[name] = Info{Path: dir + "/" + name, Name: name, Dir: true}
			}
			return
		}
		if e != nil {
			seen[rest] = d.info(p, *e)
		} else if _, ok := seen[rest]; !ok {
			seen[rest] = Info{Path: p, Name: rest, Dir: true}
		}
	}
	for p, e := range r.Files {
		if strings.HasPrefix(p, dir+"/") {
			e := e
			add(p, &e)
		}
	}
	for p := range d.dirs {
		if strings.HasPrefix(p, dir+"/") {
			add(p, nil)
		}
	}
	for p, sf := range d.scratch {
		if path.Dir(p) == dir {
			seen[path.Base(p)] = d.scratchInfo(p, sf)
		}
	}
	out := make([]Info, 0, len(seen))
	for _, i := range seen {
		i.Pinned = d.pinnedLocked(i.Path)
		out = append(out, i)
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Dir != out[b].Dir {
			return out[a].Dir
		}
		return out[a].Name < out[b].Name
	})
	return out, nil
}

// Tree returns every file and folder on the disk (not the root and
// not this device's temp files), keyed by disk path, and whether the
// vault has a manifest at all (false: an empty or reset vault, where
// VD-1 says nothing may be deleted). force reads the manifest again.
func (d *Disk) Tree(force bool) (map[string]Info, bool, error) {
	if err := d.Refresh(force); err != nil {
		return nil, false, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	r := d.remote
	out := make(map[string]Info, len(r.Files))
	addDirs := func(p string) {
		for dir := path.Dir(p); dir != Root && dir != "/" && dir != "."; dir = path.Dir(dir) {
			if _, ok := out[dir]; ok {
				break
			}
			out[dir] = Info{Path: dir, Name: path.Base(dir), Dir: true}
		}
	}
	for p, e := range r.Files {
		out[p] = d.info(p, e)
		addDirs(p)
	}
	for p := range d.dirs {
		if _, ok := out[p]; !ok {
			out[p] = Info{Path: p, Name: path.Base(p), Dir: true}
		}
		addDirs(p)
	}
	return out, r.Exists, nil
}

// FetchHash is Fetch by content: the cached copy of hash, or the
// download of any file in the manifest with that content. A program
// that opens a file Windows still shows under an older name (renamed
// here or elsewhere) gets exactly the content it was listed with.
func (d *Disk) FetchHash(hash string) (string, error) {
	if hash == "" {
		return "", ErrNotExist
	}
	if d.cached(hash) {
		local := d.objPath(hash)
		now := d.clock()
		_ = os.Chtimes(local, now, now)
		return local, nil
	}
	for try := 0; try < 2; try++ {
		r, err := d.snapshot()
		if err != nil {
			return "", err
		}
		d.mu.Lock()
		var found *vault.Entry
		for _, e := range r.Files {
			if e.H == hash {
				e := e
				found = &e
				break
			}
		}
		d.mu.Unlock()
		if found != nil {
			return d.fetchEntry(*found)
		}
		if err := d.Refresh(true); err != nil {
			return "", err
		}
	}
	return "", ErrNotExist
}

// ---------- cache ----------

func (d *Disk) objPath(hash string) string { return filepath.Join(d.CacheDir, "objects", hash) }

func (d *Disk) cached(hash string) bool {
	if hash == "" || d.CacheDir == "" {
		return false
	}
	_, err := os.Stat(d.objPath(hash))
	return err == nil
}

// Fetch returns a local, read-only copy of a file's content: the cache
// file named by its hash (downloaded and hash-checked first if needed).
// Callers must not write to it.
func (d *Disk) Fetch(p string) (string, Info, error) {
	if p = Clean(p); p == "" {
		return "", Info{}, ErrNotExist
	}
	r, err := d.snapshot()
	if err != nil {
		return "", Info{}, err
	}
	d.mu.Lock()
	if sf, ok := d.scratch[p]; ok {
		d.mu.Unlock()
		return sf.local, d.scratchInfo(p, sf), nil
	}
	e, ok := r.Files[p]
	dir := !ok && d.isDir(r, p)
	d.mu.Unlock()
	if !ok {
		if dir {
			return "", Info{}, ErrIsDir
		}
		return "", Info{}, ErrNotExist
	}
	local, err := d.fetchEntry(e)
	if err != nil {
		return "", Info{}, err
	}
	return local, d.info(p, e), nil
}

func (d *Disk) fetchEntry(e vault.Entry) (string, error) {
	local := d.objPath(e.H)
	d.fetchMu.Lock()
	defer d.fetchMu.Unlock()
	if _, err := os.Stat(local); err == nil {
		now := d.clock()
		_ = os.Chtimes(local, now, now) // most recently used
		return local, nil
	}
	plain, err := d.Vault.Download(e) // VD-H checked
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(local), 0o700); err != nil {
		return "", err
	}
	tmp := local + ".part"
	if err := os.WriteFile(tmp, plain, 0o400); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, local); err != nil {
		os.Remove(tmp)
		return "", err
	}
	d.prune(local)
	return local, nil
}

// prune drops the least recently used cache files above MaxCache,
// never keep (just fetched) and never an offline file (pins.go, which
// do not count toward MaxCache either). An open file stays readable on
// Linux and Android after its name is gone.
func (d *Disk) prune(keep string) {
	pinned, known := d.pinnedHashes()
	if !known {
		return
	}
	dir := filepath.Join(d.CacheDir, "objects")
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type f struct {
		p string
		n int64
		t time.Time
	}
	var files []f
	var total int64
	for _, e := range ents {
		fi, err := e.Info()
		if err != nil || e.IsDir() || strings.HasSuffix(e.Name(), ".part") || pinned[e.Name()] {
			continue
		}
		files = append(files, f{filepath.Join(dir, e.Name()), fi.Size(), fi.ModTime()})
		total += fi.Size()
	}
	sort.Slice(files, func(a, b int) bool { return files[a].t.Before(files[b].t) })
	for _, x := range files {
		if total <= d.maxCache() {
			break
		}
		if x.p == keep {
			continue
		}
		if os.Remove(x.p) == nil {
			total -= x.n
		}
	}
}

// CacheSize returns the bytes the cache holds.
func (d *Disk) CacheSize() int64 {
	var total int64
	ents, _ := os.ReadDir(filepath.Join(d.CacheDir, "objects"))
	for _, e := range ents {
		if fi, err := e.Info(); err == nil && !e.IsDir() {
			total += fi.Size()
		}
	}
	return total
}

// ClearCache removes every downloaded copy (they come back on open)
// except offline files.
func (d *Disk) ClearCache() error {
	if len(d.Pins()) == 0 {
		d.fetchMu.Lock()
		defer d.fetchMu.Unlock()
		return os.RemoveAll(filepath.Join(d.CacheDir, "objects"))
	}
	if err := d.Refresh(true); err != nil {
		return err // what the pins hold is unknown: keep everything
	}
	pinned, _ := d.pinnedHashes()
	d.fetchMu.Lock()
	defer d.fetchMu.Unlock()
	dir := filepath.Join(d.CacheDir, "objects")
	ents, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, e := range ents {
		if !pinned[e.Name()] {
			if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

// TempFile is a scratch file in the cache folder for a write in
// progress (same file system as the cache, so it can be renamed in).
func (d *Disk) TempFile() (*os.File, error) {
	dir := filepath.Join(d.CacheDir, "writing")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return os.CreateTemp(dir, "w-*")
}

// ---------- changes ----------

// change applies fn to a fresh copy of the manifest and commits it on
// the revision read, re-reading and re-applying on a refused write.
func (d *Disk) change(fn func(files map[string]vault.Entry) error) error {
	for try := 0; try < 5; try++ {
		r, err := d.Vault.FetchManifest()
		if err != nil {
			return err
		}
		work := *r
		work.Files = make(map[string]vault.Entry, len(r.Files)+1)
		for p, e := range r.Files {
			work.Files[p] = e
		}
		if err := fn(work.Files); err != nil {
			d.setRemote(r)
			return err
		}
		err = d.Vault.Commit(&work)
		if errors.Is(err, vault.ErrConflict) {
			continue
		}
		if err != nil {
			return err
		}
		d.setRemote(&work)
		return nil
	}
	return vault.ErrConflict
}

func nameOK(p string) bool {
	for _, seg := range strings.Split(strings.TrimPrefix(p, Root+"/"), "/") {
		if mirror.Ignored(seg) || strings.ContainsAny(seg, "\x00\\") {
			return false
		}
	}
	return true
}

// Put stores the content of local at p. base is the hash the writer
// started from ("" for a new file or a full overwrite of an unknown
// one): when the file changed elsewhere since, both versions are kept.
func (d *Disk) Put(p, local, base string) (Info, error) {
	if p = Clean(p); p == "" || p == Root {
		return Info{}, ErrBadName
	}
	if isScratch(p) {
		return d.putScratch(p, local)
	}
	if !nameOK(p) {
		return Info{}, ErrBadName
	}
	fi, err := os.Stat(local)
	if err != nil {
		return Info{}, err
	}
	if fi.Size() > d.maxFile() {
		return Info{}, ErrTooBig
	}
	plain, err := os.ReadFile(local)
	if err != nil {
		return Info{}, err
	}
	r, err := d.snapshot()
	if err != nil {
		return Info{}, err
	}
	d.mu.Lock()
	if d.isDir(r, p) {
		d.mu.Unlock()
		return Info{}, ErrIsDir
	}
	d.mu.Unlock()
	mt := mime.TypeByExtension(path.Ext(p))
	if i := strings.IndexByte(mt, ';'); i >= 0 {
		mt = mt[:i]
	}
	e, err := d.Vault.Upload(r, plain, mt)
	if err != nil {
		return Info{}, err
	}
	err = d.change(func(files map[string]vault.Entry) error {
		if hasPrefix(files, p) {
			return ErrIsDir
		}
		if cur, ok := files[p]; ok && cur.H != e.H && cur.H != base {
			// Changed elsewhere while open here: ours keeps the name,
			// theirs is kept beside it (FILES-V).
			now := d.clock()
			name := mirror.ConflictName(p, now, 0)
			for n := 1; ; n++ {
				if _, taken := files[name]; !taken {
					break
				}
				name = mirror.ConflictName(p, now, n)
			}
			files[name] = cur
		}
		files[p] = e
		return nil
	})
	if err != nil {
		return Info{}, err
	}
	d.keep(e.H, plain)
	return d.info(p, e), nil
}

var errUnchanged = errors.New("nothing to change")

// PutNew stores the content of local as a new file named name in dir
// and never replaces anything (sharing to orOS, camera uploads): when
// that name holds the same content already, nothing is written (a
// retried upload); when it holds something else, the file becomes
// "name (2).ext", "name (3).ext", ... It returns the file as stored.
func (d *Disk) PutNew(dir, name, local string) (Info, error) {
	if dir = Clean(dir); dir == "" || name == "" || strings.ContainsAny(name, "/\\\x00") {
		return Info{}, ErrBadName
	}
	if Clean(dir+"/"+name) != dir+"/"+name || !nameOK(dir+"/"+name) {
		return Info{}, ErrBadName
	}
	fi, err := os.Stat(local)
	if err != nil {
		return Info{}, err
	}
	if fi.Size() > d.maxFile() {
		return Info{}, ErrTooBig
	}
	plain, err := os.ReadFile(local)
	if err != nil {
		return Info{}, err
	}
	r, err := d.snapshot()
	if err != nil {
		return Info{}, err
	}
	mt := mime.TypeByExtension(path.Ext(name))
	if i := strings.IndexByte(mt, ';'); i >= 0 {
		mt = mt[:i]
	}
	e, err := d.Vault.Upload(r, plain, mt)
	if err != nil {
		return Info{}, err
	}
	ext := path.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	var at string
	err = d.change(func(files map[string]vault.Entry) error {
		if _, ok := files[dir]; ok {
			return ErrNotDir
		}
		for n := 1; ; n++ {
			at = dir + "/" + name
			if n > 1 {
				at = fmt.Sprintf("%s/%s (%d)%s", dir, stem, n, ext)
			}
			cur, taken := files[at]
			if taken && cur.H == e.H {
				return errUnchanged
			}
			if !taken && !hasPrefix(files, at) {
				files[at] = e
				return nil
			}
		}
	})
	if err != nil && !errors.Is(err, errUnchanged) {
		return Info{}, err
	}
	d.keep(e.H, plain)
	return d.info(at, e), nil
}

// keep puts content just uploaded into the cache, so opening it again
// needs no download.
func (d *Disk) keep(hash string, plain []byte) {
	if d.CacheDir == "" || d.cached(hash) {
		return
	}
	local := d.objPath(hash)
	if os.MkdirAll(filepath.Dir(local), 0o700) != nil {
		return
	}
	tmp := local + ".part"
	if os.WriteFile(tmp, plain, 0o400) == nil && os.Rename(tmp, local) == nil {
		d.prune(local)
	} else {
		os.Remove(tmp)
	}
}

// PutReader is Put from a stream (DocumentsProvider writes).
func (d *Disk) PutReader(p string, src io.Reader, base string) (Info, error) {
	f, err := d.TempFile()
	if err != nil {
		return Info{}, err
	}
	defer os.Remove(f.Name())
	_, err = io.Copy(f, io.LimitReader(src, d.maxFile()+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return Info{}, err
	}
	return d.Put(p, f.Name(), base)
}

// Mkdir makes an empty folder. The manifest holds files only, so it is
// shown on this device until a file is put in it.
func (d *Disk) Mkdir(p string) error {
	if p = Clean(p); p == "" || p == Root {
		return ErrBadName
	}
	if !nameOK(p) {
		return ErrBadName
	}
	r, err := d.snapshot()
	if err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := r.Files[p]; ok || d.isDir(r, p) {
		return ErrExist
	}
	parent := path.Dir(p)
	if _, ok := r.Files[parent]; ok {
		return ErrNotDir
	}
	if d.dirs == nil {
		d.dirs = map[string]bool{}
	}
	d.dirs[p] = true
	return nil
}

// Remove deletes a file, or a folder with everything in it when all is
// set (an empty folder otherwise). Files that changed elsewhere since
// they were last listed here are left alone: an edit beats a deletion.
func (d *Disk) Remove(p string, all bool) error {
	if p = Clean(p); p == "" || p == Root {
		return ErrBadName
	}
	d.mu.Lock()
	if sf, ok := d.scratch[p]; ok {
		delete(d.scratch, p)
		d.mu.Unlock()
		os.Remove(sf.local)
		return nil
	}
	d.mu.Unlock()
	r, err := d.snapshot()
	if err != nil {
		return err
	}
	d.mu.Lock()
	seen := map[string]string{}
	if e, ok := r.Files[p]; ok {
		seen[p] = e.H
	} else {
		for q, e := range r.Files {
			if strings.HasPrefix(q, p+"/") {
				seen[q] = e.H
			}
		}
		if len(seen) == 0 {
			ok := d.dirs[p]
			for q := range d.dirs {
				if q == p || strings.HasPrefix(q, p+"/") {
					delete(d.dirs, q)
				}
			}
			d.mu.Unlock()
			if !ok {
				return ErrNotExist
			}
			return nil
		}
		if !all {
			d.mu.Unlock()
			return ErrNotEmpty
		}
	}
	d.mu.Unlock()
	return d.change(func(files map[string]vault.Entry) error {
		for q, h := range seen {
			if cur, ok := files[q]; ok && cur.H == h {
				delete(files, q)
			}
		}
		return nil
	})
}

// Rename moves a file or a folder (everything under it). The content is
// not uploaded again: the entries move. A file at the target is
// replaced, as rename(2) does.
func (d *Disk) Rename(from, to string) error {
	from, to = Clean(from), Clean(to)
	if from == "" || to == "" || from == Root || to == Root {
		return ErrBadName
	}
	if from == to {
		return nil
	}
	if strings.HasPrefix(to, from+"/") {
		return ErrBadName
	}
	d.mu.Lock()
	sf, fromScratch := d.scratch[from]
	d.mu.Unlock()
	switch {
	case fromScratch:
		// A program's temp file becomes the real file (or another temp).
		var base string
		if cur, err := d.Stat(to); err == nil {
			if cur.Dir {
				return ErrIsDir
			}
			base = cur.Hash
		}
		if _, err := d.Put(to, sf.local, base); err != nil {
			return err
		}
		d.mu.Lock()
		if cur, ok := d.scratch[from]; ok && cur.local == sf.local {
			delete(d.scratch, from)
		}
		d.mu.Unlock()
		os.Remove(sf.local) // Put copied it
		return nil
	case isScratch(to):
		// The real file moved aside as a backup ("a.txt~"): keep its
		// content here, then take it out of the vault.
		local, _, err := d.Fetch(from)
		if err != nil {
			return err
		}
		if _, err := d.putScratch(to, local); err != nil {
			return err
		}
		return d.Remove(from, false)
	}
	if !nameOK(to) {
		return ErrBadName
	}
	r, err := d.snapshot()
	if err != nil {
		return err
	}
	d.mu.Lock()
	_, isFile := r.Files[from]
	isDir := !isFile && d.isDir(r, from)
	if !isFile && !isDir {
		d.mu.Unlock()
		return ErrNotExist
	}
	if isDir && !hasPrefix(r.Files, from) {
		// Only a folder made here: rename it here.
		for q := range d.dirs {
			if q == from || strings.HasPrefix(q, from+"/") {
				delete(d.dirs, q)
				d.dirs[to+strings.TrimPrefix(q, from)] = true
			}
		}
		d.mu.Unlock()
		return nil
	}
	d.mu.Unlock()
	return d.change(func(files map[string]vault.Entry) error {
		if isFile {
			e, ok := files[from]
			if !ok {
				return ErrNotExist
			}
			if hasPrefix(files, to) {
				return ErrIsDir
			}
			delete(files, from)
			files[to] = e
			return nil
		}
		if _, ok := files[to]; ok {
			return ErrNotDir
		}
		if hasPrefix(files, to) {
			return ErrNotEmpty
		}
		moved := false
		for q, e := range files {
			if strings.HasPrefix(q, from+"/") {
				delete(files, q)
				files[to+strings.TrimPrefix(q, from)] = e
				moved = true
			}
		}
		if !moved {
			return ErrNotExist
		}
		return nil
	})
}
