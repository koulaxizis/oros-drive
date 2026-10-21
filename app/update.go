package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// New-release notice (UPD-1). The desktop app asks GitHub once a day for
// the latest release of orOS Drive and, when it is newer than this build,
// the settings page and the tray offer its download page. Nothing is
// downloaded or installed by itself, nothing is sent but the request, and
// the notice never pops up on its own (free-forever rule: no nagging).
// Settings › "Tell me about new versions" turns the check off.

// ReleaseAPI answers with the latest release (tests point it elsewhere).
var ReleaseAPI = "https://api.github.com/repos/koulaxizis/oros-drive/releases/latest"

// ReleasesPage is where a release's files are; the tag is appended.
const ReleasesPage = "https://github.com/koulaxizis/oros-drive/releases/tag/"

const updateEvery = 24 * time.Hour

var releaseTag = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)$`)

// UpdateInfo is a newer release than this build.
type UpdateInfo struct {
	Version string `json:"version"`
	URL     string `json:"url"`
}

// semver splits "1.2.3" (or "v1.2.3"); ok is false for anything else,
// such as "dev" or a test build "0.0.0-<sha>".
func semver(v string) (n [3]int, ok bool) {
	m := releaseTag.FindStringSubmatch("v" + strings.TrimPrefix(v, "v"))
	if m == nil {
		return n, false
	}
	for i := range n {
		n[i], _ = strconv.Atoi(m[i+1])
	}
	return n, true
}

// newer reports whether release latest is newer than build current.
// Builds without a release version never see a notice.
func newer(current, latest string) bool {
	c, ok1 := semver(current)
	l, ok2 := semver(latest)
	if !ok1 || !ok2 {
		return false
	}
	for i := range c {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

// latestRelease returns the tag of the latest release ("v0.3.0").
func latestRelease(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", ReleaseAPI, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "orOS-Drive")
	c := &http.Client{Timeout: 30 * time.Second}
	r, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		return "", errors.New("release check: " + r.Status)
	}
	var rel struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&rel); err != nil {
		return "", err
	}
	if rel.Draft || rel.Prerelease || !releaseTag.MatchString(rel.Tag) {
		return "", errors.New("release check: no usable release")
	}
	return rel.Tag, nil
}

// CheckUpdate asks once for the latest release and records it when newer.
func (s *Service) CheckUpdate(ctx context.Context) error {
	s.mu.Lock()
	off := s.settings.NoUpdateCheck
	s.mu.Unlock()
	if off {
		return nil
	}
	if _, ok := semver(s.Version); !ok {
		return nil // dev and test builds
	}
	tag, err := latestRelease(ctx)
	if err != nil {
		return err
	}
	var u *UpdateInfo
	if newer(s.Version, tag) {
		u = &UpdateInfo{Version: strings.TrimPrefix(tag, "v"), URL: ReleasesPage + tag}
	}
	s.mu.Lock()
	changed := (u == nil) != (s.update == nil) || (u != nil && *u != *s.update)
	s.update = u
	s.mu.Unlock()
	if changed {
		s.notify()
	}
	return nil
}

// CheckUpdates runs CheckUpdate a minute after start and then once a day,
// and again right after the check is turned back on.
func (s *Service) CheckUpdates(ctx context.Context) {
	t := time.NewTimer(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.updateNow:
		case <-t.C:
		}
		_ = s.CheckUpdate(ctx) // offline or GitHub busy: next time
		if !t.Stop() {
			select {
			case <-t.C:
			default:
			}
		}
		t.Reset(updateEvery)
	}
}
