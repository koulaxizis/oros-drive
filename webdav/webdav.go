// Package webdav is a WebDAV client for the orOS vault: Nextcloud,
// ownCloud, Synology, QNAP or any WebDAV server. It follows sync.js's
// webdavProvider (orOS WebDAV W2/W3) exactly, so orOS in the browser
// and orOS Drive see the same vault:
//
//   - layout under <url><dir> (default "/orOS"): vault/<key>;
//   - the ETag is the revision; create only = If-None-Match: *;
//     replace only that revision = If-Match: <etag>; refused = 412
//     (the Dropbox rev and strict_conflict of SY-D4);
//   - Probe proves all of this on the real server before anything is
//     saved (davProbe): a server that ignores the conditions would let
//     two devices overwrite each other in silence;
//   - big immutable objects go up in Nextcloud chunks (davPutChunked).
//
// orOS Drive is not a web page, so it needs no CORS and no relay.
package webdav

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/koulaxizis/oros-drive/vault"
)

// The reasons a connection is refused: the same codes as sync.js
// (sync.err.dav-…), so both apps say the same sentence.
var (
	ErrURL         = errors.New("dav-url")         // not https (http only for this computer), or a user/query in it
	ErrMissing     = errors.New("dav-missing")     // no user or password
	ErrAuth        = errors.New("dav-auth")        // the server refused the user or password
	ErrNotDAV      = errors.New("dav-notdav")      // no WebDAV at this address
	ErrReadOnly    = errors.New("dav-readonly")    // cannot create the folder or write
	ErrConditions  = errors.New("dav-conditions")  // ignores If-Match / If-None-Match, or weak or slow ETags
	ErrETag        = errors.New("dav-etag")        // sends no ETag
	ErrUnreachable = errors.New("dav-unreachable") // offline, DNS, TLS
)

// Code is the "dav-…" code an error carries ("" for none): the
// settings page turns it into one sentence.
func Code(err error) string {
	for _, e := range []error{ErrURL, ErrMissing, ErrAuth, ErrNotDAV, ErrReadOnly, ErrConditions,
		ErrETag, ErrUnreachable, ErrNotNextcloud, ErrLoginExpired} {
		if errors.Is(err, e) {
			return e.Error()
		}
	}
	return ""
}

// DefaultDir is the folder orOS keeps its data in on the server.
const DefaultDir = "/orOS"

// Root is the vault folder under Dir (sync.js DAV_ROOT).
const Root = "vault"

// ChunkSize is sync.js DAV_CHUNK: objects over it go up in pieces on
// Nextcloud.
const ChunkSize = 10 << 20

const propfind = `<?xml version="1.0" encoding="utf-8"?><d:propfind xmlns:d="DAV:"><d:prop>` +
	`<d:resourcetype/><d:getcontentlength/><d:getlastmodified/><d:getetag/></d:prop></d:propfind>`

// Conf is one WebDAV connection (sync.js oros-dav-conf, without relay).
type Conf struct {
	URL  string `json:"url"` // no trailing slash
	User string `json:"user"`
	Dir  string `json:"dir"` // "/a/b"
}

// NormDir is davNormDir: "a//b/" → "/a/b"; empty → "/orOS"; ".." refused.
func NormDir(d string) (string, error) {
	var parts []string
	for _, x := range strings.Split(d, "/") {
		switch x {
		case "", ".":
		case "..":
			return "", ErrURL
		default:
			parts = append(parts, x)
		}
	}
	if len(parts) == 0 {
		return DefaultDir, nil
	}
	return "/" + strings.Join(parts, "/"), nil
}

func isLocal(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// NormConf is davNormConf: https only; plain http only for this
// computer (a local test server); no user, password, query or
// fragment in the address.
func NormConf(rawURL, user, dir string) (Conf, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" {
		return Conf{}, ErrURL
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && isLocal(u.Hostname())) {
		return Conf{}, ErrURL
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return Conf{}, ErrURL
	}
	d, err := NormDir(dir)
	if err != nil {
		return Conf{}, err
	}
	return Conf{
		URL:  u.Scheme + "://" + u.Host + strings.TrimRight(u.EscapedPath(), "/"),
		User: strings.TrimSpace(user),
		Dir:  d,
	}, nil
}

// CloudID names this cloud like sync.js davCloudId.
func (c Conf) CloudID() string { return "webdav:" + c.User + "@" + c.URL + c.Dir }

// Account is how the connection is shown: "chris@cloud.example.com/orOS".
func (c Conf) Account() string {
	host := c.URL
	if u, err := url.Parse(c.URL); err == nil {
		host = u.Host
	}
	return c.User + "@" + host + c.Dir
}

func esc(rel string) string {
	parts := strings.Split(rel, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// Client is one user's WebDAV folder. It implements vault.Store and
// vault.Inventory with keys under <Dir>/vault.
type Client struct {
	Conf Conf
	Pass string
	HTTP *http.Client
	// ChunkSize can be lowered by tests.
	ChunkSize int
	sleep     func(time.Duration)
}

// New returns a client; nothing is checked (see Connect).
func New(c Conf, pass string) *Client {
	return &Client{Conf: c, Pass: pass, HTTP: &http.Client{Timeout: 10 * time.Minute},
		ChunkSize: ChunkSize, sleep: time.Sleep}
}

func (c *Client) base() string { return c.Conf.URL + esc(c.Conf.Dir) }

func (c *Client) at(rel string) string {
	if rel == "" {
		return c.base()
	}
	return c.base() + "/" + esc(rel)
}

func keyRel(key string) (string, error) {
	key = strings.TrimLeft(key, "/")
	if key == "" || strings.ContainsRune(key, '\\') {
		return "", fmt.Errorf("bad key %q", key)
	}
	for _, p := range strings.Split(key, "/") {
		if p == "" || p == "." || p == ".." {
			return "", fmt.Errorf("bad key %q", key)
		}
	}
	return Root + "/" + key, nil
}

// req sends one request to an absolute URL. Reads (GET, HEAD,
// PROPFIND) are tried again on a network error or 429 / 5xx; writes
// never are, because a write that landed and is sent again would
// come back 412 and read as someone else's change.
func (c *Client) req(method, abs string, h map[string]string, body []byte) (*http.Response, error) {
	retry := method == "GET" || method == "HEAD" || method == "PROPFIND"
	for attempt := 0; ; attempt++ {
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(body)
		}
		r, err := http.NewRequest(method, abs, rd)
		if err != nil {
			return nil, err
		}
		r.SetBasicAuth(c.Conf.User, c.Pass)
		r.Header.Set("User-Agent", "orOS-Drive")
		for k, v := range h {
			r.Header.Set(k, v)
		}
		if body != nil {
			r.ContentLength = int64(len(body))
		}
		res, err := c.HTTP.Do(r)
		if err != nil {
			if retry && attempt < 3 {
				c.sleep(time.Duration(1<<attempt) * time.Second)
				continue
			}
			return nil, unreachable(err)
		}
		if retry && attempt < 3 && (res.StatusCode == 429 || res.StatusCode >= 500) {
			drain(res)
			c.sleep(time.Duration(1<<attempt) * time.Second)
			continue
		}
		return res, nil
	}
}

func unreachable(err error) error {
	var ne net.Error
	var ue *url.Error
	if errors.As(err, &ne) || errors.As(err, &ue) {
		return fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	return err
}

func drain(res *http.Response) {
	io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
	res.Body.Close()
}

func ok(res *http.Response) bool { return res.StatusCode >= 200 && res.StatusCode < 300 }

// check turns a failed answer into an error (and closes it).
func check(res *http.Response, what string) error {
	if ok(res) {
		return nil
	}
	drain(res)
	if res.StatusCode == 401 || res.StatusCode == 403 {
		return fmt.Errorf("%w: %d", ErrAuth, res.StatusCode)
	}
	return fmt.Errorf("%s failed: %d", what, res.StatusCode)
}

// etag is davEtag: quoted exactly as the server compares it in
// If-Match. A weak ETag (W/"…") is kept as it is (Probe refuses it).
func etag(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || strings.HasPrefix(v, "W/") || strings.HasPrefix(v, `"`) {
		return v
	}
	return `"` + v + `"`
}

func etagOf(res *http.Response) string { return etag(res.Header.Get("ETag")) }

// ---------- multistatus ----------

type multistatus struct {
	Responses []struct {
		Href     string `xml:"DAV: href"`
		Propstat []struct {
			Prop struct {
				Type struct {
					Collection *struct{} `xml:"DAV: collection"`
				} `xml:"DAV: resourcetype"`
				Length   string `xml:"DAV: getcontentlength"`
				Modified string `xml:"DAV: getlastmodified"`
				ETag     string `xml:"DAV: getetag"`
			} `xml:"DAV: prop"`
		} `xml:"DAV: propstat"`
	} `xml:"DAV: response"`
}

type entry struct {
	rel      string // under the base folder
	dir      bool
	size     int64
	modified time.Time
	etag     string
}

// pathOf is davPathOf: the decoded path of an href, no trailing "/".
func pathOf(href string) string {
	p := strings.TrimSpace(href)
	if u, err := url.Parse(p); err == nil {
		if u.Scheme != "" {
			p = u.EscapedPath()
		} else {
			p = strings.SplitN(strings.SplitN(p, "?", 2)[0], "#", 2)[0]
		}
	}
	if d, err := url.PathUnescape(p); err == nil {
		p = d
	}
	return strings.TrimRight(p, "/")
}

// children is davChildren: what folder rel holds (nil, nil when the
// folder does not exist).
func (c *Client) children(rel string) ([]entry, error) {
	res, err := c.req("PROPFIND", c.at(rel), map[string]string{
		"Depth": "1", "Content-Type": "application/xml; charset=utf-8",
	}, []byte(propfind))
	if err != nil {
		return nil, err
	}
	if res.StatusCode == 404 {
		drain(res)
		return nil, nil
	}
	if err := check(res, "list"); err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var ms multistatus
	if err := xml.NewDecoder(io.LimitReader(res.Body, 256<<20)).Decode(&ms); err != nil {
		return nil, fmt.Errorf("list: %w", err)
	}
	basePath := pathOf(c.base())
	self := basePath
	if rel != "" {
		self += "/" + rel
	}
	out := []entry{}
	for _, r := range ms.Responses {
		p := pathOf(r.Href)
		if p == self || !strings.HasPrefix(p, self+"/") {
			continue
		}
		e := entry{rel: p[len(basePath)+1:]}
		for _, ps := range r.Propstat {
			pr := ps.Prop
			if pr.Type.Collection != nil {
				e.dir = true
			}
			if n, err := strconv.ParseInt(strings.TrimSpace(pr.Length), 10, 64); err == nil {
				e.size = n
			}
			if t, err := http.ParseTime(strings.TrimSpace(pr.Modified)); err == nil {
				e.modified = t
			}
			if pr.ETag != "" {
				e.etag = etag(pr.ETag)
			}
		}
		out = append(out, e)
	}
	return out, nil
}

// ---------- vault.Store ----------

// Get downloads a key. A missing key is (nil, "", nil).
func (c *Client) Get(key string) ([]byte, string, error) {
	rel, err := keyRel(key)
	if err != nil {
		return nil, "", err
	}
	res, err := c.req("GET", c.at(rel), nil, nil)
	if err != nil {
		return nil, "", err
	}
	if res.StatusCode == 404 {
		drain(res)
		return nil, "", nil
	}
	if err := check(res, "download"); err != nil {
		return nil, "", err
	}
	defer res.Body.Close()
	rev := etagOf(res)
	if rev == "" {
		return nil, "", errors.New("download: no ETag in the response")
	}
	b, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, "", err
	}
	return b, rev, nil
}

func parent(rel string) string {
	if i := strings.LastIndex(rel, "/"); i >= 0 {
		return rel[:i]
	}
	return ""
}

// mkcols is davMkcols: MKCOL every folder from the base down to rel
// (the base itself included). 405 = already there.
func (c *Client) mkcols(rel string) error {
	var urls []string
	dir := strings.Split(strings.Trim(c.Conf.Dir, "/"), "/")
	for i := range dir {
		if dir[i] != "" {
			urls = append(urls, c.Conf.URL+esc("/"+strings.Join(dir[:i+1], "/")))
		}
	}
	var acc []string
	for _, p := range strings.Split(rel, "/") {
		if p != "" {
			acc = append(acc, p)
			urls = append(urls, c.at(strings.Join(acc, "/")))
		}
	}
	for _, u := range urls {
		res, err := c.req("MKCOL", u+"/", nil, nil)
		if err != nil {
			return err
		}
		if res.StatusCode == 405 {
			drain(res)
			continue
		}
		if err := check(res, "mkcol"); err != nil {
			return err
		}
		drain(res)
	}
	return nil
}

// put is davPut: PUT with a condition; a missing parent folder
// (409 / 404) is created once and the write tried again.
func (c *Client) put(rel string, data []byte, mode vault.PutMode) (*http.Response, error) {
	h := map[string]string{"Content-Type": "application/octet-stream"}
	switch {
	case mode.IsCreateOnly():
		h["If-None-Match"] = "*"
	case mode.IsUpdate():
		h["If-Match"] = mode.Rev
	}
	res, err := c.req("PUT", c.at(rel), h, data)
	if err != nil || (res.StatusCode != 409 && res.StatusCode != 404) {
		return res, err
	}
	drain(res)
	if err := c.mkcols(parent(rel)); err != nil {
		return nil, err
	}
	return c.req("PUT", c.at(rel), h, data)
}

// Put uploads a key with the given condition.
func (c *Client) Put(key string, data []byte, mode vault.PutMode) (string, error) {
	rel, err := keyRel(key)
	if err != nil {
		return "", err
	}
	if mode.IsUpdate() && mode.Rev == "" {
		// No If-Match would be an overwrite: never guess, read again.
		return "", vault.ErrConflict
	}
	var res *http.Response
	if mode.IsOverwrite() && len(data) > c.ChunkSize && chunkBase(c.Conf.URL) != "" {
		if res, err = c.putChunked(rel, data); err != nil {
			return "", err
		}
	}
	if res == nil {
		if res, err = c.put(rel, data, mode); err != nil {
			return "", err
		}
	}
	if res.StatusCode == 412 && !mode.IsOverwrite() {
		drain(res)
		return "", vault.ErrConflict
	}
	if err := check(res, "upload"); err != nil {
		return "", err
	}
	drain(res)
	if mode.IsUpdate() && res.StatusCode == 201 {
		// "Created" for a replace: the key was gone (a reset vault) and
		// the server ignored If-Match on a missing file (WsgiDAV does).
		// Take the new file away again and read the cloud anew.
		if err := c.Delete(key); err != nil {
			return "", err
		}
		return "", vault.ErrConflict
	}
	return etagOf(res), nil
}

// Rev returns a key's ETag ("" and false when missing).
func (c *Client) Rev(key string) (string, bool, error) {
	rel, err := keyRel(key)
	if err != nil {
		return "", false, err
	}
	res, err := c.req("HEAD", c.at(rel), nil, nil)
	if err != nil {
		return "", false, err
	}
	if res.StatusCode == 404 {
		drain(res)
		return "", false, nil
	}
	if err := check(res, "metadata"); err != nil {
		return "", false, err
	}
	drain(res)
	return etagOf(res), true, nil
}

// Delete removes a key; already gone counts as deleted.
func (c *Client) Delete(key string) error {
	rel, err := keyRel(key)
	if err != nil {
		return err
	}
	res, err := c.req("DELETE", c.at(rel), nil, nil)
	if err != nil {
		return err
	}
	if res.StatusCode == 404 {
		drain(res)
		return nil
	}
	if err := check(res, "delete"); err != nil {
		return err
	}
	drain(res)
	return nil
}

// List returns every key under prefix, lower-cased like Dropbox's
// path_lower (vault keys are lower-case hex and names).
func (c *Client) List(prefix string) ([]string, error) {
	infos, err := c.ListInfo(prefix)
	out := make([]string, len(infos))
	for i, o := range infos {
		out[i] = o.Key
	}
	return out, err
}

// ListInfo walks the vault folder one level at a time (many servers
// refuse Depth: infinity), like sync.js listPrefix.
func (c *Client) ListInfo(prefix string) ([]vault.ObjectInfo, error) {
	prefix = strings.ToLower(prefix)
	var out []vault.ObjectInfo
	var walk func(rel string) error
	walk = func(rel string) error {
		list, err := c.children(rel)
		if err != nil {
			return err
		}
		for _, e := range list {
			if e.dir {
				if err := walk(e.rel); err != nil {
					return err
				}
				continue
			}
			k := strings.ToLower(e.rel[len(Root)+1:])
			if strings.HasPrefix(k, prefix) {
				out = append(out, vault.ObjectInfo{Key: k, Size: e.size, Modified: e.modified})
			}
		}
		return nil
	}
	err := walk(Root)
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, err
}

var _ vault.Store = (*Client)(nil)
var _ vault.Inventory = (*Client)(nil)

// ---------- Nextcloud chunked upload ----------

var chunkRe = regexp.MustCompile(`^(.*?/remote\.php/dav)/files/([^/]+)(?:/|$)`)

// chunkBase is davChunkBase: Nextcloud's uploads folder for this user,
// "" for other servers.
func chunkBase(u string) string {
	m := chunkRe.FindStringSubmatch(u)
	if m == nil {
		return ""
	}
	return m[1] + "/uploads/" + m[2]
}

func randID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// putChunked is davPutChunked: pieces go into uploads/<user>/<id>,
// then one MOVE of ".file" puts them together at the target. Only for
// plain overwrites (immutable objects), so If-Match keeps its meaning.
// (nil, nil) = this server takes no chunks: send the single PUT.
func (c *Client) putChunked(rel string, data []byte) (*http.Response, error) {
	dest := c.at(rel)
	dir := chunkBase(c.Conf.URL) + "/oros-" + randID()
	hd := map[string]string{"Destination": dest}
	res, err := c.req("MKCOL", dir, hd, nil)
	if err != nil {
		return nil, nil
	}
	if res.StatusCode == 401 || res.StatusCode == 403 {
		return nil, check(res, "upload")
	}
	if !ok(res) {
		drain(res)
		return nil, nil
	}
	drain(res)
	cleanup := func() {
		if r, err := c.req("DELETE", dir+"/", nil, nil); err == nil {
			drain(r)
		}
	}
	n := 0
	for off := 0; off < len(data); {
		end := min(off+c.ChunkSize, len(data))
		n++
		r, err := c.req("PUT", fmt.Sprintf("%s/%05d", dir, n), map[string]string{
			"Destination": dest, "Content-Type": "application/octet-stream",
		}, data[off:end])
		if err != nil {
			cleanup()
			return nil, nil
		}
		if err := check(r, "upload"); err != nil {
			cleanup()
			return nil, err
		}
		drain(r)
		off = end
	}
	move := func() (*http.Response, error) { return c.req("MOVE", dir+"/.file", hd, nil) }
	r, err := move()
	if err == nil && (r.StatusCode == 409 || r.StatusCode == 404) {
		drain(r)
		if err = c.mkcols(parent(rel)); err == nil {
			r, err = move()
		}
	}
	if err != nil {
		cleanup()
		if errors.Is(err, ErrUnreachable) {
			return nil, nil
		}
		return nil, err
	}
	if !ok(r) {
		cleanup()
		return nil, check(r, "upload")
	}
	return r, nil
}

// ---------- connection ----------

// Connect checks the server (Probe) and returns the client.
func Connect(conf Conf, pass string) (*Client, error) {
	if conf.User == "" || pass == "" {
		return nil, ErrMissing
	}
	c := New(conf, pass)
	if err := c.Probe(); err != nil {
		return nil, err
	}
	return c, nil
}

// Probe is davProbe: it proves, on the real server and before
// anything is saved, every promise the vault relies on.
func (c *Client) Probe() (err error) {
	name := ".oros-probe-" + randID()
	abs := c.at(name)
	do := func(method string, h map[string]string, body string) (*http.Response, error) {
		var b []byte
		if body != "" {
			b = []byte(body)
		}
		res, err := c.req(method, abs, h, b)
		if err == nil {
			drain(res)
		}
		return res, err
	}
	defer func() {
		if r, derr := c.req("DELETE", abs, nil, nil); derr == nil {
			drain(r)
		}
		switch {
		case errors.Is(err, ErrUnreachable):
			err = ErrUnreachable
		case errors.Is(err, ErrAuth):
			err = ErrAuth
		}
	}()
	res, err := c.req("PROPFIND", c.Conf.URL+"/", map[string]string{
		"Depth": "0", "Content-Type": "application/xml; charset=utf-8",
	}, []byte(propfind))
	if err != nil {
		return err
	}
	drain(res)
	if res.StatusCode == 401 || res.StatusCode == 403 {
		return ErrAuth
	}
	if res.StatusCode != 207 {
		return ErrNotDAV
	}
	if err := c.mkcols(""); err != nil {
		if errors.Is(err, ErrAuth) || errors.Is(err, ErrUnreachable) {
			return err
		}
		return ErrReadOnly
	}
	if res, err = do("PUT", map[string]string{"If-None-Match": "*"}, "1"); err != nil {
		return err
	}
	if !ok(res) {
		return ErrReadOnly
	}
	if res, err = do("PUT", map[string]string{"If-None-Match": "*"}, "2"); err != nil {
		return err
	}
	if res.StatusCode != 412 {
		return ErrConditions
	}
	if res, err = do("GET", nil, ""); err != nil {
		return err
	}
	if !ok(res) {
		return ErrReadOnly
	}
	tag := etagOf(res)
	if tag == "" {
		return ErrETag
	}
	if strings.HasPrefix(tag, "W/") {
		return ErrConditions
	}
	if res, err = do("PUT", map[string]string{"If-Match": `"oros-probe-wrong"`}, "3"); err != nil {
		return err
	}
	if res.StatusCode != 412 {
		return ErrConditions
	}
	if res, err = do("PUT", map[string]string{"If-Match": tag}, "4"); err != nil {
		return err
	}
	if !ok(res) {
		return ErrConditions
	}
	if res, err = do("HEAD", nil, ""); err != nil {
		return err
	}
	tag2 := ""
	if ok(res) {
		tag2 = etagOf(res)
	}
	if tag2 == "" {
		return ErrETag
	}
	// Two writes of the same size within one second: an ETag made of
	// the size and a whole-second time stays the same, and a stale
	// device's If-Match would then still pass.
	if tag2 == tag {
		return ErrConditions
	}
	if res, err = do("PUT", map[string]string{"If-Match": tag2}, "5"); err != nil {
		return err
	}
	if !ok(res) {
		return ErrConditions
	}
	return nil
}

var serverRe = regexp.MustCompile(`/remote\.php/.*$`)

// Revoke deletes the Nextcloud app password that signs these requests
// (best effort on sign-out; other servers have no such call).
func (c *Client) Revoke() error {
	if !strings.Contains(c.Conf.URL, "/remote.php/") {
		return nil
	}
	server := serverRe.ReplaceAllString(c.Conf.URL, "")
	res, err := c.req("DELETE", server+"/ocs/v2.php/core/apppassword", map[string]string{"OCS-APIRequest": "true"}, nil)
	if err != nil {
		return err
	}
	drain(res)
	if !ok(res) {
		return fmt.Errorf("revoke: %d", res.StatusCode)
	}
	return nil
}
