package mirror

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// FileInfo describes one entry of a mirrored folder.
type FileInfo struct {
	Name    string `json:"name"`
	Dir     bool   `json:"dir"`
	Regular bool   `json:"regular"` // a plain file (not a link or device)
	Size    int64  `json:"size"`
	MTime   int64  `json:"mtime"` // Unix ns
}

// Folder is a mirrored folder as the engine sees it: paths relative to
// its root with "/" ("" = the root). A desktop folder is a directory
// (OSFolder); on Android it is a folder the user granted through the
// system picker, served by the app.
type Folder interface {
	// Stat returns fs.ErrNotExist (wrapped or not) for a missing entry.
	Stat(rel string) (FileInfo, error)
	List(rel string) ([]FileInfo, error)
	Open(rel string) (io.ReadCloser, error)
	// Write replaces the file in one step (no reader sees half a file),
	// creating parent folders, and returns the new file's info.
	Write(rel string, data []byte) (FileInfo, error)
	// Remove moves the file to the trash and removes folders it leaves
	// empty, up to the root.
	Remove(rel string) error
}

func isNotExist(err error) bool { return errors.Is(err, fs.ErrNotExist) }

// OSFolder is a directory of this computer.
type OSFolder struct {
	Root  string
	Name  string // shown in the trash
	Trash Trash  // nil: removed files are deleted
}

func (f *OSFolder) abs(rel string) string {
	if rel == "" {
		return f.Root
	}
	return filepath.Join(f.Root, filepath.FromSlash(rel))
}

func info(name string, fi os.FileInfo) FileInfo {
	return FileInfo{Name: name, Dir: fi.IsDir(), Regular: fi.Mode().IsRegular(), Size: fi.Size(), MTime: fi.ModTime().UnixNano()}
}

func (f *OSFolder) Stat(rel string) (FileInfo, error) {
	fi, err := os.Lstat(f.abs(rel))
	if err != nil {
		return FileInfo{}, err
	}
	return info(path.Base(rel), fi), nil
}

func (f *OSFolder) List(rel string) ([]FileInfo, error) {
	ents, err := os.ReadDir(f.abs(rel))
	if err != nil {
		return nil, err
	}
	out := make([]FileInfo, 0, len(ents))
	for _, e := range ents {
		fi, err := e.Info()
		if err != nil {
			continue // vanished meanwhile
		}
		out = append(out, info(e.Name(), fi))
	}
	return out, nil
}

func (f *OSFolder) Open(rel string) (io.ReadCloser, error) { return os.Open(f.abs(rel)) }

func (f *OSFolder) Write(rel string, data []byte) (FileInfo, error) {
	abs := f.abs(rel)
	dir := filepath.Dir(abs)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return FileInfo{}, err
	}
	var rnd [6]byte
	rand.Read(rnd[:])
	tmp := filepath.Join(dir, tmpPrefix+hex.EncodeToString(rnd[:]))
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return FileInfo{}, err
	}
	_, werr := out.Write(data)
	if werr == nil {
		werr = out.Sync()
	}
	if cerr := out.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp, abs)
	}
	if werr != nil {
		os.Remove(tmp)
		return FileInfo{}, werr
	}
	return f.Stat(rel)
}

func (f *OSFolder) Remove(rel string) error {
	abs := f.abs(rel)
	if f.Trash != nil {
		if err := f.Trash.Move(abs, f.Name, rel); err != nil {
			return err
		}
	} else if err := os.Remove(abs); err != nil {
		return err
	}
	root := filepath.Clean(f.Root)
	for d := filepath.Dir(abs); d != root && strings.HasPrefix(d, root); d = filepath.Dir(d) {
		if os.Remove(d) != nil {
			break
		}
	}
	return nil
}
