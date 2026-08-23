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
	"math/rand"
	"net/http"
	"time"

	"rogerai.fm/roger/v6/internal/towercore/dispatch"
	"rogerai.fm/roger/v6/internal/towercore/envelope"
	"rogerai.fm/roger/v6/internal/towercore/link"
	"rogerai.fm/roger/v6/internal/towercore/reputation"
	"rogerai.fm/roger/v6/internal/towerhub"
)

// edgeBridgeTimeout bounds one bridged drive. Longer than the canary's, because a real
// prompt is real work; still finite, because the fallback behind it is the point.
const edgeBridgeTimeout = 120 * time.Second

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
	maxPriceOut      float64 // the global out-price ceiling, enforced against the tower band
	pinNode          string
	excludeNodes     map[string]bool
}

// soft is the both-fabrics mode: a direct node stands ready behind this call, so any
// gate that would refuse (auth, rate, slot, balance) returns false and lets the direct
// path serve instead of writing an edge-shaped refusal to a consumer the network could
// have served. Hard mode (edge-only) writes the honest refusal, because there is no one
// behind it.
func (b *broker) relayViaEdge(w http.ResponseWriter, r *http.Request, model string, stream bool, body []byte, rng *rand.Rand, soft bool, auth edgeBridgeAuth) bool {
	ts := b.tower
	if ts == nil || ts.dispatch == nil {
		return false
	}
	// A cheap eligibility probe before any consumer gating: if no eligible Tower hosts the
	// model there is nothing to say, and the caller's "no node offers" stays the answer.
	if _, _, ok := b.edgeTargetFor(model, rng, nil); !ok {
		return false
	}

	// A GRANT can never ride the edge. A grant is an owner's authorization to serve on
	// THEIR OWN hardware at their price; bridging it would relay it onto a stranger's
	// Tower and bill the grant wallet at the Tower's price. Direct-only, always.
	if auth.grant {
		return false // let the direct path answer - it is the only one a grant may use
	}
	// A browser-session caller (Playbox) has no device signature to bind an edge
	// acknowledgement to, so it cannot ride the sealed path. Direct-only.
	if auth.sessionAuthed {
		return false
	}
	// TOWER INFERENCE REQUIRES A SIGNED-IN ACCOUNT - the consent gate, checked against the
	// VERIFIED pubkey relay resolved, never a header read here. An anonymous caller is
	// told the truth (the model IS served, just not to the unsigned) rather than the
	// misleading "no node offers".
	o, found, oerr := b.db.OwnerByPubkey(auth.pubHex)
	if auth.pubHex == "" || oerr != nil || !found || o.Anonymized || b.isOwnerBanned(o.Pubkey) {
		if soft {
			return false
		}
		jsonErr(w, http.StatusForbidden, "tower inference requires a signed-in account that has accepted the terms of service")
		return true
	}
	// The wallet is the one relay resolved - not re-derived. It must match the account the
	// verified pubkey owns, or the caller is trying to bill an identity it did not prove.
	consumerWallet, cwok := accountWalletForOwner(o)
	if !cwok || consumerWallet != auth.wallet {
		if soft {
			return false
		}
		jsonErr(w, http.StatusForbidden, "tower inference requires a signed-in account that has accepted the terms of service")
		return true
	}
	// One identity, one bucket, one standing cap - identical to the authorize endpoint, so
	// the bridge is not a way around either bound.
	if allowed, retry := b.rl.allow("edge:" + consumerWallet); !allowed {
		if soft {
			return false
		}
		w.Header().Set("Retry-After", fmt.Sprintf("%d", retry))
		jsonErr(w, http.StatusTooManyRequests, "rate limit exceeded - slow down")
		return true
	}
	if !b.edgeAccountReserve(consumerWallet) {
		if soft {
			return false
		}
		w.Header().Set("Retry-After", "5")
		jsonErr(w, http.StatusTooManyRequests,
			"too many edge attempts open on this account at once - finish or abandon some before opening more")
		return true
	}
	slotHeld := true
	defer func() {
		if slotHeld {
			b.edgeAccountRelease(consumerWallet)
		}
	}()

	// A confidential-only request cannot ride the edge: the sealed hub protects the
	// PAYLOAD, but a Tower is a third party in the path, and confidential traffic promises
	// no third party. Direct-only.
	if auth.confidentialOnly {
		return false
	}
	// The caller's exclusions carry in, and a pinned node forces the direct path (an edge
	// pin names a station, a different namespace - honour it by declining the bridge).
	if auth.pinNode != "" {
		return false
	}
	exclude := map[string]bool{}
	for k := range auth.excludeNodes {
		exclude[k] = true
	}
	tries := edgeBridgeMaxTowers
	if soft {
		tries = 1 // a direct node is ready; do not spend the full budget on failing towers
	}
	for attempt := 0; attempt < tries; attempt++ {
		target, row, ok := b.edgeTargetFor(model, rng, exclude)
		if !ok {
			break
		}
		// The projection is not a security boundary: the price is re-checked against the
		// public band at the moment it becomes money, exactly as authorize does.
		if row.PriceIn != 0 || row.PriceOut != 0 {
			if floor, ceiling, bok := towerPriceBand(model); !bok ||
				row.PriceIn < floor || row.PriceIn > ceiling ||
				row.PriceOut < floor || row.PriceOut > ceiling {
				log.Printf("edge bridge: routable row for %s/%s carries an out-of-band price (%d/%d) - excluded",
					row.TowerID, row.StationID, row.PriceIn, row.PriceOut)
				exclude[row.TowerID] = true
				continue
			}
		}
		// THE CONSUMER'S OUT-PRICE CEILING, the same global cap the direct path enforces
		// in pickFor. A Tower whose price exceeds what the caller agreed to pay is excluded
		// - never silently billed above the cap.
		if auth.maxPriceOut > 0 && edgeRowPriceOut(row.PriceOut) > auth.maxPriceOut {
			exclude[row.TowerID] = true
			continue
		}
		// The bridge is the consumer's agent: it holds the ephemeral keys, exactly as the
		// canary does. The account is billed via the wallet on the hold and the inflight
		// ledger; the grant's consumer key only binds the acknowledgement, which the
		// bridge itself signs.
		_, consumerKey, err := ed25519.GenerateKey(crand.Reader)
		if err != nil {
			break
		}
		envPub, envPriv, err := envelope.NewKey()
		if err != nil {
			break
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
			exclude[target.TowerID] = true
			continue
		}
		// Paid traffic reserves the ceiling up front; settle captures the actual figure and
		// refunds the rest. Free traffic skips the hold. Same rule, same formula.
		maxCost := edgePriceCredits(g.MaxIn, g.MaxOut)
		if tc := tokenCostCredits(g.MaxTokIn, g.MaxTokOut, row.PriceIn, row.PriceOut); tc > maxCost {
			maxCost = tc
		}
		if maxCost > 0 {
			if hok, herr := b.db.HoldFor(consumerWallet, g.AttemptID, maxCost); herr != nil || !hok {
				if soft {
					return false
				}
				jsonErr(w, http.StatusPaymentRequired, "insufficient balance for this request")
				return true
			}
		}
		if err := b.openEdgeAttempt(g, target); err != nil {
			log.Printf("edge bridge: could not record attempt %s: %v", g.AttemptID, err)
			if maxCost > 0 {
				if _, rerr := b.db.ReleaseHoldFor(consumerWallet, g.AttemptID); rerr != nil {
					log.Printf("edge bridge: could not release hold for orphaned attempt %s: %v", g.AttemptID, rerr)
				}
			}
			exclude[target.TowerID] = true
			continue
		}
		b.edgeEnterInflight(g.AttemptID, row.NodeID, consumerWallet, g.Deadline)
		slotHeld = false // the ledger entry owns the slot from here

		answer, outcome := b.driveSealed(g, target, row.Endpoint, row.TLSSPKI, consumerKey, envPriv,
			sealedDrive{tag: "bridge", body: body, timeout: edgeBridgeTimeout, usageIn: int64(len(body))})
		if len(answer) > 0 {
			writeBridgedAnswer(w, g, answer, stream)
			return true
		}
		// FAILED: the hold is released now, not at the orphan sweep - a consumer whose
		// request we are about to serve elsewhere must not have funds pinned behind a dead
		// Tower. The failure is evidence on the Tower's record (organic, never a canary
		// count), and the Tower is excluded for the rest of this request.
		if maxCost > 0 {
			if _, rerr := b.db.ReleaseHoldFor(consumerWallet, g.AttemptID); rerr != nil {
				log.Printf("edge bridge: could not release hold for failed attempt %s: %v", g.AttemptID, rerr)
			}
		}
		b.edgeExitInflight(g.AttemptID)
		// A "" outcome is Core's own internal error or a by-design non-public-endpoint
		// skip - NOT the Tower's fault, so it records nothing. A real failure records the
		// station-attributable kind, so the canary rate - the suspension signal - stays
		// exactly what the canary alone measured.
		if outcome == reputation.CanaryFail {
			b.recordOutcome(target.TowerID, target.StationID, g.AttemptID, reputation.StationFault)
		}
		log.Printf("edge bridge: tower %s failed for %s (attempt %s) - trying the next relay",
			target.TowerID, model, g.AttemptID)
		exclude[target.TowerID] = true
	}
	return false
}

// writeBridgedAnswer hands the station's answer back in the contract shape the client
// already parses. The answer bytes ARE the upstream's OpenAI-shaped JSON - the station
// seals its upstream's response body verbatim - so the non-streamed path passes them
// through, and the streamed path wraps them as one delta chunk plus [DONE].
func writeBridgedAnswer(w http.ResponseWriter, g dispatch.EdgeGrant, answer []byte, stream bool) {
	var usage struct {
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	_ = json.Unmarshal(answer, &usage)
	// The cost basis is the grant's PINNED price over the answer's own usage - the same
	// numbers settlement will clamp the capture to. Reported per million, like the direct
	// path's price header.
	cost := float64(usage.Usage.PromptTokens)*float64(g.PriceInMicros)/1e12 +
		float64(usage.Usage.CompletionTokens)*float64(g.PriceOutMicros)/1e12
	w.Header().Set("X-RogerAI-Provider", g.RelayName)
	w.Header().Set("X-RogerAI-Relay", g.TowerID)
	w.Header().Set("X-RogerAI-Cost", fmtCostHeader(cost))
	w.Header().Set("X-RogerAI-Tokens-In", fmt.Sprintf("%d", usage.Usage.PromptTokens))
	w.Header().Set("X-RogerAI-Tokens-Out", fmt.Sprintf("%d", usage.Usage.CompletionTokens))
	if !stream {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(answer)
		return
	}
	// A streaming request served by the edge arrives whole - the hub is submit/answer, not
	// a byte stream - so the answer goes out as one well-formed SSE chunk. Honest about
	// the shape rather than pretending to token-stream.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	content := ""
	if len(usage.Choices) > 0 {
		content = usage.Choices[0].Message.Content
	}
	chunk, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{{"delta": map[string]string{"content": content}, "index": 0}},
		"usage": map[string]int{"prompt_tokens": usage.Usage.PromptTokens,
			"completion_tokens": usage.Usage.CompletionTokens},
	})
	fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// sealedDrive parameterizes one sealed submit: what rides in the envelope, how long the
// drive may take, and what the acknowledgement claims was sent.
type sealedDrive struct {
	tag     string
	body    []byte
	timeout time.Duration
	usageIn int64
}

// driveSealed is the sealed loop the canary proved, shared with the bridge: seal to the
// station, submit through the Tower's hub, open the answer, verify the receipt binds to
// the opened bytes, acknowledge. Returns the opened answer (nil on any failure) and the
// canary-vocabulary outcome for the caller to interpret.
func (b *broker) driveSealed(grant dispatch.EdgeGrant, target dispatch.Target, endpoint, endpointPin string,
	consumerKey ed25519.PrivateKey, envPriv []byte, d sealedDrive) ([]byte, reputation.Outcome) {
	firstByte := time.Now()
	sealedReq, err := envelope.SealTo(target.SessionKey, d.body, grant.AttemptID)
	if err != nil {
		// The STATION'S: what failed is the session key the Station itself advertised on
		// its attachment, before the Tower was given the chance to do anything at all.
		log.Printf("%s: station %s on tower %s advertises a session key nothing can be sealed to: %v",
			d.tag, target.StationID, target.TowerID, err)
		return nil, reputation.StationFault
	}
	sealedRaw, err := sealedReq.Marshal()
	if err != nil {
		log.Printf("%s: could not encode a submit for tower %s: %v", d.tag, target.TowerID, err)
		return nil, ""
	}
	if verr := endpointNotPublic(context.Background(), endpoint, b.canaryVet); verr != nil {
		log.Printf("%s: tower %s endpoint %s skipped: %v (unreachable by design, not a failure)",
			d.tag, target.TowerID, endpoint, verr)
		return nil, ""
	}
	base, httpc, err := towerhub.ReachVetted(endpoint, endpointPin, b.canaryVet)
	if err != nil {
		log.Printf("%s: tower %s advertises an unusable data plane: %v", d.tag, target.TowerID, err)
		return nil, reputation.CanaryFail
	}
	hc := &towerhub.Client{BaseURL: base, HTTP: httpc}
	ctx, cancel := context.WithTimeout(context.Background(), d.timeout)
	defer cancel()
	res, err := hc.SubmitJob(ctx, grant.Signed, sealedRaw)
	if isDesignSkip(err) {
		log.Printf("%s: tower %s endpoint %s skipped at dial: %v", d.tag, target.TowerID, endpoint, err)
		return nil, ""
	}
	if err != nil || res.Failure != "" || len(res.Envelope) == 0 || len(res.Receipt) == 0 {
		return nil, reputation.CanaryFail
	}
	parsed, err := envelope.Parse(res.Envelope)
	if err != nil {
		return nil, reputation.CanaryFail
	}
	answer, err := envelope.OpenWith(envPriv, parsed, grant.AttemptID)
	if err != nil || len(answer) == 0 {
		return nil, reputation.CanaryFail
	}
	rec, err := dispatch.ParseReceipt(res.Receipt, target.AssertionKey, link.PublicNetwork,
		grant.AttemptID, target.StationID)
	if err != nil || rec.ResponseDigest == "" {
		return nil, reputation.CanaryFail
	}
	if rec.ResponseDigest != dispatch.DigestOf(answer) {
		return nil, reputation.CanaryFail
	}
	if ts := b.tower; ts != nil && ts.acks != nil {
		if ack, aerr := dispatch.SignAck(consumerKey, link.PublicNetwork, grant.AttemptID,
			answer, dispatch.Usage{In: d.usageIn, Out: int64(len(answer))}, firstByte, time.Now()); aerr == nil {
			_ = ts.acks.Put(grant.AttemptID, ack)
		}
	}
	return answer, reputation.CanaryPass
}

// edgeRowPriceOut converts a routable row's out-price (micro-USD per 1M tokens) to the
// credits-per-1M-tokens figure the consumer's X-Roger-Max-Price-Out ceiling is expressed
// in, so the bridge compares like with like against that cap.
func edgeRowPriceOut(micros int64) float64 { return float64(micros) / 1e6 }
