package agent

import (
	"crypto/ed25519"
	"encoding/hex"
	"testing"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// TestSessionAccessors covers the read-only Session accessors plus record(): the
// offer fields, the effective (overridden) price, the band triple, and the counter
// folding done by record (requests, completion tokens, owner-share earnings).
func TestSessionAccessors(t *testing.T) {
	s := &Session{
		cfg:            Config{Model: "qwen", PriceIn: 1, PriceOut: 2, NodeID: "node-1", Upstream: "http://up/v1"},
		effPriceIn:     0.5,
		effPriceOut:    0.7,
		overrideActive: true,
		bandID:         "band_1",
		bandCode:       "8F3K-9M2Q",
		bandDisplay:    "147.520 MHz",
	}

	if s.Model() != "qwen" || s.Node() != "node-1" || s.Upstream() != "http://up/v1" {
		t.Errorf("offer accessors = %q/%q/%q", s.Model(), s.Node(), s.Upstream())
	}
	if in, out := s.Price(); in != 1 || out != 2 {
		t.Errorf("Price = %v/%v, want 1/2", in, out)
	}
	if in, out, ovr := s.EffectivePrice(); in != 0.5 || out != 0.7 || !ovr {
		t.Errorf("EffectivePrice = %v/%v/%v, want 0.5/0.7/true", in, out, ovr)
	}
	if id, code, disp := s.Band(); id != "band_1" || code != "8F3K-9M2Q" || disp != "147.520 MHz" {
		t.Errorf("Band = %q/%q/%q", id, code, disp)
	}

	// record folds a served receipt into the counters: +1 req, +completion tokens, and
	// owner-share = cost*(1-fee).
	rec := protocol.UsageReceipt{PromptTokens: 100, CompletionTokens: 50, PriceIn: 1_000_000, PriceOut: 2_000_000}
	s.record(rec, 0.30)
	reqs, toks := s.Served()
	if reqs != 1 || toks != 50 {
		t.Errorf("Served = %d reqs / %d toks, want 1/50", reqs, toks)
	}
	wantEarn := rec.Cost() * (1 - 0.30)
	if got := s.Earnings(); got < wantEarn-1e-6 || got > wantEarn+1e-6 {
		t.Errorf("Earnings = %v, want %v (cost*(1-fee))", got, wantEarn)
	}
	// A second record accumulates.
	s.record(rec, 0.30)
	if reqs, _ := s.Served(); reqs != 2 {
		t.Errorf("Served after 2 records = %d reqs, want 2", reqs)
	}
}

// TestLinkStateRoundTrip covers Link/setLink and the nil-receiver guard on setLink.
func TestLinkStateRoundTrip(t *testing.T) {
	s := &Session{}
	if s.Link() != LinkConnecting {
		t.Errorf("default Link = %v, want LinkConnecting", s.Link())
	}
	s.setLink(LinkOnAir)
	if s.Link() != LinkOnAir {
		t.Errorf("Link after setLink = %v, want LinkOnAir", s.Link())
	}
	var nilS *Session
	nilS.setLink(LinkReconnecting) // must not panic
}

// TestIsStream covers the streaming-request sniff: stream:true, stream:false, and a
// malformed body (defaults to non-stream).
func TestIsStream(t *testing.T) {
	if !isStream([]byte(`{"stream":true}`)) {
		t.Error("stream:true must be detected")
	}
	if isStream([]byte(`{"stream":false}`)) {
		t.Error("stream:false must be non-stream")
	}
	if isStream([]byte(`not json`)) {
		t.Error("malformed body must default to non-stream")
	}
}

// TestRandIndex covers the bounded random index: n<=0 -> 0, and n>0 stays in [0,n).
func TestRandIndex(t *testing.T) {
	if randIndex(0) != 0 || randIndex(-5) != 0 {
		t.Error("randIndex(<=0) must be 0")
	}
	for i := 0; i < 200; i++ {
		if v := randIndex(10); v < 0 || v >= 10 {
			t.Fatalf("randIndex(10) = %d, out of [0,10)", v)
		}
	}
}

// TestReportData64 covers the attestation report_data binding: it equals
// protocol.AttestationReportData(pubhex, noncehex) byte-for-byte, and a bad nonce hex
// returns an error.
func TestReportData64(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	nonceHex := hex.EncodeToString([]byte("a-broker-nonce!!"))

	got, err := reportData64(pub, nonceHex)
	if err != nil {
		t.Fatal(err)
	}
	want := protocol.AttestationReportData(hex.EncodeToString(pub), nonceHex)
	if hex.EncodeToString(got[:]) != hex.EncodeToString(want) {
		t.Error("reportData64 must equal protocol.AttestationReportData")
	}
	if _, err := reportData64(pub, "zz"); err == nil {
		t.Error("bad nonce hex must error")
	}
}

// TestTEEUnavailableOnTestHost covers detectTEE/teeAvailable + generateQuote on a host
// with no SEV-SNP guest device: detection is honest ("") and quote generation errors,
// so the node never sends a fake confidential claim.
func TestTEEUnavailableOnTestHost(t *testing.T) {
	if k := detectTEE(); k != "" {
		t.Skipf("running on real TEE hardware (%s); skipping the no-TEE assertions", k)
	}
	var rd [64]byte
	if _, err := generateQuote(rd); err == nil {
		t.Error("generateQuote should fail with no TEE device present")
	}
}
