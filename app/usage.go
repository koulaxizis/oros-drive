package app

import (
	"errors"
	"time"

	"github.com/koulaxizis/oros-drive/dropbox"
	"github.com/koulaxizis/oros-drive/vault"
)

// Usage is the vault's space, for the settings page.
func (s *Service) Usage() (*vault.Usage, error) {
	v, err := s.vaultNow()
	if err != nil {
		return nil, err
	}
	return v.Usage(time.Now())
}

// CleanUp removes objects no file uses any more (vault.CleanUp), never
// while a pass runs.
func (s *Service) CleanUp() (removed int, bytes int64, err error) {
	s.mu.Lock()
	if s.syncing {
		s.mu.Unlock()
		return 0, 0, errBusy
	}
	v, err := s.vaultLocked()
	if err != nil {
		s.mu.Unlock()
		return 0, 0, err
	}
	s.syncing = true
	s.mu.Unlock()
	s.notify()
	defer func() {
		s.mu.Lock()
		s.syncing = false
		s.mu.Unlock()
		s.notify()
	}()
	return v.CleanUp(time.Now())
}

var errBusy = errors.New("a sync is running; try again in a moment")

func (s *Service) vaultNow() (*vault.Vault, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.vaultLocked()
}

func (s *Service) vaultLocked() (*vault.Vault, error) {
	st := s.store()
	if st == nil {
		return nil, dropbox.ErrNotConnected
	}
	if s.vkey == nil {
		return nil, vault.ErrLocked
	}
	k, err := vault.DeriveKeys(s.vkey.Key)
	if err != nil {
		return nil, err
	}
	return vault.New(st, nil).WithKeys(k), nil
}
