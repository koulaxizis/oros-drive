// Package app is the orOS Drive desktop app: the mirror running in the
// background, the settings page on localhost and the tray icon. The
// command line and the tray share one Service.
package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/koulaxizis/oros-drive/dropbox"
	"github.com/koulaxizis/oros-drive/internal/config"
	"github.com/koulaxizis/oros-drive/internal/secret"
	"github.com/koulaxizis/oros-drive/mirror"
	"github.com/koulaxizis/oros-drive/vault"
	"github.com/koulaxizis/oros-drive/vdisk"
	"github.com/koulaxizis/oros-drive/webdav"
)

// OrosURL opens orOS in the browser.
const OrosURL = "https://useoros.online/"

// Status is what the settings page and the tray show.
type Status struct {
	SignedIn  bool                  `json:"signedIn"`
	Provider  string                `json:"provider,omitempty"` // "dropbox" or "webdav"
	Account   string                `json:"account,omitempty"`
	Unlocked  bool                  `json:"unlocked"`
	NeedsKey  string                `json:"needsKey,omitempty"` // "locked", "empty" (no vault yet), "reset"
	Syncing   bool                  `json:"syncing"`
	Paused    bool                  `json:"paused"`
	Error     string                `json:"error,omitempty"`
	LastSync  time.Time             `json:"lastSync,omitempty"`
	Last      *mirror.Result        `json:"last,omitempty"`
	Events    []mirror.Event        `json:"events"`
	Settings  config.Settings       `json:"settings"`
	SecretsIn string                `json:"secretsIn"`
	Login     string                `json:"login,omitempty"` // the sign-in in progress: "waiting" or an error
	Version   string                `json:"version"`
	Folders   map[string]FolderInfo `json:"folders"`
	Mount     MountInfo             `json:"mount"`
	Update    *UpdateInfo           `json:"update,omitempty"` // a newer release (UPD-1)
}

// FolderInfo is a folder's last known state.
type FolderInfo struct {
	mirror.FolderStatus
}

// Service owns the settings, the secrets, the engine and the loop.
type Service struct {
	Version string
	// Store overrides the cloud (tests).
	Store vault.Store
	// Folders serves mirrored folders that are not directories (Android:
	// folders granted through the system picker, Pair.Local holding the
	// grant). nil = desktop directories.
	Folders func(mirror.Pair) mirror.Folder

	mu       sync.Mutex
	settings *config.Settings
	client   *dropbox.Client
	dav      *webdav.Client // instead of client: a WebDAV / Nextcloud folder
	account  string
	vkey     *secret.VaultKey
	engine   *mirror.Engine
	last     *mirror.Result
	lastSync time.Time
	lastErr  string
	needsKey string
	syncing  bool
	login    string
	events   []mirror.Event
	approve  map[string]bool
	folders  map[string]mirror.FolderStatus
	lastRev  string
	disk     *vdisk.Disk
	diskKey  *secret.VaultKey
	mnt      mountState
	update   *UpdateInfo

	kick      chan struct{}
	changed   chan struct{} // settings changed: rebuild the watcher
	onChange  func()        // tray refresh
	updateNow chan struct{} // the release check was turned back on
}

// NewService loads settings and secrets.
func NewService(version string) (*Service, error) {
	st, err := config.Load()
	if err != nil {
		return nil, err
	}
	s := &Service{Version: version, settings: st, approve: map[string]bool{},
		folders: map[string]mirror.FolderStatus{},
		kick:    make(chan struct{}, 1), changed: make(chan struct{}, 1), updateNow: make(chan struct{}, 1)}
	if d := os.Getenv("OROS_DRIVE_STORE_DIR"); d != "" {
		// A vault in a local folder instead of Dropbox: tests, and
		// trying the app on a copied vault.
		s.Store, s.account = vault.NewDirStore(d), "local folder "+d
	} else if c, pass, err := secret.LoadWebDAV(); err == nil {
		s.dav, s.account = webdav.New(c, pass), c.Account()
	} else if t, err := secret.LoadTokens(); err == nil {
		s.client = s.newClient(t)
	}
	if k, err := secret.LoadVaultKey(); err == nil {
		s.vkey = k
	}
	return s, nil
}

func (s *Service) newClient(t dropbox.Tokens) *dropbox.Client {
	c := dropbox.New(t)
	c.OnTokens = func(t dropbox.Tokens) { _ = secret.SaveTokens(t) }
	return c
}

func (s *Service) store() vault.Store {
	if s.Store != nil {
		return s.Store
	}
	if s.dav != nil {
		return s.dav
	}
	if s.client == nil {
		return nil
	}
	return s.client
}

func (s *Service) provider() string {
	switch {
	case s.Store != nil:
		return ""
	case s.dav != nil:
		return "webdav"
	case s.client != nil:
		return "dropbox"
	}
	return ""
}

// cloudID names the cloud the mirror state belongs to (mirror.NoteCloud).
func (s *Service) cloudID() string {
	if s.Store == nil && s.dav != nil {
		return s.dav.Conf.CloudID()
	}
	return "dropbox"
}

// OnChange registers a callback for status changes (the tray).
func (s *Service) OnChange(f func()) {
	s.mu.Lock()
	s.onChange = f
	s.mu.Unlock()
}

func (s *Service) notify() {
	s.mu.Lock()
	f := s.onChange
	s.mu.Unlock()
	if f != nil {
		f()
	}
}

// Kick asks for a pass as soon as possible.
func (s *Service) Kick() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

func (s *Service) settingsChanged() {
	select {
	case s.changed <- struct{}{}:
	default:
	}
	s.Kick()
}

// Status returns a snapshot.
func (s *Service) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Status{
		SignedIn: s.store() != nil, Provider: s.provider(), Account: s.account, Unlocked: s.vkey != nil,
		NeedsKey: s.needsKey, Syncing: s.syncing, Paused: s.settings.Paused,
		Error: s.lastErr, LastSync: s.lastSync, Last: s.last,
		Events: append([]mirror.Event{}, s.events...), Settings: *s.settings,
		SecretsIn: secret.Where(), Login: s.login, Version: s.Version,
		Folders: map[string]FolderInfo{},
		Mount:   MountInfo{Supported: mountSupported, Path: s.mnt.path, Error: s.mnt.err, Mounted: s.mnt.srv != nil, Default: defaultMount()},
		Update:  s.update,
	}
	st.Settings.Folders = append([]mirror.Pair{}, s.settings.Folders...)
	for id, f := range s.folders {
		st.Folders[id] = FolderInfo{f}
	}
	return st
}

func (s *Service) addEvent(ev mirror.Event) {
	s.mu.Lock()
	s.events = append(s.events, ev)
	if n := len(s.events); n > 200 {
		s.events = append([]mirror.Event{}, s.events[n-200:]...)
	}
	s.mu.Unlock()
}

// ---------- Sign-in ----------

// StartLogin begins the Dropbox sign-in and returns the page to open.
// The answer comes back to localhost:47800 (the address orOS's Dropbox
// app allows); Status().Login tells how it went.
func (s *Service) StartLogin() (string, error) {
	c := dropbox.New(dropbox.Tokens{})
	p := dropbox.NewPKCE()
	u := c.AuthorizeURL(p, dropbox.LoopbackRedirect)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	ready := make(chan error, 1)
	go func() {
		defer cancel()
		code, err := dropbox.WaitForCodeReady(ctx, p, ready)
		if err == nil {
			var t dropbox.Tokens
			if t, err = c.Exchange(code, p, dropbox.LoopbackRedirect); err == nil {
				err = s.signedIn(c, t)
			}
		}
		s.mu.Lock()
		if err != nil {
			s.login = err.Error()
		} else {
			s.login = ""
		}
		s.mu.Unlock()
		s.notify()
		s.Kick()
	}()
	if err := <-ready; err != nil {
		return "", err
	}
	s.mu.Lock()
	s.login = "waiting"
	s.mu.Unlock()
	return u, nil
}

func (s *Service) signedIn(c *dropbox.Client, t dropbox.Tokens) error {
	if err := secret.SaveTokens(t); err != nil {
		return err
	}
	a, err := c.CurrentAccount()
	_ = secret.DeleteWebDAV()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.client, s.dav = s.newClient(t), nil
	if err == nil {
		s.account = a.Email
	}
	// A key from another Dropbox account is not this vault's key.
	if s.vkey != nil && s.vkey.Account != "" && a != nil && s.vkey.Account != a.Email {
		s.vkey = nil
		_ = secret.DeleteVaultKey()
	}
	s.engine = nil
	return nil
}

// ErrSignedIn: sign out before connecting another cloud.
var ErrSignedIn = errors.New("dav-connected")

// ConnectWebDAV checks a WebDAV server (webdav.Probe) and, only when it
// keeps every promise the vault needs, makes it this computer's cloud.
// Nothing is saved on failure; the error is a webdav.Err… code.
func (s *Service) ConnectWebDAV(rawURL, user, pass, dir string) error {
	s.mu.Lock()
	in := s.store() != nil
	s.mu.Unlock()
	if in {
		return ErrSignedIn
	}
	conf, err := webdav.NormConf(rawURL, user, dir)
	if err != nil {
		return err
	}
	c, err := webdav.Connect(conf, pass)
	if err != nil {
		return err
	}
	if err := s.davSignedIn(c); err != nil {
		return err
	}
	s.notify()
	s.Kick()
	return nil
}

// StartNextcloud begins a Nextcloud sign-in (Login Flow v2) and
// returns the server's page to open; Status().Login tells how it went.
func (s *Service) StartNextcloud(rawURL, dir string) (string, error) {
	s.mu.Lock()
	in := s.store() != nil
	s.mu.Unlock()
	if in {
		return "", ErrSignedIn
	}
	ctx, cancel := context.WithTimeout(context.Background(), 21*time.Minute)
	l, err := webdav.StartNextcloud(ctx, rawURL, dir)
	if err != nil {
		cancel()
		return "", err
	}
	s.mu.Lock()
	s.login = "waiting"
	s.mu.Unlock()
	go func() {
		defer cancel()
		c, err := l.Wait(ctx)
		if err == nil {
			err = s.davSignedIn(c)
		}
		s.mu.Lock()
		if code := webdav.Code(err); code != "" {
			s.login = code
		} else if err != nil {
			s.login = err.Error()
		} else {
			s.login = ""
		}
		s.mu.Unlock()
		s.notify()
		s.Kick()
	}()
	return l.URL, nil
}

func (s *Service) davSignedIn(c *webdav.Client) error {
	if err := secret.SaveWebDAV(c.Conf, c.Pass); err != nil {
		return err
	}
	_ = secret.DeleteTokens()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dav, s.client, s.account = c, nil, c.Conf.Account()
	// A key from another cloud is not this vault's key.
	if s.vkey != nil && s.vkey.Account != s.account {
		s.vkey = nil
		_ = secret.DeleteVaultKey()
	}
	s.engine, s.needsKey, s.lastErr = nil, "", ""
	return nil
}

// Logout forgets the sign-in and the vault key. Local files stay.
func (s *Service) Logout() error {
	s.mu.Lock()
	c, d := s.client, s.dav
	s.client, s.dav, s.account, s.vkey, s.engine, s.needsKey = nil, nil, "", nil, nil, ""
	s.mu.Unlock()
	if c != nil {
		_ = c.Revoke()
	}
	if d != nil {
		_ = d.Revoke()
	}
	_ = secret.DeleteVaultKey()
	_ = secret.DeleteWebDAV()
	return secret.DeleteTokens()
}

// ---------- Vault key ----------

var ErrNoVault = errors.New("there is no orOS vault in this cloud yet")

// Unlock opens the vault key with the orOS sync passphrase and keeps
// the key (not the passphrase) in the keychain. create = the user
// confirmed starting a new vault because there is none yet.
func (s *Service) Unlock(pass string, create bool) error {
	s.mu.Lock()
	st := s.store()
	account := s.account
	s.mu.Unlock()
	if st == nil {
		return dropbox.ErrNotConnected
	}
	if pass == "" {
		return errors.New("empty passphrase")
	}
	pp := vault.NewPassphrase(pass)
	doc, _, err := st.Get(vault.KeyFileKey)
	if err != nil {
		return err
	}
	var k *vault.Keys
	if doc == nil {
		if !create {
			return ErrNoVault
		}
		v := vault.New(st, pp)
		if k, err = v.LoadKeys(true); err != nil {
			return err
		}
		if doc, _, err = st.Get(vault.KeyFileKey); err != nil {
			return err
		}
	} else if k, err = vault.OpenKeyFile(doc, pp); err != nil {
		return err
	}
	vk := &secret.VaultKey{Key: k.Raw(), KeyFile: vault.SHA256Hex(doc), Account: account}
	if err := secret.SaveVaultKey(vk); err != nil {
		return err
	}
	s.mu.Lock()
	s.vkey, s.engine, s.needsKey, s.lastErr = vk, nil, "", ""
	s.mu.Unlock()
	s.Kick()
	return nil
}

// Lock forgets the vault key: the mirror stops until the next unlock.
func (s *Service) Lock() error {
	s.mu.Lock()
	s.vkey, s.engine = nil, nil
	s.mu.Unlock()
	return secret.DeleteVaultKey()
}

// keyFileMatches: the key file in the cloud is the one the key came
// from. Checked before the first manifest of an empty vault is written.
func (s *Service) keyFileMatches(st vault.Store, vk *secret.VaultKey) error {
	doc, _, err := st.Get(vault.KeyFileKey)
	if err != nil {
		return err
	}
	if doc == nil || vault.SHA256Hex(doc) != vk.KeyFile {
		s.mu.Lock()
		s.needsKey = "reset"
		s.mu.Unlock()
		return vault.ErrLocked
	}
	return nil
}

// ---------- Folders ----------

func randomID() string {
	var b [8]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func inside(a, b string) bool {
	rel, err := filepath.Rel(b, a)
	return err == nil && (rel == "." || filepath.IsLocal(rel))
}

// AddFolder starts mirroring a folder of this computer into
// /Drive/<device>/<name>. The first pass only adds: nothing on either
// side is deleted because of it.
func (s *Service) AddFolder(local, name string) (mirror.Pair, error) {
	if s.Folders != nil {
		return s.addGranted(local, name)
	}
	if !filepath.IsAbs(local) {
		return mirror.Pair{}, errors.New("give the full path of the folder")
	}
	local = filepath.Clean(local)
	info, err := os.Stat(local)
	if err != nil || !info.IsDir() {
		return mirror.Pair{}, fmt.Errorf("%s is not a folder", local)
	}
	if cfg, err := config.Dir(); err == nil && (inside(cfg, local) || inside(local, cfg)) {
		return mirror.Pair{}, errors.New("this folder holds orOS Drive's own settings")
	}
	if home, err := os.UserHomeDir(); err == nil && filepath.Clean(home) == local {
		return mirror.Pair{}, errors.New("pick folders inside your home folder, not the whole home folder")
	}
	if filepath.Dir(local) == local {
		return mirror.Pair{}, errors.New("a whole disk cannot be mirrored; pick a folder")
	}
	if name == "" {
		name = filepath.Base(local)
	}
	name = config.CleanName(name)
	if name == "" || !mirror.LocalNameOK(name, "windows") {
		return mirror.Pair{}, errors.New("choose another name")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	remote := path.Join(mirror.DriveRoot, s.settings.Device, name)
	for _, p := range s.settings.Folders {
		if inside(local, p.Local) || inside(p.Local, local) {
			return mirror.Pair{}, fmt.Errorf("%s overlaps the mirrored folder %s", local, p.Local)
		}
		if strings.EqualFold(p.Remote, remote) {
			return mirror.Pair{}, fmt.Errorf("the name %q is already used; choose another", name)
		}
	}
	p := mirror.Pair{ID: randomID(), Local: local, Remote: remote}
	s.settings.Folders = append(s.settings.Folders, p)
	if err := s.settings.Save(); err != nil {
		s.settings.Folders = s.settings.Folders[:len(s.settings.Folders)-1]
		return mirror.Pair{}, err
	}
	defer s.settingsChanged()
	return p, nil
}

// addGranted adds a folder the host app holds a grant for (Android).
func (s *Service) addGranted(local, name string) (mirror.Pair, error) {
	name = config.CleanName(name)
	if local == "" || name == "" || !mirror.LocalNameOK(name, "windows") {
		return mirror.Pair{}, errors.New("choose another name")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	remote := path.Join(mirror.DriveRoot, s.settings.Device, name)
	for _, p := range s.settings.Folders {
		if p.Local == local {
			return mirror.Pair{}, errors.New("this folder is already mirrored")
		}
		if strings.EqualFold(p.Remote, remote) {
			return mirror.Pair{}, fmt.Errorf("the name %q is already used; choose another", name)
		}
	}
	p := mirror.Pair{ID: randomID(), Local: local, Remote: remote}
	if info, err := s.Folders(p).Stat(""); err != nil || !info.Dir {
		return mirror.Pair{}, errors.New("this folder cannot be opened")
	}
	s.settings.Folders = append(s.settings.Folders, p)
	if err := s.settings.Save(); err != nil {
		s.settings.Folders = s.settings.Folders[:len(s.settings.Folders)-1]
		return mirror.Pair{}, err
	}
	defer s.settingsChanged()
	return p, nil
}

// RemoveFolder stops mirroring. Files stay where they are, here and in
// orOS.
func (s *Service) RemoveFolder(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.settings.Folders[:0:0]
	for _, p := range s.settings.Folders {
		if p.ID != id {
			out = append(out, p)
		}
	}
	if len(out) == len(s.settings.Folders) {
		return errors.New("no such folder")
	}
	s.settings.Folders = out
	delete(s.folders, id)
	if dir, err := config.StateDir(); err == nil {
		_ = mirror.DeleteState(dir, id)
	}
	defer s.settingsChanged()
	return s.settings.Save()
}

// PauseFolder pauses or resumes one folder.
func (s *Service) PauseFolder(id string, paused bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.settings.Folders {
		if s.settings.Folders[i].ID == id {
			s.settings.Folders[i].Paused = paused
			defer s.settingsChanged()
			return s.settings.Save()
		}
	}
	return errors.New("no such folder")
}

// ApproveDeletes lets the next pass carry out a folder's held deletions.
func (s *Service) ApproveDeletes(id string) {
	s.mu.Lock()
	s.approve[id] = true
	s.mu.Unlock()
	s.Kick()
}

// SetPaused pauses or resumes everything.
func (s *Service) SetPaused(p bool) error {
	s.mu.Lock()
	s.settings.Paused = p
	err := s.settings.Save()
	s.mu.Unlock()
	s.settingsChanged()
	s.notify()
	return err
}

// Prefs are the settings the page can change.
type Prefs struct {
	Device     *string `json:"device,omitempty"`
	MaxFileMB  *int    `json:"maxFileMB,omitempty"`
	MaxDeletes *int    `json:"maxDeletes,omitempty"`
	Lang       *string `json:"lang,omitempty"`
	Autostart  *bool   `json:"autostart,omitempty"`
	Mount      *string `json:"mount,omitempty"` // "" = off
	// UpdateCheck turns the daily new-release check on or off (UPD-1).
	UpdateCheck *bool `json:"updateCheck,omitempty"`
}

// SetPrefs applies changed settings. The device name can change only
// while no folder is mirrored (it is part of every mirrored path).
func (s *Service) SetPrefs(p Prefs) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.Device != nil {
		d := config.CleanName(*p.Device)
		if d != s.settings.Device {
			if len(s.settings.Folders) > 0 {
				return errors.New("remove the mirrored folders before renaming this computer")
			}
			if d == "" || !mirror.LocalNameOK(d, "windows") {
				return errors.New("choose another name")
			}
			s.settings.Device = d
		}
	}
	if p.MaxFileMB != nil {
		if *p.MaxFileMB < 1 || *p.MaxFileMB > 4096 {
			return errors.New("size limit: 1 to 4096 MB")
		}
		s.settings.MaxFileMB = *p.MaxFileMB
	}
	if p.MaxDeletes != nil {
		if *p.MaxDeletes < 1 {
			return errors.New("the deletion limit must be at least 1")
		}
		s.settings.MaxDeletes = *p.MaxDeletes
	}
	if p.Lang != nil {
		switch *p.Lang {
		case "", "en", "el":
			s.settings.Lang = *p.Lang
		}
	}
	if p.Mount != nil {
		m := strings.TrimSpace(*p.Mount)
		if m != "" {
			if !mountSupported {
				return errors.New("not available on this system yet")
			}
			if !filepath.IsAbs(m) {
				return errors.New("choose a full folder path")
			}
			m = filepath.Clean(m)
		}
		if err := s.forgetMountLocked(s.settings.Mount, m); err != nil {
			return err
		}
		s.settings.Mount = m
	}
	if p.UpdateCheck != nil {
		s.settings.NoUpdateCheck = !*p.UpdateCheck
		if *p.UpdateCheck {
			select {
			case s.updateNow <- struct{}{}:
			default:
			}
		} else {
			s.update = nil
		}
	}
	if p.Autostart != nil {
		if err := SetAutostart(*p.Autostart); err != nil {
			return err
		}
		s.settings.NoAutostart = !*p.Autostart
	}
	s.engine = nil // new limits
	defer s.settingsChanged()
	return s.settings.Save()
}

// ---------- Passes ----------

func (s *Service) engineLocked() (*mirror.Engine, error) {
	st := s.store()
	if st == nil {
		return nil, dropbox.ErrNotConnected
	}
	if s.vkey == nil {
		return nil, vault.ErrLocked
	}
	if s.engine != nil {
		return s.engine, nil
	}
	k, err := vault.DeriveKeys(s.vkey.Key)
	if err != nil {
		return nil, err
	}
	stateDir, err := config.StateDir()
	if err != nil {
		return nil, err
	}
	trashDir, err := config.TrashDir()
	if err != nil {
		return nil, err
	}
	if _, err := mirror.NoteCloud(stateDir, s.cloudID()); err != nil {
		return nil, err
	}
	vk := s.vkey
	e := &mirror.Engine{
		Vault:    vault.New(st, nil).WithKeys(k),
		StateDir: stateDir,
		Trash:    &mirror.DirTrash{Root: trashDir},
		Opt: mirror.Options{
			MaxFileSize: int64(s.settings.MaxFileMB) << 20,
			MaxDeletes:  s.settings.MaxDeletes,
		},
		Log:       s.addEvent,
		Folder:    s.Folders,
		CanCreate: func() error { return s.keyFileMatches(st, vk) },
	}
	s.engine = e
	return e, nil
}

// SyncOnce runs one pass now. It reports whether files were still
// being written (run again soon).
func (s *Service) SyncOnce() (busy bool, err error) {
	s.mu.Lock()
	if s.settings.Paused {
		s.mu.Unlock()
		return false, nil
	}
	if s.syncing {
		// A pass is already running (the loop, the CLI, or on Android the
		// screen and the background worker): never two over one state.
		s.mu.Unlock()
		return true, nil
	}
	e, err := s.engineLocked()
	if err != nil {
		if errors.Is(err, vault.ErrLocked) && s.needsKey == "" && s.store() != nil {
			s.needsKey = "locked"
		}
		s.mu.Unlock()
		return false, err
	}
	pairs := append([]mirror.Pair{}, s.settings.Folders...)
	approve := s.approve
	s.approve = map[string]bool{}
	s.syncing = true
	s.mu.Unlock()
	s.notify()

	res, err := e.Sync(pairs, approve)

	s.mu.Lock()
	s.syncing = false
	if res != nil {
		s.last = res
		for id, f := range res.Folders {
			s.folders[id] = *f
		}
	}
	if err != nil {
		s.lastErr = err.Error()
		if errors.Is(err, vault.ErrLocked) || errors.Is(err, vault.ErrNoKey) {
			s.engine = nil
			if s.needsKey == "" {
				s.needsKey = "locked"
			}
		}
	} else {
		s.lastErr, s.lastSync = "", time.Now()
		if rev, ok, rerr := e.Vault.Store.Rev(vault.ManifestKey); rerr == nil && ok {
			s.lastRev = rev
		}
	}
	s.mu.Unlock()
	s.notify()
	if res != nil {
		busy = res.Busy
	}
	return busy, err
}

// RemoteMoved tells whether another device changed the vault since the
// last pass (Android's background worker asks before a full pass).
func (s *Service) RemoteMoved() bool { return s.remoteMoved() }

// remoteMoved: one cheap metadata call tells whether another device
// changed the vault since the last pass.
func (s *Service) remoteMoved() bool {
	s.mu.Lock()
	st, last := s.store(), s.lastRev
	ready := st != nil && s.vkey != nil && !s.settings.Paused
	s.mu.Unlock()
	if !ready {
		return false
	}
	rev, ok, err := st.Rev(vault.ManifestKey)
	if err != nil {
		return false
	}
	return ok && rev != last
}

func (s *Service) pairs() []mirror.Pair {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]mirror.Pair{}, s.settings.Folders...)
}

// Run is the background loop: a pass when a mirrored folder changes
// (after a short quiet time), when the vault moves (checked every 30
// seconds), every 10 minutes anyway, and on Kick. Failures back off
// up to 10 minutes.
func (s *Service) Run(ctx context.Context) {
	w := newWatcher(s.pairs())
	defer w.Close()
	poll := time.NewTicker(30 * time.Second)
	defer poll.Stop()
	full := time.NewTicker(10 * time.Minute)
	defer full.Stop()
	purge := time.NewTicker(24 * time.Hour)
	defer purge.Stop()
	s.purgeTrash()
	s.syncMount()
	defer s.unmount()

	var retry <-chan time.Time
	fail := 0
	run := func() {
		s.syncMount() // after an unlock or a lock
		busy, err := s.SyncOnce()
		retry = nil
		switch {
		case err != nil && !errors.Is(err, vault.ErrLocked) && !errors.Is(err, dropbox.ErrNotConnected):
			fail++
			d := time.Duration(1<<min(fail, 5)) * 20 * time.Second
			retry = time.After(min(d, 10*time.Minute))
		case busy:
			fail = 0
			retry = time.After(5 * time.Second)
		default:
			fail = 0
		}
	}
	run()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.kick:
			run()
		case <-s.changed:
			w.Close()
			w = newWatcher(s.pairs())
			s.syncMount()
		case <-w.Events():
			run()
		case <-retry:
			run()
		case <-poll.C:
			s.syncMount()
			if s.remoteMoved() {
				run()
			}
		case <-full.C:
			run()
		case <-purge.C:
			s.purgeTrash()
		}
	}
}

func (s *Service) purgeTrash() {
	if d, err := config.TrashDir(); err == nil {
		_ = (&mirror.DirTrash{Root: d}).Purge()
	}
}

// RefreshAccount fills in the account name (best effort, at start).
func (s *Service) RefreshAccount() {
	s.mu.Lock()
	c := s.client
	s.mu.Unlock()
	if c == nil {
		return
	}
	if a, err := c.CurrentAccount(); err == nil {
		s.mu.Lock()
		s.account = a.Email
		s.mu.Unlock()
		s.notify()
	}
}
