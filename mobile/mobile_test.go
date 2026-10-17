package mobile

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/koulaxizis/oros-drive/mirror"
	"github.com/koulaxizis/oros-drive/vault"
)

// fakeFolders plays the Android app: grants are directories here, and
// everything crosses as JSON like it does over gomobile.
type fakeFolders struct{}

func (fakeFolders) dir(grant string) *mirror.OSFolder { return &mirror.OSFolder{Root: grant} }

func wrap(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return errors.New("not-found")
	}
	return err
}

func (f fakeFolders) Stat(g, rel string) (string, error) {
	fi, err := f.dir(g).Stat(rel)
	if err != nil {
		return "", wrap(err)
	}
	return asJSON(fi), nil
}

func (f fakeFolders) List(g, rel string) (string, error) {
	l, err := f.dir(g).List(rel)
	if err != nil {
		return "", wrap(err)
	}
	return asJSON(l), nil
}

func (f fakeFolders) Read(g, rel string, off int64, n int) ([]byte, error) {
	b, err := os.ReadFile(filepath.Join(g, filepath.FromSlash(rel)))
	if err != nil {
		return nil, wrap(err)
	}
	if off >= int64(len(b)) {
		return []byte{}, nil
	}
	b = b[off:]
	if len(b) > n {
		b = b[:n]
	}
	return b, nil
}

func (f fakeFolders) Write(g, rel string, data []byte) (string, error) {
	fi, err := f.dir(g).Write(rel, data)
	if err != nil {
		return "", err
	}
	return asJSON(fi), nil
}

func (f fakeFolders) Remove(g, rel string) error { return wrap(f.dir(g).Remove(rel)) }

type memSecrets map[string]string

func (m memSecrets) Get(n string) (string, error) { return m[n], nil }
func (m memSecrets) Set(n, v string) error        { m[n] = v; return nil }
func (m memSecrets) Delete(n string) error        { delete(m, n); return nil }

func TestPhoneMirrorsAGrantedFolder(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OROS_DRIVE_STORE_DIR", filepath.Join(dir, "cloud"))
	sec := memSecrets{}
	d, err := NewDrive(filepath.Join(dir, "data"), "Pixel 8", fakeFolders{}, sec)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Unlock("phone pass", false); err == nil || err.Error() != "no-vault" {
		t.Fatalf("want no-vault, got %v", err)
	}
	if err := d.Unlock("phone pass", true); err != nil {
		t.Fatal(err)
	}
	if sec["vault-key"] == "" {
		t.Fatal("the vault key did not go to the keystore")
	}
	if err := d.Unlock("wrong", false); err == nil || err.Error() != "locked" {
		t.Fatalf("want locked, got %v", err)
	}
	grant := filepath.Join(dir, "DCIM")
	os.MkdirAll(grant, 0o755)
	big := strings.Repeat("x", 3<<20) // more than one 1 MB chunk
	os.WriteFile(filepath.Join(grant, "big.bin"), []byte(big), 0o644)
	os.WriteFile(filepath.Join(grant, "a.txt"), []byte("hi"), 0o644)
	old := time.Now().Add(-time.Minute)
	os.Chtimes(filepath.Join(grant, "big.bin"), old, old)
	os.Chtimes(filepath.Join(grant, "a.txt"), old, old)
	pj, err := d.AddFolder(grant, "Camera")
	if err != nil {
		t.Fatal(err)
	}
	var p mirror.Pair
	json.Unmarshal([]byte(pj), &p)
	if p.Remote != "/internal/Drive/Pixel 8/Camera" {
		t.Fatal(p.Remote)
	}
	if _, err := d.AddFolder(grant, "Again"); err == nil {
		t.Fatal("the same grant was added twice")
	}
	if _, err := d.Sync(); err != nil {
		t.Fatal(err)
	}
	v := vault.New(d.s.Store, vault.NewPassphrase("phone pass")) // the same store: DirStore keeps revisions in memory
	r, err := v.FetchManifest()
	if err != nil {
		t.Fatal(err)
	}
	b, err := v.Download(r.Files[p.Remote+"/big.bin"])
	if err != nil || string(b) != big {
		t.Fatalf("big file: %v, %d bytes", err, len(b))
	}
	// orOS adds a file; the phone gets it on the next pass.
	if err := v.PutFiles(map[string]vault.File{p.Remote + "/from-oros.txt": {Data: []byte("hello phone")}}, nil); err != nil {
		t.Fatal(err)
	}
	if !d.VaultMoved() {
		t.Fatal("VaultMoved missed the change")
	}
	if _, err := d.Sync(); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(grant, "from-oros.txt")); string(got) != "hello phone" {
		t.Fatal(string(got))
	}
	var st struct {
		Unlocked bool `json:"unlocked"`
		Settings struct {
			Device string `json:"device"`
		} `json:"settings"`
	}
	json.Unmarshal([]byte(d.Status()), &st)
	if !st.Unlocked || st.Settings.Device != "Pixel 8" {
		t.Fatal(d.Status())
	}
}
