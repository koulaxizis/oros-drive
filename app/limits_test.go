package app

import (
	"runtime"
	"testing"
)

// Chris 2026-11-27: the size limit defaults to the largest allowed
// (Android keeps 500 MB); a limit the person set wins.
func TestMaxFileDefault(t *testing.T) {
	want := 4096
	if runtime.GOOS == "android" {
		want = 500
	}
	if DefaultMaxFileMB() != want || maxFileMB(0) != want {
		t.Fatalf("default %d, want %d", maxFileMB(0), want)
	}
	if maxFileMB(700) != 700 {
		t.Fatal("a set limit must win")
	}
	if MaxFileMBLimit != 4096 {
		t.Fatal("the settings page and SetPrefs take 1 to 4096")
	}
}
