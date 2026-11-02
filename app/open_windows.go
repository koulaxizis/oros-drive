package app

import (
	"os/exec"

	"golang.org/x/sys/windows"
)

// Open opens a link in the default browser or a folder in Explorer.
// Links go through ShellExecute and not through rundll32 url.dll, which
// antivirus heuristics read as a malware trait.
func Open(target string) error {
	if webLink(target) {
		verb, _ := windows.UTF16PtrFromString("open")
		file, err := windows.UTF16PtrFromString(target)
		if err != nil {
			return err
		}
		return windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
	}
	cmd := exec.Command("explorer", target)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}
