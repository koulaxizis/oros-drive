package onelock

import (
	"errors"

	"golang.org/x/sys/windows"
)

// Take holds the per-session named mutex name (dir is not used: the
// mutex is per Windows session, so per signed-in user).
func Take(name, dir string) (*Lock, error) {
	p, err := windows.UTF16PtrFromString(`Local\` + name)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateMutex(nil, false, p)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		if h != 0 {
			windows.CloseHandle(h)
		}
		return nil, ErrHeld
	}
	if err != nil {
		return nil, err
	}
	return &Lock{release: func() { windows.CloseHandle(h) }}, nil
}
