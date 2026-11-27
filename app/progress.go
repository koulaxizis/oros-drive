package app

import (
	"fmt"
	"strings"

	"github.com/koulaxizis/oros-drive/mirror"
)

// SyncText says what a running pass is doing, for the tray: "Checking
// files…" until the uploads start, then "Uploading 120 of 22600
// (1.2 GB of 43 GB)". The settings page says the same (app.js syncText).
func SyncText(p *mirror.Progress, greek bool) string {
	if p == nil || p.Files == 0 {
		if greek {
			return "Ελέγχει τα αρχεία…"
		}
		return "Checking files…"
	}
	if greek {
		return fmt.Sprintf("Ανεβαίνουν %d από %d (%s από %s)", p.FilesDone, p.Files,
			strings.ReplaceAll(sizeText(p.BytesDone), ".", ","), strings.ReplaceAll(sizeText(p.Bytes), ".", ","))
	}
	return fmt.Sprintf("Uploading %d of %d (%s of %s)", p.FilesDone, p.Files, sizeText(p.BytesDone), sizeText(p.Bytes))
}

// sizeText: 512 B, 3.4 MB, 43 GB.
func sizeText(n int64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	f, i := float64(n), 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	switch {
	case i == 0:
		return fmt.Sprintf("%d B", n)
	case f < 10:
		return fmt.Sprintf("%.1f %s", f, units[i])
	}
	return fmt.Sprintf("%.0f %s", f, units[i])
}
