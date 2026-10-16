package vault

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// vault.js constants.
const (
	ManifestKey   = "manifest.json"
	KeyFileKey    = "key.json"
	ObjectsPrefix = "objects/"
	// RootPath is the orOS disk mount (fs.js ROOT_PATH). Manifest paths
	// are full paths under it: "/internal/Docs/a.txt".
	RootPath = "/internal"
)

var (
	ErrNoKey    = errors.New("vault key missing")
	ErrMismatch = errors.New("vault object does not match its manifest entry") // VD-H
	ErrMissing  = errors.New("missing vault object")
	ErrManifest = errors.New("bad manifest")
)

// Entry is one manifest file entry. Fields an older orOS may have left
// out stay out (pointers / raw): vault.js treats a missing size as
// "unknown", not as 0, and a re-written entry must not change that.
type Entry struct {
	H    string          `json:"h"`
	S    *int64          `json:"s,omitempty"`
	M    json.RawMessage `json:"m,omitempty"`
	Mime string          `json:"mime,omitempty"`
	V    int             `json:"v,omitempty"` // 1 = LEGACY object (passphrase envelope, objects/<h>)
}

// Size returns the size and whether it is known.
func (e Entry) Size() (int64, bool) {
	if e.S == nil {
		return 0, false
	}
	return *e.S, true
}

// ModTime returns the "m" timestamp (an ISO string in every orOS
// version that wrote one) or the zero time.
func (e Entry) ModTime() time.Time {
	var s string
	if json.Unmarshal(e.M, &s) == nil {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// NewEntry builds an entry the way vault.js pushCloud does.
func NewEntry(plain []byte, mime string, now time.Time) Entry {
	n := int64(len(plain))
	m, _ := json.Marshal(now.UTC().Format("2006-01-02T15:04:05.000Z")) // Date.toISOString()
	if mime == "" {
		mime = "application/octet-stream"
	}
	return Entry{H: SHA256Hex(plain), S: &n, M: m, Mime: mime}
}

// Remote is a fetched cloud manifest.
type Remote struct {
	Exists bool   // false = no manifest: an empty vault (VD-1: deletes nothing)
	Rev    string // Dropbox rev of manifest.json when Exists
	Files  map[string]Entry
	Legacy bool // the manifest itself is the pre-VD-KEY format
}

// Paths returns the manifest paths, sorted.
func (r *Remote) Paths() []string {
	out := make([]string, 0, len(r.Files))
	for p := range r.Files {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// Vault is one user's vault: a store and the passphrase that opens it.
type Vault struct {
	Store Store
	Pass  *Passphrase
	keys  *Keys
	now   func() time.Time
}

func New(store Store, pass *Passphrase) *Vault {
	return &Vault{Store: store, Pass: pass, now: time.Now}
}

// WithKeys starts from a vault key kept elsewhere (the OS keychain), so
// the passphrase is not needed for every run. A key that no longer
// opens the manifest is dropped and looked up again, as vault.js does.
func (v *Vault) WithKeys(k *Keys) *Vault { v.keys = k; return v }

// Keys returns the keys in use (nil before LoadKeys).
func (v *Vault) Keys() *Keys { return v.keys }

// LoadKeys = loadVaultKeys. create = true only on a push: an empty
// vault needs no key until something goes into it. A key file made by
// another device first wins (create-only write).
func (v *Vault) LoadKeys(create bool) (*Keys, error) {
	if v.keys != nil {
		return v.keys, nil
	}
	data, _, err := v.Store.Get(KeyFileKey)
	if err != nil {
		return nil, err
	}
	if data != nil {
		if v.Pass == nil {
			return nil, ErrLocked
		}
		k, err := OpenKeyFile(data, v.Pass)
		if err != nil {
			return nil, err
		}
		v.keys = k
		return k, nil
	}
	if !create {
		return nil, nil
	}
	if v.Pass == nil {
		return nil, ErrLocked
	}
	doc, k, err := NewKeyFile(v.Pass)
	if err != nil {
		return nil, err
	}
	if _, err := v.Store.Put(KeyFileKey, doc, CreateOnly); err != nil {
		if errors.Is(err, ErrConflict) {
			return v.LoadKeys(false)
		}
		return nil, err
	}
	v.keys = k
	return k, nil
}

// manifestDoc is the plaintext of a v2 manifest.
type manifestDoc struct {
	Ver   int              `json:"ver"`
	Files map[string]Entry `json:"files"`
}

// openManifest: first byte "{" = LEGACY (passphrase-sealed JSON text,
// { ver:1, files }); 0x02 = v2 sealed with the vault key.
func (v *Vault) openManifest(buf []byte) (map[string]Entry, bool, error) {
	if isLegacyManifest(buf) {
		if v.Pass == nil {
			return nil, true, ErrLocked
		}
		plain, err := v.Pass.OpenJSON(buf)
		if err != nil {
			return nil, true, err
		}
		var m manifestDoc
		if err := json.Unmarshal(plain, &m); err != nil || m.Ver != 1 || m.Files == nil {
			return nil, true, ErrManifest
		}
		files := map[string]Entry{}
		for p, e := range m.Files {
			if e.H != "" {
				e.V = 1
				files[p] = e
			}
		}
		return files, true, nil
	}
	retried := false
	for {
		k, err := v.LoadKeys(false)
		if err == nil && k == nil {
			err = ErrNoKey
		}
		var plain []byte
		if err == nil {
			plain, err = k.Open(buf)
		}
		if err != nil {
			// A key cached from before the cloud vault was wiped and
			// rebuilt elsewhere cannot open the new manifest: forget
			// it, fetch the current key once, try again.
			if !retried && v.keys != nil {
				v.keys, retried = nil, true
				continue
			}
			return nil, false, err
		}
		var m manifestDoc
		if err := json.Unmarshal(plain, &m); err != nil || m.Ver != 2 || m.Files == nil {
			return nil, false, ErrManifest
		}
		return m.Files, false, nil
	}
}

// FetchManifest reads the cloud manifest. A missing manifest is an
// empty vault, never "everything was deleted" (VD-1).
func (v *Vault) FetchManifest() (*Remote, error) {
	buf, rev, err := v.Store.Get(ManifestKey)
	if err != nil {
		return nil, err
	}
	if buf == nil {
		return &Remote{Files: map[string]Entry{}}, nil
	}
	files, legacy, err := v.openManifest(buf)
	if err != nil {
		return nil, err
	}
	return &Remote{Exists: true, Rev: rev, Files: files, Legacy: legacy}, nil
}

// Download returns a file's plaintext. It is accepted only when it
// hashes to the entry's h (VD-H): AES-GCM proves the object was sealed
// with the vault key, not which file it is.
func (v *Vault) Download(e Entry) ([]byte, error) {
	var plain []byte
	if e.V == 1 {
		buf, _, err := v.Store.Get(ObjectsPrefix + e.H)
		if err != nil {
			return nil, err
		}
		if buf == nil {
			return nil, ErrMissing
		}
		if v.Pass == nil {
			return nil, ErrLocked
		}
		if plain, err = v.Pass.OpenLegacyBytes(buf); err != nil {
			return nil, err
		}
	} else {
		k, err := v.LoadKeys(false)
		if err != nil {
			return nil, err
		}
		if k == nil {
			return nil, ErrNoKey
		}
		buf, _, err := v.Store.Get(k.ObjectKey(e.H))
		if err != nil {
			return nil, err
		}
		if buf == nil {
			return nil, ErrMissing
		}
		if plain, err = k.Open(buf); err != nil {
			return nil, err
		}
	}
	if SHA256Hex(plain) != e.H {
		return nil, ErrMismatch
	}
	return plain, nil
}

// objectStored: the manifest says this content is stored AND one
// existence probe confirms it (vault.js objectStored).
func (v *Vault) objectStored(k *Keys, hash string, cloudHashes map[string]bool) bool {
	if !cloudHashes[hash] {
		return false
	}
	_, ok, err := v.Store.Rev(k.ObjectKey(hash))
	return err == nil && ok
}

// File is content to put into the vault at a manifest path.
type File struct {
	Data []byte
	Mime string
}

// PutFiles adds or replaces files (and removes the paths in del) in one
// manifest write, conditional on the revision just read; a refused
// write starts over, at most twice (MAX_SYNC_TRIES), like vault.js.
// It is the building block of the mirror engine; wave 1 uses it only
// in tests and the interop harness.
func (v *Vault) PutFiles(files map[string]File, del []string) error {
	for path := range files {
		if err := CheckPath(path); err != nil {
			return err
		}
	}
	for tries := 0; ; tries++ {
		err := v.putOnce(files, del)
		if errors.Is(err, ErrConflict) && tries < 2 {
			continue
		}
		return err
	}
}

func (v *Vault) putOnce(files map[string]File, del []string) error {
	remote, err := v.FetchManifest()
	if err != nil {
		return err
	}
	if !remote.Exists {
		v.keys = nil // VD-KEY: a cached key may have no key file any more
	}
	k, err := v.LoadKeys(true)
	if err != nil {
		return err
	}
	cloudHashes := map[string]bool{}
	for _, e := range remote.Files {
		if e.V != 1 {
			cloudHashes[e.H] = true
		}
	}
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		f := files[p]
		e := NewEntry(f.Data, f.Mime, v.now())
		if !v.objectStored(k, e.H, cloudHashes) {
			if _, err := v.Store.Put(k.ObjectKey(e.H), k.Seal(f.Data), Overwrite); err != nil {
				return err
			}
		}
		cloudHashes[e.H] = true
		remote.Files[p] = e
	}
	for _, p := range del {
		delete(remote.Files, p)
	}
	return v.writeManifest(k, remote)
}

func (v *Vault) writeManifest(k *Keys, remote *Remote) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // JSON.stringify does not escape <, >, &
	if err := enc.Encode(manifestDoc{Ver: 2, Files: remote.Files}); err != nil {
		return err
	}
	plain := bytes.TrimRight(buf.Bytes(), "\n")
	mode := CreateOnly
	if remote.Exists {
		mode = UpdateRev(remote.Rev)
	}
	_, err := v.Store.Put(ManifestKey, k.Seal(plain), mode)
	return err
}

// CheckPath accepts only the form fs.js accepts: "/internal/a/b".
func CheckPath(p string) error {
	if !strings.HasPrefix(p, RootPath+"/") {
		return fmt.Errorf("path %q is not under %s", p, RootPath)
	}
	for _, seg := range strings.Split(p[len(RootPath)+1:], "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("bad path %q", p)
		}
	}
	return nil
}

// DiskPath turns a user path ("Docs/a.txt", "/Docs/a.txt") into a
// manifest path ("/internal/Docs/a.txt"). An empty path is the root; a
// path that already starts with "/internal/" is kept as it is.
func DiskPath(user string) string {
	if user == RootPath || strings.HasPrefix(user, RootPath+"/") {
		return strings.TrimRight(user, "/")
	}
	user = strings.Trim(user, "/")
	if user == "" {
		return RootPath
	}
	return RootPath + "/" + user
}

// UserPath is the reverse of DiskPath, for display.
func UserPath(disk string) string {
	return strings.TrimPrefix(strings.TrimPrefix(disk, RootPath), "/")
}
