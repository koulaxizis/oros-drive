package app

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

// guiExe is the windowless "orOS Drive.exe" beside orosdrive.exe when
// both are installed, so no console window opens at login.
func guiExe() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if strings.EqualFold(filepath.Base(exe), "orosdrive.exe") {
		gui := filepath.Join(filepath.Dir(exe), "orOS Drive.exe")
		if _, err := os.Stat(gui); err == nil {
			return gui, nil
		}
	}
	return exe, nil
}

// SetAutostart starts orOS Drive at login (HKCU Run), or stops doing so.
func SetAutostart(on bool) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !on {
		if err := k.DeleteValue("orOS Drive"); err != nil && err != registry.ErrNotExist {
			return err
		}
		return nil
	}
	exe, err := guiExe()
	if err != nil {
		return err
	}
	return k.SetStringValue("orOS Drive", `"`+exe+`" run --background`)
}

// AutostartOn reports whether the Run value exists.
func AutostartOn() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue("orOS Drive")
	return err == nil
}
