package main

// The edge bridge: /v1/chat/completions serving a model through a Tower's sealed hub.
//
// Contract: features/tower/edge_fanout.feature. The relay audit found the two fabrics
// were fully partitioned - every real consumer called the direct endpoint, which refused
// Towers outright, so no live traffic could ride one and Towers could not earn. The
// bridge closes that: on a direct miss (and on the fan-out coin when both fabrics serve),
// the broker itself drives the sealed edge loop the canary already proved - authorize,
// seal, submit to the Tower's hub, open, acknowledge - as the consumer's agent.
//
// What the Tower sees is unchanged: a sealed payload it cannot read, sealed back to a key
// only this broker holds. What the CONSUMER sees is unchanged too: the same endpoint, the
// same response shape, the same headers. Core's own visibility - the plaintext it already
// relays on the direct path - is exactly the visibility it has here.

import (
	"context"
	"crypto/ed25519"
	crand "crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/rand"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/towercore/dispatch"
	"rogerai.fm/roger/v6/internal/towercore/envelope"
	"rogerai.fm/roger/v6/internal/towercore/fleet"
	"rogerai.fm/roger/v6/internal/towercore/link"
	"rogerai.fm/roger/v6/internal/towercore/reputation"
	"rogerai.fm/roger/v6/internal/towerhub"
)

// edgeBridgeTimeout bounds one bridged drive. Longer than the canary's, because a real
// prompt is real work; still finite, because the fallback behind it is the point.
const edgeBridgeTimeout = 120 * time.Second

// edgeBridgeSoftTimeout bounds a soft-mode drive: a direct node is already picked and
// waiting, so a dead Tower must fail fast rather than spend the full budget.
const edgeBridgeSoftTimeout = 15 * time.Second

// edgeBridgeMaxTowers bounds the tower-to-tower retry: a failing Tower falls back to
// another, and past that the caller falls back to the direct fabric or refuses honestly.
const edgeBridgeMaxTowers = 2

// relayViaEdge serves one consumer request through the edge fabric. It reports whether it
// WROTE A RESPONSE: false means "nothing here for you" and the caller keeps its existing
// refusal, so this can never change what a consumer sees except by serving them.
// edgeBridgeAuth is the identity and constraints the RELAY already resolved - passed in,
// never re-derived from headers. Re-reading X-Roger-Pubkey here (the first cut's
// CRITICAL bug) trusts an unverified header: on the grant path relay never verifies a
// signature, so a forged pubkey would bill any victim's wallet. The authoritative values
// are the only ones this path may spend against.
type edgeBridgeAuth struct {
	wallet           string // the money key relay resolved (account wallet or grant wallet)
	pubHex           string // the VERIFIED consumer pubkey (identityOf checked its signature)
	grant            bool   // a grant-bearing request: refused - a grant binds specific hardware
	sessionAuthed    bool   // a browser-session caller (Playbox): no device signature to bind
	confidentialOnly bool
	maxPriceIn       float64 // the consumer's in-price ceiling (X-Roger-Max-Price)
	maxPriceOut      float64 // the consumer's out-price ceiling (X-Roger-Max-Price-Out)
	pinNode          string
	freqBand         bool // a private-band (X-Roger-Freq) tune-in: never diverts to a public Tower
	freeOrSelf       bool // the direct pick resolved to $0 (grant-free or self-use): never billed on a Tower
	// PARITY WITH THE DIRECT PATH (regression_pins defect 4): the bridge evaluates the
	// same min-tps floor, the same exclusion set (a Tower id or its node id in the set
	// declines that Tower) and ranks Towers with the same pref weights.
	edgeConstraints
	// namedIDs: the request named stations (provider.only / provider.order / a pin), so a
	// refusal must not reveal which of those ids are Towers - an account-less caller gets the
	// uniform no_match the direct path gives an unknown id, never the tower-specific 403.
	namedIDs bool
}

// edgeConstraints is the consumer's routing shape as the edge placement sees it: the
// hard filters and the scoring profile the direct path's pickFor applies. The zero value
// is "no constraint, balanced", which is what the authorize endpoint and the canary pass.
type edgeConstraints struct {
	minTPS       float64
	exclude      map[string]bool
	pref         pref
	promptTokens int
	// PARITY, slice 1 part C (contract §6): every hard filter pickFor applies, evaluated on
	// the Tower row - attributes the row does not carry (quant, capabilities, ctx, curated)
	// are read from the registration of the node behind it, exactly where the broker keeps
	// them for any node; a declared attribute that is not declared leaves the row ineligible,
	// a measured one that is not measured passes (the direct path's own rule).
	allow          map[string]bool // provider.only: a Tower id or its node id must be listed; nil = no filter
	pin            string          // X-Roger-Node: a Tower id or its node id; "" = no pin
	quants         map[string]bool
	needTools      bool
	needVision     bool
	selfHostedOnly bool
	freeOnly       bool
	sort           sortKey
	maxPriceIn     float64 // $/1M ceilings, 0 = none (the relay's effective caps)
	maxPriceOut    float64
	capReq         float64 // provider.max_price.request: a row whose input cost alone meets it buys no output
	netFilters             // params_b, min_ctx, max_ttft_ms, trust_min verified, region, on the joined node
	// declined tallies, per constraint, the Tower ids a row was declined for during one
	// request (nil = not recorded): the edge_bridge_declined{<constraint>} counters and the
	// one decline log line per request and constraint (edge_bridge_parity.feature).
	declined map[string]map[string]bool
}

// note records one row declined by a consumer constraint. Never a liveness, ban or
// band refusal - those are the fleet's state, not something the consumer asked for.
func (c edgeConstraints) note(reason, towerID string) {
	if c.declined == nil {
		return
	}
	if c.declined[reason] == nil {
		c.declined[reason] = map[string]bool{}
	}
	c.declined[reason][towerID] = true
}

// edgeMetric is what a strict sort ranks a Tower row by: the row's own price and the
// measured speed of the node behind it, the same three metrics pickFor sorts a direct
// station by.
type edgeMetric struct {
	in, out float64 // $/1M
	tps     float64 // 0 = unmeasured (sorts last)
	ttft    float64 // ms, 0 = unmeasured (sorts last)
}

// edgeRefusal is a consumer-facing refusal a bridge gate produced (not a Tower failure):
// hard mode writes it, soft mode lets the direct fabric answer instead.
type edgeRefusal struct {
	status     int
	msg        string
	retryAfter string
}

func (e *edgeRefusal) write(w http.ResponseWriter) {
	if e.retryAfter != "" {
		w.Header().Set("Retry-After", e.retryAfter)
	}
	w.Header().Set("X-RogerAI-Cost", "0")
	jsonErr(w, e.status, e.msg)
}

// edgeCallerFor resolves, ONCE per request, whether the consumer the relay authenticated may
// ride the edge at all and which wallet is billed. These are the gates that do not depend on
// the row: a grant, a browser session, confidential-only, a private band and $0 traffic are
// direct-only in silence (the direct fabric answers for them); an account-less key is refused
// out loud in hard mode UNLESS the request named ids (namedIDs), when the uniform no_match
// stands so nothing discloses which ids are Towers (contract §6).
func (b *broker) edgeCallerFor(auth edgeBridgeAuth) (wallet string, ok bool, refusal *edgeRefusal) {
	ts := b.tower
	if ts == nil || ts.dispatch == nil || auth.grant || auth.sessionAuthed || auth.confidentialOnly || auth.freqBand || auth.freeOrSelf {
		return "", false, nil
	}
	denied := &edgeRefusal{status: http.StatusForbidden, msg: "tower inference requires a signed-in account that has accepted the terms of service"}
	if auth.namedIDs {
		denied = nil
	}
	o, found, oerr := b.db.OwnerByPubkey(auth.pubHex)
	if auth.pubHex == "" || oerr != nil || !found || o.Anonymized || b.isOwnerBanned(o.Pubkey) {
		return "", false, denied
	}
	// The wallet is the one relay resolved - not re-derived. It must match the account the
	// verified pubkey owns, or the caller is trying to bill an identity it did not prove.
	consumerWallet, cwok := accountWalletForOwner(o)
	if !cwok || consumerWallet != auth.wallet {
		return "", false, denied
	}
	return consumerWallet, true, nil
}

// edgeHold says whose hold a bridged attempt rides on. The relay's attempt plan places ONE
// hold per request, sized for its priciest (model, station) pair including a Tower pair, and
// moves it to the attempt that runs (rekey: the pending-hold row follows the attempt id the
// Tower's settlement later captures). A caller outside a plan (relayViaEdge) places the
// attempt's own hold, as the bridge always did.
type edgeHold struct {
	payer   string
	key     *string // the plan's current hold key; nil = place an own hold
	maxCost float64 // the plan's ceiling (rekey) or ignored (own hold sizes itself)
}

// edgeOutcome is what one bridged attempt came to when it did not answer.
type edgeOutcome struct {
	status     int          // the Tower station's upstream status, when it reported one (429, 5xx)
	retryAfter int          // seconds, from that status
	refusal    *edgeRefusal // a gate refusal (rate, slot, balance) rather than a Tower failure
	outcome    reputation.Outcome
}

// edgeAttempt drives ONE bridged attempt to one Tower row: mint the grant, hold or rekey, open
// the attempt, seal, submit, open, acknowledge. It returns the opened answer and the grant on
// success; otherwise the outcome. It never writes to the consumer - callers decide what the
// consumer sees (serve, fall back, or surface the Tower's own status).
func (b *broker) edgeAttempt(r *http.Request, target dispatch.Target, row fleet.Station, model string, body []byte, consumerWallet string, hold edgeHold, soft bool, deadline time.Time) ([]byte, dispatch.EdgeGrant, edgeOutcome) {
	ts := b.tower
	var none dispatch.EdgeGrant
	// The projection is not a security boundary: the price is re-checked against the
	// public band at the moment it becomes money, exactly as authorize does.
	if row.PriceIn != 0 || row.PriceOut != 0 {
		if floor, ceiling, bok := towerPriceBand(model); !bok ||
			row.PriceIn < floor || row.PriceIn > ceiling ||
			row.PriceOut < floor || row.PriceOut > ceiling {
			log.Printf("edge bridge: routable row for %s/%s carries an out-of-band price (%d/%d) - excluded",
				row.TowerID, row.StationID, row.PriceIn, row.PriceOut)
			return nil, none, edgeOutcome{}
		}
	}
	// One identity, one bucket, one standing cap - identical to the authorize endpoint, so
	// the bridge is not a way around either bound.
	if allowed, retry := b.rl.allow("edge:" + consumerWallet); !allowed {
		return nil, none, edgeOutcome{refusal: &edgeRefusal{status: http.StatusTooManyRequests,
			msg: "rate limit exceeded - slow down", retryAfter: fmt.Sprintf("%d", retry)}}
	}
	if !b.edgeAccountReserve(consumerWallet) {
		return nil, none, edgeOutcome{refusal: &edgeRefusal{status: http.StatusTooManyRequests,
			msg: "too many edge attempts open on this account at once - finish or abandon some before opening more", retryAfter: "5"}}
	}
	slotHeld := true
	defer func() {
		if slotHeld {
			b.edgeAccountRelease(consumerWallet)
		}
	}()
	// The bridge is the consumer's agent: it holds the ephemeral keys, exactly as the
	// canary does. The account is billed via the wallet on the hold and the inflight
	// ledger; the grant's consumer key only binds the acknowledgement, which the
	// bridge itself signs.
	_, consumerKey, err := ed25519.GenerateKey(crand.Reader)
	if err != nil {
		return nil, none, edgeOutcome{}
	}
	envPub, envPriv, err := envelope.NewKey()
	if err != nil {
		return nil, none, edgeOutcome{}
	}
	g, err := ts.dispatch.MintEdge(dispatch.EdgeTarget{
		TowerID: target.TowerID, StationID: target.StationID, StationEpoch: target.StationEpoch,
		Model: target.Model, Modality: target.Modality,
		RelayName: target.StationID + "." + relayDomain(),
		MaxIn:     edgeMaxBytes, MaxOut: edgeMaxBytes,
		MaxTokIn: edgeMaxTokens, MaxTokOut: edgeMaxTokens,
		AssertionKey:   target.AssertionKey,
		ConsumerKey:    consumerKey.Public().(ed25519.PublicKey),
		ConsumerEnvKey: envPub,
		PriceInMicros:  row.PriceIn, PriceOutMicros: row.PriceOut,
	})
	if err != nil {
		log.Printf("edge bridge: could not mint for tower %s: %v", target.TowerID, err)
		return nil, none, edgeOutcome{}
	}
	// MONEY. Own hold: the ceiling up front, settle captures the actual figure and refunds
	// the rest (free traffic skips it). Plan hold: the request's ONE hold follows this attempt
	// id, so the Tower's settlement captures it and the relay's exit paths keep refunding it
	// until something does.
	ownHold := 0.0
	if hold.key == nil {
		ownHold = edgeGrantCeiling(row.PriceIn, row.PriceOut)
		if ownHold > 0 {
			if hok, herr := b.db.HoldFor(consumerWallet, g.AttemptID, ownHold); herr != nil || !hok {
				return nil, none, edgeOutcome{refusal: &edgeRefusal{status: http.StatusPaymentRequired, msg: "insufficient balance for this request"}}
			}
		}
	} else if !b.rekeyHold(hold.payer, hold.key, g.AttemptID, hold.maxCost) {
		return nil, none, edgeOutcome{}
	}
	releaseOwn := func() {
		if ownHold > 0 {
			if _, rerr := b.db.ReleaseHoldFor(consumerWallet, g.AttemptID); rerr != nil {
				log.Printf("edge bridge: could not release hold for attempt %s: %v", g.AttemptID, rerr)
			}
		}
	}
	if err := b.openEdgeAttempt(g, target); err != nil {
		log.Printf("edge bridge: could not record attempt %s: %v", g.AttemptID, err)
		releaseOwn()
		return nil, none, edgeOutcome{}
	}
	b.edgeEnterInflight(g.AttemptID, row.NodeID, consumerWallet, g.Deadline)
	slotHeld = false // the ledger entry owns the slot from here

	// Record where this request came from - coarse country only, for the admin detail
	// view's demand map. Attributed to the attempt being routed (idempotent), so a retry
	// does not double-count; a failed drive still counts as demand from that country,
	// which is exactly the signal an operator watching for an anomaly wants to see.
	if ts.origin != nil && r != nil {
		if oerr := ts.origin.Record(target.TowerID, g.AttemptID, clientCountry(r), time.Now()); oerr != nil {
			log.Printf("edge bridge: could not record traffic origin for tower %s attempt %s: %v",
				target.TowerID, g.AttemptID, oerr)
		}
	}
	unstreamed := unstreamBody(body)
	driveTimeout := edgeBridgeTimeout
	if soft {
		driveTimeout = edgeBridgeSoftTimeout // a direct node waits behind this; do not stall on a dead Tower
	}
	// The request's own deadline bounds a planned attempt: a bridged try never extends the
	// consumer's wait past the window a direct one is allowed.
	if !deadline.IsZero() {
		if left := time.Until(deadline); left < driveTimeout {
			driveTimeout = left
		}
	}
	answer, outcome, failure := b.driveSealedF(g, target, row.Endpoint, row.TLSSPKI, consumerKey, envPriv,
		sealedDrive{tag: "bridge", body: unstreamed, timeout: driveTimeout, usageIn: int64(len(unstreamed))})
	if len(answer) > 0 {
		return answer, g, edgeOutcome{outcome: outcome}
	}
	// FAILED: an own hold is released now, not at the orphan sweep - a consumer whose
	// request we are about to serve elsewhere must not have funds pinned behind a dead
	// Tower. The failure is evidence on the Tower's record (organic, never a canary
	// count), and the Tower is excluded for the rest of this request by the caller.
	releaseOwn()
	b.edgeExitInflight(g.AttemptID)
	if outcome == reputation.CanaryFail || outcome == reputation.StationFault {
		b.recordOutcome(target.TowerID, target.StationID, g.AttemptID, reputation.StationFault)
	}
	status, retryAfter := upstreamClassStatus(failure)
	log.Printf("edge bridge: tower %s failed for %s (attempt %s) - trying the next relay",
		target.TowerID, model, g.AttemptID)
	return nil, g, edgeOutcome{status: status, retryAfter: retryAfter, outcome: outcome}
}

// upstreamClassRe reads the Tower station's failure class when the model behind it replied
// with a status: "the model did not answer (status 429, retry-after 6)". Only the two numbers
// cross the blind Tower (internal/station: UpstreamStatusError.Class); an old station's bare
// class yields 0, and the caller treats that as a generic failure.
var upstreamClassRe = regexp.MustCompile(`\(status (\d{3})(?:, retry-after (\d+))?\)`)

// edgeGrantCeiling is the most a bridged attempt can be billed: the larger of the grant's byte
// bound and its token bound at the row's prices. A bridged attempt's own hold is sized at it,
// and a plan hold covering a Tower pair is never below it (founder ruling 2026-10-02), so the
// Tower's settlement - which clamps to the hold - can never underpay the operator.
func edgeGrantCeiling(priceInMicros, priceOutMicros int64) float64 {
	return math.Max(edgePriceCredits(edgeMaxBytes, edgeMaxBytes), tokenCostCredits(edgeMaxTokens, edgeMaxTokens, priceInMicros, priceOutMicros))
}

func upstreamClassStatus(failure string) (status, retryAfter int) {
	m := upstreamClassRe.FindStringSubmatch(failure)
	if m == nil {
		return 0, 0
	}
	status, _ = strconv.Atoi(m[1])
	if status < 400 || status > 599 {
		// A Tower is a third party: what it reports for the station behind it is accepted
		// only as a FAILURE. A "200" or a "999" here would answer the consumer an error body
		// under a success status; it is a bad gateway.
		status = http.StatusBadGateway
	}
	if m[2] != "" {
		retryAfter, _ = strconv.Atoi(m[2])
		if maxSecs := int(cooldownMax() / time.Second); retryAfter > maxSecs {
			retryAfter = maxSecs // never a longer back-off than the broker itself would impose
		}
	}
	return status, retryAfter
}

// writeTowerFailure answers the consumer with the Tower station's own upstream status when
// the bridge was the last word (no fallback allowed, or nothing left to fall back to), with
// its Retry-After when it carried one - never a synthetic no_match for a band that is on air.
func writeTowerFailure(w http.ResponseWriter, out edgeOutcome) {
	w.Header().Set("X-RogerAI-Cost", "0")
	if out.retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(out.retryAfter))
	}
	jsonErr(w, out.status, fmt.Sprintf("the station behind the tower replied %d", out.status))
}

// soft is the both-fabrics mode: a direct node stands ready behind this call, so any
// gate that would refuse (auth, rate, slot, balance) returns false and lets the direct
// path serve instead of writing an edge-shaped refusal to a consumer the network could
// have served. Hard mode (edge-only) writes the honest refusal, because there is no one
// behind it. (The relay's attempt plan now drives bridged attempts itself through
// edgeAttempt; this entry point remains for single-shot callers and the canary-era tests.)
func (b *broker) relayViaEdge(w http.ResponseWriter, r *http.Request, model string, stream bool, body []byte, rng *rand.Rand, soft bool, auth edgeBridgeAuth) bool {
	ts := b.tower
	if ts == nil || ts.dispatch == nil {
		return false
	}
	// A single-shot caller states its pin and price caps on the auth itself (the relay's
	// plan states them on the constraints); fold them in so the row is judged under both.
	c := auth.edgeConstraints
	if c.pin == "" {
		c.pin = auth.pinNode
	}
	if c.maxPriceIn == 0 {
		c.maxPriceIn = auth.maxPriceIn
	}
	if c.maxPriceOut == 0 {
		c.maxPriceOut = auth.maxPriceOut
	}
	auth.edgeConstraints = c
	// A cheap eligibility probe before any consumer gating: if no eligible Tower hosts the
	// model there is nothing to say, and the caller's "no node offers" stays the answer.
	if _, _, ok := b.edgeTargetForC(model, rng, nil, auth.edgeConstraints); !ok {
		return false
	}
	consumerWallet, ok, refusal := b.edgeCallerFor(auth)
	if !ok {
		if refusal != nil && !soft {
			refusal.write(w)
			return true
		}
		return false
	}
	exclude := map[string]bool{}
	tries := edgeBridgeMaxTowers
	if soft {
		tries = 1 // a direct node is ready; do not spend the full budget on failing towers
	}
	var last edgeOutcome
	for attempt := 0; attempt < tries; attempt++ {
		target, row, ok := b.edgeTargetForC(model, rng, exclude, auth.edgeConstraints)
		if !ok {
			break
		}
		answer, g, out := b.edgeAttempt(r, target, row, model, body, consumerWallet, edgeHold{}, soft, time.Time{})
		if len(answer) > 0 {
			b.writeBridgedAnswer(w, g, row, auth.pubHex, answer, stream)
			return true
		}
		if out.refusal != nil {
			if soft {
				return false
			}
			out.refusal.write(w)
			return true
		}
		last = out
		exclude[target.TowerID] = true
	}
	if !soft && last.status >= 400 {
		writeTowerFailure(w, last)
		return true
	}
	return false
}

// bridgedReceipt is the receipt a bridged answer carries (contract §7): the attempt id, the
// relay name the broker dispatched to as the node, the Tower that carried it, the served
// model and the answer's own usage at the grant's pinned prices, broker-signed with the
// same covering scheme as a direct receipt. The node signature is the Tower fabric's to
// give: nothing on this side holds the Tower's key, so the field stays empty here.
func (b *broker) bridgedReceipt(g dispatch.EdgeGrant, row fleet.Station, consumerPub string, answer []byte) (protocol.UsageReceipt, float64) {
	var usage struct {
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	_ = json.Unmarshal(answer, &usage)
	rec := protocol.UsageReceipt{
		RequestID: g.AttemptID, NodeID: g.RelayName, Relay: g.TowerID, Model: g.Model,
		User:         b.pseudonym(protocol.UserIDFromPubkey(consumerPub), g.RelayName),
		PromptTokens: usage.Usage.PromptTokens, CompletionTokens: usage.Usage.CompletionTokens,
		PriceIn: edgeRowPrice(g.PriceInMicros), PriceOut: edgeRowPrice(g.PriceOutMicros),
		TS:      time.Now().Unix(),
		Curated: b.nodeCurated(row.NodeID),
	}
	rec.SignBroker(b.priv)
	return rec, rec.Cost()
}

// writeBridgedAnswer hands the station's answer back in the contract shape the client
// already parses: the same headers a direct answer carries (receipt, provider, model, relay,
// cost, tokens). The answer bytes ARE the upstream's OpenAI-shaped JSON - the station seals
// its upstream's response body verbatim - so the non-streamed path passes them through, and
// the streamed path wraps them as one delta chunk, the broker's usage chunk, then [DONE].
func (b *broker) writeBridgedAnswer(w http.ResponseWriter, g dispatch.EdgeGrant, row fleet.Station, consumerPub string, answer []byte, stream bool) (protocol.UsageReceipt, float64) {
	rec, cost := b.bridgedReceipt(g, row, consumerPub, answer)
	setBridgedHeaders(w.Header(), g, rec, cost)
	if !stream {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(answer)
		return rec, cost
	}
	// A streaming request served by the edge arrives whole - the hub is submit/answer, not
	// a byte stream - so the answer goes out as one well-formed SSE chunk. Honest about
	// the shape rather than pretending to token-stream.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "data: %s\n\n", bridgedDeltaChunk(answer, rec))
	fmt.Fprintf(w, "data: %s\n\n", bridgedUsageChunk(g, rec, cost))
	fmt.Fprintf(w, ": rogerai-cost=%s\n\n", fmtCostHeader(cost))
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	return rec, cost
}

func setBridgedHeaders(h http.Header, g dispatch.EdgeGrant, rec protocol.UsageReceipt, cost float64) {
	h.Set("X-RogerAI-Receipt", protocol.EncodeReceipt(rec))
	h.Set("X-RogerAI-Provider", g.RelayName)
	h.Set("X-RogerAI-Model", g.Model)
	h.Set("X-RogerAI-Relay", g.TowerID)
	h.Set("X-RogerAI-Cost", fmtCostHeader(cost))
	h.Set("X-RogerAI-Tokens-In", fmt.Sprintf("%d", rec.PromptTokens))
	h.Set("X-RogerAI-Tokens-Out", fmt.Sprintf("%d", rec.CompletionTokens))
	h.Set("X-RogerAI-Price", fmt.Sprintf("in=%.4f;out=%.4f;locked_until=0", rec.PriceIn, rec.PriceOut))
}

// bridgedDeltaChunk re-frames the whole answer as one delta chunk.
func bridgedDeltaChunk(answer []byte, rec protocol.UsageReceipt) []byte {
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	_ = json.Unmarshal(answer, &parsed)
	content := ""
	if len(parsed.Choices) > 0 {
		content = parsed.Choices[0].Message.Content
	}
	chunk, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{{"delta": map[string]string{"content": content}, "index": 0}},
		"usage":   map[string]int{"prompt_tokens": rec.PromptTokens, "completion_tokens": rec.CompletionTokens},
	})
	return chunk
}

// bridgedUsageChunk is the broker's usage chunk for a bridged stream (contract §7): the same
// shape the direct path ends every stream with, naming the relay and the Tower.
func bridgedUsageChunk(g dispatch.EdgeGrant, rec protocol.UsageReceipt, cost float64) []byte {
	return usageChunkJSON(rec.PromptTokens, rec.CompletionTokens, cost, map[string]any{
		"receipt": protocol.EncodeReceipt(rec), "node": g.RelayName, "relay": g.TowerID, "model": g.Model,
		"tokens_in": rec.PromptTokens, "tokens_out": rec.CompletionTokens,
		"price_in": rec.PriceIn, "price_out": rec.PriceOut,
	})
}

// sealedDrive parameterizes one sealed submit: what rides in the envelope, how long the
// drive may take, and what the acknowledgement claims was sent.
type sealedDrive struct {
	tag     string
	body    []byte
	timeout time.Duration
	usageIn int64
}

// unstreamBody forces "stream":false and drops stream_options before a body is sealed to
// a station. The hub is submit/answer, not a byte stream: a station handed "stream":true
// returns SSE frames, which the JSON parser in writeBridgedAnswer cannot read - so a
// streaming consumer got an empty answer while the drive "succeeded" and settlement
// billed the framing bytes. The consumer still gets a stream; it is re-framed from the
// one JSON body on the way out. A body with neither key is passed through UNTOUCHED
// (byte-identical: the Tower sees exactly what the consumer sent, less the routing
// carriers the relay already stripped).
func unstreamBody(body []byte) []byte {
	kvs, ok := jsonObject(body)
	if !ok {
		return body
	}
	touch := false
	for _, kv := range kvs {
		if kv.key == "stream" || kv.key == "stream_options" {
			touch = true
		}
	}
	if !touch {
		return body
	}
	return rewriteBodyDrop(body, map[string]bool{"stream_options": true}, map[string]json.RawMessage{"stream": json.RawMessage("false")})
}

// driveSealed is the sealed loop the canary proved, shared with the bridge: seal to the
// station, submit through the Tower's hub, open the answer, verify the receipt binds to
// the opened bytes, acknowledge. Returns the opened answer (nil on any failure) and the
// canary-vocabulary outcome for the caller to interpret.
func (b *broker) driveSealed(grant dispatch.EdgeGrant, target dispatch.Target, endpoint, endpointPin string,
	consumerKey ed25519.PrivateKey, envPriv []byte, d sealedDrive) ([]byte, reputation.Outcome) {
	answer, outcome, _ := b.driveSealedF(grant, target, endpoint, endpointPin, consumerKey, envPriv, d)
	return answer, outcome
}

// driveSealedF is driveSealed returning the Tower station's failure class too, so a caller
// may surface the model's own status (a 429 and its Retry-After) instead of a generic miss.
func (b *broker) driveSealedF(grant dispatch.EdgeGrant, target dispatch.Target, endpoint, endpointPin string,
	consumerKey ed25519.PrivateKey, envPriv []byte, d sealedDrive) ([]byte, reputation.Outcome, string) {
	firstByte := time.Now()
	sealedReq, err := envelope.SealTo(target.SessionKey, d.body, grant.AttemptID)
	if err != nil {
		// The STATION'S: what failed is the session key the Station itself advertised on
		// its attachment, before the Tower was given the chance to do anything at all.
		log.Printf("%s: station %s on tower %s advertises a session key nothing can be sealed to: %v",
			d.tag, target.StationID, target.TowerID, err)
		return nil, reputation.StationFault, ""
	}
	sealedRaw, err := sealedReq.Marshal()
	if err != nil {
		log.Printf("%s: could not encode a submit for tower %s: %v", d.tag, target.TowerID, err)
		return nil, "", ""
	}
	if verr := endpointNotPublic(context.Background(), endpoint, b.canaryVet); verr != nil {
		log.Printf("%s: tower %s endpoint %s skipped: %v (unreachable by design, not a failure)",
			d.tag, target.TowerID, endpoint, verr)
		return nil, "", ""
	}
	base, httpc, err := towerhub.ReachVetted(endpoint, endpointPin, b.canaryVet)
	if err != nil {
		log.Printf("%s: tower %s advertises an unusable data plane: %v", d.tag, target.TowerID, err)
		return nil, reputation.CanaryFail, ""
	}
	hc := &towerhub.Client{BaseURL: base, HTTP: httpc}
	ctx, cancel := context.WithTimeout(context.Background(), d.timeout)
	defer cancel()
	res, err := hc.SubmitJob(ctx, grant.Signed, sealedRaw)
	if isDesignSkip(err) {
		log.Printf("%s: tower %s endpoint %s skipped at dial: %v", d.tag, target.TowerID, endpoint, err)
		return nil, "", ""
	}
	if err != nil || res.Failure != "" || len(res.Envelope) == 0 || len(res.Receipt) == 0 {
		return nil, reputation.CanaryFail, res.Failure
	}
	parsed, err := envelope.Parse(res.Envelope)
	if err != nil {
		return nil, reputation.CanaryFail, ""
	}
	answer, err := envelope.OpenWith(envPriv, parsed, grant.AttemptID)
	if err != nil || len(answer) == 0 {
		return nil, reputation.CanaryFail, ""
	}
	rec, err := dispatch.ParseReceipt(res.Receipt, target.AssertionKey, link.PublicNetwork,
		grant.AttemptID, target.StationID)
	if err != nil || rec.ResponseDigest == "" {
		return nil, reputation.CanaryFail, ""
	}
	if rec.ResponseDigest != dispatch.DigestOf(answer) {
		return nil, reputation.CanaryFail, ""
	}
	if ts := b.tower; ts != nil && ts.acks != nil {
		if ack, aerr := dispatch.SignAck(consumerKey, link.PublicNetwork, grant.AttemptID,
			answer, dispatch.Usage{In: d.usageIn, Out: int64(len(answer))}, firstByte, time.Now()); aerr == nil {
			_ = ts.acks.Put(grant.AttemptID, ack)
		}
	}
	return answer, reputation.CanaryPass, ""
}

// edgeRowPrice converts a routable row's price (micro-USD per 1M tokens, in OR out) to the
// credits-per-1M-tokens figure the consumer's X-Roger-Max-Price ceilings are expressed in,
// so the bridge compares like with like against either cap.
func edgeRowPrice(micros int64) float64 { return float64(micros) / 1e6 }
