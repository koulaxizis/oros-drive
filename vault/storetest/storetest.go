// Package storetest holds the promises every vault.Store makes, as one
// test each store runs (DirStore, WebDAV), so the vault, the mirror
// and the disk can rely on them whichever cloud the user chose.
package storetest

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"github.com/koulaxizis/oros-drive/vault"
)

// Run checks st, which must start empty.
func Run(t *testing.T, st vault.Store) {
	t.Helper()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	// Missing is never an error.
	if b, rev, err := st.Get("manifest.json"); b != nil || rev != "" || err != nil {
		t.Fatalf("missing get: %q %q %v", b, rev, err)
	}
	if rev, ok, err := st.Rev("manifest.json"); rev != "" || ok || err != nil {
		t.Fatalf("missing rev: %q %v %v", rev, ok, err)
	}
	must(st.Delete("objects/none"))
	if l, err := st.List(""); err != nil || len(l) != 0 {
		t.Fatalf("empty list: %v %v", l, err)
	}

	// Create only: once (key.json, the first manifest).
	r1, err := st.Put("manifest.json", []byte("one"), vault.CreateOnly)
	must(err)
	if _, err := st.Put("manifest.json", []byte("two"), vault.CreateOnly); !errors.Is(err, vault.ErrConflict) {
		t.Fatal("second create:", err)
	}
	b, rev, err := st.Get("manifest.json")
	must(err)
	if string(b) != "one" || rev == "" {
		t.Fatalf("get: %q %q", b, rev)
	}
	if r1 != "" && r1 != rev {
		t.Fatalf("put rev %q, get rev %q", r1, rev)
	}
	if r, ok, err := st.Rev("manifest.json"); err != nil || !ok || r != rev {
		t.Fatalf("rev %q %v %v, want %q", r, ok, err, rev)
	}

	// Update: only the revision read (SY-D4); anything else is refused.
	r2, err := st.Put("manifest.json", []byte("two"), vault.UpdateRev(rev))
	must(err)
	if _, err := st.Put("manifest.json", []byte("stale"), vault.UpdateRev(rev)); !errors.Is(err, vault.ErrConflict) {
		t.Fatal("stale update:", err)
	}
	if _, err := st.Put("manifest.json", []byte("blind"), vault.UpdateRev("")); !errors.Is(err, vault.ErrConflict) {
		t.Fatal("update without a revision:", err)
	}
	if _, err := st.Put("missing.json", []byte("x"), vault.UpdateRev(rev)); !errors.Is(err, vault.ErrConflict) {
		t.Fatal("update of a missing key:", err)
	}
	b, rev2, err := st.Get("manifest.json")
	must(err)
	if string(b) != "two" || rev2 == rev {
		t.Fatalf("after update: %q %q (old %q)", b, rev2, rev)
	}
	if r2 != "" && r2 != rev2 {
		t.Fatalf("update rev %q, get rev %q", r2, rev2)
	}

	// Overwrite (immutable objects), in folders that do not exist yet.
	data := bytes.Repeat([]byte{0, 1, 2, 0xff}, 5000)
	_, err = st.Put("objects/ab12", data, vault.Overwrite)
	must(err)
	_, err = st.Put("objects/ab12", data, vault.Overwrite)
	must(err)
	_, err = st.Put("objects/cd34", []byte{}, vault.Overwrite)
	must(err)
	if b, _, err := st.Get("objects/ab12"); err != nil || !bytes.Equal(b, data) {
		t.Fatalf("object: %d bytes %v", len(b), err)
	}
	if b, rev, err := st.Get("objects/cd34"); err != nil || b == nil || len(b) != 0 || rev == "" {
		t.Fatalf("empty object: %v %q %v", b, rev, err)
	}

	l, err := st.List("objects/")
	must(err)
	if want := []string{"objects/ab12", "objects/cd34"}; !reflect.DeepEqual(l, want) {
		t.Fatalf("list objects: %v", l)
	}
	l, err = st.List("")
	must(err)
	if want := []string{"manifest.json", "objects/ab12", "objects/cd34"}; !reflect.DeepEqual(l, want) {
		t.Fatalf("list all: %v", l)
	}
	if inv, ok := st.(vault.Inventory); ok {
		infos, err := inv.ListInfo("objects/")
		must(err)
		if len(infos) != 2 || infos[0].Size != int64(len(data)) || infos[1].Size != 0 || infos[0].Modified.IsZero() {
			t.Fatalf("list info: %+v", infos)
		}
	}

	must(st.Delete("objects/ab12"))
	must(st.Delete("objects/ab12"))
	if b, _, err := st.Get("objects/ab12"); b != nil || err != nil {
		t.Fatal("deleted object still there", err)
	}
	if _, ok, _ := st.Rev("objects/ab12"); ok {
		t.Fatal("deleted object has a revision")
	}

	// Keys never leave the vault.
	for _, k := range []string{"", "../x", "objects/../../x", `a\b`} {
		if _, err := st.Put(k, []byte("x"), vault.Overwrite); err == nil {
			t.Errorf("bad key %q written", k)
		}
	}
}
