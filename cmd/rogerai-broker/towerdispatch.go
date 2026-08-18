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
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"golang.org/x/crypto/hkdf"

	"rogerai.fm/roger/v5/internal/towercore/attempt"
	"rogerai.fm/roger/v5/internal/towercore/cert"
	"rogerai.fm/roger/v5/internal/towercore/dispatch"
	"rogerai.fm/roger/v5/internal/towercore/fleet"
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
	return deriveKeyFrom(ca, dispatchKeyLabel)
}

// deriveKeyFrom is the one derivation, used with a different label per purpose.
func deriveKeyFrom(ca *cert.Authority, label string) (ed25519.PrivateKey, error) {
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
	if _, err := io.ReadFull(hkdf.New(sha256.New, scalar, nil, []byte(label)), seed); err != nil {
		return nil, err
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

const towerFinalizationGrace = 2 * time.Minute

// noteAttempt records an observation against an attempt, best effort.
//
// The state change is the ledger's business and a failure to record it must not change what
// the caller is told about their request - the attempt's own deadline sweep is what
// eventually closes a chain that missed an event.
func (b *broker) noteAttempt(attemptID string, obs attempt.Observation) {
	ts := b.tower
	if ts == nil || ts.attempts == nil {
		return
	}
	if _, err := ts.attempts.Commit(attemptID, obs); err != nil {
		log.Printf("attempt %s: could not record %s: %v", attemptID, obs.Kind, err)
	}
}

func (b *broker) targetFor(towerID, stationID, model, modality string) (dispatch.Target, bool) {
	at, found, err := b.tower.stations.Station(stationID)
	if err != nil || !found || !at.Live() {
		// An unreadable attachment is not an eligible one: dispatching to a Station whose
		// recorded key we could not read means accepting a receipt we cannot check.
		return dispatch.Target{}, false
	}
	// And it must still be behind THIS Tower. A Station that has been rehomed since the
	// projection was written must not be dispatched to through its old origin.
	if at.Origin.TowerID != towerID {
		return dispatch.Target{}, false
	}
	key, kerr := hex.DecodeString(at.AssertionKey)
	if kerr != nil || len(key) != ed25519.PublicKeySize {
		return dispatch.Target{}, false
	}
	// The SECURE-SESSION key, from the same attachment record. A Station whose recorded
	// session key is unusable is not dispatchable: the alternative would be relaying its
	// content in the clear, which is the thing this is for.
	session, serr := hex.DecodeString(at.SessionKey)
	if serr != nil || len(session) != 32 {
		return dispatch.Target{}, false
	}
	return dispatch.Target{
		TowerID: towerID, StationID: stationID, StationEpoch: at.Epoch,
		Model: model, Modality: modality, AssertionKey: ed25519.PublicKey(key),
		SessionKey: session,
	}, true
}

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
		"envelope_key": fmt.Sprintf("%x", ts.envelopePub),
		"note": "pin BOTH into a Station. dispatch_key is what proves a grant came from Roger " +
			"Core rather than from the relay; envelope_key is what a result is sealed to so " +
			"the relay cannot read it on the way back.",
	})
}

// publishRoutable mirrors this Tower's accepted, routable leaves so every instance can see
// them.
//
// Best effort by design. It is a READ MODEL - the inventory, its signatures and its chain
// were decided by the instance that accepted them, and nothing reads this to make a security
// decision: a dispatch still re-checks the attachment before it issues a grant. So a failure
// here costs REACHABILITY (a Tower routable only through the broker it is connected to,
// which is what the whole system did until now) rather than correctness, and taking the push
// down over it would be trading a real outage for a partial one.
func (b *broker) publishRoutable(towerID string) {
	ts := b.tower
	if ts == nil || ts.routable == nil {
		return
	}
	// The data-plane endpoint comes from the LIVE SESSION, stamped onto every row at publish
	// time. Rows are published by the one instance holding the link - the only instance that
	// knows the endpoint - and read by every other, which is exactly the hop the projection
	// exists to carry. A Tower that advertises no endpoint publishes rows without one, and
	// those rows are simply never offered to an edge consumer.
	//
	// ONLY self-attached nodes are routable now: the tower-pushed LEAF rows died with the
	// leaf-station generation (their endpoint fed a raw-TLS dial nothing serves anymore, and
	// with the invite flow gone no leaf can be attached to verify against).
	endpoint, _ := ts.link.RelayEndpoint(towerID)
	var rows []fleet.Station
	// SELF-ATTACHED nodes (Option C): their offer lives on the attachment (band-checked at
	// attach), not in a tower's signed inventory - the tower is pure transport for them and
	// pushes no leaf on their behalf. This stays the projection's ONE writer, and Replace
	// keeps its whole-tower semantics.
	if ts.stations != nil {
		ats, aerr := ts.stations.ByTower(towerID)
		if aerr != nil {
			// A partial merge would silently de-list every self node on this tower until the
			// next sweep. Keep the projection as it was rather than publishing a known-partial
			// set; the sweep retries shortly.
			log.Printf("tower %s: could not read self-attached nodes (%v) - projection left unchanged", towerID, aerr)
			return
		}
		{
			for _, at := range ats {
				if at.Model == "" || at.HubToken == "" {
					continue // classic-flow attachment: its offers come from the inventory
				}
				rows = append(rows, fleet.Station{
					TowerID: towerID, StationID: at.StationID, OfferID: "self-" + at.StationID,
					Model: at.Model, Modality: at.Modality, Capacity: 1,
					Expires:  time.Now().Add(selfOfferTTL),
					Endpoint: endpoint,
					PriceIn:  at.PriceIn, PriceOut: at.PriceOut,
					// The join, carried from the attachment where Core verified it, so a
					// reader of this projection can rank the row by measured health.
					NodeID: at.NodeID,
				})
			}
		}
	}
	if err := ts.routable.Replace(towerID, rows); err != nil {
		log.Printf("tower %s: could not publish the routable fleet: %v", towerID, err)
	}
}

// selfOfferTTL bounds how long a self-attached node's routable row lives without a refresh.
// The periodic sweep republishes live towers, so a healthy row never lapses; a tower that
// goes dark stops being refreshed and its rows age out with it.
const selfOfferTTL = time.Hour

// forgetRoutable withdraws a Tower's fleet everywhere at once, for a drain or a revocation.
func (b *broker) forgetRoutable(towerID string) {
	ts := b.tower
	if ts == nil || ts.routable == nil {
		return
	}
	if err := ts.routable.Forget(towerID); err != nil {
		log.Printf("tower %s: could not withdraw the routable fleet: %v", towerID, err)
	}
}

// attemptKeyLabel domain-separates the attempt-state signer from every other use of the CA
// root, including the grant signer.
//
// The spec asks for a purpose-separated attempt-state SERVICE. This is not that yet, but it
// is its own key with its own label - so a compromise of the dispatch signer cannot forge
// attempt state, which is the record money is decided from and the one an operator would
// most want to rewrite.
const attemptKeyLabel = "rogerai tower attempt state signer v1"

// envelopeKeyLabel domain-separates the key results are sealed to. X25519 rather than
// Ed25519: this one receives rather than signs.
const envelopeKeyLabel = "rogerai tower envelope recipient v1"

func deriveAttemptKey(ca *cert.Authority) (ed25519.PrivateKey, error) {
	return deriveKeyFrom(ca, attemptKeyLabel)
}

// deriveEnvelopeKey produces the X25519 key a Station seals results to.
//
// Derived like the others, so it is stable across restarts - which matters because a Station
// PINS it - and so any instance can open a result whichever one dispatched the request.
func deriveEnvelopeKey(ca *cert.Authority) ([]byte, error) {
	seed, err := deriveKeyFrom(ca, envelopeKeyLabel)
	if err != nil {
		return nil, err
	}
	// An Ed25519 private key's first 32 bytes are its seed, which is uniform HKDF output
	// here; X25519 clamps what it is given, so using it as a scalar is safe.
	return seed.Seed(), nil
}

// nextAttemptSequence assigns the independently-assigned Core ordering.
//
// Monotonic and concurrency-safe, which Config.Sequence requires: two attempts handed the
// same position are two attempts nothing downstream can put in order.
func (b *broker) nextAttemptSequence() int64 { return b.attemptSeq.Add(1) }
