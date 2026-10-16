package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// autostartFile is the freedesktop autostart entry (GNOME, KDE, Xfce,
// Cinnamon, MATE all read it).
func autostartFile() (string, error) {
	d, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "autostart", "oros-drive.desktop"), nil
}

// SetAutostart starts orOS Drive at login, or stops doing so.
func SetAutostart(on bool) error {
	p, err := autostartFile()
	if err != nil {
		return err
	}
	if !on {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if appimage := os.Getenv("APPIMAGE"); appimage != "" {
		exe = appimage
	}
	quoted := `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", "$", `\$`).Replace(exe) + `"`
	entry := "[Desktop Entry]\nType=Application\nName=orOS Drive\nComment=Mirror folders into your orOS Vault\n" +
		"Exec=" + quoted + " run --background\nIcon=oros-drive\nTerminal=false\nX-GNOME-Autostart-enabled=true\n"
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(entry), 0o644)
}

// AutostartOn reports whether the entry exists.
func AutostartOn() bool {
	p, err := autostartFile()
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}
