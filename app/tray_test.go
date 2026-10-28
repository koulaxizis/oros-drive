//go:build !android

package app

import (
	"bytes"
	"image/color"
	"image/png"
	"runtime"
	"testing"
)

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

// The Drive icon is the orOS mark in reverse colours: a gold tile, never
// the dark tile of the orOS app's tray icon (oros-portable).
func TestTrayIconIsReversed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ICO wrapping")
	}
	img, err := png.Decode(bytes.NewReader(trayIcon("ok")))
	if err != nil {
		t.Fatal(err)
	}
	c := color.NRGBAModel.Convert(img.At(32, 58)).(color.NRGBA) // tile, below the mark
	if c != markGold {
		t.Fatalf("tile %v, want gold", c)
	}
	if c := color.NRGBAModel.Convert(img.At(0, 0)).(color.NRGBA); c.A != 0 {
		t.Fatalf("corner %v, want transparent", c)
	}
}
