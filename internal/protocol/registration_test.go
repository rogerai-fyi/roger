package protocol

import (
	"crypto/ed25519"
	"crypto/sha512"
	"encoding/hex"
	"testing"
)

// TestSignVerifyRegistration covers proof-of-possession: a registration signed by a
// node's private key verifies against its declared pubkey, and every tamper vector
// (wrong key, mutated field, garbage hex, wrong-length key, empty sig) fails closed.
func TestSignVerifyRegistration(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	reg := NodeRegistration{
		NodeID: "n1", PubKey: hex.EncodeToString(pub), BridgeURL: "https://x", Region: "us",
		Offers: []ModelOffer{{Model: "qwen", PriceIn: 1, PriceOut: 2}},
	}
	reg.SignRegistration(priv)
	if reg.Sig == "" {
		t.Fatal("SignRegistration left Sig empty")
	}
	if !reg.VerifyRegistration() {
		t.Fatal("a correctly-signed registration must verify")
	}

	// Tampering with a signed field breaks verification (Sig covers the canonical body).
	bad := reg
	bad.Region = "eu"
	if bad.VerifyRegistration() {
		t.Error("a mutated field must fail verification")
	}

	// A different key cannot have produced this signature.
	otherPub, _, _ := ed25519.GenerateKey(nil)
	wrongKey := reg
	wrongKey.PubKey = hex.EncodeToString(otherPub)
	if wrongKey.VerifyRegistration() {
		t.Error("signature must not verify under a different pubkey")
	}

	// Malformed inputs fail closed.
	for name, m := range map[string]NodeRegistration{
		"bad pubkey hex": {PubKey: "zz", Sig: reg.Sig},
		"short pubkey":   {PubKey: hex.EncodeToString([]byte{1, 2, 3}), Sig: reg.Sig},
		"bad sig hex":    {PubKey: reg.PubKey, Sig: "zz"},
		"empty sig":      {PubKey: reg.PubKey, Sig: ""},
	} {
		if m.VerifyRegistration() {
			t.Errorf("%s should fail verification", name)
		}
	}
}

// TestAttestationReportData locks the report_data binding: SHA-512 over pubkey-bytes
// then nonce-bytes (exactly 64 bytes), deterministic, key- and nonce-sensitive, and
// nil on malformed hex (so a bad input simply fails the quote check).
func TestAttestationReportData(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	pubHex := hex.EncodeToString(pub)
	nonceHex := hex.EncodeToString([]byte("0123456789abcdef"))

	got := AttestationReportData(pubHex, nonceHex)
	if len(got) != 64 {
		t.Fatalf("report_data length = %d, want 64 (SEV-SNP)", len(got))
	}
	// Matches an independent SHA-512(pub || nonce).
	h := sha512.New()
	h.Write(pub)
	nb, _ := hex.DecodeString(nonceHex)
	h.Write(nb)
	if hex.EncodeToString(got) != hex.EncodeToString(h.Sum(nil)) {
		t.Error("report_data does not equal SHA-512(pub || nonce)")
	}
	// Deterministic.
	if hex.EncodeToString(AttestationReportData(pubHex, nonceHex)) != hex.EncodeToString(got) {
		t.Error("report_data must be deterministic")
	}
	// Nonce-sensitive.
	if hex.EncodeToString(AttestationReportData(pubHex, hex.EncodeToString([]byte("DIFFERENT-nonce!")))) == hex.EncodeToString(got) {
		t.Error("a different nonce must change report_data")
	}
	// Malformed hex -> nil (never matches).
	if AttestationReportData("zz", nonceHex) != nil {
		t.Error("bad pubkey hex must yield nil")
	}
	if AttestationReportData(pubHex, "zz") != nil {
		t.Error("bad nonce hex must yield nil")
	}
}

// TestHHMMEdges covers the time-of-day parser's reject paths: missing colon, non-numeric
// parts, and out-of-range hours/minutes, plus a valid round-trip.
func TestHHMMEdges(t *testing.T) {
	if v, ok := hhmm("09:30"); !ok || v != 9*60+30 {
		t.Errorf("hhmm(09:30) = %d,%v want 570,true", v, ok)
	}
	for _, bad := range []string{"930", "ab:cd", "24:00", "12:60", "-1:00", "12:-5", ""} {
		if _, ok := hhmm(bad); ok {
			t.Errorf("hhmm(%q) = ok, want rejected", bad)
		}
	}
}

// TestPad3 covers the 3-wide zero-pad including the negative clamp and the no-pad path.
func TestPad3(t *testing.T) {
	cases := map[int]string{-5: "000", 0: "000", 7: "007", 42: "042", 521: "521", 1234: "1234"}
	for in, want := range cases {
		if got := pad3(in); got != want {
			t.Errorf("pad3(%d) = %q, want %q", in, got, want)
		}
	}
}
