package vdisk

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
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
