package webdav

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Nextcloud Login Flow v2 (sync.js connectNextcloud, WebDAV W3): the
// user signs in on their own server's page and approves orOS Drive;
// the app gets an app password made for it and never sees the main
// password.

var (
	ErrNotNextcloud = errors.New("dav-notnextcloud")
	ErrLoginExpired = errors.New("dav-login-expired")
)

// PollEvery is how often the server is asked whether the user approved
// (sync.js waits 3 s without the relay). Tests lower it.
var PollEvery = 3 * time.Second

// loginTimeout is Nextcloud's own limit for a login token.
const loginTimeout = 20 * time.Minute

var phpRe = regexp.MustCompile(`/(index\.php|remote\.php)(/.*)?$`)

// Login is a Nextcloud sign-in waiting for the user.
type Login struct {
	// URL is the server's page the user opens to approve orOS Drive.
	URL      string
	server   string
	dir      string
	endpoint string
	token    string
	http     *http.Client
}

func sameOrigin(a, b string) bool {
	ua, err1 := url.Parse(a)
	ub, err2 := url.Parse(b)
	return err1 == nil && err2 == nil && ua.Scheme == ub.Scheme && ua.Host == ub.Host && ua.Host != ""
}

func ncPost(ctx context.Context, hc *http.Client, u, body string) (*http.Response, error) {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	r, err := http.NewRequestWithContext(ctx, "POST", u, rd)
	if err != nil {
		return nil, err
	}
	if body != "" {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	r.Header.Set("User-Agent", "orOS-Drive")
	res, err := hc.Do(r)
	if err != nil {
		return nil, unreachable(err)
	}
	return res, nil
}

// StartNextcloud asks the server for a login (rawURL may be the
// server, a page of it or its WebDAV address). Open Login.URL, then
// call Wait.
func StartNextcloud(ctx context.Context, rawURL, dir string) (*Login, error) {
	conf, err := NormConf(rawURL, "-", dir)
	if err != nil {
		return nil, err
	}
	server := phpRe.ReplaceAllString(conf.URL, "")
	hc := &http.Client{Timeout: time.Minute}
	res, err := ncPost(ctx, hc, server+"/index.php/login/v2", "")
	if err != nil {
		if errors.Is(err, ErrUnreachable) {
			return nil, ErrUnreachable
		}
		return nil, err
	}
	if res.StatusCode == 404 {
		drain(res)
		return nil, ErrNotNextcloud
	}
	if err := check(res, "login"); err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var start struct {
		Login string `json:"login"`
		Poll  struct {
			Token    string `json:"token"`
			Endpoint string `json:"endpoint"`
		} `json:"poll"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&start) != nil ||
		start.Poll.Token == "" || !sameOrigin(start.Poll.Endpoint, server) ||
		!(strings.HasPrefix(start.Login, "https://") || strings.HasPrefix(start.Login, "http://")) {
		return nil, ErrNotNextcloud
	}
	return &Login{URL: start.Login, server: server, dir: conf.Dir,
		endpoint: start.Poll.Endpoint, token: start.Poll.Token, http: hc}, nil
}

// Wait polls until the user approved, then finds the user's files
// folder, checks it (Probe) and returns the connection. A connection
// that fails the check deletes its new app password again.
func (l *Login) Wait(ctx context.Context) (*Client, error) {
	ctx, cancel := context.WithTimeout(ctx, loginTimeout)
	defer cancel()
	var got struct {
		LoginName   string `json:"loginName"`
		AppPassword string `json:"appPassword"`
	}
	for {
		res, err := ncPost(ctx, l.http, l.endpoint, "token="+url.QueryEscape(l.token))
		if err == nil {
			switch {
			case res.StatusCode == 200:
				err = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&got)
				res.Body.Close()
				if err != nil || got.LoginName == "" || got.AppPassword == "" {
					return nil, ErrNotNextcloud
				}
			case res.StatusCode == 404 || res.StatusCode == 429:
				drain(res) // not approved yet
			default:
				if err := check(res, "login"); err != nil {
					return nil, err
				}
			}
		} else if !errors.Is(err, ErrUnreachable) {
			return nil, err
		}
		if got.AppPassword != "" {
			break
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil, ErrLoginExpired
			}
			return nil, ctx.Err()
		case <-time.After(PollEvery):
		}
	}
	tmp := New(Conf{URL: l.server, User: got.LoginName}, got.AppPassword)
	uid := tmp.userID()
	conf := Conf{URL: l.server + "/remote.php/dav/files/" + url.PathEscape(uid), User: got.LoginName, Dir: l.dir}
	c, err := Connect(conf, got.AppPassword)
	if err != nil {
		// The new app password would stay unused on the server.
		_ = New(conf, got.AppPassword).Revoke()
		return nil, err
	}
	return c, nil
}

var principalRe = regexp.MustCompile(`/principals/users/([^/<]+)/?<`)

// userID is ncUserId: the id the files live under (it can differ from
// the login name: LDAP, e-mail logins). Falls back to the login name.
func (c *Client) userID() string {
	body := `<?xml version="1.0"?><d:propfind xmlns:d="DAV:"><d:prop><d:current-user-principal/></d:prop></d:propfind>`
	res, err := c.req("PROPFIND", c.Conf.URL+"/remote.php/dav/", map[string]string{
		"Depth": "0", "Content-Type": "application/xml; charset=utf-8",
	}, []byte(body))
	if err != nil {
		return c.Conf.User
	}
	defer res.Body.Close()
	if res.StatusCode != 207 {
		return c.Conf.User
	}
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	m := principalRe.FindSubmatch(b)
	if m == nil {
		return c.Conf.User
	}
	if id, err := url.PathUnescape(string(m[1])); err == nil && id != "" {
		return id
	}
	return c.Conf.User
}
