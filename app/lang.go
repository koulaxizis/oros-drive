package app

import (
	"os"
	"strings"
)

// systemGreek: the user's system language is Greek (LANG/LC_* on
// Linux; on Windows the user default locale, see lang_windows.go).
func systemGreek() bool {
	if g, ok := windowsGreek(); ok {
		return g
	}
	for _, v := range []string{"LC_ALL", "LC_MESSAGES", "LANG", "LANGUAGE"} {
		if s := os.Getenv(v); s != "" {
			return strings.HasPrefix(strings.ToLower(s), "el")
		}
	}
	return false
}
