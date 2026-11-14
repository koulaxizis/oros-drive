package app

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/koulaxizis/oros-drive/internal/config"
	"github.com/koulaxizis/oros-drive/vault"
)

// blobStore is a local vault that also has orOS's sync blob next to
// it, like Dropbox and WebDAV (vault.BlobReader).
type blobStore struct {
	*vault.DirStore
	blob []byte
}

func (b *blobStore) GetSyncBlob() ([]byte, error) { return b.blob, nil }

// vp as sync.js vaultProof makes it.
func vpFor(raw []byte, kind string, n int64) string {
	sk := sha256.Sum256(append([]byte("oros-signal"), raw...))
	m := hmac.New(sha256.New, sk[:])
	m.Write([]byte(kind + ":" + strconv.FormatInt(n, 10)))
	return base64.StdEncoding.EncodeToString(m.Sum(nil))
}

func signalBlob(kind string, n int64, vp string) []byte {
	return []byte(`{"ver":"oros-signal","sig":{"kind":"` + kind + `","n":` + strconv.FormatInt(n, 10) + `,"iter":600000,"salt":"s","iv":"i","data":"d","vp":"` + vp + `"}}`)
}

// A service with one mirrored, synced folder and something in the disk cache.
func sosService(t *testing.T) (*Service, *blobStore, string, string) {
	s, dir := newService(t)
	bs := &blobStore{DirStore: vault.NewDirStore(filepath.Join(dir, "cloud"))}
	s.Store = bs
	if err := s.Unlock(pass, true); err != nil {
		t.Fatal(err)
	}
	docs := filepath.Join(dir, "Docs")
	os.MkdirAll(docs, 0o755)
	os.WriteFile(filepath.Join(docs, "a.txt"), []byte("mine"), 0o644)
	old := time.Now().Add(-time.Minute)
	os.Chtimes(filepath.Join(docs, "a.txt"), old, old)
	if _, err := s.AddFolder(docs, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	cache, _ := config.CacheDir()
	os.MkdirAll(cache, 0o700)
	os.WriteFile(filepath.Join(cache, "copy"), []byte("cached"), 0o600)
	return s, bs, docs, cache
}

func TestConfirmedSOSErasesDriveAndKeepsMirroredFolders(t *testing.T) {
	s, bs, docs, cache := sosService(t)
	n := time.Now().UnixMilli()
	bs.blob = signalBlob("sos", n, vpFor(s.vkey.Key, "sos", n))
	if _, err := s.SyncOnce(); !errors.Is(err, ErrErased) {
		t.Fatalf("want ErrErased, got %v", err)
	}
	st := s.Status()
	if st.Unlocked || st.Syncing || st.Settings.SOS == 0 || st.Settings.Mount != "" {
		t.Fatalf("after SOS: %+v", st)
	}
	if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Fatal("the disk cache is still there")
	}
	if b, _ := os.ReadFile(filepath.Join(docs, "a.txt")); string(b) != "mine" {
		t.Fatal("a mirrored folder's own file was touched")
	}
	// The key is gone from this computer too: a restart is locked.
	s2, err := NewService("test")
	if err != nil {
		t.Fatal(err)
	}
	if s2.Status().Unlocked {
		t.Fatal("the vault key survived the SOS")
	}
	if s2.Status().Settings.SOS == 0 {
		t.Fatal("the SOS time was not kept for the page")
	}
}

func TestSOSRidingInANormalBlobErasesToo(t *testing.T) {
	s, bs, _, _ := sosService(t)
	n := time.Now().UnixMilli()
	bs.blob = []byte(`{"ver":1,"iter":600000,"salt":"s","iv":"i","data":"d","sig":{"kind":"sos","n":` + strconv.FormatInt(n, 10) + `,"salt":"s","iv":"i","data":"d","vp":"` + vpFor(s.vkey.Key, "sos", n) + `"}}`)
	if _, err := s.SyncOnce(); !errors.Is(err, ErrErased) {
		t.Fatalf("want ErrErased, got %v", err)
	}
}

func TestUnconfirmedSignalStopsAndDeletesNothing(t *testing.T) {
	for name, blob := range map[string][]byte{
		"sos without vp":     signalBlob("sos", 5, ""),
		"sos with other key": signalBlob("sos", 5, vpFor(make([]byte, 32), "sos", 5)),
		"reset":              signalBlob("reset", 5, ""),
	} {
		t.Run(name, func(t *testing.T) {
			s, bs, docs, cache := sosService(t)
			bs.blob = blob
			before, _, _ := bs.Get(vault.ManifestKey)
			os.WriteFile(filepath.Join(docs, "b.txt"), []byte("new"), 0o644)
			old := time.Now().Add(-time.Minute)
			os.Chtimes(filepath.Join(docs, "b.txt"), old, old)
			if _, err := s.SyncOnce(); !errors.Is(err, ErrSignal) {
				t.Fatalf("want ErrSignal, got %v", err)
			}
			st := s.Status()
			want := "closed"
			if name == "reset" {
				want = "reset"
			}
			if st.Signal != want || !st.Unlocked || st.Syncing || st.Settings.SOS != 0 {
				t.Fatalf("status %+v", st)
			}
			if b, _ := os.ReadFile(filepath.Join(cache, "copy")); string(b) != "cached" {
				t.Fatal("the cache was touched")
			}
			if after, _, _ := bs.Get(vault.ManifestKey); string(after) != string(before) {
				t.Fatal("the pass ran while the cloud held a signal")
			}
			// The signal goes (orOS started the cloud again with a normal
			// blob): the pass runs again.
			bs.blob = []byte(`{"ver":1,"iter":600000,"salt":"s","iv":"i","data":"d"}`)
			if _, err := s.SyncOnce(); err != nil {
				t.Fatal(err)
			}
			if s.Status().Signal != "" {
				t.Fatal("the signal notice stayed")
			}
		})
	}
}

func TestOldSOSInANewCloudIsIgnored(t *testing.T) {
	// Drive signed in to a cloud started again after an SOS: the sig in
	// its blobs was made under the old key, which this Drive never had.
	s, bs, _, cache := sosService(t)
	bs.blob = []byte(`{"ver":1,"iter":600000,"salt":"s","iv":"i","data":"d","sig":{"kind":"sos","n":5,"salt":"s","iv":"i","data":"d","vp":"` + vpFor(make([]byte, 32), "sos", 5) + `"}}`)
	if _, err := s.SyncOnce(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(cache, "copy")); string(b) != "cached" {
		t.Fatal("the cache was touched")
	}
}
