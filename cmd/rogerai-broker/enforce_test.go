package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// enforce_test.go covers the broker-side enforcement: the owner-keyed strike->ban
// ladder, the durable owner ban blocking register + relay pick under a NEW node id
// (anti-rotation), the impossible-input byte floor, and the void-on-no-output gate.

// enforceBroker builds a broker wired with a real Mem store + the strike thresholds +
// all maps the enforcement paths touch.
func enforceBroker(db store.Store) *broker {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	return &broker{
		db:           db,
		priv:         ed25519.NewKeyFromSeed(seed),
		nodes:        map[string]protocol.NodeRegistration{},
		tunnels:      map[string]*nodeTunnel{},
		lastSeen:     map[string]time.Time{},
		confidential: map[string]bool{},
		private:      map[string]bool{},
		bandOf:       map[string]string{},
		tps:          map[string]float64{},
		inflight:     map[string]int{},
		success:      map[string]float64{},
		trust:        map[string]trustState{},
		banned:       map[string]bool{},
		bannedOwners: map[string]bool{},
		strikeWarnAt: strikeWarnAt(),
		strikeBanAt:  strikeBanAt(),
		recount:      recountConfig{tolerance: 0.02},
		rl:           loadRateLimiter(),
	}
}

// TestImpossibleInputStrikesAndBans: a claim of MORE prompt tokens than the body has
// bytes is a zero-doubt signal -> the owner is struck WITH evidence and banned on the
// first strike. The strike evidence records claimed-vs-bytes.
func TestImpossibleInputStrikesAndBans(t *testing.T) {
	db := store.NewMem()
	b := enforceBroker(db)
	_ = db.BindNode("node1", "owner1")

	// settleRecountPrompt with claimed (5000) > bodyLen (50) must clamp to 50 AND strike.
	billed := b.settleRecountPrompt("node1", "rq1", "m", "tiny prompt", 5000, 50)
	if billed != 50 {
		t.Errorf("byte-floor clamp = %d, want 50 (clamped to body bytes)", billed)
	}
	strikes, _ := db.StrikesByOwner("owner1", 0)
	if len(strikes) == 0 {
		t.Fatal("impossible input must record an owner strike with evidence")
	}
	if !strings.Contains(strikes[len(strikes)-1].Evidence, "5000") {
		t.Errorf("strike evidence must record the claimed count, got %q", strikes[len(strikes)-1].Evidence)
	}
	// Zero-doubt -> banned on the first strike.
	if banned, _, _ := db.IsOwnerBanned("owner1"); !banned {
		t.Error("impossible input is zero-doubt and must ban the owner on the first strike")
	}
	if !b.isOwnerBanned("owner1") {
		t.Error("the in-memory owner-ban cache must reflect the ban immediately")
	}
}

// TestSettleRecountPromptNoClampWhenPlausible: a claim within the byte budget is NOT
// clamped and does NOT strike when the recount sidecar is off.
func TestSettleRecountPromptNoClampWhenPlausible(t *testing.T) {
	db := store.NewMem()
	b := enforceBroker(db) // recount disabled (no url)
	_ = db.BindNode("node1", "owner1")
	if got := b.settleRecountPrompt("node1", "rq1", "m", "a much longer prompt body", 10, 200); got != 10 {
		t.Errorf("plausible claim must pass through unchanged, got %d", got)
	}
	if strikes, _ := db.StrikesByOwner("owner1", 0); len(strikes) != 0 {
		t.Errorf("a plausible claim must NOT strike, got %d strikes", len(strikes))
	}
}

// TestEmptyOutputAccruesStrikesThenBans: empty-output is an ACCUMULATING signal - it
// warns, then bans the owner once it crosses the ban threshold. Each strike carries
// evidence; idempotency means the same request id does not double-strike.
func TestEmptyOutputAccruesStrikesThenBans(t *testing.T) {
	db := store.NewMem()
	b := enforceBroker(db)
	b.strikeWarnAt, b.strikeBanAt = 2, 3
	_ = db.BindNode("node1", "owner1")

	rec := func(id string) protocol.UsageReceipt {
		return protocol.UsageReceipt{RequestID: id, NodeID: "node1", PromptTokens: 100, CompletionTokens: 0}
	}
	// Strikes 1, 2 (warn at 2): not yet banned.
	b.flagEmptyOutput("node1", rec("r1"), 200)
	b.flagEmptyOutput("node1", rec("r2"), 200)
	if banned, _, _ := db.IsOwnerBanned("owner1"); banned {
		t.Error("owner must NOT be banned before the ban threshold")
	}
	// A retry of r2 must not double-strike (idempotent).
	b.flagEmptyOutput("node1", rec("r2"), 200)
	if n := len(mustStrikes(t, db, "owner1")); n != 2 {
		t.Errorf("retry must not double-strike, strike count = %d, want 2", n)
	}
	// Strike 3 crosses the ban threshold.
	b.flagEmptyOutput("node1", rec("r3"), 200)
	if banned, _, _ := db.IsOwnerBanned("owner1"); !banned {
		t.Error("owner must be banned once empty-output strikes cross the ban threshold")
	}
}

func mustStrikes(t *testing.T, db store.Store, acct string) []store.Strike {
	t.Helper()
	s, err := db.StrikesByOwner(acct, 0)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestOwnerBanBlocksRegisterUnderNewNodeID: a banned owner cannot come back on air under
// a FRESH node id / callsign - register rejects the new node id, keyed on the durable
// owner account, not the (rotated) node id.
func TestOwnerBanBlocksRegisterUnderNewNodeID(t *testing.T) {
	db := store.NewMem()
	b := enforceBroker(db)

	// Owner with a signing pubkey, bound to a GitHub account (earning requires login).
	pub, priv, _ := ed25519.GenerateKey(nil)
	pubHex := hex.EncodeToString(pub)
	ownerPubkey := protocol.UserIDFromPubkey(pubHex) // NOTE: requireOwner keys on the raw pubkey
	_ = ownerPubkey
	_ = db.BindOwner(store.Owner{GitHubID: 7, Login: "op", Pubkey: pubHex})
	// Ban the OWNER account (owner pubkey is the durable identity used by requireOwner).
	b.banOwner(pubHex, "impossible-input", `{}`)

	// Register a BRAND NEW node id under the banned owner's signing key.
	reg := signedPricedReg(t, "fresh-callsign-999", priv, pubHex)
	rec := postRegister(t, b, reg, priv, pubHex)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("banned owner registering a NEW node id: want 403, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	if _, ok := b.nodes["fresh-callsign-999"]; ok {
		t.Error("a banned owner's fresh node id must NOT go on air")
	}
}

// TestOwnerBanBlocksRelayPickUnderNewNodeID: a banned owner's FRESH node id is dropped
// from relay routing (pick) even though it is live and offers the model - the ban
// follows the owner, not the rotated node id.
func TestOwnerBanBlocksRelayPickUnderNewNodeID(t *testing.T) {
	db := store.NewMem()
	b := enforceBroker(db)

	// Two nodes, DIFFERENT ids, SAME owner. Owner is banned.
	_ = db.BindNode("old-id", "owner1")
	_ = db.BindNode("new-id", "owner1") // the "comeback" callsign
	b.banOwner("owner1", "empty-output", `{}`)

	now := time.Now()
	b.nodes["new-id"] = protocol.NodeRegistration{NodeID: "new-id", Offers: []protocol.ModelOffer{{Model: "m", PriceOut: 0.1}}}
	b.lastSeen["new-id"] = now

	b.mu.Lock()
	_, _, ok := b.pick("m", false, 0, 0, 0, "", nil, nil, nil)
	b.mu.Unlock()
	if ok {
		t.Error("a banned owner's fresh node id must be dropped from relay pick (anti-rotation)")
	}

	// A node under a DIFFERENT, un-banned owner is still pickable.
	_ = db.BindNode("honest-id", "owner2")
	b.nodes["honest-id"] = protocol.NodeRegistration{NodeID: "honest-id", Offers: []protocol.ModelOffer{{Model: "m", PriceOut: 0.1}}}
	b.lastSeen["honest-id"] = now
	b.mu.Lock()
	node, _, ok2 := b.pick("m", false, 0, 0, 0, "", nil, nil, nil)
	b.mu.Unlock()
	if !ok2 || node.NodeID != "honest-id" {
		t.Errorf("an un-banned owner's node must remain pickable, got node=%q ok=%v", node.NodeID, ok2)
	}
}

// TestProducedUsableOutputVoidGate: the void predicate is false (-> $0 charge) for an
// errored response, an empty/whitespace completion, and a claim-without-text; true only
// for a real completion.
func TestProducedUsableOutputVoidGate(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		text      string
		claimComp int
		want      bool
	}{
		{"good output", 200, "hello", 5, true},
		{"errored 500", 500, "hello", 5, false},
		{"errored 400", 400, "hello", 5, false},
		{"empty completion", 200, "", 0, false},
		{"whitespace completion", 200, "   \n\t ", 7, false},
		{"claimed-without-text", 200, "", 42, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := producedUsableOutput(c.status, c.text, c.claimComp); got != c.want {
				t.Errorf("producedUsableOutput(%d,%q,%d) = %v, want %v", c.status, c.text, c.claimComp, got, c.want)
			}
		})
	}
}

// TestRelayVoidsNoOutput: a full relay round-trip where the node returns NO usable
// output (here: an empty completion while claiming output tokens) must charge $0, mint
// NO earning lot, and refund the consumer's pre-auth hold in FULL (balance unchanged).
func TestRelayVoidsNoOutput(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"empty completion", 200, `{"choices":[{"message":{"content":""}}]}`},
		{"errored upstream", 502, `{"error":"upstream boom"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := store.NewMem()
			b := relayBroker(db)

			nodePub, nodePriv, _ := ed25519.GenerateKey(nil)
			b.nodes["paid"] = protocol.NodeRegistration{
				NodeID: "paid", PubKey: hex.EncodeToString(nodePub),
				Offers: []protocol.ModelOffer{{Model: "m", PriceOut: 1000, PriceIn: 1000, Ctx: 256}},
			}
			b.lastSeen["paid"] = time.Now()
			tun := &nodeTunnel{jobs: make(chan protocol.Job, 1), waiters: map[string]chan protocol.JobResult{}}
			b.tunnels["paid"] = tun
			_ = db.BindNode("paid", "owner1")

			// Logged-in consumer with a funded wallet.
			_, userPriv, _ := ed25519.GenerateKey(nil)
			userPubHex := hex.EncodeToString(userPriv.Public().(ed25519.PublicKey))
			_ = db.BindOwner(store.Owner{GitHubID: 7, Login: "consumer", Pubkey: userPubHex})
			startBal, _ := db.AddCredits("u_gh_7", 500)

			// A node goroutine answers the job with the (no-output) result + a signed receipt.
			go func() {
				job := <-tun.jobs
				rec := protocol.UsageReceipt{
					RequestID: job.ID, NodeID: "paid", Model: "m",
					PromptTokens: 10, CompletionTokens: 5, // claims output, but body has none
					PriceIn: 1000, PriceOut: 1000, TS: time.Now().Unix(),
				}
				rec.SignNode(nodePriv)
				res := protocol.JobResult{ID: job.ID, Status: c.status, Body: []byte(c.body), Receipt: rec}
				tun.mu.Lock()
				ch := tun.waiters[job.ID]
				tun.mu.Unlock()
				ch <- res
			}()

			body := []byte(`{"model":"m","max_tokens":8}`)
			r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))
			signReq(r, userPriv, body)
			w := httptest.NewRecorder()
			b.relay(w, r)

			// $0 charged: balance unchanged (hold placed then fully refunded).
			endBal, _ := db.PeekBalance("u_gh_7")
			if endBal != startBal {
				t.Errorf("void must refund the hold in full: balance %v -> %v (want unchanged)", startBal, endBal)
			}
			if sp, _ := db.SpendOf("u_gh_7"); sp != 0 {
				t.Errorf("void must charge $0, got spend %v", sp)
			}
			// No earning lot minted for the owner.
			if earn, _ := db.EarningsOf("paid"); earn != 0 {
				t.Errorf("void must mint NO earning, got %v", earn)
			}
			split, _ := db.EarningSplitOf("owner1", time.Now())
			if split.Held != 0 || split.Payable != 0 {
				t.Errorf("void must create no earning lot, got held=%v payable=%v", split.Held, split.Payable)
			}
		})
	}
}

// TestRelaySymmetricByteFloorCapsInput: a node that claims an IMPOSSIBLE prompt count
// (more tokens than the request body has bytes) is billed on the clamped byte count, not
// its claim, on a full relay round-trip. The consumer is charged the floored input cost,
// a KindAdjust audit row is written, and the owner is struck (zero-doubt) + banned.
func TestRelaySymmetricByteFloorCapsInput(t *testing.T) {
	db := store.NewMem()
	b := relayBroker(db)

	nodePub, nodePriv, _ := ed25519.GenerateKey(nil)
	b.nodes["paid"] = protocol.NodeRegistration{
		NodeID: "paid", PubKey: hex.EncodeToString(nodePub),
		Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 1.0, PriceOut: 1.0, Ctx: 4096}},
	}
	b.lastSeen["paid"] = time.Now()
	tun := &nodeTunnel{jobs: make(chan protocol.Job, 1), waiters: map[string]chan protocol.JobResult{}}
	b.tunnels["paid"] = tun
	_ = db.BindNode("paid", "owner1")

	_, userPriv, _ := ed25519.GenerateKey(nil)
	userPubHex := hex.EncodeToString(userPriv.Public().(ed25519.PublicKey))
	_ = db.BindOwner(store.Owner{GitHubID: 7, Login: "consumer", Pubkey: userPubHex})
	_, _ = db.AddCredits("u_gh_7", 1e9)

	body := []byte(`{"model":"m","max_tokens":4}`)
	bodyLen := len(body)

	go func() {
		job := <-tun.jobs
		rec := protocol.UsageReceipt{
			RequestID: job.ID, NodeID: "paid", Model: "m",
			PromptTokens: 1_000_000, CompletionTokens: 1, // claims a MASSIVE impossible input
			PriceIn: 1.0, PriceOut: 1.0, TS: time.Now().Unix(),
		}
		rec.SignNode(nodePriv)
		res := protocol.JobResult{ID: job.ID, Status: 200,
			Body: []byte(`{"choices":[{"message":{"content":"ok"}}]}`), Receipt: rec}
		tun.mu.Lock()
		ch := tun.waiters[job.ID]
		tun.mu.Unlock()
		ch <- res
	}()

	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))
	signReq(r, userPriv, body)
	w := httptest.NewRecorder()
	b.relay(w, r)

	// The recorded entry must carry the CLAMPED input count (<= body bytes), never the
	// 1,000,000 claim.
	ents, _ := db.RecentByUser("u_gh_7", 10)
	if len(ents) != 1 {
		t.Fatalf("want 1 settled entry, got %d", len(ents))
	}
	if ents[0].PromptTokens > bodyLen {
		t.Errorf("billed prompt tokens = %d, want clamped to <= body bytes (%d)", ents[0].PromptTokens, bodyLen)
	}
	// The owner is struck (zero-doubt) and banned.
	if strikes := mustStrikes(t, db, "owner1"); len(strikes) == 0 {
		t.Error("impossible input on the relay path must strike the owner")
	}
	if banned, _, _ := db.IsOwnerBanned("owner1"); !banned {
		t.Error("impossible input on the relay path must ban the owner (zero-doubt)")
	}
	// A KindAdjust audit row records the downward adjustment.
	rows, _ := db.LedgerOf("u_gh_7", []string{store.KindAdjust}, 0)
	if len(rows) == 0 {
		t.Error("a downward billing adjustment must write a KindAdjust audit row")
	}
}

// relayBroker builds a broker with every map the full relay path touches.
func relayBroker(db store.Store) *broker {
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	return &broker{
		priv:          brokerPriv,
		db:            db,
		nodes:         map[string]protocol.NodeRegistration{},
		tunnels:       map[string]*nodeTunnel{},
		lastSeen:      map[string]time.Time{},
		confidential:  map[string]bool{},
		private:       map[string]bool{},
		bandOf:        map[string]string{},
		tps:           map[string]float64{},
		inflight:      map[string]int{},
		success:       map[string]float64{},
		trust:         map[string]trustState{},
		successCount:  map[string]int{},
		concurrentTPS: map[string]float64{},
		probeSched:    map[string]*probeState{},
		streams:       map[string]*streamSink{},
		quotes:        map[string]priceQuote{},
		pubOfUser:     map[string]string{},
		banned:        map[string]bool{},
		bannedOwners:  map[string]bool{},
		strikeWarnAt:  strikeWarnAt(),
		strikeBanAt:   strikeBanAt(),
		seedFunds:     0,
		feeRate:       0.30,
		lockWin:       time.Hour,
		rl:            loadRateLimiter(),
		mod:           moderation{}, // screen() off by default in dev
	}
}

// --- register helpers (mirror the priced-node register contract) -----------

func signedPricedReg(t *testing.T, nodeID string, priv ed25519.PrivateKey, pubHex string) protocol.NodeRegistration {
	t.Helper()
	reg := protocol.NodeRegistration{
		NodeID: nodeID, PubKey: pubHex, BridgeURL: "http://x", BridgeToken: "tok",
		Offers: []protocol.ModelOffer{{Model: "m", PriceOut: 0.1}}, // priced -> owner-bound
		TS:     time.Now().Unix(),
	}
	reg.SignRegistration(priv)
	return reg
}

func postRegister(t *testing.T, b *broker, reg protocol.NodeRegistration, priv ed25519.PrivateKey, pubHex string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(reg)
	r := httptest.NewRequest(http.MethodPost, "/nodes/register", strings.NewReader(string(body)))
	// The priced-register path requires the signed identity headers + the pubkey header
	// requireOwner reads.
	pub, ts, sig := protocol.SignRequest(priv, r.Method, r.URL.Path, body)
	r.Header.Set(protocol.HeaderPubkey, pub)
	r.Header.Set(protocol.HeaderTS, strconv.FormatInt(ts, 10))
	r.Header.Set(protocol.HeaderSig, sig)
	rec := httptest.NewRecorder()
	b.register(rec, r)
	return rec
}
