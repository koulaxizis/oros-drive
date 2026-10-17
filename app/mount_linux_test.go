//go:build linux && !android

package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/koulaxizis/oros-drive/vault"
)

// The setting mounts the disk while unlocked and unmounts it on lock.
func TestMountFollowsTheSettingAndTheLock(t *testing.T) {
	if _, err := os.Stat("/dev/fuse"); err != nil {
		t.Skip("no /dev/fuse")
	}
	s, dir := newService(t)
	if err := s.Unlock(pass, true); err != nil {
		t.Fatal(err)
	}
	v := vault.New(s.Store, vault.NewPassphrase(pass))
	if err := v.PutFiles(map[string]vault.File{"/internal/Docs/hello.txt": {Data: []byte("hello")}}, nil); err != nil {
		t.Fatal(err)
	}
	mnt := filepath.Join(dir, "orOS")
	rel := "relative"
	if err := s.SetPrefs(Prefs{Mount: &rel}); err == nil {
		t.Fatal("a relative path was accepted")
	}
	if err := s.SetPrefs(Prefs{Mount: &mnt}); err != nil {
		t.Fatal(err)
	}
	s.syncMount()
	defer s.unmount()
	st := s.Status().Mount
	if !st.Mounted {
		t.Skipf("cannot mount here: %s", st.Error)
	}
	if b, err := os.ReadFile(filepath.Join(mnt, "Docs/hello.txt")); err != nil || string(b) != "hello" {
		t.Fatalf("%q %v", b, err)
	}
	if err := s.Lock(); err != nil {
		t.Fatal(err)
	}
	s.syncMount()
	if s.Status().Mount.Mounted {
		t.Fatal("still mounted after lock")
	}
	if ents, _ := os.ReadDir(mnt); len(ents) != 0 {
		t.Fatal("the folder still shows the disk")
	}
	// A folder with files in it is never mounted over.
	off := ""
	s.SetPrefs(Prefs{Mount: &off})
	full := filepath.Join(dir, "full")
	os.MkdirAll(full, 0o755)
	os.WriteFile(filepath.Join(full, "mine.txt"), []byte("x"), 0o644)
	if err := s.Unlock(pass, false); err != nil {
		t.Fatal(err)
	}
	s.SetPrefs(Prefs{Mount: &full})
	s.syncMount()
	if m := s.Status().Mount; m.Mounted || m.Error == "" {
		t.Fatalf("%+v", m)
	}
}
