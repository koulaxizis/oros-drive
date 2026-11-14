package vault

import "testing"

// The vector was made with the formula sync.js vaultProof uses (and
// tests/sos.test.js in koulaxizis/oros checks sync.js against it):
// raw = bytes 0..31, "sos:1794680000000".
const sigVP = "i7EgscTZ84SzMRZibsqp8L2HJ5MqMRDiOpqwzlKBUIU="

func testKeys(t *testing.T, first byte) *Keys {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = first + byte(i)
	}
	k, err := DeriveKeys(raw)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestParseSignal(t *testing.T) {
	sg, ok := ParseSignal([]byte(`{"ver":"oros-signal","sig":{"kind":"sos","n":1794680000000,"iter":600000,"salt":"s","iv":"i","data":"d","vp":"` + sigVP + `"}}`))
	if !ok || sg.Kind != "sos" || sg.N != 1794680000000 || sg.VP != sigVP || !sg.Only {
		t.Fatalf("signal blob: %+v %v", sg, ok)
	}
	// A cloud started again carries the SOS in its normal blobs.
	sg, ok = ParseSignal([]byte(`{"ver":1,"iter":600000,"salt":"s","iv":"i","data":"d","sig":{"kind":"sos","n":5,"salt":"s","iv":"i","data":"d"}}`))
	if !ok || sg.Only || sg.VP != "" || sg.N != 5 {
		t.Fatalf("normal blob with sig: %+v %v", sg, ok)
	}
	for _, bad := range []string{
		``, `x`, `{}`, `{"ver":1,"data":"d"}`, `{"ver":"oros-signal"}`,
		`{"ver":"oros-signal","sig":{"kind":"other","n":5}}`,
		`{"ver":"oros-signal","sig":{"kind":"sos","n":-1}}`,
		`{"ver":"oros-signal","sig":{"kind":"sos","n":1.5}}`,
		`{"ver":"oros-signal","sig":{"kind":"sos","n":"5"}}`,
		`{"ver":"oros-signal","sig":{"kind":"sos"}}`,
	} {
		if _, ok := ParseSignal([]byte(bad)); ok {
			t.Errorf("%q read as a signal", bad)
		}
	}
}

func TestConfirmSignal(t *testing.T) {
	k := testKeys(t, 0)
	sg := Signal{Kind: "sos", N: 1794680000000, VP: sigVP, Only: true}
	if !k.ConfirmSignal(sg) {
		t.Fatal("the vector from sync.js is not confirmed")
	}
	for name, s := range map[string]Signal{
		"no vp":      {Kind: "sos", N: 1794680000000},
		"other time": {Kind: "sos", N: 1794680000001, VP: sigVP},
		"other kind": {Kind: "reset", N: 1794680000000, VP: sigVP},
		"bad base64": {Kind: "sos", N: 1794680000000, VP: "%%%"},
	} {
		if k.ConfirmSignal(s) {
			t.Errorf("%s: confirmed", name)
		}
	}
	if testKeys(t, 1).ConfirmSignal(sg) {
		t.Error("another vault key confirms the signal")
	}
	var none *Keys
	if none.ConfirmSignal(sg) {
		t.Error("no key confirms the signal")
	}
}
