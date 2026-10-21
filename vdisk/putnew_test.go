package vdisk

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/koulaxizis/oros-drive/vault"
)

func TestPutNewNeverReplaces(t *testing.T) {
	e := newEnv(t)
	e.browser(map[string]string{"/internal/In/photo.jpg": "theirs"})
	d := e.disk("phone")
	local := filepath.Join(t.TempDir(), "x")
	put := func(content string) Info {
		t.Helper()
		os.WriteFile(local, []byte(content), 0o600)
		i, err := d.PutNew("/internal/In", "photo.jpg", local)
		if err != nil {
			t.Fatal(err)
		}
		return i
	}
	if i := put("mine"); i.Path != "/internal/In/photo (2).jpg" {
		t.Fatal(i.Path)
	}
	if e.content("/internal/In/photo.jpg") != "theirs" || e.content("/internal/In/photo (2).jpg") != "mine" {
		t.Fatal("replaced")
	}
	// A retried upload of the same content writes nothing new.
	before := len(e.manifest())
	if i := put("mine"); i.Path != "/internal/In/photo (2).jpg" || len(e.manifest()) != before {
		t.Fatal(i.Path, len(e.manifest()))
	}
	if i := put("third"); i.Path != "/internal/In/photo (3).jpg" {
		t.Fatal(i.Path)
	}
	// A new folder is made on the way; same content under the first
	// name counts as already there.
	os.WriteFile(local, []byte("theirs"), 0o600)
	if i, err := d.PutNew("/internal/In", "photo.jpg", local); err != nil || i.Path != "/internal/In/photo.jpg" {
		t.Fatal(i.Path, err)
	}
	if i, err := d.PutNew("/internal/New/Sub", "a.txt", local); err != nil || i.Path != "/internal/New/Sub/a.txt" {
		t.Fatal(i, err)
	}
	for _, bad := range []string{"", "..", "a/b", "x.tmp~", ".~lock.a#"} {
		if _, err := d.PutNew("/internal/In", bad, local); !errors.Is(err, ErrBadName) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	// A folder by that name is skipped, a file as the target folder refused.
	if i, err := d.PutNew("/internal", "In", local); err != nil || i.Path != "/internal/In (2)" {
		t.Fatal(i.Path, err)
	}
	if _, err := d.PutNew("/internal/In/photo.jpg", "a", local); !errors.Is(err, ErrNotDir) {
		t.Fatal(err)
	}
}

// countPuts counts manifest writes.
type countPuts struct {
	vault.Store
	n int
}

func (c *countPuts) Put(key string, data []byte, mode vault.PutMode) (string, error) {
	if key == vault.ManifestKey {
		c.n++
	}
	return c.Store.Put(key, data, mode)
}

// Camera backup sends many photos with one manifest write; each one
// keeps the PutNew rules and one that cannot go does not stop the rest.
func TestPutNewManyOneWrite(t *testing.T) {
	e := newEnv(t)
	e.browser(map[string]string{"/internal/Cam/a.jpg": "theirs", "/internal/Cam/b.jpg": "b"})
	cs := &countPuts{Store: e.store}
	d := &Disk{Vault: vault.New(cs, nil).WithKeys(keys), CacheDir: filepath.Join(e.root, "phone"), TTL: time.Nanosecond}
	dir := t.TempDir()
	file := func(name, content string) string {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte(content), 0o600)
		return p
	}
	items := []NewFile{
		{Dir: "/internal/Cam", Name: "a.jpg", Local: file("1", "mine")},    // taken by other content
		{Dir: "/internal/Cam", Name: "b.jpg", Local: file("2", "b")},       // already there
		{Dir: "/internal/Cam", Name: "x.tmp~", Local: file("3", "x")},      // a name orOS cannot hold
		{Dir: "/internal/Cam/Shots", Name: "c.png", Local: file("4", "c")}, // a new folder on the way
		{Dir: "/internal/Cam", Name: "gone.jpg", Local: filepath.Join(dir, "missing")},
		{Dir: "/internal/Cam/a.jpg", Name: "d.jpg", Local: file("5", "d")}, // a file as the folder
	}
	res, err := d.PutNewMany(items)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/internal/Cam/a (2).jpg", "/internal/Cam/b.jpg", "", "/internal/Cam/Shots/c.png", "", ""}
	for i, w := range want {
		if res[i].Info.Path != w {
			t.Errorf("%d: %q, want %q (%v)", i, res[i].Info.Path, w, res[i].Err)
		}
	}
	if !errors.Is(res[2].Err, ErrBadName) || res[4].Err == nil || !errors.Is(res[5].Err, ErrNotDir) {
		t.Fatal(res[2].Err, res[4].Err, res[5].Err)
	}
	if cs.n != 1 {
		t.Fatalf("%d manifest writes", cs.n)
	}
	if e.content("/internal/Cam/a.jpg") != "theirs" || e.content("/internal/Cam/a (2).jpg") != "mine" || e.content("/internal/Cam/Shots/c.png") != "c" {
		t.Fatal("wrong content")
	}
	// The same batch again writes nothing.
	cs.n = 0
	if res, err = d.PutNewMany(items[:2]); err != nil || cs.n != 0 || res[0].Info.Path != "/internal/Cam/a (2).jpg" {
		t.Fatal(err, cs.n, res[0].Info.Path)
	}
}
