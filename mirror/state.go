package mirror

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Record is what the engine remembers about one file of a mirrored
// folder.
type Record struct {
	Size  int64  `json:"s"`
	MTime int64  `json:"m"`           // local modification time (Unix ns) when Local was computed
	Local string `json:"l,omitempty"` // SHA-256 of the local file at Size/MTime: a cache, so unchanged files are not read again
	Base  string `json:"b,omitempty"` // the content both sides had at the last sync; "" = never synced
}

// State is one mirrored folder's memory, kept beside the app config.
type State struct {
	Files map[string]*Record `json:"files"`
	// Conflicts: relative path -> cloud hash already kept as a conflict
	// copy, so a pass that fails after making the copy does not make a
	// second one (vault.js CONFLICT_KEY).
	Conflicts map[string]string `json:"conflicts,omitempty"`
}

func (s *State) synced() bool {
	for _, r := range s.Files {
		if r.Base != "" {
			return true
		}
	}
	return false
}

func stateFile(dir, id string) string { return filepath.Join(dir, id+".json") }

// LoadState reads a folder's state; a missing file is a fresh state.
// A fresh state never deletes anything: a deletion needs a Base.
func LoadState(dir, id string) (*State, error) {
	s := &State{Files: map[string]*Record{}, Conflicts: map[string]string{}}
	b, err := os.ReadFile(stateFile(dir, id))
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, s); err != nil {
		return nil, err
	}
	if s.Files == nil {
		s.Files = map[string]*Record{}
	}
	if s.Conflicts == nil {
		s.Conflicts = map[string]string{}
	}
	return s, nil
}

// Save writes the state atomically (temp file + rename).
func (s *State) Save(dir, id string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	p := stateFile(dir, id)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// DeleteState forgets a folder (it was removed from the mirror list).
func DeleteState(dir, id string) error {
	err := os.Remove(stateFile(dir, id))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

const cloudFile = "cloud"

// NoteCloud records which cloud the folders in dir sync with (orOS
// sync.js noteCloud). When it changes (Dropbox to WebDAV, another
// server), what was synced with the old cloud says nothing about the
// new one: every folder forgets its Base and its kept conflicts, so
// the first pass with the new cloud is a first sync and deletes
// nothing. States from before the marker belong to "dropbox".
func NoteCloud(dir, id string) (changed bool, err error) {
	p := filepath.Join(dir, cloudFile)
	prev := "dropbox"
	if b, err := os.ReadFile(p); err == nil {
		prev = strings.TrimSpace(string(b))
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if prev == id {
		return false, nil
	}
	ents, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	for _, e := range ents {
		fid, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || e.IsDir() {
			continue
		}
		st, err := LoadState(dir, fid)
		if err != nil {
			return false, err
		}
		for _, r := range st.Files {
			r.Base = ""
		}
		st.Conflicts = map[string]string{}
		if err := st.Save(dir, fid); err != nil {
			return false, err
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return true, err
	}
	return true, os.WriteFile(p, []byte(id+"\n"), 0o600)
}
