package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// TestStreamHonorsNonStreamPriceLock locks the price-lock parity between the non-stream
// and STREAM relay paths. The 24h lock protects a consumer from an owner's mid-engagement
// price hike. The non-stream relay keys the lock on the SIGNED consumer identity
// (lockedPrice(user,...)); for a logged-in caller that id differs from the payer wallet
// ("u_gh_<id>"). The streaming relay previously keyed the lock on the PAYER wallet, so it
// minted a SEPARATE lock and a logged-in user's streamed request escaped the lock the
// non-stream path had already established - eating the hiked price. This drives a real
// relay round-trip on each path and asserts the stream bills the LOCKED (original) price.
func TestStreamHonorsNonStreamPriceLock(t *testing.T) {
	db := store.NewMem()
	b := relayBroker(db)

	nodePub, nodePriv, _ := ed25519.GenerateKey(nil)
	b.nodes["paid"] = protocol.NodeRegistration{
		NodeID: "paid", PubKey: hex.EncodeToString(nodePub),
		// Public market (not a grant / not self) -> fixed=false -> the relay applies the
		// price-lock window. PriceIn 0 so cost depends only on the (locked vs current) out
		// price, making the assertion unambiguous.
		Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 0, PriceOut: 1.0, Ctx: 4096}},
	}
	b.lastSeen["paid"] = time.Now()
	tun := &nodeTunnel{jobs: make(chan protocol.Job, 1), waiters: map[string]chan protocol.JobResult{}}
	b.tunnels["paid"] = tun
	_ = db.BindNode("paid", "owner1")

	// A logged-in consumer: the signed pubkey-derived id differs from the payer wallet
	// (u_gh_9), which is exactly the case the bug depended on.
	_, userPriv, _ := ed25519.GenerateKey(nil)
	userPubHex := hex.EncodeToString(userPriv.Public().(ed25519.PublicKey))
	_ = db.BindOwner(store.Owner{GitHubID: 9, Login: "consumer", Pubkey: userPubHex})
	_, _ = db.AddCredits("u_gh_9", 1e9)

	// One node responder for the whole test: answer every dispatched job with a signed
	// receipt for 1000 completion tokens (price is set by the broker, not the node).
	go func() {
		for job := range tun.jobs {
			rec := protocol.UsageReceipt{
				RequestID: job.ID, NodeID: "paid", Model: "m",
				PromptTokens: 0, CompletionTokens: 1000, TS: time.Now().Unix(),
			}
			rec.SignNode(nodePriv)
			res := protocol.JobResult{
				ID: job.ID, Status: 200,
				Body:    []byte(`{"choices":[{"message":{"content":"ok"}}]}`),
				Receipt: rec,
			}
			tun.mu.Lock()
			ch := tun.waiters[job.ID]
			tun.mu.Unlock()
			if ch != nil {
				ch <- res
			}
		}
	}()

	doRelay := func(stream bool) {
		body := []byte(`{"model":"m","max_tokens":1000}`)
		if stream {
			body = []byte(`{"model":"m","max_tokens":1000,"stream":true}`)
		}
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))
		signReq(r, userPriv, body)
		w := httptest.NewRecorder()
		b.relay(w, r)
	}

	// 1) NON-STREAM request at the base price (out=1.0): mints the lock under the signed
	//    consumer id. cost = 1000 * 1.0 / 1e6 = 0.001.
	doRelay(false)

	// 2) Owner HIKES the out price 10x.
	reg := b.nodes["paid"]
	reg.Offers[0].PriceOut = 10.0
	b.nodes["paid"] = reg

	// 3) STREAM request: must be billed at the LOCKED 1.0, not the hiked 10.0.
	doRelay(true)

	ents, _ := db.RecentByUser("u_gh_9", 10)
	if len(ents) != 2 {
		t.Fatalf("want 2 settled entries (non-stream + stream), got %d", len(ents))
	}
	// Both settle in the same second, so entry order is ambiguous; assert order-free that
	// NEITHER request was billed above the locked price. With the bug the streamed request
	// is billed at the hiked 10x (its lock keyed on the payer wallet, missing the lock the
	// non-stream path minted under the signed consumer id).
	const locked = 1000 * 1.0 / 1e6 // the locked-price cost
	const hiked = 1000 * 10.0 / 1e6 // what the un-locked stream would have billed
	maxCost := 0.0
	for _, e := range ents {
		if e.Cost > maxCost {
			maxCost = e.Cost
		}
	}
	if !approxEq(maxCost, locked) {
		t.Errorf("a request was billed %.6f, want all at the LOCKED %.6f (the stream path must "+
			"honor the price lock the non-stream path minted, not the hiked %.6f)", maxCost, locked, hiked)
	}
}

func approxEq(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 1e-9
}
