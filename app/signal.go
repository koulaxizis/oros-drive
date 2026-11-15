package app

import (
	"errors"
	"os"
	"time"

	"github.com/koulaxizis/oros-drive/internal/config"
	"github.com/koulaxizis/oros-drive/internal/secret"
	"github.com/koulaxizis/oros-drive/vault"
)

// SOS-1 (koulaxizis/oros Bible Part V "Signals", Part IX SOS-1 S2).
// An SOS in orOS replaces the sync blob next to vault/ with a signal
// and removes vault/. orOS Drive keeps the vault key but never the
// passphrase, so it trusts only the signal's "vp", the proof under
// the vault key:
//   - confirmed SOS (signal-only blob, or "sig" riding in a normal blob
//     of a cloud started again): Drive erases itself here: the Explorer
//     / FUSE disk lets go (Windows: its downloaded files go too), the
//     disk cache is removed, the vault key and the sign-in are
//     forgotten (Logout revokes the token). Mirrored folders are the
//     person's own files and are never touched.
//   - a signal-only blob it cannot confirm (a reset, an SOS without
//     vp, or one sealed by someone with the Dropbox but not the key):
//     nothing runs and nothing is deleted while it is there; its cache
//     may hold the last copy of files someone removed from the cloud.
//   - a normal blob whose "sig" it cannot confirm: an SOS from before
//     this cloud's current key (Drive signed in after it): ignored.

// ErrSignal: the pass did not run because the cloud holds a signal.
var ErrSignal = errors.New("the cloud holds an orOS signal (SOS or reset): orOS Drive waits")

// ErrErased: Drive erased itself after a confirmed SOS.
var ErrErased = errors.New("orOS was closed with SOS: orOS Drive on this computer erased itself and signed out")

type signalVerdict int

const (
	signalNone signalVerdict = iota
	signalStop
	signalErase
)

// readSignal reads the sync blob (only clouds that have one: Dropbox,
// WebDAV) and decides. A failed read decides nothing (the pass runs as
// before, and the next one reads again).
func (s *Service) readSignal(st vault.Store, vk *secret.VaultKey) signalVerdict {
	br, ok := st.(vault.BlobReader)
	if !ok || vk == nil {
		return s.setSignal("")
	}
	blob, err := br.GetSyncBlob()
	if err != nil {
		return signalNone
	}
	sg, ok := vault.ParseSignal(blob)
	if !ok {
		return s.setSignal("")
	}
	k, err := vault.DeriveKeys(vk.Key)
	if err == nil && sg.Kind == "sos" && k.ConfirmSignal(sg) {
		return signalErase
	}
	if !sg.Only {
		return s.setSignal("")
	}
	if sg.Kind == "reset" {
		s.setSignal("reset")
	} else {
		s.setSignal("closed")
	}
	return signalStop
}

func (s *Service) setSignal(v string) signalVerdict {
	s.mu.Lock()
	changed := s.signal != v
	s.signal = v
	s.mu.Unlock()
	if changed {
		s.settingsChanged() // the disk follows (syncMount)
		s.notify()
	}
	if v == "" {
		return signalNone
	}
	return signalStop
}

// eraseForSOS erases this computer's orOS Drive (see above) and
// returns ErrErased. Called from a pass (syncing is set).
func (s *Service) eraseForSOS() error {
	s.mu.Lock()
	mount := s.settings.Mount
	s.eraseMountLocked(mount)
	s.settings.Mount = ""
	s.settings.SOS = time.Now().UnixMilli()
	_ = s.settings.Save()
	s.disk, s.diskKey = nil, nil
	s.signal = ""
	s.syncing = false
	s.lastErr = ErrErased.Error()
	s.mu.Unlock()
	if d, err := config.CacheDir(); err == nil {
		_ = os.RemoveAll(d)
	}
	_ = s.Logout()
	s.settingsChanged()
	s.notify()
	return ErrErased
}
