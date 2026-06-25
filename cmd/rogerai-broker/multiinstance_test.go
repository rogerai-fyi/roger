package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// PRE-SCALE Stage 2 tests: the cross-instance job/result/stream RENDEZVOUS bus.
//
// These simulate TWO broker instances (A and B) sharing ONE Valkey (miniredis): a
// job dispatched on instance A is served by a provider long-polling ONLY instance B,
// the result returns to A over the bus, an SSE stream flows A<-bus<-B in order,
// inflight merges across instances, the price-lock holds cross-instance, and a bus
// error fails the request cleanly with no double-charge.

// newMIBroker builds a fully-wired broker for the multi-instance relay path, pointed at
// the shared miniredis vs. nil (flag-off). Both instances in a test share the SAME
// brokerPriv so a node-signed receipt verifies on either, and the SAME db so the
// pre-dispatch Hold + Settle are on the durable money truth (as in production where
// Postgres is shared). When mr != nil it is multi-instance (bus ON); when mr == nil the
// broker is the in-memory single-instance fast-path (shared==nil, multiInstance==false).
func newMIBroker(t *testing.T, brokerPriv ed25519.PrivateKey, db store.Store, mr *miniredis.Miniredis) *broker {
	t.Helper()
	b := &broker{
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
		seedFunds:     100,
		lockWin:       time.Hour,
		rl:            loadRateLimiter(),
	}
	if mr != nil {
		vs, err := newValkeyStore("redis://" + mr.Addr())
		if err != nil {
			t.Fatalf("newValkeyStore: %v", err)
		}
		t.Cleanup(func() { _ = vs.Close() })
		b.shared = vs
		b.multiInstance = true
		b.instanceID = newInstanceID()
		b.peerInflight = map[string]int{}
	}
	return b
}

// miRegisterNode wires a node into a broker instance's in-memory registry + tunnel so
// pickFor can select it and agentPoll/agentResult/agentStream authenticate it. The same
// node id + pubkey + bridge token is used on every instance in a test.
func miRegisterNode(b *broker, nodeID, pubHex, token string, offers []protocol.ModelOffer) {
	b.nodes[nodeID] = protocol.NodeRegistration{NodeID: nodeID, PubKey: pubHex, BridgeToken: token, Offers: offers}
	b.lastSeen[nodeID] = time.Now()
	b.tunnels[nodeID] = &nodeTunnel{jobs: make(chan protocol.Job, 64), waiters: map[string]chan protocol.JobResult{}, token: token}
}

// miNodeBearer is the node-auth header agentPoll/agentResult/agentStream expect.
func miNodeBearer(token string) string { return "Bearer " + token }

// miSignedRelayReq builds a SIGNED relay request for a free model so the spend gate +
// Hold are bypassed (free => no hold, no balance needed), keeping the test focused on
// the rendezvous. Returns the request + the user's pubkey-derived id.
func miSignedRelayReq(t *testing.T, userPriv ed25519.PrivateKey, body []byte, hdr map[string]string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	signReq(r, userPriv, body)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	return r
}

// miSignedResult builds a node-signed JobResult for jobID so VerifyNode passes on the
// originating instance. completion is the assistant text returned in the body.
func miSignedResult(jobID, nodeID, model, completion string, nodePriv ed25519.PrivateKey, status int) protocol.JobResult {
	body := fmt.Sprintf(`{"choices":[{"message":{"content":%q}}]}`, completion)
	rec := protocol.UsageReceipt{
		RequestID: jobID, NodeID: nodeID, Model: model,
		PromptTokens: 5, CompletionTokens: 7, TS: time.Now().Unix(),
	}
	rec.SignNode(nodePriv)
	return protocol.JobResult{ID: jobID, Status: status, Body: json.RawMessage(body), Receipt: rec}
}

// pollOnce drives instance B's agentPoll once and decodes the dispatched job. It blocks
// up to the poll window; returns ok=false on a 204 (no job).
func pollOnce(t *testing.T, b *broker, nodeID, token string) (protocol.Job, bool) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/agent/poll?node="+nodeID, nil)
	r.Header.Set("Authorization", miNodeBearer(token))
	w := httptest.NewRecorder()
	b.agentPoll(w, r)
	if w.Code == http.StatusNoContent {
		return protocol.Job{}, false
	}
	var job protocol.Job
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
		return protocol.Job{}, false
	}
	return job, true
}

// TestMultiInstanceNonStreamRendezvous: a job dispatched on instance A is served by a
// provider long-polling ONLY instance B; the result returns to A over the bus and A
// relays the completion to the waiting consumer.
func TestMultiInstanceNonStreamRendezvous(t *testing.T) {
	mr := miniredis.RunT(t)
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	db := store.NewMem()

	a := newMIBroker(t, brokerPriv, db, mr)
	bInst := newMIBroker(t, brokerPriv, db, mr)

	nodePub, nodePriv, _ := ed25519.GenerateKey(nil)
	pubHex := hex.EncodeToString(nodePub)
	const token = "tok-n1"
	offers := []protocol.ModelOffer{{Model: "free-m"}} // free => no hold
	// A can PICK the node (registry); B holds the live poll.
	miRegisterNode(a, "n1", pubHex, token, offers)
	miRegisterNode(bInst, "n1", pubHex, token, offers)

	// The provider long-polls instance B and serves the job from there.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		job, ok := pollOnce(t, bInst, "n1", token)
		if !ok {
			t.Errorf("instance B poll got no job - the cross-instance dispatch did not arrive")
			return
		}
		res := miSignedResult(job.ID, "n1", "free-m", "hello from B", nodePriv, 200)
		rb, _ := json.Marshal(res)
		rr := httptest.NewRequest(http.MethodPost, "/agent/result?node=n1", bytes.NewReader(rb))
		rr.Header.Set("Authorization", miNodeBearer(token))
		rw := httptest.NewRecorder()
		bInst.agentResult(rw, rr)
		if rw.Code != http.StatusOK {
			t.Errorf("instance B agentResult = %d, want 200", rw.Code)
		}
	}()

	// Give B's poll a moment to subscribe before A dispatches.
	time.Sleep(150 * time.Millisecond)

	_, userPriv, _ := ed25519.GenerateKey(nil)
	body := []byte(`{"model":"free-m","max_tokens":8}`)
	r := miSignedRelayReq(t, userPriv, body, nil)
	w := httptest.NewRecorder()
	a.relay(w, r)
	wg.Wait()

	if w.Code != http.StatusOK {
		t.Fatalf("relay on A = %d, want 200 (got body: %s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "hello from B") {
		t.Errorf("relay body = %q, want it to contain the completion served on instance B", w.Body.String())
	}
	if got := w.Header().Get("X-RogerAI-Provider"); got != "n1" {
		t.Errorf("X-RogerAI-Provider = %q, want n1", got)
	}
}

// TestMultiInstanceStreamRendezvous: an SSE stream's chunks flow A<-bus<-B IN ORDER. The
// provider polls instance B, streams chunks via B's /agent/stream, and posts the receipt
// via B's /agent/result; instance A relays the ordered chunks to the waiting consumer.
func TestMultiInstanceStreamRendezvous(t *testing.T) {
	mr := miniredis.RunT(t)
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	db := store.NewMem()
	a := newMIBroker(t, brokerPriv, db, mr)
	bInst := newMIBroker(t, brokerPriv, db, mr)

	nodePub, nodePriv, _ := ed25519.GenerateKey(nil)
	pubHex := hex.EncodeToString(nodePub)
	const token = "tok-n1"
	offers := []protocol.ModelOffer{{Model: "free-m"}}
	miRegisterNode(a, "n1", pubHex, token, offers)
	miRegisterNode(bInst, "n1", pubHex, token, offers)

	chunks := []string{
		"data: {\"choices\":[{\"delta\":{\"content\":\"one \"}}]}\n\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\"two \"}}]}\n\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\"three\"}}]}\n\n",
		"data: [DONE]\n\n",
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		job, ok := pollOnce(t, bInst, "n1", token)
		if !ok {
			t.Errorf("instance B poll got no stream job")
			return
		}
		// Stream the chunks through B's /agent/stream in order.
		pr, pw := io.Pipe()
		go func() {
			for _, c := range chunks {
				_, _ = pw.Write([]byte(c))
			}
			_ = pw.Close()
		}()
		sr := httptest.NewRequest(http.MethodPost, "/agent/stream?node=n1&job="+job.ID, pr)
		sr.Header.Set("Authorization", miNodeBearer(token))
		sw := httptest.NewRecorder()
		bInst.agentStream(sw, sr)
		// Then post the final receipt so A settles + returns.
		res := miSignedResult(job.ID, "n1", "free-m", "one two three", nodePriv, 200)
		rb, _ := json.Marshal(res)
		rr := httptest.NewRequest(http.MethodPost, "/agent/result?node=n1", bytes.NewReader(rb))
		rr.Header.Set("Authorization", miNodeBearer(token))
		rw := httptest.NewRecorder()
		bInst.agentResult(rw, rr)
	}()

	time.Sleep(150 * time.Millisecond)

	_, userPriv, _ := ed25519.GenerateKey(nil)
	body := []byte(`{"model":"free-m","stream":true,"max_tokens":8}`)
	r := miSignedRelayReq(t, userPriv, body, nil)
	w := httptest.NewRecorder()
	a.relay(w, r)
	wg.Wait()

	got := w.Body.String()
	// The three deltas must appear IN ORDER on the originating instance A.
	iOne := strings.Index(got, "one ")
	iTwo := strings.Index(got, "two ")
	iThree := strings.Index(got, "three")
	if iOne < 0 || iTwo < 0 || iThree < 0 {
		t.Fatalf("stream relay on A missing chunks: %q", got)
	}
	if !(iOne < iTwo && iTwo < iThree) {
		t.Errorf("stream chunks out of order on A: one@%d two@%d three@%d (body=%q)", iOne, iTwo, iThree, got)
	}
}

// TestMultiInstanceNoPollerIsBusy: with the bus wired but NO provider polling on any
// instance, the non-stream relay returns a clean 503 ("node busy") - the cross-instance
// equivalent of a full local job queue - and never hangs or double-charges.
func TestMultiInstanceNoPollerIsBusy(t *testing.T) {
	mr := miniredis.RunT(t)
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	a := newMIBroker(t, brokerPriv, store.NewMem(), mr)
	nodePub, _, _ := ed25519.GenerateKey(nil)
	miRegisterNode(a, "n1", hex.EncodeToString(nodePub), "tok", []protocol.ModelOffer{{Model: "free-m"}})

	_, userPriv, _ := ed25519.GenerateKey(nil)
	body := []byte(`{"model":"free-m","max_tokens":8}`)
	r := miSignedRelayReq(t, userPriv, body, nil)
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { a.relay(w, r); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("relay hung with no poller - should 503 immediately when delivered==0")
	}
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("no-poller relay = %d, want 503 (node busy)", w.Code)
	}
}

// TestMultiInstanceBusErrorFailsClean: a DEAD bus fails a PAID relay cleanly with no
// dispatch AND no double-charge: the pre-dispatch Hold is refunded so the wallet balance
// is unchanged after the failed request.
func TestMultiInstanceBusErrorFailsClean(t *testing.T) {
	mr := miniredis.RunT(t)
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	db := store.NewMem()
	a := newMIBroker(t, brokerPriv, db, mr)

	// A logged-in wallet with a balance so the Hold lands (the paid path), then we kill
	// the bus so the dispatch fails AFTER the hold - the deferred ReleaseHold must refund.
	_, userPriv, _ := ed25519.GenerateKey(nil)
	userPubHex := hex.EncodeToString(userPriv.Public().(ed25519.PublicKey))
	_ = db.BindOwner(store.Owner{GitHubID: 7, Login: "octocat", Pubkey: userPubHex})
	startBal, _ := db.BalanceOf("u_gh_7", 50)

	nodePub, _, _ := ed25519.GenerateKey(nil)
	miRegisterNode(a, "n1", hex.EncodeToString(nodePub), "tok",
		[]protocol.ModelOffer{{Model: "paid-m", PriceOut: 0.5}})

	mr.Close() // bus is now dead: subscribe/publish error out

	body := []byte(`{"model":"paid-m","max_tokens":8}`)
	r := miSignedRelayReq(t, userPriv, body, nil)
	w := httptest.NewRecorder()
	a.relay(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("dead-bus relay = %d, want 503 (clean fail)", w.Code)
	}
	endBal, _ := db.BalanceOf("u_gh_7", 0)
	if endBal != startBal {
		t.Errorf("balance changed across a failed relay: start=%.6f end=%.6f - the hold was NOT refunded (double-charge risk)", startBal, endBal)
	}
}

// TestMultiInstanceInflightMerges: inflight is mirrored cross-instance via the shared
// liveness-style write-through + merge so capacity-aware pick sees a peer's load.
// (Externalized inflight: write-through on enter/exit, merged on the same sync loop.)
func TestMultiInstanceInflightMerges(t *testing.T) {
	mr := miniredis.RunT(t)
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	a := newMIBroker(t, brokerPriv, store.NewMem(), mr)
	bInst := newMIBroker(t, brokerPriv, store.NewMem(), mr)

	// Instance A has 3 in-flight on n1; instance B starts at 0. After a merge, B's merged
	// PEER inflight for n1 must reflect A's load (so B's capacity-aware pick sees it).
	a.enterInflight("n1")
	a.enterInflight("n1")
	a.enterInflight("n1")

	bInst.mergeSharedInflight()
	bInst.metricsMu.Lock()
	peer := bInst.peerInflight["n1"]
	bInst.metricsMu.Unlock()
	if peer < 3 {
		t.Errorf("instance B peerInflight(n1) = %d after merge, want >= 3 (A's load is invisible cross-instance)", peer)
	}
	// B's own local count stays 0 (it served nothing) - the merge is additive in pick,
	// not a clobber of the local count.
	if got := bInst.inflightOf("n1"); got != 0 {
		t.Errorf("instance B local inflight(n1) = %d, want 0 (peer load must not clobber local)", got)
	}
	// After A drains, a fresh merge drops the peer load back toward 0.
	a.exitInflight("n1", true)
	a.exitInflight("n1", true)
	a.exitInflight("n1", true)
	bInst.mergeSharedInflight()
	bInst.metricsMu.Lock()
	peer = bInst.peerInflight["n1"]
	bInst.metricsMu.Unlock()
	if peer != 0 {
		t.Errorf("instance B peerInflight(n1) = %d after A drained, want 0", peer)
	}
}

// TestMultiInstancePriceLockHolds: a price quoted+locked on instance A is honored on
// instance B (the 24h price-lock is shared), so an owner cannot raise a user's price by
// landing them on a different instance.
func TestMultiInstancePriceLockHolds(t *testing.T) {
	mr := miniredis.RunT(t)
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	a := newMIBroker(t, brokerPriv, store.NewMem(), mr)
	bInst := newMIBroker(t, brokerPriv, store.NewMem(), mr)

	// User first sees price out=1.0 on instance A -> locked.
	inA, outA, _ := a.lockedPrice("u1", "n1", "m", 1.0, 1.0)
	if outA != 1.0 || inA != 1.0 {
		t.Fatalf("first quote on A = in %.2f/out %.2f, want 1.0/1.0", inA, outA)
	}
	// The owner RAISES the price to 5.0. On instance B the SAME user must still be billed
	// the locked 1.0, not the new 5.0.
	inB, outB, _ := bInst.lockedPrice("u1", "n1", "m", 5.0, 5.0)
	if outB != 1.0 || inB != 1.0 {
		t.Errorf("locked price on B = in %.2f/out %.2f, want the cross-instance lock 1.0/1.0", inB, outB)
	}
}

// TestFlagOffNonStreamUnchanged is the byte-for-byte guard: with the flag OFF
// (shared==nil, multiInstance==false) the non-stream relay uses the in-memory channel
// path - a local poller serves it, exactly as today, with NO bus involved.
func TestFlagOffNonStreamUnchanged(t *testing.T) {
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	db := store.NewMem()
	b := newMIBroker(t, brokerPriv, db, nil) // nil mr => flag OFF
	if b.shared != nil || b.multiInstance {
		t.Fatal("flag-off broker must have shared==nil and multiInstance==false")
	}
	nodePub, nodePriv, _ := ed25519.GenerateKey(nil)
	pubHex := hex.EncodeToString(nodePub)
	const token = "tok"
	miRegisterNode(b, "n1", pubHex, token, []protocol.ModelOffer{{Model: "free-m"}})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		job, ok := pollOnce(t, b, "n1", token)
		if !ok {
			t.Error("flag-off local poll got no job")
			return
		}
		res := miSignedResult(job.ID, "n1", "free-m", "local hello", nodePriv, 200)
		rb, _ := json.Marshal(res)
		rr := httptest.NewRequest(http.MethodPost, "/agent/result?node=n1", bytes.NewReader(rb))
		rr.Header.Set("Authorization", miNodeBearer(token))
		b.agentResult(httptest.NewRecorder(), rr)
	}()
	time.Sleep(100 * time.Millisecond)

	_, userPriv, _ := ed25519.GenerateKey(nil)
	body := []byte(`{"model":"free-m","max_tokens":8}`)
	r := miSignedRelayReq(t, userPriv, body, nil)
	w := httptest.NewRecorder()
	b.relay(w, r)
	wg.Wait()
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "local hello") {
		t.Errorf("flag-off relay = %d body=%q, want 200 + local completion", w.Code, w.Body.String())
	}
}
