package mobile

import (
	"errors"
	"io/fs"

	"github.com/koulaxizis/oros-drive/dropbox"
	"github.com/koulaxizis/oros-drive/vault"
	"github.com/koulaxizis/oros-drive/vdisk"
)

// The whole orOS disk for Android's DocumentsProvider (wave 5): the
// "orOS" entry in the Files app and in every file picker. Paths are
// disk paths ("/internal/Docs/a.txt"; "/internal" is the root). Errors
// the provider tells apart carry a fixed message: "not-found",
// "exists", "not-empty", "too-big", "bad-name", "locked" (signed out
// or no vault key).

func diskErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, fs.ErrNotExist):
		return errors.New("not-found")
	case errors.Is(err, fs.ErrExist):
		return errors.New("exists")
	case errors.Is(err, vdisk.ErrNotEmpty):
		return errors.New("not-empty")
	case errors.Is(err, vdisk.ErrTooBig):
		return errors.New("too-big")
	case errors.Is(err, vdisk.ErrBadName), errors.Is(err, vdisk.ErrIsDir), errors.Is(err, vdisk.ErrNotDir):
		return errors.New("bad-name")
	case errors.Is(err, vault.ErrLocked), errors.Is(err, vault.ErrNoKey), errors.Is(err, dropbox.ErrNotConnected):
		return errors.New("locked")
	}
	return err
}

func (d *Drive) disk() (*vdisk.Disk, error) {
	k, err := d.s.Disk()
	return k, diskErr(err)
}

// DiskRefresh reads the manifest again (a pull-to-refresh).
func (d *Drive) DiskRefresh() error {
	k, err := d.disk()
	if err != nil {
		return err
	}
	return diskErr(k.Refresh(true))
}

// DiskStat returns one entry as JSON (vdisk.Info).
func (d *Drive) DiskStat(path string) (string, error) {
	k, err := d.disk()
	if err != nil {
		return "", err
	}
	i, err := k.Stat(path)
	if err != nil {
		return "", diskErr(err)
	}
	return asJSON(i), nil
}

// DiskList returns a folder's entries as JSON ([]vdisk.Info).
func (d *Drive) DiskList(path string) (string, error) {
	k, err := d.disk()
	if err != nil {
		return "", err
	}
	l, err := k.List(path)
	if err != nil {
		return "", diskErr(err)
	}
	return asJSON(l), nil
}

// DiskFetch downloads a file (once; it is cached) and returns the path
// of a read-only local copy.
func (d *Drive) DiskFetch(path string) (string, error) {
	k, err := d.disk()
	if err != nil {
		return "", err
	}
	local, _, err := k.Fetch(path)
	return local, diskErr(err)
}

// DiskTempPath gives a scratch file for a write in progress; the
// provider writes there and hands it to DiskPut.
func (d *Drive) DiskTempPath() (string, error) {
	k, err := d.disk()
	if err != nil {
		return "", err
	}
	f, err := k.TempFile()
	if err != nil {
		return "", err
	}
	name := f.Name()
	return name, f.Close()
}

// DiskPut uploads the content of local to path. base is the hash the
// write started from ("" for a new file); a file changed elsewhere in
// the meantime is kept beside it as a conflict copy. Returns JSON.
func (d *Drive) DiskPut(path, local, base string) (string, error) {
	k, err := d.disk()
	if err != nil {
		return "", err
	}
	i, err := k.Put(path, local, base)
	if err != nil {
		return "", diskErr(err)
	}
	return asJSON(i), nil
}

func (d *Drive) DiskMkdir(path string) error {
	k, err := d.disk()
	if err != nil {
		return err
	}
	return diskErr(k.Mkdir(path))
}

// DiskRemove deletes a file or a folder with everything in it.
func (d *Drive) DiskRemove(path string) error {
	k, err := d.disk()
	if err != nil {
		return err
	}
	return diskErr(k.Remove(path, true))
}

func (d *Drive) DiskRename(from, to string) error {
	k, err := d.disk()
	if err != nil {
		return err
	}
	return diskErr(k.Rename(from, to))
}

// DiskCacheSize and DiskClearCache manage the downloaded copies.
func (d *Drive) DiskCacheSize() int64 {
	k, err := d.disk()
	if err != nil {
		return 0
	}
	return k.CacheSize()
}

func (d *Drive) DiskClearCache() error {
	k, err := d.disk()
	if err != nil {
		return err
	}
	return k.ClearCache()
}

// DiskPin keeps a file or folder on this phone (on) or lets it go back
// to the ordinary cache (off). The files arrive with DiskSyncPins.
func (d *Drive) DiskPin(path string, on bool) error {
	k, err := d.disk()
	if err != nil {
		return err
	}
	return diskErr(k.SetPin(path, on))
}

// DiskPins returns the offline paths as a JSON array.
func (d *Drive) DiskPins() (string, error) {
	k, err := d.disk()
	if err != nil {
		return "", err
	}
	return asJSON(k.Pins()), nil
}

// DiskSyncPins downloads every offline file not on the phone yet (the
// worker runs it after each pass) and returns a vdisk.PinStatus as JSON.
func (d *Drive) DiskSyncPins() (string, error) {
	k, err := d.disk()
	if err != nil {
		return "", err
	}
	st, err := k.SyncPins()
	if err != nil {
		return "", diskErr(err)
	}
	return asJSON(st), nil
}
