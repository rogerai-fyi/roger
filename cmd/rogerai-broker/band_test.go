package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// newBandBroker builds a minimally-wired broker with a Mem store + a linked owner,
// returning the broker, the owner's user signing key, and the node signing key.
func newBandBroker(t *testing.T) (*broker, ed25519.PrivateKey, ed25519.PrivateKey, string) {
	t.Helper()
	mem := store.NewMem()
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	b := &broker{
		db:           mem,
		priv:         brokerPriv,
		nodes:        map[string]protocol.NodeRegistration{},
		tunnels:      map[string]*nodeTunnel{},
		lastSeen:     map[string]time.Time{},
		confidential: map[string]bool{},
		private:      map[string]bool{},
		bandOf:       map[string]string{},
		tps:          map[string]float64{},
		pubOfUser:    map[string]string{},
		seedFunds:    100,
	}
	nodePub, nodePriv, _ := ed25519.GenerateKey(nil)
	_, userPriv, _ := ed25519.GenerateKey(nil)
	userPubHex := hex.EncodeToString(userPriv.Public().(ed25519.PublicKey))
	_ = mem.BindOwner(store.Owner{GitHubID: 1, Login: "owner", Pubkey: userPubHex})
	_ = nodePub
	return b, userPriv, nodePriv, hex.EncodeToString(nodePub)
}

// registerPrivate registers a private node and returns the broker response + status.
func registerPrivate(t *testing.T, b *broker, nodePriv ed25519.PrivateKey, nodePubHex string, userPriv ed25519.PrivateKey, signUser bool) (map[string]any, int) {
	t.Helper()
	reg := protocol.NodeRegistration{
		NodeID: "priv1", PubKey: nodePubHex, BridgeToken: "tok", TS: time.Now().Unix(),
		Offers: []protocol.ModelOffer{{Model: "m", Ctx: 4096}}, Private: true,
	}
	reg.SignRegistration(nodePriv)
	body, _ := json.Marshal(reg)
	r := httptest.NewRequest(http.MethodPost, "/nodes/register", bytes.NewReader(body))
	if signUser {
		signReq(r, userPriv, body)
	}
	w := httptest.NewRecorder()
	b.register(w, r)
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	return resp, w.Code
}

// TestPrivateRegisterLoginGated: anonymous private register is rejected (401); a
// signed but unlinked key is 403; a linked owner mints a band + gets the code ONCE,
// and a re-register returns only band_id (never the code again). The free cap of 1
// blocks a second band for the same owner.
func TestPrivateRegisterLoginGated(t *testing.T) {
	b, userPriv, nodePriv, nodePubHex := newBandBroker(t)

	// Anonymous (unsigned) private -> 401.
	if _, code := registerPrivate(t, b, nodePriv, nodePubHex, userPriv, false); code != http.StatusUnauthorized {
		t.Errorf("anon private register = %d, want 401", code)
	}

	// Signed, linked owner -> 200 + a one-time code.
	resp, code := registerPrivate(t, b, nodePriv, nodePubHex, userPriv, true)
	if code != http.StatusOK {
		t.Fatalf("owner private register = %d, want 200", code)
	}
	code1, _ := resp["band_code"].(string)
	if code1 == "" {
		t.Fatalf("first private register did not return a band_code")
	}
	if resp["band_id"] == nil {
		t.Errorf("response missing band_id")
	}

	// Re-register (same node) -> band_id but NO code (shown once).
	resp2, code := registerPrivate(t, b, nodePriv, nodePubHex, userPriv, true)
	if code != http.StatusOK {
		t.Fatalf("re-register = %d, want 200", code)
	}
	if c, _ := resp2["band_code"].(string); c != "" {
		t.Errorf("re-register leaked the code again: %q", c)
	}
	if resp2["band_id"] == nil {
		t.Errorf("re-register missing band_id")
	}

	// Free cap: a SECOND distinct node for the same owner is rejected (quota 1).
	reg := protocol.NodeRegistration{
		NodeID: "priv2", PubKey: nodePubHex, BridgeToken: "tok", TS: time.Now().Unix(),
		Offers: []protocol.ModelOffer{{Model: "m"}}, Private: true,
	}
	reg.SignRegistration(nodePriv)
	body, _ := json.Marshal(reg)
	r := httptest.NewRequest(http.MethodPost, "/nodes/register", bytes.NewReader(body))
	signReq(r, userPriv, body)
	w := httptest.NewRecorder()
	b.register(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("second band (over free cap) = %d, want 403", w.Code)
	}
}

// TestPrivateHiddenFromMarket: a private node never appears in /discover or /market.
func TestPrivateHiddenFromMarket(t *testing.T) {
	b, userPriv, nodePriv, nodePubHex := newBandBroker(t)
	if _, code := registerPrivate(t, b, nodePriv, nodePubHex, userPriv, true); code != http.StatusOK {
		t.Fatalf("register = %d", code)
	}

	// /discover must not list the private node.
	w := httptest.NewRecorder()
	b.discover(w, httptest.NewRequest(http.MethodGet, "/discover", nil))
	var disc struct {
		Offers []offerView `json:"offers"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &disc)
	for _, o := range disc.Offers {
		if o.NodeID == "priv1" {
			t.Errorf("/discover leaked the private node")
		}
	}

	// /market must not aggregate the private node (no providers for model "m").
	w = httptest.NewRecorder()
	b.market(w, httptest.NewRequest(http.MethodGet, "/market", nil))
	var mkt struct {
		Market []marketView `json:"market"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &mkt)
	for _, mv := range mkt.Market {
		if mv.Model == "m" && mv.Providers > 0 {
			t.Errorf("/market counted the private node as a public provider")
		}
	}
}

// TestBandResolveUniform: the resolver is a constant-work, no-oracle surface. A
// WRONG code, a REVOKED band, and a VALID-BUT-OFFLINE band all return the IDENTICAL
// 404 {"offers":[]} - so codes can't be enumerated by watching the response. A valid
// LIVE band returns the node's offers.
func TestBandResolveUniform(t *testing.T) {
	b, userPriv, nodePriv, nodePubHex := newBandBroker(t)
	resp, code := registerPrivate(t, b, nodePriv, nodePubHex, userPriv, true)
	if code != http.StatusOK {
		t.Fatalf("register = %d", code)
	}
	realCode := resp["band_code"].(string)

	resolve := func(freq string) (int, string) {
		body, _ := json.Marshal(map[string]string{"freq": freq})
		w := httptest.NewRecorder()
		b.bandResolve(w, httptest.NewRequest(http.MethodPost, "/bands/resolve", bytes.NewReader(body)))
		return w.Code, w.Body.String()
	}

	// Valid + live -> 200 with offers.
	st, body := resolve(realCode)
	if st != http.StatusOK || !strings.Contains(body, "\"node_id\":\"priv1\"") {
		t.Fatalf("valid resolve = %d body=%s, want 200 with the node's offers", st, body)
	}

	// Wrong code -> uniform 404 {"offers":[]}.
	wrongSt, wrongBody := resolve("147.520 MHz · ZZZZ-ZZZZ")
	if wrongSt != http.StatusNotFound {
		t.Errorf("wrong code = %d, want 404", wrongSt)
	}

	// Take the node OFFLINE (age out lastSeen), then resolve the REAL code: it must be
	// byte-identical to the wrong-code reply (no wrong-vs-offline oracle).
	b.mu.Lock()
	b.lastSeen["priv1"] = time.Now().Add(-2 * nodeTTL)
	b.mu.Unlock()
	offSt, offBody := resolve(realCode)
	if offSt != wrongSt || offBody != wrongBody {
		t.Errorf("offline-vs-wrong differ (oracle!): off=(%d,%q) wrong=(%d,%q)", offSt, offBody, wrongSt, wrongBody)
	}
}

// TestFreqRoutesOnlyToBandNode: a resolved X-Roger-Freq admits ONLY its node into
// pick (privateAllow), and a private node is never picked without it. Also covers
// the relay-level uniform error for an unknown freq.
func TestFreqRoutesOnlyToBandNode(t *testing.T) {
	b, userPriv, nodePriv, nodePubHex := newBandBroker(t)
	resp, code := registerPrivate(t, b, nodePriv, nodePubHex, userPriv, true)
	if code != http.StatusOK {
		t.Fatalf("register = %d", code)
	}
	realCode := resp["band_code"].(string)

	// Without a freq, the private node is invisible to pick (public market path).
	b.mu.Lock()
	_, _, ok := b.pick("m", false, 0, 0, 0, "", nil, nil, nil)
	b.mu.Unlock()
	if ok {
		t.Errorf("private node picked on the OPEN MARKET path (no freq) - must be hidden")
	}

	// resolveFreqAllow on the real code yields exactly {priv1}; pick then admits it.
	allow, band, present := b.resolveFreqAllow(realCode, time.Now())
	if !present || !allow["priv1"] || band.NodeID != "priv1" {
		t.Fatalf("resolveFreqAllow(real) = allow=%v band=%+v present=%v", allow, band, present)
	}
	b.mu.Lock()
	node, _, ok := b.pick("m", false, 0, 0, 0, "", nil, nil, allow)
	b.mu.Unlock()
	if !ok || node.NodeID != "priv1" {
		t.Errorf("freq-admitted pick = %+v ok=%v, want priv1", node, ok)
	}

	// An unknown freq -> present but empty allow (the relay turns this into the uniform
	// "no station on that frequency" 503).
	allow2, _, present2 := b.resolveFreqAllow("147.520 MHz · ZZZZ-ZZZZ", time.Now())
	if !present2 || len(allow2) != 0 {
		t.Errorf("unknown freq = allow=%v present=%v, want present + empty allow", allow2, present2)
	}
}

// TestPrivateSelfUseFree: consuming your OWN private node is $0 (resolvePricing free),
// exactly like a public self-use - the freq is admission, not a bill.
func TestPrivateSelfUseFree(t *testing.T) {
	b, userPriv, nodePriv, nodePubHex := newBandBroker(t)
	if _, code := registerPrivate(t, b, nodePriv, nodePubHex, userPriv, true); code != http.StatusOK {
		t.Fatalf("register = %d", code)
	}
	// The node's owner pubkey is the user pubkey (bound at register). Self-use is when
	// the consuming signed identity == UserIDFromPubkey(owner pubkey).
	ownerPubHex := hex.EncodeToString(userPriv.Public().(ed25519.PublicKey))
	selfID := protocol.UserIDFromPubkey(ownerPubHex)
	node := b.nodes["priv1"]
	pricing := b.resolvePricing(grantContext{}, false, selfID, "wallet", node, node.Offers[0])
	if !pricing.free {
		t.Errorf("self-use of own private node billed (free=%v) - should be $0", pricing.free)
	}
}

// TestBandOffersCarryRealMetrics: a PRIVATE band offer must carry the SAME real,
// enriched metrics the public /discover path computes - a non-zero signal + a
// populated terms breakdown + the verified bit + ttft + ctx - not the degraded/empty
// view the band path used to return. This asserts the shared enrichment ran on the
// private path (the metrics are identical to what the same node yields on /discover).
func TestBandOffersCarryRealMetrics(t *testing.T) {
	b, userPriv, nodePriv, nodePubHex := newBandBroker(t)
	// metricsMu-guarded maps the enrichment reads/writes. Seed a recently-probed,
	// canary-passed, fast node so the signal is well above zero and verified is true.
	b.trust = map[string]trustState{}
	b.success = map[string]float64{}
	b.inflight = map[string]int{}
	b.concurrentTPS = map[string]float64{}
	b.banned = map[string]bool{}
	b.successCount = map[string]int{}

	resp, code := registerPrivate(t, b, nodePriv, nodePubHex, userPriv, true)
	if code != http.StatusOK {
		t.Fatalf("register = %d", code)
	}
	realCode := resp["band_code"].(string)

	now := time.Now()
	b.mu.Lock()
	b.lastSeen["priv1"] = now
	b.mu.Unlock()
	b.metricsMu.Lock()
	b.trust["priv1"] = trustState{probed: true, probeOK: true, ttftMs: 250}
	b.tps["priv1"] = 120
	b.metricsMu.Unlock()

	// Resolve the band and assert the enriched fields are populated.
	body, _ := json.Marshal(map[string]string{"freq": realCode})
	w := httptest.NewRecorder()
	b.bandResolve(w, httptest.NewRequest(http.MethodPost, "/bands/resolve", bytes.NewReader(body)))
	if w.Code != http.StatusOK {
		t.Fatalf("band resolve = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var out struct {
		Offers []offerView `json:"offers"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Offers) != 1 {
		t.Fatalf("got %d band offers, want 1", len(out.Offers))
	}
	o := out.Offers[0]
	if o.NodeID != "priv1" {
		t.Fatalf("offer node = %q, want priv1", o.NodeID)
	}
	if o.Signal <= 0 {
		t.Errorf("private-band Signal = %d, want a non-zero enriched signal", o.Signal)
	}
	if o.Terms.Total != o.Signal || o.Terms == (signalTerms{}) {
		t.Errorf("private-band Terms not enriched: terms=%+v signal=%d", o.Terms, o.Signal)
	}
	if !o.Verified {
		t.Error("private-band Verified = false, want true (node has a passed canary)")
	}
	if !o.Online {
		t.Error("private-band Online = false, want true")
	}
	if o.TTFTMs != 250 || o.TPS != 120 {
		t.Errorf("private-band ttft=%v tps=%v, want 250/120", o.TTFTMs, o.TPS)
	}
	if o.Ctx != 4096 {
		t.Errorf("private-band ctx = %d, want 4096 (carried from the offer)", o.Ctx)
	}

	// Equivalence: the SAME node on the public /discover path yields the same signal +
	// verified, proving the band path reuses the shared enrichment, not a degraded copy.
	b.mu.Lock()
	b.private["priv1"] = false // expose it publicly just for this comparison
	b.mu.Unlock()
	dw := httptest.NewRecorder()
	b.discover(dw, httptest.NewRequest(http.MethodGet, "/discover", nil))
	var disc struct {
		Offers []offerView `json:"offers"`
	}
	_ = json.Unmarshal(dw.Body.Bytes(), &disc)
	var pub *offerView
	for i := range disc.Offers {
		if disc.Offers[i].NodeID == "priv1" {
			pub = &disc.Offers[i]
			break
		}
	}
	if pub == nil {
		t.Fatal("public /discover did not list the node for the equivalence check")
	}
	// Signal + verified must match exactly; Terms is float-valued and scaled by a recency
	// factor computed at each path's own time.Now(), so it can differ by a sub-point
	// rounding - assert the integer Total agrees rather than exact float equality.
	if pub.Signal != o.Signal || pub.Verified != o.Verified || pub.Terms.Total != o.Terms.Total {
		t.Errorf("band vs discover metrics differ: band(sig=%d ver=%v terms=%+v) discover(sig=%d ver=%v terms=%+v)",
			o.Signal, o.Verified, o.Terms, pub.Signal, pub.Verified, pub.Terms)
	}
}

// TestRegisterPriceCeiling: a public node whose price exceeds the hard ceiling is
// rejected at register with copy that states the REAL remedy (lower the price below the
// ceiling) and does NOT suggest --private as a price bypass - the ceiling is global for
// every band (pinned across public/private/confidential by TestRegisterCeilingGlobalAllBands).
func TestRegisterPriceCeiling(t *testing.T) {
	b, _, nodePriv, nodePubHex := newBandBroker(t)
	reg := protocol.NodeRegistration{
		NodeID: "greedy", PubKey: nodePubHex, BridgeToken: "tok", TS: time.Now().Unix(),
		Offers: []protocol.ModelOffer{{Model: "m", PriceOut: 9999}}, // way over the $100 ceiling
	}
	reg.SignRegistration(nodePriv)
	body, _ := json.Marshal(reg)
	w := httptest.NewRecorder()
	b.register(w, httptest.NewRequest(http.MethodPost, "/nodes/register", bytes.NewReader(body)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("over-ceiling register = %d, want 400", w.Code)
	}
	resp := w.Body.String()
	if !strings.Contains(resp, "lower the price below the ceiling") {
		t.Errorf("ceiling rejection should state the real remedy (lower the price), got %q", resp)
	}
	if strings.Contains(resp, "--private") {
		t.Errorf("ceiling rejection must NOT suggest --private as a price bypass, got %q", resp)
	}
}
