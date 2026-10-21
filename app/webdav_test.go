package app

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/koulaxizis/oros-drive/vault"
	"github.com/koulaxizis/oros-drive/webdav"
	"github.com/koulaxizis/oros-drive/webdav/davtest"
)

// The whole app on a WebDAV server instead of Dropbox: connect (the
// server is checked first), unlock, mirror, restart, sign out.
func TestWebDAVCloud(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OROS_DRIVE_CONFIG", filepath.Join(dir, "config"))
	t.Setenv("OROS_DRIVE_NO_KEYRING", "1")
	srv := davtest.New("chris", "app pass", "/remote.php/dav/files/chris")
	defer srv.Close()
	url := srv.URL + "/remote.php/dav/files/chris"

	s, err := NewService("test")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ConnectWebDAV(url, "chris", "wrong", ""); !errors.Is(err, webdav.ErrAuth) || s.Status().SignedIn {
		t.Fatal("wrong password:", err)
	}
	if err := s.ConnectWebDAV("http://cloud.example.com/dav", "chris", "app pass", ""); !errors.Is(err, webdav.ErrURL) {
		t.Fatal("plain http:", err)
	}
	if err := s.ConnectWebDAV(url, "chris", "app pass", ""); err != nil {
		t.Fatal(err)
	}
	st := s.Status()
	if !st.SignedIn || st.Provider != "webdav" || st.Account != "chris@"+srv.Listener.Addr().String()+"/orOS" {
		t.Fatalf("%+v", st)
	}
	if err := s.ConnectWebDAV(url, "chris", "app pass", ""); !errors.Is(err, ErrSignedIn) {
		t.Fatal("second connect:", err)
	}
	if err := s.Unlock(pass, true); err != nil {
		t.Fatal(err)
	}
	docs := filepath.Join(dir, "Docs")
	os.MkdirAll(docs, 0o755)
	os.WriteFile(filepath.Join(docs, "a.txt"), []byte("hello"), 0o644)
	old := time.Now().Add(-time.Minute)
	os.Chtimes(filepath.Join(docs, "a.txt"), old, old)
	p, err := s.AddFolder(docs, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	if srv.File("/remote.php/dav/files/chris/orOS/vault/manifest.json") == nil {
		t.Fatal("nothing on the server", srv.Files(""))
	}

	// A restarted app is on the same server, unlocked.
	s2, err := NewService("test")
	if err != nil {
		t.Fatal(err)
	}
	if st := s2.Status(); st.Provider != "webdav" || !st.Unlocked {
		t.Fatalf("%+v", st)
	}
	conf, _ := webdav.NormConf(url, "chris", "")
	v := vault.New(webdav.New(conf, "app pass"), vault.NewPassphrase(pass))
	r, err := v.FetchManifest()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Files[p.Remote+"/a.txt"]; !ok {
		t.Fatal(r.Paths())
	}

	if err := s2.Logout(); err != nil {
		t.Fatal(err)
	}
	s3, _ := NewService("test")
	if st := s3.Status(); st.SignedIn || st.Unlocked {
		t.Fatalf("still signed in: %+v", st)
	}
	if _, err := os.Stat(filepath.Join(docs, "a.txt")); err != nil {
		t.Fatal("sign-out touched the folder")
	}
}
