package app

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"time"
)

// The Dropbox sign-in when orOS holds localhost:47800 (installed orOS,
// Portable): Dropbox sends the answer there, and orOS's launcher, seeing
// a state that HandoffState made, sends the browser on to
// http://127.0.0.1:<port>/oauth/done with the same query. Only the
// state of the sign-in this app started is taken: it is 16 random
// bytes, so no other page can sign this app in to another account.

type codeResult struct {
	code string
	err  error
}

type handoff struct {
	state string
	done  chan codeResult
}

// orosAddr is where orOS answers (a variable for tests).
var orosAddr = "http://127.0.0.1:47800"

// orosTakesSignIn: the program on 47800 is an orOS that passes the
// sign-in on (GET /__oros/drive-signin answers {"ok":true}).
func orosTakesSignIn() bool {
	c := &http.Client{Timeout: 2 * time.Second}
	res, err := c.Get(orosAddr + "/__oros/drive-signin")
	if err != nil {
		return false
	}
	defer res.Body.Close()
	var out struct {
		OK bool `json:"ok"`
	}
	return res.StatusCode == 200 && json.NewDecoder(res.Body).Decode(&out) == nil && out.OK
}

// HandoffState is the sign-in state that tells orOS which port to send
// the answer to: "orosdrive-<port>-<random>".
func HandoffState(port int, random string) string {
	return fmt.Sprintf("orosdrive-%d-%s", port, random)
}

var handoffRE = regexp.MustCompile(`^orosdrive-(\d{5})-[A-Za-z0-9_-]{16,64}$`)

// HandoffPort reads the port out of a HandoffState; 0 = not one, or a
// port outside the settings page's range (orOS sends nowhere else).
func HandoffPort(state string) int {
	m := handoffRE.FindStringSubmatch(state)
	if m == nil {
		return 0
	}
	p, _ := strconv.Atoi(m[1])
	if p < uiPort || p >= uiPort+10 {
		return 0
	}
	return p
}

var errNoHandoff = errors.New("no sign-in is waiting for this answer")

// takeHandoff hands the answer to the waiting sign-in when the state is
// its own.
func (s *Service) takeHandoff(state, code, failure string) error {
	s.mu.Lock()
	h := s.handoff
	if h == nil || subtle.ConstantTimeCompare([]byte(state), []byte(h.state)) != 1 {
		s.mu.Unlock()
		return errNoHandoff
	}
	s.handoff = nil
	s.mu.Unlock()
	r := codeResult{code: code}
	switch {
	case failure != "":
		r = codeResult{err: fmt.Errorf("sign-in cancelled: %s", failure)}
	case code == "":
		r = codeResult{err: errors.New("sign-in answer without a code")}
	}
	h.done <- r
	return r.err
}
