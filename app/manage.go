package app

import (
	"errors"
	"os"
	"path/filepath"
)

// One orOS icon (bundle plan, oros-portable "orOS tray"): installed
// orOS starts this app at login and shows it in its own tray menu, so
// orOS Drive then shows no icon of its own and has no Run value or
// autostart entry of its own. It stays its own program: its folder,
// settings, sign-in, Explorer folder and uninstaller do not move.

// managedActive: orOS (the program at by) is there to start and show
// this app. A program that is gone is not: orOS was uninstalled
// without letting go, so this app shows and starts itself again.
func managedActive(by string) bool {
	if by == "" {
		return false
	}
	fi, err := os.Stat(by)
	return err == nil && !fi.IsDir()
}

// Managed reports whether orOS shows this app now.
func (s *Service) Managed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return managedActive(s.settings.ManagedBy)
}

// ErrManagedBy: the program given is not a file.
var ErrManagedBy = errors.New("the orOS program was not found")

// SetManaged hands this app to the orOS program at by, or ("") takes it
// back. Handing over turns this app's own start at login off (orOS
// starts it now) and remembers whether it was on; taking back turns it
// on again when it was on, or when folders are mirrored (they must go
// on syncing after orOS is gone). The icon hides or shows at once.
func (s *Service) SetManaged(by string) error {
	if by != "" {
		abs, err := filepath.Abs(by)
		if err != nil || !managedActive(abs) {
			return ErrManagedBy
		}
		by = abs
	}
	s.mu.Lock()
	was := s.settings.ManagedBy
	if by == was {
		s.mu.Unlock()
		return nil
	}
	if by != "" {
		if was == "" {
			s.settings.OwnAutostart = AutostartOn()
		}
		if err := SetAutostart(false); err != nil {
			s.mu.Unlock()
			return err
		}
	} else {
		if s.settings.OwnAutostart || len(s.settings.Folders) > 0 {
			if err := SetAutostart(true); err != nil {
				s.mu.Unlock()
				return err
			}
			s.settings.NoAutostart = false
		}
		s.settings.OwnAutostart = false
	}
	s.settings.ManagedBy = by
	err := s.settings.Save()
	s.mu.Unlock()
	go s.notify() // the app hides or shows its icon now
	return err
}

// LetGoIfOrphaned takes the app back when the orOS program that
// managed it is gone, and reports whether it did. Run at start.
func (s *Service) LetGoIfOrphaned() bool {
	s.mu.Lock()
	by := s.settings.ManagedBy
	s.mu.Unlock()
	if by == "" || managedActive(by) {
		return false
	}
	return s.SetManaged("") == nil
}

// DropOwnAutostart removes this app's own start at login while orOS
// starts it (a standalone orOS Drive setup installed over a managed
// one writes it again), so it never starts twice. Run at start.
func (s *Service) DropOwnAutostart() {
	if s.Managed() && AutostartOn() {
		s.mu.Lock()
		s.settings.OwnAutostart = true
		s.settings.Save()
		s.mu.Unlock()
		_ = SetAutostart(false)
	}
}

// Summary is what orOS's tray shows of this app (GET /api/summary). Its
// fields only ever grow; V changes if one ever changes meaning.
type Summary struct {
	V        int    `json:"v"`
	Version  string `json:"version"`
	State    string `json:"state"` // StateOf: warn, paused, sync, bad, ok
	SignedIn bool   `json:"signedIn"`
	Unlocked bool   `json:"unlocked"`
	Paused   bool   `json:"paused"`
	Syncing  bool   `json:"syncing"`
	Folders  int    `json:"folders"`
	Managed  bool   `json:"managed"`
	Update   string `json:"update,omitempty"` // a newer release's version
}

// SummaryOf condenses a status for orOS's tray.
func SummaryOf(st Status) Summary {
	sum := Summary{V: 1, Version: st.Version, State: StateOf(st), SignedIn: st.SignedIn, Unlocked: st.Unlocked,
		Paused: st.Paused, Syncing: st.Syncing, Folders: len(st.Settings.Folders), Managed: st.Managed}
	if st.Update != nil {
		sum.Update = st.Update.Version
	}
	return sum
}
