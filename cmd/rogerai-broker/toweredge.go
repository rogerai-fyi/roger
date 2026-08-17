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
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"rogerai.fm/roger/v5/internal/protocol"
	"rogerai.fm/roger/v5/internal/towercore/admit"
	"rogerai.fm/roger/v5/internal/towercore/attempt"
	"rogerai.fm/roger/v5/internal/towercore/comp"
	"rogerai.fm/roger/v5/internal/towercore/dispatch"
	"rogerai.fm/roger/v5/internal/towercore/earnings"
	"rogerai.fm/roger/v5/internal/towercore/fleet"
	"rogerai.fm/roger/v5/internal/towercore/link"
	"rogerai.fm/roger/v5/internal/towercore/reputation"
	"rogerai.fm/roger/v5/internal/towerobj"
)

// maxEdgeSettleGrace is how long after the grant's deadline a receipt may still settle. The
// grant deadline bounds EXECUTION - the Station refuses work past it - but the receipt travels
// by a slower road: Station outbox, Tower collection, one more hop to Core. Evidence for work
// done in time must not fail because its courier ran on a schedule.
const maxEdgeSettleGrace = 10 * time.Minute

// edgeSettleGrace keeps the settlement window strictly INSIDE the pre-auth hold's lifetime when
// edge billing is on. A consumer's hold is reclaimed by the orphan sweep after holdTTL; if the
// settlement window (grant lifetime + this grace) outran that, a late-but-valid receipt would
// find its hold already swept and settle for free with the operator unpaid. So the grace is
// capped a few minutes under holdTTL, so the hold always outlives the deadline it guards. With a
// generous holdTTL it is just maxEdgeSettleGrace; a short holdTTL shortens the courier window
// rather than silently losing the money.
func edgeSettleGrace() time.Duration {
	g := holdTTL() - 3*time.Minute // margin over the (sub-2m) grant lifetime
	if g > maxEdgeSettleGrace {
		g = maxEdgeSettleGrace
	}
	if g < time.Minute {
		g = time.Minute
	}
	return g
}

// towerNodePrefix tags an earning lot's node as a Tower-RELAY share, so the earnings surface can
// split "serving" (a node ran the model) from "relaying" (a Tower carried the traffic) for one
// operator. IsTowerNode reads it back.
const towerNodePrefix = "tower:"

func towerNode(towerID string) string { return towerNodePrefix + towerID }

// IsTowerNode reports whether an earning-rollup node key is a Tower-relay share.
func IsTowerNode(node string) bool { return strings.HasPrefix(node, towerNodePrefix) }

// edgeMaxBytes caps what one grant may authorize in either direction, whatever the caller
// asks for. It matches the Station's own request ceiling: a grant for more than a Station
// will read is a promise the network cannot keep.
const edgeMaxBytes = 8 << 20

// edgeMaxTokens caps the token ceiling an edge grant may authorize (Option C per-token
// billing). A token is at least one byte, so this stays well under edgeMaxBytes; ~1M tokens
// is far more than any single request needs and bounds the worst-case wallet hold + payout.
const edgeMaxTokens = 1 << 20

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
	// The account the grant is issued TO. Signed into the grant, so the acknowledgement can
	// only come from this consumer - not any account that later learns the attempt id. The
	// pubkey header is already validated: identityOf above verified a signature with it, so it
	// is well-formed hex of the right length by the time we reach here.
	consumerKey, _ := hex.DecodeString(r.Header.Get(protocol.HeaderPubkey))
	var req struct {
		Model     string `json:"model"`
		MaxIn     int64  `json:"max_in,omitempty"`
		MaxOut    int64  `json:"max_out,omitempty"`
		MaxTokIn  int64  `json:"max_tok_in,omitempty"`
		MaxTokOut int64  `json:"max_tok_out,omitempty"`
		// ConsumerEnvKey is the consumer's X25519 public key (hex, 32 bytes), OPTIONAL: on the
		// hub (Topology 2) path the node seals its ANSWER to this, so it crosses the tower
		// unreadable. Signed into the grant, so the tower cannot swap it.
		ConsumerEnvKey string `json:"consumer_env_key,omitempty"`
	}
	if err := json.Unmarshal(body, &req); err != nil || req.Model == "" {
		jsonErr(w, http.StatusBadRequest, "an edge authorization names the model it wants")
		return
	}
	// TOWER INFERENCE REQUIRES A SIGNED-IN ACCOUNT. Being served - and billed - for
	// tower-relayed inference is limited to accounts that signed in, which is where the terms of
	// service (including that this traffic is charged) are accepted. A signature proves possession
	// of a key; this proves the key belongs to a real, non-anonymized account that accepted the
	// terms. It is the consent gate for charging real money, checked before any Station is chosen
	// or any hold is placed.
	o, found, oerr := b.db.OwnerByPubkey(hex.EncodeToString(consumerKey))
	if oerr != nil || !found || o.Anonymized {
		jsonErr(w, http.StatusForbidden, "tower inference requires a signed-in account that has accepted the terms of service")
		return
	}
	// A banned account is signed in but not entitled to be served or charged. The refusal is the
	// same 403 as an absent account, so a ban is not distinguishable from "not signed in" to a
	// prober. The ban is checked per DEVICE KEY, matching the direct serving path (tunnel.go); a
	// per-ACCOUNT ban (one that follows every device key an account holds, as self-dealing
	// detection already does via sameAccount) is a system-wide model change to make deliberately,
	// not something the edge path should do unilaterally and inconsistently with the rest.
	if b.isOwnerBanned(o.Pubkey) {
		jsonErr(w, http.StatusForbidden, "tower inference requires a signed-in account that has accepted the terms of service")
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
	// TOKEN ceilings for the Option C per-token path, bounded like the byte ceilings. The
	// consumer declares what it wants authorized (it need not reveal the request - the broker
	// is blind); an unset or over-large bound falls back to edgeMaxTokens. These ride the grant
	// alongside the byte ceilings and bound both the wallet hold and the settle-time token
	// clamp. Tokens <= bytes always, so edgeMaxTokens <= edgeMaxBytes.
	maxTokIn, maxTokOut := req.MaxTokIn, req.MaxTokOut
	if maxTokIn <= 0 || maxTokIn > edgeMaxTokens {
		maxTokIn = edgeMaxTokens
	}
	if maxTokOut <= 0 || maxTokOut > edgeMaxTokens {
		maxTokOut = edgeMaxTokens
	}

	var consumerEnvKey []byte
	if req.ConsumerEnvKey != "" {
		raw, derr := hex.DecodeString(req.ConsumerEnvKey)
		if derr != nil || len(raw) != 32 {
			jsonErr(w, http.StatusBadRequest, "consumer_env_key must be a hex-encoded 32-byte X25519 public key")
			return
		}
		consumerEnvKey = raw
	}

	target, row, ok := b.edgeTargetFor(req.Model)
	endpoint := row.Endpoint
	if !ok {
		// The same refusal whether the model is unknown, every Station is busy, or no Tower
		// carries a data plane: what a consumer needs to know is "not here, not now", and
		// enumerating which Towers exist is nobody's business.
		jsonErr(w, http.StatusServiceUnavailable, "no Station can take this on the edge path right now")
		return
	}
	// THE FLEET PROJECTION IS NOT A SECURITY BOUNDARY - the price is re-checked against the
	// public band HERE, at the moment it becomes money. The row's price came from a signed,
	// band-checked leaf, but the projection rows themselves are unsigned database state; an
	// out-of-band writer (or a future second publisher) must not be able to pin an arbitrary
	// price into a Core-signed grant. Out of band -> refuse rather than clamp: a wrong price
	// is a wrong offer, not one to silently reprice.
	if row.PriceIn != 0 || row.PriceOut != 0 {
		if floor, ceiling, bok := towerPriceBand(req.Model); !bok ||
			row.PriceIn < floor || row.PriceIn > ceiling ||
			row.PriceOut < floor || row.PriceOut > ceiling {
			log.Printf("edge authorize: routable row for %s/%s carries an out-of-band price (%d/%d) - refused",
				row.TowerID, row.StationID, row.PriceIn, row.PriceOut)
			jsonErr(w, http.StatusServiceUnavailable, "no Station can take this on the edge path right now")
			return
		}
	}

	g, err := ts.dispatch.MintEdge(dispatch.EdgeTarget{
		TowerID: target.TowerID, StationID: target.StationID, StationEpoch: target.StationEpoch,
		Model: target.Model, Modality: target.Modality,
		RelayName: target.StationID + "." + relayDomain(),
		MaxIn:     maxIn, MaxOut: maxOut,
		MaxTokIn: maxTokIn, MaxTokOut: maxTokOut, AssertionKey: target.AssertionKey,
		ConsumerKey: consumerKey, ConsumerEnvKey: consumerEnvKey,
		// THE PRICE IS PINNED HERE, from the Station's signed, band-checked offer, into the
		// Core-signed grant - so settlement bills the number the consumer authorized against,
		// and a price change between authorize and settle cannot reprice this attempt.
		PriceInMicros: row.PriceIn, PriceOutMicros: row.PriceOut,
	})
	if err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "could not authorize this attempt - try again")
		return
	}
	// PAID EDGE TRAFFIC RESERVES FUNDS UP FRONT. When a per-byte edge price is configured, hold
	// the price of the grant's CEILING against the consumer's wallet before the attempt is handed
	// out, so the work is only authorized if the consumer can pay for the most it could cost; the
	// settle-time capture refunds the unused remainder. Free (unpriced) edge traffic skips this.
	// The grant was minted but is NOT recorded until the hold succeeds, so a refused hold leaves
	// no usable attempt behind.
	// Hold and capture MUST use the SAME wallet or funds move between pots. Both use the
	// consumer's ACCOUNT wallet (u_gh_/u_apple_/u_email_) - resolved here from the owner the
	// account gate already looked up - so a relayed request reserves from and bills the same
	// balance a direct request would, not the device-key wallet.
	consumerWallet, cwok := accountWalletForOwner(o)
	if !cwok {
		jsonErr(w, http.StatusForbidden, "tower inference requires a signed-in account that has accepted the terms of service")
		return
	}
	// The hold covers the WORST CASE under whichever tariff can bill this attempt: the byte
	// tariff's ceiling price, or the token ceiling at the grant's pinned per-token price. The
	// settle-time capture charges the actual figure and refunds the remainder.
	maxCost := edgePriceCredits(maxIn, maxOut)
	if tc := tokenCostCredits(maxTokIn, maxTokOut, row.PriceIn, row.PriceOut); tc > maxCost {
		maxCost = tc
	}
	if maxCost > 0 {
		if ok, herr := b.db.HoldFor(consumerWallet, g.AttemptID, maxCost); herr != nil || !ok {
			jsonErr(w, http.StatusPaymentRequired, "insufficient balance for this request")
			return
		}
	}
	// RECORDED BEFORE IT IS HANDED OUT, on both ledgers, exactly as the relayed path does
	// it: an authorization nobody recorded is work whose outcome cannot be established
	// afterwards. The dispatch record is what makes the nonce one-use at settlement, and
	// its deadline extends past the grant's by the settlement grace - the grant bounds
	// execution, the record bounds evidence.
	if err := b.openEdgeAttempt(g, target); err != nil {
		log.Printf("edge authorize: could not record attempt %s: %v", g.AttemptID, err)
		// If a hold was placed just above, release it: the attempt does not exist, so it will
		// never settle to capture it, and leaving it would strand the consumer's funds until the
		// orphan sweep. Idempotent and a no-op when no hold was placed (unpriced traffic).
		if maxCost > 0 {
			if _, rerr := b.db.ReleaseHoldFor(consumerWallet, g.AttemptID); rerr != nil {
				log.Printf("edge authorize: could not release hold for orphaned attempt %s: %v", g.AttemptID, rerr)
			}
		}
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
		// THE PRICE, IN THE OPEN. The pinned per-token price, the token ceilings, and the
		// worst-case hold are what the consumer is agreeing to by using this grant - inside
		// the base64 grant is not "shown", so they are echoed here where a client can display
		// them before a byte is sent.
		"max_tok_in":       g.MaxTokIn,
		"max_tok_out":      g.MaxTokOut,
		"price_in_micros":  g.PriceInMicros,
		"price_out_micros": g.PriceOutMicros,
		"max_hold_credits": round6(maxCost),
		// The STATION'S session key, straight from Core's attachment record: what the consumer
		// seals its REQUEST to on the hub path. Handed here - Core to consumer - so the tower
		// never gets to name the key its relayed bytes are encrypted to.
		"station_session_key": hex.EncodeToString(target.SessionKey),
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
		Nonce: g.Nonce, Deadline: g.Deadline.Add(edgeSettleGrace()),
		Grant: g.Signed, AssertionKey: target.AssertionKey, ConsumerKey: g.ConsumerKey,
		State: dispatch.StateIssued,
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
		FinalizationCeiling: g.Deadline.Add(edgeSettleGrace()),
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
func (b *broker) edgeTargetFor(model string) (dispatch.Target, fleet.Station, bool) {
	ts := b.tower
	if ts == nil || ts.routable == nil {
		return dispatch.Target{}, fleet.Station{}, false
	}
	rows, err := ts.routable.Candidates(model, time.Now())
	if err != nil {
		log.Printf("edge authorize: cannot read the routable fleet: %v", err)
		return dispatch.Target{}, fleet.Station{}, false
	}
	for _, row := range rows {
		if row.Endpoint == "" {
			continue
		}
		if !ts.registry.MayTakeWork(row.TowerID) {
			continue
		}
		if target, ok := b.targetFor(row.TowerID, row.StationID, row.Model, row.Modality); ok {
			// The whole ROW rides back: the endpoint the consumer connects to, and the signed
			// leaf's consumer price that authorize pins into the grant.
			return target, row, true
		}
	}
	return dispatch.Target{}, fleet.Station{}, false
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
	// BOUND TO THE AUTHORIZED CONSUMER. The attempt must exist and its grant must have been
	// issued to THIS caller's key - otherwise any signed-in account that learned an attempt id
	// could file an acknowledgement for somebody else's work, and could spray them at random
	// ids to grow the store. A review found the ack unbound. Answered the same for an unknown
	// attempt and one issued to a different consumer, so this cannot probe which ids exist.
	rec, found, gerr := ts.dispatch.Store().Get(req.AttemptID)
	if gerr != nil {
		jsonErr(w, http.StatusServiceUnavailable, "could not read the attempt - try again")
		return
	}
	if !found {
		jsonErr(w, http.StatusNotFound, "no such attempt to acknowledge")
		return
	}
	if subtleConstEq(rec.ConsumerKey, pub) != 1 {
		// Issued to a different consumer (or not an edge attempt at all). Answered the same as
		// an unknown attempt: a caller acknowledging work that is not theirs learns nothing.
		jsonErr(w, http.StatusNotFound, "no such attempt to acknowledge")
		return
	}
	// VERIFIED AGAINST THE KEY THAT SIGNED THE REQUEST, not one named in the object, and that
	// key is now known to be the authorized consumer's.
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
func (b *broker) settleEdgeAttempt(attemptID string, receipt dispatch.Receipt) (dispatch.Settlement, bool, error) {
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
	settled, err := dispatch.Reconcile(receipt, ack)
	if errors.Is(err, dispatch.ErrDigestMismatch) {
		// THE ACK DISAGREES WITH THE RECEIPT ABOUT THE BYTES. A review found the old handling
		// dangerous: it VOIDED the settlement and blamed the Tower, so a lying consumer could
		// deny the Station its pay and frame a third party by signing a false digest - and Core
		// cannot tell, from two digests, whether the relay tampered or the consumer lied.
		//
		// So the disagreement no longer voids anything. The attempt SETTLES on the Station's
		// receipt (uncorroborated - the Station is paid for the work it signed), and the
		// dispute is recorded as a SIGNAL that feeds the rate, not a single-attempt penalty.
		// A Tower actually tampering shows an unusual dispute rate across many attempts; one
		// consumer's lie shows up as one dispute and is lost in the noise. The transcript audit
		// is what can look closer.
		settled, err = dispatch.Reconcile(receipt, nil)
		return settled, true, err
	}
	// A usage contradiction under matching digests is also a dispute: the digests agree on the
	// bytes, so the usage must too, and one party lied about the length of what both signed for.
	// Settled conservatively (the lower figure) by Reconcile; flagged here so it is audited.
	return settled, settled.UsageDisputed, err
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
	//
	// wire_in/wire_out are NOT that mistake returning: they are the Tower's own count of the
	// SEALED bytes it relayed, and settlement uses them only as an UPPER bound on the billable
	// bytes (spec: "The Tower's wire count bounds what a Station can bill"). Sealed bytes
	// bound the plaintext they carry, so the attestation can lower a bill - never raise one -
	// and a Tower that lies low only shrinks its own 10%.
	var req struct {
		TowerID   string `json:"tower_id"`
		StationID string `json:"station_id"`
		AttemptID string `json:"attempt_id"`
		// Receipt is base64 of the Station's signed object, relayed verbatim.
		Receipt string `json:"receipt"`
		WireIn  int64  `json:"wire_in"`
		WireOut int64  `json:"wire_out"`
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
	// BIND THE SETTLEMENT TO THE STATION THE GRANT COMMITTED TO. The record names the Station
	// this attempt was granted for; the request names a Station too, and the two must be the
	// same. Without this, a Tower running more than one attached Station (the ordinary
	// multi-GPU case) could settle attempt X - granted for Station Z - with a receipt its OWN
	// Station Y signed, closing the attempt against Y and accruing Y's owner for work X never
	// authorized. It would also slip the ceiling check, which reads the grant's Station and
	// would find it was for Z, not Y. Everything below therefore uses the request's Station id
	// only after it is proven equal to the granted one.
	rec, recFound, recErr := ts.dispatch.Store().Get(req.AttemptID)
	if recErr != nil {
		jsonErr(w, http.StatusServiceUnavailable, "could not read this attempt - try again in a moment")
		return
	}
	if !recFound || rec.StationID != req.StationID {
		// Uniform with "no such attempt": a settlement naming the wrong Station for a real
		// attempt must not be distinguishable from one naming an attempt that does not exist.
		jsonErr(w, http.StatusNotFound, "no such attempt for this Station")
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
	// RECONCILE FIRST - it is read-only. An earlier order claimed the attempt and only then
	// reconciled, so a receipt that could not be reconciled (e.g. it reports negative usage)
	// returned 400 having already moved the attempt to `claimed`, where a retry can never
	// re-settle it and the operator's pay is lost. Reconciling before the claim means a bad
	// receipt is refused without consuming the one-use claim.
	settled, disputed, err := b.settleEdgeAttempt(req.AttemptID, receipt)
	if err != nil {
		b.noteAttempt(req.AttemptID, attempt.Observation{
			Kind: attempt.KindExecutionFailed, EvidenceHash: req.AttemptID,
			Reason: err.Error(), ReleaseID: "release-" + req.AttemptID,
		})
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}

	// ONE-USE, ENFORCED HERE, through the same shared store the relayed path uses. The claim is
	// a compare-and-swap keyed by Tower, so a Tower cannot close out an attempt granted through
	// another, and a replayed receipt loses the swap on whichever broker it reaches.
	//
	// RECOVERABLE, deliberately: claim and settle are two swaps, and a fault or crash between
	// them (or a Settle that failed transiently) leaves the attempt stranded in `claimed`. An
	// edge attempt has no other reason to sit claimed - unlike the relayed path there is no
	// dispatched work in flight - so a retry from the same Tower RE-DRIVES it through Settle
	// rather than being refused forever. A stranded settlement no longer permanently loses the
	// operator's pay to one bad moment. A genuine double-settle still loses the Settle swap
	// below, and the accrual keyed by attempt id cannot double-count whichever path commits.
	now := time.Now()
	alreadySettled := false
	if _, cerr := ts.dispatch.Store().ClaimByID(req.AttemptID, req.TowerID, now); cerr != nil {
		switch {
		case errors.Is(cerr, dispatch.ErrAlreadySettled):
			// The one-use dispatch settle already committed, but the wallet capture and evidence
			// writes that FOLLOW it are separate, non-atomic, and idempotent. A prior attempt that
			// faulted after the settle but before the capture would otherwise be refused here and
			// leave the consumer's hold to be swept (free work) and the operators unpaid. So we do
			// not 409: we skip the (already-done) Settle and re-run the post-settlement steps,
			// every one of which is idempotent, to COMPLETE a half-finished settlement.
			alreadySettled = true
		case errors.Is(cerr, dispatch.ErrAlreadyClaimed):
			// Stranded from a prior interrupted settle - fall through and re-drive Settle.
		case errors.Is(cerr, dispatch.ErrExpired):
			jsonErr(w, http.StatusForbidden, "this attempt's settlement window has closed")
			return
		default:
			jsonErr(w, http.StatusNotFound, "no such attempt for this Tower")
			return
		}
	}
	if !alreadySettled {
		if _, serr := ts.dispatch.Store().Settle(req.AttemptID, now); serr != nil {
			if errors.Is(serr, dispatch.ErrAlreadySettled) {
				// A concurrent re-drive won the settle; complete the idempotent post-processing
				// rather than 409, so billing still finishes if that winner faulted before it.
				alreadySettled = true
			} else if errors.Is(serr, dispatch.ErrExpired) {
				jsonErr(w, http.StatusForbidden, "this attempt's settlement window has closed")
				return
			} else {
				log.Printf("edge settle: attempt %s reconciled but not committed: %v", req.AttemptID, serr)
				jsonErr(w, http.StatusServiceUnavailable, "could not commit this settlement - retry")
				return
			}
		}
	}
	// BOUND THE BILLABLE FIGURE TO WHAT THE GRANT AUTHORIZED. On the no-acknowledgement path
	// (the common one) the billable usage is the Station's own signed number, and the Station's
	// operator is the party being paid - so without this the amount owed is bounded only by the
	// operator's own signature. The grant's ceiling is the one quantity in the exchange the
	// payee did not choose; a receipt claiming more than it has exceeded its authorization, so
	// the figure is clamped to the ceiling AND the attempt is treated as disputed and audited.
	// This protects the consumer (who is charged the billable figure) as much as the fisc.
	// The record was fetched and its Station bound to the request above; reuse it. The ceiling
	// is read from the grant against rec.StationID - the Station the grant actually names -
	// which equals req.StationID by the gate above, so no attacker-chosen value reaches it.
	//
	// EXACT, not coarse: billable usage and the grant ceiling are in the SAME unit - bytes.
	// The Station measures usage as len(bytes) in and out (internal/station/edge.go), precisely
	// because bytes are what both ends can count identically without sharing a tokenizer, and
	// the grant's MaxIn/MaxOut are byte ceilings. So clamping billable to the ceiling caps the
	// claim at exactly what was authorized - a Station is held to the last byte, not a loose
	// approximation. (The Station also refuses to PRODUCE past MaxOut at execution time; this is
	// the trustworthy backstop, since the Station is the party being paid.)
	model := rec.Model
	if maxIn, maxOut, cerr := dispatch.EdgeGrantCeiling(rec.Grant, ts.dispatchPub,
		link.PublicNetwork, rec.StationID); cerr == nil {
		if settled.Billable.In > maxIn || settled.Billable.Out > maxOut {
			log.Printf("edge settle: attempt %s billable (%d/%d) exceeds grant ceiling (%d/%d) - clamped and disputed",
				req.AttemptID, settled.Billable.In, settled.Billable.Out, maxIn, maxOut)
			settled.Billable.In = min(settled.Billable.In, maxIn)
			settled.Billable.Out = min(settled.Billable.Out, maxOut)
			disputed = true
		}
	} else {
		// A grant we stored that will not yield its own ceiling should never happen - openEdgeAttempt
		// always stores a valid Core-signed grant - so reaching here means either our own bug or a
		// tampered record. We do NOT trap the operator's pay behind it (the settlement still commits
		// on the unclamped figure, the safe direction for an honest operator caught by our fault),
		// but we refuse to let an unbounded figure through UNFLAGGED: it is marked disputed and
		// force-audited below, so a human sees every settlement whose bound we could not apply. A
		// money bound that cannot be checked is a money bound that gets a second look, not a pass.
		log.Printf("edge settle: attempt %s grant ceiling unreadable (%v) - billable NOT clamped, flagged for audit", req.AttemptID, cerr)
		disputed = true
	}
	// TOKEN CEILING CLAMP (Option C). The per-token figure a node is paid on must not exceed
	// what Core authorized in the grant, exactly as the byte figure is clamped above. A ceiling
	// of 0 means "no token ceiling" (a byte-only grant, or one minted before token billing) - in
	// that case the token figure is left to the byte cap + audit, NOT clamped to zero. This lands
	// the clamp BEFORE any node populates a real token claim, so an unclamped raw operator claim
	// can never reach the money path. Reads the token ceiling from the SAME signed grant.
	if maxTokIn, maxTokOut, terr := dispatch.EdgeGrantTokenCeiling(rec.Grant, ts.dispatchPub,
		link.PublicNetwork, rec.StationID); terr == nil {
		if maxTokIn > 0 && settled.BillableTokens.In > maxTokIn {
			settled.BillableTokens.In = maxTokIn
			disputed = true
		}
		if maxTokOut > 0 && settled.BillableTokens.Out > maxTokOut {
			settled.BillableTokens.Out = maxTokOut
			disputed = true
		}
	} else {
		// Same safe direction as the byte ceiling above: a token ceiling we cannot read from a
		// grant we stored should never happen, but we do not trap the operator's pay behind it -
		// we flag it disputed and force-audit so an unclamped token figure never passes
		// unexamined once BillableTokens becomes money (P4+). Today this is implicitly covered
		// because the byte read fails on the same grant, but it must stand on its own.
		disputed = true
	}
	// THE TOWER'S WIRE ATTESTATION (P8; spec: "The Tower's wire count bounds what a Station
	// can bill"). The Tower cannot read the session, but it can WEIGH it: the sealed request
	// and sealed result it relayed are each at least as large as the plaintext they carry, so
	// the Tower's own byte counts are an upper bound on the Station's byte claim - and the
	// Tower is an independent party (it earns a % of gross, so understating shrinks its own
	// pay, and the clamp means overstating changes nothing). Absent/zero counts change
	// nothing; a Station claim above the attested wire is provably inflated - clamped and
	// disputed. Runs BEFORE tokens<=bytes so the tightened byte figure bounds tokens too.
	if req.WireIn > 0 && settled.Billable.In > req.WireIn {
		log.Printf("edge settle: attempt %s billable in %d exceeds the tower's wire count %d - clamped and disputed",
			req.AttemptID, settled.Billable.In, req.WireIn)
		settled.Billable.In = req.WireIn
		disputed = true
	}
	if req.WireOut > 0 && settled.Billable.Out > req.WireOut {
		log.Printf("edge settle: attempt %s billable out %d exceeds the tower's wire count %d - clamped and disputed",
			req.AttemptID, settled.Billable.Out, req.WireOut)
		settled.Billable.Out = req.WireOut
		disputed = true
	}
	// TOKENS <= BYTES, enforced with data Core already holds. A token is at least one byte, so
	// the byte figure - itself already clamped to the grant's byte ceiling above and re-checked
	// against the transcript at audit - is a hard upper bound on tokens. A token claim exceeding
	// the bytes actually served is provably inflated, so clamp it and dispute. This is the cheap,
	// attestation-free bound that lets token PRICING land without waiting on the full Tower
	// byte-attestation (which only tightens this, replacing the node's own byte claim with the
	// Tower's independent wire count). It runs AFTER the byte and token-ceiling clamps so both
	// figures are final.
	if settled.BillableTokens.In > settled.Billable.In {
		settled.BillableTokens.In = settled.Billable.In
		disputed = true
	}
	if settled.BillableTokens.Out > settled.Billable.Out {
		settled.BillableTokens.Out = settled.Billable.Out
		disputed = true
	}
	// A TOKEN-PRICED grant settled with NO token claim but nonzero bytes is anomalous: an
	// updated node always reports its model's usage, so a zero claim on real output is either
	// an old node or a node gaming the byte-fallback tariff (its cost is capped at the token
	// ceiling either way - see settleEdgeMoney - but the pattern deserves the audit's eyes,
	// like every other figure that smells of inflation).
	if pin, pout, perr := dispatch.EdgeGrantPricing(rec.Grant, ts.dispatchPub,
		link.PublicNetwork, rec.StationID); perr == nil && (pin > 0 || pout > 0) &&
		settled.BillableTokens.In == 0 && settled.BillableTokens.Out == 0 &&
		(settled.Billable.In > 0 || settled.Billable.Out > 0) {
		disputed = true
	}
	// THE FUNDING LEDGER, written after the one-use settlement has committed and keyed by this
	// attempt id, so it accrues exactly once however this request is retried or raced. The
	// amount is computed from the BILLABLE usage - now bounded by the grant ceiling above, and
	// itself the reconciled receipt/ack figure, never the Tower's own count. Owner comes from
	// the attachment record, not the message. This records what is OWED; nothing here moves money.
	b.accrueEarnings(ts, req.TowerID, at.Owner, model, rec.ConsumerKey, settled, now)
	// AND THE REAL WALLET: when edge traffic is priced, this captures the consumer's hold and
	// pays the Station owner and the Tower operator their shares through the same EarningLot
	// lifecycle as direct-node serving. Free (unpriced) traffic is a no-op here.
	b.settleEdgeMoney(ts, req.TowerID, req.StationID, at.Owner, rec, settled, now)
	if alreadySettled {
		// A replay or a completion of an interrupted settle. The MONEY above is idempotent and now
		// finished; we stop here rather than re-running the reputation/AUDIT steps below.
		// Audit selection is the one non-idempotent step: its Resolve deletes the wanted row when
		// the transcript arrives, so re-selecting would RE-OPEN a resolved audit and make a Tower
		// re-serve a transcript it already proved. The fresh settle recorded those; a replay must
		// not disturb them. The ATTEMPT CHAIN, however, IS walked (state-gated, so a completed
		// chain is untouched): a crash between the money committing and the chain events would
		// otherwise strand the ledger at `issued` forever while the wallet says settled, and
		// the courier's retry is exactly the call that can repair it. The one-use contract
		// still answers 409, which the courier treats as done.
		b.catchUpEdgeAttemptChain(req.AttemptID, receipt.ResponseDigest)
		jsonErr(w, http.StatusConflict, "this attempt has already been settled")
		return
	}
	// The attempt chain hears about it AFTER the store's answer is final, mirroring the
	// relayed path: evidence first, then the settlement commitment.
	b.catchUpEdgeAttemptChain(req.AttemptID, receipt.ResponseDigest)
	// AND THE REPUTATION LEDGER, so the RATE this Tower is judged on reflects this attempt.
	// The outcome is a fact about what settled; whether the rate warrants action is decided
	// separately, in evaluateTower, on evidence this records.
	outcome := reputation.Uncorroborated
	if settled.Corroborated {
		outcome = reputation.Corroborated
	}
	if disputed {
		// A signal, not a sentence: the dispute RATE is what an evaluation reads. One dispute
		// is a consumer who may be lying; a Tower's unusual dispute rate is a Tower to look at.
		outcome = reputation.Disputed
	}
	b.recordOutcome(req.TowerID, req.AttemptID, outcome)
	// A sampled fraction is selected for post-hoc content review, and a DISPUTED attempt is
	// audited regardless of the sample - the transcript is the closest look available at
	// whether the Station is self-consistent about the bytes it signed. The digests AND the
	// Station's CLAIMED usage come from the receipt just verified: the audit re-checks that
	// claim against the true length of the transcript bytes, which is what catches an
	// unacknowledged attempt whose operator inflated its own usage_out.
	if disputed {
		b.forceAudit(req.TowerID, req.StationID, req.AttemptID,
			receipt.RequestDigest, receipt.ResponseDigest, receipt.Usage.In, receipt.Usage.Out)
	} else {
		b.selectForAudit(req.TowerID, req.StationID, req.AttemptID,
			receipt.RequestDigest, receipt.ResponseDigest, receipt.Usage.In, receipt.Usage.Out)
		// THE ADAPTIVE LAYER (spec: "The audit rate adapts to the evidence"): a fresh
		// Station or an anomalous recent history elevates this settlement's selection odds
		// beyond the deterministic sample - by an unpredictable coin, so a tower cannot
		// compute which attempts are watched. Skipped when the baseline already selected.
		if !auditSampled(req.AttemptID) {
			b.adaptiveAudit(req.TowerID, req.StationID, req.AttemptID,
				receipt.RequestDigest, receipt.ResponseDigest, receipt.Usage.In, receipt.Usage.Out)
		}
	}
	// Judged AFTER the outcome is recorded, so this attempt is in the window. The verdict may
	// quarantine the Tower on strong evidence; it never touches THIS settlement, which has
	// already committed - the money is decided, the reputation is a separate consequence.
	b.evaluateTower(req.TowerID)
	writeJSON(w, http.StatusOK, map[string]any{
		"attempt_id":   settled.AttemptID,
		"corroborated": settled.Corroborated,
		"disputed":     disputed,
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
// towerOperatorAccount resolves a Tower to its operator's WALLET account - the hex account key
// (owner pubkey) the earnings/payout surface is keyed on (EarningSplitOf/RequestPayout use
// o.Pubkey). This bridge exists because the Tower registry stores its owner as an account LOGIN
// or a derived u_ id (towerOperator returns o.Login or UserIDFromPubkey), never the hex pubkey
// the wallet lifecycle needs. A compensated operator has a verified account, so the login
// resolves; if it cannot be resolved to a wallet account, the Tower earns nothing (logged) rather
// than crediting a guess.
func (b *broker) towerOperatorAccount(towerID string) (string, bool) {
	ts := b.tower
	if ts == nil {
		return "", false
	}
	tw, ok := ts.registry.Get(towerID)
	if !ok || tw.Owner == "" {
		return "", false
	}
	// Already a wallet account key (an owner pubkey the store knows)?
	if o, found, err := b.db.OwnerByPubkey(tw.Owner); err == nil && found && !o.Anonymized {
		return o.Pubkey, true
	}
	// The usual case: the owner is a login; resolve it to the account's pubkey.
	if o, found, err := b.db.OwnerByLogin(tw.Owner); err == nil && found && !o.Anonymized && o.Pubkey != "" {
		return o.Pubkey, true
	}
	return "", false
}

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

// accrueEarnings records what the Station's operator is owed for one settled attempt.
//
// It runs AFTER the one-use settlement has committed, so the attempt it accrues for really
// executed exactly once. The write is idempotent on the attempt id, so a retried or raced
// settle accrues once regardless. A failure is logged, not returned: the money is decided by
// the settlement that already committed, and the amount is a pure function of the billable
// usage stored with the receipt, so a dropped accrual under-pays (the safe direction) and can
// be re-derived later - it can never be double-counted or invented.
//
// The amount is computed from settled.Billable - the reconciled receipt/ack usage - never from
// anything the relaying Tower put in a message. Nothing here moves money.
func (b *broker) accrueEarnings(ts *towerSubsystem, towerID, owner, model string, consumerKey []byte, settled dispatch.Settlement, at time.Time) {
	if ts == nil || ts.earnings == nil || owner == "" {
		return
	}
	// SELF-DEALING: an operator routing their OWN traffic through their OWN Station to farm a
	// revenue share on their own spend. Core cannot distinguish a fabricated attempt from a real
	// one cryptographically - a colluding consumer account signs a perfectly good ack over real
	// model output - so the defence is at the ACCOUNT level: if the consumer and the Station's
	// owner are the same account, the attempt earns nothing. The row is still recorded (the usage
	// is evidence), just excluded from what is owed. This catches the same-account case; sybil
	// accounts funded from one source are caught by the funded-work and linkage checks that
	// belong to the revenue-share program, not here.
	selfDealing := len(consumerKey) > 0 && b.sameAccount(hex.EncodeToString(consumerKey), owner)
	if selfDealing {
		log.Printf("tower %s: attempt %s is self-dealing (consumer owns the Station) - recorded, not owed",
			towerID, settled.AttemptID)
	}
	micros := edgeAccrualMicros(settled.Billable.In, settled.Billable.Out)
	if err := ts.earnings.Accrue(earnings.Accrual{
		TowerID: towerID, Owner: owner, AttemptID: settled.AttemptID, Model: model,
		UsageIn: settled.Billable.In, UsageOut: settled.Billable.Out, Micros: micros,
		Corroborated: settled.Corroborated, SelfDealing: selfDealing, At: at,
	}); err != nil {
		log.Printf("tower %s: could not accrue earnings for %s: %v", towerID, settled.AttemptID, err)
	}
}

// settleEdgeMoney bills a Tower-relayed attempt through the SHARED wallet - the same
// EarningLot/payout/chargeback machinery that pays direct nodes - and splits it: the consumer is
// charged, the serving Station's owner earns cost*(1-fee), and the relaying Tower's operator
// earns its share of the platform's margin (cost*fee*towerRate, the founder-approved 10%). Both
// credits are clawed back together if the request is refunded. FREE (unpriced) edge traffic does
// nothing here - billing turns on only when a per-byte edge price is configured.
func (b *broker) settleEdgeMoney(ts *towerSubsystem, towerID, stationID, stationOwner string, rec dispatch.Record, settled dispatch.Settlement, now time.Time) {
	// TOKEN-PRICED FIRST (Option C). The price was pinned into the Core-signed grant at
	// authorize, so settlement honors it REGARDLESS of the env byte-tariff switch - exactly the
	// lock-price property the direct path has. Billable tokens have already been clamped to the
	// grant token ceiling and the tokens<=bytes bound above, so the figure is safe to price.
	if pin, pout, perr := dispatch.EdgeGrantPricing(rec.Grant, ts.dispatchPub,
		link.PublicNetwork, rec.StationID); perr == nil && (pin > 0 || pout > 0) {
		if settled.BillableTokens.In > 0 || settled.BillableTokens.Out > 0 {
			cost := tokenCostCredits(settled.BillableTokens.In, settled.BillableTokens.Out, pin, pout)
			b.captureEdgeCharge(towerID, stationID, stationOwner, rec.ConsumerKey, settled.AttemptID,
				rec.Model, cost, settled.BillableTokens.In, settled.BillableTokens.Out, now)
			return
		}
		// A token-priced grant whose node signed NO token claim (an old node): bill the byte
		// tariff, but CAPPED at what the token ceilings would have cost at the pinned price -
		// otherwise a node whose token price is LOW could zero its claim to be paid the higher
		// platform byte rate (the arbitrage the audit flagged). And ALWAYS capture, even with
		// the byte tariff off: a token-priced attempt always placed a hold, and capturing at
		// zero refunds it immediately rather than stranding it until the sweep.
		byteCost := edgePriceCredits(settled.Billable.In, settled.Billable.Out)
		if maxTokIn, maxTokOut, terr := dispatch.EdgeGrantTokenCeiling(rec.Grant, ts.dispatchPub,
			link.PublicNetwork, rec.StationID); terr == nil {
			if ceilingCost := tokenCostCredits(maxTokIn, maxTokOut, pin, pout); byteCost > ceilingCost {
				byteCost = ceilingCost
			}
		}
		b.captureEdgeCharge(towerID, stationID, stationOwner, rec.ConsumerKey, settled.AttemptID,
			rec.Model, byteCost, settled.Billable.In, settled.Billable.Out, now)
		return
	}
	if !edgePricingOn() {
		// Edge pricing is off, so no holds were placed and there is nothing to capture. We
		// deliberately do NOT call SettleEdge here: it would claim a consumer receipt for free
		// traffic that was never billed. If pricing was toggled OFF between this attempt's
		// authorize and settle (a live env change without a restart - rare, self-inflicted), its
		// hold is not captured here; the pending-hold sweep refunds it, so the consumer is made
		// whole (the operator simply earns nothing on that in-flight attempt).
		return
	}
	// SettleEdge charges against the AUTHORIZE-TIME reservation, using its exact recorded amount
	// and no-op-ing if none exists - so we do not recompute or pass a held figure here, and a
	// swept hold or a changed price cannot produce a wrong refund. We still call it at cost 0 (an
	// empty result while pricing remains on): that CAPTURES the hold at zero cost, refunding the
	// full reservation, rather than stranding it.
	// The byte-priced (blind / canary / probe) path prices the settled billable BYTES; the
	// token-priced branch above prices tokens at the grant's pinned rate. Both share the
	// split + wallet + SettleEdge logic in captureEdgeCharge.
	cost := edgePriceCredits(settled.Billable.In, settled.Billable.Out)
	b.captureEdgeCharge(towerID, stationID, stationOwner, rec.ConsumerKey, settled.AttemptID, rec.Model,
		cost, settled.Billable.In, settled.Billable.Out, now)
}

// edgeConsumerWallet resolves the ACCOUNT wallet to bill for an edge consumer key, so a
// relayed request draws from the SAME balance as a direct one (u_gh_/u_apple_/u_email_),
// not the device-key wallet. It resolves the owner behind the key and its account wallet;
// ok=false for a key not bound to a non-anonymized account (e.g. an ephemeral canary key),
// in which case nothing is billed. The authorize-time account gate already requires a bound
// account, so a real relayed request always resolves here.
func (b *broker) edgeConsumerWallet(consumerKey []byte) (string, bool) {
	o, ok, err := b.db.OwnerByPubkey(hex.EncodeToString(consumerKey))
	if err != nil || !ok {
		return "", false
	}
	return accountWalletForOwner(o)
}

// edgeShares splits an edge/relay charge of `cost` credits three ways, all fractions of
// GROSS (the founder-set model, 2026-08-13, overriding the earlier "share of net platform
// revenue" basis - see operator_revenue_share.feature):
//
//	station owner : 1 - feeRate            -> 70% at the default 30% fee (unchanged)
//	tower operator: edgeTowerRate()        -> 10% of GROSS, the relay cut
//	platform      : feeRate - towerRate    -> 20%, i.e. the platform ABSORBS the tower's
//	                                          cut out of its own margin, so a Station is
//	                                          never paid less because its traffic was relayed.
//
// The tower rate is capped at feeRate so the platform's share can never go negative.
func (b *broker) edgeShares(cost float64) (stationShare, towerShare float64) {
	tr := edgeTowerRate()
	if tr > b.feeRate {
		tr = b.feeRate
	}
	return cost * (1 - b.feeRate), cost * tr
}

// captureEdgeCharge bills a settled edge/relay attempt against the consumer's ACCOUNT
// wallet: it captures the authorize-time hold at `cost` credits (refunding the unused
// reservation, no-op if no hold exists), and mints the Station-owner and Tower-operator
// earning lots via the 70/10/20 split. inUnits/outUnits are recorded on the lineage
// receipt (bytes on the blind path, tokens on the overflow path). Shared by the
// byte-priced settle and the token-priced overflow path so both bill identically.
func (b *broker) captureEdgeCharge(towerID, stationID, stationOwner string, consumerKey []byte, attemptID, model string, cost float64, inUnits, outUnits int64, now time.Time) {
	wallet, ok := b.edgeConsumerWallet(consumerKey)
	if !ok {
		// Not a billable account (e.g. an ephemeral canary key). No hold was ever placed
		// against an account wallet, so there is nothing to capture and nothing to earn.
		return
	}
	stationShare, towerShare := b.edgeShares(cost)
	towerAcct, ok := b.towerOperatorAccount(towerID)
	if !ok {
		log.Printf("edge settle: attempt %s - Tower %s operator has no resolvable wallet account; Tower earns nothing",
			attemptID, towerID)
	}
	r := protocol.UsageReceipt{
		RequestID: attemptID, Model: model,
		PromptTokens: int(inUnits), CompletionTokens: int(outUnits), TS: now.Unix(),
	}
	// The Tower lot is tagged with a "tower:" node prefix so the earnings surface can tell a
	// Tower-RELAY share apart from a node-SERVING share for the same operator (the dashboard shows
	// them separately). It is provenance only - clawback and payout key on the account and request,
	// not the node - so the prefix changes no money.
	if _, err := b.db.SettleEdge(wallet, stationID, stationOwner, towerNode(towerID), towerAcct,
		cost, stationShare, towerShare, r); err != nil {
		log.Printf("edge settle: could not bill attempt %s: %v", attemptID, err)
	}
}

// sameAccount reports whether two user pubkeys belong to the same account. Two pubkeys are the
// same account if they are literally equal, or if they resolve to owner records that share a
// binding identity - the GitHub id, the Apple subject, or the login. A person may hold several
// device keys under one account, so comparing raw pubkeys alone would miss the operator who
// consumes on one key and runs a Station under another.
func (b *broker) sameAccount(pubA, pubB string) bool {
	if pubA == "" || pubB == "" {
		return false
	}
	if pubA == pubB {
		return true
	}
	oa, foundA, err := b.db.OwnerByPubkey(pubA)
	if err != nil || !foundA {
		return false
	}
	ob, foundB, err := b.db.OwnerByPubkey(pubB)
	if err != nil || !foundB {
		return false
	}
	switch {
	case oa.GitHubID != 0 && oa.GitHubID == ob.GitHubID:
		return true
	case oa.AppleSub != "" && oa.AppleSub == ob.AppleSub:
		return true
	case oa.Login != "" && oa.Login == ob.Login:
		return true
	}
	return false
}

// edgeAccrualMicros prices one attempt's billable usage.
//
// The rate is millionths of the settlement currency's minor unit per token, read from the
// environment so pricing is an operations decision rather than a code change, and defaulting to
// zero: the ledger records the billable usage on every attempt whatever the rate, so a rate set
// later re-prices the same stored inputs. Integer millionths keep accrual exact - no rounding
// error is ever carried forward.
//
// The arithmetic SATURATES rather than wraps. Billable is clamped to the grant ceiling before
// it reaches here, but the rate is an arbitrary non-negative int64 an operator sets, and a
// large rate times a large ceiling could overflow. A silent wrap would record a small, wrong
// debt an adversary could steer; saturating to MaxInt64 instead records an obviously-capped
// figure and logs it, so the misconfiguration is visible rather than exploitable. Inputs are
// non-negative (checkAccrual and envMicros both guarantee it), so MaxInt64 is the only bound
// that can be hit.
func edgeAccrualMicros(in, out int64) int64 {
	return satAdd(satMul(in, edgeRateMicrosPerTokenIn()), satMul(out, edgeRateMicrosPerTokenOut()))
}

// satMul multiplies two non-negative int64s, saturating at MaxInt64 instead of overflowing. The
// checked multiply is the canonical comp arithmetic; this is the accrual path's deliberate
// saturate-and-log wrapper, so one misconfigured rate records an absurd (visible) number rather
// than wedging settlement or wrapping to a small, steerable one.
func satMul(a, b int64) int64 {
	p, err := comp.CheckedMul(a, b)
	if err != nil {
		log.Printf("tower: accrual price overflowed (%d * %d); capped at MaxInt64", a, b)
		return math.MaxInt64
	}
	return p
}

// satAdd adds two non-negative int64s, saturating at MaxInt64.
func satAdd(a, b int64) int64 {
	sum, err := comp.CheckedAdd(a, b)
	if err != nil {
		log.Printf("tower: accrual sum overflowed (%d + %d); capped at MaxInt64", a, b)
		return math.MaxInt64
	}
	return sum
}

func edgeRateMicrosPerTokenIn() int64  { return envMicros("ROGERAI_TOWER_ACCRUAL_MICROS_IN") }
func edgeRateMicrosPerTokenOut() int64 { return envMicros("ROGERAI_TOWER_ACCRUAL_MICROS_OUT") }

func envMicros(name string) int64 {
	v := os.Getenv(name)
	if v == "" {
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		log.Printf("tower: %s is not a non-negative integer (%q); pricing that side at zero", name, v)
		return 0
	}
	return n
}

// edgeTowerRateDefault is the Tower operator's share of GROSS on a relayed attempt - the
// founder-set 10% (2026-08-13, overriding the earlier "share of net platform revenue" basis).
// Overridable by config; the cut comes out of the PLATFORM's margin (its fee drops from 30%
// to 20% at the default), never the serving Station's 70% share. Capped at feeRate in
// edgeShares so the platform's residual can never go negative.
const edgeTowerRateDefault = 0.10

// Edge prices are expressed as CREDITS PER MILLION BYTES (1 credit = $1), mirroring the direct
// path's $/1M tokens so the two surfaces read alike. Billing is ON by default at these rates; an
// operator overrides them with ROGERAI_TOWER_EDGE_PRICE_IN/OUT (also credits per 1M bytes) or
// sets both to 0 to make edge traffic free again. The defaults approximate a mid-range model
// (~$0.50/1M tokens) at roughly four bytes per token, kept deliberately modest.
const (
	defaultEdgePricePerMBIn  = 0.05 // credits per 1,000,000 input bytes
	defaultEdgePricePerMBOut = 0.15 // credits per 1,000,000 output bytes
)

// tokenCostCredits prices token usage in consumer credits (1 credit = $1) at a pinned
// per-token price: prices are MICRO-USD PER 1,000,000 TOKENS, so
// cost = (tokens x priceMicros) / 1e6 [per-1M] / 1e6 [micros->USD]. Negative inputs (which
// every upstream guard already refuses) price as zero rather than minting negative money.
func tokenCostCredits(tokIn, tokOut, priceInMicros, priceOutMicros int64) float64 {
	if tokIn < 0 || tokOut < 0 || priceInMicros < 0 || priceOutMicros < 0 {
		return 0
	}
	return (float64(tokIn)*float64(priceInMicros) + float64(tokOut)*float64(priceOutMicros)) / 1e12
}

// edgePriceCredits prices an edge attempt's billable bytes in consumer credits. The consumer is
// charged this, the Station owner earns cost*(1-fee), and the Tower operator earns cost*fee*rate -
// all through the one wallet.
func edgePriceCredits(inBytes, outBytes int64) float64 {
	return (float64(inBytes)*edgeRatePerMB("ROGERAI_TOWER_EDGE_PRICE_IN", defaultEdgePricePerMBIn) +
		float64(outBytes)*edgeRatePerMB("ROGERAI_TOWER_EDGE_PRICE_OUT", defaultEdgePricePerMBOut)) / 1e6
}

// edgeRatePerMB reads a credits-per-1M-bytes rate from the environment, falling back to the given
// default. A malformed or negative value logs and falls back rather than pricing at zero silently.
func edgeRatePerMB(name string, def float64) float64 {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 || math.IsInf(f, 0) || math.IsNaN(f) {
		// Inf/NaN would poison every cost computed from this rate (NaN even slips past the
		// settle-time cost<=held clamp, whose comparison is false for NaN), so a non-finite
		// rate is refused exactly like a negative one.
		log.Printf("tower: %s is not a finite non-negative number (%q); using default %.4f", name, v, def)
		return def
	}
	return f
}

// edgePricingOn reports whether edge traffic is billed at all. With the non-zero defaults it is
// TRUE unless an operator sets both rates to 0. When off, no holds are placed and settlement does
// no wallet work; when on, every attempt holds at authorize and captures at settle (even a
// zero-cost capture releases the hold).
func edgePricingOn() bool {
	return edgeRatePerMB("ROGERAI_TOWER_EDGE_PRICE_IN", defaultEdgePricePerMBIn) > 0 ||
		edgeRatePerMB("ROGERAI_TOWER_EDGE_PRICE_OUT", defaultEdgePricePerMBOut) > 0
}

// edgeTowerRate is the Tower's fraction of GROSS, defaulting to 10% and clamped to [0,1] here
// (edgeShares further caps it at feeRate); a bad value falls back to the default rather than
// paying an absurd share.
func edgeTowerRate() float64 {
	v := os.Getenv("ROGERAI_TOWER_REVENUE_RATE")
	if v == "" {
		return edgeTowerRateDefault
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 || f > 1 {
		log.Printf("tower: ROGERAI_TOWER_REVENUE_RATE %q is not in [0,1]; using default %.2f", v, edgeTowerRateDefault)
		return edgeTowerRateDefault
	}
	return f
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

// subtleConstEq compares two keys in constant time and returns 1 on equal. A short-circuit
// bytes.Equal would leak, by timing, how much of an authorized consumer key an attacker has
// guessed - and the consumer key, while public, gates whose acknowledgement is accepted.
func subtleConstEq(a, b []byte) int {
	if len(a) != len(b) {
		return 0
	}
	return subtle.ConstantTimeCompare(a, b)
}

// catchUpEdgeAttemptChain walks a hub-path attempt's evidence chain to settled, entering at
// wherever the ledger currently stands (state-gated, so it is idempotent and safe on replays).
//
// On the hub path Core is BLIND between authorize and the settle receipt - there is no relay
// lease acceptance or grant claim for it to observe, so the attempt may still be `issued`
// when the receipt arrives, and the evidence event alone would be refused (the spec's
// exhaustive table deliberately has no issued->settled shortcut). The tower-forwarded,
// station-signed receipt IS the first proof the grant was accepted for dispatch on its bound
// session, so the chain walks the spec's own rows: dispatch accepted, then the evidence,
// then the settlement (terminal, so it records why it ended - the relayed path's rule).
//
// Called ONLY after the receipt has fully verified and the one-use settlement store has
// answered: the events record what Core observed, never what a caller merely claimed.
func (b *broker) catchUpEdgeAttemptChain(attemptID, evidenceHash string) {
	ts := b.tower
	if ts == nil || ts.attempts == nil {
		return
	}
	state, _, ok, err := ts.attempts.State(attemptID)
	if err != nil || !ok {
		return
	}
	if state == attempt.StateIssued {
		b.noteAttempt(attemptID, attempt.Observation{
			Kind: attempt.KindDispatchAccepted, EvidenceHash: evidenceHash,
		})
		state = attempt.StateLeased
	}
	if state == attempt.StateLeased || state == attempt.StateExecuting {
		b.noteAttempt(attemptID, attempt.Observation{
			Kind: attempt.KindEvidenceObserved, EvidenceHash: evidenceHash,
		})
		state = attempt.StateEvidenceComplete
	}
	if state == attempt.StateEvidenceComplete {
		b.noteAttempt(attemptID, attempt.Observation{
			Kind: attempt.KindSettlementCommitted, EvidenceHash: evidenceHash,
			Reason: "settled",
		})
	}
}
