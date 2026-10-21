// Package mobile is orOS Drive for the orOS Android app, built with
// `gomobile bind` into an .aar. The app supplies two things Go cannot
// reach on Android: the folders the user granted through the system
// picker (Folders) and the Android Keystore (Secrets). Everything
// else (Dropbox or WebDAV, the vault, the mirror rules) is the same code as the
// desktop app.
//
// gomobile passes only simple types, so lists and records cross as
// JSON text.
package mobile

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"strings"

	"github.com/koulaxizis/oros-drive/app"
	"github.com/koulaxizis/oros-drive/internal/config"
	"github.com/koulaxizis/oros-drive/internal/secret"
	"github.com/koulaxizis/oros-drive/mirror"
	"github.com/koulaxizis/oros-drive/webdav"
)

// Folders is implemented by the Android app over the Storage Access
// Framework. folder is the grant (the tree URI) a folder was added
// with; rel is a path inside it with "/". A missing entry is an error
// whose message is "not-found".
type Folders interface {
	Stat(folder, rel string) (string, error) // JSON FileInfo
	List(folder, rel string) (string, error) // JSON []FileInfo
	Read(folder, rel string, offset int64, n int) ([]byte, error)
	Write(folder, rel string, data []byte) (string, error) // JSON FileInfo
	Remove(folder, rel string) error                       // into the app's trash
}

// Secrets is implemented by the Android app over the Android Keystore.
// Get returns "" for a missing item.
type Secrets interface {
	Get(name string) (string, error)
	Set(name, value string) error
	Delete(name string) error
}

// Drive is the mirror for one Android app.
type Drive struct {
	s *app.Service
}

// NewDrive starts from the app's private folder (settings, per-folder
// state). device names the phone in orOS ("Pixel 8") the first time.
func NewDrive(dataDir, device string, folders Folders, secrets Secrets) (*Drive, error) {
	if err := os.Setenv("OROS_DRIVE_CONFIG", dataDir); err != nil {
		return nil, err
	}
	secret.SetBackend(secrets)
	s, err := app.NewService("android")
	if err != nil {
		return nil, err
	}
	s.Folders = func(p mirror.Pair) mirror.Folder { return &folder{f: folders, grant: p.Local} }
	if st := s.Status(); len(st.Settings.Folders) == 0 && st.Settings.Device == config.DefaultDevice() {
		if d := config.CleanName(device); d != "" {
			_ = s.SetPrefs(app.Prefs{Device: &d})
		}
	}
	return &Drive{s: s}, nil
}

func asJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// Status is the app.Status as JSON.
func (d *Drive) Status() string { return asJSON(d.s.Status()) }

// LoginURL starts the Dropbox sign-in: open the returned page in a
// Custom Tab; Dropbox sends the browser back to localhost:47800, which
// this library answers. Status().login tells how it went.
func (d *Drive) LoginURL() (string, error) { return d.s.StartLogin() }

// ConnectWebDAV checks a WebDAV folder (Nextcloud, ownCloud, a NAS)
// and makes it the cloud. Errors are "dav-…" codes (the app shows one
// sentence for each).
func (d *Drive) ConnectWebDAV(url, user, pass, dir string) error {
	return davCode(d.s.ConnectWebDAV(url, user, pass, dir))
}

// NextcloudLoginURL starts a Nextcloud sign-in: open the returned page
// in a Custom Tab; Status().login tells how it went.
func (d *Drive) NextcloudLoginURL(server, dir string) (string, error) {
	u, err := d.s.StartNextcloud(server, dir)
	return u, davCode(err)
}

func davCode(err error) error {
	if c := webdav.Code(err); c != "" {
		return errors.New(c)
	}
	return err
}

// Logout forgets the sign-in and the vault key.
func (d *Drive) Logout() error { return d.s.Logout() }

// Unlock opens the vault key with the orOS sync passphrase. An error
// "no-vault" means there is none yet (create = true starts one).
func (d *Drive) Unlock(passphrase string, create bool) error {
	err := d.s.Unlock(passphrase, create)
	if errors.Is(err, app.ErrNoVault) {
		return errors.New("no-vault")
	}
	if err != nil && strings.Contains(err.Error(), "vault-key-locked") {
		return errors.New("locked")
	}
	return err
}

// Lock forgets the vault key.
func (d *Drive) Lock() error { return d.s.Lock() }

// AddFolder mirrors a granted folder (its tree URI) as name. Returns
// the folder as JSON.
func (d *Drive) AddFolder(grant, name string) (string, error) {
	p, err := d.s.AddFolder(grant, name)
	if err != nil {
		return "", err
	}
	return asJSON(p), nil
}

func (d *Drive) RemoveFolder(id string) error             { return d.s.RemoveFolder(id) }
func (d *Drive) PauseFolder(id string, paused bool) error { return d.s.PauseFolder(id, paused) }
func (d *Drive) ApproveDeletes(id string)                 { d.s.ApproveDeletes(id) }
func (d *Drive) SetPaused(paused bool) error              { return d.s.SetPaused(paused) }

// SetPrefs takes app.Prefs as JSON.
func (d *Drive) SetPrefs(prefsJSON string) error {
	var p app.Prefs
	if err := json.Unmarshal([]byte(prefsJSON), &p); err != nil {
		return err
	}
	p.Autostart = nil // Android schedules the background work itself
	return d.s.SetPrefs(p)
}

// Sync runs one pass and returns whether files were still being
// written (run again soon).
func (d *Drive) Sync() (bool, error) { return d.s.SyncOnce() }

// VaultMoved: another device changed the vault since the last pass
// (one cheap call; the background worker asks before a full pass).
func (d *Drive) VaultMoved() bool { return d.s.RemoteMoved() }

// folder adapts the app's Folders to mirror.Folder.
type folder struct {
	f     Folders
	grant string
}

func notFound(err error) error {
	if err != nil && strings.Contains(err.Error(), "not-found") {
		return fs.ErrNotExist
	}
	return err
}

func (a *folder) Stat(rel string) (mirror.FileInfo, error) {
	var fi mirror.FileInfo
	s, err := a.f.Stat(a.grant, rel)
	if err != nil {
		return fi, notFound(err)
	}
	return fi, json.Unmarshal([]byte(s), &fi)
}

func (a *folder) List(rel string) ([]mirror.FileInfo, error) {
	s, err := a.f.List(a.grant, rel)
	if err != nil {
		return nil, notFound(err)
	}
	var out []mirror.FileInfo
	return out, json.Unmarshal([]byte(s), &out)
}

func (a *folder) Write(rel string, data []byte) (mirror.FileInfo, error) {
	var fi mirror.FileInfo
	s, err := a.f.Write(a.grant, rel, data)
	if err != nil {
		return fi, err
	}
	return fi, json.Unmarshal([]byte(s), &fi)
}

func (a *folder) Remove(rel string) error { return notFound(a.f.Remove(a.grant, rel)) }

const chunk = 1 << 20

func (a *folder) Open(rel string) (io.ReadCloser, error) {
	if _, err := a.Stat(rel); err != nil {
		return nil, err
	}
	return &reader{a: a, rel: rel}, nil
}

// reader reads a granted file in 1 MB chunks.
type reader struct {
	a   *folder
	rel string
	off int64
	buf []byte
	eof bool
}

func (r *reader) Read(p []byte) (int, error) {
	if len(r.buf) == 0 {
		if r.eof {
			return 0, io.EOF
		}
		b, err := r.a.f.Read(r.a.grant, r.rel, r.off, chunk)
		if err != nil {
			return 0, notFound(err)
		}
		if len(b) == 0 {
			r.eof = true
			return 0, io.EOF
		}
		if len(b) < chunk {
			r.eof = true
		}
		r.off += int64(len(b))
		r.buf = b
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}

func (r *reader) Close() error { return nil }
