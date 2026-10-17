//go:build windows && (amd64 || arm64)

package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/koulaxizis/oros-drive/vault"
	"github.com/koulaxizis/oros-drive/vdisk/cloudfiles"
)

// The setting connects the folder while unlocked; a lock disconnects
// it, turning the setting off ends the folder's life as the disk.
func TestExplorerFolderFollowsTheSetting(t *testing.T) {
	if !mountSupported {
		t.Skip("no Cloud Files API")
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
	if err := s.SetPrefs(Prefs{Mount: &mnt}); err != nil {
		t.Fatal(err)
	}
	s.syncMount()
	defer s.unmount()
	if st := s.Status().Mount; !st.Mounted {
		t.Skipf("cannot connect here: %s", st.Error)
	}
	read := func() (string, error) {
		var b []byte
		var err error
		for i := 0; i < 50; i++ { // the first pass runs in the background
			if b, err = os.ReadFile(filepath.Join(mnt, "Docs", "hello.txt")); err == nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		return string(b), err
	}
	if b, err := read(); err != nil || b != "hello" {
		t.Fatalf("%q %v", b, err)
	}
	if err := s.Lock(); err != nil {
		t.Fatal(err)
	}
	s.syncMount()
	if s.Status().Mount.Mounted {
		t.Fatal("still connected after lock")
	}
	if ok, _ := cloudfiles.Registered(mnt); !ok {
		t.Fatal("a lock unregistered the folder")
	}
	if err := s.Unlock(pass, false); err != nil {
		t.Fatal(err)
	}
	s.syncMount()
	if !s.Status().Mount.Mounted {
		t.Fatal("not connected again after unlock")
	}
	off := ""
	s.SetPrefs(Prefs{Mount: &off})
	s.syncMount()
	if ok, _ := cloudfiles.Registered(mnt); ok {
		t.Fatal("still registered after the setting was turned off")
	}
	// What was opened stays as a normal file.
	if b, err := os.ReadFile(filepath.Join(mnt, "Docs", "hello.txt")); err != nil || string(b) != "hello" {
		t.Fatalf("%q %v", b, err)
	}
	// A folder with files in it is refused.
	full := filepath.Join(dir, "full")
	os.MkdirAll(full, 0o755)
	os.WriteFile(filepath.Join(full, "mine.txt"), []byte("x"), 0o644)
	s.SetPrefs(Prefs{Mount: &full})
	s.syncMount()
	if m := s.Status().Mount; m.Mounted || m.Error == "" {
		t.Fatalf("%+v", m)
	}
}
