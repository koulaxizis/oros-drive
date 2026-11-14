package vault

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strconv"
)

// SOS-1 signals (koulaxizis/oros sync.js, Bible Part V "Signals").
// An SOS or a factory reset in orOS replaces the sync blob
// (orOS-data.json, next to vault/) with
//
//	{"ver": "oros-signal", "sig": {"kind": "sos"|"reset", "n": <ms>, ..., "vp": "<base64>"}}
//
// and a cloud started again after an SOS carries the same "sig" in
// its normal blobs. The rest of "sig" is sealed with the sync
// passphrase, which orOS Drive never keeps. "vp" is the part Drive
// can check: base64 HMAC-SHA-256 of "<kind>:<n>" under
// SHA-256("oros-signal" || raw vault key) (sync.js vaultProof).

// BlobReader is a cloud that can read orOS's sync blob, which lives
// next to vault/ (Dropbox "/orOS-data.json", WebDAV
// "<dir>/orOS-data.json"). A missing blob is (nil, nil).
type BlobReader interface {
	GetSyncBlob() ([]byte, error)
}

// SyncBlobName is the sync blob's name next to vault/ (sync.js BLOB_PATH).
const SyncBlobName = "orOS-data.json"

// SignalVer is the "ver" of a blob that holds only a signal.
const SignalVer = "oros-signal"

// Signal is what Drive reads from the sync blob.
type Signal struct {
	Kind string // "sos" or "reset"
	N    int64  // when it was made (ms)
	VP   string // the proof under the vault key; "" = none
	// Only: the blob holds the signal and nothing else (no data).
	Only bool
}

// ParseSignal reads the sync blob's text. ok = false: no signal (a
// normal blob, no blob, or something that is not orOS's).
func ParseSignal(blob []byte) (Signal, bool) {
	if len(blob) == 0 {
		return Signal{}, false
	}
	var env struct {
		Ver json.RawMessage `json:"ver"`
		Sig *struct {
			Kind string          `json:"kind"`
			N    json.RawMessage `json:"n"`
			VP   string          `json:"vp"`
		} `json:"sig"`
	}
	if json.Unmarshal(blob, &env) != nil || env.Sig == nil {
		return Signal{}, false
	}
	if env.Sig.Kind != "sos" && env.Sig.Kind != "reset" {
		return Signal{}, false
	}
	n, err := strconv.ParseInt(string(env.Sig.N), 10, 64)
	if err != nil || n <= 0 {
		return Signal{}, false
	}
	var ver string
	only := json.Unmarshal(env.Ver, &ver) == nil && ver == SignalVer
	return Signal{Kind: env.Sig.Kind, N: n, VP: env.Sig.VP, Only: only}, true
}

// ConfirmSignal tells whether the signal was made by someone who held
// this vault key (the orOS that sent it opened key.json with the
// passphrase before the wipe). A signal without vp, or with another
// key's vp, is not confirmed.
func (k *Keys) ConfirmSignal(sg Signal) bool {
	if k == nil || sg.VP == "" {
		return false
	}
	got, err := base64.StdEncoding.DecodeString(sg.VP)
	if err != nil {
		return false
	}
	m := hmac.New(sha256.New, subKey(k.raw, "oros-signal"))
	m.Write([]byte(sg.Kind + ":" + strconv.FormatInt(sg.N, 10)))
	return hmac.Equal(got, m.Sum(nil))
}
