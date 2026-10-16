package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/koulaxizis/oros-drive/app"
	"github.com/koulaxizis/oros-drive/internal/config"
	"github.com/koulaxizis/oros-drive/mirror"
)

// hasDesktop: a tray needs a graphical session (always there on
// Windows; on Linux, a display server).
func hasDesktop() bool {
	if runtime.GOOS != "linux" && runtime.GOOS != "freebsd" {
		return true
	}
	return os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	noTray := fs.Bool("no-tray", false, "no tray icon (servers); the settings page still works")
	background := fs.Bool("background", false, "do not open the settings page (start at login)")
	fs.Parse(args)

	if link := app.Running(); link != "" {
		if !*background {
			return openURL(link)
		}
		return nil
	}
	s, err := app.NewService(version)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var quit func()
	ui, err := app.StartUI(s, func() { quit() })
	if err != nil {
		return err
	}
	defer ui.Close()
	go s.RefreshAccount()
	go s.Run(ctx)

	st := s.Status()
	if !*background && (!st.SignedIn || !st.Unlocked || len(st.Settings.Folders) == 0) {
		_ = openURL(ui.URL())
	}
	fmt.Fprintln(os.Stderr, "orOS Drive is running. Settings:", ui.URL())

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	if *noTray || !hasDesktop() {
		done := make(chan struct{})
		quit = func() { close(done) }
		select {
		case <-sig:
		case <-done:
		}
		return nil
	}
	tray := &app.Tray{S: s, UI: ui}
	quit = tray.Quit
	go func() { <-sig; tray.Quit() }()
	tray.Run()
	return nil
}

func cmdOpen() error {
	link := app.Running()
	if link == "" {
		return errors.New("orOS Drive is not running: start it with `orosdrive run`")
	}
	fmt.Println(link)
	return openURL(link)
}

// call sends one request to the running app, so the CLI and the app
// never write the settings at the same time. ok = false: no app runs.
func call(path string, body any) (map[string]any, bool, error) {
	link := app.Running()
	if link == "" {
		return nil, false, nil
	}
	i := strings.Index(link, "/?t=")
	base, tok := link[:i], link[i+4:]
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", base+"/api/"+path, bytes.NewReader(b))
	req.Header.Set("X-OD", "1")
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "orosdrive", Value: tok})
	res, err := (&http.Client{Timeout: 10 * time.Minute}).Do(req)
	if err != nil {
		return nil, true, err
	}
	defer res.Body.Close()
	var out map[string]any
	json.NewDecoder(res.Body).Decode(&out)
	if res.StatusCode != 200 {
		msg, _ := out["error"].(string)
		return nil, true, errors.New(msg)
	}
	return out, true, nil
}

func cmdUnlock() error {
	pass, err := readPassphrase()
	if err != nil {
		return err
	}
	out, running, err := call("unlock", map[string]any{"passphrase": pass})
	if running {
		if err != nil {
			if err.Error() == "locked" {
				return friendlyLocked()
			}
			return err
		}
		if out["noVault"] == true {
			return errors.New("there is no orOS vault in this Dropbox yet: sync once from orOS first, or start one from the settings page")
		}
		fmt.Println("Unlocked.")
		return nil
	}
	s, err := app.NewService(version)
	if err != nil {
		return err
	}
	if err := s.Unlock(pass, false); err != nil {
		if errors.Is(err, app.ErrNoVault) {
			return errors.New("there is no orOS vault in this Dropbox yet: sync once from orOS first, or start one from the settings page")
		}
		return err
	}
	fmt.Println("Unlocked. The vault key is kept in the system keychain.")
	return nil
}

func friendlyLocked() error {
	return errors.New("this passphrase does not open the vault (use the orOS sync passphrase)")
}

func cmdAdd(args []string) error {
	fs := flag.NewFlagSet("add", flag.ExitOnError)
	name := fs.String("name", "", "name of the folder in orOS (default: the folder's own name)")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return errors.New("usage: orosdrive add [--name N] <folder>")
	}
	abs, err := filepath.Abs(fs.Arg(0))
	if err != nil {
		return err
	}
	out, running, err := call("folders/add", map[string]string{"path": abs, "name": *name})
	if running {
		if err != nil {
			return err
		}
		fmt.Printf("Mirroring %s into orOS %v\n", abs, out["remote"])
		return nil
	}
	s, err := app.NewService(version)
	if err != nil {
		return err
	}
	p, err := s.AddFolder(abs, *name)
	if err != nil {
		return err
	}
	fmt.Printf("Mirroring %s into orOS %s (start the app, or run `orosdrive sync`)\n", abs, p.Remote)
	return nil
}

func cmdFolders() error {
	st, err := config.Load()
	if err != nil {
		return err
	}
	if len(st.Folders) == 0 {
		fmt.Println("No mirrored folders. Add one with `orosdrive add <folder>`.")
		return nil
	}
	for _, p := range st.Folders {
		paused := ""
		if p.Paused {
			paused = "  (paused)"
		}
		fmt.Printf("%-20s %s  ->  %s%s\n", p.Name(), p.Local, p.Remote, paused)
	}
	return nil
}

func findFolder(st *config.Settings, key string) (mirror.Pair, bool) {
	for _, p := range st.Folders {
		if p.ID == key || strings.EqualFold(p.Name(), key) || p.Local == key {
			return p, true
		}
	}
	if abs, err := filepath.Abs(key); err == nil {
		for _, p := range st.Folders {
			if p.Local == abs {
				return p, true
			}
		}
	}
	return mirror.Pair{}, false
}

func cmdRemove(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: orosdrive remove <name or folder>")
	}
	st, err := config.Load()
	if err != nil {
		return err
	}
	p, ok := findFolder(st, args[0])
	if !ok {
		return fmt.Errorf("no mirrored folder %q", args[0])
	}
	if _, running, err := call("folders/remove", map[string]string{"id": p.ID}); running {
		if err != nil {
			return err
		}
	} else {
		s, err := app.NewService(version)
		if err != nil {
			return err
		}
		if err := s.RemoveFolder(p.ID); err != nil {
			return err
		}
	}
	fmt.Printf("Stopped mirroring %s. Its files stay here and in orOS.\n", p.Local)
	return nil
}

func cmdSync() error {
	if _, running, err := call("sync", map[string]any{}); running {
		if err != nil {
			return err
		}
		fmt.Println("Asked the running app to sync now.")
		return nil
	}
	s, err := app.NewService(version)
	if err != nil {
		return err
	}
	_, err = s.SyncOnce()
	st := s.Status()
	for _, e := range st.Events {
		fmt.Printf("%-14s %s/%s %s\n", e.Kind, e.Folder, e.Path, e.Detail)
	}
	if err != nil {
		return err
	}
	for _, p := range st.Settings.Folders {
		f := st.Folders[p.ID]
		line := f.State
		switch f.State {
		case mirror.StateHeld:
			line = fmt.Sprintf("held: %d deletions wait for a yes in the settings page", f.HeldDeletes)
		case mirror.StateMissing:
			line = "folder missing, nothing changed"
		case mirror.StateReset:
			line = "the vault is empty although this folder was synced; nothing changed"
		case mirror.StateError:
			line = "error: " + f.Error
		}
		fmt.Printf("%-20s %s\n", p.Name(), line)
	}
	if r := st.Last; r != nil {
		fmt.Printf("up %d, down %d, deleted here %d, deleted in orOS %d, conflicts %d, failed %d\n",
			r.Uploaded, r.Downloaded, r.DeletedLocal, r.DeletedRemote, r.Conflicts, r.Failed)
	}
	return nil
}
