package vdisk

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/koulaxizis/oros-drive/vault"
)

var (
	keyOnce sync.Once
	keyDoc  []byte
	keys    *vault.Keys
)

type env struct {
	t     *testing.T
	store *vault.DirStore
	root  string
}

func newEnv(t *testing.T) *env {
	keyOnce.Do(func() {
		var err error
		keyDoc, keys, err = vault.NewKeyFile(vault.NewPassphrase("vdisk test passphrase"))
		if err != nil {
			panic(err)
		}
	})
	root := t.TempDir()
	st := vault.NewDirStore(filepath.Join(root, "cloud"))
	if _, err := st.Put(vault.KeyFileKey, keyDoc, vault.CreateOnly); err != nil {
		t.Fatal(err)
	}
	return &env{t: t, store: st, root: root}
}

// disk is one device's view; every disk shares the env's vault.
func (e *env) disk(name string) *Disk {
	return &Disk{Vault: vault.New(e.store, nil).WithKeys(keys), CacheDir: filepath.Join(e.root, name), TTL: time.Nanosecond}
}

// browser writes the vault directly, like orOS does.
func (e *env) browser(files map[string]string, del ...string) {
	e.t.Helper()
	v := vault.New(e.store, vault.NewPassphrase("vdisk test passphrase")).WithKeys(keys)
	in := map[string]vault.File{}
	for p, s := range files {
		in[p] = vault.File{Data: []byte(s)}
	}
	if err := v.PutFiles(in, del); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) manifest() map[string]vault.Entry {
	e.t.Helper()
	r, err := vault.New(e.store, nil).WithKeys(keys).FetchManifest()
	if err != nil {
		e.t.Fatal(err)
	}
	return r.Files
}

func (e *env) content(p string) string {
	e.t.Helper()
	f, ok := e.manifest()[p]
	if !ok {
		e.t.Fatalf("%s not in the manifest", p)
	}
	b, err := vault.New(e.store, nil).WithKeys(keys).Download(f)
	if err != nil {
		e.t.Fatal(err)
	}
	return string(b)
}

func write(t *testing.T, s string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func names(list []Info) string {
	var out []string
	for _, i := range list {
		n := i.Name
		if i.Dir {
			n += "/"
		}
		out = append(out, n)
	}
	return strings.Join(out, " ")
}

func TestListStatFetch(t *testing.T) {
	e := newEnv(t)
	e.browser(map[string]string{"/internal/Docs/a.txt": "alpha", "/internal/Docs/sub/b.txt": "beta", "/internal/c.md": "c"})
	d := e.disk("pc")
	root, err := d.List("")
	if err != nil {
		t.Fatal(err)
	}
	if got := names(root); got != "Docs/ c.md" {
		t.Fatal(got)
	}
	docs, _ := d.List("Docs")
	if got := names(docs); got != "sub/ a.txt" {
		t.Fatal(got)
	}
	st, err := d.Stat("/internal/Docs/a.txt")
	if err != nil || st.Dir || st.Size != 5 || st.Cached {
		t.Fatalf("%+v %v", st, err)
	}
	if st, _ := d.Stat("Docs/sub"); !st.Dir {
		t.Fatal("sub is a folder")
	}
	if _, err := d.Stat("nope"); !errors.Is(err, ErrNotExist) {
		t.Fatal(err)
	}
	if _, err := d.List("Docs/a.txt"); !errors.Is(err, ErrNotDir) {
		t.Fatal(err)
	}
	local, info, err := d.Fetch("Docs/a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(local); string(b) != "alpha" || !info.Cached {
		t.Fatalf("%q %+v", b, info)
	}
	if _, _, err := d.Fetch("Docs"); !errors.Is(err, ErrIsDir) {
		t.Fatal(err)
	}
	if Clean("../etc") != "" || Clean("/internal/a/../b") != "" || Clean("a/b") != "/internal/a/b" {
		t.Fatal("Clean")
	}
}

func TestFetchRejectsAWrongObject(t *testing.T) {
	e := newEnv(t)
	e.browser(map[string]string{"/internal/a.txt": "real", "/internal/b.txt": "other"})
	m := e.manifest()
	// a's object replaced by b's: AES-GCM opens it, VD-H must refuse it.
	other, _, err := e.store.Get(keys.ObjectKey(m["/internal/b.txt"].H))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.Put(keys.ObjectKey(m["/internal/a.txt"].H), other, vault.Overwrite); err != nil {
		t.Fatal(err)
	}
	d := e.disk("pc")
	if _, _, err := d.Fetch("a.txt"); !errors.Is(err, vault.ErrMismatch) {
		t.Fatal(err)
	}
	if st, _ := d.Stat("a.txt"); st.Cached {
		t.Fatal("a refused object was cached")
	}
}

func TestPutNewAndOverwrite(t *testing.T) {
	e := newEnv(t)
	d := e.disk("pc")
	info, err := d.Put("Notes/n.txt", write(t, "one"), "")
	if err != nil {
		t.Fatal(err)
	}
	if e.content("/internal/Notes/n.txt") != "one" || !info.Cached || info.Mime != "text/plain" {
		t.Fatalf("%+v", info)
	}
	if _, err := d.Put("Notes/n.txt", write(t, "two"), info.Hash); err != nil {
		t.Fatal(err)
	}
	if e.content("/internal/Notes/n.txt") != "two" || len(e.manifest()) != 1 {
		t.Fatal(e.manifest())
	}
	// Another device sees it at once.
	if b, _, err := e.disk("phone").Fetch("Notes/n.txt"); err != nil {
		t.Fatal(err)
	} else if s, _ := os.ReadFile(b); string(s) != "two" {
		t.Fatal(string(s))
	}
}

func TestPutKeepsBothWhenChangedElsewhere(t *testing.T) {
	e := newEnv(t)
	e.browser(map[string]string{"/internal/a.txt": "v1"})
	d := e.disk("pc")
	st, _ := d.Stat("a.txt")
	e.browser(map[string]string{"/internal/a.txt": "browser edit"})
	d.now = func() time.Time { return time.Date(2026, 10, 17, 9, 30, 0, 0, time.UTC) }
	if _, err := d.Put("a.txt", write(t, "pc edit"), st.Hash); err != nil {
		t.Fatal(err)
	}
	if e.content("/internal/a.txt") != "pc edit" {
		t.Fatal("ours keeps the name")
	}
	if e.content("/internal/a (conflict 20261017-093000).txt") != "browser edit" {
		t.Fatal(e.manifest())
	}
	// Same content on both sides: no conflict copy.
	st2, _ := d.Stat("a.txt")
	e.browser(map[string]string{"/internal/a.txt": "same"})
	if _, err := d.Put("a.txt", write(t, "same"), st2.Hash); err != nil {
		t.Fatal(err)
	}
	if len(e.manifest()) != 2 {
		t.Fatal(e.manifest())
	}
}

func TestPutRetriesOnAConcurrentCommit(t *testing.T) {
	e := newEnv(t)
	e.browser(map[string]string{"/internal/x.txt": "x"})
	d := e.disk("pc")
	d.TTL = time.Hour // keep the stale manifest in hand
	if _, err := d.Stat("x.txt"); err != nil {
		t.Fatal(err)
	}
	e.browser(map[string]string{"/internal/y.txt": "y"}) // moves the revision
	if _, err := d.Put("z.txt", write(t, "z"), ""); err != nil {
		t.Fatal(err)
	}
	m := e.manifest()
	if len(m) != 3 {
		t.Fatalf("the browser's file was lost: %v", m)
	}
}

func TestRefusedNames(t *testing.T) {
	e := newEnv(t)
	d := e.disk("pc")
	for _, p := range []string{"", "../up", "~$x/a.txt"} {
		if _, err := d.Put(p, write(t, "x"), ""); err == nil {
			t.Fatalf("%q accepted", p)
		}
	}
	d.MaxFile = 3
	if _, err := d.Put("big.bin", write(t, "four"), ""); !errors.Is(err, ErrTooBig) {
		t.Fatal(err)
	}
}

func TestMkdirShowsUntilAFileLands(t *testing.T) {
	e := newEnv(t)
	d := e.disk("pc")
	if err := d.Mkdir("New"); err != nil {
		t.Fatal(err)
	}
	if err := d.Mkdir("New"); !errors.Is(err, ErrExist) {
		t.Fatal(err)
	}
	if l, _ := d.List(""); names(l) != "New/" {
		t.Fatal(names(l))
	}
	if l, err := d.List("New"); err != nil || len(l) != 0 {
		t.Fatal(l, err)
	}
	if _, err := d.Put("New/f.txt", write(t, "f"), ""); err != nil {
		t.Fatal(err)
	}
	if len(d.dirs) != 0 {
		t.Fatal("folder kept after a file landed")
	}
	if err := d.Mkdir("Gone"); err != nil {
		t.Fatal(err)
	}
	if err := d.Remove("Gone", false); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Stat("Gone"); !errors.Is(err, ErrNotExist) {
		t.Fatal(err)
	}
}

func TestRemove(t *testing.T) {
	e := newEnv(t)
	e.browser(map[string]string{"/internal/D/a": "a", "/internal/D/b": "b", "/internal/f": "f"})
	d := e.disk("pc")
	if err := d.Remove("D", false); !errors.Is(err, ErrNotEmpty) {
		t.Fatal(err)
	}
	if err := d.Remove("f", false); err != nil {
		t.Fatal(err)
	}
	if err := d.Remove("D", true); err != nil {
		t.Fatal(err)
	}
	if len(e.manifest()) != 0 {
		t.Fatal(e.manifest())
	}
	if err := d.Remove("D", true); !errors.Is(err, ErrNotExist) {
		t.Fatal(err)
	}
}

func TestRemoveSparesAFileChangedElsewhere(t *testing.T) {
	e := newEnv(t)
	e.browser(map[string]string{"/internal/D/a": "a", "/internal/D/b": "b"})
	d := e.disk("pc")
	d.TTL = time.Hour
	if _, err := d.List("D"); err != nil {
		t.Fatal(err)
	}
	e.browser(map[string]string{"/internal/D/a": "edited in orOS"})
	if err := d.Remove("D", true); err != nil {
		t.Fatal(err)
	}
	m := e.manifest()
	if _, ok := m["/internal/D/a"]; !ok || len(m) != 1 {
		t.Fatalf("an edit beats a deletion: %v", m)
	}
}

func TestRename(t *testing.T) {
	e := newEnv(t)
	e.browser(map[string]string{"/internal/D/a": "a", "/internal/D/s/b": "b", "/internal/f": "f", "/internal/g": "g"})
	d := e.disk("pc")
	before := e.manifest()["/internal/f"].H
	if err := d.Rename("f", "Docs/f2"); err != nil {
		t.Fatal(err)
	}
	if e.manifest()["/internal/Docs/f2"].H != before {
		t.Fatal("content changed in a rename")
	}
	if err := d.Rename("D", "E"); err != nil {
		t.Fatal(err)
	}
	if e.content("/internal/E/s/b") != "b" || e.content("/internal/E/a") != "a" {
		t.Fatal(e.manifest())
	}
	if err := d.Rename("g", "Docs/f2"); err != nil { // replaces, like rename(2)
		t.Fatal(err)
	}
	if e.content("/internal/Docs/f2") != "g" || len(e.manifest()) != 3 {
		t.Fatal(e.manifest())
	}
	if err := d.Rename("E", "E/inside"); !errors.Is(err, ErrBadName) {
		t.Fatal(err)
	}
	if err := d.Rename("nope", "x"); !errors.Is(err, ErrNotExist) {
		t.Fatal(err)
	}
	if err := d.Rename("E", "Docs/f2"); !errors.Is(err, ErrNotDir) {
		t.Fatal(err)
	}
}

func TestCacheStaysUnderItsLimit(t *testing.T) {
	e := newEnv(t)
	files := map[string]string{}
	for _, n := range []string{"1", "2", "3", "4"} {
		files["/internal/"+n] = strings.Repeat(n, 100)
	}
	e.browser(files)
	d := e.disk("pc")
	d.MaxCache = 250
	base := time.Now()
	for i, n := range []string{"1", "2", "3", "4"} {
		i := i
		d.now = func() time.Time { return base.Add(time.Duration(i) * time.Minute) }
		if _, _, err := d.Fetch(n); err != nil {
			t.Fatal(err)
		}
	}
	if s := d.CacheSize(); s > 250 {
		t.Fatalf("cache %d", s)
	}
	if st, _ := d.Stat("4"); !st.Cached {
		t.Fatal("the newest file was dropped")
	}
	if st, _ := d.Stat("1"); st.Cached {
		t.Fatal("the oldest file was kept")
	}
	if err := d.ClearCache(); err != nil || d.CacheSize() != 0 {
		t.Fatal(err)
	}
}

func TestPutReader(t *testing.T) {
	e := newEnv(t)
	d := e.disk("phone")
	if _, err := d.PutReader("s.txt", strings.NewReader("streamed"), ""); err != nil {
		t.Fatal(err)
	}
	if e.content("/internal/s.txt") != "streamed" {
		t.Fatal("content")
	}
	d.MaxFile = 4
	if _, err := d.PutReader("big", strings.NewReader("too long"), ""); !errors.Is(err, ErrTooBig) {
		t.Fatal(err)
	}
}

// Editors and office suites save by writing a temp or lock file and
// renaming it over the original; those names never reach the vault.
func TestTempFilesStayLocal(t *testing.T) {
	e := newEnv(t)
	e.browser(map[string]string{"/internal/Docs/report.docx": "v1"})
	d := e.disk("pc")
	if _, err := d.Put("Docs/~$report.docx", write(t, "lock"), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Put("Docs/lu123.tmp", write(t, "v2"), ""); err != nil {
		t.Fatal(err)
	}
	if l, _ := d.List("Docs"); names(l) != "lu123.tmp report.docx ~$report.docx" {
		t.Fatal(names(l))
	}
	if len(e.manifest()) != 1 {
		t.Fatalf("a temp file reached the vault: %v", e.manifest())
	}
	// Move the original aside, the temp file over it, drop the backup.
	if err := d.Rename("Docs/report.docx", "Docs/report.docx~"); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.manifest()["/internal/Docs/report.docx"]; ok {
		t.Fatal("moved aside but still in the vault")
	}
	if local, _, err := d.Fetch("Docs/report.docx~"); err != nil {
		t.Fatal(err)
	} else if b, _ := os.ReadFile(local); string(b) != "v1" {
		t.Fatal(string(b))
	}
	if err := d.Rename("Docs/lu123.tmp", "Docs/report.docx"); err != nil {
		t.Fatal(err)
	}
	if e.content("/internal/Docs/report.docx") != "v2" {
		t.Fatal("saved content")
	}
	for _, p := range []string{"Docs/report.docx~", "Docs/~$report.docx"} {
		if err := d.Remove(p, false); err != nil {
			t.Fatal(err)
		}
	}
	if l, _ := d.List("Docs"); names(l) != "report.docx" {
		t.Fatal(names(l))
	}
	if len(e.manifest()) != 1 {
		t.Fatal(e.manifest())
	}
	if ents, _ := os.ReadDir(filepath.Join(d.CacheDir, "scratch")); len(ents) != 0 {
		t.Fatalf("%d scratch files left", len(ents))
	}
}
