//go:build linux

package fusefs

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/koulaxizis/oros-drive/vault"
	"github.com/koulaxizis/oros-drive/vdisk"
)

const pass = "fuse test passphrase"

// A real mount over a vault in a folder; skipped where FUSE is not
// available (no /dev/fuse, no permission, no fusermount3).
func mount(t *testing.T) (dir string, st *vault.DirStore, v *vault.Vault) {
	t.Helper()
	if _, err := os.Stat("/dev/fuse"); err != nil {
		t.Skip("no /dev/fuse")
	}
	root := t.TempDir()
	st = vault.NewDirStore(filepath.Join(root, "cloud"))
	keyDoc, keys, err := vault.NewKeyFile(vault.NewPassphrase(pass))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Put(vault.KeyFileKey, keyDoc, vault.CreateOnly); err != nil {
		t.Fatal(err)
	}
	v = vault.New(st, vault.NewPassphrase(pass)).WithKeys(keys)
	if err := v.PutFiles(map[string]vault.File{
		"/internal/Docs/a.txt":     {Data: []byte("alpha")},
		"/internal/Docs/sub/b.txt": {Data: []byte("beta")},
	}, nil); err != nil {
		t.Fatal(err)
	}
	d := &vdisk.Disk{Vault: vault.New(st, nil).WithKeys(keys), CacheDir: filepath.Join(root, "cache"), TTL: time.Nanosecond}
	dir = filepath.Join(root, "mnt")
	os.MkdirAll(dir, 0o755)
	srv, err := Mount(d, dir)
	if err != nil {
		t.Skipf("cannot mount here: %v", err)
	}
	t.Cleanup(func() { srv.Unmount() })
	return dir, st, v
}

func content(t *testing.T, v *vault.Vault, p string) (string, bool) {
	t.Helper()
	r, err := v.FetchManifest()
	if err != nil {
		t.Fatal(err)
	}
	e, ok := r.Files[p]
	if !ok {
		return "", false
	}
	b, err := v.Download(e)
	if err != nil {
		t.Fatal(err)
	}
	return string(b), true
}

func TestMountedDisk(t *testing.T) {
	dir, _, v := mount(t)

	ents, err := os.ReadDir(filepath.Join(dir, "Docs"))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range ents {
		got = append(got, e.Name())
	}
	sort.Strings(got)
	if strings.Join(got, ",") != "a.txt,sub" {
		t.Fatal(got)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "Docs/a.txt")); err != nil || string(b) != "alpha" {
		t.Fatalf("%q %v", b, err)
	}
	if fi, err := os.Stat(filepath.Join(dir, "Docs/sub/b.txt")); err != nil || fi.Size() != 4 {
		t.Fatal(fi, err)
	}

	// New file: uploaded on close.
	if err := os.WriteFile(filepath.Join(dir, "Docs/new.txt"), []byte("made in Linux"), 0o644); err != nil {
		t.Fatal(err)
	}
	if s, ok := content(t, v, "/internal/Docs/new.txt"); !ok || s != "made in Linux" {
		t.Fatalf("%q %v", s, ok)
	}

	// Append to an existing file.
	f, err := os.OpenFile(filepath.Join(dir, "Docs/a.txt"), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(" plus")
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if s, _ := content(t, v, "/internal/Docs/a.txt"); s != "alpha plus" {
		t.Fatal(s)
	}

	// Truncate in place.
	if err := os.Truncate(filepath.Join(dir, "Docs/a.txt"), 5); err != nil {
		t.Fatal(err)
	}
	if s, _ := content(t, v, "/internal/Docs/a.txt"); s != "alpha" {
		t.Fatal(s)
	}

	// Folders, rename, delete.
	if err := os.Mkdir(filepath.Join(dir, "Empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(dir, "Empty")); err != nil || !fi.IsDir() {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "Docs/sub"), filepath.Join(dir, "Moved")); err != nil {
		t.Fatal(err)
	}
	if s, ok := content(t, v, "/internal/Moved/b.txt"); !ok || s != "beta" {
		t.Fatal(s, ok)
	}
	if err := os.Remove(filepath.Join(dir, "Docs/new.txt")); err != nil {
		t.Fatal(err)
	}
	if _, ok := content(t, v, "/internal/Docs/new.txt"); ok {
		t.Fatal("deleted file still in the vault")
	}
	if err := os.Remove(filepath.Join(dir, "Moved")); err == nil {
		t.Fatal("a non-empty folder was removed")
	}

	// A program that writes a temp file and renames it over the
	// original (editors, office suites).
	tmp := filepath.Join(dir, "Docs/.a.txt.swp-new")
	if err := os.WriteFile(tmp, []byte("saved by an editor"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, "Docs/a.txt")); err != nil {
		t.Fatal(err)
	}
	if s, _ := content(t, v, "/internal/Docs/a.txt"); s != "saved by an editor" {
		t.Fatal(s)
	}

	// An office suite: lock file, temp file renamed over the original.
	lock := filepath.Join(dir, "Docs/.~lock.a.txt#")
	os.WriteFile(lock, []byte("lock"), 0o644)
	if err := os.WriteFile(filepath.Join(dir, "Docs/lu42.tmp"), []byte("office save"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "Docs/lu42.tmp"), filepath.Join(dir, "Docs/a.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if s, _ := content(t, v, "/internal/Docs/a.txt"); s != "office save" {
		t.Fatal(s)
	}
	if r, _ := v.FetchManifest(); len(r.Files) != 2 {
		t.Fatalf("temp files reached the vault: %v", r.Paths())
	}

	// Changes made in orOS show up.
	if err := v.PutFiles(map[string]vault.File{"/internal/Docs/from-orOS.txt": {Data: []byte("hi")}}, nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond) // the kernel's entry cache (1 s)
	if b, err := os.ReadFile(filepath.Join(dir, "Docs/from-orOS.txt")); err != nil || string(b) != "hi" {
		t.Fatalf("%q %v", b, err)
	}

	// A big copy with cp.
	big := bytes.Repeat([]byte("0123456789"), 300000)
	src := filepath.Join(t.TempDir(), "big.bin")
	os.WriteFile(src, big, 0o644)
	if out, err := exec.Command("cp", src, filepath.Join(dir, "big.bin")).CombinedOutput(); err != nil {
		t.Fatal(string(out), err)
	}
	if s, ok := content(t, v, "/internal/big.bin"); !ok || s != string(big) {
		t.Fatal("big file differs")
	}
}
