//go:build linux && !android

package app

import (
	"errors"
	"os"
	"os/exec"
	"syscall"

	"github.com/koulaxizis/oros-drive/vdisk/fusefs"
)

const mountSupported = true

// syncMount mounts or unmounts the orOS disk to match the settings and
// the lock: mounted while a folder is chosen and the vault is unlocked.
func (s *Service) syncMount() {
	s.mu.Lock()
	defer s.mu.Unlock()
	want := s.settings.Mount
	if s.vkey == nil || s.store() == nil {
		want = ""
	}
	if s.mnt.srv != nil && s.mnt.path == want && s.diskKey == s.vkey {
		return
	}
	if s.mnt.srv != nil {
		if err := s.mnt.srv.Unmount(); err != nil {
			s.mnt.err = err.Error() // busy: a program still has a file open
			return
		}
		s.mnt.srv = nil
	}
	s.mnt.path, s.mnt.err = want, ""
	if want == "" {
		return
	}
	d, err := s.diskLocked()
	if err != nil {
		s.mnt.err = err.Error()
		return
	}
	if err := os.MkdirAll(want, 0o755); err != nil {
		s.mnt.err = err.Error()
		return
	}
	if ents, err := os.ReadDir(want); err != nil || len(ents) > 0 {
		if err == nil {
			err = errors.New("the folder must be empty")
		}
		s.mnt.err = err.Error()
		return
	}
	srv, err := fusefs.Mount(d, want)
	if err != nil {
		s.mnt.err = err.Error()
		return
	}
	s.mnt.srv = srv
}

func (s *Service) unmount() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mnt.srv != nil {
		if s.mnt.srv.Unmount() != nil {
			// A program still has a file open: detach the folder now
			// (it lets go when the program does) rather than leave a
			// dead mount behind when the app exits.
			if syscall.Unmount(s.mnt.path, syscall.MNT_DETACH) != nil {
				_ = exec.Command("fusermount3", "-u", "-z", s.mnt.path).Run()
			}
		}
		s.mnt.srv = nil
	}
}
