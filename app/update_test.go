package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestNewerVersion(t *testing.T) {
	for _, c := range []struct {
		cur, latest string
		want        bool
	}{
		{"0.2.1", "v0.2.2", true},
		{"0.2.1", "v0.10.0", true},
		{"0.2.1", "v1.0.0", true},
		{"0.2.1", "v0.2.1", false},
		{"0.3.0", "v0.2.9", false},
		{"dev", "v9.9.9", false},
		{"0.0.0-abc123", "v0.2.1", false},
		{"android", "v0.2.1", false},
		{"0.2.1", "v0.3.0-rc1", false},
		{"0.2.1", "latest", false},
	} {
		if got := newer(c.cur, c.latest); got != c.want {
			t.Errorf("newer(%q, %q) = %v", c.cur, c.latest, got)
		}
	}
}

// fakeReleases answers like GitHub's latest-release endpoint.
func fakeReleases(t *testing.T, body string) *atomic.Int32 {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	old := ReleaseAPI
	ReleaseAPI = srv.URL
	t.Cleanup(func() { ReleaseAPI = old })
	return &calls
}

func TestCheckUpdate(t *testing.T) {
	calls := fakeReleases(t, `{"tag_name":"v0.3.0","draft":false,"prerelease":false}`)
	s, _ := newService(t)
	ctx := context.Background()

	// A build without a release version never asks.
	if err := s.CheckUpdate(ctx); err != nil || calls.Load() != 0 || s.Status().Update != nil {
		t.Fatal("test build asked", err, calls.Load())
	}

	s.Version = "0.2.1"
	if err := s.CheckUpdate(ctx); err != nil {
		t.Fatal(err)
	}
	u := s.Status().Update
	if u == nil || u.Version != "0.3.0" || u.URL != ReleasesPage+"v0.3.0" {
		t.Fatalf("update %+v", u)
	}

	// Turned off: the notice goes and GitHub is not asked again.
	off := false
	if err := s.SetPrefs(Prefs{UpdateCheck: &off}); err != nil {
		t.Fatal(err)
	}
	if s.Status().Update != nil || !s.Status().Settings.NoUpdateCheck {
		t.Fatal("still shown after turning off")
	}
	n := calls.Load()
	if err := s.CheckUpdate(ctx); err != nil || calls.Load() != n {
		t.Fatal("asked while off")
	}

	// Back on: CheckUpdates is told to ask at once.
	on := true
	if err := s.SetPrefs(Prefs{UpdateCheck: &on}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.updateNow:
	default:
		t.Fatal("no check asked for")
	}

	// Up to date: no notice.
	s.Version = "0.3.0"
	if err := s.CheckUpdate(ctx); err != nil || s.Status().Update != nil {
		t.Fatal("notice for the same version", err)
	}
}

func TestCheckUpdateIgnoresOddReleases(t *testing.T) {
	for _, body := range []string{
		`{"tag_name":"v0.3.0","prerelease":true}`,
		`{"tag_name":"v0.3.0","draft":true}`,
		`{"tag_name":"v0.3.0\"><script>"}`,
		`{"tag_name":"javascript:alert(1)"}`,
		`not json`,
	} {
		fakeReleases(t, body)
		s, _ := newService(t)
		s.Version = "0.2.1"
		if err := s.CheckUpdate(context.Background()); err == nil || s.Status().Update != nil {
			t.Errorf("%s: accepted", body)
		}
	}
}
