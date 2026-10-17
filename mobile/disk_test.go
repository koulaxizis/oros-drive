package mobile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/koulaxizis/oros-drive/vault"
	"github.com/koulaxizis/oros-drive/vdisk"
)

// The calls the DocumentsProvider makes, as it makes them.
func TestDiskForTheFilesApp(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OROS_DRIVE_STORE_DIR", filepath.Join(dir, "cloud"))
	d, err := NewDrive(filepath.Join(dir, "data"), "Pixel 8", fakeFolders{}, memSecrets{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.DiskList("/internal"); err == nil || err.Error() != "locked" {
		t.Fatalf("want locked, got %v", err)
	}
	if err := d.Unlock("phone pass", true); err != nil {
		t.Fatal(err)
	}
	v := vault.New(d.s.Store, vault.NewPassphrase("phone pass"))
	if err := v.PutFiles(map[string]vault.File{"/internal/Docs/a.txt": {Data: []byte("alpha")}}, nil); err != nil {
		t.Fatal(err)
	}
	var list []vdisk.Info
	js, err := d.DiskList("/internal")
	if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal([]byte(js), &list)
	if len(list) != 1 || list[0].Name != "Docs" || !list[0].Dir {
		t.Fatal(js)
	}
	local, err := d.DiskFetch("/internal/Docs/a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(local); string(b) != "alpha" {
		t.Fatal(string(b))
	}
	var st vdisk.Info
	js, _ = d.DiskStat("/internal/Docs/a.txt")
	json.Unmarshal([]byte(js), &st)

	// Save over it from an app (write "wt" to a temp, then put).
	tmp, err := d.DiskTempPath()
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(tmp, []byte("edited on the phone"), 0o600)
	if _, err := d.DiskPut("/internal/Docs/a.txt", tmp, st.Hash); err != nil {
		t.Fatal(err)
	}
	if err := d.DiskMkdir("/internal/New"); err != nil {
		t.Fatal(err)
	}
	if err := d.DiskRename("/internal/Docs/a.txt", "/internal/New/b.txt"); err != nil {
		t.Fatal(err)
	}
	r, _ := v.FetchManifest()
	if b, err := v.Download(r.Files["/internal/New/b.txt"]); err != nil || string(b) != "edited on the phone" {
		t.Fatalf("%q %v", b, err)
	}
	if _, err := d.DiskStat("/internal/Docs/a.txt"); err == nil || err.Error() != "not-found" {
		t.Fatal(err)
	}
	if err := d.DiskMkdir("/internal/New"); err == nil || err.Error() != "exists" {
		t.Fatal(err)
	}
	if err := d.DiskRemove("/internal/New"); err != nil {
		t.Fatal(err)
	}
	if r, _ := v.FetchManifest(); len(r.Files) != 0 {
		t.Fatal(r.Paths())
	}
	if d.DiskCacheSize() == 0 {
		t.Fatal("nothing cached")
	}
	if err := d.DiskClearCache(); err != nil || d.DiskCacheSize() != 0 {
		t.Fatal(err)
	}
}
