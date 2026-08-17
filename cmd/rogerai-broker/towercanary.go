package main

// towercanary.go is Roger Core probing a Tower by using it - authorize an attempt through it,
// carry a request as a consumer would, and see whether a valid answer comes back.
//
// Contract: features/tower/edge_dispatch.feature.
//
// # WHAT A CANARY CATCHES, AND WHAT IT DOES NOT
//
// It catches "serving nothing at all": a Tower that accepts work and drops it, or a Station
// that is down behind a Tower that looks healthy on its link. Core originates the attempt so
// the Tower cannot tell it from a customer's, and the SUCCESS SIGNAL is a valid Station
// receipt over the bytes that came back - a Tower that returned nothing, or garbage, cannot
// produce one.
//
// It does NOT check that the model's ANSWER is correct. Core cannot know the right answer to
// an arbitrary operator's model, so a canary is a liveness-and-integrity probe, not a content
// one - that is what the sampled transcript audit is for. Stated plainly so nobody reads a
// green canary as "this Tower is honest about content".
//
// # WHY IT IS INDISTINGUISHABLE
//
// The grant, the relay name, the endpoint and the timing are an ordinary edge attempt's,
// because they ARE one: minted the same way, recorded on the same ledgers, settled by the
// same courier. Nothing about the wire says canary. The only difference is that Core is the
// consumer and knows the outcome without waiting for anyone to tell it.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"log"
	"strings"
	"time"

	"rogerai.fm/roger/v5/internal/towercore/dispatch"
	"rogerai.fm/roger/v5/internal/towercore/envelope"
	"rogerai.fm/roger/v5/internal/towercore/fleet"
	"rogerai.fm/roger/v5/internal/towercore/link"
	"rogerai.fm/roger/v5/internal/towercore/reputation"
	"rogerai.fm/roger/v5/internal/towerhub"
)

// canaryPrompts are innocuous requests a canary rotates between. The BODY names the grant's
// real model and reads like any tiny consumer request: an earlier body literally said
// "model":"canary", which let any node grep the plaintext and serve honestly on exactly the
// watched attempts - defeating the one probe designed to be unpredictable (audit CRITICAL).
var canaryPrompts = []string{
	"hi", "what time is it", "thanks", "one word: ready?", "say ok", "hello there",
}

// canaryBodyFor builds one probe body for the target's own model, prompt chosen by an
// unpredictable coin. The content is never checked - only that a signed answer came back.
func canaryBodyFor(model string) []byte {
	var buf [1]byte
	_, _ = rand.Read(buf[:])
	prompt := canaryPrompts[int(buf[0])%len(canaryPrompts)]
	raw, err := json.Marshal(map[string]any{
		"model": model, "max_tokens": 16,
		"messages": []map[string]string{{"role": "user", "content": prompt}},
	})
	if err != nil {
		return []byte(`{"messages":[{"role":"user","content":"hi"}],"max_tokens":16}`)
	}
	return raw
}

// canaryTimeout bounds one probe. A Tower that has not answered in this long is not carrying
// work, which is the finding.
const canaryTimeout = 30 * time.Second

// RunCanary probes one Tower and records the outcome. Exported for a scheduler to call; the
// broker's own periodic sweep calls it too.
//
// It reports the verdict so a caller can see what happened, but the RECORD is the point: a
// single failure is nothing, a pattern is what suspends a Tower, and the pattern lives in the
// reputation ledger this writes to.
func (b *broker) RunCanary(towerID string) reputation.Outcome {
	ts := b.tower
	if ts == nil || ts.ca == nil {
		return ""
	}
	target, row, ok := b.canaryTargetFor(towerID)
	endpoint := row.Endpoint
	if !ok {
		// No routable Station with a data plane behind this Tower to probe. Not a failure -
		// there is nothing to canary - so nothing is recorded.
		return ""
	}
	// Core is the consumer for a canary, so it holds an ephemeral consumer key - fresh per
	// probe, so a canary is not tied to a standing account. The SAME key is bound into the
	// grant and drives the request, so the acknowledgement (if the probe made one) would
	// verify, exactly as a real consumer's does.
	consumerPub, consumerKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return ""
	}
	// The canary also holds an ephemeral X25519 envelope key, bound into the grant like any
	// consumer's - the node seals the answer to it, and only Core opens it.
	envPub, envPriv, err := envelope.NewKey()
	if err != nil {
		return ""
	}
	// The grant is shaped exactly like a paying consumer's: the row's own pinned prices and
	// the standard token ceilings. A canary grant with zero pricing on a priced node was
	// itself a marker (audit M1); no hold is ever placed, so pinning the price costs nothing.
	grant, err := ts.dispatch.MintEdge(dispatch.EdgeTarget{
		TowerID: target.TowerID, StationID: target.StationID, StationEpoch: target.StationEpoch,
		Model: target.Model, Modality: target.Modality,
		RelayName: target.StationID + "." + relayDomain(),
		MaxIn:     edgeMaxBytes, MaxOut: edgeMaxBytes, AssertionKey: target.AssertionKey,
		MaxTokIn: edgeMaxTokens, MaxTokOut: edgeMaxTokens,
		PriceInMicros: row.PriceIn, PriceOutMicros: row.PriceOut,
		ConsumerKey: consumerPub, ConsumerEnvKey: envPub,
	})
	if err != nil {
		return ""
	}
	// Recorded exactly as a customer attempt is - if a canary skipped this it would BE
	// distinguishable, and an attempt nobody recorded could not settle through the courier.
	if err := b.openEdgeAttempt(grant, target); err != nil {
		log.Printf("canary: could not record attempt for tower %s: %v", towerID, err)
		return ""
	}

	outcome := b.driveSealedCanary(grant, target, endpoint, consumerKey, envPriv)
	b.recordOutcome(towerID, grant.AttemptID, outcome)
	b.evaluateTower(towerID)
	return outcome
}

// driveSealedCanary probes a HUB-path (self-attached) node exactly as a sealed consumer
// does: seal the canary body to the node's session key, submit the ciphertext to the tower's
// hub, open the answer with the grant-bound envelope key, and demand a valid station receipt
// over real bytes. A tower that served nothing, or made something up, fails every step.
func (b *broker) driveSealedCanary(grant dispatch.EdgeGrant, target dispatch.Target, endpoint string, consumerKey ed25519.PrivateKey, envPriv []byte) reputation.Outcome {
	firstByte := time.Now()
	sealedReq, err := envelope.SealTo(target.SessionKey, canaryBodyFor(grant.Model), grant.AttemptID)
	if err != nil {
		return reputation.CanaryFail
	}
	sealedRaw, err := sealedReq.Marshal()
	if err != nil {
		return reputation.CanaryFail
	}
	base := endpoint
	if !strings.Contains(base, "://") {
		base = "http://" + base
	}
	hc := &towerhub.Client{BaseURL: base}
	ctx, cancel := context.WithTimeout(context.Background(), canaryTimeout)
	defer cancel()
	res, err := hc.SubmitJob(ctx, grant.Signed, sealedRaw)
	if err != nil || res.Failure != "" || len(res.Envelope) == 0 || len(res.Receipt) == 0 {
		return reputation.CanaryFail
	}
	parsed, err := envelope.Parse(res.Envelope)
	if err != nil {
		return reputation.CanaryFail
	}
	answer, err := envelope.OpenWith(envPriv, parsed, grant.AttemptID)
	if err != nil || len(answer) == 0 {
		// Unopenable or empty: whatever came back was not sealed to the grant's key over
		// this attempt - a substitution or a blank, both fails.
		return reputation.CanaryFail
	}
	rec, err := dispatch.ParseReceipt(res.Receipt, target.AssertionKey, link.PublicNetwork,
		grant.AttemptID, target.StationID)
	if err != nil || rec.ResponseDigest == "" {
		return reputation.CanaryFail
	}
	// The receipt must be over the BYTES CORE OPENED, not merely valid: a node that signed a
	// digest of something else has substituted content (audit L4 - one line, real binding).
	if rec.ResponseDigest != dispatch.DigestOf(answer) {
		return reputation.CanaryFail
	}
	// ACKNOWLEDGE, exactly as a first-party consumer does (audit M2): without this every
	// canary settles Uncorroborated and an otherwise-idle tower accumulates a 100%
	// uncorroborated rate out of probes Core itself sent. Core is the consumer here, so the
	// ack goes straight into its own store.
	if ts := b.tower; ts != nil && ts.acks != nil {
		if ack, aerr := dispatch.SignAck(consumerKey, link.PublicNetwork, grant.AttemptID,
			answer, dispatch.Usage{In: 0, Out: int64(len(answer))}, firstByte, time.Now()); aerr == nil {
			_ = ts.acks.Put(grant.AttemptID, ack)
		}
	}
	return reputation.CanaryPass
}

// canaryTargetFor picks a routable Station with a data plane behind a specific Tower.
//
// Per-Tower, unlike edgeTargetFor which picks the best Station for a model across the whole
// fleet: a canary tests ONE Tower, so it must route to that Tower or not at all.
func (b *broker) canaryTargetFor(towerID string) (dispatch.Target, fleet.Station, bool) {
	ts := b.tower
	if ts == nil || ts.routable == nil || !ts.registry.MayTakeWork(towerID) {
		return dispatch.Target{}, fleet.Station{}, false
	}
	// From the routable PROJECTION, not this instance's in-memory inventory: a canary may run
	// on an instance that does not hold the Tower's link, and reading local inventory would
	// make it blind to exactly the Towers another instance is carrying.
	rows, err := ts.routable.ByTower(towerID, time.Now())
	if err != nil {
		return dispatch.Target{}, fleet.Station{}, false
	}
	for _, row := range rows {
		if row.Endpoint == "" {
			continue
		}
		// Only SELF-ATTACHED (hub) rows are canary targets now: the raw-TLS drive died with
		// the leaf-station generation, and probing a leaf row sealed would fail a plane it
		// never served.
		if !strings.HasPrefix(row.OfferID, "self-") {
			continue
		}
		if target, ok := b.targetFor(row.TowerID, row.StationID, row.Model, row.Modality); ok {
			return target, row, true
		}
	}
	return dispatch.Target{}, fleet.Station{}, false
}

// canaryInterval is how often Core probes the fleet. Frequent enough that a Tower that goes
// dark is caught within minutes; cheap, because each canary is one small round trip.
const canaryInterval = 5 * time.Minute

// towerCanarySweep probes the routable fleet on a timer until stopped.
func (b *broker) towerCanarySweep(stop <-chan struct{}) {
	if b.tower == nil {
		return
	}
	t := time.NewTicker(canaryInterval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			b.towerCanarySweepOnce()
		}
	}
}

// towerCanarySweepOnce probes every Tower with a data plane once. Split out so the sweep is
// testable without a ticker.
func (b *broker) towerCanarySweepOnce() {
	ts := b.tower
	if ts == nil || ts.routable == nil {
		return
	}
	towers, err := ts.routable.RoutableTowers(time.Now())
	if err != nil {
		log.Printf("canary sweep: could not list routable towers: %v", err)
		return
	}
	for _, towerID := range towers {
		if outcome := b.RunCanary(towerID); outcome == reputation.CanaryFail {
			log.Printf("canary: tower %s failed", towerID)
		}
	}
}
