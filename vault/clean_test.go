package vault

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var later = time.Now().Add(MinAge + time.Hour) // every object is old enough

// A vault with two files replaced or deleted: two unused objects.
func cleanVault(t *testing.T) (*DirStore, *Vault, map[string][]byte) {
	t.Helper()
	st := copyStore(t, "v2")
	v := New(st, NewPassphrase(golden))
	want := readPlain(t, "testdata/v2/disk")
	if err := v.PutFiles(map[string]File{"/internal/gone.txt": {Data: []byte("short-lived")}, "/internal/x.txt": {Data: []byte("x1")}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := v.PutFiles(map[string]File{"/internal/x.txt": {Data: []byte("x2")}}, []string{"/internal/gone.txt"}); err != nil {
		t.Fatal(err)
	}
	want["/internal/x.txt"] = []byte("x2")
	return st, v, want
}

func TestUsageAndCleanUp(t *testing.T) {
	st, v, want := cleanVault(t)
	u, err := v.Usage(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if u.Files != len(want) || u.Unused != 2 || u.Free != 0 || u.Missing != 0 || u.Hold != "" {
		t.Fatalf("%+v", u)
	}
	if n, _, err := v.CleanUp(time.Now()); err != nil || n != 0 {
		t.Fatal("young objects removed", n, err)
	}
	u, _ = v.Usage(later)
	if u.Free != 2 || u.FreeB != u.UnusedB || u.FreeB == 0 {
		t.Fatalf("%+v", u)
	}
	n, b, err := v.CleanUp(later)
	if err != nil || n != 2 || b != u.FreeB {
		t.Fatal(n, b, err)
	}
	checkAll(t, New(st, NewPassphrase(golden)), want)
	if u, _ := v.Usage(later); u.Unused != 0 || u.Objects != u.Files-countDup(want) {
		t.Fatalf("%+v", u)
	}
	for _, k := range []string{ManifestKey, KeyFileKey} {
		if _, ok, _ := st.Rev(k); !ok {
			t.Fatal(k, "removed")
		}
	}
}

// countDup: files sharing content share one object.
func countDup(files map[string][]byte) int {
	seen, dup := map[string]bool{}, 0
	for _, b := range files {
		h := SHA256Hex(b)
		if seen[h] {
			dup++
		}
		seen[h] = true
	}
	return dup
}

func TestCleanUpNeedsAManifest(t *testing.T) {
	st, v, _ := cleanVault(t)
	st.Delete(ManifestKey)
	u, err := v.Usage(later)
	if err != nil || u.Hold != "no-manifest" || u.Free != 0 {
		t.Fatalf("%+v %v", u, err)
	}
	before, _ := st.List(ObjectsPrefix)
	if _, _, err := v.CleanUp(later); !errors.Is(err, ErrNoManifest) {
		t.Fatal(err)
	}
	if after, _ := st.List(ObjectsPrefix); len(after) != len(before) {
		t.Fatal("objects removed without a manifest")
	}
}

// Most referenced objects missing (a partial listing, a wrong key):
// nothing is removed.
func TestCleanUpStopsWhenTheObjectsDoNotMatch(t *testing.T) {
	st, v, _ := cleanVault(t)
	r, _ := v.FetchManifest()
	k, _ := v.LoadKeys(false)
	for _, e := range r.Files {
		st.Delete(k.ObjectKey(e.H))
	}
	u, _ := v.Usage(later)
	if u.Hold != "mismatch" || u.Free != 0 || u.Unused != 2 {
		t.Fatalf("%+v", u)
	}
	if _, _, err := v.CleanUp(later); !errors.Is(err, ErrCleanOff) {
		t.Fatal(err)
	}
	if u2, _ := v.Usage(later); u2.Unused != 2 {
		t.Fatal("removed anyway")
	}
}

// Every name an entry can use counts, and only objects/<name> keys go.
func TestCleanUpKeepsLegacyNamesAndOtherKeys(t *testing.T) {
	st, v, want := cleanVault(t)
	h := SHA256Hex(want["/internal/x.txt"])
	for _, k := range []string{ObjectsPrefix + h, "objects/deeper/old", "other/thing"} {
		if _, err := st.Put(k, []byte("data"), Overwrite); err != nil {
			t.Fatal(err)
		}
	}
	if n, _, err := v.CleanUp(later); err != nil || n != 2 {
		t.Fatal(n, err)
	}
	for _, k := range []string{ObjectsPrefix + h, "objects/deeper/old", "other/thing"} {
		if _, ok, _ := st.Rev(k); !ok {
			t.Fatal(k, "removed")
		}
	}
}

// moving runs f once, right after the first object removal.
type moving struct {
	*DirStore
	f func()
}

func (m *moving) Delete(key string) error {
	err := m.DirStore.Delete(key)
	if m.f != nil && err == nil {
		f := m.f
		m.f = nil
		f()
	}
	return err
}

func TestCleanUpStopsWhenTheManifestMoves(t *testing.T) {
	st, _, _ := cleanVault(t)
	other := New(st, NewPassphrase(golden))
	m := &moving{DirStore: st, f: func() {
		if err := other.PutFiles(map[string]File{"/internal/new.txt": {Data: []byte("new")}}, nil); err != nil {
			t.Fatal(err)
		}
	}}
	n, _, err := New(m, NewPassphrase(golden)).CleanUp(later)
	if !errors.Is(err, ErrCleanMoved) || n != 1 {
		t.Fatal(n, err)
	}
}

// A device names a removed object again in the instant between the
// last check and the removal: reported, not silent.
func TestCleanUpReportsARace(t *testing.T) {
	st := copyStore(t, "v2")
	v := New(st, NewPassphrase(golden))
	v.PutFiles(map[string]File{"/internal/once.txt": {Data: []byte("once")}}, nil)
	v.PutFiles(nil, []string{"/internal/once.txt"})
	m := &moving{DirStore: st, f: func() {
		r, _ := v.FetchManifest()
		r.Files["/internal/back.txt"] = NewEntry([]byte("once"), "", time.Now())
		if err := v.Commit(r); err != nil {
			t.Fatal(err)
		}
	}}
	_, _, err := New(m, NewPassphrase(golden)).CleanUp(later)
	if !errors.Is(err, ErrCleanRace) {
		t.Fatal(err)
	}
}

func TestDirStoreListInfo(t *testing.T) {
	st := NewDirStore(t.TempDir())
	st.Put("objects/aa", []byte("12345"), Overwrite)
	old := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	os.Chtimes(filepath.Join(st.Root, "objects", "aa"), old, old)
	infos, err := st.ListInfo("objects/")
	if err != nil || len(infos) != 1 || infos[0].Size != 5 || !infos[0].Modified.Equal(old) {
		t.Fatalf("%+v %v", infos, err)
	}
}
