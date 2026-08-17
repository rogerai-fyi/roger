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
	"crypto/x509"
	"encoding/base64"
	"log"
	"strings"
	"time"

	"rogerai.fm/roger/v5/internal/edgeclient"
	"rogerai.fm/roger/v5/internal/towercore/dispatch"
	"rogerai.fm/roger/v5/internal/towercore/envelope"
	"rogerai.fm/roger/v5/internal/towercore/link"
	"rogerai.fm/roger/v5/internal/towercore/reputation"
	"rogerai.fm/roger/v5/internal/towerhub"
)

// canaryBody is what a canary asks a model. Small and harmless; the answer's CONTENT is not
// checked, only that a signed one came back, so it does not matter what the model replies.
var canaryBody = []byte(`{"model":"canary","messages":[{"role":"user","content":"ping"}],"max_tokens":1}`)

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
	target, endpoint, sealedPath, ok := b.canaryTargetFor(towerID)
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
	// On the sealed path the canary also holds an ephemeral X25519 envelope key, bound into
	// the grant like any consumer's - the node seals the answer to it, and only Core opens it.
	var envPub, envPriv []byte
	if sealedPath {
		if envPub, envPriv, err = envelope.NewKey(); err != nil {
			return ""
		}
	}
	grant, err := ts.dispatch.MintEdge(dispatch.EdgeTarget{
		TowerID: target.TowerID, StationID: target.StationID, StationEpoch: target.StationEpoch,
		Model: target.Model, Modality: target.Modality,
		RelayName: target.StationID + "." + relayDomain(),
		MaxIn:     edgeMaxBytes, MaxOut: edgeMaxBytes, AssertionKey: target.AssertionKey,
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

	var outcome reputation.Outcome
	if sealedPath {
		outcome = b.driveSealedCanary(grant, target, endpoint, envPriv)
	} else {
		outcome = b.driveCanary(grant, target, endpoint, consumerKey)
	}
	b.recordOutcome(towerID, grant.AttemptID, outcome)
	b.evaluateTower(towerID)
	return outcome
}

// driveCanary carries the request as a consumer and judges the answer.
func (b *broker) driveCanary(grant dispatch.EdgeGrant, target dispatch.Target, endpoint string, consumerKey ed25519.PrivateKey) reputation.Outcome {
	roots := x509.NewCertPool()
	roots.AddCert(b.tower.ca.Root())
	client := &edgeclient.Client{Key: consumerKey, Roots: roots, Network: link.PublicNetwork}
	ctx, cancel := context.WithTimeout(context.Background(), canaryTimeout)
	defer cancel()

	res, err := client.Do(ctx, edgeclient.Authorization{
		AttemptID: grant.AttemptID, Grant: base64.StdEncoding.EncodeToString(grant.Signed),
		RelayName: grant.RelayName, Endpoint: endpoint,
		MaxIn: grant.MaxIn, MaxOut: grant.MaxOut,
	}, "/v1/chat/completions", canaryBody)
	if err != nil || res.Status != 200 || len(res.Body) == 0 {
		// Did not connect, did not complete, or came back empty. The "serving nothing" case.
		return reputation.CanaryFail
	}
	// A VALID STATION RECEIPT over the bytes that came back is the pass. A Tower that returned
	// something it made up cannot produce one signed by the Station's attachment-recorded key.
	raw, err := base64.StdEncoding.DecodeString(res.Receipt())
	if err != nil {
		return reputation.CanaryFail
	}
	rec, err := dispatch.ParseReceipt(raw, target.AssertionKey, link.PublicNetwork,
		grant.AttemptID, target.StationID)
	if err != nil {
		return reputation.CanaryFail
	}
	if rec.ResponseDigest == "" {
		return reputation.CanaryFail
	}
	return reputation.CanaryPass
}

// driveSealedCanary probes a HUB-path (self-attached) node exactly as a sealed consumer
// does: seal the canary body to the node's session key, submit the ciphertext to the tower's
// hub, open the answer with the grant-bound envelope key, and demand a valid station receipt
// over real bytes. A tower that served nothing, or made something up, fails every step.
func (b *broker) driveSealedCanary(grant dispatch.EdgeGrant, target dispatch.Target, endpoint string, envPriv []byte) reputation.Outcome {
	sealedReq, err := envelope.SealTo(target.SessionKey, canaryBody, grant.AttemptID)
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
	return reputation.CanaryPass
}

// canaryTargetFor picks a routable Station with a data plane behind a specific Tower.
//
// Per-Tower, unlike edgeTargetFor which picks the best Station for a model across the whole
// fleet: a canary tests ONE Tower, so it must route to that Tower or not at all.
func (b *broker) canaryTargetFor(towerID string) (dispatch.Target, string, bool, bool) {
	ts := b.tower
	if ts == nil || ts.routable == nil || !ts.registry.MayTakeWork(towerID) {
		return dispatch.Target{}, "", false, false
	}
	// From the routable PROJECTION, not this instance's in-memory inventory: a canary may run
	// on an instance that does not hold the Tower's link, and reading local inventory would
	// make it blind to exactly the Towers another instance is carrying.
	rows, err := ts.routable.ByTower(towerID, time.Now())
	if err != nil {
		return dispatch.Target{}, "", false, false
	}
	for _, row := range rows {
		if row.Endpoint == "" {
			continue
		}
		// A SELF-ATTACHED row serves the HUB path: the canary probes it with a SEALED submit,
		// exactly as a real consumer would - not the raw-TLS dial, which would fail every
		// time and quarantine a healthy hub tower on evidence Core manufactured itself.
		sealed := strings.HasPrefix(row.OfferID, "self-")
		if target, ok := b.targetFor(row.TowerID, row.StationID, row.Model, row.Modality); ok {
			return target, row.Endpoint, sealed, true
		}
	}
	return dispatch.Target{}, "", false, false
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
