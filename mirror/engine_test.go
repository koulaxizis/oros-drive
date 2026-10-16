package mirror

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/koulaxizis/oros-drive/vault"
)

// One vault key for the whole package: deriving it takes 600k PBKDF2
// rounds.
var (
	keyOnce sync.Once
	keyDoc  []byte
	keys    *vault.Keys
)

const testPass = "mirror test passphrase"

type env struct {
	t     *testing.T
	store *vault.DirStore
	root  string
	pass  *vault.Passphrase
}

func newEnv(t *testing.T) *env {
	keyOnce.Do(func() {
		var err error
		keyDoc, keys, err = vault.NewKeyFile(vault.NewPassphrase(testPass))
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

// device is one computer mirroring folders into the shared vault.
type device struct {
	*Engine
	name   string
	env    *env
	pairs  []Pair
	events []Event
}

func (e *env) device(name string) *device {
	d := &device{env: e, name: name}
	dir := filepath.Join(e.root, name)
	d.Engine = &Engine{
		Vault:    vault.New(e.store, nil).WithKeys(keys),
		StateDir: filepath.Join(dir, "state"),
		Trash:    &DirTrash{Root: filepath.Join(dir, "trash")},
		Opt:      Options{StableAfter: -1},
	}
	d.Log = func(ev Event) { d.events = append(d.events, ev) }
	return d
}

func (d *device) folder(id, remote string) string {
	local := filepath.Join(d.env.root, d.name+"-"+id)
	if err := os.MkdirAll(local, 0o755); err != nil {
		d.env.t.Fatal(err)
	}
	d.pairs = append(d.pairs, Pair{ID: id, Local: local, Remote: remote})
	return local
}

func (d *device) sync(approve ...string) *Result {
	d.env.t.Helper()
	ap := map[string]bool{}
	for _, a := range approve {
		ap[a] = true
	}
	r, err := d.Sync(d.pairs, ap)
	if err != nil {
		d.env.t.Fatalf("sync: %v", err)
	}
	return r
}

// browser stands in for orOS: it edits the vault directly.
func (e *env) browser() *vault.Vault {
	if e.pass == nil {
		e.pass = vault.NewPassphrase(testPass)
	}
	return vault.New(e.store, e.pass).WithKeys(keys)
}

func (e *env) cloud() map[string]string {
	e.t.Helper()
	r, err := e.browser().FetchManifest()
	if err != nil {
		e.t.Fatal(err)
	}
	out := map[string]string{}
	for p, ent := range r.Files {
		b, err := e.browser().Download(ent)
		if err != nil {
			e.t.Fatalf("%s: %v", p, err)
		}
		out[p] = string(b)
	}
	return out
}

func (e *env) put(path, text string) {
	e.t.Helper()
	if err := e.browser().PutFiles(map[string]vault.File{path: {Data: []byte(text)}}, nil); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) del(path string) {
	e.t.Helper()
	if err := e.browser().PutFiles(nil, []string{path}); err != nil {
		e.t.Fatal(err)
	}
}

func write(t *testing.T, root, rel, text string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	// Each write gets a distinct time, as a real edit would.
	bump(t, p)
}

var clock = time.Now().Add(-time.Hour)

func bump(t *testing.T, p string) {
	clock = clock.Add(time.Second)
	if err := os.Chtimes(p, clock, clock); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, root, rel string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if os.IsNotExist(err) {
		return "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(b), true
}

func files(t *testing.T, root string) []string {
	var out []string
	filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() {
			rel, _ := filepath.Rel(root, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(out)
	return out
}

const R = DriveRoot + "/PC/Docs"

func TestUploadThenSecondDeviceDownloads(t *testing.T) {
	e := newEnv(t)
	a := e.device("a")
	la := a.folder("docs", R)
	write(t, la, "report.txt", "quarterly")
	write(t, la, "sub/deep/photo.jpg", "jpeg")
	r := a.sync()
	if r.Uploaded != 2 {
		t.Fatalf("uploaded %d", r.Uploaded)
	}
	c := e.cloud()
	if c[R+"/report.txt"] != "quarterly" || c[R+"/sub/deep/photo.jpg"] != "jpeg" {
		t.Fatalf("cloud %v", c)
	}
	b := e.device("b")
	lb := b.folder("docs", R)
	if r := b.sync(); r.Downloaded != 2 || r.Uploaded != 0 {
		t.Fatalf("b: %+v", r)
	}
	if got, _ := read(t, lb, "sub/deep/photo.jpg"); got != "jpeg" {
		t.Fatal(got)
	}
}

func TestIdlePassWritesNothing(t *testing.T) {
	e := newEnv(t)
	a := e.device("a")
	la := a.folder("docs", R)
	write(t, la, "a.txt", "a")
	a.sync()
	rev, _, _ := e.store.Rev(vault.ManifestKey)
	r := a.sync()
	r2 := a.sync()
	if r.Uploaded+r.Downloaded+r2.Uploaded+r2.Downloaded != 0 {
		t.Fatalf("idle passes moved files: %+v %+v", r, r2)
	}
	if now, _, _ := e.store.Rev(vault.ManifestKey); now != rev {
		t.Fatal("an idle pass rewrote the manifest")
	}
}

func TestOwnDownloadsAreNotUploadedAgain(t *testing.T) {
	e := newEnv(t)
	e.put(R+"/from-oros.txt", "hello")
	a := e.device("a")
	a.folder("docs", R)
	if r := a.sync(); r.Downloaded != 1 {
		t.Fatalf("%+v", r)
	}
	rev, _, _ := e.store.Rev(vault.ManifestKey)
	if r := a.sync(); r.Uploaded != 0 {
		t.Fatalf("own write uploaded: %+v", r)
	}
	if now, _, _ := e.store.Rev(vault.ManifestKey); now != rev {
		t.Fatal("manifest rewritten")
	}
}

func TestEditsFlowBothWays(t *testing.T) {
	e := newEnv(t)
	a := e.device("a")
	la := a.folder("docs", R)
	write(t, la, "a.txt", "v1")
	a.sync()
	write(t, la, "a.txt", "v2 local")
	if r := a.sync(); r.Uploaded != 1 {
		t.Fatalf("%+v", r)
	}
	if e.cloud()[R+"/a.txt"] != "v2 local" {
		t.Fatal("not uploaded")
	}
	e.put(R+"/a.txt", "v3 from orOS")
	if r := a.sync(); r.Downloaded != 1 {
		t.Fatalf("%+v", r)
	}
	if got, _ := read(t, la, "a.txt"); got != "v3 from orOS" {
		t.Fatal(got)
	}
}

func TestDeletesFlowBothWaysAndLocalGoesToTrash(t *testing.T) {
	e := newEnv(t)
	a := e.device("a")
	la := a.folder("docs", R)
	write(t, la, "keep.txt", "k")
	write(t, la, "gone-here.txt", "x")
	write(t, la, "old/gone-there.txt", "y")
	a.sync()
	os.Remove(filepath.Join(la, "gone-here.txt"))
	e.del(R + "/old/gone-there.txt")
	r := a.sync()
	if r.DeletedRemote != 1 || r.DeletedLocal != 1 {
		t.Fatalf("%+v", r)
	}
	c := e.cloud()
	if _, ok := c[R+"/gone-here.txt"]; ok {
		t.Fatal("cloud still has the file deleted here")
	}
	if _, ok := read(t, la, "old/gone-there.txt"); ok {
		t.Fatal("local still has the file deleted in orOS")
	}
	if _, err := os.Stat(filepath.Join(la, "old")); !os.IsNotExist(err) {
		t.Fatal("emptied folder left behind")
	}
	tr := files(t, a.Trash.(*DirTrash).Root)
	if len(tr) != 1 || !strings.HasSuffix(tr[0], "Docs/old/gone-there.txt") {
		t.Fatalf("trash %v", tr)
	}
}

func TestConflictKeepsBothVersionsOnce(t *testing.T) {
	e := newEnv(t)
	a := e.device("a")
	la := a.folder("docs", R)
	write(t, la, "plan.md", "base")
	a.sync()
	write(t, la, "plan.md", "local edit")
	e.put(R+"/plan.md", "orOS edit")
	r := a.sync()
	if r.Conflicts != 1 {
		t.Fatalf("%+v", r)
	}
	if got, _ := read(t, la, "plan.md"); got != "local edit" {
		t.Fatal("local lost its path:", got)
	}
	fs := files(t, la)
	if len(fs) != 2 || !strings.HasPrefix(fs[0], "plan (conflict ") || !strings.HasSuffix(fs[0], ").md") {
		t.Fatalf("files %v", fs)
	}
	if got, _ := read(t, la, fs[0]); got != "orOS edit" {
		t.Fatal(got)
	}
	c := e.cloud()
	if c[R+"/plan.md"] != "local edit" || c[R+"/"+fs[0]] != "orOS edit" {
		t.Fatalf("cloud %v", c)
	}
	if r := a.sync(); r.Conflicts+r.Uploaded+r.Downloaded != 0 {
		t.Fatalf("not quiet after the conflict: %+v", r)
	}
}

func TestEditBeatsDelete(t *testing.T) {
	e := newEnv(t)
	a := e.device("a")
	la := a.folder("docs", R)
	write(t, la, "x.txt", "base")
	write(t, la, "y.txt", "base")
	a.sync()
	// x: deleted here, edited in orOS -> the edit comes back.
	os.Remove(filepath.Join(la, "x.txt"))
	e.put(R+"/x.txt", "edited in orOS")
	// y: edited here, deleted in orOS -> goes back up.
	write(t, la, "y.txt", "edited here")
	e.del(R + "/y.txt")
	a.sync()
	if got, _ := read(t, la, "x.txt"); got != "edited in orOS" {
		t.Fatal("x:", got)
	}
	if e.cloud()[R+"/y.txt"] != "edited here" {
		t.Fatal("y not restored in the cloud")
	}
}

func TestFirstSyncNeverDeletes(t *testing.T) {
	e := newEnv(t)
	e.put(R+"/cloud-only.txt", "c")
	e.put(R+"/both.txt", "cloud version")
	a := e.device("a")
	la := a.folder("docs", R)
	write(t, la, "local-only.txt", "l")
	write(t, la, "both.txt", "local version")
	r := a.sync()
	if r.DeletedLocal+r.DeletedRemote != 0 {
		t.Fatalf("%+v", r)
	}
	c := e.cloud()
	if c[R+"/cloud-only.txt"] != "c" || c[R+"/local-only.txt"] != "l" || c[R+"/both.txt"] != "local version" {
		t.Fatalf("cloud %v", c)
	}
	if got, _ := read(t, la, "cloud-only.txt"); got != "c" {
		t.Fatal(got)
	}
	if len(files(t, la)) != 4 { // + the conflict copy of both.txt
		t.Fatal(files(t, la))
	}
}

func TestManyDeletionsWaitForYes(t *testing.T) {
	e := newEnv(t)
	a := e.device("a")
	a.Opt.MaxDeletes = 5
	la := a.folder("docs", R)
	for i := 0; i < 8; i++ {
		write(t, la, "f"+itoa(i)+".txt", "x")
	}
	a.sync()
	for i := 0; i < 8; i++ {
		os.Remove(filepath.Join(la, "f"+itoa(i)+".txt"))
	}
	r := a.sync()
	if st := r.Folders["docs"]; st.State != StateHeld || st.HeldDeletes != 8 || r.DeletedRemote != 0 {
		t.Fatalf("%+v %+v", r, st)
	}
	if len(e.cloud()) != 8 {
		t.Fatal("held deletions reached the cloud")
	}
	if r := a.sync("docs"); r.DeletedRemote != 8 || r.Folders["docs"].State != StateOK {
		t.Fatalf("%+v", r)
	}
	if len(e.cloud()) != 0 {
		t.Fatal(e.cloud())
	}
}

func TestMissingFolderTouchesNothing(t *testing.T) {
	e := newEnv(t)
	a := e.device("a")
	la := a.folder("docs", R)
	write(t, la, "a.txt", "a")
	a.sync()
	os.RemoveAll(la) // an unplugged disk
	r := a.sync()
	if r.Folders["docs"].State != StateMissing || len(e.cloud()) != 1 {
		t.Fatalf("%+v %v", r.Folders["docs"], e.cloud())
	}
}

func TestResetVaultStops(t *testing.T) {
	e := newEnv(t)
	a := e.device("a")
	la := a.folder("docs", R)
	write(t, la, "a.txt", "a")
	a.sync()
	e.store.Delete(vault.ManifestKey)
	r := a.sync()
	if r.Folders["docs"].State != StateReset {
		t.Fatalf("%+v", r.Folders["docs"])
	}
	if _, ok := read(t, la, "a.txt"); !ok {
		t.Fatal("local file removed")
	}
	if rev, ok, _ := e.store.Rev(vault.ManifestKey); ok {
		t.Fatal("manifest re-created", rev)
	}
}

func TestFileStillBeingWrittenWaits(t *testing.T) {
	e := newEnv(t)
	a := e.device("a")
	a.Opt.StableAfter = time.Hour
	la := a.folder("docs", R)
	os.WriteFile(filepath.Join(la, "growing.iso"), []byte("part"), 0o644)
	r := a.sync()
	if r.Uploaded != 0 || !r.Busy {
		t.Fatalf("%+v", r)
	}
}

func TestIgnoredNamesStayPut(t *testing.T) {
	e := newEnv(t)
	e.put(R+"/~$report.docx", "word lock made in orOS")
	a := e.device("a")
	la := a.folder("docs", R)
	write(t, la, "~$budget.xlsx", "lock")
	write(t, la, ".orosdrive-abc", "half download")
	write(t, la, "movie.mp4.part", "partial")
	write(t, la, "real.txt", "real")
	a.sync()
	a.sync()
	c := e.cloud()
	if len(c) != 2 || c[R+"/real.txt"] != "real" || c[R+"/~$report.docx"] == "" {
		t.Fatalf("cloud %v", c)
	}
	if _, ok := read(t, la, "~$report.docx"); ok {
		t.Fatal("ignored cloud file downloaded")
	}
}

func TestCloudFileInsideIgnoredFolderStays(t *testing.T) {
	e := newEnv(t)
	e.put(R+"/.Trash-1000/files/x.txt", "someone's trash")
	a := e.device("a")
	a.folder("docs", R)
	a.sync()
	a.sync()
	if len(e.cloud()) != 1 {
		t.Fatal("the cloud file inside an ignored folder was deleted")
	}
}

func TestNonUTF8NamesAreLeftAlone(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("only Linux allows such names")
	}
	e := newEnv(t)
	a := e.device("a")
	la := a.folder("docs", R)
	write(t, la, "ok.txt", "ok")
	write(t, la, "bad\xff.txt", "latin-1 name")
	r := a.sync()
	a.sync()
	if r.Uploaded != 1 || len(r.Folders["docs"].Skipped) != 1 || len(e.cloud()) != 1 {
		t.Fatalf("%+v %v", r, e.cloud())
	}
	if _, ok := read(t, la, "bad\xff.txt"); !ok {
		t.Fatal("the file was moved")
	}
}

// raceStore lets another device commit between this device's manifest
// read and its write, once.
type raceStore struct {
	*vault.DirStore
	once  sync.Once
	other func()
}

func (s *raceStore) Put(key string, data []byte, mode vault.PutMode) (string, error) {
	if key == vault.ManifestKey {
		s.once.Do(s.other)
	}
	return s.DirStore.Put(key, data, mode)
}

func TestConcurrentWriterIsNeverOverwritten(t *testing.T) {
	e := newEnv(t)
	a := e.device("a")
	la := a.folder("docs", R)
	write(t, la, "a.txt", "a")
	a.sync()
	rs := &raceStore{DirStore: e.store}
	rs.other = func() {
		e.put(R+"/a.txt", "changed in orOS meanwhile")
		e.put("/internal/Notes/n.md", "note")
	}
	a.Vault = vault.New(rs, nil).WithKeys(keys)
	write(t, la, "a.txt", "changed here")
	write(t, la, "b.txt", "new here")
	if _, err := a.Sync(a.pairs, nil); err != nil {
		t.Fatal(err)
	}
	c := e.cloud()
	if c["/internal/Notes/n.md"] != "note" || c[R+"/b.txt"] != "new here" || c[R+"/a.txt"] != "changed here" {
		t.Fatalf("cloud %v", c)
	}
	found := false
	for p, v := range c {
		if strings.Contains(p, "(conflict ") && v == "changed in orOS meanwhile" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the other device's edit was lost: %v", c)
	}
}

func TestTooBigStaysLocal(t *testing.T) {
	e := newEnv(t)
	a := e.device("a")
	a.Opt.MaxFileSize = 10
	la := a.folder("docs", R)
	write(t, la, "big.bin", "more than ten bytes")
	write(t, la, "small", "ok")
	r := a.sync()
	if r.Uploaded != 1 || len(r.Folders["docs"].LocalOnly) != 1 {
		t.Fatalf("%+v %+v", r, r.Folders["docs"])
	}
}

func TestOtherVaultFilesArePreserved(t *testing.T) {
	e := newEnv(t)
	e.put("/internal/Notes/todo.md", "milk")
	e.put(DriveRoot+"/Laptop/Docs/x.txt", "other computer")
	a := e.device("a")
	la := a.folder("docs", R)
	write(t, la, "a.txt", "a")
	a.sync()
	os.Remove(filepath.Join(la, "a.txt"))
	a.sync()
	c := e.cloud()
	if len(c) != 2 || c["/internal/Notes/todo.md"] != "milk" || c[DriveRoot+"/Laptop/Docs/x.txt"] != "other computer" {
		t.Fatalf("cloud %v", c)
	}
}

func TestNamesWindowsCannotHoldAreSkipped(t *testing.T) {
	old := goos
	goos = "windows"
	defer func() { goos = old }()
	e := newEnv(t)
	e.put(R+"/a:b.txt", "colon")
	e.put(R+"/Same.txt", "1")
	e.put(R+"/same.txt", "2")
	a := e.device("a")
	la := a.folder("docs", R)
	r := a.sync()
	fs := files(t, la)
	if len(fs) != 1 || fs[0] != "Same.txt" {
		t.Fatalf("files %v", fs)
	}
	if len(r.Folders["docs"].Skipped) != 2 || len(e.cloud()) != 3 {
		t.Fatalf("%+v", r.Folders["docs"])
	}
	a.sync()
	if len(e.cloud()) != 3 {
		t.Fatal("skipped cloud files were deleted")
	}
}

func TestUnreadableFolderIsNotADeletion(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs POSIX permissions and a non-root user")
	}
	e := newEnv(t)
	a := e.device("a")
	la := a.folder("docs", R)
	write(t, la, "locked/a.txt", "a")
	a.sync()
	os.Chmod(filepath.Join(la, "locked"), 0)
	defer os.Chmod(filepath.Join(la, "locked"), 0o755)
	if r := a.sync(); r.DeletedRemote != 0 {
		t.Fatalf("%+v", r)
	}
	if len(e.cloud()) != 1 {
		t.Fatal("deleted")
	}
}

func TestLocalChangeDuringPassWins(t *testing.T) {
	e := newEnv(t)
	a := e.device("a")
	la := a.folder("docs", R)
	write(t, la, "a.txt", "v1")
	a.sync()
	e.put(R+"/a.txt", "from orOS")
	// The user saves between the scan and the download.
	lf := &localFile{Size: 2, MTime: 0}
	if err := a.download(&a.pairs[0], &State{Files: map[string]*Record{}}, "a.txt", filepath.Join(la, "a.txt"), vault.Entry{}, lf); err == nil {
		t.Fatal("expected a refusal")
	}
	if got, _ := read(t, la, "a.txt"); got != "v1" {
		t.Fatal(got)
	}
}

func TestDecideTable(t *testing.T) {
	cases := []struct {
		b, l, r string
		want    Action
	}{
		{"", "", "", Same}, {"x", "x", "x", Same}, {"", "a", "a", Same}, {"x", "", "", Same},
		{"x", "x", "y", Download}, {"x", "x", "", DeleteLocal},
		{"x", "y", "x", Upload}, {"x", "", "x", DeleteRemote},
		{"x", "", "y", Download}, {"x", "y", "", Upload},
		{"x", "y", "z", Conflict}, {"", "a", "b", Conflict},
		{"", "a", "", Upload}, {"", "", "b", Download},
	}
	for _, c := range cases {
		if got := decide(c.b, c.l, c.r); got != c.want {
			t.Errorf("decide(%q,%q,%q) = %v, want %v", c.b, c.l, c.r, got, c.want)
		}
	}
}

func TestConflictNameMatchesVaultJS(t *testing.T) {
	ts := time.Date(2026, 10, 16, 9, 5, 7, 0, time.Local)
	if got := ConflictName("a/plan.md", ts, 0); got != "a/plan (conflict 20261016-090507).md" {
		t.Fatal(got)
	}
	if got := ConflictName(".bashrc", ts, 2); got != ".bashrc (conflict 20261016-090507-2)" {
		t.Fatal(got)
	}
}
