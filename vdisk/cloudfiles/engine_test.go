package cloudfiles

import (
	"errors"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/koulaxizis/oros-drive/vault"
	"github.com/koulaxizis/oros-drive/vdisk"
)

const pass = "cloudfiles test passphrase"

var (
	keyOnce sync.Once
	keyDoc  []byte
	keys    *vault.Keys
)

// fake is a folder with Windows' placeholder rules, in memory.
type fake struct {
	mu      sync.Mutex
	e       *Engine
	files   map[string]*ffile
	gone    bool // the folder itself is missing
	busy    map[string]bool
	trashed []string
}

type ffile struct {
	dir, ph, inSync bool
	data            []byte // nil = no content here yet (a dehydrated placeholder)
	id              string
	size            int64
	mtime           time.Time
}

func (f *fake) Scan() (map[string]Entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.gone {
		return nil, os.ErrNotExist
	}
	out := map[string]Entry{}
	for rel, x := range f.files {
		out[rel] = Entry{Dir: x.dir, Placeholder: x.ph, InSync: x.inSync, Size: x.size, MTime: x.mtime}
	}
	return out, nil
}

func (f *fake) Identity(rel string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if x, ok := f.files[rel]; ok && x.ph {
		return x.id, nil
	}
	return "", os.ErrNotExist
}

func (f *fake) parentOK(rel string) error {
	if d := path.Dir(rel); d != "." {
		if x, ok := f.files[d]; !ok || !x.dir {
			return os.ErrNotExist
		}
	}
	return nil
}

func (f *fake) CreateFile(rel string, i vdisk.Info) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.files[rel]; ok {
		return os.ErrExist
	}
	if err := f.parentOK(rel); err != nil {
		return err
	}
	f.files[rel] = &ffile{ph: true, inSync: true, id: i.Hash, size: i.Size, mtime: i.MTime}
	return nil
}

func (f *fake) CreateDir(rel string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.files[rel]; ok {
		return os.ErrExist
	}
	if err := f.parentOK(rel); err != nil {
		return err
	}
	f.files[rel] = &ffile{dir: true, ph: true, inSync: true}
	return nil
}

func (f *fake) Update(rel string, i vdisk.Info) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	x, ok := f.files[rel]
	if !ok {
		return os.ErrNotExist
	}
	if !x.inSync {
		return ErrChanged
	}
	if f.busy[rel] {
		return ErrBusy
	}
	x.id, x.data, x.size, x.mtime = i.Hash, nil, i.Size, i.MTime
	return nil
}

// hydrate fills a placeholder the way Windows asks the engine to.
func (f *fake) hydrate(x *ffile) error {
	if x.data != nil || !x.ph {
		return nil
	}
	local, err := f.e.Fetch(x.id)
	if err != nil {
		return err
	}
	b, err := os.ReadFile(local)
	if err != nil {
		return err
	}
	x.data = b
	return nil
}

func (f *fake) Snapshot(rel, tmp string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	x, ok := f.files[rel]
	if !ok {
		return os.ErrNotExist
	}
	if f.busy[rel] {
		return ErrBusy
	}
	if err := f.hydrate(x); err != nil {
		return err
	}
	return os.WriteFile(tmp, x.data, 0o600)
}

func (f *fake) MarkInSync(rel, hash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	x, ok := f.files[rel]
	if !ok {
		return os.ErrNotExist
	}
	if vault.SHA256Hex(x.data) != hash {
		return ErrChanged
	}
	x.ph, x.inSync, x.id = true, true, hash
	return nil
}

func (f *fake) MarkDirInSync(rel string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[rel].ph, f.files[rel].inSync = true, true
	return nil
}

func (f *fake) Remove(rel string, dir bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	x, ok := f.files[rel]
	if !ok {
		return nil
	}
	if dir {
		for k := range f.files {
			if strings.HasPrefix(k, rel+"/") {
				return errors.New("not empty")
			}
		}
	} else if !x.inSync {
		return ErrChanged
	}
	if x.data != nil {
		f.trashed = append(f.trashed, rel)
	}
	delete(f.files, rel)
	return nil
}

// ----- what a person does in Explorer -----

func (f *fake) write(rel, s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if x, ok := f.files[rel]; ok {
		x.data, x.inSync, x.size, x.mtime = []byte(s), false, int64(len(s)), time.Now().Add(-time.Minute)
		return
	}
	f.files[rel] = &ffile{data: []byte(s), size: int64(len(s)), mtime: time.Now().Add(-time.Minute)}
}

func (f *fake) mkdir(rel string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[rel] = &ffile{dir: true}
}

func (f *fake) read(t *testing.T, rel string) string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	x, ok := f.files[rel]
	if !ok {
		t.Fatalf("%s is not in the folder", rel)
	}
	if err := f.hydrate(x); err != nil {
		t.Fatal(err)
	}
	return string(x.data)
}

func (f *fake) hydrated(rel string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	x, ok := f.files[rel]
	return ok && x.data != nil
}

// del deletes the way Explorer does: Windows asks the engine first.
func (f *fake) del(t *testing.T, rel string) {
	t.Helper()
	f.mu.Lock()
	ph := f.files[rel] != nil && f.files[rel].ph
	f.mu.Unlock()
	if ph {
		if err := f.e.OnDelete(rel); err != nil {
			t.Fatal(err)
		}
	}
	f.mu.Lock()
	delete(f.files, rel)
	f.mu.Unlock()
}

func (f *fake) rename(t *testing.T, from, to string) {
	t.Helper()
	f.mu.Lock()
	ph := f.files[from] != nil && f.files[from].ph
	f.mu.Unlock()
	if ph {
		if err := f.e.OnRename(from, to, true, true); err != nil {
			t.Fatal(err)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, x := range f.files {
		if k == from || strings.HasPrefix(k, from+"/") {
			delete(f.files, k)
			f.files[to+strings.TrimPrefix(k, from)] = x
		}
	}
}

func (f *fake) names() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for k, x := range f.files {
		if x.dir {
			k += "/"
		}
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

// ----- setup -----

type env struct {
	t     *testing.T
	store *vault.DirStore
	root  string
	f     *fake
	e     *Engine
	clock time.Time
}

func newEnv(t *testing.T) *env {
	keyOnce.Do(func() {
		var err error
		keyDoc, keys, err = vault.NewKeyFile(vault.NewPassphrase(pass))
		if err != nil {
			panic(err)
		}
	})
	root := t.TempDir()
	st := vault.NewDirStore(filepath.Join(root, "cloud"))
	if _, err := st.Put(vault.KeyFileKey, keyDoc, vault.CreateOnly); err != nil {
		t.Fatal(err)
	}
	en := &env{t: t, store: st, root: root, clock: time.Now()}
	d := &vdisk.Disk{Vault: vault.New(st, nil).WithKeys(keys), CacheDir: filepath.Join(root, "cache"), TTL: time.Nanosecond}
	en.f = &fake{files: map[string]*ffile{}, busy: map[string]bool{}}
	en.e = &Engine{Disk: d, Local: en.f, StatePath: filepath.Join(root, "state.json"), Settle: time.Nanosecond,
		GOOS: "windows", Now: func() time.Time { return en.clock }}
	en.f.e = en.e
	return en
}

func (en *env) browser(files map[string]string, del ...string) {
	en.t.Helper()
	v := vault.New(en.store, vault.NewPassphrase(pass)).WithKeys(keys)
	in := map[string]vault.File{}
	for p, s := range files {
		in["/internal/"+p] = vault.File{Data: []byte(s)}
	}
	for i := range del {
		del[i] = "/internal/" + del[i]
	}
	if err := v.PutFiles(in, del); err != nil {
		en.t.Fatal(err)
	}
}

func (en *env) vault() map[string]string {
	en.t.Helper()
	v := vault.New(en.store, nil).WithKeys(keys)
	r, err := v.FetchManifest()
	if err != nil {
		en.t.Fatal(err)
	}
	out := map[string]string{}
	for p, e := range r.Files {
		b, err := v.Download(e)
		if err != nil {
			en.t.Fatal(err)
		}
		out[strings.TrimPrefix(p, "/internal/")] = string(b)
	}
	return out
}

func (en *env) pass() Report {
	en.t.Helper()
	en.clock = en.clock.Add(time.Second)
	// The disk's manifest TTL runs on the real clock, too coarse on
	// Windows to tell two steps of a test apart.
	if err := en.e.Disk.Refresh(true); err != nil {
		en.t.Fatal(err)
	}
	rep, err := en.e.Pass()
	if err != nil {
		en.t.Fatal(err)
	}
	if len(rep.Errors) > 0 {
		en.t.Fatal(rep.Errors)
	}
	return rep
}

func keysOf(m map[string]string) string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

// ----- tests -----

func TestPlaceholdersComeFromOrOS(t *testing.T) {
	en := newEnv(t)
	en.browser(map[string]string{"Docs/a.txt": "alpha", "Docs/sub/b.txt": "beta", "c.md": "c", "bad:name.txt": "x"})
	rep := en.pass()
	if got := en.f.names(); got != "Docs/ Docs/a.txt Docs/sub/ Docs/sub/b.txt c.md" {
		t.Fatal(got)
	}
	if len(rep.Skipped) != 1 || rep.Skipped[0] != "bad:name.txt" {
		t.Fatal(rep.Skipped) // a name Windows cannot hold stays in orOS
	}
	if en.f.hydrated("Docs/a.txt") {
		t.Fatal("content came before it was opened")
	}
	if s := en.f.read(t, "Docs/sub/b.txt"); s != "beta" {
		t.Fatal(s)
	}
	// Nothing to do the second time.
	before := en.vault()
	en.pass()
	if keysOf(en.vault()) != keysOf(before) {
		t.Fatal("a quiet pass changed the vault")
	}
}

func TestFilesWrittenHereAreUploaded(t *testing.T) {
	en := newEnv(t)
	en.browser(map[string]string{"Docs/a.txt": "alpha"})
	en.pass()
	en.f.write("Docs/new.txt", "made in Windows")
	en.f.write("Docs/a.txt", "alpha, edited")
	en.f.mkdir("Empty")
	en.pass()
	v := en.vault()
	if v["Docs/new.txt"] != "made in Windows" || v["Docs/a.txt"] != "alpha, edited" || len(v) != 2 {
		t.Fatal(v)
	}
	if !en.f.files["Docs/new.txt"].inSync || !en.f.files["Empty"].ph {
		t.Fatal("not marked in sync")
	}
	tree, _, _ := en.e.Disk.Tree(true)
	if i, ok := tree["/internal/Empty"]; !ok || !i.Dir {
		t.Fatal("the empty folder is not shown in orOS")
	}
	// A file still being written waits.
	en.e.Settle = time.Hour
	en.f.mu.Lock()
	en.f.files["late.txt"] = &ffile{data: []byte("x"), size: 1, mtime: en.clock}
	en.f.mu.Unlock()
	en.pass()
	if _, ok := en.vault()["late.txt"]; ok {
		t.Fatal("uploaded while being written")
	}
	en.e.Settle = time.Nanosecond
	// So does a file a program holds open for writing.
	en.f.busy["late.txt"] = true
	en.pass()
	if _, ok := en.vault()["late.txt"]; ok {
		t.Fatal("uploaded while open")
	}
	delete(en.f.busy, "late.txt")
	en.pass()
	if en.vault()["late.txt"] != "x" {
		t.Fatal("not uploaded once closed")
	}
}

func TestChangedOnBothSidesKeepsBoth(t *testing.T) {
	en := newEnv(t)
	en.browser(map[string]string{"a.txt": "one"})
	en.pass()
	en.f.write("a.txt", "two, here")
	en.browser(map[string]string{"a.txt": "two, in orOS"})
	en.pass()
	v := en.vault()
	if v["a.txt"] != "two, here" || len(v) != 2 {
		t.Fatal(v)
	}
	var copyName string
	for k, s := range v {
		if strings.HasPrefix(k, "a (conflict ") && s == "two, in orOS" {
			copyName = k
		}
	}
	if copyName == "" {
		t.Fatal("no conflict copy", v)
	}
	en.pass()
	if en.f.read(t, copyName) != "two, in orOS" {
		t.Fatal("the conflict copy is not in the folder")
	}
}

func TestChangesInOrOSReachInSyncFilesOnly(t *testing.T) {
	en := newEnv(t)
	en.browser(map[string]string{"a.txt": "one", "b.txt": "one"})
	en.pass()
	if en.f.read(t, "a.txt") != "one" {
		t.Fatal()
	}
	en.browser(map[string]string{"a.txt": "two", "b.txt": "two"})
	en.f.busy["b.txt"] = true // open in a program: tried again later
	en.pass()
	if en.f.hydrated("a.txt") {
		t.Fatal("the old content was kept")
	}
	if en.f.read(t, "a.txt") != "two" {
		t.Fatal("new content not shown")
	}
	delete(en.f.busy, "b.txt")
	en.pass()
	if en.f.read(t, "b.txt") != "two" {
		t.Fatal("not updated after it was closed")
	}
}

func TestDeletionsGoBothWays(t *testing.T) {
	en := newEnv(t)
	en.browser(map[string]string{"Docs/a.txt": "a", "Docs/b.txt": "b", "Docs/c.txt": "c", "d.txt": "d"})
	en.pass()
	en.f.read(t, "Docs/b.txt")
	en.f.write("Docs/c.txt", "c, edited here")
	en.browser(nil, "Docs/a.txt", "Docs/b.txt", "Docs/c.txt")
	en.pass()
	if got := en.f.names(); got != "Docs/ Docs/c.txt d.txt" {
		t.Fatal(got)
	}
	if len(en.f.trashed) != 1 || en.f.trashed[0] != "Docs/b.txt" {
		t.Fatal("downloaded content not kept in the trash", en.f.trashed)
	}
	// The edit beats the deletion: it is back in orOS.
	if en.vault()["Docs/c.txt"] != "c, edited here" {
		t.Fatal(en.vault())
	}
	// Deleted here: Windows asks first, the vault follows.
	en.f.del(t, "d.txt")
	if _, ok := en.vault()["d.txt"]; ok {
		t.Fatal("deleted file still in orOS")
	}
	en.pass()
	if got := en.f.names(); got != "Docs/ Docs/c.txt" {
		t.Fatal(got)
	}
	// A folder deleted in orOS goes here once it is empty.
	en.browser(nil, "Docs/c.txt")
	en.pass()
	if got := en.f.names(); got != "" {
		t.Fatal(got)
	}
}

func TestAnEmptyOrResetVaultDeletesNothing(t *testing.T) {
	en := newEnv(t)
	en.browser(map[string]string{"a.txt": "a", "Docs/b.txt": "b"})
	en.pass()
	en.f.read(t, "a.txt")
	if err := en.store.Delete(vault.ManifestKey); err != nil {
		t.Fatal(err)
	}
	en.pass()
	if got := en.f.names(); got != "Docs/ Docs/b.txt a.txt" {
		t.Fatal(got)
	}
}

func TestMissingFolderDeletesNothing(t *testing.T) {
	en := newEnv(t)
	en.browser(map[string]string{"a.txt": "a"})
	en.pass()
	en.f.gone = true
	en.clock = en.clock.Add(time.Hour)
	if _, err := en.e.Pass(); err == nil {
		t.Fatal("a missing folder passed")
	}
	if len(en.vault()) != 1 {
		t.Fatal("the vault lost files")
	}
}

func TestManyDeletionsWait(t *testing.T) {
	en := newEnv(t)
	en.e.MaxDeletes = 2
	en.browser(map[string]string{"a": "a", "b": "b", "c": "c"})
	en.pass()
	en.browser(nil, "a", "b", "c")
	if rep := en.pass(); rep.Held != 3 {
		t.Fatal(rep.Held)
	}
	if got := en.f.names(); got != "a b c" {
		t.Fatal(got)
	}
}

func TestDeletedWhileTheAppWasOff(t *testing.T) {
	en := newEnv(t)
	en.browser(map[string]string{"a.txt": "a", "b.txt": "b"})
	en.pass()
	en.f.mu.Lock()
	delete(en.f.files, "a.txt") // no callback
	en.f.mu.Unlock()
	en.pass()
	if _, ok := en.vault()["a.txt"]; !ok {
		t.Fatal("deleted before the grace time")
	}
	if en.f.names() != "b.txt" {
		t.Fatal("brought back", en.f.names())
	}
	en.clock = en.clock.Add(time.Minute)
	en.pass()
	if _, ok := en.vault()["a.txt"]; ok {
		t.Fatal("not deleted in orOS")
	}
}

func TestSavingThroughATempNameKeepsOneFile(t *testing.T) {
	en := newEnv(t)
	en.browser(map[string]string{"report.docx": "v1"})
	en.pass()
	en.f.read(t, "report.docx")
	// Word: original → ~WRL0001.tmp, new content → original, temp deleted.
	en.f.rename(t, "report.docx", "~WRL0001.tmp")
	en.pass() // a pass in the middle of the save changes nothing
	if en.f.names() != "~WRL0001.tmp" {
		t.Fatal("a pass mid-save touched the folder:", en.f.names())
	}
	en.f.write("report.docx", "v2")
	en.f.del(t, "~WRL0001.tmp")
	en.pass()
	v := en.vault()
	if v["report.docx"] != "v2" || len(v) != 1 {
		t.Fatal("expected one file with the new content", v)
	}
}

func TestRenamesGoToOrOS(t *testing.T) {
	en := newEnv(t)
	en.browser(map[string]string{"Docs/a.txt": "a", "Docs/sub/b.txt": "b"})
	en.pass()
	en.f.rename(t, "Docs/a.txt", "Docs/renamed.txt")
	en.f.rename(t, "Docs/sub", "Moved")
	v := en.vault()
	if v["Docs/renamed.txt"] != "a" || v["Moved/b.txt"] != "b" || len(v) != 2 {
		t.Fatal(v)
	}
	en.pass()
	if got := en.f.names(); got != "Docs/ Docs/renamed.txt Moved/ Moved/b.txt" {
		t.Fatal(got)
	}
	if len(en.vault()) != 2 {
		t.Fatal("duplicates after a pass", en.vault())
	}
	// Moved out of the folder (or to the Recycle Bin) = deleted.
	if err := en.e.OnRename("Moved/b.txt", "", true, false); err != nil {
		t.Fatal(err)
	}
	if _, ok := en.vault()["Moved/b.txt"]; ok {
		t.Fatal("moved-out file still in orOS")
	}
}

func TestTooBigStaysHere(t *testing.T) {
	en := newEnv(t)
	en.e.Disk.MaxFile = 4
	en.f.write("big.bin", "0123456789")
	if rep := en.pass(); len(rep.LocalOnly) != 1 {
		t.Fatal(rep)
	}
	if rep := en.pass(); len(rep.LocalOnly) != 1 || len(en.vault()) != 0 {
		t.Fatal(rep)
	}
}

func TestStateSurvivesARestart(t *testing.T) {
	en := newEnv(t)
	en.browser(map[string]string{"a.txt": "a"})
	en.pass()
	again := &Engine{Disk: en.e.Disk, Local: en.f, StatePath: en.e.StatePath, GOOS: "windows", Settle: time.Nanosecond}
	if got := keysOf(again.Synced()); got != "a.txt" {
		t.Fatal(got)
	}
}

// Windows asks first and carries the deletion or move out after the
// answer; a pass that runs in between must not bring the file back.
func TestAPassDuringADeleteOrMoveBringsNothingBack(t *testing.T) {
	en := newEnv(t)
	en.browser(map[string]string{"a.txt": "a", "Docs/b.txt": "b"})
	en.pass()
	if err := en.e.OnDelete("a.txt"); err != nil {
		t.Fatal(err)
	}
	if err := en.e.OnRename("Docs", "Moved", true, true); err != nil {
		t.Fatal(err)
	}
	en.pass() // the folder still shows a.txt and Docs/b.txt
	v := en.vault()
	if _, ok := v["a.txt"]; ok || v["Moved/b.txt"] != "b" || len(v) != 1 {
		t.Fatal("brought back", v)
	}
	en.f.mu.Lock()
	delete(en.f.files, "a.txt")
	en.f.files["Moved"], en.f.files["Moved/b.txt"] = en.f.files["Docs"], en.f.files["Docs/b.txt"]
	delete(en.f.files, "Docs")
	delete(en.f.files, "Docs/b.txt")
	en.f.mu.Unlock()
	en.pass()
	if got := en.f.names(); got != "Moved/ Moved/b.txt" {
		t.Fatal(got)
	}
	if v := en.vault(); len(v) != 1 {
		t.Fatal(v)
	}
	// A new file under the old name later is a new file.
	en.f.write("a.txt", "new")
	en.pass()
	if en.vault()["a.txt"] != "new" {
		t.Fatal("a new file under a deleted name was not uploaded")
	}
}
