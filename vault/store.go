package vault

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ErrConflict is a refused conditional write (sync.js "storage-conflict").
var ErrConflict = errors.New("storage-conflict")

// PutMode is the conditional write of sync.js adapterCommit (SY-D4).
type PutMode struct {
	kind int // 0 overwrite, 1 add (must not exist), 2 update (must be at Rev)
	Rev  string
}

var (
	Overwrite  = PutMode{}        // immutable content-addressed objects
	CreateOnly = PutMode{kind: 1} // knownRev null
)

// UpdateRev: replace only this revision (knownRev string).
func UpdateRev(rev string) PutMode { return PutMode{kind: 2, Rev: rev} }

func (m PutMode) IsOverwrite() bool  { return m.kind == 0 }
func (m PutMode) IsCreateOnly() bool { return m.kind == 1 }
func (m PutMode) IsUpdate() bool     { return m.kind == 2 }

// Store is the storage adapter of sync.js (orosSync.storage): keys are
// relative to the vault root ("/vault" in the Dropbox app folder) and
// content is always already encrypted.
type Store interface {
	// Get returns (nil, "", nil) for a missing key: missing is never an error.
	Get(key string) (data []byte, rev string, err error)
	// Put writes conditionally; a refused condition returns ErrConflict.
	Put(key string, data []byte, mode PutMode) (rev string, err error)
	// Rev returns "" and false for a missing key.
	Rev(key string) (rev string, ok bool, err error)
	// Delete: gone already counts as deleted.
	Delete(key string) error
	// List returns every key under prefix ("" = all), lower-cased like Dropbox.
	List(prefix string) ([]string, error)
}

// ObjectInfo is one stored key with its size and the time the provider
// stored it (Dropbox server_modified: re-uploading identical content
// does not move it).
type ObjectInfo struct {
	Key      string
	Size     int64
	Modified time.Time
}

// Inventory is a Store that can also list sizes and times (clean-up
// and the usage view need them; both stores here have it).
type Inventory interface {
	ListInfo(prefix string) ([]ObjectInfo, error)
}

// DirStore keeps a vault in a local folder: one file per key, its
// revision in memory. Used by tests and by the interop harness, which
// moves a vault between the Go code and the real vault.js.
type DirStore struct {
	Root string
	mu   sync.Mutex
	revs map[string]string
	n    int
}

func NewDirStore(root string) *DirStore {
	return &DirStore{Root: root, revs: map[string]string{}}
}

func (d *DirStore) file(key string) (string, error) {
	key = strings.TrimLeft(key, "/")
	if key == "" || strings.Contains(key, "..") || strings.ContainsRune(key, '\\') {
		return "", fmt.Errorf("bad key %q", key)
	}
	return filepath.Join(d.Root, filepath.FromSlash(key)), nil
}

func (d *DirStore) rev(key string) string {
	if r, ok := d.revs[key]; ok {
		return r
	}
	d.n++
	r := fmt.Sprintf("dir%d", d.n)
	d.revs[key] = r
	return r
}

func (d *DirStore) Get(key string) ([]byte, string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	p, err := d.file(key)
	if err != nil {
		return nil, "", err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	return b, d.rev(key), nil
}

func (d *DirStore) Put(key string, data []byte, mode PutMode) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	p, err := d.file(key)
	if err != nil {
		return "", err
	}
	_, statErr := os.Stat(p)
	exists := statErr == nil
	switch {
	case mode.IsCreateOnly() && exists:
		return "", ErrConflict
	case mode.IsUpdate() && (!exists || d.rev(key) != mode.Rev):
		return "", ErrConflict
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		return "", err
	}
	delete(d.revs, key)
	return d.rev(key), nil
}

func (d *DirStore) Rev(key string) (string, bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	p, err := d.file(key)
	if err != nil {
		return "", false, err
	}
	if _, err := os.Stat(p); err != nil {
		return "", false, nil
	}
	return d.rev(key), true, nil
}

func (d *DirStore) Delete(key string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	p, err := d.file(key)
	if err != nil {
		return err
	}
	delete(d.revs, key)
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (d *DirStore) List(prefix string) ([]string, error) {
	infos, err := d.ListInfo(prefix)
	out := make([]string, len(infos))
	for i, o := range infos {
		out[i] = o.Key
	}
	return out, err
}

// ListInfo: the file's modification time stands for server_modified.
func (d *DirStore) ListInfo(prefix string) ([]ObjectInfo, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []ObjectInfo
	err := filepath.WalkDir(d.Root, func(p string, e os.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if e.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(d.Root, p)
		k := strings.ToLower(filepath.ToSlash(rel))
		if strings.HasPrefix(k, strings.ToLower(prefix)) {
			fi, err := e.Info()
			if err != nil {
				return err
			}
			out = append(out, ObjectInfo{Key: k, Size: fi.Size(), Modified: fi.ModTime()})
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, err
}
