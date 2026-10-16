// Package vault speaks the orOS Vault Drive format (VD-KEY) exactly as
// vault.js and sync.js in koulaxizis/oros write it. Every constant and
// envelope here mirrors those files; OROS_BIBLE.md ("vault.js" and
// "sync.js") is the written contract. Any change must keep the interop
// tests in interop/ green against the real JavaScript.
package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

// sync.js constants.
const (
	BlobVersion   = 1       // BLOB_VERSION
	PBKDF2Rounds  = 600000  // PBKDF2_ROUNDS, new writes
	PBKDF2Legacy  = 100000  // PBKDF2_LEGACY, blobs without "iter" and raw legacy objects
	PBKDF2Max     = 5000000 // PBKDF2_MAX, a larger "iter" is refused
	saltLen       = 16
	ivLen         = 12
	envelopeV2Tag = 0x02
)

var (
	ErrBlobVersion = errors.New("unsupported blob version")
	ErrEnvelope    = errors.New("bad vault envelope")
	ErrLocked      = errors.New("vault-key-locked") // no wrap opens with this passphrase
)

// Passphrase derives and caches the AES-GCM keys of the passphrase
// envelopes (sync.js deriveKey, A70 cache).
type Passphrase struct {
	pass  string
	mu    sync.Mutex
	cache map[string][]byte
}

func NewPassphrase(pass string) *Passphrase {
	return &Passphrase{pass: pass, cache: map[string][]byte{}}
}

func (p *Passphrase) key(salt []byte, rounds int) ([]byte, error) {
	id := fmt.Sprintf("%d\x00%x", rounds, salt)
	p.mu.Lock()
	defer p.mu.Unlock()
	if k, ok := p.cache[id]; ok {
		return k, nil
	}
	k, err := pbkdf2.Key(sha256.New, p.pass, salt, rounds, 32)
	if err != nil {
		return nil, err
	}
	if len(p.cache) >= 16 {
		p.cache = map[string][]byte{}
	}
	p.cache[id] = k
	return k, nil
}

func gcm(key []byte) (cipher.AEAD, error) {
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand: " + err.Error())
	}
	return b
}

// sealedBlob is the JSON envelope of encryptBlob / decryptBlob
// (vaultCrypto.encryptJson / decryptJson). Field order matches sync.js.
type sealedBlob struct {
	Ver  int    `json:"ver"`
	Iter *int   `json:"iter,omitempty"`
	Salt string `json:"salt"`
	IV   string `json:"iv"`
	Data string `json:"data"`
}

// blobRounds: absent "iter" = PBKDF2Legacy; anything outside
// [PBKDF2Legacy, PBKDF2Max] is refused (a crafted blob must not hang us).
func blobRounds(b sealedBlob) (int, error) {
	if b.Iter == nil {
		return PBKDF2Legacy, nil
	}
	n := *b.Iter
	if n < PBKDF2Legacy || n > PBKDF2Max {
		return 0, ErrBlobVersion
	}
	return n, nil
}

// SealJSON = encryptJson: object → sealed JSON text, PBKDF2Rounds.
func (p *Passphrase) SealJSON(v any) (string, error) {
	plain, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	salt, iv := randomBytes(saltLen), randomBytes(ivLen)
	key, err := p.key(salt, PBKDF2Rounds)
	if err != nil {
		return "", err
	}
	a, err := gcm(key)
	if err != nil {
		return "", err
	}
	rounds := PBKDF2Rounds
	out, err := json.Marshal(sealedBlob{
		Ver:  BlobVersion,
		Iter: &rounds,
		Salt: base64.StdEncoding.EncodeToString(salt),
		IV:   base64.StdEncoding.EncodeToString(iv),
		Data: base64.StdEncoding.EncodeToString(a.Seal(nil, iv, plain, nil)),
	})
	return string(out), err
}

// OpenJSON = decryptJson: sealed JSON text → plaintext JSON bytes.
func (p *Passphrase) OpenJSON(text []byte) ([]byte, error) {
	var b sealedBlob
	if err := json.Unmarshal(text, &b); err != nil {
		return nil, err
	}
	if b.Ver != BlobVersion {
		return nil, ErrBlobVersion
	}
	rounds, err := blobRounds(b)
	if err != nil {
		return nil, err
	}
	salt, err1 := base64.StdEncoding.DecodeString(b.Salt)
	iv, err2 := base64.StdEncoding.DecodeString(b.IV)
	data, err3 := base64.StdEncoding.DecodeString(b.Data)
	if err := errors.Join(err1, err2, err3); err != nil {
		return nil, err
	}
	if len(iv) != ivLen {
		return nil, ErrEnvelope
	}
	key, err := p.key(salt, rounds)
	if err != nil {
		return nil, err
	}
	a, err := gcm(key)
	if err != nil {
		return nil, err
	}
	return a.Open(nil, iv, data, nil)
}

// OpenLegacyBytes = decryptBytes: salt(16)|iv(12)|ciphertext, always
// PBKDF2Legacy rounds. Only objects written before VD-KEY use it.
func (p *Passphrase) OpenLegacyBytes(env []byte) ([]byte, error) {
	if len(env) < saltLen+ivLen {
		return nil, ErrEnvelope
	}
	key, err := p.key(env[:saltLen], PBKDF2Legacy)
	if err != nil {
		return nil, err
	}
	a, err := gcm(key)
	if err != nil {
		return nil, err
	}
	return a.Open(nil, env[saltLen:saltLen+ivLen], env[saltLen+ivLen:], nil)
}

// SealLegacyBytes = encryptBytes. Only tests need it (to build a vault
// the way an old orOS wrote it); new content never uses it.
func (p *Passphrase) SealLegacyBytes(plain []byte) ([]byte, error) {
	salt, iv := randomBytes(saltLen), randomBytes(ivLen)
	key, err := p.key(salt, PBKDF2Legacy)
	if err != nil {
		return nil, err
	}
	a, err := gcm(key)
	if err != nil {
		return nil, err
	}
	out := append(append(append([]byte{}, salt...), iv...), a.Seal(nil, iv, plain, nil)...)
	return out, nil
}

// Keys are the two keys derived from the 32-byte vault key
// (vault.js deriveVaultKeys).
type Keys struct {
	enc cipher.AEAD
	mac []byte
	raw []byte
}

func subKey(raw []byte, label string) []byte {
	h := sha256.Sum256(append([]byte(label), raw...))
	return h[:]
}

// DeriveKeys turns the raw vault key into the AES-GCM key
// (SHA-256("orOS-vault-enc:" + key)) and the HMAC key
// (SHA-256("orOS-vault-name:" + key)).
func DeriveKeys(raw []byte) (*Keys, error) {
	if len(raw) != 32 {
		return nil, errors.New("vault key must be 32 bytes")
	}
	a, err := gcm(subKey(raw, "orOS-vault-enc:"))
	if err != nil {
		return nil, err
	}
	return &Keys{enc: a, mac: subKey(raw, "orOS-vault-name:"), raw: append([]byte{}, raw...)}, nil
}

// Raw returns a copy of the vault key (for the OS keychain).
func (k *Keys) Raw() []byte { return append([]byte{}, k.raw...) }

// Seal = sealV2: 0x02 | iv(12) | AES-GCM(plain).
func (k *Keys) Seal(plain []byte) []byte {
	iv := randomBytes(ivLen)
	out := make([]byte, 0, 1+ivLen+len(plain)+16)
	out = append(out, envelopeV2Tag)
	out = append(out, iv...)
	return k.enc.Seal(out, iv, plain, nil)
}

// Open = openV2.
func (k *Keys) Open(env []byte) ([]byte, error) {
	if len(env) < 14 || env[0] != envelopeV2Tag {
		return nil, ErrEnvelope
	}
	return k.enc.Open(nil, env[1:13], env[13:], nil)
}

// ObjectKey = objectKey: "objects/" + hex(HMAC-SHA-256(macKey, hash)),
// where hash is the hex SHA-256 of the plaintext, as text.
func (k *Keys) ObjectKey(hash string) string {
	m := hmac.New(sha256.New, k.mac)
	m.Write([]byte(hash))
	return ObjectsPrefix + hex.EncodeToString(m.Sum(nil))
}

// SHA256Hex = vaultCrypto.sha256Hex.
func SHA256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// keyFile is /vault/key.json: { ver:1, wraps:[sealed JSON text, …] },
// each wrap = SealJSON({ k: base64(vault key) }).
type keyFile struct {
	Ver   int      `json:"ver"`
	Wraps []string `json:"wraps"`
}

type wrapped struct {
	K string `json:"k"`
}

// OpenKeyFile tries every wrap with the passphrase (vault.js openWraps).
// Two wraps exist only during a passphrase change (new, old).
func OpenKeyFile(data []byte, p *Passphrase) (*Keys, error) {
	var doc keyFile
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, errors.New("bad vault key file")
	}
	for _, w := range doc.Wraps {
		plain, err := p.OpenJSON([]byte(w))
		if err != nil {
			continue
		}
		var o wrapped
		if json.Unmarshal(plain, &o) != nil || o.K == "" {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(o.K)
		if err != nil {
			continue
		}
		return DeriveKeys(raw)
	}
	return nil, ErrLocked
}

// NewKeyFile makes a fresh random vault key and its key.json, the way
// vault.js does on a first push to an empty vault.
func NewKeyFile(p *Passphrase) ([]byte, *Keys, error) {
	raw := randomBytes(32)
	sealed, err := p.SealJSON(wrapped{K: base64.StdEncoding.EncodeToString(raw)})
	if err != nil {
		return nil, nil, err
	}
	doc, err := json.Marshal(keyFile{Ver: 1, Wraps: []string{sealed}})
	if err != nil {
		return nil, nil, err
	}
	k, err := DeriveKeys(raw)
	return doc, k, err
}

func isLegacyManifest(b []byte) bool {
	return len(b) > 0 && b[0] == '{'
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }
