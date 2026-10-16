// Package config keeps orOS Drive's settings: this computer's name in
// the vault, the mirrored folders and the safety limits. Secrets are
// not here (see package secret).
package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/koulaxizis/oros-drive/mirror"
)

// Dir is the config folder; OROS_DRIVE_CONFIG overrides it (tests).
func Dir() (string, error) {
	if d := os.Getenv("OROS_DRIVE_CONFIG"); d != "" {
		return d, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "orOS Drive"), nil
}

// Settings is app.json.
type Settings struct {
	Device      string        `json:"device"` // folder name of this computer under /Drive
	Folders     []mirror.Pair `json:"folders"`
	Paused      bool          `json:"paused,omitempty"`
	MaxFileMB   int           `json:"maxFileMB,omitempty"`  // 0 = 500
	MaxDeletes  int           `json:"maxDeletes,omitempty"` // 0 = 50
	Lang        string        `json:"lang,omitempty"`       // "en", "el" or "" (from the system)
	NoAutostart bool          `json:"noAutostart,omitempty"`
}

func settingsFile() (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "app.json"), nil
}

// Load reads the settings; missing means defaults, with the device
// named after the computer.
func Load() (*Settings, error) {
	s := &Settings{}
	p, err := settingsFile()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		if err := json.Unmarshal(b, s); err != nil {
			return nil, err
		}
	}
	if s.Device == "" {
		s.Device = DefaultDevice()
	}
	return s, nil
}

// Save writes app.json atomically.
func (s *Settings) Save() error {
	p, err := settingsFile()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// StateDir and TrashDir sit beside app.json.
func StateDir() (string, error) { return sub("state") }
func TrashDir() (string, error) { return sub("Trash") }

func sub(name string) (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, name), nil
}

// DefaultDevice is the computer's name, made safe as a folder name.
func DefaultDevice() string {
	h, _ := os.Hostname()
	h = CleanName(strings.SplitN(h, ".", 2)[0])
	if h == "" {
		return "Computer"
	}
	return h
}

// CleanName makes a name usable as one orOS folder name on every
// system: no slashes, no characters Windows refuses, no dots at the
// ends.
func CleanName(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || strings.ContainsRune(`/\<>:"|?*`, r) {
			return -1
		}
		return r
	}, s)
	s = strings.Trim(strings.TrimSpace(s), ".")
	if len(s) > 64 {
		s = s[:64]
	}
	return strings.TrimSpace(s)
}
