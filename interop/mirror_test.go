package interop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/koulaxizis/oros-drive/mirror"
	"github.com/koulaxizis/oros-drive/vault"
)

// The desktop mirror and the real orOS browser code edit the same
// mirrored folder, taking turns.

const driveDir = "Drive/PC/Έγγραφα"

type mirrorEnv struct {
	*env
	local string
	eng   *mirror.Engine
	pair  mirror.Pair
}

func setupMirror(t *testing.T) *mirrorEnv {
	e := setup(t)
	d := t.TempDir()
	m := &mirrorEnv{env: e, local: filepath.Join(d, "local")}
	os.MkdirAll(m.local, 0o755)
	// orOS starts the vault (key file + a note), as a real user would.
	writeDisk(t, e.disk, map[string][]byte{"Notes/todo.md": []byte("milk")})
	e.jsSync(true)
	v := goVault(e)
	if _, err := v.LoadKeys(false); err != nil {
		t.Fatal(err)
	}
	m.eng = &mirror.Engine{
		Vault:    v,
		StateDir: filepath.Join(d, "state"),
		Trash:    &mirror.DirTrash{Root: filepath.Join(d, "trash")},
		Opt:      mirror.Options{StableAfter: -1},
	}
	m.pair = mirror.Pair{ID: "docs", Local: m.local, Remote: vault.RootPath + "/" + driveDir}
	return m
}

func (m *mirrorEnv) pass() *mirror.Result {
	m.t.Helper()
	r, err := m.eng.Sync([]mirror.Pair{m.pair}, nil)
	if err != nil {
		m.t.Fatal(err)
	}
	return r
}

func (m *mirrorEnv) browser(touch bool) stats {
	args := []string{"sync", m.state, pass, "--fetch-drive"}
	if touch {
		args = append(args, "--touch")
	}
	return m.js(args...)
}

var tick = time.Now().Add(-time.Hour)

func (m *mirrorEnv) writeLocal(rel, text string) {
	p := filepath.Join(m.local, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte(text), 0o644)
	tick = tick.Add(time.Second)
	os.Chtimes(p, tick, tick)
}

func (m *mirrorEnv) diskDrive() map[string][]byte {
	out := map[string][]byte{}
	for k, v := range readDisk(m.t, m.disk) {
		if strings.HasPrefix(k, driveDir+"/") {
			out[strings.TrimPrefix(k, driveDir+"/")] = v
		}
	}
	return out
}

func TestMirrorAndBrowserTakeTurns(t *testing.T) {
	m := setupMirror(t)
	files := map[string][]byte{}
	for k, v := range sample() {
		files[k] = v
		m.writeLocal(k, string(v))
	}
	if r := m.pass(); r.Uploaded != len(files) {
		t.Fatalf("mirror uploaded %d of %d", r.Uploaded, len(files))
	}
	// orOS sees the folder (fetching the on-demand files) and keeps the note.
	m.browser(false)
	sameFiles(t, m.diskDrive(), files)
	if string(readDisk(t, m.disk)["Notes/todo.md"]) != "milk" {
		t.Fatal("the note was lost")
	}

	// orOS edits one file, adds one, deletes one.
	writeDisk(t, filepath.Join(m.disk, driveDir), map[string][]byte{
		"a.txt":           []byte("edited in orOS"),
		"νέο/από-orOS.md": []byte("new"),
	})
	os.Remove(filepath.Join(m.disk, driveDir, "empty.txt"))
	if s := m.browser(true); s.Failed != 0 {
		t.Fatalf("browser: %+v", s)
	}
	r := m.pass()
	if r.Downloaded != 2 || r.DeletedLocal != 1 || r.Uploaded != 0 {
		t.Fatalf("mirror after orOS edits: %+v", r)
	}
	files["a.txt"] = []byte("edited in orOS")
	files["νέο/από-orOS.md"] = []byte("new")
	delete(files, "empty.txt")
	sameFiles(t, readDisk(t, m.local), files)

	// The computer edits; orOS picks it up; then both are quiet.
	m.writeLocal("Docs/Έγγραφα/σημείωση.md", "from the computer")
	if r := m.pass(); r.Uploaded != 1 {
		t.Fatalf("%+v", r)
	}
	m.browser(false)
	files["Docs/Έγγραφα/σημείωση.md"] = []byte("from the computer")
	sameFiles(t, m.diskDrive(), files)
	if s := m.browser(false); s.Uploaded+s.Downloaded != 0 {
		t.Fatalf("browser not quiet: %+v", s)
	}
	if r := m.pass(); r.Uploaded+r.Downloaded+r.DeletedLocal+r.DeletedRemote != 0 {
		t.Fatalf("mirror not quiet: %+v", r)
	}
}

func TestMirrorConflictWithBrowser(t *testing.T) {
	m := setupMirror(t)
	m.writeLocal("plan.md", "base")
	m.pass()
	m.browser(false)
	writeDisk(t, filepath.Join(m.disk, driveDir), map[string][]byte{"plan.md": []byte("orOS version")})
	m.browser(true)
	m.writeLocal("plan.md", "computer version")
	if r := m.pass(); r.Conflicts != 1 {
		t.Fatalf("%+v", r)
	}
	m.browser(false)
	got := m.diskDrive()
	if string(got["plan.md"]) != "computer version" || len(got) != 2 {
		t.Fatalf("orOS has %v", keys(got))
	}
	for k, v := range got {
		if k != "plan.md" && (!strings.HasPrefix(k, "plan (conflict ") || string(v) != "orOS version") {
			t.Fatalf("conflict copy %q = %q", k, v)
		}
	}
	sameFiles(t, readDisk(t, m.local), got)
}

func keys(m map[string][]byte) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
