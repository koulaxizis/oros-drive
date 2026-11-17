// Package onelock makes sure one orOS Drive app runs per user: the
// login start, orOS's tray and a double click can all start it at the
// same moment, before any of them has written instance.json.
package onelock

import "errors"

// ErrHeld: another orOS Drive app of this user holds the lock.
var ErrHeld = errors.New("orOS Drive is already starting")

// Lock is held until Release (or the process ends).
type Lock struct{ release func() }

// Release lets the next app start.
func (l *Lock) Release() {
	if l != nil && l.release != nil {
		l.release()
		l.release = nil
	}
}
