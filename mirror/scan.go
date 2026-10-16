package mirror

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// localFile is one file found by a scan.
type localFile struct {
	Size  int64
	MTime int64
	Hash  string
	// Unknown: the file is still being written (changed in the last
	// few seconds) or could not be read. Its path is left alone this
	// pass, on both sides.
	Unknown bool
}

type scanResult struct {
	files  map[string]*localFile
	fold   map[string]string // lower-cased path -> path, on case-folding systems
	unread []string
	busy   bool // a file was still changing: run again soon
}

func hashFile(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// scan walks a mirrored folder. Symbolic links are not followed and
// ignored names (lock files, partial downloads) are skipped with
// everything under them. A file whose size and time match the state
// keeps its cached hash; any other file is read and hashed.
func scan(root string, st *State, now time.Time, stableAfter time.Duration) (*scanResult, error) {
	res := &scanResult{files: map[string]*localFile{}, fold: map[string]string{}}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root {
				return err
			}
			if rel, e := filepath.Rel(root, p); e == nil {
				res.unread = append(res.unread, filepath.ToSlash(rel))
			}
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if p == root {
			return nil
		}
		name := d.Name()
		if Ignored(name) {
			if strings.HasPrefix(name, tmpPrefix) && d.Type().IsRegular() {
				if info, e := d.Info(); e == nil && now.Sub(info.ModTime()) > time.Hour {
					os.Remove(p) // a download that died half-way
				}
			}
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return nil // symlinks, devices, sockets
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if !utf8.ValidString(rel) {
			// orOS paths are text: a name in another encoding would
			// come back under a different name. Listed, left alone.
			res.unread = append(res.unread, strings.ToValidUTF8(rel, "?"))
			return nil
		}
		info, err := d.Info()
		if err != nil {
			res.unread = append(res.unread, rel)
			return nil
		}
		lf := &localFile{Size: info.Size(), MTime: info.ModTime().UnixNano()}
		res.files[rel] = lf
		if caseFolds(goos) {
			res.fold[strings.ToLower(rel)] = rel
		}
		if age := now.Sub(info.ModTime()); age >= 0 && age < stableAfter {
			lf.Unknown, res.busy = true, true
			return nil
		}
		if r := st.Files[rel]; r != nil && r.Local != "" && r.Size == lf.Size && r.MTime == lf.MTime {
			lf.Hash = r.Local
			return nil
		}
		h, err := hashFile(p)
		if err != nil {
			lf.Unknown = true
			res.unread = append(res.unread, rel)
			return nil
		}
		lf.Hash = h
		return nil
	})
	return res, err
}
