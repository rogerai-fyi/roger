package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// audioRelay handles POST /v1/audio/speech (TTS). Same money spine as relay(), metered by the
// EXACT input characters (Unicode runes) instead of tokens: because the char count is known up
// front, the hold equals the final charge (no recount). Routes ONLY to tts nodes; empty input is
// refused before any hold; a paid voice with no funded wallet is 403; the response is the node's
// audio bytes with the same X-RogerAI-* meter headers as chat. See VOICE-AUDIO-DESIGN.md.
func (b *broker) audioRelay(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodPost) {
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))

	// --- Auth: a grant key, else a signed identity (identical to the chat relay). ---
	gc, gok, gerr := b.resolveGrant(r)
	if gerr != "" {
		jsonErr(w, http.StatusUnauthorized, gerr)
		return
	}
	var user, wallet string
	if gok {
		user, wallet = gc.wallet, gc.wallet
	} else {
		u, authed, iok := b.identityOf(r, body)
		if !iok {
			jsonErr(w, http.StatusUnauthorized, "invalid request signature")
			return
		}
		user, wallet = u, b.walletOf(r, u)
		if !authed {
			jsonErr(w, http.StatusUnauthorized, "spending requires a signed request")
			return
		}
	}

	// --- Rate limit (grant bucket, else per-IP for anon + per-user). ---
	if gok {
		if ok, retry := b.grantRL.allowAt(gc.grant.ID, gc.grant.RPM, gc.grant.Burst); !ok {
			w.Header().Set("Retry-After", strconv.Itoa(retry))
			jsonErr(w, http.StatusTooManyRequests, "grant rate limit exceeded - slow down")
			return
		}
	} else {
		if user == "anon" {
			if ok, retry := b.anonRL.allow(clientIP(r)); !ok {
				w.Header().Set("Retry-After", strconv.Itoa(retry))
				jsonErr(w, http.StatusTooManyRequests, "rate limit exceeded - slow down")
				return
			}
		}
		if ok, retry := b.rl.allow(user); !ok {
			w.Header().Set("Retry-After", strconv.Itoa(retry))
			jsonErr(w, http.StatusTooManyRequests, "rate limit exceeded - slow down")
			return
		}
	}

	var req struct {
		Model string `json:"model"`
		Input string `json:"input"`
	}
	_ = json.Unmarshal(body, &req)

	// D2: empty / whitespace-only input is refused BEFORE any hold - no charge for nothing.
	chars := len([]rune(strings.TrimSpace(req.Input)))
	if chars == 0 {
		jsonErr(w, http.StatusBadRequest, "empty input")
		return
	}

	if gok {
		if st, msg := b.grantCapCheck(gc.grant); st != 0 {
			jsonErr(w, st, msg)
			return
		}
	}
	// Moderation screens the text to be spoken, before any node is paid.
	if res := b.mod.screen(req.Input); !res.allow() {
		if res.csam {
			b.preserveCSAM(b.pseudonym(user, "audio"), clientIP(r), res.category, body)
		}
		jsonErr(w, res.status, res.msg)
		return
	}

	// --- Route to a TTS node ONLY (modality isolation via pickFor). ---
	requestID := protocol.NewRequestID()
	b.mu.Lock()
	node, offer, ok := b.pickFor(req.Model, false, 0, 0, 0, "", nil, nil, nil,
		pickReq{modality: protocol.ModalityTTS, rng: seededRand(requestID)})
	t := b.tunnels[node.NodeID]
	b.mu.Unlock()
	if !ok || t == nil {
		jsonErr(w, http.StatusServiceUnavailable, "no voice station on air for "+req.Model)
		return
	}

	pricing := b.resolvePricing(gc, gok, user, wallet, node, offer)
	payer := pricing.payer
	grantID := ""
	if gok {
		grantID = gc.grant.ID
	}

	// Per-1M-char price -> this request's exact cost (chars is the broker's count, never a node
	// claim). A price-0 / free-window offer is $0.
	pin := pricing.in
	if !pricing.fixed {
		ain, _, afree, _ := offer.ActivePrice(time.Now())
		pin = ain
		if afree {
			pin = 0
		}
	}
	cost := float64(chars) * pin / 1e6
	if pricing.free {
		cost = 0
	}

	// A PAID voice with no funded wallet -> 403 (the app shows "Sign in to use this voice").
	if !gok && cost > 0 && !walletLoggedIn(payer) {
		jsonErr(w, http.StatusForbidden, "sign in to use this voice")
		return
	}

	// Hold the exact char cost before dispatch (hold == finalize; count known up front).
	settled := false
	if cost > 0 {
		if st, msg := b.monthlyCapCheck(w, payer, cost, time.Now()); st != 0 {
			jsonErr(w, st, msg)
			return
		}
		b.ensureSeeded(payer)
		held, herr := b.db.HoldFor(payer, requestID, cost)
		if herr != nil {
			jsonErr(w, http.StatusInternalServerError, "wallet error")
			return
		}
		if !held {
			jsonErr(w, http.StatusPaymentRequired, "insufficient balance - add funds")
			return
		}
		defer func() {
			if !settled {
				b.db.ReleaseHoldFor(payer, requestID)
			}
		}()
	}

	// Dispatch to the node's bridge + await the audio result.
	job := protocol.Job{ID: requestID, User: b.pseudonym(user, node.NodeID), Body: body}
	resCh := make(chan protocol.JobResult, 1)
	t.mu.Lock()
	t.waiters[job.ID] = resCh
	t.mu.Unlock()
	defer func() { t.mu.Lock(); delete(t.waiters, job.ID); t.mu.Unlock() }()
	select {
	case t.jobs <- job:
	case <-time.After(3 * time.Second):
		jsonErr(w, http.StatusServiceUnavailable, "voice station busy")
		return
	}

	select {
	case res := <-resCh:
		if res.Status < 200 || res.Status >= 400 || len(res.Body) == 0 {
			jsonErr(w, http.StatusBadGateway, "voice station returned no audio") // hold refunds via defer
			return
		}
		rec := res.Receipt
		rec.PriceIn, rec.GrantID = pin, grantID
		rec.SignBroker(b.priv)
		newBal := 0.0
		if cost > 0 {
			nb, ferr := b.settleRequest(payer, node.NodeID, cost, cost, rec, grantID, pricing.free)
			if ferr != nil {
				log.Printf("audio settle FAILED user=%s node=%s: %v", user, node.NodeID, ferr)
			} else {
				settled, newBal = true, nb
			}
		} else {
			settled = true
		}
		w.Header().Set("X-RogerAI-Provider", node.NodeID)
		w.Header().Set("X-RogerAI-Cost", fmtCostHeader(cost))
		if cost > 0 {
			w.Header().Set("X-RogerAI-Balance", ftoa(round6(newBal)))
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(res.Body)
	case <-time.After(nonStreamRelayWait):
		jsonErr(w, http.StatusGatewayTimeout, "voice station timed out")
	}
}
