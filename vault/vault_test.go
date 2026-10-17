package vault

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testdata/v2 and testdata/legacy were written by the real vault.js
// (interop/vaultjs.cjs) with this passphrase; disk/ holds the plain files.
const golden = "golden passphrase"

func readPlain(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		b, err := os.ReadFile(p)
		out[RootPath+"/"+filepath.ToSlash(rel)] = b
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// copyStore gives a test its own writable copy of a golden vault.
func copyStore(t *testing.T, name string) *DirStore {
	t.Helper()
	src := filepath.Join("testdata", name, "store")
	dst := t.TempDir()
	err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return NewDirStore(dst)
}

func checkAll(t *testing.T, v *Vault, want map[string][]byte) *Remote {
	t.Helper()
	r, err := v.FetchManifest()
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Files) != len(want) {
		t.Fatalf("%d files in the manifest, want %d", len(r.Files), len(want))
	}
	for p, w := range want {
		e, ok := r.Files[p]
		if !ok {
			t.Fatalf("%s missing from the manifest", p)
		}
		got, err := v.Download(e)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if !bytes.Equal(got, w) {
			t.Fatalf("%s: content differs", p)
		}
		if s, ok := e.Size(); !ok || s != int64(len(w)) {
			t.Fatalf("%s: size %d, %v", p, s, ok)
		}
	}
	return r
}

func TestGoldenV2(t *testing.T) {
	v := New(copyStore(t, "v2"), NewPassphrase(golden))
	r := checkAll(t, v, readPlain(t, "testdata/v2/disk"))
	if r.Legacy || !r.Exists {
		t.Fatalf("legacy=%v exists=%v", r.Legacy, r.Exists)
	}
}

func TestGoldenLegacy(t *testing.T) {
	v := New(copyStore(t, "legacy"), NewPassphrase(golden))
	r := checkAll(t, v, readPlain(t, "testdata/legacy/disk"))
	if !r.Legacy {
		t.Fatal("not read as legacy")
	}
	for p, e := range r.Files {
		if e.V != 1 {
			t.Fatalf("%s not marked v:1", p)
		}
	}
}

func TestWrongPassphraseIsLocked(t *testing.T) {
	for _, name := range []string{"v2", "legacy"} {
		v := New(copyStore(t, name), NewPassphrase("wrong"))
		if _, err := v.FetchManifest(); err == nil {
			t.Fatalf("%s: a wrong passphrase opened the manifest", name)
		}
	}
}

// VD-H: swapping two objects (or rolling one back) must not put one
// file's content at another file's path.
func TestSwappedObjectsAreRefused(t *testing.T) {
	st := copyStore(t, "v2")
	v := New(st, NewPassphrase(golden))
	r, err := v.FetchManifest()
	if err != nil {
		t.Fatal(err)
	}
	k := v.Keys()
	a, b := r.Files["/internal/a.txt"], r.Files["/internal/Docs/bin.dat"]
	ka, kb := k.ObjectKey(a.H), k.ObjectKey(b.H)
	da, _, _ := st.Get(ka)
	db, _, _ := st.Get(kb)
	st.Put(ka, db, Overwrite)
	st.Put(kb, da, Overwrite)
	if _, err := v.Download(a); !errors.Is(err, ErrMismatch) {
		t.Fatalf("got %v, want ErrMismatch", err)
	}
}

func TestTamperedEnvelopeFails(t *testing.T) {
	st := copyStore(t, "v2")
	v := New(st, NewPassphrase(golden))
	r, _ := v.FetchManifest()
	e := r.Files["/internal/a.txt"]
	key := v.Keys().ObjectKey(e.H)
	d, _, _ := st.Get(key)
	d[len(d)-1] ^= 1
	st.Put(key, d, Overwrite)
	if _, err := v.Download(e); err == nil {
		t.Fatal("a flipped bit was accepted")
	}
}

// During a passphrase change key.json carries two wraps (new, old);
// either passphrase opens the vault key.
func TestTwoWraps(t *testing.T) {
	oldP, newP := NewPassphrase("old"), NewPassphrase("new")
	doc, k, err := NewKeyFile(oldP)
	if err != nil {
		t.Fatal(err)
	}
	var kf keyFile
	json.Unmarshal(doc, &kf)
	sealed, _ := newP.SealJSON(wrapped{K: b64(k.Raw())})
	kf.Wraps = []string{sealed, kf.Wraps[0]}
	two, _ := json.Marshal(kf)
	for _, p := range []*Passphrase{oldP, newP} {
		got, err := OpenKeyFile(two, p)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got.Raw(), k.Raw()) {
			t.Fatal("different key")
		}
	}
	if _, err := OpenKeyFile(two, NewPassphrase("other")); !errors.Is(err, ErrLocked) {
		t.Fatalf("got %v", err)
	}
}

func TestAbsurdRoundCountRefused(t *testing.T) {
	p := NewPassphrase("x")
	s, _ := p.SealJSON(map[string]int{"a": 1})
	var b map[string]any
	json.Unmarshal([]byte(s), &b)
	b["iter"] = 1e9
	bad, _ := json.Marshal(b)
	if _, err := p.OpenJSON(bad); !errors.Is(err, ErrBlobVersion) {
		t.Fatalf("got %v", err)
	}
	b["iter"] = 1000 // below the legacy floor
	bad, _ = json.Marshal(b)
	if _, err := p.OpenJSON(bad); !errors.Is(err, ErrBlobVersion) {
		t.Fatalf("got %v", err)
	}
}

func TestEmptyVault(t *testing.T) {
	v := New(NewDirStore(t.TempDir()), NewPassphrase("p"))
	r, err := v.FetchManifest()
	if err != nil || r.Exists || len(r.Files) != 0 {
		t.Fatalf("%v %+v", err, r)
	}
	if k, err := v.LoadKeys(false); k != nil || err != nil {
		t.Fatal("an empty vault must not get a key on a read")
	}
}

// conflictStore refuses the first n conditional manifest writes, as if
// another device had just written.
type conflictStore struct {
	*DirStore
	n int
}

func (c *conflictStore) Put(key string, data []byte, mode PutMode) (string, error) {
	if key == ManifestKey && !mode.IsOverwrite() && c.n > 0 {
		c.n--
		// another device adds its own file in between
		other := New(c.DirStore, NewPassphrase(golden))
		if err := other.PutFiles(map[string]File{"/internal/other.txt": {Data: []byte("other device")}}, nil); err != nil {
			return "", err
		}
		return "", ErrConflict
	}
	return c.DirStore.Put(key, data, mode)
}

func TestPutFilesRetriesOnConflictAndKeepsTheOtherWrite(t *testing.T) {
	st := &conflictStore{DirStore: copyStore(t, "v2"), n: 1}
	v := New(st, NewPassphrase(golden))
	if err := v.PutFiles(map[string]File{"/internal/mine.txt": {Data: []byte("mine")}}, nil); err != nil {
		t.Fatal(err)
	}
	want := readPlain(t, "testdata/v2/disk")
	want["/internal/mine.txt"] = []byte("mine")
	want["/internal/other.txt"] = []byte("other device")
	checkAll(t, New(st.DirStore, NewPassphrase(golden)), want)
}

func TestPutFilesGivesUpAfterTwoRetries(t *testing.T) {
	st := &conflictStore{DirStore: copyStore(t, "v2"), n: 5}
	v := New(st, NewPassphrase(golden))
	err := v.PutFiles(map[string]File{"/internal/mine.txt": {Data: []byte("mine")}}, nil)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("got %v", err)
	}
}

func TestFirstWriteCreatesKeyAndManifest(t *testing.T) {
	st := NewDirStore(t.TempDir())
	v := New(st, NewPassphrase("p"))
	if err := v.PutFiles(map[string]File{"/internal/x": {Data: []byte("x")}}, nil); err != nil {
		t.Fatal(err)
	}
	// A second device with only the passphrase reads it.
	checkAll(t, New(st, NewPassphrase("p")), map[string][]byte{"/internal/x": []byte("x")})
	// Identical content is stored once.
	if err := v.PutFiles(map[string]File{"/internal/copy-of-x": {Data: []byte("x")}}, nil); err != nil {
		t.Fatal(err)
	}
	objs, _ := st.List(ObjectsPrefix)
	if len(objs) != 1 {
		t.Fatalf("%d objects, want 1", len(objs))
	}
}

func TestEntryFieldsSurviveARewrite(t *testing.T) {
	// An entry without "s" (an old orOS) keeps no "s" when the manifest
	// is rewritten: vault.js reads a missing size as unknown, not 0.
	var e Entry
	json.Unmarshal([]byte(`{"h":"ab","m":"2026-01-01T00:00:00.000Z","mime":"text/plain"}`), &e)
	out, _ := json.Marshal(e)
	if strings.Contains(string(out), `"s"`) {
		t.Fatalf("size invented: %s", out)
	}
	if e.ModTime().Year() != 2026 {
		t.Fatal("m not parsed")
	}
}

func TestPaths(t *testing.T) {
	cases := map[string]string{
		"": "/internal", "/": "/internal", "Docs/a.txt": "/internal/Docs/a.txt",
		"/Docs/": "/internal/Docs", "/internal/Docs": "/internal/Docs", "internal": "/internal/internal",
	}
	for in, want := range cases {
		if got := DiskPath(in); got != want {
			t.Errorf("DiskPath(%q) = %q, want %q", in, got, want)
		}
	}
	for _, bad := range []string{"/x", "/internal", "/internal/", "/internal/a/../b", "/internal//a"} {
		if CheckPath(bad) == nil {
			t.Errorf("CheckPath(%q) accepted", bad)
		}
	}
	if CheckPath("/internal/Έγγραφα/σημείωση.md") != nil {
		t.Error("Greek path refused")
	}
}

func TestDirStoreRejectsEscapes(t *testing.T) {
	st := NewDirStore(t.TempDir())
	for _, k := range []string{"../x", "a/../../x", `a\b`, ""} {
		if _, err := st.Put(k, []byte("x"), Overwrite); err == nil {
			t.Errorf("key %q accepted", k)
		}
	}
}
