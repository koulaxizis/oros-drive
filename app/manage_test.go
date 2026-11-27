package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/koulaxizis/oros-drive/mirror"
)

// fakeOros is an "orOS program" file for ManagedBy; on Linux the login
// start goes to XDG_CONFIG_HOME in the temp folder.
func fakeOros(t *testing.T, dir string) string {
	if runtime.GOOS == "linux" {
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg"))
	}
	p := filepath.Join(dir, "orOS.exe")
	if err := os.WriteFile(p, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestManagedHandsOverAndBack(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("writes the real HKCU Run value; the setup test covers Windows")
	}
	s, dir := newService(t)
	oros := fakeOros(t, dir)
	if runtime.GOOS == "linux" {
		if err := SetAutostart(true); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetManaged(filepath.Join(dir, "nope.exe")); err != ErrManagedBy {
		t.Fatalf("a missing program: %v", err)
	}
	if err := s.SetManaged(oros); err != nil {
		t.Fatal(err)
	}
	st := s.Status()
	if !st.Managed || st.Settings.ManagedBy != oros {
		t.Fatalf("not managed: %+v", st.Settings)
	}
	if AutostartOn() {
		t.Fatal("still starts by itself at login while orOS starts it")
	}
	// The page cannot turn the own start or icon back on meanwhile.
	on := true
	if err := s.SetPrefs(Prefs{Autostart: &on, Tray: &on}); err != nil {
		t.Fatal(err)
	}
	if AutostartOn() {
		t.Fatal("the settings page turned the own start on under orOS")
	}
	if SummaryOf(s.Status()).Managed != true {
		t.Fatal("summary does not say managed")
	}
	if err := s.SetManaged(""); err != nil {
		t.Fatal(err)
	}
	if s.Status().Managed {
		t.Fatal("still managed")
	}
	if runtime.GOOS == "linux" && !AutostartOn() {
		t.Fatal("the own start at login was not put back")
	}
}

func TestOrphanedLetsGo(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("writes the real HKCU Run value")
	}
	s, dir := newService(t)
	oros := fakeOros(t, dir)
	if err := s.SetManaged(oros); err != nil {
		t.Fatal(err)
	}
	if s.LetGoIfOrphaned() {
		t.Fatal("let go while orOS is there")
	}
	os.Remove(oros) // orOS uninstalled without letting go
	if s.Status().Managed {
		t.Fatal("a gone orOS still counts as managing")
	}
	if !s.LetGoIfOrphaned() {
		t.Fatal("did not let go")
	}
	if s.Status().Settings.ManagedBy != "" {
		t.Fatal("ManagedBy kept")
	}
}

func TestHandoffState(t *testing.T) {
	st := HandoffState(47812, "abcdefghijklmnopqrstuv")
	if HandoffPort(st) != 47812 {
		t.Fatal(st)
	}
	for _, bad := range []string{"", "abc", "orosdrive-47800-abcdefghijklmnopqrstuv", "orosdrive-80-abcdefghijklmnopqrstuv",
		"orosdrive-47812-short", "orosdrive-47812-abcdefghijklmnopqrstuv&x=1", "xorosdrive-47812-abcdefghijklmnopqrstuv"} {
		if HandoffPort(bad) != 0 {
			t.Errorf("%q gave a port", bad)
		}
	}
}

func TestHandoffTakesOnlyItsOwnState(t *testing.T) {
	s, _ := newService(t)
	h := &handoff{state: HandoffState(47811, "abcdefghijklmnopqrstuv"), done: make(chan codeResult, 1)}
	s.handoff = h
	ui := &UI{S: s, Token: "tok", Port: 47811}
	get := func(q string) int {
		req := httptest.NewRequest("GET", "http://127.0.0.1:47811/oauth/done?"+q, nil)
		w := httptest.NewRecorder()
		ui.Handler().ServeHTTP(w, req)
		return w.Code
	}
	if c := get("code=evil&state=orosdrive-47811-zzzzzzzzzzzzzzzzzzzzzz"); c != 400 {
		t.Fatalf("another state: %d", c)
	}
	if s.handoff == nil {
		t.Fatal("a wrong state ended the sign-in")
	}
	if c := get("code=good&state=" + h.state); c != 200 {
		t.Fatalf("own state: %d", c)
	}
	if r := <-h.done; r.code != "good" || r.err != nil {
		t.Fatalf("got %+v", r)
	}
	if c := get("code=again&state=" + h.state); c != 400 {
		t.Fatal("the same answer was taken twice")
	}
}

func TestOrosTakesSignIn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/__oros/drive-signin" {
			json.NewEncoder(w).Encode(map[string]bool{"ok": true})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	old := orosAddr
	defer func() { orosAddr = old }()
	orosAddr = srv.URL
	if !orosTakesSignIn() {
		t.Fatal("orOS answer not seen")
	}
	orosAddr = srv.URL + "/elsewhere"
	if orosTakesSignIn() {
		t.Fatal("a 404 counted as orOS")
	}
}

func TestSummaryNeedsCookie(t *testing.T) {
	s, _ := newService(t)
	ui := &UI{S: s, Token: "tok", Port: 47811}
	req := httptest.NewRequest("GET", "http://127.0.0.1:47811/api/summary", nil)
	w := httptest.NewRecorder()
	ui.Handler().ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatalf("no cookie: %d", w.Code)
	}
	req.AddCookie(&http.Cookie{Name: "orosdrive", Value: "tok"})
	w = httptest.NewRecorder()
	ui.Handler().ServeHTTP(w, req)
	var sum Summary
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &sum) != nil || sum.V != 1 || sum.State != "warn" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

// orOS's tray says "Uploading X of Y" from the summary: progress only
// while syncing, and the held deletions as one count.
func TestSummaryProgressAndHeld(t *testing.T) {
	p := &mirror.Progress{Files: 10, FilesDone: 4, Bytes: 100, BytesDone: 40}
	st := Status{Syncing: true, Progress: p, Mount: MountInfo{Held: &mountHeld{GoneCount: 748, LostCount: 2}}}
	sum := SummaryOf(st)
	if sum.Progress != p || sum.Held != 750 {
		t.Fatalf("summary %+v", sum)
	}
	st.Syncing = false
	if SummaryOf(st).Progress != nil {
		t.Fatal("progress after the pass")
	}
}
