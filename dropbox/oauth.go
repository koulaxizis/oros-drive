package dropbox

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"time"
)

// LoopbackRedirect is the redirect orOS's Dropbox app already allows
// for orOS Portable. Dropbox wants an exact match, so the port is fixed.
const LoopbackRedirect = "http://localhost:47800/"

// PKCE is one sign-in attempt.
type PKCE struct {
	Verifier  string
	Challenge string
	State     string
}

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// NewPKCE makes a verifier (32 random bytes, like sync.js), its S256
// challenge and a state value.
func NewPKCE() PKCE {
	v := make([]byte, 32)
	s := make([]byte, 16)
	if _, err := rand.Read(v); err != nil {
		panic(err)
	}
	if _, err := rand.Read(s); err != nil {
		panic(err)
	}
	ver := b64url(v)
	sum := sha256.Sum256([]byte(ver))
	return PKCE{Verifier: ver, Challenge: b64url(sum[:]), State: b64url(s)}
}

// AuthorizeURL is the Dropbox sign-in page. redirect "" = Dropbox shows
// the code on its page for the user to paste (no local port needed).
func (c *Client) AuthorizeURL(p PKCE, redirect string) string {
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {c.AppKey},
		"token_access_type":     {"offline"},
		"code_challenge":        {p.Challenge},
		"code_challenge_method": {"S256"},
	}
	if redirect != "" {
		q.Set("redirect_uri", redirect)
		q.Set("state", p.State)
	}
	return c.AuthURL + "?" + q.Encode()
}

// Exchange trades the code for tokens (and keeps them in the client).
func (c *Client) Exchange(code string, p PKCE, redirect string) (Tokens, error) {
	form := url.Values{
		"grant_type": {"authorization_code"}, "code": {code},
		"client_id": {c.AppKey}, "code_verifier": {p.Verifier},
	}
	if redirect != "" {
		form.Set("redirect_uri", redirect)
	}
	t, err := c.postToken(form)
	if err != nil {
		return Tokens{}, err
	}
	if t.RefreshToken == "" {
		return Tokens{}, errors.New("dropbox gave no refresh token")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.store(t), nil
}

// WaitForCode listens on localhost:47800 (IPv4 and, when possible,
// IPv6) for the redirect back from Dropbox and returns the code.
// Port busy usually means orOS Portable is running: close it, or sign
// in with the paste-the-code way.
func WaitForCode(ctx context.Context, p PKCE, addrs ...string) (string, error) {
	if len(addrs) == 0 {
		addrs = []string{"127.0.0.1:47800", "[::1]:47800"}
	}
	var lns []net.Listener
	for i, a := range addrs {
		ln, err := net.Listen("tcp", a)
		if err != nil {
			if i == 0 {
				return "", fmt.Errorf("cannot listen on %s (is orOS Portable running?): %w", a, err)
			}
			continue
		}
		lns = append(lns, ln)
	}
	done := make(chan codeResult, 1)
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: codeHandler(p, done)}
	for _, ln := range lns {
		go srv.Serve(ln)
	}
	defer srv.Close()
	select {
	case r := <-done:
		return r.code, r.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

type codeResult struct {
	code string
	err  error
}

// codeHandler answers Dropbox's redirect back to localhost and hands
// the code (or the reason there is none) to done, once.
func codeHandler(p PKCE, done chan<- codeResult) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/" || (q.Get("code") == "" && q.Get("error") == "") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		var res codeResult
		switch {
		case q.Get("state") != p.State:
			res.err = errors.New("sign-in answer with a wrong state (ignored)")
		case q.Get("error") != "":
			res.err = fmt.Errorf("sign-in cancelled: %s", q.Get("error"))
		default:
			res.code = q.Get("code")
		}
		msg := "orOS Drive is signed in to Dropbox. You can close this tab."
		if res.err != nil {
			msg = "orOS Drive: " + res.err.Error()
		}
		fmt.Fprintf(w, "<!doctype html><meta charset=utf-8><title>orOS Drive</title><p style=\"font:16px system-ui;margin:3em\">%s</p>", html.EscapeString(msg))
		select {
		case done <- res:
		default:
		}
	})
}
