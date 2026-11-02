package app

import (
	"net/url"
	"strings"
)

// webLink reports whether target is an http(s) link. Only such links go
// to the browser; anything else is opened as a folder.
func webLink(target string) bool {
	u, err := url.Parse(target)
	if err != nil {
		return false
	}
	s := strings.ToLower(u.Scheme)
	return (s == "http" || s == "https") && u.Host != ""
}
