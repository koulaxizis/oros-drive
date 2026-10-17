//go:build windows && (amd64 || arm64)

package cloudfiles

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/koulaxizis/oros-drive/vault"
	"github.com/koulaxizis/oros-drive/vdisk"
)

func dehydrated(t *testing.T, p string) bool {
	t.Helper()
	a, err := windows.GetFileAttributes(utf16(long(p)))
	if err != nil {
		t.Fatal(err)
	}
	return a&(windows.FILE_ATTRIBUTE_OFFLINE|windows.FILE_ATTRIBUTE_RECALL_ON_DATA_ACCESS) != 0
}

func listDir(t *testing.T, dir string) string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() {
			n += "/"
		}
		out = append(out, n)
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

// cmd runs a cmd.exe built-in, as another program would.
func cmd(t *testing.T, args ...string) {
	t.Helper()
	out, err := exec.Command("cmd", append([]string{"/c"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %v %s", args, err, out)
	}
}

// A real sync root in a temp folder: Explorer's view of the disk.
func TestExplorerFolder(t *testing.T) {
	en := newEnv(t) // the vault; the fake folder is not used here
	en.browser(map[string]string{"Docs/a.txt": "alpha", "Docs/sub/b.txt": "beta", "Docs/word.docx": "v1"})
	d := &vdisk.Disk{Vault: vault.New(en.store, nil).WithKeys(keys), CacheDir: filepath.Join(en.root, "cache"), TTL: time.Nanosecond}
	dir := filepath.Join(en.root, "orOS")
	Debug = func(msg string) { t.Log(time.Now().Format("15:04:05.000"), msg) }
	defer func() { Debug = nil }()
	srv, err := Mount(d, dir, Options{
		StatePath: filepath.Join(en.root, "cf.json"),
		TrashDir:  filepath.Join(en.root, "trash"),
		Interval:  time.Hour, // passes run by the test
		Settle:    time.Millisecond,
		Logf:      func(k, rel, detail string) { t.Logf("%s %s %s", k, rel, detail) },
	})
	if err != nil {
		t.Skipf("no Cloud Files here: %v", err)
	}
	unmounted := false
	defer func() {
		if !unmounted {
			srv.Unmount()
			Unregister(dir, "")
		}
	}()
	sync := func() {
		t.Helper()
		time.Sleep(20 * time.Millisecond) // past Settle
		if err := d.Refresh(true); err != nil {
			t.Fatal(err)
		}
		rep, err := srv.Sync()
		if err != nil {
			t.Fatal(err)
		}
		if len(rep.Errors) > 0 {
			t.Fatal(rep.Errors)
		}
	}
	sync()

	// Listed, not downloaded.
	if got := listDir(t, filepath.Join(dir, "Docs")); got != "a.txt sub/ word.docx" {
		t.Fatal(got)
	}
	a := filepath.Join(dir, "Docs", "a.txt")
	if fi, err := os.Stat(a); err != nil || fi.Size() != 5 {
		t.Fatal(fi, err)
	}
	if !dehydrated(t, a) {
		t.Fatal("content is there before anyone opened it")
	}
	// Opening downloads it (FETCH_DATA).
	if b, err := os.ReadFile(a); err != nil || string(b) != "alpha" {
		t.Fatalf("%q %v", b, err)
	}
	if dehydrated(t, a) {
		t.Fatal("still marked as not downloaded")
	}

	// New file and an edit go to orOS.
	if err := os.WriteFile(filepath.Join(dir, "Docs", "new.txt"), []byte("made in Windows"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a, []byte("alpha, edited"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "Empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	sync()
	v := en.vault()
	if v["Docs/new.txt"] != "made in Windows" || v["Docs/a.txt"] != "alpha, edited" {
		t.Fatal(v)
	}
	ents, err := srv.w.Scan()
	if err != nil {
		t.Fatal(err)
	}
	if e := ents["Docs/new.txt"]; !e.Placeholder || !e.InSync {
		t.Fatalf("not marked in sync: %+v", e)
	}
	if e := ents["Empty"]; !e.Placeholder {
		t.Fatalf("folder not a placeholder: %+v", e)
	}

	// Delete and rename in another program (Windows does not call the
	// provider for its own changes): orOS follows before Windows
	// carries them out.
	cmd(t, "del", filepath.Join(dir, "Docs", "new.txt"))
	if _, ok := en.vault()["Docs/new.txt"]; ok {
		t.Error("deleted file still in orOS")
	}
	cmd(t, "move", filepath.Join(dir, "Docs", "sub"), filepath.Join(dir, "Moved"))
	if v := en.vault(); v["Moved/b.txt"] != "beta" {
		t.Error("rename not in orOS", v)
	}

	// Changes in orOS reach the folder.
	en.browser(map[string]string{"Docs/a.txt": "from orOS", "Docs/added.txt": "added"})
	sync()
	if b, _ := os.ReadFile(a); string(b) != "from orOS" {
		t.Fatalf("%q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "Docs", "added.txt")); string(b) != "added" {
		t.Fatalf("%q", b)
	}
	// Deleted in orOS: gone here; the downloaded one went to the trash.
	en.browser(nil, "Docs/added.txt")
	sync()
	if _, err := os.Stat(filepath.Join(dir, "Docs", "added.txt")); !os.IsNotExist(err) {
		t.Fatal("still here", err)
	}

	// Word: original → ~WRL0001.tmp, new file → original, temp deleted.
	w := filepath.Join(dir, "Docs", "word.docx")
	if b, _ := os.ReadFile(w); string(b) != "v1" {
		t.Fatal(string(b))
	}
	os.WriteFile(filepath.Join(dir, "Docs", "~WRD0002.tmp"), []byte("v2"), 0o644)
	if err := os.Rename(w, filepath.Join(dir, "Docs", "~WRL0001.tmp")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "Docs", "~WRD0002.tmp"), w); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "Docs", "~WRL0001.tmp")); err != nil {
		t.Fatal(err)
	}
	// LibreOffice: new file renamed over the original.
	os.WriteFile(filepath.Join(dir, "Docs", "lu1.tmp"), []byte("office save"), 0o644)
	if err := os.Rename(filepath.Join(dir, "Docs", "lu1.tmp"), a); err != nil {
		t.Fatal(err)
	}
	sync()
	v = en.vault()
	if v["Docs/word.docx"] != "v2" || v["Docs/a.txt"] != "office save" || len(v) != 3 {
		t.Fatal(v)
	}

	// Turned off: downloaded files stay as normal files, the rest go
	// (they are in orOS), and Windows forgets the folder.
	srv.Unmount()
	unmounted = true
	if err := Unregister(dir, ""); err != nil {
		t.Fatal(err)
	}
	if ok, _ := Registered(dir); ok {
		t.Fatal("still registered")
	}
	if b, err := os.ReadFile(a); err != nil || string(b) != "office save" {
		t.Fatalf("%q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "Moved", "b.txt")); !os.IsNotExist(err) {
		t.Fatal("a file never downloaded was left behind", err)
	}
}
