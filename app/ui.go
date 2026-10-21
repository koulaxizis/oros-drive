package app

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/koulaxizis/oros-drive/internal/config"
	"github.com/koulaxizis/oros-drive/vault"
	"github.com/koulaxizis/oros-drive/webdav"
)

//go:embed web
var webFS embed.FS

// First port of the settings page; 47800 belongs to the Dropbox
// sign-in redirect (and orOS Portable).
const uiPort = 47811

// UI is the settings page, served on 127.0.0.1 only. Every request
// needs the session cookie, which only a link carrying the random
// token can set; changes also need a custom header, so no web page
// open in the browser can drive it (no CORS answers, SameSite=Strict).
type UI struct {
	S     *Service
	Token string
	Port  int
	Quit  func()
	srv   *http.Server
}

// Instance is written beside app.json so a second start (or the CLI)
// finds the running app and opens its page instead of starting twice.
type Instance struct {
	Port  int    `json:"port"`
	Token string `json:"token"`
	PID   int    `json:"pid"`
}

func instanceFile() (string, error) {
	d, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "instance.json"), nil
}

// Running returns the running app's settings link, or "".
func Running() string {
	p, err := instanceFile()
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	var in Instance
	if json.Unmarshal(b, &in) != nil || in.Port == 0 {
		return ""
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", in.Port)
	req, _ := http.NewRequest("GET", base+"/api/ping", nil)
	req.AddCookie(&http.Cookie{Name: "orosdrive", Value: in.Token})
	c := &http.Client{Timeout: 2 * time.Second}
	res, err := c.Do(req)
	if err != nil {
		return ""
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		return ""
	}
	return base + "/?t=" + in.Token
}

// StartUI listens on the first free port from 47811 and records the
// instance.
func StartUI(s *Service, quit func()) (*UI, error) {
	var tok [24]byte
	if _, err := rand.Read(tok[:]); err != nil {
		return nil, err
	}
	u := &UI{S: s, Token: hex.EncodeToString(tok[:]), Quit: quit}
	var ln net.Listener
	var err error
	for port := uiPort; port < uiPort+10; port++ {
		if ln, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
			u.Port = port
			break
		}
	}
	if ln == nil {
		return nil, fmt.Errorf("no free port for the settings page: %w", err)
	}
	u.srv = &http.Server{Handler: u.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go u.srv.Serve(ln)
	if p, err := instanceFile(); err == nil {
		os.MkdirAll(filepath.Dir(p), 0o700)
		b, _ := json.Marshal(Instance{Port: u.Port, Token: u.Token, PID: os.Getpid()})
		os.WriteFile(p, b, 0o600)
	}
	return u, nil
}

// URL is the link that opens the page (and sets its cookie).
func (u *UI) URL() string { return fmt.Sprintf("http://127.0.0.1:%d/?t=%s", u.Port, u.Token) }

// Close stops the page and removes the instance record.
func (u *UI) Close() {
	if u.srv != nil {
		u.srv.Close()
	}
	if p, err := instanceFile(); err == nil {
		os.Remove(p)
	}
}

func (u *UI) authed(r *http.Request) bool {
	c, err := r.Cookie("orosdrive")
	return err == nil && subtle.ConstantTimeCompare([]byte(c.Value), []byte(u.Token)) == 1
}

// Handler is the page's router (exported for tests).
func (u *UI) Handler() http.Handler {
	static, _ := fs.Sub(webFS, "web")
	files := http.FileServer(http.FS(static))
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			files.ServeHTTP(w, r)
			return
		}
		if t := r.URL.Query().Get("t"); t != "" {
			if subtle.ConstantTimeCompare([]byte(t), []byte(u.Token)) == 1 {
				http.SetCookie(w, &http.Cookie{Name: "orosdrive", Value: u.Token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
			}
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		if !u.authed(r) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `<!doctype html><meta charset=utf-8><title>orOS Drive</title><p style="font:16px system-ui;margin:3em">Open the settings from the orOS Drive icon in the tray (or run <code>orosdrive open</code>).<br>Άνοιξε τις ρυθμίσεις από το εικονίδιο του orOS Drive (ή τρέξε <code>orosdrive open</code>).</p>`)
			return
		}
		files.ServeHTTP(w, r)
	})
	api := func(path string, method string, h func(r *http.Request) (any, error)) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if !u.authed(r) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			if r.Method != method {
				http.Error(w, "method", http.StatusMethodNotAllowed)
				return
			}
			if method == "POST" && r.Header.Get("X-OD") != "1" {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
			out, err := h(r)
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]string{"error": friendly(err)})
				return
			}
			if out == nil {
				out = map[string]bool{"ok": true}
			}
			json.NewEncoder(w).Encode(out)
		})
	}
	api("/api/ping", "GET", func(r *http.Request) (any, error) { return nil, nil })
	api("/api/status", "GET", func(r *http.Request) (any, error) {
		st := u.S.Status()
		return struct {
			Status
			Autostart bool `json:"autostart"`
		}{st, AutostartOn()}, nil
	})
	api("/api/login", "POST", func(r *http.Request) (any, error) {
		link, err := u.S.StartLogin()
		if err != nil {
			return nil, err
		}
		_ = Open(link)
		return map[string]string{"url": link}, nil
	})
	api("/api/login/webdav", "POST", func(r *http.Request) (any, error) {
		var in struct{ URL, User, Pass, Dir string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			return nil, err
		}
		return nil, u.S.ConnectWebDAV(in.URL, in.User, in.Pass, in.Dir)
	})
	api("/api/login/nextcloud", "POST", func(r *http.Request) (any, error) {
		var in struct{ URL, Dir string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			return nil, err
		}
		link, err := u.S.StartNextcloud(in.URL, in.Dir)
		if err != nil {
			return nil, err
		}
		_ = Open(link)
		return map[string]string{"url": link}, nil
	})
	api("/api/logout", "POST", func(r *http.Request) (any, error) { return nil, u.S.Logout() })
	api("/api/unlock", "POST", func(r *http.Request) (any, error) {
		var in struct {
			Passphrase string `json:"passphrase"`
			Create     bool   `json:"create"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			return nil, err
		}
		err := u.S.Unlock(in.Passphrase, in.Create)
		if errors.Is(err, ErrNoVault) {
			return map[string]bool{"noVault": true}, nil
		}
		return nil, err
	})
	api("/api/lock", "POST", func(r *http.Request) (any, error) { return nil, u.S.Lock() })
	api("/api/sync", "POST", func(r *http.Request) (any, error) { u.S.Kick(); return nil, nil })
	api("/api/pause", "POST", func(r *http.Request) (any, error) {
		var in struct {
			Paused bool `json:"paused"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			return nil, err
		}
		return nil, u.S.SetPaused(in.Paused)
	})
	api("/api/folders/add", "POST", func(r *http.Request) (any, error) {
		var in struct{ Path, Name string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			return nil, err
		}
		return u.S.AddFolder(strings.TrimSpace(in.Path), strings.TrimSpace(in.Name))
	})
	folderOp := func(path string, f func(id string, paused bool) error) {
		api(path, "POST", func(r *http.Request) (any, error) {
			var in struct {
				ID     string `json:"id"`
				Paused bool   `json:"paused"`
			}
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				return nil, err
			}
			return nil, f(in.ID, in.Paused)
		})
	}
	folderOp("/api/folders/remove", func(id string, _ bool) error { return u.S.RemoveFolder(id) })
	folderOp("/api/folders/pause", u.S.PauseFolder)
	folderOp("/api/folders/approve", func(id string, _ bool) error { u.S.ApproveDeletes(id); return nil })
	folderOp("/api/folders/open", func(id string, _ bool) error {
		for _, p := range u.S.pairs() {
			if p.ID == id {
				return Open(p.Local)
			}
		}
		return errors.New("no such folder")
	})
	api("/api/prefs", "POST", func(r *http.Request) (any, error) {
		var p Prefs
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			return nil, err
		}
		return nil, u.S.SetPrefs(p)
	})
	api("/api/open", "POST", func(r *http.Request) (any, error) {
		var in struct{ What string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			return nil, err
		}
		switch in.What {
		case "oros":
			return nil, Open(OrosURL)
		case "trash":
			d, err := config.TrashDir()
			if err != nil {
				return nil, err
			}
			os.MkdirAll(d, 0o700)
			return nil, Open(d)
		}
		if in.What == "disk" {
			if m := u.S.Status().Mount; m.Mounted {
				return nil, Open(m.Path)
			}
			return nil, errors.New("not shown")
		}
		return nil, errors.New("unknown")
	})
	api("/api/usage", "GET", func(r *http.Request) (any, error) { return u.S.Usage() })
	api("/api/cleanup", "POST", func(r *http.Request) (any, error) {
		n, b, err := u.S.CleanUp()
		if err != nil {
			return nil, err
		}
		return map[string]int64{"removed": int64(n), "bytes": b}, nil
	})
	api("/api/browse", "GET", func(r *http.Request) (any, error) { return browse(r.URL.Query().Get("path")) })
	api("/api/quit", "POST", func(r *http.Request) (any, error) {
		if u.Quit != nil {
			go func() { time.Sleep(200 * time.Millisecond); u.Quit() }()
		}
		return nil, nil
	})
	return u.guard(mux)
}

// guard refuses requests addressed to any other host name (DNS
// rebinding) and sets the page's security headers.
func (u *UI) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, port, err := net.SplitHostPort(host); err == nil && (port == fmt.Sprint(u.Port) || u.Port == 0) {
			host = h
		} else {
			host = ""
		}
		if host != "127.0.0.1" && host != "localhost" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; frame-ancestors 'none'; form-action 'none'; base-uri 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func friendly(err error) string {
	switch {
	case errors.Is(err, vault.ErrLocked):
		return "locked"
	case errors.Is(err, ErrNoVault):
		return "no-vault"
	case errors.Is(err, ErrSignedIn):
		return ErrSignedIn.Error()
	}
	if c := webdav.Code(err); c != "" {
		return c
	}
	return err.Error()
}

// browse lists sub-folders for the folder picker.
type browseResult struct {
	Path   string   `json:"path"`
	Parent string   `json:"parent,omitempty"`
	Dirs   []string `json:"dirs"`
	Roots  []string `json:"roots"`
	Sep    string   `json:"sep"`
}

func browse(p string) (*browseResult, error) {
	home, _ := os.UserHomeDir()
	if p == "" {
		p = home
	}
	p = filepath.Clean(p)
	if !filepath.IsAbs(p) {
		return nil, errors.New("not a full path")
	}
	ents, err := os.ReadDir(p)
	if err != nil {
		return nil, err
	}
	res := &browseResult{Path: p, Dirs: []string{}, Roots: roots(home), Sep: string(filepath.Separator)}
	if parent := filepath.Dir(p); parent != p {
		res.Parent = parent
	}
	for _, e := range ents {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") && !strings.HasPrefix(e.Name(), "$") {
			res.Dirs = append(res.Dirs, e.Name())
		}
	}
	sort.Slice(res.Dirs, func(i, j int) bool { return strings.ToLower(res.Dirs[i]) < strings.ToLower(res.Dirs[j]) })
	return res, nil
}

func roots(home string) []string {
	var out []string
	if home != "" {
		out = append(out, home)
	}
	if runtime.GOOS == "windows" {
		for c := 'C'; c <= 'Z'; c++ {
			d := string(c) + `:\`
			if _, err := os.Stat(d); err == nil {
				out = append(out, d)
			}
		}
		return out
	}
	out = append(out, "/")
	for _, base := range []string{"/media", "/run/media", "/mnt"} {
		if ents, err := os.ReadDir(base); err == nil {
			for _, e := range ents {
				if e.IsDir() {
					out = append(out, filepath.Join(base, e.Name()))
				}
			}
		}
	}
	return out
}

// Open opens a link or a folder with the system's handler.
func Open(target string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		if strings.HasPrefix(target, "http") {
			cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
		} else {
			cmd = exec.Command("explorer", target)
		}
	case "darwin":
		cmd = exec.Command("open", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}
