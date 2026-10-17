//go:build windows && (amd64 || arm64)

package app

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"time"

	"github.com/koulaxizis/oros-drive/internal/config"
	"github.com/koulaxizis/oros-drive/mirror"
	"github.com/koulaxizis/oros-drive/vdisk/cloudfiles"
)

// The orOS disk in Explorer: a Cloud Files sync root (wave 6).
var mountSupported = cloudfiles.Supported()

// cfState is where the engine keeps what both sides agreed on, one
// file per folder.
func cfState(dir string) string {
	cache, err := config.CacheDir()
	if err != nil {
		return ""
	}
	h := sha256.Sum256([]byte(strings.ToLower(dir)))
	return filepath.Join(cache, "cloudfiles-"+hex.EncodeToString(h[:8])+".json")
}

// syncMount connects the folder while a folder is chosen and the vault
// is unlocked. A lock only disconnects it (files not downloaded wait);
// choosing another folder or none ends the old one's life as the disk.
func (s *Service) syncMount() {
	s.mu.Lock()
	defer s.mu.Unlock()
	want := s.settings.Mount
	if s.vkey == nil || s.store() == nil {
		want = ""
	}
	if s.mnt.srv != nil && s.mnt.path == want && s.diskKey == s.vkey {
		return
	}
	if s.mnt.srv != nil {
		s.mnt.srv.Unmount()
		s.mnt.srv = nil
		if old := s.mnt.path; old != "" && !strings.EqualFold(old, s.settings.Mount) {
			if err := cloudfiles.Unregister(old, cfState(old)); err != nil {
				s.mnt.err = err.Error()
			}
		}
	}
	s.mnt.path, s.mnt.err = want, ""
	if want == "" {
		return
	}
	d, err := s.diskLocked()
	if err != nil {
		s.mnt.err = err.Error()
		return
	}
	trash, _ := config.TrashDir()
	srv, err := cloudfiles.Mount(d, want, cloudfiles.Options{
		StatePath: cfState(want),
		TrashDir:  trash,
		Logf: func(kind, rel, detail string) {
			// Never wait for s.mu here: a pass can run while it is held.
			go s.addEvent(mirror.Event{Time: time.Now(), Kind: kind, Folder: "orOS", Path: rel, Detail: detail})
		},
	})
	if err != nil {
		s.mnt.err = err.Error()
		return
	}
	s.mnt.srv = srv
}

// forgetMountLocked ends the old folder's life as the orOS disk when
// the setting moves away from it and no connection here will do it:
// the vault is locked, or this is not the running app (the setting
// changed from the command line, or by the uninstaller). Otherwise
// Windows keeps a folder of placeholders nothing can open.
func (s *Service) forgetMountLocked(old, now string) error {
	if old == "" || strings.EqualFold(old, now) {
		return nil
	}
	if s.mnt.srv != nil && strings.EqualFold(s.mnt.path, old) {
		return nil // syncMount unmounts it first, then unregisters
	}
	return cloudfiles.Unregister(old, cfState(old))
}

func (s *Service) unmount() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mnt.srv != nil {
		s.mnt.srv.Unmount()
		s.mnt.srv = nil
	}
}
