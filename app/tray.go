//go:build !android

package app

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"runtime"
	"sync"

	"fyne.io/systray"
)

// Tray shows the app's state in the system tray and offers the
// common actions. On a Linux desktop without a tray (plain GNOME
// needs the AppIndicator extension), the app runs the same and the
// settings page is the way in.
type Tray struct {
	S      *Service
	UI     *UI
	OnQuit func()

	mu     sync.Mutex
	status *systray.MenuItem
	pause  *systray.MenuItem
	icon   string
}

// Run blocks until Quit; call it from main.
func (t *Tray) Run() { systray.Run(t.ready, func() {}) }

// Quit closes the tray (and with it Run).
func (t *Tray) Quit() { systray.Quit() }

func (t *Tray) ready() {
	systray.SetTitle("orOS Drive")
	t.status = systray.AddMenuItem("orOS Drive", "")
	t.status.Disable()
	systray.AddSeparator()
	open := systray.AddMenuItem(t.word("Settings", "Ρυθμίσεις"), "")
	syncNow := systray.AddMenuItem(t.word("Sync now", "Συγχρονισμός τώρα"), "")
	t.pause = systray.AddMenuItem(t.word("Pause", "Παύση"), "")
	oros := systray.AddMenuItem(t.word("Open orOS", "Άνοιγμα orOS"), "")
	systray.AddSeparator()
	quit := systray.AddMenuItem(t.word("Quit", "Έξοδος"), "")
	t.refresh()
	t.S.OnChange(t.refresh)
	go func() {
		for {
			select {
			case <-open.ClickedCh:
				Open(t.UI.URL())
			case <-syncNow.ClickedCh:
				t.S.Kick()
			case <-t.pause.ClickedCh:
				t.S.SetPaused(!t.S.Status().Paused)
			case <-oros.ClickedCh:
				Open(OrosURL)
			case <-quit.ClickedCh:
				if t.OnQuit != nil {
					t.OnQuit()
				}
				systray.Quit()
				return
			}
		}
	}()
}

func (t *Tray) greek() bool {
	st := t.S.Status()
	if st.Settings.Lang != "" {
		return st.Settings.Lang == "el"
	}
	return systemGreek()
}

func (t *Tray) word(en, el string) string {
	if t.greek() {
		return el
	}
	return en
}

func (t *Tray) refresh() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.status == nil {
		return
	}
	st := t.S.Status()
	text, kind := t.word("Up to date", "Ενημερωμένο"), "ok"
	switch {
	case !st.SignedIn || !st.Unlocked:
		text, kind = t.word("Open the settings to set up", "Άνοιξε τις ρυθμίσεις για να ξεκινήσεις"), "warn"
	case st.Paused:
		text, kind = t.word("Paused", "Σε παύση"), "paused"
	case st.Syncing:
		text, kind = t.word("Syncing…", "Συγχρονίζει…"), "sync"
	case st.Error != "":
		text, kind = t.word("Needs attention", "Θέλει προσοχή"), "bad"
	default:
		for _, f := range st.Folders {
			if f.State != "ok" && f.State != "paused" {
				text, kind = t.word("Needs attention", "Θέλει προσοχή"), "bad"
			}
		}
	}
	t.status.SetTitle(text)
	systray.SetTooltip("orOS Drive: " + text)
	if st.Paused {
		t.pause.SetTitle(t.word("Resume", "Συνέχεια"))
	} else {
		t.pause.SetTitle(t.word("Pause", "Παύση"))
	}
	if kind != t.icon {
		t.icon = kind
		systray.SetIcon(trayIcon(kind))
	}
}

// trayIcon is the orOS mark with a small dot for the state.
func trayIcon(kind string) []byte {
	src, err := webFS.ReadFile("web/icon.png")
	if err != nil {
		return nil
	}
	img, err := png.Decode(bytes.NewReader(src))
	if err != nil {
		return src
	}
	const size = 64
	out := shrink(img, size)
	dot := map[string]color.RGBA{
		"sync": {0x4a, 0x90, 0xe2, 0xff}, "warn": {0xe0, 0xa8, 0x4a, 0xff},
		"bad": {0xe5, 0x48, 0x3d, 0xff}, "paused": {0x9a, 0x9a, 0x9a, 0xff},
	}
	if c, ok := dot[kind]; ok {
		cx, cy, r := size-14, size-14, 12
		for y := cy - r - 2; y <= cy+r+2; y++ {
			for x := cx - r - 2; x <= cx+r+2; x++ {
				d := (x-cx)*(x-cx) + (y-cy)*(y-cy)
				if d <= r*r {
					out.Set(x, y, c)
				} else if d <= (r+2)*(r+2) {
					out.Set(x, y, color.RGBA{0x14, 0x13, 0x10, 0xff})
				}
			}
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, out)
	if runtime.GOOS == "windows" {
		return pngToICO(buf.Bytes(), size)
	}
	return buf.Bytes()
}

// shrink scales by averaging (box filter): good enough for an icon.
func shrink(img image.Image, size int) *image.RGBA {
	b := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			x0, x1 := b.Min.X+x*b.Dx()/size, b.Min.X+(x+1)*b.Dx()/size
			y0, y1 := b.Min.Y+y*b.Dy()/size, b.Min.Y+(y+1)*b.Dy()/size
			var r, g, bl, a, n uint32
			for yy := y0; yy < y1; yy++ {
				for xx := x0; xx < x1; xx++ {
					c := color.NRGBAModel.Convert(img.At(xx, yy)).(color.NRGBA)
					r += uint32(c.R) * uint32(c.A)
					g += uint32(c.G) * uint32(c.A)
					bl += uint32(c.B) * uint32(c.A)
					a += uint32(c.A)
					n++
				}
			}
			if a > 0 {
				out.Set(x, y, color.NRGBA{uint8(r / a), uint8(g / a), uint8(bl / a), uint8(a / n)})
			}
		}
	}
	return out
}

// pngToICO wraps one PNG in an ICO container (Windows Vista and later
// read PNG-compressed icons).
func pngToICO(p []byte, size int) []byte {
	var b bytes.Buffer
	binary.Write(&b, binary.LittleEndian, [3]uint16{0, 1, 1})
	s := byte(size)
	if size >= 256 {
		s = 0
	}
	b.Write([]byte{s, s, 0, 0})
	binary.Write(&b, binary.LittleEndian, [2]uint16{1, 32})
	binary.Write(&b, binary.LittleEndian, [2]uint32{uint32(len(p)), 22})
	b.Write(p)
	return b.Bytes()
}
