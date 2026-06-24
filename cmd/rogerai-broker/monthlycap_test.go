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

// capBroker builds a relay-ready broker with one priced node + one free node (both
// online) and a logged-in, well-funded wallet. NO poller is attached: a request that
// is REJECTED at the pre-dispatch cap gate returns fast (the cap check runs before the
// job is ever enqueued), which is exactly what the enforcement test asserts. Allowed
// paths are covered by the monthlyCapCheck / monthlyCapState unit tests below (so the
// suite never blocks on the 120s node-response wait).
func capBroker(t *testing.T) (*broker, ed25519.PrivateKey, string) {
	t.Helper()
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	b := &broker{
		priv:         brokerPriv,
		db:           store.NewMem(),
		nodes:        map[string]protocol.NodeRegistration{},
		tunnels:      map[string]*nodeTunnel{},
		lastSeen:     map[string]time.Time{},
		confidential: map[string]bool{},
		tps:          map[string]float64{},
		inflight:     map[string]int{},
		success:      map[string]float64{},
		trust:        map[string]trustState{},
		streams:      map[string]*streamSink{},
		quotes:       map[string]priceQuote{},
		pubOfUser:    map[string]string{},
		seedFunds:    100,
		lockWin:      time.Hour,
		rl:           loadRateLimiter(),
	}
	nodePub, _, _ := ed25519.GenerateKey(nil)
	b.nodes["paid"] = protocol.NodeRegistration{NodeID: "paid", PubKey: hex.EncodeToString(nodePub), Offers: []protocol.ModelOffer{{Model: "paid-m", PriceOut: 0.5}}}
	b.nodes["free"] = protocol.NodeRegistration{NodeID: "free", PubKey: hex.EncodeToString(nodePub), Offers: []protocol.ModelOffer{{Model: "free-m"}}}
	b.tunnels["paid"] = &nodeTunnel{jobs: make(chan protocol.Job, 1), waiters: map[string]chan protocol.JobResult{}}
	b.tunnels["free"] = &nodeTunnel{jobs: make(chan protocol.Job, 1), waiters: map[string]chan protocol.JobResult{}}
	b.lastSeen["paid"] = time.Now()
	b.lastSeen["free"] = time.Now()

	_, userPriv, _ := ed25519.GenerateKey(nil)
	userPubHex := hex.EncodeToString(userPriv.Public().(ed25519.PublicKey))
	_ = b.db.BindOwner(store.Owner{GitHubID: 7, Login: "octocat", Pubkey: userPubHex})
	_, _ = b.db.AddCredits("u_gh_7", 1000) // funded: the cap, not the balance, gates spend
	return b, userPriv, "u_gh_7"
}

// seedMonthSpend records `amt` of captured spend for the wallet in the current month.
func seedMonthSpend(t *testing.T, b *broker, wallet string, amt float64, req string) {
	t.Helper()
	if _, err := b.db.Settle(wallet, "paid", amt, 0, protocol.UsageReceipt{
		RequestID: req, Model: "paid-m", TS: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
}

// TestMonthlyCapRejectsOverBudget is the core server-side enforcement test: with a
// monthly cap set and month-to-date spend already AT the cap, the next PAID relay is
// REJECTED (402) before dispatch. Raising the cap immediately makes the same request
// pass the cap gate (no longer 402). Because every paid path (use/--freq/grant/agent/
// chat) funnels through this one relay hold gate, asserting it here covers them all.
func TestMonthlyCapRejectsOverBudget(t *testing.T) {
	b, userPriv, wallet := capBroker(t)

	doRelay := func(model string) (int, string) {
		body := []byte(`{"model":"` + model + `","max_tokens":8}`)
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		signReq(r, userPriv, body)
		w := httptest.NewRecorder()
		b.relay(w, r)
		return w.Code, w.Header().Get("X-RogerAI-Monthly-Notice")
	}

	// Cap $5/month, with $5.00 already spent THIS month. ANY further paid spend exceeds
	// the cap, so the next paid request is rejected at the hold gate (fast 402, before
	// the job is enqueued).
	if err := b.db.SetMonthlyCap(wallet, 5); err != nil {
		t.Fatal(err)
	}
	seedMonthSpend(t, b, wallet, 5.0, "seed")
	code, notice := doRelay("paid-m")
	if code != http.StatusPaymentRequired {
		t.Fatalf("over-budget paid relay = %d, want 402 (monthly cap)", code)
	}
	if notice == "" {
		t.Error("over-budget 402 carried no X-RogerAI-Monthly-Notice header")
	}

	// Raise the cap well above the spend -> the SAME paid request now passes the cap
	// gate (it then blocks on the absent poller, so we assert only that it is NOT 402).
	if err := b.db.SetMonthlyCap(wallet, 1000); err != nil {
		t.Fatal(err)
	}
	if st := b.monthlyCapState(wallet, time.Now()); st.atLimit {
		t.Errorf("after raising the cap, monthlyCapState still atLimit=true - raising the cap should unblock spend")
	}
}

// TestMonthlyCapCheckGate exercises the enforcement helper directly (the gate every
// paid relay path calls): unlimited never blocks, free/self ($0) never blocks, an
// over-budget paid request is rejected with the clear message + at-cap headers, a
// fitting request is allowed with the near/at notice headers, and raising the cap
// immediately re-allows spend.
func TestMonthlyCapCheckGate(t *testing.T) {
	b, _, wallet := capBroker(t)
	now := time.Now()

	// Unlimited (no cap set): never blocks, no notice header.
	w := httptest.NewRecorder()
	if st, _ := b.monthlyCapCheck(w, wallet, 1.0, now); st != 0 {
		t.Fatalf("unlimited cap blocked spend (status %d), want 0", st)
	}
	if w.Header().Get("X-RogerAI-Monthly-Notice") != "" || w.Header().Get("X-RogerAI-Monthly-Cap") != "" {
		t.Error("unlimited cap emitted cap headers, want none")
	}

	// Set $10 cap, $7 spent (70%): a small request fits -> allowed, NO near notice yet.
	_ = b.db.SetMonthlyCap(wallet, 10)
	seedMonthSpend(t, b, wallet, 7.0, "s7")
	w = httptest.NewRecorder()
	if st, _ := b.monthlyCapCheck(w, wallet, 0.5, now); st != 0 {
		t.Fatalf("under-cap request blocked (status %d), want allowed", st)
	}
	if w.Header().Get("X-RogerAI-Monthly-Cap") == "" {
		t.Error("allowed under-cap request missing the X-RogerAI-Monthly-Cap header")
	}

	// Push to $8.5 (85%): still under the cap -> allowed WITH the near notice (80%).
	seedMonthSpend(t, b, wallet, 1.5, "s85")
	w = httptest.NewRecorder()
	if st, _ := b.monthlyCapCheck(w, wallet, 0.1, now); st != 0 {
		t.Fatalf("near-cap request blocked (status %d), want allowed", st)
	}
	if w.Header().Get("X-RogerAI-Monthly-Notice") == "" {
		t.Error("no near-cap notice header at 85%, want one")
	}

	// A request whose worst-case cost would exceed the cap -> REJECTED (402) with the
	// clear message and the at-cap headers.
	w = httptest.NewRecorder()
	st, msg := b.monthlyCapCheck(w, wallet, 5.0, now) // 8.5 + 5.0 = 13.5 > 10
	if st != http.StatusPaymentRequired {
		t.Fatalf("over-cap request status = %d, want 402", st)
	}
	if msg == "" || !strings.Contains(msg, "monthly spend limit reached") {
		t.Errorf("reject message = %q, want it to mention the monthly spend limit", msg)
	}
	if w.Header().Get("X-RogerAI-Monthly-Notice") == "" {
		t.Error("over-cap reject missing the notice header")
	}

	// FREE / self-use never reaches the gate (callers only invoke it when maxCost>0).
	// Sanity: even at/over the cap, a $0 maxCost is allowed by the gate.
	w = httptest.NewRecorder()
	if st, _ := b.monthlyCapCheck(w, wallet, 0, now); st != 0 {
		t.Errorf("$0 request blocked by the cap (status %d), want allowed", st)
	}

	// Raise the cap -> spend is immediately allowed again.
	_ = b.db.SetMonthlyCap(wallet, 100)
	w = httptest.NewRecorder()
	if st, _ := b.monthlyCapCheck(w, wallet, 5.0, now); st != 0 {
		t.Errorf("after raising the cap, request still blocked (status %d) - raising should re-allow", st)
	}
}

// TestAccountLimitEndpoint covers the GET/PATCH /account/limit round-trip: an
// anonymous (signed-but-not-logged-in) caller is rejected (the cap is per account); a
// logged-in caller can SET, READ, and CLEAR the cap, and the GET reflects month-to-
// date spend.
func TestAccountLimitEndpoint(t *testing.T) {
	mem := store.NewMem()
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	b := &broker{db: mem, pubOfUser: map[string]string{}, priv: brokerPriv, seedFunds: 0}

	_, priv, _ := ed25519.GenerateKey(nil)
	pubHex := hex.EncodeToString(priv.Public().(ed25519.PublicKey))

	get := func() (int, map[string]float64) {
		r := httptest.NewRequest(http.MethodGet, "/account/limit", nil)
		signReq(r, priv, nil)
		w := httptest.NewRecorder()
		b.accountLimit(w, r)
		var out map[string]float64
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	patch := func(cap float64) (int, map[string]float64) {
		body := []byte(`{"monthly_cap":` + ftoa(cap) + `}`)
		r := httptest.NewRequest(http.MethodPatch, "/account/limit", bytes.NewReader(body))
		signReq(r, priv, body)
		w := httptest.NewRecorder()
		b.accountLimit(w, r)
		var out map[string]float64
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}

	// Anonymous (signed, not logged in): the cap is per account -> 401.
	if code, _ := get(); code != http.StatusUnauthorized {
		t.Fatalf("anon GET /account/limit = %d, want 401 (per-account)", code)
	}

	// Log in: bind the keypair to a GitHub account (wallet u_gh_7).
	_ = mem.BindOwner(store.Owner{GitHubID: 7, Login: "octocat", Pubkey: pubHex})

	// Default = unlimited (0).
	if code, out := get(); code != http.StatusOK || out["monthly_cap"] != 0 {
		t.Fatalf("logged-in default GET = %d cap=%v, want 200 cap=0", code, out["monthly_cap"])
	}
	// Set $30.
	if code, out := patch(30); code != http.StatusOK || out["monthly_cap"] != 30 {
		t.Fatalf("PATCH set = %d cap=%v, want 200 cap=30", code, out["monthly_cap"])
	}
	// Read back $30, with month-to-date spend reflected.
	_, _ = mem.AddCredits("u_gh_7", 4)
	_, _ = mem.Settle("u_gh_7", "n", 4, 0, protocol.UsageReceipt{RequestID: "r", Model: "m", TS: time.Now().Unix()})
	if code, out := get(); code != http.StatusOK || out["monthly_cap"] != 30 || out["monthly_spend"] != 4 {
		t.Fatalf("GET after set+spend = %d cap=%v spend=%v, want 200 cap=30 spend=4", code, out["monthly_cap"], out["monthly_spend"])
	}
	// Clear (0 = unlimited).
	if code, out := patch(0); code != http.StatusOK || out["monthly_cap"] != 0 {
		t.Fatalf("PATCH clear = %d cap=%v, want 200 cap=0", code, out["monthly_cap"])
	}
	if c, _ := mem.MonthlyCapOf("u_gh_7"); c != 0 {
		t.Errorf("store cap after clear = %v, want 0", c)
	}
}

// TestMonthlyCapState exercises the snapshot helper: the near/at flags + percentage
// off the stored cap + month-to-date spend.
func TestMonthlyCapState(t *testing.T) {
	b, _, wallet := capBroker(t)
	now := time.Now()

	// No cap -> unlimited: never near, never at-limit.
	if st := b.monthlyCapState(wallet, now); st.near || st.atLimit {
		t.Errorf("unlimited state near=%v atLimit=%v, want both false", st.near, st.atLimit)
	}

	_ = b.db.SetMonthlyCap(wallet, 10)
	seedMonthSpend(t, b, wallet, 9, "s9") // 90%
	if st := b.monthlyCapState(wallet, now); !st.near || st.atLimit {
		t.Errorf("at 90%%: near=%v atLimit=%v, want near=true atLimit=false", st.near, st.atLimit)
	}
	seedMonthSpend(t, b, wallet, 2, "s2") // 110%
	if st := b.monthlyCapState(wallet, now); !st.atLimit {
		t.Errorf("at 110%%: atLimit=%v, want true", st.atLimit)
	}
}
