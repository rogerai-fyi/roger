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

	"errors"
	"rogerai.fm/roger/v6/internal/towercore/dispatch"
	"rogerai.fm/roger/v6/internal/towercore/envelope"
	"rogerai.fm/roger/v6/internal/towercore/fleet"
	"rogerai.fm/roger/v6/internal/towercore/link"
	"rogerai.fm/roger/v6/internal/towercore/reputation"
	"rogerai.fm/roger/v6/internal/towerhub"
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
	endpoint, endpointPin := row.Endpoint, row.TLSSPKI
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

	outcome := b.driveSealedCanary(grant, target, endpoint, endpointPin, consumerKey, envPriv)
	if outcome == "" {
		// Core could not build the probe out of its own materials. Nobody is at fault but this
		// process, and an aborted probe is not evidence about anybody - see driveSealedCanary.
		return ""
	}
	// ON THE LEDGER, NAMING BOTH PARTIES. The row carries the Station it probed as well as the
	// Tower that carried it, which is the half that used to be missing: the ledger was keyed on
	// (tower, attempt) with no station column at all, so a probe's finding could only ever land
	// on the Tower. The Station is named on EVERY outcome here, pass or fail, tower's fault or
	// station's - naming the machine is a separate question from judging it.
	//
	// The OUTCOME is what decides whose fault it is, and for a canary that is almost always the
	// Tower's: a probe rides the Tower end to end, so a Tower can drop it, stall it past the
	// deadline, substitute the sealed answer, or simply report that nobody is serving the
	// Station. There is no failure on this path a hostile Tower could not have caused, which is
	// exactly why none of them may be moved to the Station on the Tower's say-so. The single
	// exception is a probe that never reached the Tower at all (see driveSealedCanary).
	b.recordOutcome(towerID, row.StationID, grant.AttemptID, outcome)
	// AND IN PROCESS, for placement. This map is not a duplicate of the ledger row above: it is
	// this instance's own placement reading, deliberately kept out of b.trust and read on the
	// authorize path with no store round trip - see edgeCanaryHealth for the whole argument.
	// The DURABLE half is what the probe rotation and the Tower's verdict now read, so a peer's
	// probes count and a restart forgets nothing that matters.
	b.recordEdgeCanary(row.StationID, outcome)
	b.evaluateTower(towerID)
	return outcome
}

// edgeCanaryHealth is what the edge fabric's own probes have found out about ONE Station.
//
// Separate from trustState, deliberately, and the separation is the same one M1's second
// correction drew between edge load and relayed load. trustState is the CLASSIC fabric's
// record: pickFor drops on its probeFails, probeOnce skips on it, /discover prints it. Folding
// a tower canary's verdict into it would let a Tower operator who black-holes traffic depress
// the paid-fabric score of every node behind them - the exact lever that was closed on the
// load counter one release ago, re-opened on the health counter.
//
// So it is its own record, read only by edge placement, and read in one direction: it can send
// a Station to Tier B and it can never lift one.
type edgeCanaryHealth struct {
	// fails is the CONSECUTIVE failure streak, reset by a pass - the same shape as
	// trustState.probeFails, so "troubled" means the same thing on both fabrics.
	fails int
	// at is when this Station was last probed, which is what spreads the next probe: coverage
	// is the point of a canary, and a rotation needs to know who has waited longest.
	at time.Time
}

// edgeCanaryFailBar is how many consecutive failed canaries send a Station to Tier B. Two,
// matching pickFor's probeFails bar: one failure is a blip and the fleet is small enough that
// treating every blip as a demotion would empty Tier A.
const edgeCanaryFailBar = 2

// recordEdgeCanary files a probe's verdict against the Station it actually probed.
//
// A StationFault counts here exactly as a CanaryFail does. For PLACEMENT the question is only
// "can a consumer be served here", and a Station whose own advertised key Core cannot seal to
// answers that with a no as flatly as one that never replies. The two are distinguished on the
// durable ledger, where the question is whose fault it is; they are not distinguished here,
// where it is not.
func (b *broker) recordEdgeCanary(stationID string, outcome reputation.Outcome) {
	if stationID == "" || (outcome != reputation.CanaryPass && outcome != reputation.CanaryFail &&
		outcome != reputation.StationFault) {
		return // an aborted probe is not evidence about anybody
	}
	b.metricsMu.Lock()
	defer b.metricsMu.Unlock()
	if b.edgeCanary == nil {
		b.edgeCanary = map[string]edgeCanaryHealth{}
	}
	h := b.edgeCanary[stationID]
	if outcome != reputation.CanaryPass {
		h.fails++
	} else {
		h.fails = 0
	}
	h.at = time.Now()
	b.edgeCanary[stationID] = h
}

// edgeCanaryTroubledLocked reports whether this Station's own edge probes are failing. Caller
// holds metricsMu (edgeEligible holds it for the whole fleet).
func (b *broker) edgeCanaryTroubledLocked(stationID string) bool {
	return b.edgeCanary[stationID].fails >= edgeCanaryFailBar
}

// edgeCanaryAgeLocked is how long since this Station was last probed, and it answers a very
// long time for one that never has been - a Station with no evidence is the one a coverage
// rotation most needs to reach. Caller holds metricsMu.
func (b *broker) edgeCanaryAgeLocked(stationID string, now time.Time) time.Duration {
	h, seen := b.edgeCanary[stationID]
	if !seen || h.at.IsZero() {
		return neverCanariedAge
	}
	if age := now.Sub(h.at); age > 0 {
		return age
	}
	return 0
}

// neverCanariedAge is the staleness a never-probed Station is scored at. Any value far past
// canaryInterval works; it is a constant rather than a literal so the intent - "longer ago than
// anything real" - is stated where the score is computed.
const neverCanariedAge = 1000 * canaryInterval

// driveSealedCanary probes a HUB-path (self-attached) node exactly as a sealed consumer
// does: seal the canary body to the node's session key, submit the ciphertext to the tower's
// hub, open the answer with the grant-bound envelope key, and demand a valid station receipt
// over real bytes. A tower that served nothing, or made something up, fails every step.
// THE CANARY IS THE THIRD PARTY THAT DIALS A HUB, and it was the easiest one to leave behind:
// it is not the node's leg and not the consumer's client, it is Core probing its own fleet, and
// it built its base URL inline with a copy of the same "http://" + endpoint the other two had.
// Left as it was it would have gone on probing over plaintext against a TLS listener and
// recorded a REPUTATION FAILURE for every tower that turned TLS on - the change would have
// suspended exactly the operators who did the right thing. It goes through towerhub.Reach with
// everyone else.
// The verdict is one of three things, and the third is new: an empty outcome means the probe
// never happened and nothing is recorded about anybody, because the only thing that went wrong
// was inside this process.
func (b *broker) driveSealedCanary(grant dispatch.EdgeGrant, target dispatch.Target, endpoint, endpointPin string, consumerKey ed25519.PrivateKey, envPriv []byte) reputation.Outcome {
	firstByte := time.Now()
	sealedReq, err := envelope.SealTo(target.SessionKey, canaryBodyFor(grant.Model), grant.AttemptID)
	if err != nil {
		// THE STATION'S, and it is the cleanest attribution in the whole system: sealing happens
		// before Core has dialed anything, so the Tower has not been given the chance to do
		// anything wrong yet. What failed is the SESSION KEY the Station itself put on its
		// attachment - the X25519 half nothing proves possession of, so a Station may advertise
		// a key that no envelope can be sealed to, and thirty-two zero bytes is admitted today.
		// This used to record a canary failure against the TOWER, so a squatter's dead
		// attachment spent its Tower's reputation on every sweep, forever, for the price of one
		// attach. That is the sentence docs/relay-selection-design.md 5.6 kept repeating - "the
		// squatter's own Station is the only casualty" - and this is the line that had made it
		// false.
		log.Printf("canary: station %s on tower %s advertises a session key nothing can be sealed to: %v",
			target.StationID, target.TowerID, err)
		return reputation.StationFault
	}
	sealedRaw, err := sealedReq.Marshal()
	if err != nil {
		// Core's own encoding of Core's own envelope. Not evidence about the Tower and not
		// evidence about the Station: an empty outcome records nothing at all.
		log.Printf("canary: could not encode a probe for tower %s: %v", target.TowerID, err)
		return ""
	}
	// The canary is Roger Core dialing an operator-supplied address. Vetted at the
	// socket, on the RESOLVED addresses: a Tower advertising loopback, a private range,
	// or the metadata service must not turn Core's own probe into a request against
	// Core's host or network (server-side request forgery). A loopback advert is a
	// legitimate same-machine test rig for its OWN node - it is simply not canaryable,
	// and the skip is recorded as unreachable-by-design rather than counted as a failure.
	if verr := endpointNotPublic(context.Background(), endpoint, b.canaryVet); verr != nil {
		log.Printf("canary: tower %s endpoint %s skipped: %v (unreachable by design, not a failure)",
			target.TowerID, endpoint, verr)
		return ""
	}
	base, httpc, err := towerhub.ReachVetted(endpoint, endpointPin, b.canaryVet)
	if err != nil {
		// An endpoint or pin this process cannot even build a client for is the tower's own
		// advertisement being malformed. That is a finding about the tower, not a skip: a
		// consumer routed there would fail in exactly the same way.
		log.Printf("canary: tower %s advertises an unusable data plane: %v", target.TowerID, err)
		return reputation.CanaryFail
	}
	hc := &towerhub.Client{BaseURL: base, HTTP: httpc}
	ctx, cancel := context.WithTimeout(context.Background(), canaryTimeout)
	defer cancel()
	res, err := hc.SubmitJob(ctx, grant.Signed, sealedRaw)
	if isDesignSkip(err) {
		// A dial-time refusal from the vet is the rebinding backstop behind the
		// pre-screen: unreachable by design, not a failing Tower. ONLY this error skips;
		// a nil error or any other transport failure falls through to be judged below.
		log.Printf("canary: tower %s endpoint %s skipped at dial: %v", target.TowerID, endpoint, err)
		return ""
	}
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
//
// # IT USED TO TAKE THE FIRST ONE, FOREVER
//
// This loop returned the first row that resolved, over a projection query that sorts by station
// id - so behind each Tower the lexicographically first Station was canaried on every sweep for
// the life of the deployment, and every other Station behind it was never probed at all. That is
// the pre-M1 bug, surviving in the one place that produces edge-SPECIFIC health evidence, and it
// did three separate kinds of damage: the one probed Station's health became its whole Tower's
// reputation, one bad machine could suspend a relay carrying twenty good ones, and nineteen
// operators rode free on a twentieth's uptime.
//
// # WHAT IT SELECTS ON, AND WHY IT IS NOT SCORE
//
// The same selectP2C the paid router and edge placement both use, but weighted by STALENESS
// rather than quality, because a canary and a placement want opposite things. A placement wants
// the best Station; a canary wants the one whose health is least known, and ranking probes by
// quality would probe the healthy Stations most and leave a sick one un-probed precisely
// because it is sick - a feedback loop that keeps its own evidence from ever arriving.
//
// So the score is how long it has been since this Station was last probed, normalized against
// the sweep interval, and a Station that has NEVER been probed scores the ceiling. P2C's live-
// load tie-break is kept as it comes out of edgeEligible, which means that between two equally
// overdue Stations the idler one is probed - the same courtesy probeOnce extends on the classic
// fabric, and it costs the busy one nothing.
//
// Eligibility is edgeEligible's, both tiers merged, and it is a PREFERENCE rather than a gate.
// A canary must reach a Tier B Station - being in Tier B is a reason to probe it, not a reason
// not to - and it should prefer Stations a consumer could actually be sent to, so a failure it
// records against the Tower is a failure on the path consumers use. But when NOTHING behind a
// Tower is placeable it falls back to every Station that resolved, because the alternative is
// worse: a Tower whose machines have all gone quiet would stop being probed at exactly the
// moment it stopped working, and its reputation would freeze at whatever it last was, unable to
// degrade or to recover. A Tower with nothing reachable behind it is not carrying work, and that
// IS the finding this probe exists to make.
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
	shortlist := make([]fleet.Station, 0, len(rows))
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
		shortlist = append(shortlist, row)
	}
	// One read for the whole shortlist, and the same authority re-check placement makes - a
	// canary that dispatched to an attachment placement would refuse is not probing the fabric
	// consumers use.
	keep, targets := b.resolveEdgeCandidates(shortlist)
	if len(keep) == 0 {
		return dispatch.Target{}, fleet.Station{}, false
	}
	now := time.Now()
	tierA, tierB := b.edgeEligible(keep, b.bannedOwnerNodeSet(), now)
	probable := make([]scoredCand, 0, len(tierA)+len(tierB))
	probable = append(probable, tierA...)
	probable = append(probable, tierB...)
	if len(probable) == 0 {
		// The fallback: everything that resolved, scored at zero load, because there is no
		// eligibility reading to carry over for a candidate eligibility rejected.
		for i := range keep {
			probable = append(probable, scoredCand{idx: i})
		}
	}
	chosen := selectP2C(b.canaryCoverage(keep, probable, b.stationCanaryEvidence(towerID, now), now),
		canaryBeta, edgePlacementRand())
	if chosen < 0 {
		return dispatch.Target{}, fleet.Station{}, false
	}
	return targets[chosen], keep[chosen], true
}

// canaryBeta is the sampling concentration for canary coverage. ONE - draw in proportion to how
// overdue a Station is, and no more sharply than that. Edge placement uses the router's balanced
// beta because it is trying to route to the best Station; a rotation that concentrated the same
// way would over-probe whichever Station happened to be most overdue and starve the rest, which
// is the magnet this function exists to remove wearing a different hat.
const canaryBeta = 1.0

// canaryTroubledSlowdown is how much longer a Station that is failing every probe waits between
// them: its staleness is measured against ten sweep intervals instead of one.
//
// # WHY THE PROBE BUDGET IS WHERE THE FOUNDER'S RULING LANDS
//
// The harm was never that one probe was blamed on the wrong party. It was AMPLIFICATION: probe
// budget is spent per Station and the verdict is read per Tower, so a Station that can never
// answer soaked the rotation forever and its Tower paid for every failure. One dead machine
// behind a two-Station Tower was half the sweep and forty percent is the quarantine bar; and
// because attaching is self-serve, anyone could attach a handful of Stations that do not serve
// and take somebody else's honest Tower off the fabric with them. That is a denial primitive
// against an operator who did nothing, built out of a health probe.
//
// So a Station that is failing everything keeps costing its Tower - it must, or the fix would be
// a laundry - but it costs it a TENTH as much per sweep. The effect is proportional, which is
// the property that makes it safe: a Tower failing one Station in twenty scores near zero, a
// Tower failing nineteen in twenty still fails most of its probes and still quarantines, and
// there is no threshold for an attacker to sit just underneath.
//
// # WHY IT IS A SLOWER CLOCK AND NOT AN EXCLUSION
//
// Because the score stays bounded and keeps climbing. A Station probed rarely eventually becomes
// stale enough to win a draw whatever its history, so the evidence that could clear it can
// always arrive - the same reason canaryTargetFor probes a demoted Station rather than skipping
// it. An exclusion would freeze a Station's record at its worst moment, and it would freeze a
// black-holing Tower's too, by leaving it nothing to probe.
const canaryTroubledSlowdown = 10

// stationCanaryEvidence reads what the DURABLE ledger knows about each Station behind one Tower.
//
// From the ledger rather than from b.edgeCanary, deliberately, and this is the answer to "should
// the per-station evidence become durable too". Both, with different jobs. The in-process map
// stays exactly what its comment says it is - this instance's placement reading, off the
// authorize path, deliberately not in b.trust. But the probe ROTATION shapes the evidence a
// Tower is judged on, and a judgement built out of one process's memory is one broker's fraction
// of the evidence mistaken for the whole: an attempt is probed by whichever instance holds the
// Tower's link, that instance restarts, and the rotation begins again from nothing while the
// verdict it feeds is computed from a shared table that remembers everything. Reading the same
// ledger the verdict reads is what stops the two disagreeing.
//
// It costs one grouped scan of one Tower's window per sweep - every five minutes, not per
// request - and a failure to read it simply damps nobody, which is today's behaviour.
func (b *broker) stationCanaryEvidence(towerID string, now time.Time) map[string]reputation.Tally {
	ts := b.tower
	if ts == nil || ts.outcomes == nil {
		return nil
	}
	byStation, err := ts.outcomes.TallyByStation(towerID, now.Add(-reputationWindow))
	if err != nil {
		log.Printf("canary: could not read per-station evidence for tower %s: %v", towerID, err)
		return nil
	}
	return byStation
}

// canaryTroubled reports whether the durable window says this Station has answered nothing.
//
// Both halves are required. ZERO PASSES, because one pass is proof the machine can serve through
// this Tower and the failures around it are then a question about carriage rather than about the
// machine - and it is what makes recovery instant: the first probe that gets through puts a
// Station back on the fast clock. AND AT LEAST THE FAIL BAR, the same two consecutive failures
// that demote a Station in placement, because one failure is a blip and a Station with a single
// fail and no pass yet is usually one that has simply not been probed twice.
//
// A StationFault counts with the fails: for the purpose of "is it worth spending probes here",
// a Station whose key nothing can seal to is as unanswerable as one that never replies.
func canaryTroubled(t reputation.Tally) bool {
	return t.CanaryPass == 0 && t.CanaryFail+t.StationFault >= edgeCanaryFailBar
}

// canaryCoverage scores the probable Stations by how overdue each one's probe is.
//
// age/(age+canaryInterval) is a bounded 0..1 staleness: zero for a Station probed just now, a
// half at one sweep interval, approaching one for a Station nobody has looked at in a long time
// - and exactly the ceiling for one that has never been probed at all. Bounded matters because
// selectP2C's band is a RELATIVE gap from the best score, so an unbounded score would make one
// very old Station push every other out of the band and become the magnet again.
//
// A Station the ledger says has answered nothing is measured against a LONGER interval instead -
// see canaryTroubledSlowdown for why the probe budget is where a dead Station's cost to its
// Tower is bounded.
//
// The load comes from edgeEligible's own scoring pass, so no second lock acquisition and no
// second instant: the number the tie-break uses is the number placement saw.
func (b *broker) canaryCoverage(keep []fleet.Station, probable []scoredCand, byStation map[string]reputation.Tally, now time.Time) []scoredCand {
	b.metricsMu.Lock()
	defer b.metricsMu.Unlock()
	out := make([]scoredCand, 0, len(probable))
	for _, c := range probable {
		stationID := keep[c.idx].StationID
		age := b.edgeCanaryAgeLocked(stationID, now)
		interval := time.Duration(canaryInterval)
		if canaryTroubled(byStation[stationID]) {
			interval *= canaryTroubledSlowdown
		}
		out = append(out, scoredCand{
			idx:   c.idx,
			score: float64(age) / float64(age+interval),
			load:  c.load,
		})
	}
	return out
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

// isDesignSkip reports whether a hub-submit error is the dial-time vet's own refusal - and
// ONLY that. A nil error (the submit worked) and every ordinary transport failure return
// false, so a healthy canary is not skipped and a Tower that dropped the work is not
// excused. Extracted so this decision is proven without staging a live DNS rebind.
func isDesignSkip(err error) bool { return errors.Is(err, errNotPublic) }
