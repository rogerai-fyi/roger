package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"

	"github.com/google/go-sev-guest/abi"
	spb "github.com/google/go-sev-guest/proto/sevsnp"
	sgtest "github.com/google/go-sev-guest/testing"
	"github.com/google/go-sev-guest/verify/trust"
)

// ---------------------------------------------------------------------------
// Real SEV-SNP backend tests: build a genuinely signed synthetic quote with the
// go-sev-guest test cert chain, then verify it through sevSNPVerifier. These prove
// the library is wired correctly (signature chain + report_data binding + measurement
// allowlist) - the same code path runs against real AMD silicon in production.
// ---------------------------------------------------------------------------

// signedQuote builds a SEV-SNP attestation report carrying reportData + measurement,
// signs it with the test VCEK, and returns the raw extended-report bytes (report ||
// cert table) plus a verifier whose trusted roots are the test signer's ARK/ASK.
func signedQuote(t *testing.T, reportData [64]byte, measurement []byte) ([]byte, *sevSNPVerifier) {
	t.Helper()
	now := time.Now()
	signer, err := sgtest.DefaultTestOnlyCertChain(sgtest.GetProductName(), now)
	if err != nil {
		t.Fatalf("build test cert chain: %v", err)
	}

	rpt := &spb.Report{
		Version:         2,
		Policy:          abi.SnpPolicyToBytes(abi.SnpPolicy{}),
		SignatureAlgo:   abi.SignEcdsaP384Sha384,
		FamilyId:        make([]byte, 16),
		ImageId:         make([]byte, 16),
		ReportData:      reportData[:],
		Measurement:     measurement,
		HostData:        make([]byte, 32),
		IdKeyDigest:     make([]byte, 48),
		AuthorKeyDigest: make([]byte, 48),
		ReportId:        make([]byte, 32),
		ReportIdMa:      make([]byte, 32),
		ChipId:          signer.HWID[:],
		Signature:       make([]byte, abi.SignatureSize),
	}
	raw, err := abi.ReportToAbiBytes(rpt)
	if err != nil {
		t.Fatalf("report to bytes: %v", err)
	}
	// The VCEK signs the SignedComponent (all bytes before the signature field); the
	// verifier checks the signature over exactly those bytes.
	r, s, err := signer.Sign(abi.SignedComponent(raw))
	if err != nil {
		t.Fatalf("sign report: %v", err)
	}
	if err := abi.SetSignature(r, s, raw); err != nil {
		t.Fatalf("set signature: %v", err)
	}
	certs, err := signer.CertTableBytes()
	if err != nil {
		t.Fatalf("cert table: %v", err)
	}
	quote := append(raw, certs...)

	// Trusted roots = the test signer's ARK/ASK (production uses the embedded AMD
	// roots). DisableCertFetching because the VCEK is embedded in the quote's cert
	// table, so no KDS round-trip is needed in the test.
	root := trust.AMDRootCertsProduct(sgtest.GetProductLine())
	root.ProductCerts = &trust.ProductCerts{Ark: signer.Ark, Ask: signer.Ask}
	v := &sevSNPVerifier{getter: nil}
	v.testRoots = map[string][]*trust.AMDRootCerts{sgtest.GetProductLine(): {root}}
	v.testProduct = sgtest.GetProduct(t)
	return quote, v
}

func bindingReportData(t *testing.T, pubHex, nonceHex string) [64]byte {
	t.Helper()
	rd := protocol.AttestationReportData(pubHex, nonceHex)
	if len(rd) != 64 {
		t.Fatalf("report_data binding length = %d, want 64", len(rd))
	}
	var out [64]byte
	copy(out[:], rd)
	return out
}

func TestSEVSNPVerifier_ValidQuote(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	pubHex := hex.EncodeToString(pub)
	nonce := "deadbeef"
	measurement := make([]byte, abi.MeasurementSize)
	copy(measurement, []byte("ROGERAI-APPROVED-STACK"))

	rd := bindingReportData(t, pubHex, nonce)
	quote, v := signedQuote(t, rd, measurement)

	got, err := v.Verify(context.Background(), attestParams{
		quote: quote, pubHex: pubHex, nonceHex: nonce, measurements: [][]byte{measurement},
	})
	if err != nil {
		t.Fatalf("valid quote rejected: %v", err)
	}
	if string(got) != string(measurement) {
		t.Errorf("returned measurement = %x, want %x", got, measurement)
	}
}

func TestSEVSNPVerifier_ForgedSignature(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	pubHex := hex.EncodeToString(pub)
	nonce := "cafe"
	measurement := make([]byte, abi.MeasurementSize)
	rd := bindingReportData(t, pubHex, nonce)
	quote, v := signedQuote(t, rd, measurement)

	// Corrupt the signature bytes -> chain verification must fail.
	quote[0x2A0] ^= 0xFF
	if _, err := v.Verify(context.Background(), attestParams{
		quote: quote, pubHex: pubHex, nonceHex: nonce, measurements: [][]byte{measurement},
	}); err == nil {
		t.Fatal("forged/corrupted signature was accepted")
	}
}

func TestSEVSNPVerifier_ReplayedToDifferentKey(t *testing.T) {
	// A quote bound to pubA+nonce must NOT verify when presented as if for pubB
	// (replay by another node) or with a different nonce (stale replay).
	pubA, _, _ := ed25519.GenerateKey(nil)
	pubB, _, _ := ed25519.GenerateKey(nil)
	pubAHex, pubBHex := hex.EncodeToString(pubA), hex.EncodeToString(pubB)
	nonce := "0011"
	measurement := make([]byte, abi.MeasurementSize)
	rd := bindingReportData(t, pubAHex, nonce)
	quote, v := signedQuote(t, rd, measurement)

	if _, err := v.Verify(context.Background(), attestParams{
		quote: quote, pubHex: pubBHex, nonceHex: nonce, measurements: [][]byte{measurement},
	}); err == nil {
		t.Error("quote bound to pubA was accepted for pubB (replay by another node)")
	}
	if _, err := v.Verify(context.Background(), attestParams{
		quote: quote, pubHex: pubAHex, nonceHex: "ffff", measurements: [][]byte{measurement},
	}); err == nil {
		t.Error("quote bound to one nonce was accepted for a different nonce (stale replay)")
	}
}

func TestSEVSNPVerifier_NonAllowlistedMeasurement(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	pubHex := hex.EncodeToString(pub)
	nonce := "abcd"
	measurement := make([]byte, abi.MeasurementSize)
	copy(measurement, []byte("UNKNOWN-MEASUREMENT"))
	rd := bindingReportData(t, pubHex, nonce)
	quote, v := signedQuote(t, rd, measurement)

	approved := make([]byte, abi.MeasurementSize)
	copy(approved, []byte("DIFFERENT-APPROVED"))
	if _, err := v.Verify(context.Background(), attestParams{
		quote: quote, pubHex: pubHex, nonceHex: nonce, measurements: [][]byte{approved},
	}); err == nil {
		t.Error("a non-allowlisted launch measurement was accepted")
	}
}

func TestSEVSNPVerifier_EmptyAllowlistRejects(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	pubHex := hex.EncodeToString(pub)
	nonce := "1234"
	measurement := make([]byte, abi.MeasurementSize)
	rd := bindingReportData(t, pubHex, nonce)
	quote, v := signedQuote(t, rd, measurement)
	if _, err := v.Verify(context.Background(), attestParams{
		quote: quote, pubHex: pubHex, nonceHex: nonce, measurements: nil,
	}); err == nil {
		t.Error("empty allowlist must reject (fail-closed)")
	}
}

// ---------------------------------------------------------------------------
// Flow tests: registry / handshake / re-attestation, driven by a deterministic
// mock verifier so we exercise the broker policy without rebuilding a quote.
// ---------------------------------------------------------------------------

type mockVerifier struct {
	ok      bool
	wantPub string
	wantNon string
}

func (m *mockVerifier) Kind() string { return attestSEVSNP }
func (m *mockVerifier) Verify(_ context.Context, p attestParams) ([]byte, error) {
	if !m.ok {
		return nil, errors.New("mock: forced failure")
	}
	if m.wantPub != "" && p.pubHex != m.wantPub {
		return nil, errors.New("mock: pubkey mismatch")
	}
	if m.wantNon != "" && p.nonceHex != m.wantNon {
		return nil, errors.New("mock: nonce mismatch")
	}
	return []byte("ok-measurement"), nil
}

func mockRegistry(ok bool) *attestRegistry {
	r := &attestRegistry{
		verifiers:    map[string]attestationVerifier{},
		measurements: [][]byte{[]byte("m")},
		reattestTTL:  time.Hour,
		nonceTTL:     5 * time.Minute,
		nonces:       map[string]nonceEntry{},
	}
	r.setVerifier(&mockVerifier{ok: ok})
	return r
}

func TestNonceSingleUseAndExpiry(t *testing.T) {
	r := mockRegistry(true)
	ch := r.issueNonce()
	if !r.consumeNonce(ch.Nonce) {
		t.Fatal("first consume of a fresh nonce should succeed")
	}
	if r.consumeNonce(ch.Nonce) {
		t.Fatal("a nonce must be single-use (second consume should fail)")
	}
	// Expired nonce.
	r.mu.Lock()
	r.nonces["expired"] = nonceEntry{expires: time.Now().Add(-time.Minute)}
	r.mu.Unlock()
	if r.consumeNonce("expired") {
		t.Fatal("an expired nonce must not be consumable")
	}
	if r.consumeNonce("never-issued") {
		t.Fatal("an unknown nonce must not be consumable")
	}
}

func TestVerifyRegistration_NoClaimNoBadge(t *testing.T) {
	r := mockRegistry(true)
	conf, err := r.verifyRegistration(context.Background(), protocol.NodeRegistration{NodeID: "n", Confidential: false})
	if err != nil || conf {
		t.Errorf("a node not claiming confidential must get (false,nil); got (%v,%v)", conf, err)
	}
}

func TestVerifyRegistration_ValidGrantsBadge(t *testing.T) {
	r := mockRegistry(true)
	ch := r.issueNonce()
	reg := protocol.NodeRegistration{
		NodeID: "n", Confidential: true, AttestKind: attestSEVSNP,
		PubKey: "aa", AttestNonce: ch.Nonce, Attestation: base64.StdEncoding.EncodeToString([]byte("quote")),
	}
	conf, err := r.verifyRegistration(context.Background(), reg)
	if err != nil || !conf {
		t.Fatalf("valid attestation should grant the badge; got (%v,%v)", conf, err)
	}
}

func TestVerifyRegistration_StaleNonceRejected(t *testing.T) {
	r := mockRegistry(true)
	reg := protocol.NodeRegistration{
		NodeID: "n", Confidential: true, AttestKind: attestSEVSNP,
		PubKey: "aa", AttestNonce: "never-issued", Attestation: base64.StdEncoding.EncodeToString([]byte("q")),
	}
	conf, err := r.verifyRegistration(context.Background(), reg)
	if conf || err != nil {
		// require=false -> no badge, no rejection error.
		t.Errorf("unknown nonce should yield (false,nil) with require off; got (%v,%v)", conf, err)
	}
}

func TestVerifyRegistration_RequireModeRejects(t *testing.T) {
	r := mockRegistry(false) // verifier always fails
	r.required = true
	ch := r.issueNonce()
	reg := protocol.NodeRegistration{
		NodeID: "n", Confidential: true, AttestKind: attestSEVSNP,
		PubKey: "aa", AttestNonce: ch.Nonce, Attestation: base64.StdEncoding.EncodeToString([]byte("q")),
	}
	conf, err := r.verifyRegistration(context.Background(), reg)
	if conf || err == nil {
		t.Errorf("require mode must REJECT a failed claim; got (%v,%v)", conf, err)
	}
}

func TestVerifyRegistration_FailSoftWhenNotRequired(t *testing.T) {
	r := mockRegistry(false)
	ch := r.issueNonce()
	reg := protocol.NodeRegistration{
		NodeID: "n", Confidential: true, AttestKind: attestSEVSNP,
		PubKey: "aa", AttestNonce: ch.Nonce, Attestation: base64.StdEncoding.EncodeToString([]byte("q")),
	}
	conf, err := r.verifyRegistration(context.Background(), reg)
	if conf || err != nil {
		t.Errorf("failed claim w/o require must downgrade to standard (false,nil); got (%v,%v)", conf, err)
	}
}

func TestReattestSweepDropsLapsed(t *testing.T) {
	b := newTrustBroker()
	b.attest = mockRegistry(true)
	now := time.Now()
	b.confidential["fresh"] = true
	b.attestedAt["fresh"] = now.Add(-30 * time.Minute)
	b.confidential["stale"] = true
	b.attestedAt["stale"] = now.Add(-2 * time.Hour)

	b.expireStaleAttestations(now, time.Hour)

	if !b.confidential["fresh"] {
		t.Error("a freshly-attested node should keep confidential status")
	}
	if b.confidential["stale"] {
		t.Error("a node past the re-attest cadence must lose confidential status")
	}
	if _, ok := b.attestedAt["stale"]; ok {
		t.Error("a lapsed node should be removed from attestedAt")
	}
}

// TestChallengeHandlerIssuesUsableNonce: the HTTP handshake issues a nonce that the
// registry will then accept exactly once.
func TestChallengeHandlerIssuesUsableNonce(t *testing.T) {
	b := newTrustBroker()
	b.attest = mockRegistry(true)
	rec := httptest.NewRecorder()
	b.attestChallenge(rec, httptest.NewRequest(http.MethodPost, "/nodes/challenge", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("challenge status = %d, want 200", rec.Code)
	}
	var ch protocol.AttestChallenge
	if err := json.Unmarshal(rec.Body.Bytes(), &ch); err != nil {
		t.Fatalf("decode challenge: %v", err)
	}
	if ch.Nonce == "" || ch.Expires == 0 {
		t.Fatalf("challenge missing nonce/expiry: %+v", ch)
	}
	if !b.attest.consumeNonce(ch.Nonce) {
		t.Fatal("issued nonce should be consumable once")
	}
}

// TestConfidentialRouteFilterOnlyVerified: with confidentialOnly set, pick routes
// ONLY to a node that holds verified-confidential status. A standard node is never
// selected for confidential traffic, even if it is cheaper / the only one online.
func TestConfidentialRouteFilterOnlyVerified(t *testing.T) {
	now := time.Now()
	b := newTrustBroker()
	b.nodes = map[string]protocol.NodeRegistration{
		"verified": {NodeID: "verified", Offers: []protocol.ModelOffer{{Model: "m", PriceOut: 1.0}}},
		"standard": {NodeID: "standard", Offers: []protocol.ModelOffer{{Model: "m", PriceOut: 0.1}}}, // cheaper
	}
	b.lastSeen = map[string]time.Time{"verified": now, "standard": now}
	b.confidential = map[string]bool{"verified": true, "standard": false}

	b.mu.Lock()
	node, _, ok := b.pick("m", true, 0, 0, 0, "", nil, nil)
	b.mu.Unlock()
	if !ok || node.NodeID != "verified" {
		t.Fatalf("confidential pick = %q (ok=%v), want the verified node despite the cheaper standard one", node.NodeID, ok)
	}

	// Drop the verified node's status (re-attest lapse) -> NO confidential route exists.
	b.confidential["verified"] = false
	b.mu.Lock()
	_, _, ok2 := b.pick("m", true, 0, 0, 0, "", nil, nil)
	b.mu.Unlock()
	if ok2 {
		t.Error("with no verified node, confidential routing must find nothing (never falls back to standard)")
	}
}

// TestReportDataBindingDeterministic: the node-side and broker-side binding agree
// (same pubkey + nonce -> same report_data), and differ if either input changes.
func TestReportDataBindingDeterministic(t *testing.T) {
	pub := make([]byte, ed25519.PublicKeySize)
	for i := range pub {
		pub[i] = byte(i)
	}
	pubHex := hex.EncodeToString(pub)
	a := protocol.AttestationReportData(pubHex, "aa")
	b := protocol.AttestationReportData(pubHex, "aa")
	if string(a) != string(b) || len(a) != 64 {
		t.Fatal("binding must be deterministic and 64 bytes")
	}
	if string(a) == string(protocol.AttestationReportData(pubHex, "bb")) {
		t.Error("a different nonce must change the binding")
	}
	// Sanity: matches a direct SHA-512(pub||nonce).
	h := sha512.New()
	h.Write(pub)
	nb, _ := hex.DecodeString("aa")
	h.Write(nb)
	if string(a) != string(h.Sum(nil)) {
		t.Error("binding does not match SHA-512(pub||nonce)")
	}
}
