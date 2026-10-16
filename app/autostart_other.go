//go:build !linux && !windows

package app

import "errors"

// SetAutostart is not offered on this system yet.
func SetAutostart(on bool) error {
	if on {
		return errors.New("starting at login is not supported on this system yet")
	}
	return nil
}

func AutostartOn() bool { return false }
