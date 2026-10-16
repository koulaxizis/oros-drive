package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/koulaxizis/oros-drive/vault"
)

const pass = "app test passphrase"

// newService: settings in a temp folder, secrets in files there, the
// vault in a local folder that orOS (here: PutFiles) also writes.
func newService(t *testing.T) (*Service, string) {
	dir := t.TempDir()
	t.Setenv("OROS_DRIVE_CONFIG", filepath.Join(dir, "config"))
	t.Setenv("OROS_DRIVE_NO_KEYRING", "1")
	t.Setenv("OROS_DRIVE_STORE_DIR", filepath.Join(dir, "cloud"))
	s, err := NewService("test")
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func TestUnlockAddFolderAndSync(t *testing.T) {
	s, dir := newService(t)
	if err := s.Unlock(pass, false); err != ErrNoVault {
		t.Fatalf("want ErrNoVault, got %v", err)
	}
	if err := s.Unlock(pass, true); err != nil {
		t.Fatal(err)
	}
	if err := s.Unlock("wrong", false); err == nil {
		t.Fatal("a wrong passphrase unlocked")
	}
	docs := filepath.Join(dir, "Docs")
	os.MkdirAll(docs, 0o755)
	os.WriteFile(filepath.Join(docs, "a.txt"), []byte("hello"), 0o644)
	old := time.Now().Add(-time.Minute)
	os.Chtimes(filepath.Join(docs, "a.txt"), old, old)
	p, err := s.AddFolder(docs, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.Remote, "/internal/Drive/") || !strings.HasSuffix(p.Remote, "/Docs") {
		t.Fatal(p.Remote)
	}
	if _, err := s.AddFolder(filepath.Join(docs, "sub"), ""); err == nil {
		t.Fatal("a folder inside a mirrored folder was accepted")
	}
	if _, err := s.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	// orOS (with the passphrase) sees the file.
	v := vault.New(s.Store, vault.NewPassphrase(pass))
	r, err := v.FetchManifest()
	if err != nil {
		t.Fatal(err)
	}
	e, ok := r.Files[p.Remote+"/a.txt"]
	if !ok {
		t.Fatalf("manifest %v", r.Paths())
	}
	if b, _ := v.Download(e); string(b) != "hello" {
		t.Fatal(string(b))
	}
	// A restarted app keeps the key (no passphrase needed).
	s2, err := NewService("test")
	if err != nil {
		t.Fatal(err)
	}
	if !s2.Status().Unlocked {
		t.Fatal("key not kept")
	}
	if err := s2.SetPrefs(Prefs{Device: strPtr("Other")}); err == nil {
		t.Fatal("renamed the computer while folders are mirrored")
	}
}

func strPtr(s string) *string { return &s }

func TestResetVaultIsNotRebuiltWithTheOldKey(t *testing.T) {
	s, dir := newService(t)
	if err := s.Unlock(pass, true); err != nil {
		t.Fatal(err)
	}
	docs := filepath.Join(dir, "Docs")
	os.MkdirAll(docs, 0o755)
	if _, err := s.AddFolder(docs, ""); err != nil {
		t.Fatal(err)
	}
	// orOS's reset wipes /vault and another device starts a new one.
	os.RemoveAll(filepath.Join(dir, "cloud"))
	other := vault.New(vault.NewDirStore(filepath.Join(dir, "cloud")), vault.NewPassphrase("new passphrase"))
	if _, err := other.LoadKeys(true); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(docs, "a.txt"), []byte("x"), 0o644)
	old := time.Now().Add(-time.Minute)
	os.Chtimes(filepath.Join(docs, "a.txt"), old, old)
	s.Store = vault.NewDirStore(filepath.Join(dir, "cloud"))
	s.engine = nil
	if _, err := s.SyncOnce(); err == nil {
		t.Fatal("synced into a vault whose key file changed")
	}
	if st := s.Status(); st.NeedsKey != "reset" {
		t.Fatalf("needsKey %q", st.NeedsKey)
	}
	if _, ok, _ := s.Store.Rev(vault.ManifestKey); ok {
		t.Fatal("a manifest was written with the old key")
	}
}

func uiFor(t *testing.T) (*UI, *Service) {
	s, _ := newService(t)
	u := &UI{S: s, Token: "tok", Port: 47811}
	return u, s
}

func do(u *UI, method, path, body string, cookie bool, header bool, host string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if host == "" {
		host = "127.0.0.1:47811"
	}
	req.Host = host
	if cookie {
		req.AddCookie(&http.Cookie{Name: "orosdrive", Value: "tok"})
	}
	if header {
		req.Header.Set("X-OD", "1")
	}
	w := httptest.NewRecorder()
	u.Handler().ServeHTTP(w, req)
	return w
}

func TestSettingsPageGuards(t *testing.T) {
	u, _ := uiFor(t)
	if w := do(u, "GET", "/api/status", "", false, false, ""); w.Code != 403 {
		t.Fatal("status without cookie:", w.Code)
	}
	if w := do(u, "GET", "/api/status", "", true, false, "evil.example:47811"); w.Code != 403 {
		t.Fatal("rebound host:", w.Code)
	}
	if w := do(u, "POST", "/api/pause", `{"paused":true}`, true, false, ""); w.Code != 403 {
		t.Fatal("POST without the header:", w.Code)
	}
	if w := do(u, "GET", "/", "", false, false, ""); w.Code != 403 {
		t.Fatal("page without cookie:", w.Code)
	}
	w := do(u, "GET", "/?t=tok", "", false, false, "")
	if w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Set-Cookie"), "SameSite=Strict") ||
		!strings.Contains(w.Header().Get("Set-Cookie"), "HttpOnly") {
		t.Fatal("token link:", w.Code, w.Header())
	}
	if w := do(u, "GET", "/?t=nope", "", false, false, ""); strings.Contains(w.Header().Get("Set-Cookie"), "orosdrive") {
		t.Fatal("a wrong token set the cookie")
	}
	w = do(u, "GET", "/", "", true, false, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "app.js") ||
		!strings.Contains(w.Header().Get("Content-Security-Policy"), "default-src 'self'") {
		t.Fatal("page:", w.Code)
	}
	w = do(u, "GET", "/api/status", "", true, false, "")
	var st struct {
		SignedIn bool `json:"signedIn"`
		Unlocked bool `json:"unlocked"`
	}
	json.Unmarshal(w.Body.Bytes(), &st)
	if w.Code != 200 || !st.SignedIn || st.Unlocked {
		t.Fatal("status:", w.Code, w.Body.String())
	}
	w = do(u, "POST", "/api/unlock", `{"passphrase":"p1"}`, true, true, "")
	if !strings.Contains(w.Body.String(), "noVault") {
		t.Fatal("unlock on an empty vault:", w.Body.String())
	}
	w = do(u, "POST", "/api/unlock", `{"passphrase":"p1","create":true}`, true, true, "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = do(u, "POST", "/api/folders/add", `{"path":"relative/path"}`, true, true, "")
	if w.Code != 400 {
		t.Fatal("relative path accepted")
	}
	w = do(u, "GET", "/api/browse?path=", "", true, false, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"roots"`) {
		t.Fatal(w.Body.String())
	}
}

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
