package app

import (
	"os"
	"path/filepath"

	"github.com/koulaxizis/oros-drive/dropbox"
	"github.com/koulaxizis/oros-drive/internal/config"
	"github.com/koulaxizis/oros-drive/vault"
	"github.com/koulaxizis/oros-drive/vdisk"
	"github.com/koulaxizis/oros-drive/vdisk/cloudfiles"
)

// MountInfo is the state of the folder that shows the whole orOS disk.
type MountInfo struct {
	Supported bool   `json:"supported"`
	Path      string `json:"path,omitempty"`
	Default   string `json:"default,omitempty"` // suggested folder
	Mounted   bool   `json:"mounted"`
	Error     string `json:"error,omitempty"`
	// Held: deletions waiting for the person (more than 50 at once).
	Held *mountHeld `json:"held,omitempty"`
	// Keep: the full copy's progress, on systems that have it (the
	// Windows Explorer folder, KEEP-1).
	Keep *cloudfiles.KeepReport `json:"keep,omitempty"`
}

// keepMount is a mounted disk that can keep a full copy (cloudfiles).
type keepMount interface {
	Status() (cloudfiles.Report, error)
}

func keepOf(srv unmounter) *cloudfiles.KeepReport {
	km, ok := srv.(keepMount)
	if !ok {
		return nil
	}
	rep, _ := km.Status()
	return &rep.Keep
}

// unmounter is a mounted disk (a FUSE server on Linux).
type unmounter interface{ Unmount() error }

// heldMount is a mounted disk whose deletions can wait for the person
// (the Windows Explorer folder, cloudfiles).
type heldMount interface {
	Held() cloudfiles.Held
	ApproveHeld(kind, sig string) bool
}

// heldShown is how many held paths the settings page lists.
const heldShown = 300

// mountHeld is what the settings page shows of the held deletions.
type mountHeld struct {
	Gone      []string `json:"gone,omitempty"`
	GoneCount int      `json:"goneCount,omitempty"`
	GoneSig   string   `json:"goneSig,omitempty"`
	Lost      []string `json:"lost,omitempty"`
	LostCount int      `json:"lostCount,omitempty"`
	LostSig   string   `json:"lostSig,omitempty"`
}

func heldOf(srv unmounter) *mountHeld {
	hm, ok := srv.(heldMount)
	if !ok {
		return nil
	}
	h := hm.Held()
	if h.GoneSig == "" && h.LostSig == "" {
		return nil
	}
	cut := func(l []string) []string {
		if len(l) > heldShown {
			return l[:heldShown]
		}
		return l
	}
	return &mountHeld{Gone: cut(h.Gone), GoneCount: len(h.Gone), GoneSig: h.GoneSig,
		Lost: cut(h.Lost), LostCount: len(h.Lost), LostSig: h.LostSig}
}

// ApproveMountDeletes carries out the held set the person saw (kind
// "gone" or "lost", sig from the status). False: that set is no longer
// the one waiting, nothing is deleted.
func (s *Service) ApproveMountDeletes(kind, sig string) bool {
	s.mu.Lock()
	srv := s.mnt.srv
	s.mu.Unlock()
	hm, ok := srv.(heldMount)
	return ok && (kind == "gone" || kind == "lost") && hm.ApproveHeld(kind, sig)
}

type mountState struct {
	srv  unmounter
	path string
	err  string
}

// Disk returns the orOS disk seen as a file system (vdisk), built on
// the unlocked vault key. It is rebuilt after a lock or a new key.
func (s *Service) Disk() (*vdisk.Disk, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.diskLocked()
}

func (s *Service) diskLocked() (*vdisk.Disk, error) {
	st := s.store()
	if st == nil {
		return nil, dropbox.ErrNotConnected
	}
	if s.vkey == nil {
		return nil, vault.ErrLocked
	}
	if s.disk != nil && s.diskKey == s.vkey {
		return s.disk, nil
	}
	k, err := vault.DeriveKeys(s.vkey.Key)
	if err != nil {
		return nil, err
	}
	cache, err := config.CacheDir()
	if err != nil {
		return nil, err
	}
	s.disk = &vdisk.Disk{Vault: vault.New(st, nil).WithKeys(k), CacheDir: cache, MaxFile: int64(maxFileMB(s.settings.MaxFileMB)) << 20}
	s.diskKey = s.vkey
	return s.disk, nil
}

// defaultMount suggests ~/orOS.
func defaultMount() string {
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, "orOS")
	}
	return ""
}

// Shutdown lets go of what the app holds outside itself (the mounted
// folder). Call it before the process exits.
func (s *Service) Shutdown() { s.unmount() }
