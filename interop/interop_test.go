// Package interop proves that the Go vault code and the real orOS
// browser code (sync.js + vault.js) read and write the same vault.
//
// It needs Node and a koulaxizis/oros checkout:
//
//	OROS_SRC=/path/to/oros go test ./interop/
//
// Without OROS_SRC the tests are skipped (CI always sets it).
package interop

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/koulaxizis/oros-drive/vault"
)

const pass = "interop passphrase · Ελληνικά"

type env struct {
	t                       *testing.T
	src, store, disk, state string
}

func setup(t *testing.T) *env {
	t.Helper()
	src := os.Getenv("OROS_SRC")
	if src == "" {
		t.Skip("OROS_SRC not set (path to a koulaxizis/oros checkout)")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	d := t.TempDir()
	e := &env{t: t, src: src, store: filepath.Join(d, "store"), disk: filepath.Join(d, "disk"), state: filepath.Join(d, "state.json")}
	os.MkdirAll(e.store, 0o755)
	os.MkdirAll(e.disk, 0o755)
	return e
}

type stats struct {
	Downloaded, Uploaded, Deleted, Kept, Failed, Conflicts int
}

func (e *env) js(args ...string) stats {
	e.t.Helper()
	cmd := exec.Command("node", append([]string{"vaultjs.cjs", args[0], e.src, e.store, e.disk}, args[1:]...)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		e.t.Fatalf("vault.js %s: %v\n%s", args[0], err, errb.String())
	}
	var s stats
	if args[0] == "sync" {
		if err := json.Unmarshal(out.Bytes(), &s); err != nil {
			e.t.Fatalf("vault.js output %q: %v", out.String(), err)
		}
	}
	return s
}

func (e *env) jsSync(touch bool) stats {
	args := []string{"sync", e.state, pass}
	if touch {
		args = append(args, "--touch")
	}
	return e.js(args...)
}

// sample is a disk with Greek names, a binary file, an empty file,
// nesting and characters JSON and HTML care about.
func sample() map[string][]byte {
	bin := make([]byte, 300_000)
	rand.Read(bin)
	return map[string][]byte{
		"a.txt": []byte("hello"),
		"Docs/Έγγραφα/σημείωση.md": []byte("# γεια σου\n<b>&</b> \"quotes\"\n"),
		"Docs/bin.dat":             bin,
		"empty.txt":                {},
		"Photos/2026/IMG 0001.jpg": append([]byte{0xFF, 0xD8, 0xFF}, bin[:5000]...),
	}
}

func writeDisk(t *testing.T, dir string, files map[string][]byte) {
	for rel, b := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func readDisk(t *testing.T, dir string) map[string][]byte {
	out := map[string][]byte{}
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		b, _ := os.ReadFile(p)
		out[filepath.ToSlash(rel)] = b
		return nil
	})
	return out
}

func sameFiles(t *testing.T, got, want map[string][]byte) {
	t.Helper()
	var gk, wk []string
	for k := range got {
		gk = append(gk, k)
	}
	for k := range want {
		wk = append(wk, k)
	}
	sort.Strings(gk)
	sort.Strings(wk)
	if strings.Join(gk, "|") != strings.Join(wk, "|") {
		t.Fatalf("files differ:\n got  %v\n want %v", gk, wk)
	}
	for k, w := range want {
		if !bytes.Equal(got[k], w) {
			t.Fatalf("%s: content differs (%d vs %d bytes)", k, len(got[k]), len(w))
		}
	}
}

func goVault(e *env) *vault.Vault {
	return vault.New(vault.NewDirStore(e.store), vault.NewPassphrase(pass))
}

// goRead returns every file of the vault through the Go code, keyed
// like the disk folder.
func goRead(t *testing.T, v *vault.Vault) map[string][]byte {
	t.Helper()
	r, err := v.FetchManifest()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for p, e := range r.Files {
		b, err := v.Download(e)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		out[vault.UserPath(p)] = b
	}
	return out
}

func TestBrowserWritesGoReads(t *testing.T) {
	e := setup(t)
	files := sample()
	writeDisk(t, e.disk, files)
	if s := e.jsSync(false); s.Uploaded != len(files) || s.Failed != 0 {
		t.Fatalf("vault.js push: %+v", s)
	}
	sameFiles(t, goRead(t, goVault(e)), files)
}

func TestGoWritesBrowserReads(t *testing.T) {
	e := setup(t)
	files := sample()
	put := map[string]vault.File{}
	for rel, b := range files {
		put[vault.DiskPath(rel)] = vault.File{Data: b}
	}
	if err := goVault(e).PutFiles(put, nil); err != nil {
		t.Fatal(err)
	}
	if s := e.jsSync(false); s.Downloaded != len(files) || s.Failed != 0 {
		t.Fatalf("vault.js pull: %+v", s)
	}
	sameFiles(t, readDisk(t, e.disk), files)
}

// Both write the same vault in turn, each on top of the other's work:
// nothing is lost, nobody's change comes back as a conflict.
func TestTakingTurns(t *testing.T) {
	e := setup(t)
	files := sample()
	writeDisk(t, e.disk, files)
	e.jsSync(false)

	// Go adds a file, replaces one and deletes one in the browser's vault.
	v := goVault(e)
	err := v.PutFiles(map[string]vault.File{
		"/internal/from-go.txt": {Data: []byte("written by orOS Drive"), Mime: "text/plain"},
		"/internal/a.txt":       {Data: []byte("hello, changed by Go")},
	}, []string{"/internal/empty.txt"})
	if err != nil {
		t.Fatal(err)
	}
	s := e.jsSync(false)
	if s.Failed != 0 || s.Conflicts != 0 || s.Downloaded != 2 || s.Deleted != 1 {
		t.Fatalf("vault.js after Go's change: %+v", s)
	}
	files["from-go.txt"] = []byte("written by orOS Drive")
	files["a.txt"] = []byte("hello, changed by Go")
	delete(files, "empty.txt")
	sameFiles(t, readDisk(t, e.disk), files)

	// The browser edits a file; Go reads the edit.
	files["Docs/Έγγραφα/σημείωση.md"] = []byte("edited in the browser")
	writeDisk(t, e.disk, map[string][]byte{"Docs/Έγγραφα/σημείωση.md": files["Docs/Έγγραφα/σημείωση.md"]})
	if s := e.jsSync(true); s.Failed != 0 || s.Conflicts != 0 {
		t.Fatalf("vault.js edit: %+v", s)
	}
	sameFiles(t, goRead(t, goVault(e)), files)
}

// A vault written before VD-KEY (passphrase envelopes) is readable.
func TestLegacyVault(t *testing.T) {
	e := setup(t)
	files := sample()
	writeDisk(t, e.disk, files)
	e.js("legacy", pass)
	v := goVault(e)
	r, err := v.FetchManifest()
	if err != nil {
		t.Fatal(err)
	}
	if !r.Legacy {
		t.Fatal("manifest not seen as legacy")
	}
	sameFiles(t, goRead(t, v), files)
}

// A wrong passphrase opens nothing.
func TestWrongPassphrase(t *testing.T) {
	e := setup(t)
	writeDisk(t, e.disk, map[string][]byte{"x.txt": []byte("x")})
	e.jsSync(false)
	v := vault.New(vault.NewDirStore(e.store), vault.NewPassphrase("not it"))
	if _, err := v.FetchManifest(); err != vault.ErrLocked {
		t.Fatalf("got %v, want ErrLocked", err)
	}
}
