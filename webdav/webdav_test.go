package webdav

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/koulaxizis/oros-drive/vault"
	"github.com/koulaxizis/oros-drive/vault/storetest"
	"github.com/koulaxizis/oros-drive/webdav/davtest"
)

const ncFiles = "/remote.php/dav/files/chris"

func server(t *testing.T) *davtest.Server {
	s := davtest.New("chris", "app pass", ncFiles, "/remote.php/dav/uploads/chris")
	t.Cleanup(s.Close)
	return s
}

func client(t *testing.T, s *davtest.Server) *Client {
	conf, err := NormConf(s.URL+ncFiles, "chris", "")
	if err != nil {
		t.Fatal(err)
	}
	c := New(conf, "app pass")
	c.sleep = func(time.Duration) {}
	return c
}

func TestNormConf(t *testing.T) {
	for _, c := range []struct{ url, dir, want, wantDir string }{
		{"https://cloud.example.com/remote.php/dav/files/chris/", "", "https://cloud.example.com/remote.php/dav/files/chris", "/orOS"},
		{" https://nas.local:5006 ", "a//b/./", "https://nas.local:5006", "/a/b"},
		{"http://127.0.0.1:8080/dav", "/", "http://127.0.0.1:8080/dav", "/orOS"},
		{"http://localhost/dav", "x", "http://localhost/dav", "/x"},
	} {
		got, err := NormConf(c.url, " chris ", c.dir)
		if err != nil || got.URL != c.want || got.Dir != c.wantDir || got.User != "chris" {
			t.Errorf("%q: %+v %v", c.url, got, err)
		}
	}
	for _, bad := range []string{"", "cloud.example.com", "http://cloud.example.com/dav", "ftp://x/",
		"https://u:p@x/dav", "https://x/dav?a=1", "https://x/dav#f"} {
		if _, err := NormConf(bad, "chris", ""); !errors.Is(err, ErrURL) {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := NormConf("https://x/dav", "chris", "a/../b"); !errors.Is(err, ErrURL) {
		t.Error(".. accepted")
	}
	c, _ := NormConf("https://cloud.example.com/remote.php/dav/files/chris", "chris", "")
	if c.CloudID() != "webdav:chris@https://cloud.example.com/remote.php/dav/files/chris/orOS" || c.Account() != "chris@cloud.example.com/orOS" {
		t.Error(c.CloudID(), c.Account())
	}
}

func TestStore(t *testing.T) {
	s := server(t)
	storetest.Run(t, client(t, s))
	// The layout of sync.js: <dir>/vault/<key>.
	if got := s.Files(""); len(got) != 2 || got[0] != ncFiles+"/orOS/vault/manifest.json" || got[1] != ncFiles+"/orOS/vault/objects/cd34" {
		t.Fatal(got)
	}
}

func TestStoreOnPlainWebDAV(t *testing.T) {
	// Not Nextcloud: no chunk folder, big objects in one PUT.
	s := davtest.New("chris", "pw", "/dav")
	defer s.Close()
	conf, _ := NormConf(s.URL+"/dav", "chris", "/Sync/orOS")
	c := New(conf, "pw")
	c.ChunkSize = 1000
	storetest.Run(t, c)
	if s.Chunks != 0 || s.File("/dav/Sync/orOS/vault/manifest.json") == nil {
		t.Fatal(s.Chunks, s.Files(""))
	}
}

func TestStoreOnServerThatCreatesOnIfMatch(t *testing.T) {
	s := server(t)
	s.MatchMissing = true
	storetest.Run(t, client(t, s))
}

func TestChunkedUpload(t *testing.T) {
	s := server(t)
	c := client(t, s)
	c.ChunkSize = 1000
	big := bytes.Repeat([]byte("0123456789"), 350)
	if _, err := c.Put("objects/big", big, vault.Overwrite); err != nil {
		t.Fatal(err)
	}
	if s.Chunks != 4 || !bytes.Equal(s.File(ncFiles+"/orOS/vault/objects/big"), big) {
		t.Fatal(s.Chunks, len(s.File(ncFiles+"/orOS/vault/objects/big")))
	}
	if left := s.Files("/remote.php/dav/uploads/"); len(left) != 0 {
		t.Fatal("pieces left", left)
	}
	// Conditional writes are never chunked: If-Match keeps its meaning.
	if _, err := c.Put("manifest.json", big, vault.CreateOnly); err != nil || s.Chunks != 4 {
		t.Fatal(err, s.Chunks)
	}
	// A server that takes no chunks gets the single PUT.
	s.NoChunks = true
	if _, err := c.Put("objects/big2", big, vault.Overwrite); err != nil || !bytes.Equal(s.File(ncFiles+"/orOS/vault/objects/big2"), big) {
		t.Fatal(err)
	}
}

func TestProbe(t *testing.T) {
	s := server(t)
	c := client(t, s)
	if err := c.Probe(); err != nil {
		t.Fatal(err)
	}
	if left := s.Files(""); len(left) != 0 {
		t.Fatal("probe file left", left)
	}
	if s.File(ncFiles+"/orOS") != nil {
		t.Fatal()
	}
	for name, set := range map[string]func(*davtest.Server){
		"ignores conditions": func(s *davtest.Server) { s.IgnoreConditions = true },
		"weak etags":         func(s *davtest.Server) { s.WeakETags = true },
		"second etags":       func(s *davtest.Server) { s.SecondETags = true },
	} {
		s := server(t)
		set(s)
		if err := client(t, s).Probe(); !errors.Is(err, ErrConditions) {
			t.Errorf("%s: %v", name, err)
		}
	}
	s2 := server(t)
	s2.NoETags = true
	if err := client(t, s2).Probe(); !errors.Is(err, ErrETag) {
		t.Error("no etags:", err)
	}
	bad := client(t, s)
	bad.Pass = "wrong"
	if err := bad.Probe(); !errors.Is(err, ErrAuth) {
		t.Error("wrong password:", err)
	}
	if _, err := Connect(bad.Conf, ""); !errors.Is(err, ErrMissing) {
		t.Error("no password:", err)
	}
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("<html>")) }))
	defer web.Close()
	conf, _ := NormConf(web.URL, "chris", "")
	if err := New(conf, "x").Probe(); !errors.Is(err, ErrNotDAV) {
		t.Error("web page:", err)
	}
	web.Close()
	gone := New(conf, "x")
	gone.sleep = func(time.Duration) {}
	if err := gone.Probe(); !errors.Is(err, ErrUnreachable) {
		t.Error("offline:", err)
	}
}

func TestReadOnly(t *testing.T) {
	s := server(t)
	s.Routes["MKCOL "+ncFiles+"/orOS/"] = func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) }
	if err := client(t, s).Probe(); !errors.Is(err, ErrAuth) {
		t.Error("403 folder:", err)
	}
	s.Routes["MKCOL "+ncFiles+"/orOS/"] = func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(507) }
	if err := client(t, s).Probe(); !errors.Is(err, ErrReadOnly) {
		t.Error("full:", err)
	}
}

func TestOtherDeviceWrites(t *testing.T) {
	s := server(t)
	c := client(t, s)
	if _, err := c.Put("manifest.json", []byte("a"), vault.CreateOnly); err != nil {
		t.Fatal(err)
	}
	_, rev, _ := c.Get("manifest.json")
	s.Write(ncFiles+"/orOS/vault/manifest.json", []byte("orOS in the browser"))
	if _, err := c.Put("manifest.json", []byte("b"), vault.UpdateRev(rev)); !errors.Is(err, vault.ErrConflict) {
		t.Fatal("overwrote the other device:", err)
	}
	if r, _, _ := c.Rev("manifest.json"); r == rev {
		t.Fatal("rev did not move")
	}
}

func TestRevoke(t *testing.T) {
	s := server(t)
	called := 0
	s.Routes["DELETE /ocs/v2.php/core/apppassword"] = func(w http.ResponseWriter, r *http.Request) {
		if u, p, _ := r.BasicAuth(); u == "chris" && p == "app pass" && r.Header.Get("OCS-APIRequest") == "true" {
			called++
		}
	}
	if err := client(t, s).Revoke(); err != nil || called != 1 {
		t.Fatal(err, called)
	}
	conf, _ := NormConf(s.URL+"/dav", "chris", "")
	if err := New(conf, "app pass").Revoke(); err != nil || called != 1 {
		t.Fatal("not Nextcloud:", err, called)
	}
}

func nextcloud(t *testing.T, approveAfter int) (*davtest.Server, *int) {
	s := davtest.New("chris@example.com", "app-pass", "/remote.php/dav/files/chris", "/remote.php/dav/uploads/chris")
	t.Cleanup(s.Close)
	polls, revoked := 0, 0
	s.Routes["POST /index.php/login/v2"] = func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"login": s.URL + "/login/v2/flow/abc",
			"poll":  map[string]string{"token": "tok en", "endpoint": s.URL + "/login/v2/poll"},
		})
	}
	s.Routes["POST /login/v2/poll"] = func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("token") != "tok en" {
			w.WriteHeader(400)
			return
		}
		polls++
		if polls <= approveAfter {
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"server": s.URL, "loginName": "chris@example.com", "appPassword": "app-pass"})
	}
	s.Routes["PROPFIND /remote.php/dav/"] = func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(207)
		w.Write([]byte(`<d:multistatus xmlns:d="DAV:"><d:response><d:href>/remote.php/dav/</d:href><d:propstat><d:prop>` +
			`<d:current-user-principal><d:href>/remote.php/dav/principals/users/chris/</d:href></d:current-user-principal>` +
			`</d:prop></d:propstat></d:response></d:multistatus>`))
	}
	s.Routes["DELETE /ocs/v2.php/core/apppassword"] = func(w http.ResponseWriter, r *http.Request) { revoked++ }
	return s, &revoked
}

func TestNextcloudLogin(t *testing.T) {
	PollEvery = time.Millisecond
	s, revoked := nextcloud(t, 2)
	l, err := StartNextcloud(t.Context(), s.URL+"/index.php/apps/files/", "Docs/orOS")
	if err != nil {
		t.Fatal(err)
	}
	if l.URL != s.URL+"/login/v2/flow/abc" {
		t.Fatal(l.URL)
	}
	c, err := l.Wait(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if c.Conf.URL != s.URL+"/remote.php/dav/files/chris" || c.Conf.User != "chris@example.com" || c.Conf.Dir != "/Docs/orOS" || c.Pass != "app-pass" || *revoked != 0 {
		t.Fatalf("%+v revoked %d", c.Conf, *revoked)
	}
	if _, err := c.Put("key.json", []byte("{}"), vault.CreateOnly); err != nil {
		t.Fatal(err)
	}

	// A server that fails the check: the new app password is deleted.
	s2, revoked2 := nextcloud(t, 0)
	s2.IgnoreConditions = true
	l2, err := StartNextcloud(t.Context(), s2.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l2.Wait(t.Context()); !errors.Is(err, ErrConditions) || *revoked2 != 1 {
		t.Fatal(err, *revoked2)
	}
}

func TestNextcloudRefusesOddServers(t *testing.T) {
	s := davtest.New("u", "p")
	defer s.Close()
	s.Routes["POST /index.php/login/v2"] = http.NotFound
	if _, err := StartNextcloud(t.Context(), s.URL, ""); !errors.Is(err, ErrNotNextcloud) {
		t.Error("no login endpoint:", err)
	}
	for name, body := range map[string]string{
		"poll elsewhere": `{"login":"https://x/login","poll":{"token":"t","endpoint":"https://evil.example/poll"}}`,
		"no token":       `{"login":"https://x/login","poll":{"endpoint":"` + s.URL + `/poll"}}`,
		"odd login":      `{"login":"javascript:alert(1)","poll":{"token":"t","endpoint":"` + s.URL + `/poll"}}`,
		"not json":       `<html>`,
	} {
		s.Routes["POST /index.php/login/v2"] = func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }
		if _, err := StartNextcloud(t.Context(), s.URL, ""); !errors.Is(err, ErrNotNextcloud) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := StartNextcloud(t.Context(), "http://cloud.example.com", ""); !errors.Is(err, ErrURL) {
		t.Error("plain http:", err)
	}
}

func TestHrefs(t *testing.T) {
	// Absolute hrefs, other prefixes, encoded names.
	for href, want := range map[string]string{
		"https://h/remote.php/dav/files/c/orOS/vault/a%20b/": "/remote.php/dav/files/c/orOS/vault/a b",
		"/dav/%CE%91/x": "/dav/Α/x",
		"/dav/x?y":      "/dav/x",
	} {
		if got := pathOf(href); got != want {
			t.Errorf("%q: %q", href, got)
		}
	}
	if !strings.HasPrefix(etag("abc"), `"`) || etag(`W/"x"`) != `W/"x"` || etag(` "x" `) != `"x"` || etag("") != "" {
		t.Error("etag")
	}
}
