package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bownux/rogerai/internal/protocol"
	"github.com/bownux/rogerai/internal/store"
)

// TestRegisterProofOfPossession verifies #24: a node must sign its registration
// with the private key for the pub_key it claims, the registration must be fresh,
// and a node id cannot be taken over by a different key.
func TestRegisterProofOfPossession(t *testing.T) {
	b := &broker{
		nodes:        map[string]protocol.NodeRegistration{},
		tunnels:      map[string]*nodeTunnel{},
		lastSeen:     map[string]time.Time{},
		confidential: map[string]bool{},
		tps:          map[string]float64{},
	}
	post := func(ts int64, signer ed25519.PrivateKey, pubHex string) int {
		reg := protocol.NodeRegistration{NodeID: "n1", PubKey: pubHex, TS: ts, Offers: []protocol.ModelOffer{{Model: "m"}}}
		reg.SignRegistration(signer)
		body, _ := json.Marshal(reg)
		w := httptest.NewRecorder()
		b.register(w, httptest.NewRequest(http.MethodPost, "/nodes/register", bytes.NewReader(body)))
		return w.Code
	}
	pub, priv, _ := ed25519.GenerateKey(nil)
	pubHex := hex.EncodeToString(pub)

	if code := post(time.Now().Unix(), priv, pubHex); code != http.StatusOK {
		t.Fatalf("valid register = %d, want 200", code)
	}
	_, other, _ := ed25519.GenerateKey(nil)
	if code := post(time.Now().Unix(), other, pubHex); code != http.StatusUnauthorized {
		t.Errorf("wrong-key signature = %d, want 401", code)
	}
	if code := post(time.Now().Add(-10*time.Minute).Unix(), priv, pubHex); code != http.StatusUnauthorized {
		t.Errorf("stale timestamp = %d, want 401", code)
	}
	pub2, priv2, _ := ed25519.GenerateKey(nil)
	if code := post(time.Now().Unix(), priv2, hex.EncodeToString(pub2)); code != http.StatusForbidden {
		t.Errorf("node_id takeover = %d, want 403", code)
	}
}

// TestIdentityOf verifies the P0 auth resolver: a signed request yields the
// verified pubkey-derived id (authed), an unsigned request falls back to the
// legacy header (unauthenticated), and a present-but-invalid signature is rejected.
func TestIdentityOf(t *testing.T) {
	b := &broker{pubOfUser: map[string]string{}}
	_, priv, _ := ed25519.GenerateKey(nil)
	body := []byte(`{"model":"m"}`)

	signed := func(method, path string, bd []byte) *http.Request {
		r := httptest.NewRequest(method, path, nil)
		pub, ts, sig := protocol.SignRequest(priv, method, path, bd)
		r.Header.Set(protocol.HeaderPubkey, pub)
		r.Header.Set(protocol.HeaderTS, strconv.FormatInt(ts, 10))
		r.Header.Set(protocol.HeaderSig, sig)
		return r
	}

	// Valid signature → verified id, authed, ok.
	id, authed, ok := b.identityOf(signed("POST", "/v1/chat/completions", body), body)
	if !ok || !authed {
		t.Fatalf("signed request: authed=%v ok=%v, want both true", authed, ok)
	}
	pubHex := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	if want := protocol.UserIDFromPubkey(pubHex); id != want {
		t.Errorf("identity = %q, want pubkey-derived %q", id, want)
	}

	// identityOf prefers the verified id even when a (spoofed) X-Roger-User is also
	// present - the verified pubkey wins, so a header can't redirect the wallet.
	r := signed("POST", "/v1/chat/completions", body)
	r.Header.Set(protocol.HeaderUser, "victim")
	if vid, va, _ := b.identityOf(r, body); !va || vid == "victim" {
		t.Errorf("verified id must win over X-Roger-User: id=%q authed=%v", vid, va)
	}

	// Present but INVALID signature (tampered body) → rejected (ok=false → 401).
	if _, _, ok := b.identityOf(signed("POST", "/v1/chat/completions", body), []byte(`{"model":"evil"}`)); ok {
		t.Error("invalid signature must be rejected (ok=false)")
	}

	// Malformed ts with signing headers present → rejected.
	bad := httptest.NewRequest("POST", "/x", nil)
	bad.Header.Set(protocol.HeaderPubkey, pubHex)
	bad.Header.Set(protocol.HeaderSig, "00")
	bad.Header.Set(protocol.HeaderTS, "notanumber")
	if _, _, ok := b.identityOf(bad, body); ok {
		t.Error("bad ts header must be rejected")
	}

	// Unsigned with legacy header → unauthenticated fallback (ok=true, authed=false).
	leg := httptest.NewRequest("GET", "/balance", nil)
	leg.Header.Set(protocol.HeaderUser, "legacy-user")
	uid, ua, uok := b.identityOf(leg, nil)
	if !uok || ua || uid != "legacy-user" {
		t.Errorf("legacy unsigned = (%q,%v,%v), want (legacy-user,false,true)", uid, ua, uok)
	}
}

func TestLockedPrice(t *testing.T) {
	b := &broker{quotes: map[string]priceQuote{}, lockWin: time.Hour}

	// first use → quote + lock at current price
	if in, out, _ := b.lockedPrice("u", "n", "m", 0.20, 0.30); in != 0.20 || out != 0.30 {
		t.Fatalf("first quote = %v/%v, want 0.20/0.30", in, out)
	}
	// owner RAISES → user still billed the locked price (protection)
	if in, out, _ := b.lockedPrice("u", "n", "m", 0.50, 0.80); in != 0.20 || out != 0.30 {
		t.Errorf("raise not protected: %v/%v, want 0.20/0.30", in, out)
	}
	// owner CUTS → user gets the lower price (min)
	if in, out, _ := b.lockedPrice("u", "n", "m", 0.05, 0.05); in != 0.05 || out != 0.05 {
		t.Errorf("cut not passed through: %v/%v, want 0.05/0.05", in, out)
	}
	// a different user is quoted independently at the current price
	if in, _, _ := b.lockedPrice("other", "n", "m", 0.50, 0.80); in != 0.50 {
		t.Errorf("other user quote = %v, want 0.50", in)
	}
	// after the window expires → re-quote at current
	b.quotes["u|n|m"] = priceQuote{in: 0.20, out: 0.30, until: time.Now().Add(-time.Minute)}
	if in, _, _ := b.lockedPrice("u", "n", "m", 0.40, 0.40); in != 0.40 {
		t.Errorf("post-expiry re-quote = %v, want 0.40", in)
	}
}

// TestPickPinAndExclude verifies the failover routing hints: a pinned node is the
// only candidate, and excluded nodes (the ones a client just saw fail) are skipped.
func TestPickPinAndExclude(t *testing.T) {
	now := time.Now()
	b := &broker{
		nodes: map[string]protocol.NodeRegistration{
			"a": {NodeID: "a", Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 0.5, PriceOut: 0.5}}},
			"b": {NodeID: "b", Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 0.2, PriceOut: 0.2}}},
			"c": {NodeID: "c", Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 0.1, PriceOut: 0.1}}},
		},
		lastSeen:     map[string]time.Time{"a": now, "b": now, "c": now},
		confidential: map[string]bool{},
		tps:          map[string]float64{},
	}

	// No pin/exclude → cheapest (c).
	if n, _, ok := b.pick("m", false, 0, 0, 0, "", nil); !ok || n.NodeID != "c" {
		t.Errorf("cheapest pick = %q ok=%v, want c", n.NodeID, ok)
	}
	// Exclude the cheapest two → must fall back to a.
	if n, _, ok := b.pick("m", false, 0, 0, 0, "", map[string]bool{"c": true, "b": true}); !ok || n.NodeID != "a" {
		t.Errorf("excluded pick = %q ok=%v, want a", n.NodeID, ok)
	}
	// Pin to b → only b, even though c is cheaper.
	if n, _, ok := b.pick("m", false, 0, 0, 0, "b", nil); !ok || n.NodeID != "b" {
		t.Errorf("pinned pick = %q ok=%v, want b", n.NodeID, ok)
	}
	// Pin to an excluded node → nothing eligible.
	if _, _, ok := b.pick("m", false, 0, 0, 0, "b", map[string]bool{"b": true}); ok {
		t.Error("pin+exclude of the same node should yield nothing")
	}
	// Exclude every node → nothing.
	if _, _, ok := b.pick("m", false, 0, 0, 0, "", map[string]bool{"a": true, "b": true, "c": true}); ok {
		t.Error("excluding all nodes should yield nothing")
	}
}

// TestPickPriceCaps verifies the spend caps: a station is filtered out when its
// active INPUT price exceeds max-price-in OR its active OUTPUT price exceeds
// max-price-out (cap on both, 0 = no cap on that side). pick ranks by OUTPUT
// price, so within the survivors the cheapest-out wins (matches the quote).
func TestPickPriceCaps(t *testing.T) {
	now := time.Now()
	b := &broker{
		nodes: map[string]protocol.NodeRegistration{
			// cheap in, expensive out
			"a": {NodeID: "a", Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 0.10, PriceOut: 0.90}}},
			// mid in, cheap out
			"b": {NodeID: "b", Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 0.20, PriceOut: 0.20}}},
			// expensive in, mid out
			"c": {NodeID: "c", Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 0.50, PriceOut: 0.40}}},
		},
		lastSeen:     map[string]time.Time{"a": now, "b": now, "c": now},
		confidential: map[string]bool{},
		tps:          map[string]float64{},
	}

	// No caps → cheapest-OUT (b: out 0.20, vs a 0.90, c 0.40).
	if n, _, ok := b.pick("m", false, 0, 0, 0, "", nil); !ok || n.NodeID != "b" {
		t.Errorf("no caps pick = %q ok=%v, want b", n.NodeID, ok)
	}
	// Out cap 0.30 excludes a (0.90) and c (0.40); only b survives.
	if n, _, ok := b.pick("m", false, 0, 0, 0.30, "", nil); !ok || n.NodeID != "b" {
		t.Errorf("out-cap pick = %q ok=%v, want b", n.NodeID, ok)
	}
	// In cap 0.30 excludes c (in 0.50); a and b survive, cheapest-OUT is b (0.20 vs 0.90).
	if n, _, ok := b.pick("m", false, 0, 0.30, 0, "", nil); !ok || n.NodeID != "b" {
		t.Errorf("in-cap pick = %q ok=%v, want b", n.NodeID, ok)
	}
	// Both caps: in<=0.30 keeps a,b; out<=0.30 then drops a (out 0.90) → b.
	if n, _, ok := b.pick("m", false, 0, 0.30, 0.30, "", nil); !ok || n.NodeID != "b" {
		t.Errorf("both-caps pick = %q ok=%v, want b", n.NodeID, ok)
	}
	// Out cap below every station → nothing.
	if _, _, ok := b.pick("m", false, 0, 0, 0.10, "", nil); ok {
		t.Error("out cap below all stations should yield nothing")
	}
}

func TestDashboardEndpoints(t *testing.T) {
	mem := store.NewMem()
	b := &broker{db: mem, seedFunds: 100, lastSeen: map[string]time.Time{"n1": time.Now()}}
	// settle a couple of requests for alice on n1
	_, _ = mem.BalanceOf("alice", 100)
	for i, c := range []float64{1.0, 2.0} {
		rec := protocol.UsageReceipt{RequestID: []string{"a", "b"}[i], Model: "m", TS: int64(100 + i)}
		_, _ = mem.Settle("alice", "n1", c, c*0.7, rec)
	}

	// GET /me
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	req.Header.Set("X-Roger-User", "alice")
	b.me(rec, req)
	if rec.Code != 200 {
		t.Fatalf("/me status %d", rec.Code)
	}
	var me struct {
		Balance, Spend float64
		Recent         []store.Entry
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &me)
	if me.Spend != 3.0 {
		t.Errorf("/me spend = %v want 3.0", me.Spend)
	}
	if me.Balance != 97.0 {
		t.Errorf("/me balance = %v want 97.0", me.Balance)
	}
	if len(me.Recent) != 2 {
		t.Errorf("/me recent len = %d want 2", len(me.Recent))
	}

	// GET /earnings?node=n1
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/earnings?node=n1", nil)
	b.earnings(rec, req)
	if rec.Code != 200 {
		t.Fatalf("/earnings status %d", rec.Code)
	}
	var earn struct {
		Earnings float64
		Online   bool
		Recent   []store.Entry
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &earn)
	if !earn.Online {
		t.Error("/earnings should report n1 online")
	}
	if len(earn.Recent) != 2 {
		t.Errorf("/earnings recent len = %d want 2", len(earn.Recent))
	}

	// GET /earnings with no node → 400
	rec = httptest.NewRecorder()
	b.earnings(rec, httptest.NewRequest(http.MethodGet, "/earnings", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("/earnings without node = %d want 400", rec.Code)
	}
}

func TestMarketSignal(t *testing.T) {
	// No providers → dead channel.
	if s := marketSignal(0, 0, 500, 1); s != 0 {
		t.Errorf("no-providers signal = %d want 0", s)
	}
	// Healthy: plenty of supply, fast, reliable, idle → near max.
	full := marketSignal(5, 0, 300, 1.0)
	if full < 95 {
		t.Errorf("healthy signal = %d want ~100", full)
	}
	// More supply must not lower the signal (monotonic in supply).
	if marketSignal(1, 0, 300, 1) > marketSignal(3, 0, 300, 1) {
		t.Error("signal should not decrease with more providers")
	}
	// Congestion must lower the signal vs. the same idle channel.
	idle := marketSignal(2, 0, 300, 1)
	busy := marketSignal(2, 8, 300, 1)
	if busy >= idle {
		t.Errorf("congested (%d) should be < idle (%d)", busy, idle)
	}
	// Low success rate must lower the signal.
	if marketSignal(5, 0, 300, 0.2) >= marketSignal(5, 0, 300, 1.0) {
		t.Error("low success should reduce the signal")
	}
}

func TestMarketEndpoint(t *testing.T) {
	now := time.Now()
	b := &broker{
		nodes: map[string]protocol.NodeRegistration{
			"fast": {NodeID: "fast", Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 0.5}}},
			"cheap": {NodeID: "cheap", Offers: []protocol.ModelOffer{
				{Model: "m", PriceIn: 0.1}, {Model: "other", PriceIn: 0.3},
			}},
			"stale": {NodeID: "stale", Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 0.01}}},
		},
		lastSeen:     map[string]time.Time{"fast": now, "cheap": now, "stale": now.Add(-time.Minute)},
		confidential: map[string]bool{},
		tps:          map[string]float64{"fast": 250, "cheap": 30},
		inflight:     map[string]int{"fast": 2},
		success:      map[string]float64{"fast": 1.0, "cheap": 0.9},
	}

	rec := httptest.NewRecorder()
	b.market(rec, httptest.NewRequest(http.MethodGet, "/market", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	var resp struct {
		Market []marketView `json:"market"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)

	byModel := map[string]marketView{}
	for _, mv := range resp.Market {
		byModel[mv.Model] = mv
	}
	m, ok := byModel["m"]
	if !ok {
		t.Fatalf("model m missing from market %+v", resp.Market)
	}
	// stale node excluded → 2 providers (fast, cheap)
	if m.Providers != 2 {
		t.Errorf("providers = %d want 2 (stale excluded)", m.Providers)
	}
	// min price across online offers = 0.1 (cheap), not 0.01 (stale, offline)
	if m.MinPrice != 0.1 {
		t.Errorf("min_price = %v want 0.1", m.MinPrice)
	}
	if m.BestTPS != 250 {
		t.Errorf("best_tps = %v want 250", m.BestTPS)
	}
	if m.InFlight != 2 {
		t.Errorf("in_flight = %d want 2", m.InFlight)
	}
	if m.Signal <= 0 || m.Signal > 100 {
		t.Errorf("signal = %d out of range", m.Signal)
	}
}

func TestInflightAndSuccess(t *testing.T) {
	b := &broker{inflight: map[string]int{}, success: map[string]float64{}}
	b.enterInflight("n")
	b.enterInflight("n")
	if b.inflight["n"] != 2 {
		t.Fatalf("inflight = %d want 2", b.inflight["n"])
	}
	b.exitInflight("n", true)
	if b.inflight["n"] != 1 {
		t.Errorf("inflight after exit = %d want 1", b.inflight["n"])
	}
	if b.success["n"] != 1.0 {
		t.Errorf("success after one ok = %v want 1.0", b.success["n"])
	}
	b.exitInflight("n", false) // a failure pulls the EWMA below 1
	if b.success["n"] >= 1.0 {
		t.Errorf("success after a failure = %v want <1.0", b.success["n"])
	}
	// inflight never goes negative
	b.exitInflight("n", true)
	b.exitInflight("n", true)
	if b.inflight["n"] != 0 {
		t.Errorf("inflight = %d want 0 (clamped)", b.inflight["n"])
	}
}

func TestParseNodeSet(t *testing.T) {
	if parseNodeSet("") != nil {
		t.Error("empty header should be nil set")
	}
	got := parseNodeSet(" a, b ,,c ")
	for _, want := range []string{"a", "b", "c"} {
		if !got[want] {
			t.Errorf("missing %q in %v", want, got)
		}
	}
	if len(got) != 3 {
		t.Errorf("set size = %d want 3 (%v)", len(got), got)
	}
}

func TestVerifyAttestation(t *testing.T) {
	if verifyAttestation("") || verifyAttestation("short") || verifyAttestation("dev-placeholder-attestation") {
		t.Error("empty/short/placeholder attestation must not pass")
	}
	if !verifyAttestation(strings.Repeat("a", 64)) {
		t.Error("a 64+ char attestation should pass the stub")
	}
}

func TestCORSPreflight(t *testing.T) {
	b := &broker{
		nodes: map[string]protocol.NodeRegistration{}, lastSeen: map[string]time.Time{},
		tps: map[string]float64{}, confidential: map[string]bool{},
		inflight: map[string]int{}, success: map[string]float64{},
	}
	for _, ep := range []struct {
		name string
		h    http.HandlerFunc
	}{
		{"/discover", b.discover},
		{"/market", b.market},
	} {
		rec := httptest.NewRecorder()
		ep.h(rec, httptest.NewRequest(http.MethodOptions, ep.name, nil))
		if rec.Code != http.StatusNoContent {
			t.Errorf("%s OPTIONS = %d, want 204", ep.name, rec.Code)
		}
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
			t.Errorf("%s OPTIONS Allow-Origin = %q, want *", ep.name, got)
		}
		// GET still serves and carries the CORS header.
		rec = httptest.NewRecorder()
		ep.h(rec, httptest.NewRequest(http.MethodGet, ep.name, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s GET = %d, want 200", ep.name, rec.Code)
		}
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
			t.Errorf("%s GET Allow-Origin = %q, want *", ep.name, got)
		}
	}
}
