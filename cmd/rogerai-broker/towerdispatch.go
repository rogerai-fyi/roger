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
// # SINGLE INSTANCE
//
// The queue is in-process, so a Tower must poll the instance that issued its work. Stated as
// a limit rather than left to be discovered: multi-instance needs this behind the shared
// store, exactly as node dispatch already is.

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

// towerAttemptLifetime bounds one attempt. It is what stops a Station holding work whose
// caller gave up long ago, and it is deliberately shorter than the relay's own patience.
const towerAttemptLifetime = 60 * time.Second

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

// dispatchQueue holds pending work per Tower and pending answers per attempt.
type dispatchQueue struct {
	mu      sync.Mutex
	pending map[string][]towerWork
	// wake is closed to nudge a waiting poller. A channel per Tower rather than a shared
	// condition variable, so one busy Tower never wakes every other poller in the process.
	wake    map[string]chan struct{}
	results map[string]chan towerResult
}

func newDispatchQueue() *dispatchQueue {
	return &dispatchQueue{
		pending: map[string][]towerWork{},
		wake:    map[string]chan struct{}{},
		results: map[string]chan towerResult{},
	}
}

// offer queues work for a Tower and returns the channel its answer will arrive on.
//
// The result channel is registered BEFORE the work is visible to a poller. A Tower that is
// fast enough to answer between the two would otherwise deliver into nothing, and the caller
// would wait out the whole deadline for a result that already came back.
func (q *dispatchQueue) offer(w towerWork) <-chan towerResult {
	res := make(chan towerResult, 1)
	q.mu.Lock()
	defer q.mu.Unlock()
	q.results[w.AttemptID] = res
	q.pending[w.towerID] = append(q.pending[w.towerID], w)
	if ch, ok := q.wake[w.towerID]; ok {
		close(ch)
		delete(q.wake, w.towerID)
	}
	return res
}

// take removes the next piece of work for a Tower, if any.
func (q *dispatchQueue) take(towerID string) (towerWork, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	queued := q.pending[towerID]
	if len(queued) == 0 {
		return towerWork{}, false
	}
	next := queued[0]
	if len(queued) == 1 {
		delete(q.pending, towerID)
	} else {
		q.pending[towerID] = queued[1:]
	}
	return next, true
}

// waitFor blocks until this Tower has work or the context ends.
func (q *dispatchQueue) waitFor(ctx context.Context, towerID string) (towerWork, bool) {
	for {
		if w, ok := q.take(towerID); ok {
			return w, true
		}
		q.mu.Lock()
		ch, ok := q.wake[towerID]
		if !ok {
			ch = make(chan struct{})
			q.wake[towerID] = ch
		}
		q.mu.Unlock()

		select {
		case <-ch:
			// Work arrived (or another poller was woken); loop and try to take it.
		case <-ctx.Done():
			return towerWork{}, false
		}
	}
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

	work := towerWork{
		AttemptID: grant.AttemptID, Grant: grant.Signed, Request: body,
		towerID: target.TowerID,
	}
	res := ts.queue.offer(work)
	defer ts.queue.abandon(grant.AttemptID)

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
	case <-time.After(towerAttemptLifetime):
		jsonErr(w, http.StatusGatewayTimeout, "the Station did not answer in time")
		return true
	case <-r.Context().Done():
		return true
	}
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
		work, got := ts.queue.waitFor(ctx, tw.ID)
		if !got {
			// Nothing to do. 204 rather than an empty 200 so a Tower can tell "no work" from
			// "a job with an empty body" without inspecting anything.
			wr.WriteHeader(http.StatusNoContent)
			return
		}
		// HANDING WORK OUT IS THE CLAIM, and it happens HERE rather than when the result
		// arrives. That ordering is the one-use guarantee: two polls racing for the same
		// attempt both take it off a queue, and only the one that wins the claim is given it.
		// Doing it at result time instead would mean both had already executed, which is
		// precisely what "at most one attempt reaches executing state" forbids.
		if _, err := ts.dispatch.Claim(work.AttemptID, tw.ID); err != nil {
			// Expired, or already claimed by a racing poll. Neither is this poller's problem:
			// look for the next piece of work rather than reporting a failure for somebody
			// else's attempt.
			log.Printf("tower %s: skipping attempt %s: %v", tw.ID, work.AttemptID, err)
			continue
		}
		writeJSON(wr, http.StatusOK, work)
		return
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
	if !ts.queue.deliver(req.AttemptID, towerResult{body: req.Body}) {
		// Verified, but nobody is waiting: the caller gave up. Accepted rather than an error,
		// because the Station did its job and re-sending would not help.
		writeJSON(wr, http.StatusOK, map[string]any{"ok": true, "delivered": false})
		return
	}
	writeJSON(wr, http.StatusOK, map[string]any{"ok": true, "delivered": true})
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
