package dropbox

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/koulaxizis/oros-drive/vault"
)

// fakeDropbox is just enough of the Dropbox API for the client: files
// with revisions, conditional uploads, upload sessions, metadata,
// paginated listings, delete, tokens with expiry and rotation.
type fakeDropbox struct {
	mu        sync.Mutex
	files     map[string]fakeFile // path_lower -> file
	rev       int
	sessions  map[string][]byte
	access    string // the one valid access token
	refresh   string
	fail401   int // answer 401 to the next n API calls
	fail429   int // answer 429 to the next n API calls
	refreshes int
	calls     []string
	argHeads  []string
}

type fakeFile struct {
	data []byte
	rev  string
}

func newFake() (*fakeDropbox, *Client, func()) {
	f := &fakeDropbox{files: map[string]fakeFile{}, sessions: map[string][]byte{}, access: "acc1", refresh: "ref1"}
	srv := httptest.NewServer(f)
	c := New(Tokens{Access: "acc1", Refresh: "ref1", Expiry: time.Now().Add(time.Hour)})
	c.APIBase, c.ContentBase, c.TokenURL = srv.URL+"/2/", srv.URL+"/2/", srv.URL+"/oauth2/token"
	c.sleep = func(time.Duration) {}
	return f, c, srv.Close
}

func jsonOut(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func (f *fakeDropbox) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path == "/oauth2/token" {
		r.ParseForm()
		switch r.Form.Get("grant_type") {
		case "refresh_token":
			if r.Form.Get("refresh_token") != f.refresh {
				jsonOut(w, 400, map[string]string{"error": "invalid_grant"})
				return
			}
			f.refreshes++
			f.access = fmt.Sprintf("acc%d", f.refreshes+1)
			jsonOut(w, 200, map[string]any{"access_token": f.access, "expires_in": 14400})
		case "authorization_code":
			if r.Form.Get("code") != "good-code" || r.Form.Get("code_verifier") == "" {
				jsonOut(w, 400, map[string]string{"error": "invalid_grant"})
				return
			}
			jsonOut(w, 200, map[string]any{"access_token": f.access, "refresh_token": f.refresh, "expires_in": 14400})
		}
		return
	}
	ep := strings.TrimPrefix(r.URL.Path, "/2/")
	f.calls = append(f.calls, ep)
	if f.fail429 > 0 {
		f.fail429--
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(429)
		return
	}
	if f.fail401 > 0 || r.Header.Get("Authorization") != "Bearer "+f.access {
		if f.fail401 > 0 {
			f.fail401--
			f.access = "rotated"
		}
		jsonOut(w, 401, map[string]string{"error_summary": "expired_access_token/"})
		return
	}
	var arg map[string]any
	if h := r.Header.Get("Dropbox-API-Arg"); h != "" {
		f.argHeads = append(f.argHeads, h)
		json.Unmarshal([]byte(h), &arg)
	} else if r.ContentLength != 0 {
		json.NewDecoder(r.Body).Decode(&arg)
	}
	body, _ := io.ReadAll(r.Body)
	path, _ := arg["path"].(string)
	lp := strings.ToLower(path)
	switch ep {
	case "files/download":
		file, ok := f.files[lp]
		if !ok {
			jsonOut(w, 409, map[string]string{"error_summary": "path/not_found/.."})
			return
		}
		w.Header().Set("Dropbox-API-Result", fmt.Sprintf(`{"rev":%q}`, file.rev))
		w.Write(file.data)
	case "files/upload":
		f.commit(w, arg, body)
	case "files/upload_session/start":
		id := fmt.Sprintf("s%d", len(f.sessions)+1)
		f.sessions[id] = body
		jsonOut(w, 200, map[string]string{"session_id": id})
	case "files/upload_session/append_v2":
		cur := arg["cursor"].(map[string]any)
		id := cur["session_id"].(string)
		if int(cur["offset"].(float64)) != len(f.sessions[id]) {
			jsonOut(w, 409, map[string]string{"error_summary": "incorrect_offset/"})
			return
		}
		f.sessions[id] = append(f.sessions[id], body...)
		jsonOut(w, 200, nil)
	case "files/upload_session/finish":
		cur := arg["cursor"].(map[string]any)
		data := f.sessions[cur["session_id"].(string)]
		if int(cur["offset"].(float64)) != len(data) {
			jsonOut(w, 409, map[string]string{"error_summary": "lookup_failed/incorrect_offset/"})
			return
		}
		f.commit(w, arg["commit"].(map[string]any), data)
	case "files/get_metadata":
		file, ok := f.files[lp]
		if !ok {
			jsonOut(w, 409, map[string]string{"error_summary": "path/not_found/"})
			return
		}
		jsonOut(w, 200, map[string]string{".tag": "file", "rev": file.rev})
	case "files/delete_v2":
		if _, ok := f.files[lp]; !ok {
			jsonOut(w, 409, map[string]string{"error_summary": "path_lookup/not_found/"})
			return
		}
		delete(f.files, lp)
		jsonOut(w, 200, nil)
	case "files/list_folder", "files/list_folder/continue":
		var keys []string
		for k := range f.files {
			if strings.HasPrefix(k, "/vault/") {
				keys = append(keys, k)
			}
		}
		if len(keys) == 0 {
			jsonOut(w, 409, map[string]string{"error_summary": "path/not_found/"})
			return
		}
		sort.Strings(keys)
		start := 0
		if c, ok := arg["cursor"].(string); ok {
			fmt.Sscan(c, &start)
		}
		end := min(start+2, len(keys)) // tiny pages: pagination is exercised
		var entries []map[string]any
		if start == 0 {
			entries = append(entries, map[string]any{".tag": "folder", "path_lower": "/vault/objects"})
		}
		for _, k := range keys[start:end] {
			entries = append(entries, map[string]any{".tag": "file", "path_lower": k,
				"size": len(f.files[k].data), "server_modified": "2026-09-01T10:20:30Z"})
		}
		jsonOut(w, 200, map[string]any{"entries": entries, "has_more": end < len(keys), "cursor": fmt.Sprint(end)})
	case "users/get_current_account":
		jsonOut(w, 200, map[string]any{"email": "user@example.com", "name": map[string]string{"display_name": "Test User"}})
	case "auth/token/revoke":
		jsonOut(w, 200, nil)
	default:
		jsonOut(w, 400, map[string]string{"error_summary": "unknown endpoint " + ep})
	}
}

func (f *fakeDropbox) commit(w http.ResponseWriter, c map[string]any, data []byte) {
	path := strings.ToLower(c["path"].(string))
	cur, exists := f.files[path]
	conflict := false
	switch m := c["mode"].(type) {
	case string:
		conflict = m == "add" && exists
	case map[string]any:
		conflict = !exists || cur.rev != m["update"]
	}
	if conflict {
		jsonOut(w, 409, map[string]string{"error_summary": "path/conflict/file/.."})
		return
	}
	f.rev++
	rev := fmt.Sprintf("%09x", f.rev)
	f.files[path] = fakeFile{data: append([]byte{}, data...), rev: rev}
	jsonOut(w, 200, map[string]string{"rev": rev, "path_lower": path})
}

func TestGetPutModes(t *testing.T) {
	f, c, done := newFake()
	defer done()
	if b, rev, err := c.Get("manifest.json"); b != nil || rev != "" || err != nil {
		t.Fatalf("missing key: %v %q %v", b, rev, err)
	}
	r1, err := c.Put("manifest.json", []byte("one"), vault.CreateOnly)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Put("manifest.json", []byte("two"), vault.CreateOnly); !errors.Is(err, vault.ErrConflict) {
		t.Fatalf("add over an existing file: %v", err)
	}
	if _, err := c.Put("manifest.json", []byte("two"), vault.UpdateRev("stale")); !errors.Is(err, vault.ErrConflict) {
		t.Fatalf("update on a stale rev: %v", err)
	}
	r2, err := c.Put("manifest.json", []byte("two"), vault.UpdateRev(r1))
	if err != nil {
		t.Fatal(err)
	}
	b, rev, err := c.Get("manifest.json")
	if err != nil || string(b) != "two" || rev != r2 {
		t.Fatalf("%q %q %v", b, rev, err)
	}
	if got, ok, _ := c.Rev("manifest.json"); !ok || got != r2 {
		t.Fatalf("Rev = %q %v", got, ok)
	}
	if _, ok, err := c.Rev("nope"); ok || err != nil {
		t.Fatal("missing key has a rev")
	}
	if _, ok := f.files["/vault/manifest.json"]; !ok {
		t.Fatal("not stored under /vault")
	}
	if err := c.Delete("manifest.json"); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete("manifest.json"); err != nil {
		t.Fatal("deleting a gone key must succeed")
	}
}

func TestUploadSession(t *testing.T) {
	f, c, done := newFake()
	defer done()
	c.SingleShotMax, c.SessionChunk = 10, 4
	data := []byte("0123456789abcdefghijklmnopq") // 27 bytes: start + 6 appends
	if _, err := c.Put("objects/big", data, vault.Overwrite); err != nil {
		t.Fatal(err)
	}
	got, _, _ := c.Get("objects/big")
	if !bytes.Equal(got, data) {
		t.Fatalf("got %q", got)
	}
	r, _, _ := c.Rev("objects/big")
	if _, err := c.Put("objects/big", data, vault.UpdateRev("stale")); !errors.Is(err, vault.ErrConflict) {
		t.Fatalf("session finish on a stale rev: %v", err)
	}
	if _, err := c.Put("objects/big", data, vault.UpdateRev(r)); err != nil {
		t.Fatal(err)
	}
	if len(f.sessions) != 3 { // the refused stale-rev upload used one too
		t.Fatalf("%d sessions", len(f.sessions))
	}
}

func TestRefreshOn401AndExpiry(t *testing.T) {
	f, c, done := newFake()
	defer done()
	var saved []Tokens
	c.OnTokens = func(t Tokens) { saved = append(saved, t) }
	f.fail401 = 1
	if _, err := c.Put("k", []byte("x"), vault.Overwrite); err != nil {
		t.Fatal(err)
	}
	if f.refreshes != 1 || len(saved) != 1 || saved[0].Refresh != "ref1" {
		t.Fatalf("refreshes=%d saved=%+v", f.refreshes, saved)
	}
	// An expired token is refreshed before the call.
	c.tokens.Expiry = time.Now().Add(-time.Minute)
	if _, _, err := c.Get("k"); err != nil {
		t.Fatal(err)
	}
	if f.refreshes != 2 {
		t.Fatalf("refreshes=%d", f.refreshes)
	}
}

func TestNotConnected(t *testing.T) {
	_, c, done := newFake()
	defer done()
	c.tokens = Tokens{}
	if _, _, err := c.Get("k"); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("got %v", err)
	}
}

func TestRetryOn429(t *testing.T) {
	f, c, done := newFake()
	defer done()
	f.fail429 = 2
	if _, err := c.Put("k", []byte("x"), vault.Overwrite); err != nil {
		t.Fatal(err)
	}
	f.fail429 = maxRetries + 1
	if _, err := c.Put("k", []byte("x"), vault.Overwrite); err == nil {
		t.Fatal("endless 429 must fail")
	}
}

func TestListPaginatesAndSkipsFolders(t *testing.T) {
	_, c, done := newFake()
	defer done()
	if keys, err := c.List(""); err != nil || len(keys) != 0 {
		t.Fatalf("no /vault yet: %v %v", keys, err)
	}
	for _, k := range []string{"key.json", "manifest.json", "objects/aa", "objects/bb", "objects/cc"} {
		c.Put(k, []byte(k), vault.Overwrite)
	}
	all, err := c.List("")
	if err != nil || len(all) != 5 {
		t.Fatalf("%v %v", all, err)
	}
	objs, _ := c.List("objects/")
	if strings.Join(objs, ",") != "objects/aa,objects/bb,objects/cc" {
		t.Fatalf("%v", objs)
	}
	infos, err := c.ListInfo("objects/")
	if err != nil || len(infos) != 3 || infos[0].Key != "objects/aa" || infos[0].Size != 10 ||
		!infos[0].Modified.Equal(time.Date(2026, 9, 1, 10, 20, 30, 0, time.UTC)) {
		t.Fatalf("%+v %v", infos, err)
	}
}

func TestAPIArgIsASCII(t *testing.T) {
	f, c, done := newFake()
	defer done()
	if _, err := c.Put("Έγγραφα/😀.txt", []byte("x"), vault.Overwrite); err != nil {
		t.Fatal(err)
	}
	h := f.argHeads[len(f.argHeads)-1]
	for _, r := range h {
		if r > 0x7e {
			t.Fatalf("non-ASCII in Dropbox-API-Arg: %s", h)
		}
	}
	if got, _, _ := c.Get("Έγγραφα/😀.txt"); string(got) != "x" {
		t.Fatal("escaped path did not round-trip")
	}
}

func TestOAuth(t *testing.T) {
	_, c, done := newFake()
	defer done()
	p := NewPKCE()
	u, _ := url.Parse(c.AuthorizeURL(p, LoopbackRedirect))
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("token_access_type") != "offline" ||
		q.Get("client_id") != AppKey || q.Get("redirect_uri") != LoopbackRedirect || q.Get("state") != p.State {
		t.Fatalf("authorize url: %s", u)
	}
	if strings.Contains(c.AuthorizeURL(p, ""), "redirect_uri") {
		t.Fatal("paste flow must not send a redirect")
	}
	c.tokens = Tokens{}
	if _, err := c.Exchange("bad-code", p, ""); err == nil {
		t.Fatal("bad code accepted")
	}
	tok, err := c.Exchange("good-code", p, "")
	if err != nil || tok.Refresh != "ref1" {
		t.Fatalf("%+v %v", tok, err)
	}
	if a, err := c.CurrentAccount(); err != nil || a.Email != "user@example.com" {
		t.Fatalf("%+v %v", a, err)
	}
}

// The vault on top of the Dropbox client: what the CLI does.
func TestVaultOverDropbox(t *testing.T) {
	_, c, done := newFake()
	defer done()
	v := vault.New(c, vault.NewPassphrase("p"))
	files := map[string]vault.File{
		"/internal/Έγγραφα/a.txt": {Data: []byte("alpha")},
		"/internal/b.bin":         {Data: bytes.Repeat([]byte{7}, 1000)},
	}
	if err := v.PutFiles(files, nil); err != nil {
		t.Fatal(err)
	}
	other := vault.New(c, vault.NewPassphrase("p"))
	r, err := other.FetchManifest()
	if err != nil {
		t.Fatal(err)
	}
	for p, f := range files {
		got, err := other.Download(r.Files[p])
		if err != nil || !bytes.Equal(got, f.Data) {
			t.Fatalf("%s: %v", p, err)
		}
	}
	keys, _ := c.List("")
	for _, k := range keys {
		if strings.Contains(k, "a.txt") || strings.Contains(k, "b.bin") {
			t.Fatalf("a file name reached Dropbox: %s", k)
		}
	}
}

func TestCodeHandler(t *testing.T) {
	p := NewPKCE()
	done := make(chan codeResult, 1)
	srv := httptest.NewServer(codeHandler(p, done))
	defer srv.Close()
	get := func(q string) int {
		res, err := http.Get(srv.URL + q)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if get("/favicon.ico") != 404 || len(done) != 0 {
		t.Fatal("unrelated request taken as an answer")
	}
	get("/?code=c1&state=" + p.State)
	if r := <-done; r.code != "c1" || r.err != nil {
		t.Fatalf("%+v", r)
	}
	get("/?code=c2&state=wrong")
	if r := <-done; r.err == nil {
		t.Fatal("wrong state accepted")
	}
	get("/?error=access_denied&state=" + p.State)
	if r := <-done; r.err == nil || !strings.Contains(r.err.Error(), "access_denied") {
		t.Fatalf("%+v", r)
	}
}
