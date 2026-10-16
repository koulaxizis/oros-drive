// Package secret keeps orOS Drive's two secrets: the Dropbox sign-in
// (refresh token) and the vault key. They go into the OS keychain
// (Windows Credential Manager, the Secret Service on Linux, the
// macOS Keychain). Where there is none (a Linux server without a
// desktop session), they fall back to files in the config folder that
// only this user can read, and Where() says so.
//
// The sync passphrase itself is never stored.
package secret

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/zalando/go-keyring"

	"github.com/koulaxizis/oros-drive/dropbox"
	"github.com/koulaxizis/oros-drive/internal/config"
)

const service = "orOS Drive"

const (
	itemDropbox = "dropbox"
	itemVault   = "vault-key"
)

// VaultKey is the unwrapped vault key, with what it belongs to.
type VaultKey struct {
	Key []byte `json:"-"`
	// KeyFile is the SHA-256 of /vault/key.json when the key was
	// unwrapped. A different key file in the cloud means the vault
	// was reset or rebuilt: the key is not used to create anything.
	KeyFile string `json:"keyFile"`
	Account string `json:"account,omitempty"` // Dropbox account id
}

type vaultKeyJSON struct {
	Key string `json:"key"`
	VaultKey
}

// Backend is a platform keystore supplied by the host app (Android:
// values encrypted with a key in the Android Keystore). Get returns ""
// for a missing item.
type Backend interface {
	Get(name string) (string, error)
	Set(name, value string) error
	Delete(name string) error
}

var backend Backend

// SetBackend makes every secret go to b instead of the OS keychain.
func SetBackend(b Backend) { backend = b }

func useKeyring() bool { return os.Getenv("OROS_DRIVE_NO_KEYRING") == "" }

// Where tells where secrets are kept: "keychain" or "file".
func Where() string {
	if backend != nil {
		return "keychain"
	}
	if !useKeyring() {
		return "file"
	}
	if _, err := keyring.Get(service, itemDropbox); err == nil || errors.Is(err, keyring.ErrNotFound) {
		return "keychain"
	}
	return "file"
}

func file(item string) (string, error) {
	d, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, item+".json"), nil
}

func get(item string) ([]byte, error) {
	if backend != nil {
		v, err := backend.Get(item)
		if err != nil || v == "" {
			return nil, err
		}
		return []byte(v), nil
	}
	if useKeyring() {
		s, err := keyring.Get(service, item)
		if err == nil {
			return []byte(s), nil
		}
		if !errors.Is(err, keyring.ErrNotFound) {
			// No keychain here: the file is the store.
			return getFile(item)
		}
	}
	return getFile(item)
}

func getFile(item string) ([]byte, error) {
	p, err := file(item)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return b, err
}

func set(item string, b []byte) error {
	if backend != nil {
		return backend.Set(item, string(b))
	}
	if useKeyring() {
		if err := keyring.Set(service, item, string(b)); err == nil {
			delFile(item) // an older plain copy goes
			return nil
		}
	}
	p, err := file(item)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func delFile(item string) {
	if p, err := file(item); err == nil {
		os.Remove(p)
	}
}

func del(item string) error {
	if backend != nil {
		return backend.Delete(item)
	}
	if useKeyring() {
		if err := keyring.Delete(service, item); err != nil && !errors.Is(err, keyring.ErrNotFound) {
			delFile(item)
			return nil
		}
	}
	delFile(item)
	return nil
}

// LoadTokens returns the Dropbox sign-in, or dropbox.ErrNotConnected.
func LoadTokens() (dropbox.Tokens, error) {
	var t dropbox.Tokens
	b, err := get(itemDropbox)
	if err != nil {
		return t, err
	}
	if b == nil {
		return t, dropbox.ErrNotConnected
	}
	if json.Unmarshal(b, &t) != nil || t.Refresh == "" {
		return dropbox.Tokens{}, dropbox.ErrNotConnected
	}
	return t, nil
}

func SaveTokens(t dropbox.Tokens) error {
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return set(itemDropbox, b)
}

func DeleteTokens() error { return del(itemDropbox) }

// LoadVaultKey returns the stored vault key, or nil.
func LoadVaultKey() (*VaultKey, error) {
	b, err := get(itemVault)
	if err != nil || b == nil {
		return nil, err
	}
	var j vaultKeyJSON
	if err := json.Unmarshal(b, &j); err != nil {
		return nil, nil
	}
	k, err := base64.StdEncoding.DecodeString(j.Key)
	if err != nil || len(k) != 32 {
		return nil, nil
	}
	vk := j.VaultKey
	vk.Key = k
	return &vk, nil
}

func SaveVaultKey(k *VaultKey) error {
	b, err := json.Marshal(vaultKeyJSON{Key: base64.StdEncoding.EncodeToString(k.Key), VaultKey: *k})
	if err != nil {
		return err
	}
	return set(itemVault, b)
}

func DeleteVaultKey() error { return del(itemVault) }
