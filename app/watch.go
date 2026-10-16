package app

import (
	"io/fs"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/koulaxizis/oros-drive/mirror"
)

// watcher turns file system notifications in the mirrored folders into
// one signal after things have been quiet for a moment. Where watching
// is not possible (too many folders for inotify's limit, a network
// disk), the 10-minute pass still catches every change.
type watcher struct {
	w   *fsnotify.Watcher
	out chan struct{}
	end chan struct{}
}

const quiet = 2 * time.Second

func newWatcher(pairs []mirror.Pair) *watcher {
	wt := &watcher{out: make(chan struct{}, 1), end: make(chan struct{})}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return wt
	}
	wt.w = w
	for _, p := range pairs {
		if !p.Paused {
			wt.addTree(p.Local)
		}
	}
	go wt.loop()
	return wt
}

func (wt *watcher) addTree(root string) {
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if p != root && mirror.Ignored(d.Name()) {
				return fs.SkipDir
			}
			if wt.w.Add(p) != nil {
				return fs.SkipAll // out of watches: the periodic pass covers the rest
			}
		}
		return nil
	})
}

func (wt *watcher) loop() {
	var timer *time.Timer
	var fire <-chan time.Time
	for {
		select {
		case <-wt.end:
			return
		case ev, ok := <-wt.w.Events:
			if !ok {
				return
			}
			name := filepath.Base(ev.Name)
			if mirror.Ignored(name) {
				continue
			}
			if ev.Op&fsnotify.Create != 0 {
				wt.addTree(ev.Name) // a new folder: watch it too (a no-op for files)
			}
			if timer == nil {
				timer = time.NewTimer(quiet)
			} else {
				timer.Reset(quiet)
			}
			fire = timer.C
		case <-wt.w.Errors:
		case <-fire:
			fire = nil
			select {
			case wt.out <- struct{}{}:
			default:
			}
		}
	}
}

// Events delivers one signal per quiet period after changes.
func (wt *watcher) Events() <-chan struct{} { return wt.out }

func (wt *watcher) Close() {
	select {
	case <-wt.end:
	default:
		close(wt.end)
	}
	if wt.w != nil {
		wt.w.Close()
	}
}
