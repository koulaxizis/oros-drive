package vdisk

import (
	"errors"
	"io/fs"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/koulaxizis/oros-drive/vault"
)

func TestPinsDownloadAndShow(t *testing.T) {
	e := newEnv(t)
	e.browser(map[string]string{"/internal/Docs/a.txt": "alpha", "/internal/Docs/sub/b.txt": "beta", "/internal/c.txt": "gamma"})
	d := e.disk("phone")
	if err := d.SetPin("Docs", true); err != nil {
		t.Fatal(err)
	}
	if err := d.SetPin("/internal/none", true); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("pinned a missing path:", err)
	}
	st, err := d.SyncPins()
	if err != nil || st.Files != 2 || st.Got != 2 || st.Failed != 0 || st.Bytes != 9 {
		t.Fatalf("%+v %v", st, err)
	}
	for p, want := range map[string]bool{"Docs/a.txt": true, "Docs/sub/b.txt": true, "c.txt": false} {
		i, _ := d.Stat(p)
		if i.Cached != want || i.Pinned != want {
			t.Errorf("%s: %+v", p, i)
		}
	}
	l, _ := d.List("/internal")
	for _, i := range l {
		if i.Pinned != (i.Name == "Docs") {
			t.Errorf("list %+v", i)
		}
	}
	// Nothing left to get; the list survives a restart.
	if st, _ := d.SyncPins(); st.Got != 0 || st.Files != 2 {
		t.Fatalf("second pass %+v", st)
	}
	if got := e.disk("phone").Pins(); !reflect.DeepEqual(got, []string{"/internal/Docs"}) {
		t.Fatal(got)
	}
	// Unpinning a path that is gone works and is idempotent.
	if err := d.SetPin("Docs", false); err != nil || len(d.Pins()) != 0 {
		t.Fatal(err)
	}
	if err := d.SetPin("/internal/none", false); err != nil {
		t.Fatal(err)
	}
	if i, _ := d.Stat("Docs/a.txt"); i.Pinned || !i.Cached {
		t.Fatalf("after unpin %+v", i)
	}
}

func TestPinnedFilesFollowChangesElsewhere(t *testing.T) {
	e := newEnv(t)
	e.browser(map[string]string{"/internal/n.txt": "one"})
	d := e.disk("phone")
	if err := d.SetPin("n.txt", true); err != nil {
		t.Fatal(err)
	}
	d.SyncPins()
	e.browser(map[string]string{"/internal/n.txt": "two, longer"})
	if st, _ := d.SyncPins(); st.Got != 1 {
		t.Fatalf("%+v", st)
	}
	local, _, err := d.Fetch("n.txt")
	if b, _ := os.ReadFile(local); err != nil || string(b) != "two, longer" {
		t.Fatal(string(b), err)
	}
}

func TestPinnedFilesStayInTheCache(t *testing.T) {
	e := newEnv(t)
	files := map[string]string{}
	for _, n := range []string{"1", "2", "3", "4"} {
		files["/internal/"+n] = strings.Repeat(n, 100)
	}
	files["/internal/Keep/k"] = strings.Repeat("k", 300)
	e.browser(files)
	d := e.disk("phone")
	d.MaxCache = 250
	if err := d.SetPin("Keep", true); err != nil {
		t.Fatal(err)
	}
	if _, err := d.SyncPins(); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"1", "2", "3", "4"} {
		if _, _, err := d.Fetch(n); err != nil {
			t.Fatal(err)
		}
	}
	// The pinned 300 bytes neither count nor go.
	if i, _ := d.Stat("Keep/k"); !i.Cached {
		t.Fatal("a pinned file was pruned")
	}
	if s := d.CacheSize(); s > 250+300 {
		t.Fatalf("cache %d", s)
	}
	if err := d.ClearCache(); err != nil {
		t.Fatal(err)
	}
	if i, _ := d.Stat("Keep/k"); !i.Cached {
		t.Fatal("clearing the cache removed a pinned file")
	}
	if d.CacheSize() != 300 {
		t.Fatal("clearing the cache kept other files", d.CacheSize())
	}
	// A fresh start with pins and no manifest read yet prunes nothing.
	d2 := e.disk("phone")
	d2.MaxCache = 1
	d2.prune("")
	if d2.CacheSize() != 300 {
		t.Fatal("pruned without knowing what the pins hold")
	}
}

func TestOneBadPinnedFileDoesNotStopTheOthers(t *testing.T) {
	e := newEnv(t)
	e.browser(map[string]string{"/internal/P/a.txt": "real", "/internal/P/b.txt": "other"})
	m := e.manifest()
	other, _, _ := e.store.Get(keys.ObjectKey(m["/internal/P/b.txt"].H))
	if _, err := e.store.Put(keys.ObjectKey(m["/internal/P/a.txt"].H), other, vault.Overwrite); err != nil {
		t.Fatal(err)
	}
	d := e.disk("phone")
	d.SetPin("P", true)
	st, err := d.SyncPins()
	if err != nil || st.Got != 1 || st.Failed != 1 || st.Error == "" {
		t.Fatalf("%+v %v", st, err)
	}
	if i, _ := d.Stat("P/a.txt"); i.Cached {
		t.Fatal("a refused object was kept offline")
	}
}
