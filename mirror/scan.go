package mirror

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
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

func hashFile(f Folder, rel string) (string, error) {
	r, err := f.Open(rel)
	if err != nil {
		return "", err
	}
	defer r.Close()
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// scan walks a mirrored folder. Links are not followed and ignored
// names (lock files, partial downloads) are skipped with everything
// under them. A file whose size and time match the state keeps its
// cached hash; any other file is read and hashed.
func scan(f Folder, st *State, now time.Time, stableAfter time.Duration) (*scanResult, error) {
	res := &scanResult{files: map[string]*localFile{}, fold: map[string]string{}}
	if _, err := f.List(""); err != nil {
		return nil, err
	}
	var walk func(dir string)
	walk = func(dir string) {
		ents, err := f.List(dir)
		if err != nil {
			res.unread = append(res.unread, dir)
			return
		}
		for _, e := range ents {
			rel := e.Name
			if dir != "" {
				rel = dir + "/" + e.Name
			}
			if Ignored(e.Name) {
				if strings.HasPrefix(e.Name, tmpPrefix) && e.Regular && now.Sub(time.Unix(0, e.MTime)) > time.Hour {
					f.Remove(rel) // a download that died half-way
				}
				continue
			}
			if !utf8.ValidString(rel) {
				// orOS paths are text: a name in another encoding would
				// come back under a different name. Listed, left alone.
				res.unread = append(res.unread, strings.ToValidUTF8(rel, "?"))
				continue
			}
			if e.Dir {
				walk(rel)
				continue
			}
			if !e.Regular {
				continue // links, devices, sockets
			}
			lf := &localFile{Size: e.Size, MTime: e.MTime}
			res.files[rel] = lf
			if caseFolds(goos) {
				res.fold[strings.ToLower(rel)] = rel
			}
			if age := now.Sub(time.Unix(0, e.MTime)); age >= 0 && age < stableAfter {
				lf.Unknown, res.busy = true, true
				continue
			}
			if r := st.Files[rel]; r != nil && r.Local != "" && r.Size == lf.Size && r.MTime == lf.MTime {
				lf.Hash = r.Local
				continue
			}
			h, err := hashFile(f, rel)
			if err != nil {
				lf.Unknown = true
				res.unread = append(res.unread, rel)
				continue
			}
			lf.Hash = h
		}
	}
	walk("")
	return res, nil
}
