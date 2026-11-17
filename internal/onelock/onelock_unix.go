//go:build unix

package onelock

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// Take holds an flock on <dir>/<name>.lock (released by the kernel
// when the process ends, so a crash never leaves it held).
func Take(name, dir string) (*Lock, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, name+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrHeld
		}
		return nil, err
	}
	return &Lock{release: func() { unix.Flock(int(f.Fd()), unix.LOCK_UN); f.Close() }}, nil
}
