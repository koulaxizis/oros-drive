//go:build !android

package app

import "testing"

func TestTrayIcons(t *testing.T) {
	for _, k := range []string{"ok", "sync", "warn", "bad", "paused"} {
		if b := trayIcon(k); len(b) < 100 {
			t.Fatal(k)
		}
	}
	ico := pngToICO([]byte("PNGDATA"), 64)
	if len(ico) != 22+7 || ico[2] != 1 || ico[6] != 64 {
		t.Fatal(ico)
	}
}
