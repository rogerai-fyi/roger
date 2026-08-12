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
	"net/http"

	"rogerai.fm/roger/v5/internal/protocol"
	"rogerai.fm/roger/v5/internal/towercore/dispatch"
	"rogerai.fm/roger/v5/internal/towercore/link"
)

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
func (b *broker) settleEdgeAttempt(attemptID string, receipt dispatch.Receipt,
	claimed dispatch.Usage) (dispatch.Settlement, error) {

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
	return dispatch.Reconcile(receipt, claimed, ack)
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
	var req struct {
		TowerID   string `json:"tower_id"`
		StationID string `json:"station_id"`
		AttemptID string `json:"attempt_id"`
		// Receipt is base64 of the Station's signed object, relayed verbatim.
		Receipt  string `json:"receipt"`
		UsageIn  int64  `json:"usage_in"`
		UsageOut int64  `json:"usage_out"`
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
	if req.UsageIn < 0 || req.UsageOut < 0 {
		jsonErr(w, http.StatusBadRequest, "usage cannot be negative")
		return
	}

	settled, err := b.settleEdgeAttempt(req.AttemptID, receipt,
		dispatch.Usage{In: req.UsageIn, Out: req.UsageOut})
	if err != nil {
		// A digest disagreement is the one refusal that means something happened rather than
		// something was malformed, so it is reported as a conflict and attributed.
		if errors.Is(err, dispatch.ErrDigestMismatch) {
			jsonErr(w, http.StatusConflict,
				"the Station and the consumer signed for different responses - "+
					"the only party between them is the relay")
			return
		}
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"attempt_id":   settled.AttemptID,
		"corroborated": settled.Corroborated,
		"billable_in":  settled.Billable.In,
		"billable_out": settled.Billable.Out,
	})
}
