// Package dropbox is a small Dropbox API v2 client for the orOS vault:
// the same endpoints, conditional writes and sign-in (PKCE, offline
// refresh token) that sync.js uses, under the same app key, so the
// client sees the same app folder and the same /vault.
package dropbox

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/koulaxizis/oros-drive/vault"
)

// AppKey is orOS's Dropbox app (App folder access), the one sync.js
// uses (DROPBOX_APP_KEY). Every user signs in with their own account.
const AppKey = "wnohlxz79ie8w3e"

const (
	defaultAPI     = "https://api.dropboxapi.com/2/"
	defaultContent = "https://content.dropboxapi.com/2/"
	defaultToken   = "https://api.dropboxapi.com/oauth2/token"
	defaultAuth    = "https://www.dropbox.com/oauth2/authorize"

	sessionChunk  = 4 << 20   // SESSION_CHUNK
	singleShotMax = 150 << 20 // SINGLE_SHOT_MAX
	maxRetries    = 4         // 429 / 5xx
)

// ErrNotConnected: no refresh token (sign in first).
var ErrNotConnected = errors.New("not-connected")

// Tokens are what a sign-in returns. Only Refresh needs keeping.
type Tokens struct {
	Access  string    `json:"access,omitempty"`
	Refresh string    `json:"refresh"`
	Expiry  time.Time `json:"expiry,omitempty"`
}

// Client talks to one user's Dropbox. It implements vault.Store with
// keys under Root (default "/vault" in the app folder).
type Client struct {
	AppKey      string
	HTTP        *http.Client
	APIBase     string
	ContentBase string
	TokenURL    string
	AuthURL     string
	Root        string
	// OnTokens is called after a refresh (to persist a rotated token).
	OnTokens func(Tokens)

	// SingleShotMax and SessionChunk can be lowered by tests.
	SingleShotMax int
	SessionChunk  int

	mu     sync.Mutex
	tokens Tokens
	sleep  func(time.Duration)
}

// New returns a client for the orOS app with these tokens.
func New(t Tokens) *Client {
	return &Client{
		AppKey: AppKey, HTTP: &http.Client{Timeout: 10 * time.Minute},
		APIBase: defaultAPI, ContentBase: defaultContent,
		TokenURL: defaultToken, AuthURL: defaultAuth, Root: "/vault",
		SingleShotMax: singleShotMax, SessionChunk: sessionChunk,
		tokens: t, sleep: time.Sleep,
	}
}

// Tokens returns the current tokens.
func (c *Client) Tokens() Tokens {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tokens
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	AccountID    string `json:"account_id"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

func (c *Client) postToken(form url.Values) (*tokenResponse, error) {
	res, err := c.HTTP.PostForm(c.TokenURL, form)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var t tokenResponse
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	_ = json.Unmarshal(body, &t)
	if res.StatusCode != 200 || t.AccessToken == "" {
		msg := t.ErrorDesc
		if msg == "" {
			msg = t.Error
		}
		return nil, fmt.Errorf("dropbox token: %d %s", res.StatusCode, msg)
	}
	return &t, nil
}

func (c *Client) store(t *tokenResponse) Tokens {
	exp := t.ExpiresIn
	if exp == 0 {
		exp = 14400
	}
	c.tokens.Access = t.AccessToken
	if t.RefreshToken != "" {
		c.tokens.Refresh = t.RefreshToken
	}
	c.tokens.Expiry = time.Now().Add(time.Duration(exp-300) * time.Second)
	return c.tokens
}

// token returns a fresh access token, refreshing when needed (one
// refresh at a time: a rotating refresh token must not be burnt twice).
func (c *Client) token(force bool) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !force && c.tokens.Access != "" && time.Now().Before(c.tokens.Expiry) {
		return c.tokens.Access, nil
	}
	if c.tokens.Refresh == "" {
		return "", ErrNotConnected
	}
	t, err := c.postToken(url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {c.tokens.Refresh}, "client_id": {c.AppKey},
	})
	if err != nil {
		return "", err
	}
	saved := c.store(t)
	if c.OnTokens != nil {
		c.OnTokens(saved)
	}
	return saved.Access, nil
}

// apiArg is JSON for the Dropbox-API-Arg header: HTTP headers carry
// ASCII only, so every other character is escaped as \uXXXX.
func apiArg(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	for _, r := range string(b) {
		switch {
		case r < 0x80:
			sb.WriteRune(r)
		case r > 0xFFFF:
			r -= 0x10000
			fmt.Fprintf(&sb, `\u%04x\u%04x`, 0xD800+(r>>10), 0xDC00+(r&0x3FF))
		default:
			fmt.Fprintf(&sb, `\u%04x`, r)
		}
	}
	return sb.String(), nil
}

// do sends one request with a fresh token, retrying a 401 once after a
// refresh and 429 / 5xx a few times (Retry-After, else backoff).
func (c *Client) do(build func(token string) (*http.Request, error)) (*http.Response, error) {
	refreshed := false
	for attempt := 0; ; attempt++ {
		tok, err := c.token(false)
		if err != nil {
			return nil, err
		}
		req, err := build(tok)
		if err != nil {
			return nil, err
		}
		res, err := c.HTTP.Do(req)
		if err != nil {
			if attempt < maxRetries {
				c.sleep(backoff(attempt, ""))
				continue
			}
			return nil, err
		}
		switch {
		case res.StatusCode == 401 && !refreshed:
			res.Body.Close()
			refreshed = true
			if _, err := c.token(true); err != nil {
				return nil, err
			}
			continue
		case (res.StatusCode == 429 || res.StatusCode >= 500) && attempt < maxRetries:
			ra := res.Header.Get("Retry-After")
			res.Body.Close()
			c.sleep(backoff(attempt, ra))
			continue
		}
		return res, nil
	}
}

func backoff(attempt int, retryAfter string) time.Duration {
	if s, err := strconv.Atoi(retryAfter); err == nil && s >= 0 && s < 600 {
		return time.Duration(s) * time.Second
	}
	return time.Duration(1<<attempt) * time.Second
}

func (c *Client) rpc(endpoint string, args any) (*http.Response, error) {
	body, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	return c.do(func(tok string) (*http.Request, error) {
		req, err := http.NewRequest("POST", c.APIBase+endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	})
}

func (c *Client) content(endpoint string, arg any, body []byte) (*http.Response, error) {
	a, err := apiArg(arg)
	if err != nil {
		return nil, err
	}
	return c.do(func(tok string) (*http.Request, error) {
		req, err := http.NewRequest("POST", c.ContentBase+endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("Dropbox-API-Arg", a)
		return req, nil
	})
}

// apiError is a Dropbox error body.
type apiError struct {
	Summary string `json:"error_summary"`
}

func readError(res *http.Response) string {
	defer res.Body.Close()
	var e apiError
	b, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if json.Unmarshal(b, &e) == nil && e.Summary != "" {
		return e.Summary
	}
	if utf8.Valid(b) && len(b) < 300 {
		return strings.TrimSpace(string(b))
	}
	return ""
}

func isNotFound(summary string) bool { return strings.Contains(summary, "not_found") }

func (c *Client) path(key string) string {
	return c.Root + "/" + strings.TrimLeft(key, "/")
}

// commit = sync.js adapterCommit (SY-D4).
func commit(path string, mode vault.PutMode) map[string]any {
	m := map[string]any{"path": path, "mode": "overwrite", "autorename": false, "mute": true}
	switch {
	case mode.IsCreateOnly():
		m["mode"] = "add"
		m["strict_conflict"] = true
	case mode.IsUpdate():
		m["mode"] = map[string]any{".tag": "update", "update": mode.Rev}
		m["strict_conflict"] = true
	}
	return m
}

// Get downloads a key. A missing key is (nil, "", nil).
func (c *Client) Get(key string) ([]byte, string, error) {
	res, err := c.content("files/download", map[string]string{"path": c.path(key)}, nil)
	if err != nil {
		return nil, "", err
	}
	if res.StatusCode == 409 {
		sum := readError(res)
		if isNotFound(sum) {
			return nil, "", nil
		}
		return nil, "", fmt.Errorf("download failed: 409 %s", sum)
	}
	if res.StatusCode != 200 {
		return nil, "", fmt.Errorf("download failed: %d %s", res.StatusCode, readError(res))
	}
	defer res.Body.Close()
	var meta struct {
		Rev string `json:"rev"`
	}
	if err := json.Unmarshal([]byte(res.Header.Get("Dropbox-API-Result")), &meta); err != nil || meta.Rev == "" {
		return nil, "", errors.New("download: no revision in the response")
	}
	b, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, "", err
	}
	return b, meta.Rev, nil
}

func (c *Client) finishUpload(res *http.Response, mode vault.PutMode) (string, error) {
	if res.StatusCode == 409 && !mode.IsOverwrite() {
		sum := readError(res)
		if strings.Contains(sum, "conflict") {
			return "", vault.ErrConflict
		}
		return "", fmt.Errorf("upload failed: 409 %s", sum)
	}
	if res.StatusCode != 200 {
		return "", fmt.Errorf("upload failed: %d %s", res.StatusCode, readError(res))
	}
	defer res.Body.Close()
	var meta struct {
		Rev string `json:"rev"`
	}
	_ = json.NewDecoder(res.Body).Decode(&meta)
	return meta.Rev, nil
}

// Put uploads a key with the given condition (single shot up to
// 150 MB, else an upload session in 4 MiB chunks).
func (c *Client) Put(key string, data []byte, mode vault.PutMode) (string, error) {
	path := c.path(key)
	if len(data) <= c.SingleShotMax {
		res, err := c.content("files/upload", commit(path, mode), data)
		if err != nil {
			return "", err
		}
		return c.finishUpload(res, mode)
	}
	chunk := c.SessionChunk
	first := data[:min(chunk, len(data))]
	res, err := c.content("files/upload_session/start", map[string]any{"close": false}, first)
	if err != nil {
		return "", err
	}
	if res.StatusCode != 200 {
		return "", fmt.Errorf("session start failed: %d %s", res.StatusCode, readError(res))
	}
	var started struct {
		SessionID string `json:"session_id"`
	}
	err = json.NewDecoder(res.Body).Decode(&started)
	res.Body.Close()
	if err != nil || started.SessionID == "" {
		return "", errors.New("session start: no session id")
	}
	off := len(first)
	for off < len(data) {
		end := min(off+chunk, len(data))
		res, err := c.content("files/upload_session/append_v2", map[string]any{
			"cursor": map[string]any{"session_id": started.SessionID, "offset": off}, "close": false,
		}, data[off:end])
		if err != nil {
			return "", err
		}
		if res.StatusCode != 200 {
			return "", fmt.Errorf("session append failed: %d %s", res.StatusCode, readError(res))
		}
		res.Body.Close()
		off = end
	}
	res, err = c.content("files/upload_session/finish", map[string]any{
		"cursor": map[string]any{"session_id": started.SessionID, "offset": off},
		"commit": commit(path, mode),
	}, nil)
	if err != nil {
		return "", err
	}
	return c.finishUpload(res, mode)
}

// Rev returns a key's revision ("" and false when missing).
func (c *Client) Rev(key string) (string, bool, error) {
	res, err := c.rpc("files/get_metadata", map[string]string{"path": c.path(key)})
	if err != nil {
		return "", false, err
	}
	if res.StatusCode == 409 {
		sum := readError(res)
		if isNotFound(sum) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("metadata failed: 409 %s", sum)
	}
	if res.StatusCode != 200 {
		return "", false, fmt.Errorf("metadata failed: %d %s", res.StatusCode, readError(res))
	}
	defer res.Body.Close()
	var meta struct {
		Tag string `json:".tag"`
		Rev string `json:"rev"`
	}
	if err := json.NewDecoder(res.Body).Decode(&meta); err != nil {
		return "", false, err
	}
	if meta.Tag == "folder" {
		return "", false, nil
	}
	return meta.Rev, true, nil
}

// Delete removes a key; already gone counts as deleted.
func (c *Client) Delete(key string) error {
	res, err := c.rpc("files/delete_v2", map[string]string{"path": c.path(key)})
	if err != nil {
		return err
	}
	if res.StatusCode == 200 {
		res.Body.Close()
		return nil
	}
	sum := readError(res)
	if res.StatusCode == 409 && isNotFound(sum) {
		return nil
	}
	return fmt.Errorf("delete failed: %d %s", res.StatusCode, sum)
}

// List returns every file key under prefix, lower-cased (Dropbox
// path_lower), relative to Root.
func (c *Client) List(prefix string) ([]string, error) {
	infos, err := c.ListInfo(prefix)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(infos))
	for i, o := range infos {
		out[i] = o.Key
	}
	return out, nil
}

// ListInfo is List with each file's size and server_modified.
func (c *Client) ListInfo(prefix string) ([]vault.ObjectInfo, error) {
	base := strings.ToLower(c.path(prefix))
	root := strings.ToLower(c.Root) + "/"
	var out []vault.ObjectInfo
	res, err := c.rpc("files/list_folder", map[string]any{"path": c.Root, "recursive": true, "limit": 2000})
	for {
		if err != nil {
			return nil, err
		}
		if res.StatusCode == 409 {
			sum := readError(res)
			if isNotFound(sum) {
				return out, nil // no /vault yet
			}
			return nil, fmt.Errorf("list failed: 409 %s", sum)
		}
		if res.StatusCode != 200 {
			return nil, fmt.Errorf("list failed: %d %s", res.StatusCode, readError(res))
		}
		var page struct {
			Entries []struct {
				Tag            string    `json:".tag"`
				PathLower      string    `json:"path_lower"`
				Size           int64     `json:"size"`
				ServerModified time.Time `json:"server_modified"`
			} `json:"entries"`
			HasMore bool   `json:"has_more"`
			Cursor  string `json:"cursor"`
		}
		err = json.NewDecoder(res.Body).Decode(&page)
		res.Body.Close()
		if err != nil {
			return nil, err
		}
		for _, e := range page.Entries {
			if e.Tag == "file" && strings.HasPrefix(e.PathLower, base) && strings.HasPrefix(e.PathLower, root) {
				out = append(out, vault.ObjectInfo{Key: strings.TrimPrefix(e.PathLower, root), Size: e.Size, Modified: e.ServerModified})
			}
		}
		if !page.HasMore || page.Cursor == "" {
			return out, nil
		}
		res, err = c.rpc("files/list_folder/continue", map[string]string{"cursor": page.Cursor})
	}
}

// Account is the signed-in user (for "status").
type Account struct {
	Email string
	Name  string
}

// CurrentAccount asks who is signed in.
func (c *Client) CurrentAccount() (*Account, error) {
	res, err := c.do(func(tok string) (*http.Request, error) {
		req, err := http.NewRequest("POST", c.APIBase+"users/get_current_account", nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		return req, nil
	})
	if err != nil {
		return nil, err
	}
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("account: %d %s", res.StatusCode, readError(res))
	}
	defer res.Body.Close()
	var a struct {
		Email string `json:"email"`
		Name  struct {
			Display string `json:"display_name"`
		} `json:"name"`
	}
	if err := json.NewDecoder(res.Body).Decode(&a); err != nil {
		return nil, err
	}
	return &Account{Email: a.Email, Name: a.Name.Display}, nil
}

var _ vault.Store = (*Client)(nil)

// Revoke ends this sign-in on Dropbox's side (best effort on logout).
func (c *Client) Revoke() error {
	res, err := c.rpc("auth/token/revoke", nil)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("revoke: %d", res.StatusCode)
	}
	return nil
}
