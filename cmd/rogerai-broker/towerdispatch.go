package main

// towerdispatch.go routes a request to a Station behind a Tower, and brings the answer back.
//
// # WHERE IT SITS
//
// Strictly as a FALLBACK, at the one point in the relay where the request would otherwise be
// answered "no node offers this model". That placement is the whole safety argument: a
// request a direct node can serve is completely untouched by any of this, and the money path
// - pricing, wallets, holds, settlement - is never entered, because the Tower path returns
// before it.
//
// # IT IS FREE
//
// Tower-backed work is UNCOMPENSATED in this version. Nothing is charged and nothing is
// earned, and that is the plan's own order rather than a shortcut: canary free traffic
// before ordinary paid workloads, and the compensated tier only once real-fund allocation,
// reversal, payout idempotency and ledger replay are proven. The funding reservation and
// attempt-ledger objects the full grant contract binds to do not exist, so a grant here
// carries no price and authorizes no payment. See internal/towercore/dispatch.
//
// # NOT THE TRUSTED BUS
//
// Direct nodes are dispatched to over the replica bus. Towers deliberately are not: the plan
// requires origin-aware dispatch WITHOUT sharing it, because that bus is a trusted-fleet
// channel and a Tower is an untrusted relay. This queue is its own thing, and a Tower can
// only ever see work addressed to its own Tower ID.
//
// # IT WORKS ACROSS BROKERS
//
// Production runs more than one. Two things follow, and both used to be wrong:
//
//   - THE ATTEMPT STORE IS THE QUEUE. A Tower reaches whichever instance the load balancer
//     chose, which is very often not the one that created its work, so pending work lives in
//     the durable store and a poll is a conditional UPDATE that claims one row. Single
//     delivery is not something this file arranges - it falls out of the compare-and-swap.
//   - THE RESULT CROSSES BACK over the broker's own pub/sub, because the caller is waiting
//     on the instance that issued and the answer arrives at whichever one the Tower reached.
//
// That pub/sub is Core-internal and the Tower never touches it - a Tower speaks HTTP and
// nothing else, which is what the plan means by dispatching without sharing the trusted
// replica bus. The channel namespace is distinct from node dispatch so the two cannot meet
// even by accident.

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"golang.org/x/crypto/hkdf"

	"rogerai.fm/roger/v5/internal/towercore/cert"
	"rogerai.fm/roger/v5/internal/towercore/dispatch"
	"rogerai.fm/roger/v5/internal/towercore/link"
)

// dispatchPollWait is how long a Tower's poll waits before answering "nothing yet". Short
// enough that a proxy never times out on it, long enough that a Tower is not re-polling
// constantly for an idle fleet.
//
// A var rather than a const so a test can shorten it. Production never assigns it; the
// alternative is a suite that spends half a minute per "there was nothing to collect"
// assertion, and a slow test is a test people stop running.
var dispatchPollWait = 25 * time.Second

// dispatchPollTick is how often a waiting poll re-asks the store. It is the worst-case delay
// on work created by ANOTHER instance; work created here wakes the poll immediately, so this
// is a ceiling on the cross-instance case rather than a latency everything pays.
const dispatchPollTick = 250 * time.Millisecond

// towerAttemptLifetime bounds one attempt. It is what stops a Station holding work whose
// caller gave up long ago, and it is deliberately shorter than the relay's own patience.
//
// A var only so a test can shorten it; production never assigns it. Asserting the timeout
// with the real value would mean a test that takes a minute to prove one branch.
var towerAttemptLifetime = 60 * time.Second

// dispatchKeyLabel domain-separates Core's grant key from every other use of the CA root.
const dispatchKeyLabel = "rogerai tower dispatch grant signer v1"

// deriveDispatchKey produces Core's grant-signing key from the Tower CA root.
//
// DERIVED rather than stored, and derived rather than reused. Stored would mean another
// secret with its own custody ladder to get wrong; reusing the root directly would mean the
// key that mints certificates also signs authorizations, so a mistake in either changes what
// the other means. HKDF with a fixed label gives a stable key across restarts - which
// matters, because a Station pins this public key - while keeping the two uses separate.
func deriveDispatchKey(ca *cert.Authority) (ed25519.PrivateKey, error) {
	// The CA root is ECDSA P-256 (that is what certificates are signed with), and grants are
	// Ed25519 like every other object in this protocol. So the ROOT'S SECRET SCALAR is the
	// HKDF input and the output is an Ed25519 seed - the two key types never meet, which is
	// the point: one key signs certificates, a different one signs authorizations, and
	// neither can be used as the other.
	root, ok := ca.RootKey().(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("the Tower CA root is not an ECDSA key, so no dispatch key can be derived from it")
	}
	// Fixed-width, not D.Bytes(): a big.Int drops leading zeroes, so one root in 256 would
	// derive a different key depending on how its scalar happened to be encoded. That is a
	// once-in-a-blue-moon bug that would look like a Station being unable to verify anything.
	scalar := make([]byte, 32)
	root.D.FillBytes(scalar)

	seed := make([]byte, ed25519.SeedSize)
	if _, err := io.ReadFull(hkdf.New(sha256.New, scalar, nil, []byte(dispatchKeyLabel)), seed); err != nil {
		return nil, err
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// towerWork is one attempt waiting for its Tower to collect it.
type towerWork struct {
	AttemptID string          `json:"attempt_id"`
	Grant     json.RawMessage `json:"grant"`
	// Request is the exact body the grant's digest commits to. The Tower relays it
	// unchanged; changing one byte makes the Station's own check fail.
	Request json.RawMessage `json:"request"`

	// towerID is the queue key, and is deliberately NOT serialized. The Tower does not need
	// to be told its own ID, and keeping it off the wire means the key cannot be influenced
	// by anything a Tower sends.
	towerID string
}

// towerResult is what came back.
type towerResult struct {
	body []byte
	err  error
}

// dispatchQueue is what remains in process: who is WAITING for an answer, and a nudge so a
// poller on this instance does not sit out its tick when work was created right here.
//
// The pending work itself is in the durable store - see the package comment. What cannot go
// there is a Go channel belonging to an in-flight HTTP handler, which is precisely what is
// left here.
type dispatchQueue struct {
	mu sync.Mutex
	// wake is closed to nudge this instance's poller for a Tower. A channel per Tower rather
	// than one shared signal, so a busy Tower never wakes every other poller in the process.
	wake    map[string]chan struct{}
	results map[string]chan towerResult
}

func newDispatchQueue() *dispatchQueue {
	return &dispatchQueue{
		wake:    map[string]chan struct{}{},
		results: map[string]chan towerResult{},
	}
}

// await registers this caller as the one waiting for an attempt's answer.
//
// Registered BEFORE the work becomes visible to any poller. A Station fast enough to answer
// in between would otherwise deliver into nothing, and the caller would wait out the whole
// deadline for a result that already came back.
func (q *dispatchQueue) await(attemptID string) <-chan towerResult {
	res := make(chan towerResult, 1)
	q.mu.Lock()
	defer q.mu.Unlock()
	q.results[attemptID] = res
	return res
}

// nudge wakes this instance's poller for a Tower, so work created HERE is picked up at once
// rather than on the next tick. Purely an accelerator: the tick would find it anyway, which
// is what makes the cross-instance case correct without any signalling at all.
func (q *dispatchQueue) nudge(towerID string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if ch, ok := q.wake[towerID]; ok {
		close(ch)
		delete(q.wake, towerID)
	}
}

// waiter returns the channel to sleep on until this Tower might have work.
func (q *dispatchQueue) waiter(towerID string) <-chan struct{} {
	q.mu.Lock()
	defer q.mu.Unlock()
	ch, ok := q.wake[towerID]
	if !ok {
		ch = make(chan struct{})
		q.wake[towerID] = ch
	}
	return ch
}

// deliver hands an answer to whoever is waiting for this attempt.
func (q *dispatchQueue) deliver(attemptID string, res towerResult) bool {
	q.mu.Lock()
	ch, ok := q.results[attemptID]
	delete(q.results, attemptID)
	q.mu.Unlock()
	if !ok {
		return false
	}
	ch <- res
	return true
}

// abandon drops a waiter that gave up, so a late answer is not delivered into a channel
// nobody will ever read.
func (q *dispatchQueue) abandon(attemptID string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.results, attemptID)
}

// tryTowerDispatch answers the request from a Tower-backed Station, or reports that it could
// not and leaves the caller to send its own refusal.
//
// It is called ONLY where the relay would otherwise answer "no node offers this model", so
// nothing it does can change how a request that a direct node could serve is handled.
func (b *broker) tryTowerDispatch(w http.ResponseWriter, r *http.Request, model string, body []byte, streaming bool) bool {
	ts := b.tower
	if ts == nil || ts.dispatch == nil || ts.queue == nil {
		return false
	}
	if streaming {
		// A streamed answer through a relay-of-a-relay needs the inner session, which is not
		// built. Answering a stream request with a non-streamed body would break the client's
		// contract, so this simply does not apply and the caller's 503 stands.
		return false
	}

	target, ok := b.pickTowerStation(model)
	if !ok {
		return false
	}
	grant, err := ts.dispatch.Issue(target, body)
	if err != nil {
		return false
	}

	// Waiting FIRST, issuing second: a Station fast enough to answer in between would
	// otherwise deliver into nothing.
	res := ts.queue.await(grant.AttemptID)
	defer ts.queue.abandon(grant.AttemptID)

	// And subscribe to the cross-instance channel too, because the Tower will reach whichever
	// broker the load balancer picked and that is very often not this one.
	remote, stopRemote := b.subscribeTowerResult(r.Context(), grant.AttemptID)
	defer stopRemote()

	// Issue already recorded it as pending in the store, which IS the queue - so it is
	// visible to every instance from this point. The nudge only makes THIS instance's poller
	// notice without waiting for its tick.
	ts.queue.nudge(target.TowerID)

	select {
	case got := <-res:
		if got.err != nil {
			jsonErr(w, http.StatusBadGateway, "the Station could not complete this request")
			return true
		}
		// FREE, and said so in a header rather than only in a doc: a caller has no other way
		// to tell this apart from a normally-billed answer, and "you were not charged" is
		// exactly the kind of thing that must not be implicit.
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-RogerAI-Origin", "tower")
		w.Header().Set("X-RogerAI-Cost", "0")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(got.body)
		return true
	case raw := <-remote:
		// The same answer, arriving from the instance the Tower actually reached. It has
		// already been VERIFIED and settled there - a broker only ever publishes a result it
		// accepted - so this side relays bytes rather than re-deciding.
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-RogerAI-Origin", "tower")
		w.Header().Set("X-RogerAI-Cost", "0")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(raw)
		return true
	case <-time.After(towerAttemptLifetime):
		jsonErr(w, http.StatusGatewayTimeout, "the Station did not answer in time")
		return true
	case <-r.Context().Done():
		return true
	}
}

// towerResultChannel namespaces Tower answers away from node dispatch. A Tower attempt id
// and a node job id could not collide today, and a prefix means they cannot collide tomorrow
// either - the two are different trust domains and should not share a name space by luck.
func towerResultChannel(attemptID string) string { return "twatt:" + attemptID }

// subscribeTowerResult listens for an answer settled on ANOTHER instance.
//
// Returns a nil channel on a single-instance deployment, which is not a failure: a nil
// channel simply never fires in a select, and there is no other instance for an answer to
// arrive from.
func (b *broker) subscribeTowerResult(ctx context.Context, attemptID string) (<-chan []byte, func()) {
	if !b.multiInstance || b.shared == nil {
		return nil, func() {}
	}
	ch, cancel, err := b.shared.busSubscribeResult(ctx, towerResultChannel(attemptID))
	if err != nil {
		// The local channel still works, so a bus that is unavailable costs cross-instance
		// delivery rather than the request. Worth neither failing nor pretending: the caller
		// will time out if the Tower reached a different broker.
		log.Printf("tower dispatch: cannot subscribe for %s: %v", attemptID, err)
		return nil, func() {}
	}
	return ch, cancel
}

// pickTowerStation chooses a routable Station for a model.
//
// Deliberately simple - first eligible match - because this path only runs when there is no
// direct node at all, so the choice is between "some Station" and "an error". A Tower fleet
// large enough for the scoring the direct router does is a problem to have later, and
// pretending to solve it now would mean two routers to keep honest.
func (b *broker) pickTowerStation(model string) (dispatch.Target, bool) {
	ts := b.tower
	// Only Towers holding a LIVE LINK are considered. An inventory can outlive the session
	// that pushed it by design, but there is no way to hand work to a Tower that is not
	// connected, so a disconnected one is not a candidate however fresh its offers look.
	for _, towerID := range ts.link.LiveTowers() {
		// ELIGIBILITY IS SEPARATE FROM PRESENCE. A quarantined Tower connects, pushes and
		// heartbeats exactly like an active one - that is what quarantine IS - and must
		// still never be handed customer work.
		if !ts.registry.MayTakeWork(towerID) {
			continue
		}
		for _, leaf := range ts.inv.Routable(towerID) {
			if leaf.Model != model {
				continue
			}
			at, found, err := ts.stations.Station(leaf.StationID)
			if err != nil || !found || !at.Live() {
				// An unreadable attachment is not an eligible one: dispatching to a Station
				// whose recorded key we could not read means accepting a receipt we have no
				// way to check.
				continue
			}
			key, kerr := hex.DecodeString(at.AssertionKey)
			if kerr != nil || len(key) != ed25519.PublicKeySize {
				continue
			}
			return dispatch.Target{
				TowerID: towerID, StationID: leaf.StationID, StationEpoch: at.Epoch,
				Model: leaf.Model, Modality: leaf.Modality,
				// The key from the ATTACHMENT record - never from the offer, and never from
				// anything the request carried.
				AssertionKey: ed25519.PublicKey(key),
			}, true
		}
	}
	return dispatch.Target{}, false
}

// towerDispatchPoll handles POST /tower/dispatch: a Tower collecting work for its Stations.
func (b *broker) towerDispatchPoll(wr http.ResponseWriter, r *http.Request) {
	if !allow(wr, r, http.MethodPost) {
		return
	}
	ts := b.towerAvailable(wr)
	if ts == nil {
		return
	}
	body := readTowerBody(r)
	var req struct {
		TowerID string `json:"tower_id"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		jsonErr(wr, http.StatusBadRequest, "invalid JSON body")
		return
	}
	tw, _, ok := b.towerCaller(r, body, req.TowerID)
	if !ok {
		jsonErr(wr, http.StatusForbidden, "collecting work requires a registered Tower's own signed request")
		return
	}
	// ELIGIBILITY, not just identity. A quarantined Tower holds a valid key and a live link
	// and must still never be handed customer work - that separation is the entire point of
	// quarantine, and towerCaller deliberately permits it so the link can exist at all.
	if !ts.registry.MayTakeWork(tw.ID) {
		jsonErr(wr, http.StatusForbidden, "this Tower is not eligible to take work yet")
		return
	}
	if ts.queue == nil {
		jsonErr(wr, http.StatusServiceUnavailable, "dispatch is not available")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), dispatchPollWait)
	defer cancel()
	for {
		// HANDING WORK OUT IS THE CLAIM, in one conditional UPDATE. That is the one-use
		// guarantee and it is why this asks the STORE rather than a list in this process:
		// two brokers polled at the same moment both see the attempt, and exactly one swap
		// wins it. Claiming later - when the result arrives - would mean both had already
		// executed, which is what "at most one attempt reaches executing state" forbids.
		grant, request, got, err := ts.dispatch.ClaimNext(tw.ID)
		if err != nil {
			// An unreadable store is not an empty one. Saying "no work" here would be a
			// confident wrong answer, and the Tower would poll a broken broker forever.
			jsonErr(wr, http.StatusServiceUnavailable, "could not read pending work - try again")
			return
		}
		if got {
			writeJSON(wr, http.StatusOK, towerWork{
				AttemptID: grant.AttemptID, Grant: grant.Signed, Request: request,
			})
			return
		}

		// Nothing waiting. Sleep until this instance creates some (the nudge) or until the
		// next tick, which is what finds work created on ANOTHER instance.
		select {
		case <-ts.queue.waiter(tw.ID):
		case <-time.After(dispatchPollTick):
		case <-ctx.Done():
			// 204 rather than an empty 200, so a Tower can tell "no work" from "a job with an
			// empty body" without inspecting anything.
			wr.WriteHeader(http.StatusNoContent)
			return
		}
	}
}

// towerDispatchResult handles POST /tower/dispatch/result: the answer coming back.
func (b *broker) towerDispatchResult(wr http.ResponseWriter, r *http.Request) {
	if !allow(wr, r, http.MethodPost) {
		return
	}
	ts := b.towerAvailable(wr)
	if ts == nil {
		return
	}
	body := readTowerBody(r)
	var req struct {
		TowerID   string           `json:"tower_id"`
		AttemptID string           `json:"attempt_id"`
		Receipt   dispatch.Receipt `json:"receipt"`
		Body      json.RawMessage  `json:"body"`
		Failure   string           `json:"failure"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		jsonErr(wr, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if _, _, ok := b.towerCaller(r, body, req.TowerID); !ok {
		jsonErr(wr, http.StatusForbidden, "returning a result requires a registered Tower's own signed request")
		return
	}
	if ts.queue == nil || ts.dispatch == nil {
		jsonErr(wr, http.StatusServiceUnavailable, "dispatch is not available")
		return
	}

	// A Station that could not serve says so, and the attempt ends without a receipt. This
	// is NOT a way to settle: nothing is accepted as a result, the caller is told the
	// Station failed, and the attempt is closed so it cannot also be answered.
	if req.Failure != "" {
		ts.queue.deliver(req.AttemptID, towerResult{err: errors.New(req.Failure)})
		writeJSON(wr, http.StatusOK, map[string]any{"ok": true, "recorded": "failure"})
		return
	}

	if _, err := ts.dispatch.Complete(req.AttemptID, req.Receipt, req.Body); err != nil {
		// Every one of these is a refusal to accept the answer, and the caller gets nothing.
		// 409 for a state problem, 400 for a binding one - an operator debugging a Tower
		// needs to know whether to look at timing or at bytes.
		switch {
		case errors.Is(err, dispatch.ErrNotFound), errors.Is(err, dispatch.ErrExpired),
			errors.Is(err, dispatch.ErrAlreadySettled), errors.Is(err, dispatch.ErrNotClaimed):
			jsonErr(wr, http.StatusConflict, err.Error())
		default:
			jsonErr(wr, http.StatusBadRequest, err.Error())
		}
		return
	}
	if ts.queue.deliver(req.AttemptID, towerResult{body: req.Body}) {
		writeJSON(wr, http.StatusOK, map[string]any{"ok": true, "delivered": true})
		return
	}
	// Nobody is waiting HERE. On a fleet that is the ordinary case rather than an error - the
	// caller is parked on the instance that issued the grant, and this is whichever one the
	// Tower happened to reach. Publishing hands it across.
	if b.publishTowerResult(req.AttemptID, req.Body) {
		writeJSON(wr, http.StatusOK, map[string]any{"ok": true, "delivered": true})
		return
	}
	// Verified, and nobody anywhere is waiting: the caller gave up. Accepted rather than
	// refused, because the Station did its job and re-sending would not help.
	writeJSON(wr, http.StatusOK, map[string]any{"ok": true, "delivered": false})
}

// publishTowerResult hands a settled answer to whichever instance is waiting for it.
func (b *broker) publishTowerResult(attemptID string, body []byte) bool {
	if !b.multiInstance || b.shared == nil {
		return false
	}
	if err := b.shared.busPublishResult(towerResultChannel(attemptID), body); err != nil {
		log.Printf("tower dispatch: cannot publish the result for %s: %v", attemptID, err)
		return false
	}
	return true
}

// towerDispatchKey handles GET /tower/dispatch/key: Core's grant-signing public key.
//
// PUBLIC and read-only. A Station has to verify that a grant came from Core before it
// executes anything, and it has no other channel to Core - it only ever talks to its Tower,
// which is precisely the party a forged grant would come from. Publishing the key here lets
// an operator pin it into the Station out of band.
func (b *broker) towerDispatchKey(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodGet) {
		return
	}
	cors(w)
	ts := b.towerAvailable(w)
	if ts == nil {
		return
	}
	if len(ts.dispatchPub) == 0 {
		jsonErr(w, http.StatusServiceUnavailable, "dispatch is not available")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"network":      link.PublicNetwork,
		"dispatch_key": fmt.Sprintf("%x", ts.dispatchPub),
		"note": "pin this into a Station with `roger-station trust --core-key`; " +
			"it is what proves a grant came from Roger Core rather than from the relay",
	})
}
