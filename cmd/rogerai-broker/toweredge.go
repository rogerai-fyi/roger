package main

// toweredge.go is Roger Core's half of the EDGE path: authorize, then settle. Between those
// two small messages the payload goes nowhere near this process.
//
// Contract: features/tower/edge_dispatch.feature.
//
// # WHAT CHANGED, AND WHY IT IS WORTH IT
//
// On the relayed path Core carries every byte twice and counts them itself. A Tower there
// offloads GPU time Core was never spending, which is why it was a cost centre with extra
// steps and why there was nothing worth paying an operator for.
//
// Here Core handles an authorize and an ack - two small, constant-size messages - and the
// prompt and completion travel consumer to Station through a Tower that cannot read them.
// For a long completion that is orders of magnitude less traffic through this process. That
// difference IS the operator's contribution, and it is what makes compensation coherent
// rather than charity.
//
// # WHAT CORE GIVES UP, STATED PLAINLY
//
// It cannot screen content before dispatch, because it never sees it, and it cannot count
// the bytes itself. Settlement rests instead on two signed claims from parties with opposing
// interests - the Station's receipt and the consumer's acknowledgement - reconciled in
// dispatch.Reconcile. That is weaker than first-hand observation and stronger than trusting
// either party alone. Screening for edge traffic moves entirely to sampled post-hoc audit.

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"time"

	"rogerai.fm/roger/v5/internal/protocol"
	"rogerai.fm/roger/v5/internal/towercore/admit"
	"rogerai.fm/roger/v5/internal/towercore/attempt"
	"rogerai.fm/roger/v5/internal/towercore/dispatch"
	"rogerai.fm/roger/v5/internal/towercore/link"
	"rogerai.fm/roger/v5/internal/towercore/reputation"
	"rogerai.fm/roger/v5/internal/towerobj"
)

// edgeSettleGrace is how long after the grant's deadline a receipt may still settle. The
// grant deadline bounds EXECUTION - the Station refuses work past it - but the receipt
// travels by a slower road: Station outbox, Tower collection, one more hop to Core. Evidence
// for work done in time must not fail because its courier ran on a schedule.
const edgeSettleGrace = 10 * time.Minute

// edgeMaxBytes caps what one grant may authorize in either direction, whatever the caller
// asks for. It matches the Station's own request ceiling: a grant for more than a Station
// will read is a promise the network cannot keep.
const edgeMaxBytes = 8 << 20

// relayDomain is the DNS suffix Station relay names live under. Core's to choose - a Station
// that picked its own name could answer for another Station - and configurable because the
// domain is deployment topology, not code.
func relayDomain() string {
	if v := os.Getenv("ROGERAI_TOWER_RELAY_DOMAIN"); v != "" {
		return v
	}
	return "relay.rogerai.fm"
}

// towerEdgeAuthorize is the ON-RAMP: a consumer asks to be routed to a Station through a
// Tower, and Core answers with a grant and a place to connect.
//
// This is the whole of Core's involvement in the request's data. What it hands back is a
// few hundred constant-size bytes; the prompt and the completion will travel consumer to
// Station through a relay that cannot read them, and Core will next hear about this attempt
// when the evidence comes home. That asymmetry - two small messages here, the payload
// elsewhere - is the entire reason Towers are worth paying for.
func (b *broker) towerEdgeAuthorize(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodPost) {
		return
	}
	corsCreds(w, r)
	ts := b.towerAvailable(w)
	if ts == nil {
		return
	}
	body := readTowerBody(r)
	// SIGNED, because a grant is an authorization issued to somebody. An anonymous grant
	// could not be tied to an account when its acknowledgement arrives - or when a policy
	// violation is found in audit and somebody has to be answerable for it.
	_, authed, ok := b.identityOf(r, body)
	if !ok || !authed {
		jsonErr(w, http.StatusUnauthorized, "an edge authorization needs a signed request")
		return
	}
	var req struct {
		Model  string `json:"model"`
		MaxIn  int64  `json:"max_in,omitempty"`
		MaxOut int64  `json:"max_out,omitempty"`
	}
	if err := json.Unmarshal(body, &req); err != nil || req.Model == "" {
		jsonErr(w, http.StatusBadRequest, "an edge authorization names the model it wants")
		return
	}
	// The caller may ask for LESS than the ceiling, never more. The bounds are the only
	// thing standing between one authorization and an unmetered Station.
	maxIn, maxOut := req.MaxIn, req.MaxOut
	if maxIn <= 0 || maxIn > edgeMaxBytes {
		maxIn = edgeMaxBytes
	}
	if maxOut <= 0 || maxOut > edgeMaxBytes {
		maxOut = edgeMaxBytes
	}

	target, endpoint, ok := b.edgeTargetFor(req.Model)
	if !ok {
		// The same refusal whether the model is unknown, every Station is busy, or no Tower
		// carries a data plane: what a consumer needs to know is "not here, not now", and
		// enumerating which Towers exist is nobody's business.
		jsonErr(w, http.StatusServiceUnavailable, "no Station can take this on the edge path right now")
		return
	}

	g, err := ts.dispatch.MintEdge(dispatch.EdgeTarget{
		TowerID: target.TowerID, StationID: target.StationID, StationEpoch: target.StationEpoch,
		Model: target.Model, Modality: target.Modality,
		RelayName: target.StationID + "." + relayDomain(),
		MaxIn:     maxIn, MaxOut: maxOut, AssertionKey: target.AssertionKey,
	})
	if err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "could not authorize this attempt - try again")
		return
	}
	// RECORDED BEFORE IT IS HANDED OUT, on both ledgers, exactly as the relayed path does
	// it: an authorization nobody recorded is work whose outcome cannot be established
	// afterwards. The dispatch record is what makes the nonce one-use at settlement, and
	// its deadline extends past the grant's by the settlement grace - the grant bounds
	// execution, the record bounds evidence.
	if err := b.openEdgeAttempt(g, target); err != nil {
		log.Printf("edge authorize: could not record attempt %s: %v", g.AttemptID, err)
		jsonErr(w, http.StatusServiceUnavailable, "could not record this attempt - try again")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"attempt_id": g.AttemptID,
		"grant":      base64.StdEncoding.EncodeToString(g.Signed),
		"relay_name": g.RelayName,
		// Where to CONNECT: the Tower's data plane, as the Tower itself advertised on its
		// link. The Station's own address appears nowhere - reachability is the Tower's
		// contribution, and hiding the Station is part of what the operator provides.
		"endpoint": endpoint,
		"deadline": g.Deadline.Unix(),
		"max_in":   g.MaxIn,
		"max_out":  g.MaxOut,
		"note": "connect to endpoint with TLS server name relay_name, send the grant in the " +
			"X-Rogerai-Grant header, and acknowledge what you receive at /tower/edge/ack - an " +
			"honest acknowledgement can only ever reduce what you are billed",
	})
}

// openEdgeAttempt records the attempt behind an edge grant, on both ledgers, before the
// grant leaves the building.
//
// The dispatch record is what later makes settlement one-use, and ITS deadline is the
// grant's plus the settlement grace: the grant bounds execution, the record bounds evidence,
// and a receipt for work done in time must not be refused because its courier - Station
// outbox, Tower collection, one hop to Core - ran on a schedule.
func (b *broker) openEdgeAttempt(g dispatch.EdgeGrant, target dispatch.Target) error {
	ts := b.tower
	if err := ts.dispatch.Store().Put(dispatch.Record{
		AttemptID: g.AttemptID, JobID: g.JobID, TowerID: g.TowerID, StationID: g.StationID,
		StationEpoch: g.StationEpoch, Model: g.Model, Modality: g.Modality,
		Nonce: g.Nonce, Deadline: g.Deadline.Add(edgeSettleGrace),
		Grant: g.Signed, AssertionKey: target.AssertionKey, State: dispatch.StateIssued,
	}); err != nil {
		return err
	}
	if ts.attempts == nil {
		return nil
	}
	grantHash, err := towerobj.Hash(g.Signed)
	if err != nil {
		return err
	}
	_, _, err = ts.attempts.Issue(attempt.IssueSpec{
		Network: link.PublicNetwork, JobID: g.JobID, RequestID: g.JobID,
		AttemptID: g.AttemptID, Origin: attempt.OriginJoined,
		GrantHash: grantHash, LeaseHash: grantHash,
		Hold:                attempt.NoHold(g.AttemptID),
		StationRevision:     g.StationEpoch,
		Deadline:            g.Deadline,
		FinalizationCeiling: g.Deadline.Add(edgeSettleGrace),
	})
	return err
}

// edgeTargetFor picks a Station that is reachable through a Tower's data plane.
//
// Every check re-runs against AUTHORITY: the fleet projection says a Station was routable a
// moment ago on some instance, but whether it may serve NOW is decided by the admission
// registry and the attachment record - never by the read model. And only rows with an
// endpoint qualify: a Tower that relays nothing has no edge to route a consumer to, however
// healthy its Stations are on the relayed path.
func (b *broker) edgeTargetFor(model string) (dispatch.Target, string, bool) {
	ts := b.tower
	if ts == nil || ts.routable == nil {
		return dispatch.Target{}, "", false
	}
	rows, err := ts.routable.Candidates(model, time.Now())
	if err != nil {
		log.Printf("edge authorize: cannot read the routable fleet: %v", err)
		return dispatch.Target{}, "", false
	}
	for _, row := range rows {
		if row.Endpoint == "" {
			continue
		}
		if !ts.registry.MayTakeWork(row.TowerID) {
			continue
		}
		if target, ok := b.targetFor(row.TowerID, row.StationID, row.Model, row.Modality); ok {
			return target, row.Endpoint, true
		}
	}
	return dispatch.Target{}, "", false
}

// towerEdgeAck accepts the consumer's signed statement about what it actually received.
//
// THE ONLY INDEPENDENT CLAIM CORE GETS. The Station is the party being paid and its receipt
// is its own account of its own work; this is the account of somebody with the opposite
// interest, and a Tower sits between the two able to forge neither.
func (b *broker) towerEdgeAck(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodPost) {
		return
	}
	corsCreds(w, r)
	ts := b.towerAvailable(w)
	if ts == nil {
		return
	}
	body := readTowerBody(r)

	// The acknowledgement is signed with the consumer's own key, and the REQUEST carrying it
	// is signed with the same one. Requiring both means the object is attributable later, to
	// somebody who was not present when it was made - which is the whole point of evidence in
	// a dispute with an operator.
	_, authed, ok := b.identityOf(r, body)
	if !ok || !authed {
		jsonErr(w, http.StatusUnauthorized,
			"an acknowledgement must be signed: it is evidence, and unsigned evidence settles nothing")
		return
	}
	pubHex := r.Header.Get(protocol.HeaderPubkey)
	pub, err := hex.DecodeString(pubHex)
	if err != nil || len(pub) != 32 {
		jsonErr(w, http.StatusBadRequest, "this request's public key is unreadable")
		return
	}

	var req struct {
		AttemptID string `json:"attempt_id"`
		Ack       string `json:"ack"` // base64 of the signed acknowledgement
	}
	if err := json.Unmarshal(body, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "this acknowledgement cannot be read")
		return
	}
	if req.AttemptID == "" || req.Ack == "" {
		jsonErr(w, http.StatusBadRequest, "an acknowledgement names its attempt and carries the signed object")
		return
	}
	raw, err := base64.StdEncoding.DecodeString(req.Ack)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, "this acknowledgement is not valid base64")
		return
	}
	// VERIFIED AGAINST THE KEY THAT SIGNED THE REQUEST, not one named in the object. A
	// self-describing key would let anybody file an acknowledgement as anybody.
	ack, err := dispatch.ParseAck(raw, pub, link.PublicNetwork, req.AttemptID)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := ts.acks.Put(req.AttemptID, ack); err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "this acknowledgement could not be recorded")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"attempt_id": ack.AttemptID,
		"recorded":   true,
		"note": "settlement will take the lower of this and the Station's own count, so an " +
			"honest acknowledgement can only ever reduce what you are billed",
	})
}

// settleEdgeAttempt reconciles a Station's receipt against whatever the consumer said.
//
// It settles WITHOUT an acknowledgement rather than waiting for one, and marks the result
// uncorroborated. Customers close laptops mid-stream and third-party clients will never
// acknowledge at all; an operator who lost money every time is an operator who leaves, and a
// network with no operators is not more secure, it is empty. The signal is the RATE.
func (b *broker) settleEdgeAttempt(attemptID string, receipt dispatch.Receipt) (dispatch.Settlement, error) {
	ts := b.tower
	var ack *dispatch.Ack
	if ts != nil && ts.acks != nil {
		if got, found, err := ts.acks.Get(attemptID); err == nil && found {
			ack = &got
		}
		// A LOOKUP FAILURE IS NOT A MISMATCH. Treating an unreachable store as "no
		// acknowledgement" settles uncorroborated, which is the safe direction: it never
		// invents corroboration that was not there.
	}
	return dispatch.Reconcile(receipt, ack)
}

// towerEdgeSettle takes the Station's receipt, relayed by its Tower on the link it already
// holds, and settles the attempt.
//
// # WHY THE RECEIPT COMES THIS WAY AND THE ACK COMES DIRECT
//
// A Station cannot reach Roger Core. It only ever talks to its Tower, so its receipt travels
// the one path it has. That is safe because a Tower cannot forge one: the receipt is signed
// with the assertion key recorded at ATTACHMENT, which the Tower has never held.
//
// The consumer's acknowledgement arrives separately, direct, signed with the consumer's own
// key. Two reports, two paths, two keys, and the Tower is between them holding neither. A
// Tower that alters the answer makes the two disagree about the digest, and that disagreement
// is attributable to the only party that saw both.
//
// # IT SETTLES WITHOUT AN ACKNOWLEDGEMENT
//
// On purpose. If it waited, an operator would be unpaid every time a customer closed a
// laptop, and third-party clients never acknowledge at all. The attempt settles on the
// receipt alone, marked uncorroborated, and the rate is what gets looked at.
func (b *broker) towerEdgeSettle(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodPost) {
		return
	}
	ts := b.towerAvailable(w)
	if ts == nil {
		return
	}
	body := readTowerBody(r)
	// NO USAGE FIELDS IN HERE, and their absence is the design. An earlier shape took
	// usage_in/usage_out in this body - which the TOWER sends - and fed them to settlement.
	// The claim the Station is paid on now lives inside the receipt's signature, where the
	// party forwarding it cannot hold the pen.
	var req struct {
		TowerID   string `json:"tower_id"`
		StationID string `json:"station_id"`
		AttemptID string `json:"attempt_id"`
		// Receipt is base64 of the Station's signed object, relayed verbatim.
		Receipt string `json:"receipt"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	// THE TOWER'S OWN SIGNED REQUEST. It is not being trusted for the receipt's contents -
	// that is checked below against a key it has never held - but a settlement filed by
	// anybody at all would let a stranger close other people's attempts.
	if _, _, ok := b.towerCaller(r, body, req.TowerID); !ok {
		jsonErr(w, http.StatusForbidden, "settling an attempt requires a registered Tower's own signed request")
		return
	}
	if req.AttemptID == "" || req.Receipt == "" || req.StationID == "" {
		jsonErr(w, http.StatusBadRequest, "a settlement names its attempt and Station and carries the receipt")
		return
	}
	raw, err := base64.StdEncoding.DecodeString(req.Receipt)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, "this receipt is not valid base64")
		return
	}
	// THE KEY COMES FROM THE ATTACHMENT RECORD, never from the message. Taking it from what
	// the relay sent would make "signed by the Station" mean "signed by whoever is relaying",
	// and every guarantee on this path rests on those being different things.
	at, found, err := ts.stations.Station(req.StationID)
	if err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "could not read the Station registry - try again in a moment")
		return
	}
	if !found {
		jsonErr(w, http.StatusNotFound, "no such Station")
		return
	}
	if at.Origin.TowerID != req.TowerID {
		// A Tower settling for a Station behind somebody else's origin.
		jsonErr(w, http.StatusForbidden, "that Station is not attached to this Tower")
		return
	}
	key, err := hex.DecodeString(at.AssertionKey)
	if err != nil || len(key) != 32 {
		jsonErr(w, http.StatusServiceUnavailable, "this Station's recorded key is unreadable")
		return
	}
	receipt, err := dispatch.ParseReceipt(raw, key, link.PublicNetwork, req.AttemptID, req.StationID)
	if err != nil {
		jsonErr(w, http.StatusForbidden, err.Error())
		return
	}
	// ONE-USE, ENFORCED HERE, through the same shared store the relayed path uses. The claim
	// is a compare-and-swap: a second settlement for this attempt - a replayed receipt, a
	// stale answer served twice - loses the swap on whichever broker it reaches, and the
	// refusal explains itself. This is also what ties the settling Tower to the attempt: the
	// claim is keyed by Tower, so a Tower cannot close out an attempt granted through
	// another.
	now := time.Now()
	if _, cerr := ts.dispatch.Store().ClaimByID(req.AttemptID, req.TowerID, now); cerr != nil {
		if errors.Is(cerr, dispatch.ErrAlreadySettled) || errors.Is(cerr, dispatch.ErrAlreadyClaimed) {
			jsonErr(w, http.StatusConflict, "this attempt has already been settled")
			return
		}
		if errors.Is(cerr, dispatch.ErrExpired) {
			jsonErr(w, http.StatusForbidden, "this attempt's settlement window has closed")
			return
		}
		jsonErr(w, http.StatusNotFound, "no such attempt for this Tower")
		return
	}

	settled, err := b.settleEdgeAttempt(req.AttemptID, receipt)
	if err != nil {
		// A digest disagreement is the one refusal that means something happened rather than
		// something was malformed, so it is reported as a conflict and attributed - and the
		// attempt is closed as FAILED rather than released: evidence this broken does not
		// get a second try with a different story.
		b.noteAttempt(req.AttemptID, attempt.Observation{
			Kind: attempt.KindExecutionFailed, EvidenceHash: req.AttemptID,
			Reason: err.Error(), ReleaseID: "release-" + req.AttemptID,
		})
		if errors.Is(err, dispatch.ErrDigestMismatch) {
			// A disagreement about the bytes is attributable to the relay, and it is exactly
			// the kind of thing the rate exists to surface across a Tower's attempts.
			b.recordOutcome(req.TowerID, req.AttemptID, reputation.Disputed)
			b.evaluateTower(req.TowerID)
			jsonErr(w, http.StatusConflict,
				"the Station and the consumer signed for different responses - "+
					"the only party between them is the relay")
			return
		}
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, serr := ts.dispatch.Store().Settle(req.AttemptID, now); serr != nil {
		// The claim above succeeded, so a failure here is a store fault rather than a race
		// we lost - reported, and the attempt chain still records what the evidence said.
		log.Printf("edge settle: attempt %s claimed but not settled: %v", req.AttemptID, serr)
		jsonErr(w, http.StatusServiceUnavailable, "could not commit this settlement - retry")
		return
	}
	// The attempt chain hears about it AFTER the store's answer is final, mirroring the
	// relayed path: evidence first, then the settlement commitment.
	b.noteAttempt(req.AttemptID, attempt.Observation{
		Kind: attempt.KindEvidenceObserved, EvidenceHash: receipt.ResponseDigest,
	})
	b.noteAttempt(req.AttemptID, attempt.Observation{
		Kind: attempt.KindSettlementCommitted, EvidenceHash: receipt.ResponseDigest,
	})
	// AND THE REPUTATION LEDGER, so the RATE this Tower is judged on reflects this attempt.
	// The outcome is a fact about what settled; whether the rate warrants action is decided
	// separately, in evaluateTower, on evidence this records.
	outcome := reputation.Uncorroborated
	if settled.Corroborated {
		outcome = reputation.Corroborated
	}
	b.recordOutcome(req.TowerID, req.AttemptID, outcome)
	// Judged AFTER the outcome is recorded, so this attempt is in the window. The verdict may
	// quarantine the Tower on strong evidence; it never touches THIS settlement, which has
	// already committed - the money is decided, the reputation is a separate consequence.
	b.evaluateTower(req.TowerID)
	writeJSON(w, http.StatusOK, map[string]any{
		"attempt_id":   settled.AttemptID,
		"corroborated": settled.Corroborated,
		"billable_in":  settled.Billable.In,
		"billable_out": settled.Billable.Out,
	})
}

// recordOutcome writes what became of an edge attempt to the reputation ledger, best effort.
//
// Best effort DELIBERATELY: a lost outcome under-counts a Tower's evidence, which is the safe
// direction - it can only ever make a Tower look BETTER than it is, never worse, so a dropped
// write cannot manufacture a penalty nobody earned. The settlement it describes has already
// committed; the reputation write is downstream of the money, never a gate on it.
func (b *broker) recordOutcome(towerID, attemptID string, o reputation.Outcome) {
	ts := b.tower
	if ts == nil || ts.outcomes == nil {
		return
	}
	if err := ts.outcomes.Record(reputation.Event{
		TowerID: towerID, AttemptID: attemptID, Outcome: o, At: time.Now(),
	}); err != nil {
		log.Printf("tower %s: could not record outcome %s for %s: %v", towerID, o, attemptID, err)
	}
}

// reputationWindow is how far back a Tower is judged. Long enough that a rate is a pattern
// rather than a moment, short enough that a Tower that cleaned up its act is not held to last
// month forever.
const reputationWindow = 24 * time.Hour

// evaluateTower reads a Tower's recent outcomes against the fleet and acts on the verdict.
//
// It is called after evidence is recorded, and it ACTS - quarantine on strong evidence - but
// it never reverses a settlement: "individual attempts already settled are not reversed by
// the rate alone" is enforced by this only ever moving lifecycle state, never money.
func (b *broker) evaluateTower(towerID string) reputation.Verdict {
	ts := b.tower
	if ts == nil || ts.outcomes == nil {
		return reputation.Clean
	}
	since := time.Now().Add(-reputationWindow)
	tower, err := ts.outcomes.Tally(towerID, since)
	if err != nil {
		log.Printf("tower %s: could not read outcomes: %v", towerID, err)
		return reputation.Clean
	}
	fleet, err := ts.outcomes.FleetTally(since)
	if err != nil {
		log.Printf("could not read fleet outcomes: %v", err)
		return reputation.Clean
	}
	// The baseline is the REST of the fleet, this Tower removed. A Tower compared to a fleet
	// it is part of can never look unusual relative to itself, which on a small network is
	// most of the fleet.
	verdict := ts.repPolicy.Evaluate(tower, fleet.Without(tower))
	if verdict == reputation.Quarantine {
		// SUSPENDED, not quarantine: quarantine is the post-enrollment holding pen and an
		// ACTIVE Tower cannot legally move there, while suspend is exactly "stop an active
		// Tower now, keep its identity, reversible". Both withhold work (EligibilityNone);
		// suspend is the one the transition table allows from active.
		//
		// Best effort: a Tower that cannot be moved right now is re-evaluated on its next
		// attempt and the evidence does not go away. A Tower already off gets a harmless
		// no-op refusal.
		if terr := ts.registry.Transition(towerID, admit.StateSuspended); terr != nil {
			log.Printf("tower %s: evidence warrants suspension but the move failed: %v", towerID, terr)
		} else {
			log.Printf("tower %s: suspended on reputation evidence", towerID)
			b.forgetRoutable(towerID)
		}
	}
	return verdict
}
