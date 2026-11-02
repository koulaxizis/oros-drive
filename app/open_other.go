//go:build !windows

package app

import (
	"os/exec"
	"runtime"
)

// Open opens a link or a folder with the system's handler.
func Open(target string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	cmd := exec.Command(name, target)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}
