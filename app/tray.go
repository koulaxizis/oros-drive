//go:build !android

package app

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"runtime"
	"strings"
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
	update *systray.MenuItem // "Download orOS Drive x.y.z", only when newer (UPD-1)
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
	t.update = systray.AddMenuItem("", "")
	t.update.Hide()
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
			case <-t.update.ClickedCh:
				if u := t.S.Status().Update; u != nil && strings.HasPrefix(u.URL, ReleasesPage) {
					Open(u.URL)
				}
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
	if st.Update != nil {
		t.update.SetTitle(t.word("Download orOS Drive ", "Λήψη orOS Drive ") + st.Update.Version)
		t.update.Show()
	} else {
		t.update.Hide()
	}
	if kind != t.icon {
		t.icon = kind
		systray.SetIcon(trayIcon(kind))
	}
}

// trayIcon is the orOS mark in reverse colours (gold tile, dark mark),
// so it is not mistaken for the orOS app's own tray icon next to it,
// with a small dot for the state.
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
	reverse(out)
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

// The two colours of the orOS mark (web/icon.png).
var (
	markDark = color.NRGBA{0x1b, 0x1a, 0x18, 0xff}
	markGold = color.NRGBA{0xc8, 0xa9, 0x6e, 0xff}
)

// reverse swaps the mark's two colours, keeping the soft edges between
// them and the transparent corners.
func reverse(img *image.RGBA) {
	lum := func(c color.NRGBA) int { return 299*int(c.R) + 587*int(c.G) + 114*int(c.B) }
	lo, hi := lum(markDark), lum(markGold)
	mix := func(a, b uint8, f int) uint8 { return uint8(int(a) + (int(b)-int(a))*f/1000) }
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
			if c.A == 0 {
				continue
			}
			f := (lum(c) - lo) * 1000 / (hi - lo) // 0 = dark, 1000 = gold
			f = max(0, min(1000, f))
			img.Set(x, y, color.NRGBA{mix(markGold.R, markDark.R, f), mix(markGold.G, markDark.G, f), mix(markGold.B, markDark.B, f), c.A})
		}
	}
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
