// Package davtest is a small in-memory WebDAV server for tests: the
// parts orOS uses (PROPFIND depth 0/1, MKCOL, GET, HEAD, PUT with
// If-Match / If-None-Match, DELETE, Nextcloud's chunked upload MOVE),
// with switches for the server faults Probe must refuse. Both the Go
// client and the real sync.js (interop) talk to it.
package davtest

import (
	"bytes"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

type file struct {
	data []byte
	etag string
	mod  time.Time
}

// Server is the fake. Set the switches before the first request.
type Server struct {
	*httptest.Server
	User, Pass string

	IgnoreConditions bool // accepts every If-Match / If-None-Match
	MatchMissing     bool // If-Match on a missing file creates it (WsgiDAV)
	WeakETags        bool // W/"…"
	NoETags          bool
	SecondETags      bool // size + whole second: same ETag for a quick rewrite
	NoChunks         bool // MKCOL under /uploads is refused (not Nextcloud)

	mu     sync.Mutex
	files  map[string]*file
	dirs   map[string]bool
	n      int
	Counts map[string]int // requests by method
	Chunks int            // chunk PUTs received
	// Hook runs before each request (tests change files in between).
	Hook func(r *http.Request)
	// Routes answer "METHOD /path" before anything else (Nextcloud's
	// login calls, which need no password).
	Routes map[string]http.HandlerFunc
}

// New starts a server with the folders in dirs (and their parents).
func New(user, pass string, dirs ...string) *Server {
	s := &Server{User: user, Pass: pass, files: map[string]*file{}, dirs: map[string]bool{"/": true},
		Counts: map[string]int{}, Routes: map[string]http.HandlerFunc{}}
	for _, d := range dirs {
		s.Mkdir(d)
	}
	s.Server = httptest.NewServer(s)
	return s
}

// Mkdir creates a folder and its parents.
func (s *Server) Mkdir(d string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for d = clean(d); d != "/"; d = path.Dir(d) {
		s.dirs[d] = true
	}
}

func clean(p string) string { return path.Clean("/" + p) }

// File returns a stored file (nil when missing).
func (s *Server) File(p string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f := s.files[clean(p)]; f != nil {
		return append([]byte{}, f.data...)
	}
	return nil
}

// Files lists every stored file path under prefix.
func (s *Server) Files(prefix string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for p := range s.files {
		if strings.HasPrefix(p, prefix) {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// Write stores a file directly (another device), creating folders.
func (s *Server) Write(p string, data []byte) {
	s.Mkdir(path.Dir(clean(p)))
	s.mu.Lock()
	defer s.mu.Unlock()
	s.store(clean(p), data)
}

func (s *Server) store(p string, data []byte) *file {
	s.n++
	now := time.Now()
	f := &file{data: append([]byte{}, data...), mod: now}
	switch {
	case s.SecondETags:
		f.etag = fmt.Sprintf(`"%x-%x"`, len(data), now.Unix())
	case s.WeakETags:
		f.etag = fmt.Sprintf(`W/"e%d"`, s.n)
	default:
		f.etag = fmt.Sprintf(`"e%d"`, s.n)
	}
	s.files[p] = f
	return f
}

func (s *Server) setETag(w http.ResponseWriter, f *file) {
	if !s.NoETags {
		w.Header().Set("ETag", f.etag)
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.Hook != nil {
		s.Hook(r)
	}
	if h := s.Routes[r.Method+" "+r.URL.Path]; h != nil {
		h(w, r)
		return
	}
	if u, p, ok := r.BasicAuth(); !ok || u != s.User || p != s.Pass {
		w.WriteHeader(401)
		return
	}
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Counts[r.Method]++
	p := clean(r.URL.Path)
	f := s.files[p]
	switch r.Method {
	case "GET", "HEAD":
		if f == nil {
			w.WriteHeader(404)
			return
		}
		s.setETag(w, f)
		w.Header().Set("Content-Length", fmt.Sprint(len(f.data)))
		w.WriteHeader(200)
		if r.Method == "GET" {
			w.Write(f.data)
		}
	case "PUT":
		if strings.Contains(p, "/uploads/") {
			s.Chunks++
		}
		if !s.dirs[path.Dir(p)] || s.dirs[p] {
			w.WriteHeader(409)
			return
		}
		if !s.IgnoreConditions {
			if r.Header.Get("If-None-Match") == "*" && f != nil {
				w.WriteHeader(412)
				return
			}
			if m := r.Header.Get("If-Match"); m != "" && ((f == nil && !s.MatchMissing) || (f != nil && f.etag != m) || strings.HasPrefix(m, "W/")) {
				w.WriteHeader(412)
				return
			}
		}
		code := 204
		if f == nil {
			code = 201
		}
		s.setETag(w, s.store(p, body))
		w.WriteHeader(code)
	case "DELETE":
		if f != nil {
			delete(s.files, p)
			w.WriteHeader(204)
			return
		}
		if !s.dirs[p] || p == "/" {
			w.WriteHeader(404)
			return
		}
		for k := range s.files {
			if strings.HasPrefix(k, p+"/") {
				delete(s.files, k)
			}
		}
		for k := range s.dirs {
			if k == p || strings.HasPrefix(k, p+"/") {
				delete(s.dirs, k)
			}
		}
		w.WriteHeader(204)
	case "MKCOL":
		if s.NoChunks && strings.Contains(p, "/uploads/") {
			w.WriteHeader(409)
			return
		}
		if s.dirs[p] || f != nil {
			w.WriteHeader(405)
			return
		}
		if !s.dirs[path.Dir(p)] {
			w.WriteHeader(409)
			return
		}
		s.dirs[p] = true
		w.WriteHeader(201)
	case "MOVE":
		s.move(w, r, p)
	case "PROPFIND":
		s.propfind(w, r, p)
	default:
		w.WriteHeader(405)
	}
}

// move handles Nextcloud's "MOVE <upload>/.file": the pieces, in name
// order, become the Destination.
func (s *Server) move(w http.ResponseWriter, r *http.Request, p string) {
	if path.Base(p) != ".file" || !s.dirs[path.Dir(p)] {
		w.WriteHeader(404)
		return
	}
	d, err := url.Parse(r.Header.Get("Destination"))
	if err != nil {
		w.WriteHeader(400)
		return
	}
	dest := clean(d.Path)
	if !s.dirs[path.Dir(dest)] {
		w.WriteHeader(409)
		return
	}
	dir := path.Dir(p)
	var names []string
	for k := range s.files {
		if path.Dir(k) == dir {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	var all bytes.Buffer
	for _, k := range names {
		all.Write(s.files[k].data)
		delete(s.files, k)
	}
	delete(s.dirs, dir)
	s.setETag(w, s.store(dest, all.Bytes()))
	w.WriteHeader(201)
}

func href(p string, dir bool) string {
	parts := strings.Split(p, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	h := strings.Join(parts, "/")
	if dir && !strings.HasSuffix(h, "/") {
		h += "/"
	}
	return h
}

func (s *Server) propfind(w http.ResponseWriter, r *http.Request, p string) {
	f := s.files[p]
	if f == nil && !s.dirs[p] {
		w.WriteHeader(404)
		return
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?><d:multistatus xmlns:d="DAV:">`)
	one := func(q string) {
		b.WriteString("<d:response><d:href>" + html.EscapeString(href(q, s.dirs[q])) + "</d:href><d:propstat><d:prop>")
		if s.dirs[q] {
			b.WriteString("<d:resourcetype><d:collection/></d:resourcetype>")
		} else {
			g := s.files[q]
			b.WriteString("<d:resourcetype/>")
			fmt.Fprintf(&b, "<d:getcontentlength>%d</d:getcontentlength>", len(g.data))
			b.WriteString("<d:getlastmodified>" + g.mod.UTC().Format(http.TimeFormat) + "</d:getlastmodified>")
			if !s.NoETags {
				b.WriteString("<d:getetag>" + html.EscapeString(g.etag) + "</d:getetag>")
			}
		}
		b.WriteString("</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>")
	}
	one(p)
	if r.Header.Get("Depth") != "0" && s.dirs[p] {
		var kids []string
		for k := range s.files {
			if path.Dir(k) == p && k != p {
				kids = append(kids, k)
			}
		}
		for k := range s.dirs {
			if k != "/" && path.Dir(k) == p && k != p {
				kids = append(kids, k)
			}
		}
		sort.Strings(kids)
		for _, k := range kids {
			one(k)
		}
	}
	b.WriteString("</d:multistatus>")
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(207)
	io.WriteString(w, b.String())
}

// Touch moves a file's modification time (clean-up tests).
func (s *Server) Touch(p string, t time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f := s.files[clean(p)]; f != nil {
		f.mod = t
	}
}
