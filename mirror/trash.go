package mirror

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Trash receives local files the mirror removes because they were
// deleted in orOS (or replaced by a newer version of a file deleted
// here). Nothing the engine removes is gone for good.
type Trash interface {
	Move(abs, folder, rel string) error
}

// DirTrash keeps removed files in Root/<YYYY-MM-DD>/<folder>/<rel>
// and forgets days older than Keep.
type DirTrash struct {
	Root string
	Keep time.Duration
	Now  func() time.Time
}

func (t *DirTrash) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

func (t *DirTrash) Move(abs, folder, rel string) error {
	dst := filepath.Join(t.Root, t.now().Format("2006-01-02"), safeName(folder), filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	dst = freePath(dst)
	if err := os.Rename(abs, dst); err == nil {
		return nil
	}
	// Another disk: copy, then remove.
	if err := copyFile(abs, dst); err != nil {
		os.Remove(dst)
		return err
	}
	return os.Remove(abs)
}

// Purge removes trash days older than Keep (30 days by default).
func (t *DirTrash) Purge() error {
	keep := t.Keep
	if keep <= 0 {
		keep = 30 * 24 * time.Hour
	}
	ents, err := os.ReadDir(t.Root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	limit := t.now().Add(-keep)
	for _, e := range ents {
		day, err := time.ParseInLocation("2006-01-02", e.Name(), time.Local)
		if err != nil || !e.IsDir() {
			continue
		}
		if day.Add(24 * time.Hour).Before(limit) {
			os.RemoveAll(filepath.Join(t.Root, e.Name()))
		}
	}
	return nil
}

func safeName(s string) string {
	s = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`<>:"/\|?*`, r) || r < 0x20 {
			return '_'
		}
		return r
	}, s)
	if s == "" || s == "." || s == ".." {
		return "_"
	}
	return s
}

func freePath(p string) string {
	if _, err := os.Lstat(p); errors.Is(err, os.ErrNotExist) {
		return p
	}
	ext := filepath.Ext(p)
	base := strings.TrimSuffix(p, ext)
	for n := 2; ; n++ {
		c := base + " (" + itoa(n) + ")" + ext
		if _, err := os.Lstat(c); errors.Is(err, os.ErrNotExist) {
			return c
		}
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
