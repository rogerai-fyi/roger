package agent

import (
	"crypto/ed25519"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"rogerai.fm/roger/v7/internal/protocol"
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

	// record counts what the node handled; served and earned are Core's record, never this
	// count (features/serving/probe_accounting.feature).
	rec := protocol.UsageReceipt{PromptTokens: 100, CompletionTokens: 50, PriceIn: 1_000_000, PriceOut: 2_000_000}
	s.record(rec)
	s.record(rec)
	if reqs, toks := s.Handled(); reqs != 2 || toks != 100 {
		t.Errorf("Handled = %d reqs / %d toks, want 2/100", reqs, toks)
	}
	if reqs, toks := s.Served(); reqs != 0 || toks != 0 || s.Earnings() != 0 || s.CoreCurrent() {
		t.Errorf("with no word from Core, served/earned must be empty and not current, got %d/%d/%v", reqs, toks, s.Earnings())
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

// Core's checks look like customers to the node, so the node cannot keep them out of its own
// count; what it shows as served, earned and network checks comes from Core's record instead.
func TestServedAndEarnedComeFromCoresRecord(t *testing.T) {
	var reads atomic.Int64
	answer := atomic.Int64{}
	answer.Store(http.StatusOK)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		if r.URL.Path != "/nodes/served" || r.URL.Query().Get("node") != "n-1" || r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if code := int(answer.Load()); code != http.StatusOK {
			w.WriteHeader(code)
			return
		}
		_, _ = w.Write([]byte(`{"served":3,"tokens":30,"earned":0.25}`))
	}))
	defer srv.Close()
	s := &Session{cfg: Config{Broker: srv.URL, NodeID: "n-1"}, started: time.Now(),
		rereg: newReregistrar(srv.URL, protocol.NodeRegistration{BridgeToken: "tok"}, nil)}
	for i := 0; i < 8; i++ {
		s.RecordHandledForTest(10)
	}
	waitCurrent := func(want bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for s.CoreCurrent() != want {
			if time.Now().After(deadline) {
				t.Fatalf("CoreCurrent never became %v", want)
			}
			s.ExpireCoreForTest()
			time.Sleep(5 * time.Millisecond)
		}
	}
	waitCurrent(true)
	if reqs, toks := s.Served(); reqs != 3 || toks != 30 || s.Earnings() != 0.25 {
		t.Fatalf("served %d/%d earned %v, want Core's 3/30/0.25", reqs, toks, s.Earnings())
	}
	if reqs, toks := s.ProbeStats(); reqs != 5 || toks != 50 {
		t.Fatalf("network checks %d/%d, want handled minus served 5/50", reqs, toks)
	}
	// Reading is throttled: a burst of reads asks Core once at most.
	before := reads.Load()
	for i := 0; i < 50; i++ {
		s.Served()
	}
	time.Sleep(20 * time.Millisecond)
	if n := reads.Load() - before; n > 1 {
		t.Fatalf("%d reads of Core's record in a burst", n)
	}
	// Core throttling the read keeps the numbers as they were.
	answer.Store(http.StatusTooManyRequests)
	s.ExpireCoreForTest()
	s.Served()
	time.Sleep(50 * time.Millisecond)
	if !s.CoreCurrent() {
		t.Fatal("a 429 marked Core's numbers not current")
	}
	// Core not answering keeps the last numbers, marked not current, and no checks are claimed.
	answer.Store(http.StatusServiceUnavailable)
	waitCurrent(false)
	if reqs, _ := s.Served(); reqs != 3 {
		t.Fatalf("served %d while Core cannot answer, want the last number Core gave (3)", reqs)
	}
	if reqs, _ := s.ProbeStats(); reqs != 0 {
		t.Fatalf("%d network checks claimed from a stale record", reqs)
	}
	// An unreachable Core, and an unreadable answer, are not current either.
	srv.Close()
	waitCurrent(false)
	if (&Session{}).BridgeToken() != "" {
		t.Fatal("a session with no registration has no token")
	}
}

func TestAnUnreadableRecordIsNotCurrent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	}))
	defer srv.Close()
	s := &Session{cfg: Config{Broker: srv.URL, NodeID: "n-1"},
		rereg: newReregistrar(srv.URL, protocol.NodeRegistration{BridgeToken: "tok"}, nil)}
	if _, ok, throttled := s.readCoreRecord(); ok || throttled {
		t.Fatal("an unreadable record was taken")
	}
	s.cfg.Broker = "http://[::1"
	if _, ok, _ := s.readCoreRecord(); ok {
		t.Fatal("a malformed broker URL read a record")
	}
}

// Audit finding: a LAN share has no Core, so it waited for Core's record forever. Its own count is
// its record: what it handled is what it served, current, with no checks to subtract.
func TestALANShareShowsItsOwnCount(t *testing.T) {
	s := StartLAN(Config{NodeID: "n-lan", Model: "m"}, nil, nil, io.Discard)
	defer s.Stop()
	s.RecordHandledForTest(10)
	s.RecordHandledForTest(10)
	if reqs, toks := s.Served(); reqs != 2 || toks != 20 || !s.CoreCurrent() || !s.CoreAnswered() {
		t.Fatalf("a LAN share shows %d/%d current=%v answered=%v, want its own 2/20, current", reqs, toks, s.CoreCurrent(), s.CoreAnswered())
	}
	if reqs, _ := s.ProbeStats(); reqs != 0 {
		t.Fatalf("a LAN share claimed %d network checks", reqs)
	}
}

// Audit finding: a job served after Core's last answer read as a network check until the next
// read. The checks are what the node had handled when Core answered, beyond what Core counted.
func TestAJobServedSinceCoresAnswerIsNotACheck(t *testing.T) {
	s := &Session{}
	s.RecordHandledForTest(10)
	s.RecordHandledForTest(10)
	s.SetCoreForTest(1, 10, 0, true) // Core counted one of the two
	s.RecordHandledForTest(10)       // served since Core answered
	if reqs, _ := s.ProbeStats(); reqs != 1 {
		t.Fatalf("network checks = %d, want 1: the job served since Core answered is not a check", reqs)
	}
}
