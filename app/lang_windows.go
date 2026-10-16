package app

import "golang.org/x/sys/windows"

var procGetUserDefaultUILanguage = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetUserDefaultUILanguage")

func windowsGreek() (bool, bool) {
	r, _, _ := procGetUserDefaultUILanguage.Call()
	return uint16(r)&0x3ff == 0x08, true // LANG_GREEK
}
