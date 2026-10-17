package mirror

import (
	"path"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"
)

// tmpPrefix marks the engine's own half-written downloads; they are
// never uploaded and are removed when found.
const tmpPrefix = ".orosdrive-"

// Ignored reports names that are never mirrored: editor lock and swap
// files, partial downloads and the OS's folder clutter. They change
// all the time and mean nothing on another device.
func Ignored(name string) bool {
	l := strings.ToLower(name)
	switch l {
	case "thumbs.db", "desktop.ini", ".ds_store", ".directory", "$recycle.bin", ".trash", "system volume information":
		return true
	}
	if strings.HasPrefix(l, tmpPrefix) || strings.HasPrefix(l, "~$") || strings.HasPrefix(l, ".~lock.") ||
		strings.HasPrefix(l, ".trash-") || strings.HasPrefix(l, "._") {
		return true
	}
	for _, suf := range []string{".tmp", ".temp", ".part", ".partial", ".crdownload", ".download", ".swp", ".swx", "~"} {
		if strings.HasSuffix(l, suf) {
			return true
		}
	}
	return false
}

var winReserved = map[string]bool{"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true, "com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true, "lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true}

// LocalNameOK reports whether a name that came from the vault can be
// a file or folder name on this computer. A name orOS accepts but
// Windows does not ("a:b", "notes.") is left in the cloud and listed
// as skipped, never renamed behind the user's back.
func LocalNameOK(name string, goos string) bool {
	if name == "" || name == "." || name == ".." || !utf8.ValidString(name) {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == '/' || r == 0x7f {
			return false
		}
	}
	if goos == "windows" {
		if strings.ContainsAny(name, `<>:"\|?*`) || strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
			return false
		}
		base := strings.ToLower(name)
		if i := strings.IndexByte(base, '.'); i >= 0 {
			base = base[:i]
		}
		if winReserved[strings.TrimRight(base, " ")] {
			return false
		}
	}
	return true
}

// RelOK checks every segment of a relative path ("a/b.txt").
func RelOK(rel string, goos string) bool {
	for _, seg := range strings.Split(rel, "/") {
		if !LocalNameOK(seg, goos) {
			return false
		}
	}
	return true
}

// caseFolds: file systems where "A.txt" and "a.txt" are one file.
func caseFolds(goos string) bool { return goos == "windows" || goos == "darwin" }

var goos = runtime.GOOS

// ConflictName = vault.js conflictName: "name (conflict YYYYMMDD-HHMMSS).ext",
// with "-n" added when that name is taken.
func ConflictName(rel string, t time.Time, n int) string {
	dir, name := path.Split(rel)
	base, ext := name, ""
	if dot := strings.LastIndexByte(name, '.'); dot > 0 {
		base, ext = name[:dot], name[dot:]
	}
	stamp := t.Format("20060102-150405")
	if n > 0 {
		stamp += "-" + itoa(n)
	}
	return dir + base + " (conflict " + stamp + ")" + ext
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
