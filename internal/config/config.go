// Package config keeps orOS Drive's small local state: the Dropbox
// refresh token. It is stored in the user's config folder, readable by
// the user only. (Wave 3 moves secrets into the OS keychain.)
package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/koulaxizis/oros-drive/dropbox"
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

func tokenFile() (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "dropbox.json"), nil
}

// LoadTokens returns the saved tokens, or dropbox.ErrNotConnected.
func LoadTokens() (dropbox.Tokens, error) {
	var t dropbox.Tokens
	p, err := tokenFile()
	if err != nil {
		return t, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return t, dropbox.ErrNotConnected
	}
	if err != nil {
		return t, err
	}
	if err := json.Unmarshal(b, &t); err != nil || t.Refresh == "" {
		return dropbox.Tokens{}, dropbox.ErrNotConnected
	}
	return t, nil
}

// SaveTokens writes the tokens (0600, atomic rename).
func SaveTokens(t dropbox.Tokens) error {
	p, err := tokenFile()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// DeleteTokens forgets the sign-in.
func DeleteTokens() error {
	p, err := tokenFile()
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
