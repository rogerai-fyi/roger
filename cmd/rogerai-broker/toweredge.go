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
	"math/rand"
	randv2 "math/rand/v2"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/towercore/admit"
	"rogerai.fm/roger/v6/internal/towercore/attempt"
	"rogerai.fm/roger/v6/internal/towercore/comp"
	"rogerai.fm/roger/v6/internal/towercore/dispatch"
	"rogerai.fm/roger/v6/internal/towercore/earnings"
	"rogerai.fm/roger/v6/internal/towercore/fleet"
	"rogerai.fm/roger/v6/internal/towercore/link"
	"rogerai.fm/roger/v6/internal/towercore/reputation"
	"rogerai.fm/roger/v6/internal/towerobj"
)

// maxEdgeSettleGrace is how long after the grant's deadline a receipt may still settle. The
// grant deadline bounds EXECUTION - the Station refuses work past it - but the receipt travels
// by a slower road: Station outbox, Tower collection, one more hop to Core. Evidence for work
// done in time must not fail because its courier ran on a schedule.
const maxEdgeSettleGrace = 10 * time.Minute

// minEdgeSettleGrace is the shortest courier window Core will ever allow. A grace under a minute
// would start refusing honest receipts for the ordinary cost of the road they travel - Station
// outbox, Tower collection, one hop to Core - so the derivation below is floored rather than
// allowed to shrink to nothing.
const minEdgeSettleGrace = time.Minute

// edgeSettleGrace keeps the settlement window strictly INSIDE the pre-auth hold's lifetime when
// edge billing is on. A consumer's hold is reclaimed by the orphan sweep after holdTTL; if the
// settlement window (grant lifetime + this grace) outran that, a late-but-valid receipt would
// find its hold already swept and settle for free with the operator unpaid. So the grace is
// capped a few minutes under holdTTL, so the hold always outlives the deadline it guards. With a
// generous holdTTL it is just maxEdgeSettleGrace; a short holdTTL shortens the courier window
// rather than silently losing the money.
//
// THE FLOOR BELOW IS WHY holdTTL HAS ONE. Once this clamps at minEdgeSettleGrace the derivation
// has stopped tracking holdTTL, and a small enough configured holdTTL made the window outrun the
// hold - the exact failure the derivation exists to prevent, reachable by setting one
// environment variable. holdTTL now refuses to be configured below the sum of the terms here;
// see minHoldTTL. The two constraints have to be read together, which is why each names the
// other.
func edgeSettleGrace() time.Duration {
	g := holdTTL() - 3*time.Minute // margin over the (sub-2m) grant lifetime
	if g > maxEdgeSettleGrace {
		g = maxEdgeSettleGrace
	}
	if g < minEdgeSettleGrace {
		g = minEdgeSettleGrace
	}
	return g
}

// minAttemptRetention is the floor under how long a settled or expired dispatch row is kept
// past its own deadline. It stands in for the derivation below when a deployment has disabled
// the hold sweep, where there is no last-moment-money-can-move to derive anything from.
const minAttemptRetention = 10 * time.Minute

// attemptRetention is how long a dispatch attempt row outlives its OWN deadline before the
// housekeeping sweep drops it, and it is deliberately not zero.
//
// dispatch.Registry.Reap's original comment argued the deadline made dropping safe "because
// nothing may settle after it anyway". That is true of a FRESH settlement and false of the
// repair beside it. Both attempt stores answer ErrAlreadySettled (and ErrAlreadyClaimed)
// BEFORE they answer ErrExpired, and towerEdgeSettle turns the first of those into
// `alreadySettled`, which exists precisely to re-run the idempotent wallet capture for a
// settlement that committed the one-use swap and then faulted before the money moved. That
// repair reads the row. Reaping at the instant of the deadline would delete it out from under
// the one retry that can still pay two operators for work that was really done - and the
// courier would get 404 "no such attempt", which towerjoin.SettleEdgeReceipt treats as
// permanent and abandons, rather than the 403 that says the window closed.
//
// It also keeps the 410 the epoch fence exists to send. epochFenceMoved fires off the row,
// before any deadline gate, and its log line is the only instrument in the tree counting what
// placement mobility costs. A row swept the moment its deadline passes turns a late courier's
// "this placement moved" into "no such attempt", which is the wrong sentence and an
// undercount of the one number §6.3b's rarity claim will be checked against.
//
// SO THE ROW IS KEPT UNTIL ITS MONEY CANNOT MOVE, which is when the consumer's pre-auth hold
// is reclaimed - holdTTL after authorize. The record's deadline is already authorize plus the
// attempt lifetime plus edgeSettleGrace(), and the settle-window test pins that sum strictly
// under holdTTL, so one further holdTTL past the deadline is comfortably past the hold. An
// upper bound rather than the exact subtraction on purpose: being a few minutes generous costs
// a few minutes of rows on a table that turns over in under ten, and being one second short
// costs an operator their pay.
func attemptRetention() time.Duration {
	if h := holdTTL(); h > minAttemptRetention {
		return h
	}
	return minAttemptRetention
}

// ackRetention is the same question for the acknowledgement table, answered from the attempt
// table rather than independently: an acknowledgement is only ever read by the settlement of
// the attempt it names, so it is dead exactly when that attempt's row is.
//
// Written as the sum of the terms rather than as a number, like minHoldTTL, so that changing
// any of them moves this. An ack recorded at R belongs to an attempt authorized at some A <= R
// whose row dies at A + towerAttemptLifetime + edgeSettleGrace() + attemptRetention(); since
// A <= R, reaping acks recorded before that many units ago can never outlive a row that is
// still answering settlements.
func ackRetention() time.Duration {
	return towerAttemptLifetime + edgeSettleGrace() + attemptRetention()
}

// edgeExecDeadline recovers the instant a dispatch record's WORK had to be finished by, which
// is not the instant the record carries.
//
// THE RECORD'S DEADLINE IS THE EVIDENCE CEILING. openEdgeAttempt writes
// `Deadline: g.Deadline.Add(edgeSettleGrace())` under its own comment - "the grant bounds
// execution, the record bounds evidence" - so a receipt is still admissible for minutes after
// the Station has stopped being allowed to serve. Reading that field as though it were the
// execution window is how the fence's deadline_open came to be a constant: the courier retries
// every fifteen seconds inside a settlement window measured in minutes, so essentially every
// firing found the EVIDENCE window open and logged true, and the one distinction the field was
// built to draw - a consumer still waiting versus a spool that caught up late - could not be
// drawn from it at all.
//
// SUBTRACTED RATHER THAN STORED, and the trade is worth naming. The exact alternative is a
// second timestamp on the dispatch row, which is a column, a migration and a parity obligation
// on both stores for a field that appears in one log line; the subtraction is exact whenever
// edgeSettleGrace() is what it was at authorize time, and edgeSettleGrace derives from
// ROGERAI_HOLD_TTL, which is deployment configuration and does not change under a live
// attempt's feet. If it ever does change mid-window the field is off by the delta for the
// attempts already in flight - a diagnostic reading slightly wrong for one settlement window,
// which is a different order of thing from the constant it replaces.
func edgeExecDeadline(rec dispatch.Record) time.Time {
	return rec.Deadline.Add(-edgeSettleGrace())
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
		// The tight per-IP bucket, on the way out. An unsigned caller here is by definition
		// never going to be served, so it is exactly the anon surface anonRL exists for
		// (tunnel.go applies it on the same condition), and hammering a 401 should cost the
		// hammerer something. A signed caller never reaches this and keeps its own per-account
		// bucket below.
		if allowed, retry := b.anonRL.allow(clientIP(r)); !allowed {
			w.Header().Set("Retry-After", strconv.Itoa(retry))
			jsonErr(w, http.StatusTooManyRequests, "rate limit exceeded - slow down")
			return
		}
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
	// Hold and capture MUST use the SAME wallet or funds move between pots. Both use the
	// consumer's ACCOUNT wallet (u_gh_/u_apple_/u_email_) - resolved here from the owner the
	// account gate already looked up - so a relayed request reserves from and bills the same
	// balance a direct request would, not the device-key wallet. Resolved BEFORE placement
	// because it is also the key both abuse bounds below are drawn on: one identity, one
	// bucket, one standing cap.
	consumerWallet, cwok := accountWalletForOwner(o)
	if !cwok {
		jsonErr(w, http.StatusForbidden, "tower inference requires a signed-in account that has accepted the terms of service")
		return
	}
	// THIS ENDPOINT WAS REGISTERED BARE, and it should never have been.
	//
	// Every comparable route on this broker passes through b.rl or b.anonRL (see the relay in
	// tunnel.go, /report, the audio surface); this one consulted neither, so the only cost of an
	// authorize was the signature. That was survivable while the relay fabric was opt-in behind
	// `roger share --tower`. It stopped being survivable when the flag was removed and the whole
	// signed-in fleet joined by default, because an authorize reserves a real station and the
	// number of stations reachable this way is now every share on the network.
	//
	// Keyed on the ACCOUNT wallet, not the device key: one identity, one bucket, so a caller
	// cannot multiply its rate by generating keypairs against the same account. Same discipline
	// the relay uses for a logged-in caller.
	if allowed, retry := b.rl.allow("edge:" + consumerWallet); !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		jsonErr(w, http.StatusTooManyRequests, "rate limit exceeded - slow down")
		return
	}
	// AND A STANDING CAP, which is the bound that actually matters here. The rate limiter
	// bounds how fast attempts are opened; nothing in it bounds how many stay open, and an
	// attempt pins a station for the grant's lifetime. See maxOpenEdgeAttemptsPerAccount.
	//
	// The slot is claimed HERE, before anything is minted, so a refusal costs the caller a 429
	// rather than an orphaned grant. It is released on every path that abandons the attempt,
	// and handed to edgeEnterInflight - which owns it from then until settle or expiry - on the
	// one path that does not.
	if !b.edgeAccountReserve(consumerWallet) {
		w.Header().Set("Retry-After", "5")
		jsonErr(w, http.StatusTooManyRequests,
			"too many edge attempts open on this account at once - finish or abandon some before opening more")
		return
	}
	slotHeld := true
	defer func() {
		if slotHeld {
			b.edgeAccountRelease(consumerWallet)
		}
	}()
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

	// REQUIRED since the leaf-station generation retired: the only surviving executor
	// (ServeSealed) refuses a grant without a consumer envelope key, so authorizing one
	// would take the consumer's hold for a guaranteed refusal - a stranded hold and a
	// burned attempt, not a serve (P9 audit H3).
	if req.ConsumerEnvKey == "" {
		jsonErr(w, http.StatusBadRequest, "consumer_env_key is required: the edge path is sealed end-to-end, "+
			"and the answer is encrypted to this key - without one nothing can serve you")
		return
	}
	consumerEnvKey, derr := hex.DecodeString(req.ConsumerEnvKey)
	if derr != nil || len(consumerEnvKey) != 32 {
		jsonErr(w, http.StatusBadRequest, "consumer_env_key must be a hex-encoded 32-byte X25519 public key")
		return
	}

	target, row, ok := b.edgeTargetFor(req.Model, edgePlacementRand(), nil)
	endpoint, endpointPin := row.Endpoint, row.TLSSPKI
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
	// THE STATION IS NOW RESERVED, and placement has to know. This is the edge path's half of
	// the bracket the relayed path gets for free from its dispatch loop: from here until a
	// receipt arrives (or the grant's own deadline passes) the node is expected to be carrying
	// this work, and the load divisor every candidate is scored through is only meaningful if
	// somebody says so. After the record and the hold, so nothing counts an attempt that was
	// never handed out. It counts on the EDGE counter only - see edgeEnterInflight for why a
	// reservation nobody has submitted against must not touch the paid router's number.
	b.edgeEnterInflight(g.AttemptID, row.NodeID, consumerWallet, g.Deadline)
	slotHeld = false // the ledger entry owns the account slot now; it releases it at exit
	writeJSON(w, http.StatusOK, map[string]any{
		"attempt_id": g.AttemptID,
		"grant":      base64.StdEncoding.EncodeToString(g.Signed),
		"relay_name": g.RelayName,
		// Where to CONNECT: the Tower's data plane, as the Tower itself advertised on its
		// link. The Station's own address appears nowhere - reachability is the Tower's
		// contribution, and hiding the Station is part of what the operator provides.
		"endpoint": endpoint,
		// AND WHAT MUST ANSWER THERE. The hub certificate pin the Tower advertised beside that
		// address: with it the consumer dials https and accepts exactly that certificate, and
		// without it (an older tower, or one whose operator has not turned TLS on) it dials
		// plain http exactly as it always has. It is the SAME string the serving node is given
		// at attach, from the same session, so the two ends of one hub cannot end up disagreeing
		// about whether it speaks TLS.
		"endpoint_tls_spki": endpointPin,
		"deadline":          g.Deadline.Unix(),
		"max_in":            g.MaxIn,
		"max_out":           g.MaxOut,
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
		// THE NOTE USED TO SAY "connect to endpoint with TLS server name relay_name", which
		// described the RETIRED TLS-splice relay and was false of this path for its whole life:
		// the sealed hub is plain HTTP unless the tower advertises a pin, and relay_name is a
		// label inside the grant, not a server name anybody presents. A client that believed it
		// would have been trying to negotiate SNI against an http listener.
		"note": "submit to endpoint over https, accepting only the certificate whose public key " +
			"hashes to endpoint_tls_spki (plain http when it is empty); send the grant in the " +
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
//
// rng is the placement's randomness. Pass nil for a reproducible top-1 (tests, and the
// single-candidate case where there is nothing to choose between anyway).
// exclude names Towers already tried (and failed) within one bridged request, so the
// tower-to-tower fallback never redials the relay that just dropped the work. Nil for
// every single-shot caller.
func (b *broker) edgeTargetFor(model string, rng *rand.Rand, exclude map[string]bool) (dispatch.Target, fleet.Station, bool) {
	ts := b.tower
	if ts == nil || ts.routable == nil {
		// SAID OUT LOUD, like every other refusal on this path. This one used to return in
		// silence, so "one line per refusal" - the property logEdgePlacementRefusal exists to
		// give - had a hole in exactly the case that produces no other symptom either: a broker
		// with the tower subsystem unconfigured refuses every edge consumer, forever, and looks
		// from the outside identical to an empty fleet.
		b.logEdgePlacementRefusal(model, 0, 0, 0,
			"this broker has no tower subsystem, so it can place nothing on the edge fabric")
		return dispatch.Target{}, fleet.Station{}, false
	}
	rows, err := ts.routable.Candidates(model, time.Now())
	if err != nil {
		log.Printf("edge authorize: cannot read the routable fleet: %v", err)
		return dispatch.Target{}, fleet.Station{}, false
	}
	// RANK, DO NOT TAKE THE FIRST (M1 of docs/relay-selection-design.md).
	//
	// This loop used to return rows[0]. That was not "first-fit" so much as arbitrary: the
	// projection query had no ORDER BY and the memory store ranged a map, so the same fleet
	// could answer two identical requests differently, and a strong station and a failing one
	// were equally likely to win. It stayed that way because there was nothing to rank BY -
	// probes record against the broker node id and a row is keyed by station id, with no name
	// in common until the M0 join.
	//
	// There is now. Candidates arrive in a stable order, and each carries the node id, so a
	// row can be scored on what was actually measured.
	//
	// AND RANKING ALONE IS NOT ENOUGH, which is the correction M1 needed. A pure
	// highest-score-wins over a stable order is a permanent magnet: the same station wins
	// every identical request until its own load drags it down, and until edge work counted
	// against that load (edgeEnterInflight, below) nothing ever dragged. The classic router
	// has answered this since spec 1.5 with power-of-two-choices, and this path simply omitted
	// it. It is the same selectP2C over the same scoredCand shape - one anti-magnet mechanism
	// for the whole product, not a second one invented here.
	//
	// PASS ONE is the AUTHORITY re-check, which needs no broker lock: is this row servable at
	// all. Pass two (edgeEligible) is everything the broker knows about the NODE behind the
	// row, and it runs under one acquisition of the two locks for the whole fleet - see there
	// for why that matters.
	//
	// The owner-ban set is resolved BEFORE either lock, exactly as pickFor does it: it may
	// consult the account binding cache, and doing that under metricsMu would serialize every
	// placement behind a store round-trip per candidate.
	//
	// # AND PASS ONE USED TO COST 2N DATABASE ROUND TRIPS
	//
	// It ran MayTakeWork and targetFor per candidate, each a single-row SELECT, serialized, on
	// the consumer's critical path - so a model with thirty routable Stations meant sixty-one
	// queries before a placement could be made. Neither was cached and both were being asked
	// the same small number of distinct questions over and over.
	//
	// The reason that is urgent rather than merely slow is WHICH POOL it spends. internal/store's
	// poolLimits caps maxOpen at 8 because production is a small shared managed Postgres with
	// about twenty-two usable backends across every app, and the tower subsystem is handed that
	// same *sql.DB - so these queries queue behind, and ahead of, the wallet reads, holds and
	// settlements. Under concurrent authorize load the observable failure is not slow routing.
	// It is payment timeouts.
	//
	// Both are now bounded by the number of TOWERS rather than the number of Stations:
	//
	//   - MayTakeWork is memoized for the duration of one placement. A tower's eligibility is a
	//     property of the tower, and asking twice within one placement can only ever get the
	//     same answer - or a DIFFERENT one, which would be worse: two rows compared across two
	//     instants of the same tower's lease is a ranking of a fleet state that never existed.
	//     The fleet has one to ten towers, so this is at most ten queries and usually one.
	//   - The attachment re-check is ONE query for the whole shortlist (attach.ByStations,
	//     `WHERE station_id = ANY($1)`), replacing N.
	//
	// The alternative considered and rejected was to score first and resolve only the winner,
	// looping down the drawn order on a miss. It is sound - the loop is what makes it sound,
	// since a single shot would 503 whenever the top pick happened to be stale - but it changes
	// the draw distribution whenever a drawn candidate turns out to be unresolvable, and it
	// costs an extra round trip every time that happens. The batch read reaches the same query
	// count with the filter-then-rank semantics exactly as they were, so there is no reordering
	// argument to make and none to get wrong later.
	bannedNode := b.bannedOwnerNodeSet()
	mayTakeWork := make(map[string]bool, 4)
	shortlist := make([]fleet.Station, 0, len(rows))
	for _, row := range rows {
		if exclude[row.TowerID] {
			// Already tried and failed within this bridged request: the tower-to-tower
			// fallback must never redial the relay that just dropped the work.
			continue
		}
		if row.Endpoint == "" {
			continue
		}
		// Only SELF-ATTACHED (hub) rows are servable. A legacy leaf row can linger in the
		// projection until it expires or its tower republishes; handing its endpoint to a
		// consumer would authorize a hold against a plane nothing serves (P9 audit H3).
		if !strings.HasPrefix(row.OfferID, "self-") {
			continue
		}
		may, asked := mayTakeWork[row.TowerID]
		if !asked {
			may = ts.registry.MayTakeWork(row.TowerID)
			mayTakeWork[row.TowerID] = may
		}
		if !may {
			continue
		}
		shortlist = append(shortlist, row)
	}
	if len(shortlist) == 0 {
		reason := "no Tower publishes a routable Station for this model"
		if len(rows) > 0 {
			reason = "every routable row is a legacy offer, has no data plane, or sits behind a Tower that may not take work"
		}
		b.logEdgePlacementRefusal(model, len(rows), 0, 0, reason)
		return dispatch.Target{}, fleet.Station{}, false
	}
	keep, targets := b.resolveEdgeCandidates(shortlist)
	if len(keep) == 0 {
		b.logEdgePlacementRefusal(model, len(rows), len(shortlist), 0,
			"no candidate survived the attachment re-check")
		return dispatch.Target{}, fleet.Station{}, false
	}
	tierA, tierB := b.edgeEligible(keep, bannedNode, time.Now())
	// Healthy beats failing as an absolute gate, and Tier B exists so a transient blip never
	// blanks the fleet - pickFor's own two-tier shape, for the same reason.
	pool, tier := tierA, "A"
	if len(pool) == 0 {
		pool, tier = tierB, "B"
	}
	if len(pool) == 0 {
		b.logEdgePlacementRefusal(model, len(rows), len(shortlist), len(keep),
			"every resolvable candidate's node is stale, banned or on a private band")
		return dispatch.Target{}, fleet.Station{}, false
	}
	// edgeBeta concentrates the sampling on the strong end of the band. A tie, or a nil rng,
	// still resolves to the first row of a total order, so "same fleet, same answer" survives
	// wherever it was true before.
	chosen := selectP2C(pool, edgeBeta, rng)
	if chosen < 0 {
		b.logEdgePlacementRefusal(model, len(rows), len(shortlist), len(keep),
			"the selector drew nothing from a non-empty pool")
		return dispatch.Target{}, fleet.Station{}, false
	}
	b.logEdgePlacement(model, keep[chosen], pool, tier, chosen, len(rows), len(shortlist))
	// The whole ROW rides back: the endpoint the consumer submits to, and the attachment's
	// listed price that authorize pins into the grant.
	return targets[chosen], keep[chosen], true
}

// resolveEdgeCandidates re-checks a shortlist against the attachment registry - the authority
// on whether a Station may be dispatched to at all - in ONE read for the whole list.
//
// The rule it applies is targetFromAttachment's, not a copy of it: liveness, the origin tower,
// and both keys being usable. What changes here is only how the attachments are fetched. A row
// whose attachment has vanished, been rehomed or been retired since the projection was written
// is dropped, exactly as the per-row read dropped it, so an unresolvable candidate still never
// wins a draw and never even enters one.
func (b *broker) resolveEdgeCandidates(shortlist []fleet.Station) ([]fleet.Station, []dispatch.Target) {
	if len(shortlist) == 0 {
		return nil, nil
	}
	ts := b.tower
	if ts == nil || ts.stationStore == nil {
		return nil, nil
	}
	ids := make([]string, 0, len(shortlist))
	for _, row := range shortlist {
		ids = append(ids, row.StationID)
	}
	// The STORE rather than the Registry, which is what towerSubsystem.stationStore is kept for:
	// the Registry deliberately exposes admission and single lookups, and this is neither. The
	// only thing the Registry adds on this read is an error wrapper nothing here reads.
	ats, err := ts.stationStore.ByStations(ids)
	if err != nil {
		// FAIL CLOSED, and loudly. Losing this read means Core cannot check who any of these
		// Stations are, and dispatching to a Station whose recorded key we could not read means
		// accepting a receipt we cannot verify - the same reason the per-row read refused on an
		// error. The consumer gets the ordinary "not here, not now".
		log.Printf("edge placement: cannot read %d candidate attachment(s): %v", len(ids), err)
		return nil, nil
	}
	keep := make([]fleet.Station, 0, len(shortlist))
	targets := make([]dispatch.Target, 0, len(shortlist))
	for _, row := range shortlist {
		at, found := ats[row.StationID]
		if !found {
			continue
		}
		target, ok := targetFromAttachment(row.TowerID, row.StationID, row.Model, row.Modality, at)
		if !ok {
			continue
		}
		keep = append(keep, row)
		targets = append(targets, target)
	}
	return keep, targets
}

// logEdgePlacement says where a consumer was sent, and on what evidence.
//
// # THERE WAS NO OBSERVABILITY ON PLACEMENT AT ALL
//
// Not the chosen station, not the candidate count, not the score. That is a bad property for
// any routing decision and a specific hazard for this one, because the failure mode this path
// has ALREADY had once - every request collapsing onto the lexicographically first station,
// with the other operators earning nothing - produces no error, no timeout and no unhappy
// consumer. The requests are served. Had it recurred, the first report would have come from an
// operator noticing their machine had stopped earning, if it came at all.
//
// So the line carries what is needed to see that shape in an aggregator: which station won,
// which tower carries it, how many candidates there were, how many survived each gate, and the
// score and load the decision actually turned on. Counting placements per station over a window
// is then a query rather than a new subsystem.
//
// # WHAT IS IN IT, AND WHAT IS DELIBERATELY NOT
//
// Station, tower and node ids are already in this broker's logs - the price-refusal log two
// hundred lines up prints a tower and a station, `station %s revoked by %s` prints one beside
// an owner, and probe.go prints node ids on every probe - so this is not a new exposure class.
// The CONSUMER is not here: no account, no wallet, no attempt id. Placement is a supply-side
// decision and there is no operational question it answers that needs to name the customer.
//
// Not sampled. One line per authorize is one line per paid inference request on a fabric whose
// authorize rate is bounded per account by b.rl and whose standing attempts are capped at 32,
// and it is the same order of volume as the probe lines already emitted. If edge volume ever
// makes that untrue the fix is a sampler here, not a quieter line.
func (b *broker) logEdgePlacement(model string, row fleet.Station, pool []scoredCand, tier string, chosen, candidates, shortlisted int) {
	score, load := 0.0, 0.0
	for _, c := range pool {
		if c.idx == chosen {
			score, load = c.score, c.load
			break
		}
	}
	log.Printf("edge placement model=%s station=%s tower=%s node=%s tier=%s score=%.4f load=%.0f candidates=%d servable=%d eligible=%d",
		model, row.StationID, row.TowerID, row.NodeID, tier, score, load, candidates, shortlisted, len(pool))
}

// logEdgePlacementRefusal says why nothing could be placed.
//
// The 503 a consumer sees is deliberately uninformative - "not here, not now", because
// enumerating which Towers exist is nobody's business - and it was uninformative to US as well:
// a bare jsonErr with no log line, so an operator seeing edge traffic dry up had no way to tell
// an empty fleet from a fleet that was entirely banned, entirely stale, or entirely
// unresolvable. Those want completely different responses and looked identical.
//
// The counts are the diagnosis: candidates is what the projection offered, servable is what
// survived the row-shape and tower-eligibility gates, resolvable is what still had a live
// attachment, and the reason names the gate that emptied the pool.
func (b *broker) logEdgePlacementRefusal(model string, candidates, shortlisted, resolvable int, reason string) {
	log.Printf("edge placement model=%s REFUSED candidates=%d servable=%d resolvable=%d - %s",
		model, candidates, shortlisted, resolvable, reason)
}

// edgeEligible is the edge path's half of pickFor's eligibility pass: given the rows that
// survived the authority re-check, decide which nodes behind them may take work at all, and
// score the survivors.
//
// # WHY THIS EXISTS
//
// edgeTargetFor filtered on three things - a non-empty endpoint, a self- offer, and a Tower
// that may take work - all of them properties of the TOWER or the PROJECTION ROW. Nothing
// asked whether the machine on the other end was alive, banned, or private. That was survivable
// while the relay fabric was opt-in behind a flag and mostly empty. It is not survivable now
// that every signed-in `roger share` joins it, because the M0 join means row.NodeID reaches
// every one of those answers and there is no excuse left for not asking.
//
// Two holes in particular were live. MayEnroll is checked at ATTACH TIME ONLY, so a ban applied
// afterwards - which is how the fraud pipeline actually works, since a ban follows evidence -
// left the node taking paid edge traffic and accruing earnings under a banned account. And
// nothing ever marks an attachment detached: publishRoutable republishes every live attachment
// on every sweep, so a machine that ran `roger share` once and pressed Ctrl-C stayed a routable
// candidate indefinitely, with its last trust score frozen beside it.
//
// # HARD DROPS VERSUS GRADED HEALTH
//
// Liveness and bans are HARD. Routing to a node that is not there is not degraded availability,
// it is a guaranteed timeout, a stranded consumer hold and a burned attempt - strictly worse for
// the consumer than "no Station can take this right now". A ban is a decision that has already
// been made and must not be re-litigated by a placement function.
//
// Probe health is GRADED, and this is a deliberate divergence from pickFor, which drops a
// probe-dead node outright. The classic fleet is large enough that dropping its sick members
// leaves a fleet; the edge fleet routinely has a handful of stations for a model, and a probe
// streak measured broker-to-node says less about a path that avoids the broker entirely. So a
// probe-troubled node falls to Tier B and is used only when Tier A is empty.
//
// # ONE LOCK ACQUISITION, ONE INSTANT
//
// Every candidate is read under a single hold of b.mu then metricsMu (that order - b.mu outer,
// metricsMu inner, as enrichOffersForNode in market.go establishes). The previous code called
// edgeCandidateScore and edgeCandidateLoad per candidate and each took metricsMu for itself, so
// a row's quality and its load came from two different instants and two rows came from four -
// which is a ranking of a fleet state that never existed. It is also N times the lock traffic
// on a path that runs per request.
func (b *broker) edgeEligible(rows []fleet.Station, bannedNode map[string]bool, now time.Time) (tierA, tierB []scoredCand) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.metricsMu.Lock()
	defer b.metricsMu.Unlock()
	for i, row := range rows {
		nodeID := row.NodeID
		// NO JOIN, NO ROUTE. A row without a node id cannot be liveness-checked, ban-checked or
		// privacy-checked - there is no name to ask about. Before M0 that described every row and
		// scoring it neutral was the only option; now it describes only a pre-join leftover, and
		// an unfalsifiable candidate is not one to hand a consumer's money to.
		if nodeID == "" {
			continue
		}
		// ABSENCE OF EVIDENCE IS NOT EVIDENCE OF ABSENCE - and on this path the distinction is
		// load-bearing, because an edge attempt is authorized by whichever instance the consumer
		// reached, which need not be the instance the node registered with. A node THIS instance
		// has a registration for can be judged on its heartbeat; one it has never heard of is
		// either genuinely gone or simply registered on a peer whose registry sync has not landed
		// here yet, and those must not be treated alike. So the unknown node is not dropped - it
		// falls to Tier B, reachable only when nothing better exists. (In a real multi-instance
		// deployment the shared registry and liveness mirror both halves here, so this is the
		// narrow window, not the normal case.)
		_, registered := b.nodes[nodeID]
		if registered && now.Sub(b.lastSeen[nodeID]) >= nodeTTL {
			continue // the share went home; the attachment simply has not noticed yet
		}
		if b.banned[nodeID] || bannedNode[nodeID] {
			continue // the node itself, or the account behind it, banned AFTER attach time
		}
		if b.private[nodeID] {
			continue // a private band is reachable by frequency code, never by public placement
		}
		tq := b.trust[nodeID]
		load := b.edgeLoadLocked(nodeID)
		// REAL CAPACITY, DERIVED HERE RATHER THAN CARRIED. This is the one place the input is
		// already under the lock that guards it - concurrentTPS under metricsMu - so it costs a
		// map read and gives the same number the classic router divides by. It replaces the flat
		// 1+load divisor, which was the capacity=1 case pretending to be a policy.
		//
		// The projection USED to carry a Capacity column for this, hardcoded to 1 on every
		// self-attached row, and the honest reading of that was "there is no capacity model".
		// The column is gone from fleet.Station (see its comment): a snapshot as stale as the
		// last publish sweep, of a quantity that moves with every served request, is worse than
		// deriving it at the moment of the decision.
		capacity := edgeCapacityOf(b.concurrentTPS[nodeID])
		sc := scoredCand{
			idx: i, score: edgeScore(tq, load, capacity),
			// The P2C tie-break is load PER UNIT OF CAPACITY, exactly as router.go computes it -
			// two open attempts mean something different on a four-slot rig than on a laptop.
			load: float64(load) / float64(capacity),
		}
		// The same Tier A bar pickFor draws (probeFails < 2), so "healthy" means one thing
		// across both fabrics. Success EWMA is not folded in: edgeExitInflight deliberately does
		// not feed it, so on this path it would be a classic-fabric reading judging edge work.
		//
		// AND the edge fabric's own evidence, which is the only kind that has actually exercised
		// this path: a Station whose last canary probes through its Tower failed falls to Tier B
		// (see edgeCanaryTroubledLocked). One-directional, like the recount evidence in
		// edgeQuality - a canary result may demote a Station and may never promote one - because
		// a canary that PASSED tested a route, and a canary that FAILED may have been the Tower's
		// fault rather than this Station's. Demoting on ambiguous evidence costs a slightly worse
		// placement; promoting on it would hand a consumer's money to a machine on the strength
		// of somebody else's uptime.
		if registered && tq.probeFails < 2 && !b.edgeCanaryTroubledLocked(row.StationID) {
			tierA = append(tierA, sc)
		} else {
			tierB = append(tierB, sc)
		}
	}
	return tierA, tierB
}

// edgeBeta is the P2C sampling concentration for edge placement (score^beta). The classic
// router takes this from the consumer's routing preference, which the edge path does not
// have - an edge consumer authorizes against one Station's pinned price and never expresses
// cheap/fast/reliable - so it uses the balanced anchor, the same value a request with no
// stated preference gets on the other fabric.
var edgeBeta = prefBalanced.weights().beta

// edgePlacementRand is the per-request PRNG behind the power-of-two-choices draw. A fresh
// Rand per authorize rather than one shared source, because *rand.Rand is not safe for
// concurrent use and placement runs on the request goroutine.
//
// # WHY IT IS NOT rand.NewSource
//
// It was, and that cost about five kilobytes and eighteen hundred iterations of setup per
// authorize to produce at most two random numbers. math/rand's default source is a lagged
// Fibonacci generator: seeding it fills a 607-element int64 table (~4.9KB) and stirs it, and
// then this function's entire consumer draws two band members and throws the whole thing away.
// The waste is not the allocation so much as the seeding loop, on a path that already runs
// under a rate limiter for good reasons.
//
// A v2 PCG is 16 bytes of state, seeds in two assignments, and has better statistical
// properties than the generator it replaces. It is adapted to the math/rand Source64 the
// classic router's selectP2C already takes, rather than changing that signature: selectP2C is
// shared with the paid fabric, and this is a performance fix on one caller, not a reason to
// touch the other one's randomness.
func edgePlacementRand() *rand.Rand {
	// Seeded from the v2 global generator, which is randomly seeded at startup and IS safe for
	// concurrent use - the same property the old code relied on rand.Int63 for.
	return rand.New(&pcgSource{p: randv2.NewPCG(randv2.Uint64(), randv2.Uint64())})
}

// pcgSource adapts math/rand/v2's PCG to the math/rand Source64 interface.
//
// Seed is a no-op and must be: this source is constructed already seeded, and math/rand only
// calls Seed when someone asks it to, which nothing here does. Making it re-seed from an int64
// would silently narrow the state a caller thought it had.
type pcgSource struct{ p *randv2.PCG }

func (s *pcgSource) Uint64() uint64 { return s.p.Uint64() }
func (s *pcgSource) Int63() int64   { return int64(s.p.Uint64() >> 1) }
func (s *pcgSource) Seed(int64)     {}

// edgeScore ranks one candidate. Higher is better; the shape deliberately mirrors router.go's
// `quality / load`, which is the classic path's answer to the same question and has the
// property that matters here: no station becomes a magnet.
//
// It is a PURE function of a trust reading and a load count so edgeEligible can score a whole
// fleet from one lock acquisition. edgeCandidateScore is the same policy for a single row, and
// exists for tests and for the single-row callers.
//
// # WHERE IT AGREES WITH router.go AND WHERE IT DOES NOT
//
// An earlier version of this comment claimed the load divisor was used "exactly as the
// classic router uses it". It was not, and the difference mattered: this is a smaller
// function than pickFor and the gaps are all in the direction of concentrating traffic, so
// they are worth naming rather than glossing.
//
// Shared: the quality/load shape, this instance's live load PLUS the merged cross-instance
// peer load, the SAME capacity-normalized loadFactor (1/(1+inflight/capacity)) over the same
// capacityOf derivation, and - since the P2C draw in edgeTargetFor - the same anti-all-to-one
// selection over the same band.
//
// The capacity term is new here, and it is the same function rather than a second one: it used
// to be a flat 1+load, which is the capacity=1 case, and the reason given was that the only
// capacity in reach was the projection's hardcoded 1. That was true when it was written and
// stopped being true at M0 - the node_id join reaches concurrentTPS and the hardware class, and
// edgeEligible already holds both locks that guard them. So the column is gone and the number is
// derived at the moment of the decision (see edgeEligible). A rig now absorbs more concurrent
// edge work than a laptop before its score sags, which is what the divisor was always claiming.
//
// Still different, on purpose or for want of data:
//
//   - NO SPEED-FIT. TTFT and TPS are probed broker-to-node; an edge request never goes through
//     the broker, so the number describes a path this placement is not choosing.
//   - NO PRICE MODIFIER. An edge consumer is quoted the Station's own pinned price and
//     authorizes against it before dispatch, so undercutting is not this function's decision.
//   - A FLAT NEUTRAL, NOT A DECAYING UCB RADIUS. router.go gives a fresh node an exploration
//     lift that shrinks as evidence accumulates; here an unmeasured row simply scores neutral
//     forever until a probe says otherwise. It is the same intent - do not freeze out the
//     unproven - with none of the self-extinguishing part.
//
// Price and speed-fit belong with the locality term in M5 (docs/relay-selection-design.md).
func edgeScore(tq trustState, load, capacity int) float64 {
	return edgeQuality(tq) * loadFactor(load, capacity)
}

// edgeCapacityOf is capacityOf WITHOUT the hardware class: the measured branch, or the
// conservative prior of 1.
//
// # WHY THE EDGE PATH DROPS A FIELD THE CLASSIC ROUTER KEEPS
//
// capacityOf takes two inputs. concurrentTPS is MEASURED, and measured under load specifically
// so that it cannot be won from an idle canary. `hw` is a STRING THE NODE SENDS - a field on
// protocol.NodeRegistration, sitting immediately beside Region, which §4.1 of
// docs/relay-selection-design.md names as the thing a supply-side location may never be. It maps
// "multi-gpu" to 4 and "single-gpu"/"apple" to 2, so on an unmeasured fleet a node doubles or
// quadruples its own placement score by typing a different word: measured at 0.2500 for hw=""
// against 0.5000 for hw="multi-gpu" at the same load, with the P2C tie-break quartered by the
// same string.
//
// The commit that introduced the capacity term said "an unmeasured node falls back to the same
// conservative hardware prior pickFor uses, which is 1 - so nothing about placement changes for
// a fleet nobody has measured yet". That is true of exactly one hw value, the empty one the test
// happened to use. This function makes the sentence true of all of them.
//
// THE CLASSIC ROUTER IS NOT WRONG TO KEEP IT, and the difference is not inconsistency. There,
// the claim is self-correcting and cheap: loadFactor is 1 at zero load whatever the capacity, so
// the prior only bites once the node is already busy - and a node that wins concurrent work it
// cannot serve is measured doing it (recordServed folds servedTPS into concurrentTPS whenever
// two requests shared the node), after which the measurement replaces the claim and the
// degraded TTFT and reliability follow it down. The lie costs the liar.
//
// None of that loop exists here. Edge work does not feed concurrentTPS - edgeExitInflight
// deliberately does not touch the classic counters, so a node whose traffic is all edge is never
// measured at all - and edgeQuality admits recount and canary evidence in the DOWNWARD direction
// only. So the claim would not be corrected by anything, ever: it is a permanent multiplier on a
// self-declared string, which is the shape §4.1 exists to refuse.
//
// The cost of dropping it is that a genuine rig sharing only through the fabric is normalized as
// though it had one slot, so its score sags a little faster under concurrent edge attempts than
// it strictly needs to. That is a small efficiency loss, recoverable the moment the node takes
// any classic traffic, and it is the right side to be wrong on: under-using a rig is worse
// service, over-trusting a claim is a lever.
func edgeCapacityOf(concurrentTPS float64) int {
	return capacityOf(concurrentTPS, "")
}

// edgeNeutralQuality is what a station with no canary evidence is worth: better than a
// known-bad node, worse than a proven one.
//
// UNMEASURED IS NOT BAD. A station the broker has never probed scores neutral rather than
// zero. Treating absent evidence as a bad result would quietly freeze out every newly attached
// node - it would have to win traffic to earn a score, and it could not win traffic without
// one.
const edgeNeutralQuality = 0.75

// edgeQuality turns a node's trust reading into the 0..1 quality half of the score.
//
// # UNMEASURED IS NOT BAD, AND IT IS NOT GOOD EITHER
//
// This function used to be three lines and one of them was wrong in a way that inverted its
// whole intent:
//
//	tq, probed := b.trust[row.NodeID]
//	if probed { quality = tq.score() }
//
// `probed` there is MAP PRESENCE, not tq.probed - and observeRecount creates an entry the
// first time ANY served request is re-counted, with probed=false. trustState.score() starts at
// 1.0 and is only ever subtracted from, so one served request promoted a station from the 0.75
// neutral to the 1.0 ceiling - the score reserved for a node that has passed a live canary -
// on zero liveness evidence, and it stayed there for the seven days the trust entry lives. A
// station that had never been proved to answer anything outranked every honest newly attached
// one, permanently, and won the P2C band against them.
//
// So the three cases are now distinct, and the middle one is the point:
//
//   - PROBED: canary evidence exists. score() is the whole reading and may reach 1.0, because
//     something actually confirmed this node answers.
//   - RE-COUNTED BUT NEVER PROBED: there is evidence, but it is the wrong KIND. A re-count
//     measures HONESTY (did the node's token claim match what it produced), not liveness, and
//     nothing about an honest node proves it is up now. So recount evidence is admitted in one
//     direction only: it may pull a station BELOW neutral - a node caught over-reporting is
//     worse than an unknown one, and that is a finding we already trust - but it may never lift
//     one above neutral, because there is no canary behind the lift.
//   - NOTHING AT ALL: neutral, so a freshly attached station is reachable.
func edgeQuality(tq trustState) float64 {
	if tq.probed {
		return tq.score()
	}
	if tq.recounts > 0 && tq.score() < edgeNeutralQuality {
		return tq.score()
	}
	return edgeNeutralQuality
}

// edgeCandidateScore is edgeScore for a single row, taking the lock itself. The placement path
// does NOT use it - it scores the whole fleet from one lock hold (see edgeEligible) so two
// rows are never compared across two instants.
func (b *broker) edgeCandidateScore(row fleet.Station) float64 {
	if row.NodeID == "" {
		return edgeScore(trustState{}, 0, 1)
	}
	// BOTH locks, b.mu outer then metricsMu inner - the order enrichOffersForNode in market.go
	// establishes and edgeEligible follows. Everything this function reads today lives under
	// metricsMu, but it is called from paths that hold b.mu around it and the pair must always
	// be taken in that order; acquiring them the other way round is a deadlock that compiles.
	b.mu.Lock()
	defer b.mu.Unlock()
	b.metricsMu.Lock()
	defer b.metricsMu.Unlock()
	return edgeScore(b.trust[row.NodeID], b.edgeLoadLocked(row.NodeID),
		edgeCapacityOf(b.concurrentTPS[row.NodeID]))
}

// edgeCandidateLoad is the row's live concurrency as PLACEMENT sees it: relayed work this
// instance dispatched, the merged peer-instance snapshot, and this instance's open edge
// attempts. Peer load matters more here than on the classic path, not less - an edge attempt is
// opened by whichever broker the consumer's authorize landed on and settled by whichever one
// the Tower reaches, so a busy station is routinely busy somewhere other than where it is being
// scored.
func (b *broker) edgeCandidateLoad(row fleet.Station) int {
	if row.NodeID == "" {
		return 0
	}
	b.metricsMu.Lock()
	defer b.metricsMu.Unlock()
	return b.edgeLoadLocked(row.NodeID)
}

// edgeLoadLocked sums the load signals for one node. Caller holds metricsMu.
//
// THE SUM IS ONE-WAY, and that asymmetry is the whole design (see broker.edgeLoad). Placement
// on this path adds relayed load to edge load because it is one machine and one GPU. The
// classic router and the prober add nothing back: they read b.inflight alone, so an edge
// attempt - which any signed-in account can open for the price of a few hundred bytes, before
// it has submitted anything - cannot depress a node's paid-fabric score or stop it being
// canary-probed.
//
// FOUR TERMS, NOT THREE, AND THEY PAIR UP. Each counter has a local half and a merged
// peer-instance half, and the one-way rule holds across the pair exactly as it holds locally:
// peerInflight is other instances' CLASSIC load and peerEdgeLoad is their EDGE load, published
// under a separate shared key for the same reason the local maps are separate (see
// markEdgeInflight in sharedstore.go). The peer edge term is the one that was missing, and it
// mattered most here: an edge attempt is authorized by whichever broker the consumer reached and
// settled by whichever one the Tower reaches, so on any multi-instance deployment a station's
// edge load is routinely being carried somewhere other than where it is being scored. Without
// the term every instance under-counted the same stations and over-ranked them in the same
// direction, which is a magnet dressed as a spread.
//
// IT IS STILL THE RANKING READER. A missing peer snapshot degrades to the last one, which is
// right for a divisor and wrong for a gate; anything asking "is this station idle" must use
// stationQuiescent instead. See edgeload.go.
func (b *broker) edgeLoadLocked(nodeID string) int {
	return b.inflight[nodeID] + b.peerInflight[nodeID] + b.edgeLoad[nodeID] + b.peerEdgeLoad[nodeID]
}

// maxOpenEdgeAttemptsPerAccount bounds how many edge attempts one account may hold open at
// once.
//
// An authorize is the cheapest request on the tower path and one of the most consequential: it
// pins a chosen station as busy for the grant's lifetime before the consumer has sent a byte,
// and its only cost is a ceiling hold that is refunded in full when the attempt expires unused.
// Without a cap, one funded account can hold every routable station at maximum apparent load
// indefinitely for approximately nothing - measured at 500 simultaneous pins for $0.0001 of
// held (not spent) balance. The rate limiter bounds the RATE of opening; this bounds the
// STANDING number, which is the quantity that actually does the damage.
//
// Sized well above any real client's concurrency (a consumer waiting on 32 simultaneous
// completions through relays is already at the edge of plausible) and far below what an
// attacker needs.
const maxOpenEdgeAttemptsPerAccount = 32

// edgeAttemptLoad is one open edge attempt's ledger entry: which node it is pinning, and which
// account is holding the slot.
type edgeAttemptLoad struct{ nodeID, account string }

// edgeAccountReserve claims one of an account's simultaneous-attempt slots, or reports that it
// is at its cap. Every successful reserve must be matched by exactly one release - either
// edgeAccountRelease on a path that abandons the attempt, or edgeEnterInflight, which takes
// ownership of the slot and hands it to edgeExitInflight.
func (b *broker) edgeAccountReserve(account string) bool {
	if account == "" {
		return true // unattributable traffic is refused before it gets here; nothing to cap
	}
	b.metricsMu.Lock()
	defer b.metricsMu.Unlock()
	if b.edgeOpenByAccount == nil {
		b.edgeOpenByAccount = map[string]int{}
	}
	if b.edgeOpenByAccount[account] >= maxOpenEdgeAttemptsPerAccount {
		return false
	}
	b.edgeOpenByAccount[account]++
	return true
}

// edgeAccountRelease hands a reserved slot back. Idempotent below zero.
func (b *broker) edgeAccountRelease(account string) {
	if account == "" {
		return
	}
	b.metricsMu.Lock()
	defer b.metricsMu.Unlock()
	if b.edgeOpenByAccount[account] > 0 {
		b.edgeOpenByAccount[account]--
	}
	if b.edgeOpenByAccount[account] == 0 {
		delete(b.edgeOpenByAccount, account)
	}
}

// edgeEnterInflight / edgeExitInflight bracket one edge attempt in the EDGE load counter.
//
// WITHOUT THIS THE DIVISOR WAS DECORATION. Nothing on the edge path ever moved a load counter,
// so every edge candidate scored at load 0 forever and the highest-scoring station absorbed the
// lot. The score said it was spreading work and it was not, which is worse than not claiming
// to.
//
// TWO COUNTERS, NOT ONE - the correction to the first version of this fix. It used to increment
// b.inflight directly, on the argument that a node serving both fabrics fills one GPU either
// way. That is true of load and false of what b.inflight IS: the classic paid router divides by
// it, peers merge it, and probeOnce skips any node with a non-zero count. So opening an edge
// attempt suppressed a victim's canary probes (freezing verifiedServing(), the /market signal
// and the concierge gate) and depressed its score on the fabric that pays it - and an authorize
// is available to any signed-in account for a refundable fraction of a cent. b.edgeLoad keeps
// the placement signal without handing an outside party that lever; see edgeLoadLocked.
//
// AN EXIT IS NOT GUARANTEED, so it is bounded. The edge has no dispatch loop to unwind: Core
// authorizes and then hears nothing until a receipt arrives, and plenty of attempts never
// produce one - a consumer that never connects, a Station that dies mid-serve. A counter that
// only goes up would slowly mark every station as saturated. So every entry also carries an
// expiry timer.
//
// THE EXPIRY IS THE EXECUTION DEADLINE, NOT THE SETTLEMENT ONE. It used to be the grant's
// deadline plus the settlement grace - the window in which EVIDENCE may still arrive, which is
// minutes longer than the window in which WORK may still be done. Past the grant's own deadline
// the Station refuses the attempt, so the node is by definition no longer carrying it, and
// holding the reservation open for the courier's sake pinned a station for eight minutes over a
// request that could only have run for one. A receipt that arrives after the entry has expired
// finds nothing to close, which is what edgeExitInflight is idempotent for.
func (b *broker) edgeEnterInflight(attemptID, nodeID, account string, until time.Time) {
	if attemptID == "" || nodeID == "" {
		b.edgeAccountRelease(account) // nothing to bracket, so the slot is not ours to keep
		return
	}
	b.metricsMu.Lock()
	if b.edgeInflight == nil {
		b.edgeInflight = map[string]edgeAttemptLoad{}
	}
	if _, open := b.edgeInflight[attemptID]; open {
		b.metricsMu.Unlock()
		b.edgeAccountRelease(account)
		return // idempotent: an attempt id is opened once
	}
	if b.edgeLoad == nil {
		b.edgeLoad = map[string]int{}
	}
	b.edgeInflight[attemptID] = edgeAttemptLoad{nodeID: nodeID, account: account}
	b.edgeLoad[nodeID]++
	b.metricsMu.Unlock()
	// Publish OUTSIDE the lock, exactly as exitInflight does for the classic counter: metricsMu
	// is held on the hot placement path and a shared-store round trip must never be taken under
	// it. The publisher re-reads the count under that lock itself, so nothing is carried across
	// the gap and two concurrent brackets on one node cannot publish out of order. A no-op
	// unless multi-instance is on. See writeThroughEdgeLoad and publishSharedLoad.
	b.writeThroughEdgeLoad(nodeID)
	if d := time.Until(until); d > 0 {
		time.AfterFunc(d, func() { b.edgeExitInflight(attemptID) })
	} else {
		b.edgeExitInflight(attemptID)
	}
}

// edgeExitInflight closes an open edge attempt. Idempotent by construction - the ledger entry
// is what authorizes the decrement, and it is removed with it - so the settle path and the
// expiry timer can both fire without double-counting, and a settlement for an attempt this
// instance never opened (the ordinary multi-instance case) is a no-op rather than a
// decrement of somebody else's count.
//
// That last case is also the one honest gap left here: authorize-on-A, settle-on-B leaves A
// counting until the timer fires. The timer is now the grant's execution deadline rather than
// the settlement ceiling, so the worst case is bounded by the same window the work itself had,
// and the count it holds is the edge-only one - it no longer reaches the paid router or the
// prober. Closing it properly wants the attempt ledger to carry the placement, which is M3-shaped
// work (the relay binding moves to dispatch), not something to bolt on here.
//
// Deliberately NOT folded into the success EWMA the way exitInflight does it. That EWMA gates
// Tier A on the classic path, and an edge attempt that expired unsettled is not evidence the
// node is unhealthy - a consumer closing a laptop looks identical. Marking a good node down
// on the fabric it is not even being judged on would be a worse error than the one this fixes.
func (b *broker) edgeExitInflight(attemptID string) {
	b.metricsMu.Lock()
	entry, open := b.edgeInflight[attemptID]
	if !open {
		b.metricsMu.Unlock()
		return
	}
	delete(b.edgeInflight, attemptID)
	if b.edgeLoad[entry.nodeID] > 0 {
		b.edgeLoad[entry.nodeID]--
	}
	if b.edgeLoad[entry.nodeID] == 0 {
		delete(b.edgeLoad, entry.nodeID)
	}
	if b.edgeOpenByAccount[entry.account] > 0 {
		b.edgeOpenByAccount[entry.account]--
	}
	if b.edgeOpenByAccount[entry.account] == 0 {
		delete(b.edgeOpenByAccount, entry.account)
	}
	b.metricsMu.Unlock()
	// The DECREMENT is the write that matters most to a peer: it is the one that can let a
	// placement or a re-placement proceed. It is still best-effort, and it is now REPAIRABLE,
	// which it was not: refreshSharedLoad republished non-zero counts only, so a zero that
	// failed to land was skipped by every subsequent tick and the station stayed over-stated
	// until the hash aged out at inflightTTL - a minute, on the counter whose whole purpose is
	// to say when a Station is free. The tick now re-marks every node this instance believes it
	// has a live non-zero published for, so a lost zero is corrected on the next one.
	b.writeThroughEdgeLoad(entry.nodeID)
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

// epochFenceVerdict is what the Station-epoch fence has to say about one settlement: whether
// the attachment in front of the settle path is the one the grant was minted against.
//
// It is an enumeration rather than a bool because the three ways it can fail to agree mean
// different things - the placement moved forward, Core's own view of it went backward, or
// nobody stated an epoch at all - and a bool would have collapsed the third into one of the
// first two. Which is what a bool would have done on rollout, to the whole fleet.
type epochFenceVerdict int

const (
	// epochFenceAgrees: the grant and the attachment name the same placement. The only verdict
	// that reaches the money.
	epochFenceAgrees epochFenceVerdict = iota
	// epochFenceUnstated: one side carries the int64 zero value, which means "no epoch here",
	// never "epoch zero". The fence has nothing to compare and declines to guess.
	epochFenceUnstated
	// epochFenceMoved: the attachment has advanced past the grant. This is the rehome the fence
	// was written for - the Station was re-placed while its work was in flight.
	epochFenceMoved
	// epochFenceRegressed: the attachment is BEHIND the grant, which no writer in the tree can
	// produce, since attach.Registry.Admit only ever raises an epoch. A read that lags a write,
	// or state that was restored under a live grant.
	epochFenceRegressed
)

// stationEpochFence compares the epoch a grant was minted under against the attachment's epoch
// now. It is a pure function of two integers so that the VERDICT can be tested apart from the
// eight-hundred-line settlement path that acts on it, and so that adding a fourth answer later
// is a change to one switch rather than to a chain of inequalities read three times.
func stationEpochFence(grantEpoch, attachmentEpoch int64) epochFenceVerdict {
	switch {
	case grantEpoch == 0 || attachmentEpoch == 0:
		return epochFenceUnstated
	case grantEpoch == attachmentEpoch:
		return epochFenceAgrees
	case grantEpoch < attachmentEpoch:
		return epochFenceMoved
	default:
		return epochFenceRegressed
	}
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
	// THE STATION-EPOCH FENCE, WHICH WAS CARRIED FOR ITS WHOLE LIFE AND NEVER COMPARED.
	//
	// dispatch.Record.StationEpoch is documented as the thing that "fences a rehome: work
	// granted under the old origin cannot be completed after the move". It was minted from
	// at.Epoch at authorize, signed into the grant, written to the dispatch row and read back
	// out again - and nothing anywhere ever put it beside the attachment it came from. The
	// fence the whole placement-mobility story rests on did not exist. This is it.
	//
	// WHY IT IS QUIET TODAY AND LOAD-BEARING THE DAY PLACEMENT MOVES. attach.Registry.Admit is
	// the only writer of an epoch (1 on a fresh attach, revived.Epoch+1 on a revival) and it
	// only reaches a live Station through dormancy, which needs seven days with no routable
	// stamp - so inside a settlement window (the grant's lifetime plus the settle grace) the
	// two values are equal by construction and this costs an integer compare. The relay
	// milestone makes a Station's placement something Core may CHANGE, at which point "the
	// placement this work was granted under" and "the placement in front of us now" stop being
	// the same sentence, and this compare is the only thing standing between an in-flight
	// attempt and an origin that moved under it.
	//
	// WHAT A SUPERSEDED GRANT IS WORTH: NOTHING, AND THAT IS A DECISION RATHER THAN A DEFAULT.
	// The founder's ruling on the placement model is that a Station keeps a STICKY binding to
	// one relay and Core may move it only when the Station is idle or its relay is genuinely
	// bad - so a move under live work is not a routine event to be attributed carefully, it is
	// a FAILED DELIVERY. Nobody is paid, the consumer's hold refunds, the consumer retries.
	// This code therefore works out no alternative payee: there is deliberately no "pay the
	// Station but not the relay" branch, because a share zeroed on one integer is exactly the
	// "no share is accrued, CANCELLED, or paid" that
	// features/tower/operator_revenue_share.feature forbids, and there is no withheld-lot state
	// to park one in.
	//
	// AND THE HOLD REFUNDS WITHOUT US. That had to be checked rather than assumed, because a
	// refusal that also stranded the consumer's money would be the wrong answer whatever it
	// did for the operator. It does not: releaseStaleHoldsSweepOnce calls
	// store.ReleaseStaleHolds(cutoff), which reclaims every tracked hold older than holdTTL
	// with no reference to a receipt, a dispatch row or an attempt state - and edgeSettleGrace
	// is capped strictly UNDER holdTTL precisely so the hold always outlives the window it
	// guards. The Station's in-flight reservation comes down on its own too: edgeEnterInflight
	// arms a time.AfterFunc at the grant's deadline. So refusing here commits nothing and
	// strands nothing, and the consumer is made whole by machinery that was already there.
	//
	// WHICH REFUSAL, AND WHY THE TWO DIRECTIONS GET DIFFERENT ONES. This is the load-bearing
	// part. towerjoin.SettleEdgeReceipt turns any 4xx but 409 into ErrSettlePermanent, and the
	// tower's courier then ABANDONS the receipt and drops it from a spool that survives
	// restarts (cmd/roger-tower/hub.go). A 4xx is Core saying "never bring this back", so it is
	// only ever correct when retrying provably cannot help - and the neighbouring 503 on the
	// party-resolution path below exists because a store blip is the opposite of that.
	//
	// A MOVED placement is the case where permanence is true. The epoch is monotonic per
	// Station: nothing lowers it, so no number of retries un-supersedes this grant. Answering
	// 503 would not preserve any possibility of payment - the settlement window would close on
	// a receipt that was refused identically every fifteen seconds - it would only spend the
	// window pretending, and hand the operator a silent expiry instead of a loud abandonment
	// naming the reason on their own console. (It would also hold a spool slot for the whole
	// window; bounded at 65536, so that is a footnote and not the argument.)
	//
	// A REGRESSED epoch is the opposite and must stay transient, which is why these are not one
	// branch. An attachment BEHIND the grant is not a state any writer can produce; it is a read
	// that has not caught up with a write, or restored state. Retrying is exactly what heals it,
	// and a 4xx there would delete an honest operator's pay for a replication delay.
	//
	// IT GATES ENTRY INTO A SETTLEMENT, NOT THE COMPLETION OF ONE CORE ALREADY BEGAN, and that
	// exemption is narrow enough to state exhaustively. This handler is the ONLY caller of
	// ClaimByID or Settle on an edge attempt anywhere in the tree - dispatch.Registry's Claim
	// and ClaimNext have no production callers - so a record that is not `issued` got there
	// through these lines, past this fence, at a moment when the placement agreed.
	//
	// The handler below is deliberately built to finish such a settlement: a fault between the
	// claim and the settle, or between the settle and the wallet capture, leaves an attempt that
	// the courier's next forward re-drives, and the alternative to re-driving it is the
	// consumer's hold swept for work that was really done and both operators unpaid. Refusing
	// that repair because the placement has since moved would punish an operator for OUR
	// interruption, on the one path where Core has already judged this attempt payable under the
	// placement it had. The failed-delivery rule is about work whose settlement never started.
	if rec.State != dispatch.StateIssued {
		log.Printf("edge settle: attempt %s is re-driving a settlement already begun (state %q) - the placement fence does not re-judge it",
			req.AttemptID, rec.State)
	} else {
		switch stationEpochFence(rec.StationEpoch, at.Epoch) {
		case epochFenceAgrees:
			// The ordinary path, and the only one that reaches the money.
		case epochFenceUnstated:
			// ONE SIDE CARRIES NO EPOCH, so the fence has nothing to compare and must not invent a
			// verdict. Zero is the int64 zero value, which is "not stated" and never "epoch zero":
			// treating it as a number would refuse every grant minted before the comparison existed
			// and take the fleet down on the deploy that added a check nothing had been failing.
			//
			// Deliberately symmetric. The grant side goes unstated on a dispatch row that predates
			// the column (tower_attempts.station_epoch defaults to 0); the attachment side goes
			// unstated on any attachment built outside Registry.Admit, which is the only thing that
			// has ever assigned an epoch.
			//
			// This logs on purpose and the line is the exemption's own retirement notice. Every
			// minting path in the tree today - targetFromAttachment, and therefore authorize and
			// the canary - sources the epoch from at.Epoch, which Admit sets to 1 or higher, and
			// both epoch columns have been in their CREATE TABLE since the table existed, so a
			// healthy fleet should print this zero times and the arm can then be deleted. If it
			// starts appearing in volume, something stopped stating the epoch and the fence has been
			// silently disarmed - which is precisely the failure a quiet exemption would hide.
			log.Printf("edge fence UNSTATED attempt=%s station=%s node=%s tower=%s grant_epoch=%d attach_epoch=%d - one side states no epoch, so the placement fence cannot speak for it",
				req.AttemptID, req.StationID, at.NodeID, req.TowerID, rec.StationEpoch, at.Epoch)
		case epochFenceMoved:
			// PERMANENT, AND SAID IN A STATUS THAT MEANS WHAT HAPPENED. 410 rather than the 403s
			// this path already uses for "you had no business here": nobody did anything wrong, the
			// placement this receipt was earned under is simply gone. It reads differently in a
			// tower's log, which is the only place an operator will ever see it.
			// THE ONE LINE IN THE TREE THAT COUNTS THE COST OF MOBILITY, so it is written to be
			// summed rather than read.
			//
			// The sticky-placement model (§6.3b) accepts a race between "Core observed this
			// Station idle" and "the move landed", and relies on this fence to make the loser of
			// that race safe rather than silent. The whole justification for accepting it is that
			// it will be RARE, and nobody can currently check that claim, because moves do not
			// exist yet. Every time this branch fires is exactly one request destroyed by a
			// placement change - so this line IS the instrument, and it has to carry the fields an
			// aggregation would slice by, in the key=value shape the rest of the broker's
			// operational logging already uses (probe.go, report.go, strikes.go).
			//
			// WHY A LINE AND NOT A COUNTER, following the reasoning an earlier review used to
			// reject an in-process per-station placement counter, which applies here with more
			// force rather than less. This event fires on whichever instance the settling Tower's
			// courier happens to reach - not the instance that authorized the attempt and not the
			// one that moved the placement - so a per-process count is partitioned by a variable
			// with no relationship to the thing being measured, and no single instance's number
			// means anything. The quantity we need is a RATE over weeks; a process-lifetime
			// counter resets on every deploy, and we deploy more often than this is supposed to
			// happen. And the useful slices are per-station and per-tower, which in a counter is
			// an unbounded-cardinality map on the money path with a retention policy nobody wrote.
			// The aggregated log stream is already cross-instance and already instance-tagged
			// (main.go sets a per-instance log prefix in multi-instance mode), which is precisely
			// the property the counter would lack. The shared-store counters (counterIncr) are
			// cross-instance but are documented as never authoritative and always reconciled from
			// Postgres; borrowing a money fast-path to hold a metric would be inventing a metrics
			// system in the wrong place.
			//
			// WHAT THE FIELDS ARE FOR:
			//   epochs_skipped  1 is a single move catching one attempt. Greater than 1 means the
			//                   Station moved more than once during ONE attempt's life, which is
			//                   the churn §6.3c's signal hysteresis exists to prevent; it is the first
			//                   number to look at if this line ever appears in volume.
			//   deadline_open   whether the attempt's EXECUTION window was still open when the
			//                   fence fired. It is the closest honest answer available to "was
			//                   work actually in flight": Core never observes an edge dispatch (the
			//                   consumer submits to the relay, not to us) and the receipt is not
			//                   verified until after this branch, so nothing here can assert the
			//                   Station really served. False means the courier's spool caught up
			//                   after the work had finished - still an operator unpaid, but not a
			//                   consumer left waiting. It reads the EXECUTION deadline and not the
			//                   record's own, which is a distinction this line got wrong for its
			//                   whole (short) life - see edgeExecDeadline.
			//   tower           WHICH relay was superseded, which is what makes "our moves off
			//                   tower X keep destroying work" answerable at all.
			//
			// AND WHAT IT DOES NOT MEASURE, which matters as much: it counts requests HARMED, not
			// moves that RACED. A move that lands on a Station with live work whose attempts all
			// settle before the move commits never appears here. Measuring the near-miss is the
			// gate's job and belongs at the gate, where the load at move time is known.
			log.Printf("edge fence MOVED attempt=%s station=%s node=%s tower=%s grant_epoch=%d attach_epoch=%d epochs_skipped=%d deadline_open=%t - the placement moved under work already in flight; this delivery failed, nothing is paid, and the consumer's hold returns on the orphan sweep",
				req.AttemptID, req.StationID, at.NodeID, req.TowerID, rec.StationEpoch, at.Epoch,
				at.Epoch-rec.StationEpoch, time.Now().Before(edgeExecDeadline(rec)))
			jsonErr(w, http.StatusGone, "this Station's placement changed after this attempt was authorized - the attempt is void and nothing is owed on it")
			return
		case epochFenceRegressed:
			// THE ATTACHMENT IS BEHIND THE GRANT, which no writer in the tree can produce, since the
			// epoch is monotonic per Station. So this is a lagging or restored read of Core's own
			// state rather than a claim anybody made on the wire - and settling through it would
			// verify the receipt against, and pay from, an attachment that is not the one the grant
			// was minted from. Transient in shape, so transient in answer: nothing commits and the
			// courier brings the same receipt back in fifteen seconds.
			log.Printf("edge fence REGRESSED attempt=%s station=%s node=%s tower=%s grant_epoch=%d attach_epoch=%d - Core's own attachment state is behind the grant it issued; settling nothing, the courier will retry",
				req.AttemptID, req.StationID, at.NodeID, req.TowerID, rec.StationEpoch, at.Epoch)
			jsonErr(w, http.StatusServiceUnavailable, "this Station's attachment is behind the attempt authorized against it - retry")
			return
		}
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
	// WHO IS BEING PAID, ESTABLISHED WHILE REFUSING IS STILL FREE.
	//
	// Everything below this line commits: the one-use claim, the settle, the wallet capture,
	// the lots. The three "is this the same account" questions the money split depends on used
	// to be asked from INSIDE that committed region, at the moment the shares were computed,
	// where the only two answers available were pay and do-not-pay - so a store error had to
	// become one of them, and it became pay (see sameAccount). Asked here, a store error has a
	// third answer that harms nobody: not yet.
	//
	// The position is deliberate on both sides. It is AFTER settleEdgeAttempt so that a receipt
	// that cannot be reconciled is still refused 400 on its own merits, unchanged, and an
	// unreachable owner index cannot mask a bad receipt. It is BEFORE ClaimByID so that a
	// refusal here consumes nothing: no claim, no settle, no evidence write, no capture. The
	// hold stays exactly where authorize put it, the Tower's spooled courier re-forwards the
	// same receipt in fifteen seconds, and the retry settles it properly.
	//
	// 503 and not 4xx, and the distinction is load-bearing rather than cosmetic:
	// towerjoin.SettleEdgeReceipt treats any 4xx other than 409 as ErrSettlePermanent and
	// ABANDONS the receipt, dropping it from the spool. A 4xx here would turn a five-second
	// database blip into an operator's pay deleted forever.
	parties, perr := b.resolveEdgeParties(req.TowerID, at.Owner, rec.ConsumerKey)
	if perr != nil {
		log.Printf("edge settle: attempt %s - could not resolve the paying and earning accounts (%v); settling nothing, the courier will retry", req.AttemptID, perr)
		jsonErr(w, http.StatusServiceUnavailable, "could not establish who this settlement pays - retry")
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
	// THE STATION IS FREE AGAIN. The receipt is the edge path's only "work finished" signal,
	// so this is where the in-flight count opened at authorize comes back down. Idempotent, and
	// a no-op on any instance that did not open this attempt.
	b.edgeExitInflight(req.AttemptID)
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
	// can bill" - as EVIDENCE, not money). The Tower cannot read the session, but it can
	// WEIGH it: the sealed bytes it relayed are at least as large as the plaintext they
	// carry, so a Station byte claim above the wire count is inflated OR the Tower is lying
	// low. Core cannot tell which from here - and the file's own doctrine holds: settlement
	// uses the receipt and the acknowledgement, NEVER the Tower's word. So a mismatch flags
	// the settlement disputed (which force-audits it below) and the AUDIT arbitrates: the
	// transcript proves the true byte lengths against the signed digests, a wire count below
	// them is a physical impossibility, and THAT is attributable to the Tower. A security
	// review killed the earlier clamp here - it let a consumer running its own tower send
	// wire_out:1 and buy near-free inference at an honest node's expense.
	if (req.WireIn > 0 && settled.Billable.In > req.WireIn) ||
		(req.WireOut > 0 && settled.Billable.Out > req.WireOut) {
		log.Printf("edge settle: attempt %s claim (%d/%d) exceeds the tower's wire count (%d/%d) - disputed, audit arbitrates",
			req.AttemptID, settled.Billable.In, settled.Billable.Out, req.WireIn, req.WireOut)
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
	b.accrueEarnings(ts, req.TowerID, at.Owner, model, parties, settled, now)
	// AND THE REAL WALLET: when edge traffic is priced, this captures the consumer's hold and
	// pays the Station owner and the Tower operator their shares through the same EarningLot
	// lifecycle as direct-node serving. Free (unpriced) traffic is a no-op here.
	b.settleEdgeMoney(ts, req.TowerID, req.StationID, at.Owner, parties, rec, settled, now)
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
	b.recordOutcome(req.TowerID, req.StationID, req.AttemptID, outcome)
	// A sampled fraction is selected for post-hoc content review, and a DISPUTED attempt is
	// audited regardless of the sample - the transcript is the closest look available at
	// whether the Station is self-consistent about the bytes it signed. The digests AND the
	// Station's CLAIMED usage come from the receipt just verified: the audit re-checks that
	// claim against the true length of the transcript bytes, which is what catches an
	// unacknowledged attempt whose operator inflated its own usage_out.
	if disputed {
		b.forceAudit(req.TowerID, req.StationID, req.AttemptID,
			receipt.RequestDigest, receipt.ResponseDigest, receipt.Usage.In, receipt.Usage.Out,
			req.WireIn, req.WireOut)
	} else {
		b.selectForAudit(req.TowerID, req.StationID, req.AttemptID,
			receipt.RequestDigest, receipt.ResponseDigest, receipt.Usage.In, receipt.Usage.Out,
			req.WireIn, req.WireOut)
		// THE ADAPTIVE LAYER (spec: "The audit rate adapts to the evidence"): a fresh
		// Station or an anomalous recent history elevates this settlement's selection odds
		// beyond the deterministic sample - by an unpredictable coin, so a tower cannot
		// compute which attempts are watched. Skipped when the baseline already selected.
		if !auditSampled(req.AttemptID) {
			b.adaptiveAudit(req.TowerID, req.StationID, req.AttemptID,
				receipt.RequestDigest, receipt.ResponseDigest, receipt.Usage.In, receipt.Usage.Out,
				req.WireIn, req.WireOut, at.AttachedAt)
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
//
// THREE ANSWERS, NOT TWO, and the third is why this returns an error. "No wallet account" and
// "I could not reach the store to look" used to be the same (`"", false`), and the difference
// decides money: the first means this Tower earns nothing and no self-dealing check is owed,
// the second means we do not yet know WHO earns and must not guess in either direction. A
// lookup that errored still lets the other lookup answer - one unreachable index is not a
// verdict - and the error only surfaces when neither did.
func (b *broker) towerOperatorAccount(towerID string) (string, bool, error) {
	ts := b.tower
	if ts == nil {
		return "", false, nil
	}
	tw, ok := ts.registry.Get(towerID)
	if !ok || tw.Owner == "" {
		return "", false, nil
	}
	var firstErr error
	keep := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	// Already a wallet account key (an owner pubkey the store knows)?
	o, found, err := b.db.OwnerByPubkey(tw.Owner)
	keep(err)
	if err == nil && found && !o.Anonymized {
		// CANONICAL, so the lot is minted under the same key a cash-out from any of this
		// operator's devices will read. Canonicalization is itself a set of store reads, and a
		// failed one silently keys the lot under a DEVICE row instead of the account - money
		// the operator cannot see from any other device. So its error joins the rest.
		c, cerr := b.accountOwnerOfChecked(o)
		keep(cerr)
		return c.Pubkey, true, firstErr
	}
	// The usual case: the owner is a login; resolve it to the account's pubkey.
	o, found, err = b.db.OwnerByLogin(tw.Owner)
	keep(err)
	if err == nil && found && !o.Anonymized && o.Pubkey != "" {
		c, cerr := b.accountOwnerOfChecked(o)
		keep(cerr)
		return c.Pubkey, true, firstErr
	}
	return "", false, firstErr
}

// stationID is WHICH Station the outcome concerns, and it is a separate question from whose
// fault the outcome is - that one is answered by the outcome itself (see reputation.StationFault).
// Pass the Station whenever there is exactly one, including on findings that stay the Tower's:
// evidence an operator may one day have to dispute should name the machine it is about even
// when the machine is not the one being judged. Empty where a finding genuinely concerns no
// single Station.
func (b *broker) recordOutcome(towerID, stationID, attemptID string, o reputation.Outcome) {
	ts := b.tower
	if ts == nil || ts.outcomes == nil {
		return
	}
	if err := ts.outcomes.Record(reputation.Event{
		TowerID: towerID, StationID: stationID, AttemptID: attemptID, Outcome: o, At: time.Now(),
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
func (b *broker) accrueEarnings(ts *towerSubsystem, towerID, owner, model string, parties edgeParties, settled dispatch.Settlement, at time.Time) {
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
	//
	// The verdict is the one resolveEdgeParties already took, before this settlement was allowed
	// to commit. Asking again here would be a second chance to get a different answer from the
	// same question - and a chance for a store error to answer it, which is precisely what this
	// path no longer does.
	selfDealing := parties.consumerIsStation
	if selfDealing {
		log.Printf("tower %s: attempt %s is self-dealing (consumer owns the Station) - recorded, not owed",
			towerID, settled.AttemptID)
	}
	// UNFUNDED traffic accrues nothing (audit M3): the ledger records what a CONSUMER'S spend
	// makes the platform owe an operator, and a consumer key that resolves to no account -
	// Core's own canary is the standing case; edge authorize requires a signed-in account for
	// everything else - has no spend behind it. Recording an owed amount for probes Core
	// itself sends would be the platform quietly funding a revenue share out of thin air.
	// The row is still written (usage is evidence), flagged like self-dealing is.
	unfunded := parties.keyed && !parties.billable
	micros := edgeAccrualMicros(settled.Billable.In, settled.Billable.Out)
	if unfunded {
		micros = 0
	}
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
func (b *broker) settleEdgeMoney(ts *towerSubsystem, towerID, stationID, stationOwner string, parties edgeParties, rec dispatch.Record, settled dispatch.Settlement, now time.Time) {
	// TOKEN-PRICED FIRST (Option C). The price was pinned into the Core-signed grant at
	// authorize, so settlement honors it REGARDLESS of the env byte-tariff switch - exactly the
	// lock-price property the direct path has. Billable tokens have already been clamped to the
	// grant token ceiling and the tokens<=bytes bound above, so the figure is safe to price.
	if pin, pout, perr := dispatch.EdgeGrantPricing(rec.Grant, ts.dispatchPub,
		link.PublicNetwork, rec.StationID); perr == nil && (pin > 0 || pout > 0) {
		if settled.BillableTokens.In > 0 || settled.BillableTokens.Out > 0 {
			cost := tokenCostCredits(settled.BillableTokens.In, settled.BillableTokens.Out, pin, pout)
			b.captureEdgeCharge(towerID, stationID, stationOwner, parties, settled.AttemptID,
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
		b.captureEdgeCharge(towerID, stationID, stationOwner, parties, settled.AttemptID,
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
	b.captureEdgeCharge(towerID, stationID, stationOwner, parties, settled.AttemptID, rec.Model,
		cost, settled.Billable.In, settled.Billable.Out, now)
}

// edgeConsumerWallet resolves the ACCOUNT wallet to bill for an edge consumer key, so a
// relayed request draws from the SAME balance as a direct one (u_gh_/u_apple_/u_email_),
// not the device-key wallet. It resolves the owner behind the key and its account wallet;
// ok=false for a key not bound to a non-anonymized account (e.g. an ephemeral canary key),
// in which case nothing is billed. The authorize-time account gate already requires a bound
// account, so a real relayed request always resolves here.
//
// The error is separated from ok for the same reason towerOperatorAccount separates them, and
// the consequence here was the sharper one: an unreadable owner index answered "not a billable
// account", and captureEdgeCharge returns silently on that - no capture, no lots, HTTP 200. A
// store blip could therefore hand a consumer free inference and both operators nothing, with
// the courier told the settlement succeeded and the hold left for the sweep.
func (b *broker) edgeConsumerWallet(consumerKey []byte) (string, bool, error) {
	o, ok, err := b.db.OwnerByPubkey(hex.EncodeToString(consumerKey))
	if err != nil {
		return "", false, err
	}
	if !ok {
		return "", false, nil
	}
	w, wok := accountWalletForOwner(o)
	return w, wok, nil
}

// edgeShares splits an edge/relay charge of `cost` credits three ways, all fractions of
// GROSS (the founder-set model, 2026-08-13, overriding the earlier "share of net platform
// revenue" basis - see operator_revenue_share.feature):
//
//	station owner : 1 - feeRate            -> 90% at the default 10% fee (unchanged)
//	tower operator: edgeTowerRate()        -> 10% of GROSS, the relay cut
//	platform      : feeRate - towerRate    -> 5%, i.e. the platform ABSORBS the tower's
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
func (b *broker) captureEdgeCharge(towerID, stationID, stationOwner string, parties edgeParties, attemptID, model string, cost float64, inUnits, outUnits int64, now time.Time) {
	if !parties.billable {
		// Not a billable account (e.g. an ephemeral canary key). No hold was ever placed
		// against an account wallet, so there is nothing to capture and nothing to earn.
		// Distinguished from "the store could not say" upstream, which never reaches here:
		// resolveEdgeParties refuses the settlement instead, so this really is a no-op and
		// not a failure wearing one.
		return
	}
	wallet := parties.consumerWallet
	stationShare, towerShare := b.edgeShares(cost)
	// A CURATED station relayed through a tower still settles pass-through first: the
	// operator is reimbursed the upstream list portion (cost/markup, curated_pricing.go),
	// and the tower's relay cut comes out of the MARKUP the routing fee collected - at the
	// defaults, markup pool 23.08% of cost against a 10% tower cut, so the pool covers it
	// and the pass-through is never invaded. The platform keeps the remainder.
	stationCurated := b.nodeCurated(stationID)
	if stationCurated {
		stationShare = curatedOwnerShare(cost)
		if stationShare+towerShare > cost {
			// A future tower-rate change must shrink the TOWER's cut, never the
			// reimbursement: shorting the pass-through is the underwater bug again.
			towerShare = cost - stationShare
		}
	}
	towerAcct := parties.towerAcct
	if !parties.towerPaid {
		log.Printf("edge settle: attempt %s - Tower %s operator has no resolvable wallet account; Tower earns nothing",
			attemptID, towerID)
	}
	// SELF-DEALING, ON THE MONEY. An operator routing their OWN traffic through their OWN
	// Station (or their own Tower) is buying from themselves; paying them a share of their
	// own spend is wash-trading a revenue share, and - once earnings cash out to a bank -
	// a way to convert credits into money at a discount. The attempt still settles and the
	// consumer still pays in full (the usage is evidence, and free self-service would be its
	// own exploit); the SHARE is what is withheld.
	//
	// This check used to exist only in accrueEarnings, which writes the read-only trail, and
	// only against the STATION owner - so the money path minted both lots unconditionally and
	// a Tower operator relaying their own traffic was not flagged anywhere. Both shares are
	// checked, independently, because the two parties can be different accounts.
	//
	// THE VERDICTS ARE NOT TAKEN HERE. They were taken by resolveEdgeParties before the
	// settlement was permitted to commit, and this function only spends them. That is the fix
	// for the failure mode this comment used to describe with a straight face: the old checks
	// asked the store, at this line, at a moment when the answer could no longer be refused -
	// so a database error read as "not the same account", which reads as "pay".
	if stationShare > 0 && parties.consumerIsStation {
		log.Printf("edge settle: attempt %s is self-dealing (consumer owns the Station) - recorded, not owed", attemptID)
		stationShare = 0
	}
	// NO ACCOUNT, NO LOT, NO CHECK NEEDED - and the guard now says which of those it means.
	// This used to read `towerShare > 0 && towerAcct != "" && sameAccount(...)`, where an
	// unresolvable account silently switched off the self-dealing test standing beside it
	// rather than declaring the Tower unpayable (relay-selection-design.md section 6.7, item
	// 4). towerPaid is that declaration, taken once, upstream, where an error could still be
	// told apart from an absence.
	if towerShare > 0 && parties.towerPaid && parties.consumerIsTower {
		log.Printf("edge settle: attempt %s is self-dealing (consumer owns the Tower) - recorded, not owed", attemptID)
		towerShare = 0
	}
	selfRelayed := b.recordSelfRelayed(attemptID, stationID, towerID, parties)
	r := protocol.UsageReceipt{
		RequestID: attemptID, Model: model,
		PromptTokens: int(inUnits), CompletionTokens: int(outUnits), TS: now.Unix(),
		// The SAME verdict the split used, taken once - a second live read could differ
		// mid-flight and stamp a receipt that contradicts its own settlement.
		Curated: stationCurated,
	}
	// The Tower lot is tagged with a "tower:" node prefix so the earnings surface can tell a
	// Tower-RELAY share apart from a node-SERVING share for the same operator (the dashboard shows
	// them separately). It is provenance only - clawback and payout key on the account and request,
	// not the node - so the prefix changes no money.
	if _, err := b.db.SettleEdge(wallet, stationID, stationOwner, towerNode(towerID), towerAcct,
		cost, stationShare, towerShare, selfRelayed, r); err != nil {
		log.Printf("edge settle: could not bill attempt %s: %v", attemptID, err)
	}
}

// recordSelfRelayed is the STATION-OWNER-versus-TOWER-OPERATOR pair: the third comparison, the
// one nothing in this file made until now. It returns whether to stamp this attempt's lots as
// self-relayed, and it never changes an amount.
//
// # WHAT IT IS
//
// One account serving through its own relay is paid twice for one request - 70% as the Station
// and 10% as the Tower, 80% of what an arms-length consumer paid. The two existing checks are
// both consumer-versus-someone; neither of them can see this, because the consumer here is a
// stranger who did nothing wrong and got what they paid for.
//
// # WHY IT IS EVIDENCE AND NOT ENFORCEMENT
//
// Because under the milestone that makes it reachable it is frequently the RIGHT answer.
// Today an operator cannot arrange it at all: Core picks the tower first-fit at attach
// (toweredgeattach.go), hours before any consumer exists, so nobody can choose to land on
// their own relay. Under M3 the relay becomes a per-request, locality-aware choice
// (docs/relay-selection-design.md section 6) - and at that point your own node behind your own
// relay in the same building genuinely is the lowest-latency path for a consumer in that city.
// Blocking it would mean paying the network to route traffic the long way round, and zeroing
// the share would mean charging an operator for being well placed.
//
// So the rule is the one the design review recommended and the founder endorsed: make it
// MEASURABLE. A policy - a threshold on the fraction of an operator's relay earnings that come
// from their own stations, say - can then be written against a fact that has been accumulating,
// rather than invented in an incident with no data and no column to put it in.
// store.SelfRelayedRollup is the read side; internal/store/ledger.go EarningLot.SelfRelayed is
// the fact.
//
// # WHY THE LOT AND NOT SOMEWHERE CHEAPER
//
// Both of an attempt's lots already share a request id, and both account keys are canonical, so
// for the LITERAL case a self-join over earning_lots would have found the pair without any
// schema at all. What a self-join cannot recover is the linkage verdict - two device keys under
// one GitHub id, one Apple subject or one verified email are one account to sameAccount and two
// unequal strings to SQL. Storing the verdict, taken by the code that already had to take it,
// is the smallest thing that makes the real question answerable rather than the easy half of it.
func (b *broker) recordSelfRelayed(attemptID, stationID, towerID string, p edgeParties) bool {
	if !p.stationIsTower {
		return false
	}
	// Logged at settle as well as stored, because the store is where the pattern lives and the
	// log is where the first person to wonder about it will look.
	log.Printf("edge settle: attempt %s is self-relayed - Station %s and Tower %s are one account (%s); recorded, NOT withheld",
		attemptID, stationID, towerID, p.towerAcct)
	return true
}

// sameAccount reports whether two user pubkeys belong to the same account. Two pubkeys are the
// same account if they are literally equal, or if they resolve to owner records that share a
// binding identity - the GitHub id, the Apple subject, the login, or a VERIFIED email - or if
// they resolve to the same canonical account row. A person may hold several device keys under
// one account, so comparing raw pubkeys alone would miss the operator who consumes on one key
// and runs a Station under another.
//
// # A STORE ERROR IS NOT EVIDENCE OF INNOCENCE
//
// This used to return a bare bool, and every failure - an unreachable owner index, a timeout,
// a closed pool - produced `false`. Read forward from there: false means "not the same
// account", which means "this is not self-dealing", which means PAY. A transient database
// blip during settlement was therefore a payment instruction, on the one path where the payee
// and the beneficiary are the same person and the whole point of the check is that they might
// be. The error now comes back, and every caller must decide what to do about not knowing.
// None of them may treat it as a clean "no".
//
// The symmetric hazard is real and is NOT solved by leaning the bool the other way: answering
// "yes, self-dealt" on an error withholds an honest operator's pay permanently, because the
// lot is minted once and never revisited. Neither bool is safe, which is exactly why the
// answer is three-valued. See resolveEdgeParties for what settlement does with the third one.
//
// # WHAT COUNTS AS ONE ACCOUNT, AND KEEPING IT LEVEL WITH accountOwnerOf
//
// accountkey.go already knows how to fold an account's device rows into one canonical row -
// AppleSub, then Login+GitHubID, then verified Email - because the EARNING side has to mint
// and read lots under one key. That knowledge and this check drifted apart: accountOwnerOf
// learned verified email and sameAccount never did, so two device keys that the money path
// considered one account were two strangers to the self-dealing check. A hole with exactly the
// shape of the one above, arrived at from the other side.
//
// So the last clause compares CANONICAL keys, which is by construction everything
// accountOwnerOf knows, today and after the next linkage is added to it. The explicit switch
// stays in front of it because it is strictly broader in two cases accountOwnerOf deliberately
// is not: a shared GitHub id with no login, and a shared login under different GitHub ids.
// accountOwnerOf refuses those because a rename must not silently re-key an operator's
// earnings; here, where a false positive only withholds a share and invites a look, breadth is
// the safe direction.
//
// What none of this catches is the attack that matters most: several DISTINCT verified
// identities held by one person (unresolved risk E44 in the Tower network plan, internal). That is an
// evidence problem - shared payout destination, funding instrument, device fingerprint - and
// it belongs to the revenue-share program's linkage review, not to an equality test.
func (b *broker) sameAccount(pubA, pubB string) (bool, error) {
	if pubA == "" || pubB == "" {
		return false, nil
	}
	if pubA == pubB {
		return true, nil
	}
	oa, foundA, err := b.db.OwnerByPubkey(pubA)
	if err != nil {
		return false, err
	}
	ob, foundB, err := b.db.OwnerByPubkey(pubB)
	if err != nil {
		return false, err
	}
	// NOT FOUND IS AN ANSWER, unlike an error: a pubkey bound to no owner row shares no
	// identity with anything, because there is nothing recorded to share.
	if !foundA || !foundB {
		return false, nil
	}
	switch {
	case oa.GitHubID != 0 && oa.GitHubID == ob.GitHubID:
		return true, nil
	case oa.AppleSub != "" && oa.AppleSub == ob.AppleSub:
		return true, nil
	case oa.Login != "" && oa.Login == ob.Login:
		return true, nil
	case oa.EmailVerifiedAt != 0 && ob.EmailVerifiedAt != 0 && oa.Email != "" &&
		strings.EqualFold(oa.Email, ob.Email):
		// PROVED, not merely typed in: an unverified profile email is a string anybody may
		// claim, and treating it as a binding identity would let one account withhold
		// another's earnings by claiming their address. EqualFold matches the store's own
		// verified-email lookup.
		return true, nil
	}
	// The canonical backstop. A resolution error here is only fatal if it did not already
	// find the link - a positive is a positive however incomplete the lookup was.
	ca, errA := b.accountOwnerOfChecked(oa)
	cb, errB := b.accountOwnerOfChecked(ob)
	if ca.Pubkey != "" && ca.Pubkey == cb.Pubkey {
		return true, nil
	}
	if errA != nil {
		return false, errA
	}
	if errB != nil {
		return false, errB
	}
	return false, nil
}

// edgeParties is one settled edge attempt's answer to "who are the three parties, and which of
// them are one account". It is resolved ONCE, before anything commits, and then carried through
// the money path so that no wallet write is preceded by a store read whose failure would change
// who gets paid.
//
// The three parties are the CONSUMER (who is charged), the STATION OWNER (who earns 70% of the
// node's own listed price) and the TOWER OPERATOR (who earns 10% for carrying it). The platform
// keeps the remaining 20% and is not a party that can be self-dealt with.
type edgeParties struct {
	// consumerWallet is the account wallet the hold was placed against and the capture is
	// billed to; billable is false for a key bound to no billable account (an ephemeral
	// canary), which means there is nothing to capture and nothing to earn. keyed records
	// whether there was a consumer key to resolve AT ALL, which is a different thing from an
	// unresolvable one and is kept apart so the accrual trail's unfunded rule reads exactly as
	// it always has.
	consumerWallet string
	billable       bool
	keyed          bool
	// towerAcct is the operator's canonical account key, or "" with towerPaid false when the
	// Tower has no resolvable wallet account. No account, no lot, no check needed - stated
	// here as a fact about the Tower rather than left as a conjunct inside an if, where a
	// falsy guard silently disables the self-dealing test standing next to it.
	towerAcct string
	towerPaid bool
	// consumerIsStation and consumerIsTower withhold a share: buying from yourself is not
	// earning, and once earnings cash out to a bank it is a way to convert credits into money
	// at a discount.
	consumerIsStation bool
	consumerIsTower   bool
	// stationIsTower is EVIDENCE ONLY and withholds nothing. See recordSelfRelayed.
	stationIsTower bool
}

// resolveEdgeParties answers every "are these two the same account" question this settlement
// needs, in one place, BEFORE the settlement commits.
//
// # WHY IT RUNS BEFORE THE COMMIT AND NOT WHERE THE MONEY IS SPLIT
//
// The checks used to live inside captureEdgeCharge, which runs after the one-use dispatch
// settle has already committed. At that point the handler has no way to refuse: the share is
// either paid or zeroed, both are final (the lot mints exactly once), and a store error had to
// be resolved into one of them. Moving the question in front of the commit gives settlement a
// third option that costs nobody anything - DECLINE TO ANSWER YET.
//
// # WHICH WAY THE ERROR LEANS, AND WHY IT IS NEITHER OF THE TWO OBVIOUS ONES
//
// Fail open (pay) hands a self-dealer their share for the price of a database blip they can
// provoke. Fail closed (withhold) burns an honest operator's pay for a blip they cannot even
// see, permanently, because nothing revisits a lot that was never minted. Both convert an
// unknown into a wrong answer, and settlement does not have to: this exchange has a retry rail
// under it. The Tower spools the receipt durably and re-forwards it every 15s
// (cmd/roger-tower/hub.go), treating 5xx as retryable and only 4xx as final; Core's own settle
// handler is written to complete a half-finished settlement on a re-drive. So the honest answer
// to "I cannot tell who these people are" is 503 - decide nothing, commit nothing, and be asked
// again in fifteen seconds, by which time the store is almost certainly back.
//
// That is also what the spec says to do, and it is not the self-dealing scenario:
// features/tower/operator_revenue_share.feature "Ledger or payment-store failure fails closed
// for share money" - "no share is accrued, CANCELLED, or paid" and "the operation is retried
// only from durable authoritative state". Note "cancelled": zeroing the share on unverified
// state is forbidden by the same sentence that forbids paying it.
//
// The residual cost is real and bounded, and it is the price of not guessing: if the store is
// still unreachable when the settlement window closes, ClaimByID expires the attempt, the
// consumer's hold is returned by the orphan sweep, and the work was done for free. The
// consumer is made whole, the operator is not paid, and nothing was attributed to the wrong
// account. Compare the failure it replaces - the consumer charged in full, the share paid to
// someone we could not identify - and this is the one to prefer.
func (b *broker) resolveEdgeParties(towerID, stationOwner string, consumerKey []byte) (edgeParties, error) {
	var p edgeParties
	p.keyed = len(consumerKey) > 0
	wallet, billable, err := b.edgeConsumerWallet(consumerKey)
	if err != nil {
		return edgeParties{}, err
	}
	p.consumerWallet, p.billable = wallet, billable
	acct, paid, err := b.towerOperatorAccount(towerID)
	if err != nil {
		return edgeParties{}, err
	}
	p.towerAcct, p.towerPaid = acct, paid
	consumerHex := ""
	if len(consumerKey) > 0 {
		consumerHex = hex.EncodeToString(consumerKey)
	}
	if consumerHex != "" && stationOwner != "" {
		if p.consumerIsStation, err = b.sameAccount(consumerHex, stationOwner); err != nil {
			return edgeParties{}, err
		}
	}
	if consumerHex != "" && p.towerPaid {
		if p.consumerIsTower, err = b.sameAccount(consumerHex, p.towerAcct); err != nil {
			return edgeParties{}, err
		}
	}
	// THE THIRD PAIR, which nothing compared until now: the Station's owner against the Tower's
	// operator. One account on both sides of the split collects 70% + 10% = 80% of a request an
	// arms-length consumer paid for in full. It is not refused and not withheld - see
	// recordSelfRelayed for why - but it is no longer invisible.
	if stationOwner != "" && p.towerPaid {
		if p.stationIsTower, err = b.sameAccount(stationOwner, p.towerAcct); err != nil {
			return edgeParties{}, err
		}
	}
	return p, nil
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
// to 5% at the default), never the serving Station's 90% share. Capped at feeRate in
// edgeShares so the platform's residual can never go negative.
const edgeTowerRateDefault = 0.05 // 90/5/5 since the 2026-09-01 fee ruling (was 70/10/20)

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

// edgeTowerRate is the Tower's fraction of GROSS, defaulting to 5% and clamped to [0,1] here
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
