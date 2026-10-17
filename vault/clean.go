package vault

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Clean-up of objects no manifest entry points to any more (deleted
// and replaced files leave their encrypted objects behind; vault.js
// leaves them too, "object GC deferred"). The rules, each against a
// way to lose a file:
//
//   - No manifest, a manifest this client cannot open, or no vault
//     key: nothing is removed (VD-1; without the key the keyed names
//     cannot be told apart).
//   - An object is referenced by every name an entry can use: the
//     keyed name (v2) and "objects/<h>" (legacy), whatever its "v".
//   - Only keys directly under "objects/" are ever removed; the
//     manifest, key.json and anything else are never touched.
//   - Only objects the provider stored at least MinAge ago: a device
//     uploads objects before it writes the manifest that names them,
//     and re-uploading identical content does not refresh the time.
//   - If fewer than nine in ten of the referenced objects are in the
//     listing, something is off (a partial listing, the wrong key) and
//     nothing is removed.
//   - The manifest revision is checked before each removal; when it
//     moved, the whole clean-up stops (run it again for a fresh view).
//   - Afterwards the manifest is read again and any removed object it
//     now names is reported as an error (ErrCleanRace).

// MinAge is how long an unreferenced object is kept.
const MinAge = 30 * 24 * time.Hour

var (
	ErrNoInventory = errors.New("this storage cannot list object sizes and times")
	ErrNoManifest  = errors.New("no manifest: nothing is removed")
	ErrCleanMoved  = errors.New("the vault changed during the clean-up; run it again")
	ErrCleanOff    = errors.New("the stored objects do not match the manifest; nothing is removed")
	ErrCleanRace   = errors.New("a removed object is named by the manifest again")
)

// Usage is what the vault holds.
type Usage struct {
	Files     int   `json:"files"`     // manifest entries
	FileBytes int64 `json:"fileBytes"` // their known sizes (plaintext)
	Objects   int   `json:"objects"`   // stored objects
	Stored    int64 `json:"stored"`    // bytes of all objects (encrypted)
	Unused    int   `json:"unused"`    // objects no entry names
	UnusedB   int64 `json:"unusedBytes"`
	Free      int   `json:"free"` // unused and older than MinAge: removable
	FreeB     int64 `json:"freeBytes"`
	Missing   int   `json:"missing"` // entries whose object is not stored
	Other     int64 `json:"otherBytes"`
	// Hold says why nothing can be removed: "no-manifest" or
	// "mismatch" (too few referenced objects listed, or no key).
	Hold string `json:"hold,omitempty"`

	rev  string
	free []ObjectInfo
	refs map[string]bool
}

// Usage lists the vault and sorts objects into used, unused and
// removable. It changes nothing.
func (v *Vault) Usage(now time.Time) (*Usage, error) {
	inv, ok := v.Store.(Inventory)
	if !ok {
		return nil, ErrNoInventory
	}
	remote, err := v.FetchManifest()
	if err != nil {
		return nil, err
	}
	var k *Keys
	if remote.Exists {
		if k, err = v.LoadKeys(false); err != nil {
			return nil, err
		}
	}
	infos, err := inv.ListInfo("")
	if err != nil {
		return nil, err
	}
	u := &Usage{rev: remote.Rev, refs: map[string]bool{}}
	hashes := map[string]bool{}
	for _, e := range remote.Files {
		u.Files++
		if s, ok := e.Size(); ok {
			u.FileBytes += s
		}
		if hashes[e.H] {
			continue
		}
		hashes[e.H] = true
		u.refs[strings.ToLower(ObjectsPrefix+e.H)] = true
		if k != nil {
			u.refs[k.ObjectKey(e.H)] = true
		}
	}
	stored := map[string]bool{}
	for _, o := range infos {
		name, isObj := strings.CutPrefix(o.Key, ObjectsPrefix)
		if !isObj || name == "" || strings.Contains(name, "/") {
			u.Other += o.Size
			continue
		}
		u.Objects++
		u.Stored += o.Size
		stored[o.Key] = true
		if u.refs[o.Key] {
			continue
		}
		u.Unused++
		u.UnusedB += o.Size
		if !o.Modified.IsZero() && now.Sub(o.Modified) >= MinAge {
			u.Free++
			u.FreeB += o.Size
			u.free = append(u.free, o)
		}
	}
	found := 0
	for h := range hashes {
		if stored[strings.ToLower(ObjectsPrefix+h)] || (k != nil && stored[k.ObjectKey(h)]) {
			found++
		} else {
			u.Missing++
		}
	}
	switch {
	case !remote.Exists:
		u.Hold = "no-manifest" // VD-1
	case k == nil || found*10 < len(hashes)*9:
		u.Hold = "mismatch"
	}
	if u.Hold != "" {
		u.Free, u.FreeB, u.free = 0, 0, nil
	}
	return u, nil
}

// CleanUp looks again (Usage) and removes the objects found
// removable; it returns how many and how many bytes.
func (v *Vault) CleanUp(now time.Time) (removed int, bytes int64, err error) {
	u, err := v.Usage(now)
	if err != nil {
		return 0, 0, err
	}
	switch u.Hold {
	case "no-manifest":
		return 0, 0, ErrNoManifest
	case "mismatch":
		return 0, 0, ErrCleanOff
	}
	var gone []string
	defer func() {
		if len(gone) == 0 {
			return
		}
		after, ferr := v.FetchManifest()
		if ferr != nil {
			return
		}
		k := v.keys
		for _, e := range after.Files {
			for _, g := range gone {
				if g == strings.ToLower(ObjectsPrefix+e.H) || (k != nil && g == k.ObjectKey(e.H)) {
					err = fmt.Errorf("%w: %s", ErrCleanRace, UserPath(firstPath(after, e.H)))
					return
				}
			}
		}
	}()
	for _, o := range u.free {
		rev, ok, rerr := v.Store.Rev(ManifestKey)
		if rerr != nil {
			return removed, bytes, rerr
		}
		if !ok || rev != u.rev {
			return removed, bytes, ErrCleanMoved
		}
		if derr := v.Store.Delete(o.Key); derr != nil {
			return removed, bytes, derr
		}
		gone = append(gone, o.Key)
		removed++
		bytes += o.Size
	}
	return removed, bytes, nil
}

func firstPath(r *Remote, h string) string {
	for _, p := range r.Paths() {
		if r.Files[p].H == h {
			return p
		}
	}
	return ""
}
