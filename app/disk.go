package app

import (
	"os"
	"path/filepath"

	"github.com/koulaxizis/oros-drive/dropbox"
	"github.com/koulaxizis/oros-drive/internal/config"
	"github.com/koulaxizis/oros-drive/vault"
	"github.com/koulaxizis/oros-drive/vdisk"
)

// MountInfo is the state of the folder that shows the whole orOS disk.
type MountInfo struct {
	Supported bool   `json:"supported"`
	Path      string `json:"path,omitempty"`
	Default   string `json:"default,omitempty"` // suggested folder
	Mounted   bool   `json:"mounted"`
	Error     string `json:"error,omitempty"`
}

// unmounter is a mounted disk (a FUSE server on Linux).
type unmounter interface{ Unmount() error }

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
	s.disk = &vdisk.Disk{Vault: vault.New(st, nil).WithKeys(k), CacheDir: cache, MaxFile: int64(s.settings.MaxFileMB) << 20}
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
