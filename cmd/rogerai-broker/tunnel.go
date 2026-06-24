package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// nodeTunnel is the broker's per-node relay state: a buffered job queue the node
// long-polls, and the set of result waiters keyed by job id. The token is the
// node's Bearer BridgeToken, checked on every poll/result/stream call.
type nodeTunnel struct {
	jobs    chan protocol.Job
	mu      sync.Mutex
	waiters map[string]chan protocol.JobResult
	token   string
}

// streamSink is the waiting client connection a node streams SSE chunks into.
// cap (when non-nil) accumulates the assistant completion text from the SSE
// chunks so the broker can run its L1 token re-count at stream end (off the hot
// path). Guarded by capMu since agentStream writes it while relayStream reads it.
type streamSink struct {
	w      http.ResponseWriter
	flush  func()
	capMu  sync.Mutex
	cap    *bytes.Buffer
	capRaw bytes.Buffer // carry for SSE lines split across reads
}

// register handles POST /nodes/register: a node announces itself + its offers
// (and an optional confidential attestation). Idempotent; refreshes on reconnect.
func (b *broker) register(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodPost) {
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var reg protocol.NodeRegistration
	if err := json.Unmarshal(body, &reg); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad registration")
		return
	}
	// Proof of possession: the registrant must sign with the private key matching
	// the pub_key it claims, and the registration must be fresh (anti-replay). This
	// stops anyone from registering under a key (or a node id) they do not own.
	if reg.NodeID == "" || reg.PubKey == "" {
		jsonErr(w, http.StatusBadRequest, "node_id and pub_key required")
		return
	}
	if !reg.VerifyRegistration() {
		jsonErr(w, http.StatusUnauthorized, "registration signature invalid (prove possession of pub_key)")
		return
	}
	if skew := time.Since(time.Unix(reg.TS, 0)); skew > 5*time.Minute || skew < -5*time.Minute {
		jsonErr(w, http.StatusUnauthorized, "registration timestamp stale or skewed")
		return
	}
	// Login-to-monetize: a node advertising a NONZERO price is an earning node, which
	// requires a GitHub-linked owner bound to the user signing key on this request.
	// Free/zero-priced supply (and unsigned registrations) are unaffected, so the
	// consume path and free sharing never need login.
	if offersPriced(reg.Offers) {
		uid, authed, sok := b.identityOf(r, body)
		if !sok {
			jsonErr(w, http.StatusUnauthorized, "invalid request signature")
			return
		}
		if !authed {
			jsonErr(w, http.StatusUnauthorized, "earning (priced) node registration requires `rogerai login` (a GitHub-linked owner)")
			return
		}
		owner, ok := b.requireOwner(r)
		if !ok {
			jsonErr(w, http.StatusForbidden, "earning (priced) node registration requires a GitHub-linked owner - run `rogerai login`")
			return
		}
		_ = uid
		// Attribute this node's future earnings to the owner account (TOFU: the first
		// account to register a node id owns it), so earning lots + payouts resolve.
		_ = b.db.BindNode(reg.NodeID, owner.Pubkey)
	}
	b.mu.Lock()
	// TOFU identity binding: a node_id belongs to the first pub_key that claims it;
	// later registrations for that id must use the SAME key (no takeover).
	if prev, ok := b.nodes[reg.NodeID]; ok && prev.PubKey != reg.PubKey {
		b.mu.Unlock()
		jsonErr(w, http.StatusForbidden, "node_id already bound to a different key")
		return
	}
	b.nodes[reg.NodeID] = reg
	b.lastSeen[reg.NodeID] = time.Now()
	b.confidential[reg.NodeID] = reg.Confidential && verifyAttestation(reg.Attestation)
	if t := b.tunnels[reg.NodeID]; t == nil {
		b.tunnels[reg.NodeID] = &nodeTunnel{jobs: make(chan protocol.Job, 64), waiters: map[string]chan protocol.JobResult{}, token: reg.BridgeToken}
	} else {
		t.token = reg.BridgeToken
	}
	b.mu.Unlock()
	log.Printf("registered node %s (%d offers, %s)", reg.NodeID, len(reg.Offers), reg.HW)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// offersPriced reports whether any offer advertises a nonzero price (in its base
// price or in any scheduled window) - i.e. the node intends to EARN. A purely free
// node (all prices zero, only Free windows) is not gated on login.
func offersPriced(offers []protocol.ModelOffer) bool {
	for _, o := range offers {
		if o.PriceIn > 0 || o.PriceOut > 0 {
			return true
		}
		for _, w := range o.Schedule {
			if !w.Free && (w.In > 0 || w.Out > 0) {
				return true
			}
		}
	}
	return false
}

// heartbeat handles POST /nodes/heartbeat: keeps a node marked online (~35s TTL).
// Authenticated by the node's Bearer BridgeToken (like agentPoll/agentResult): an
// unsigned or forged node_id can no longer keep a node "online" or refresh another
// node's TTL. The body is bounded (a heartbeat is a few bytes of JSON).
func (b *broker) heartbeat(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodPost) {
		return
	}
	var m struct {
		NodeID string `json:"node_id"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&m)
	if m.NodeID == "" {
		jsonErr(w, http.StatusBadRequest, "missing node_id")
		return
	}
	b.mu.Lock()
	t := b.tunnels[m.NodeID]
	b.mu.Unlock()
	if t == nil {
		jsonErr(w, http.StatusNotFound, "unknown node")
		return
	}
	if !authNode(r, t.token) {
		jsonErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	b.mu.Lock()
	b.lastSeen[m.NodeID] = time.Now()
	b.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// agentPoll handles GET /agent/poll?node=<id>: a node long-polls (held up to 25s)
// for a relayed job. Authenticated by the node's Bearer BridgeToken. 204 = re-poll.
func (b *broker) agentPoll(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodGet) {
		return
	}
	node := r.URL.Query().Get("node")
	b.mu.Lock()
	t := b.tunnels[node]
	b.mu.Unlock()
	if t == nil {
		jsonErr(w, http.StatusNotFound, "unknown node")
		return
	}
	if !authNode(r, t.token) {
		jsonErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	b.mu.Lock()
	b.lastSeen[node] = time.Now()
	b.mu.Unlock()
	select {
	case job := <-t.jobs:
		_ = json.NewEncoder(w).Encode(job)
	case <-time.After(25 * time.Second):
		w.WriteHeader(http.StatusNoContent) // re-poll
	}
}

// agentResult handles POST /agent/result?node=<id>: the node returns a served
// job's result + signed receipt. Authenticated by the node's Bearer BridgeToken.
func (b *broker) agentResult(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodPost) {
		return
	}
	node := r.URL.Query().Get("node")
	b.mu.Lock()
	t := b.tunnels[node]
	b.mu.Unlock()
	if t == nil {
		jsonErr(w, http.StatusNotFound, "unknown node")
		return
	}
	if !authNode(r, t.token) {
		jsonErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var res protocol.JobResult
	if err := json.NewDecoder(r.Body).Decode(&res); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad result")
		return
	}
	t.mu.Lock()
	ch := t.waiters[res.ID]
	t.mu.Unlock()
	if ch != nil {
		select {
		case ch <- res:
		default:
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// relay handles POST /v1/chat/completions - the OpenAI-compatible entry point. It
// matches a node (price + constraint headers), relays via the job tunnel, verifies
// and co-signs the lineage receipt, meters throughput, and settles the wallet.
func (b *broker) relay(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodPost) {
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))

	// Grant path FIRST: a `Bearer rog-grant_...` is its own authentication (the
	// owner-minted secret), so it skips the signed-identity requirement entirely and
	// resolves to a grant-scoped wallet + the issuing owner's nodes. See grant.go.
	gc, gok, gerr := b.resolveGrant(r)
	if gerr != "" {
		jsonErr(w, http.StatusUnauthorized, gerr)
		return
	}

	var user string   // the signed identity (pubkey-derived; drives self-use + price-lock)
	var wallet string // the MONEY key: github-scoped when logged in, else == user
	var authed bool
	if gok {
		user = gc.wallet // "g_<id>" grant-scoped wallet (reservedID-protected)
		wallet = user
	} else {
		var iok bool
		user, authed, iok = b.identityOf(r, body)
		if !iok {
			jsonErr(w, http.StatusUnauthorized, "invalid request signature")
			return
		}
		// One wallet per account: a logged-in keypair resolves to the SAME
		// "u_gh_<githubID>" wallet the web session uses; an unbound keypair keeps its
		// anonymous pubkey-derived id (no balance - see the paid-request gate below).
		wallet = b.walletOf(r, user)
		// Spending REQUIRES a verified (signed) identity: an unsigned legacy request can
		// never spend a wallet. This enforces the core P0 invariant directly on the spend
		// path (not just via the reserved-id guard in identityOf).
		if !authed {
			jsonErr(w, http.StatusUnauthorized, "spending requires a signed request (update to a recent `rogerai` build)")
			return
		}
	}
	// Per-caller rate limit: smooth bursts + cap sustained rate so one caller can't
	// flood the broker or a provider. Checked before the costly moderation/pick. A
	// grant uses its own bucket map keyed by grant id, with the grant's rpm/burst.
	if gok {
		if ok, retry := b.grantRL.allowAt(gc.grant.ID, gc.grant.RPM, gc.grant.Burst); !ok {
			w.Header().Set("Retry-After", strconv.Itoa(retry))
			jsonErr(w, http.StatusTooManyRequests, "grant rate limit exceeded - slow down")
			return
		}
	} else if ok, retry := b.rl.allow(user); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		jsonErr(w, http.StatusTooManyRequests, "rate limit exceeded - slow down")
		return
	}
	var req struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	_ = json.Unmarshal(body, &req)

	// Grant token caps (daily/monthly) - checked before dispatch, denied at 429.
	if gok {
		if st, msg := b.grantCapCheck(gc.grant); st != 0 {
			jsonErr(w, st, msg)
			return
		}
	}

	// Mandatory pre-dispatch content screen: an illegal prompt is blocked HERE,
	// before it reaches any provider. Off by default in dev; required + fail-closed
	// for launch (see moderation.go). Grants do NOT bypass it (owner's legal
	// exposure on shared access). Covers streaming too (this is before the branch).
	if st, msg := b.mod.screen(promptText(body)); st != 0 {
		log.Printf("moderation reject model=%s status=%d: %s", req.Model, st, msg)
		jsonErr(w, st, msg)
		return
	}

	confidentialOnly := r.Header.Get("X-Roger-Confidential") != ""
	minTPS := parseFloat(r.Header.Get("X-Roger-Min-TPS"))
	maxPrice := parseFloat(r.Header.Get("X-Roger-Max-Price"))
	maxPriceOut := parseFloat(r.Header.Get("X-Roger-Max-Price-Out"))
	// Client-side failover hints: pin to a specific node, and/or skip nodes that
	// just failed for this caller (comma-separated). These let the connector route
	// AROUND a dropped provider without the broker re-handing it the same one.
	pinNode := r.Header.Get("X-Roger-Node")
	exclude := parseNodeSet(r.Header.Get("X-Roger-Exclude-Nodes"))
	// A grant confines routing to the issuing owner's nodes (intersected with the
	// grant's node/model allow-lists) - it can never reach another owner's hardware.
	var allow map[string]bool
	if gok {
		allow = gc.nodeAllow
		if len(allow) == 0 {
			jsonErr(w, http.StatusServiceUnavailable, "no node of this grant's owner is serving right now")
			return
		}
		if gc.modelDenied(req.Model) {
			jsonErr(w, http.StatusForbidden, "this grant does not allow model "+req.Model)
			return
		}
	}
	b.mu.Lock()
	node, offer, ok := b.pick(req.Model, confidentialOnly, minTPS, maxPrice, maxPriceOut, pinNode, exclude, allow)
	t := b.tunnels[node.NodeID]
	b.mu.Unlock()
	if !ok || t == nil {
		msg := "no node offers " + req.Model
		if gok {
			msg = "no node of this grant's owner is serving " + req.Model + " right now"
		} else if confidentialOnly {
			msg += " on a confidential node"
		}
		jsonErr(w, http.StatusServiceUnavailable, msg)
		return
	}

	// Resolve the price + payer for this request. Grant: the grant's price (free/self
	// = 0/0, owner-sponsored otherwise). Signed self-use: $0 when the caller-owner
	// owns the picked node. Public: the offer's active market price billed to the
	// resolved account wallet.
	pricing := b.resolvePricing(gc, gok, user, wallet, node, offer)
	payer := pricing.payer
	grantID := ""
	if gok {
		grantID = gc.grant.ID
	}

	// Anonymous = free models + grant keys only, no balance. A not-logged-in keypair
	// hitting a PAID public model is rejected here with a clear login prompt (we never
	// silently seed an anon wallet to spend). Free models, self-use, and grants are
	// unaffected: this fires only for a public, priced offer billed to an anon wallet.
	if !gok && !pricing.free && !walletLoggedIn(payer) {
		ain, aout, afree, _ := offer.ActivePrice(time.Now())
		if !afree && (ain > 0 || aout > 0) {
			jsonErr(w, http.StatusUnauthorized, "log in to spend on paid models - run `rogerai login` (free models and grant keys work without an account)")
			return
		}
	}

	// Pre-authorize an upper-bound cost (a "hold") BEFORE doing any work, so
	// concurrent requests can never drive a wallet negative (free inference). The
	// hold is captured (Finalize) or returned (ReleaseHold) on every exit path. A
	// $0 (free/self) request places no hold - there is nothing to protect.
	maxCost := estimateMaxCost(body, pricing.in, pricing.out, offer.Ctx)
	if pricing.free {
		maxCost = 0
	}
	if maxCost > 0 {
		_, _ = b.db.BalanceOf(payer, b.seedFunds) // seed new users so the hold can land
		held, herr := b.db.Hold(payer, maxCost)
		if herr != nil {
			jsonErr(w, http.StatusInternalServerError, "wallet error")
			return
		}
		if !held {
			msg := "insufficient balance - add funds"
			if gok {
				msg = "top up to keep sponsoring this grant, or make it --free"
			}
			jsonErr(w, http.StatusPaymentRequired, msg)
			return
		}
	}

	// The provider never sees the real user identity - only a pseudonym that is
	// stable per (user, node) so the owner can count repeat customers but cannot
	// link a person, nor correlate the same user across different providers.
	job := protocol.Job{ID: protocol.NewRequestID(), User: b.pseudonym(user, node.NodeID), Body: body}
	resCh := make(chan protocol.JobResult, 1)
	t.mu.Lock()
	t.waiters[job.ID] = resCh
	t.mu.Unlock()
	defer func() { t.mu.Lock(); delete(t.waiters, job.ID); t.mu.Unlock() }()

	if req.Stream {
		b.relayStream(w, t, node, offer, streamBill{user: payer, model: req.Model, pricing: pricing, grantID: grantID}, job, resCh, maxCost)
		return
	}

	settled := false
	defer func() {
		if !settled && maxCost > 0 {
			b.db.ReleaseHold(payer, maxCost) // refund the hold if we never captured it
		}
	}()

	start := time.Now()
	b.enterInflight(node.NodeID)
	select {
	case t.jobs <- job:
	case <-time.After(3 * time.Second):
		b.exitInflight(node.NodeID, false)
		jsonErr(w, http.StatusServiceUnavailable, "node busy (no poller free)")
		return
	}

	select {
	case res := <-resCh:
		b.exitInflight(node.NodeID, res.Status < 500)
		rec := res.Receipt
		if rec.VerifyNode(node.PubKey) {
			// Resolve the billed price for this request. Free/self -> 0/0 (metering
			// only). Grant -> the grant's price. Public -> the price the user was
			// first quoted for this node+model (lockWin), so owners can't raise
			// mid-engagement.
			var pin, pout float64
			var until time.Time
			if pricing.fixed {
				pin, pout = pricing.in, pricing.out
			} else {
				curIn, curOut, _, scheduled := offer.ActivePrice(time.Now())
				if scheduled {
					// published time-of-use / free price - charge as-is, never pin it
					// (otherwise first contact in a free window would lock $0 for 24h).
					pin, pout = curIn, curOut
				} else {
					// base price in effect - protect from owner hikes for the lock window
					pin, pout, until = b.lockedPrice(user, node.NodeID, req.Model, curIn, curOut)
				}
			}
			rec.PriceIn, rec.PriceOut = pin, pout
			rec.GrantID = grantID
			rec.SignBroker(b.priv)
			cost := rec.Cost()
			if maxCost > 0 && cost > maxCost {
				cost = maxCost // never capture more than was authorized
			}
			newBal, ferr := b.settleRequest(payer, node.NodeID, maxCost, cost, rec, grantID, pricing.free)
			if ferr != nil {
				// Settle failed - leave settled=false so the deferred ReleaseHold
				// refunds the user in full (fail safe toward the customer) and emit no
				// billing headers; the completion body is still returned below.
				log.Printf("relay settle FAILED user=%s node=%s: %v - releasing hold", user, node.NodeID, ferr)
			} else {
				settled = true
				tps := 0.0
				if rec.CompletionTokens > 0 {
					if el := time.Since(start).Seconds(); el > 0 {
						tps = float64(rec.CompletionTokens) / el
						b.updateTPS(node.NodeID, tps)
					}
				}
				w.Header().Set("X-RogerAI-Receipt", protocol.EncodeReceipt(rec))
				w.Header().Set("X-RogerAI-Provider", node.NodeID)
				w.Header().Set("X-RogerAI-Cost", ftoa(round6(cost)))
				w.Header().Set("X-RogerAI-Balance", ftoa(round6(newBal)))
				lockedUntil := int64(0)
				if !until.IsZero() {
					lockedUntil = until.Unix()
				}
				w.Header().Set("X-RogerAI-Price", fmt.Sprintf("in=%.4f;out=%.4f;locked_until=%d", pin, pout, lockedUntil))
				w.Header().Set("X-RogerAI-TPS", fmt.Sprintf("%.1f", tps))
				w.Header().Set("X-RogerAI-Quality", ftoa(round6(b.trustScore(node.NodeID))))
				log.Printf("relay user=%s node=%s in=%d out=%d price=%.3f/%.3f cost=%.6f tps=%.1f", user, node.NodeID, rec.PromptTokens, rec.CompletionTokens, pin, pout, cost, tps)
				// L1 independent re-count, OFF the hot path: reconcile the node's
				// claimed completion tokens against our own tokenizer count.
				go b.recountAsync(node.NodeID, recountModel(rec, req.Model), completionText(res.Body), rec.CompletionTokens)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(res.Status)
		_, _ = w.Write(res.Body)
	case <-time.After(120 * time.Second):
		b.exitInflight(node.NodeID, false)
		jsonErr(w, http.StatusGatewayTimeout, "node timed out")
	}
}

// relayStream handles the streaming path of POST /v1/chat/completions: it sends SSE
// headers, registers the client as a sink, and enqueues the job. The node pipes
// chunks via /agent/stream straight to this client; when it finishes it posts a
// receipt (resCh) which settles the wallet. No metering headers (already streaming).
func (b *broker) relayStream(w http.ResponseWriter, t *nodeTunnel, node protocol.NodeRegistration, offer protocol.ModelOffer, bill streamBill, job protocol.Job, resCh chan protocol.JobResult, maxCost float64) {
	user, model, pricing, grantID := bill.user, bill.model, bill.pricing, bill.grantID
	settled := false
	defer func() {
		if !settled && maxCost > 0 {
			b.db.ReleaseHold(user, maxCost) // refund the hold if we never captured it
		}
	}()
	flusher, ok := w.(http.Flusher)
	if !ok {
		jsonErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	sink := &streamSink{w: w, flush: flusher.Flush}
	if b.recount.enabled() {
		sink.cap = &bytes.Buffer{} // capture completion text for the L1 re-count
	}
	b.streamMu.Lock()
	b.streams[job.ID] = sink
	b.streamMu.Unlock()
	defer func() { b.streamMu.Lock(); delete(b.streams, job.ID); b.streamMu.Unlock() }()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-RogerAI-Provider", node.NodeID)
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	start := time.Now()
	b.enterInflight(node.NodeID)
	select {
	case t.jobs <- job:
	case <-time.After(3 * time.Second):
		b.exitInflight(node.NodeID, false)
		return // headers already sent; the client just gets an empty stream
	}
	select {
	case res := <-resCh:
		b.exitInflight(node.NodeID, res.Status < 500)
		rec := res.Receipt
		if rec.VerifyNode(node.PubKey) {
			var pin, pout float64
			if pricing.fixed {
				pin, pout = pricing.in, pricing.out
			} else {
				curIn, curOut, _, scheduled := offer.ActivePrice(time.Now())
				pin, pout = curIn, curOut
				if !scheduled {
					pin, pout, _ = b.lockedPrice(user, node.NodeID, model, curIn, curOut)
				}
			}
			rec.PriceIn, rec.PriceOut = pin, pout
			rec.GrantID = grantID
			rec.SignBroker(b.priv)
			cost := rec.Cost()
			if maxCost > 0 && cost > maxCost {
				cost = maxCost
			}
			if _, ferr := b.settleRequest(user, node.NodeID, maxCost, cost, rec, grantID, pricing.free); ferr != nil {
				// settle failed - leave settled=false so the deferred ReleaseHold refunds
				log.Printf("stream settle FAILED user=%s node=%s: %v - releasing hold", user, node.NodeID, ferr)
			} else {
				settled = true
			}
			if rec.CompletionTokens > 0 {
				if el := time.Since(start).Seconds(); el > 0 {
					b.updateTPS(node.NodeID, float64(rec.CompletionTokens)/el)
				}
			}
			log.Printf("stream user=%s node=%s out=%d cost=%.6f", user, node.NodeID, rec.CompletionTokens, cost)
			// L1 independent re-count, OFF the hot path: reconcile the node's
			// claimed completion tokens against the text we captured streaming.
			if sink.cap != nil {
				sink.capMu.Lock()
				completion := sink.cap.String()
				sink.capMu.Unlock()
				go b.recountAsync(node.NodeID, recountModel(rec, model), completion, rec.CompletionTokens)
			}
		}
	case <-time.After(300 * time.Second):
		b.exitInflight(node.NodeID, false)
	}
}

// estimateMaxCost is the upper-bound credits a request could cost - used to place a
// pre-auth hold before dispatch. Output is bounded by max_tokens (capped to the
// model's ctx); the prompt is over-estimated from the body size. At the offer's
// active price, so the actual capture on settle is always <= this.
func estimateMaxCost(body []byte, in, out float64, ctx int) float64 {
	var req struct {
		MaxTokens int `json:"max_tokens"`
	}
	_ = json.Unmarshal(body, &req)
	capTok := ctx
	if capTok <= 0 {
		capTok = 8192
	}
	maxOut := req.MaxTokens
	if maxOut <= 0 || maxOut > capTok {
		maxOut = capTok
	}
	promptEst := len(body)/4 + 1 // ~chars/4 → tokens; body JSON over-estimates (safe)
	c := (float64(promptEst)*in + float64(maxOut)*out) / 1e6
	if c < 1e-6 {
		c = 1e-6 // floor so a hold is always placed
	}
	return c
}

// agentStream handles POST /agent/stream?node=&job= - the node pipes a job's SSE
// chunks here and the broker forwards them to the waiting client, flushing each.
func (b *broker) agentStream(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodPost) {
		return
	}
	nodeID := r.URL.Query().Get("node")
	jobID := r.URL.Query().Get("job")
	b.mu.Lock()
	t := b.tunnels[nodeID]
	b.mu.Unlock()
	if t == nil || !authNode(r, t.token) {
		jsonErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	b.streamMu.Lock()
	sink := b.streams[jobID]
	b.streamMu.Unlock()
	if sink == nil {
		jsonErr(w, http.StatusNotFound, "no active stream")
		return
	}
	buf := make([]byte, 8192)
	for {
		n, err := r.Body.Read(buf)
		if n > 0 {
			sink.w.Write(buf[:n])
			sink.flush()
			// Capture the streamed completion text (off-band, for the L1 re-count
			// at stream end). The bytes still go straight to the client above; this
			// only siphons a copy when capture is enabled.
			sink.capMu.Lock()
			if sink.cap != nil {
				sink.capRaw.Write(buf[:n])
				drainSSEDeltas(&sink.capRaw, sink.cap)
			}
			sink.capMu.Unlock()
		}
		if err != nil {
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// pick: cheapest-RIGHT-NOW online node offering the model, ranked by the active
// (time-of-use) OUTPUT price - what we headline and bill the most on, and what the
// client quotes, so the quoted station is the routed station; optionally restricted
// to confidential nodes. When pin
// is set, only that node is eligible (client failover pinning); nodes in exclude
// are skipped (the providers a client just saw fail). maxPriceIn / maxPriceOut are
// the user's spend caps: a station whose active input price exceeds maxPriceIn, or
// whose active OUTPUT price exceeds maxPriceOut, is filtered out (0 = no cap on
// that side). We bill primarily on output, so the out-price cap is the one the
// pricing UX surfaces; both are enforced. Caller holds lock.
func (b *broker) pick(model string, confidentialOnly bool, minTPS, maxPriceIn, maxPriceOut float64, pin string, exclude, allow map[string]bool) (protocol.NodeRegistration, protocol.ModelOffer, bool) {
	var best protocol.NodeRegistration
	var bestOffer protocol.ModelOffer
	bestPrice := 0.0
	found := false
	bestFailing := false // whether `best` is a probe-failing node (deprioritized)
	now := time.Now()
	for _, n := range b.nodes {
		if time.Since(b.lastSeen[n.NodeID]) >= 35*time.Second {
			continue
		}
		if pin != "" && n.NodeID != pin {
			continue
		}
		if exclude[n.NodeID] {
			continue
		}
		// allow (when set) confines the candidate set, e.g. a grant request may only
		// reach the issuing owner's nodes (server-derived, never caller-supplied).
		if allow != nil && !allow[n.NodeID] {
			continue
		}
		if confidentialOnly && !b.confidential[n.NodeID] {
			continue
		}
		// min-TPS: only exclude nodes we've MEASURED as too slow (unmeasured nodes
		// get a chance, so new providers aren't permanently locked out).
		if minTPS > 0 {
			if m, ok := b.tps[n.NodeID]; ok && m < minTPS {
				continue
			}
		}
		// Probe verification: a node failing recent canaries is DEPRIORITIZED -
		// only chosen if no healthy node offers the model (so a transient probe
		// failure never makes a model unavailable). See probe.go.
		failing := b.probeFailing(n.NodeID)
		for _, o := range n.Offers {
			if o.Model != model {
				continue
			}
			in, out, _, _ := o.ActivePrice(now)
			if maxPriceIn > 0 && in > maxPriceIn {
				continue
			}
			if maxPriceOut > 0 && out > maxPriceOut {
				continue
			}
			// Rank by active OUTPUT price: that is what we headline and bill the most
			// on, and what the client quotes at connect time, so the station the user
			// is shown is the station the broker routes to (quote == route). A healthy
			// node always beats a probe-failing one regardless of price; among equals
			// on health, cheapest-output wins.
			better := !found ||
				(bestFailing && !failing) || // healthy beats failing
				(bestFailing == failing && out < bestPrice) // same health: cheaper wins
			if better {
				best, bestOffer, bestPrice, found, bestFailing = n, o, out, true, failing
			}
		}
	}
	return best, bestOffer, found
}

// enterInflight / exitInflight track active requests per node (concurrency-safe).
// exit also folds the outcome into the node's success-rate EWMA.
func (b *broker) enterInflight(node string) {
	b.metricsMu.Lock()
	b.inflight[node]++
	b.metricsMu.Unlock()
}

func (b *broker) exitInflight(node string, ok bool) {
	b.metricsMu.Lock()
	if b.inflight[node] > 0 {
		b.inflight[node]--
	}
	sample := 0.0
	if ok {
		sample = 1.0
	}
	if cur, seen := b.success[node]; seen {
		b.success[node] = 0.2*sample + 0.8*cur
	} else {
		b.success[node] = sample
	}
	b.metricsMu.Unlock()
}

// updateTPS folds a throughput sample into the node's EWMA (output tokens/sec).
func (b *broker) updateTPS(node string, sample float64) {
	if sample <= 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if cur, ok := b.tps[node]; ok {
		b.tps[node] = 0.3*sample + 0.7*cur
	} else {
		b.tps[node] = sample
	}
}

// authNode checks a node-facing request's Bearer token against the node's
// registered BridgeToken (empty token never authorizes).
func authNode(r *http.Request, token string) bool {
	return token != "" && r.Header.Get("Authorization") == "Bearer "+token
}

func parseFloat(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

// drainSSEDeltas consumes COMPLETE newline-terminated lines from raw, appends
// any assistant delta text it finds to out, and leaves a trailing partial line
// in raw for the next read. Used to reconstruct the completion text from the SSE
// stream for the L1 re-count (off the hot path). Best-effort: a malformed chunk
// is skipped, never fatal.
func drainSSEDeltas(raw, out *bytes.Buffer) {
	data := raw.Bytes()
	last := bytes.LastIndexByte(data, '\n')
	if last < 0 {
		return // no complete line yet
	}
	complete := data[:last+1]
	for _, line := range bytes.Split(complete, []byte{'\n'}) {
		if t := sseDelta(line); t != "" {
			out.WriteString(t)
		}
	}
	// Keep the trailing partial line as the new carry.
	rest := append([]byte(nil), data[last+1:]...)
	raw.Reset()
	raw.Write(rest)
}

// sseDelta extracts the assistant content from one OpenAI streaming "data: {...}"
// SSE line (choices[].delta.content or choices[].text). Returns "" for keepalive
// lines, the [DONE] sentinel, or anything it can't parse.
func sseDelta(line []byte) string {
	i := bytes.IndexByte(line, '{')
	if i < 0 {
		return ""
	}
	var d struct {
		Choices []struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
			Text string `json:"text"`
		} `json:"choices"`
	}
	if json.Unmarshal(line[i:], &d) != nil {
		return ""
	}
	var s strings.Builder
	for _, c := range d.Choices {
		if c.Delta.Content != "" {
			s.WriteString(c.Delta.Content)
		} else if c.Text != "" {
			s.WriteString(c.Text)
		}
	}
	return s.String()
}

// parseNodeSet parses a comma-separated node-id list (X-Roger-Exclude-Nodes) into
// a set, ignoring empty entries. Returns nil for an empty header (no exclusions).
func parseNodeSet(s string) map[string]bool {
	if s == "" {
		return nil
	}
	set := map[string]bool{}
	for _, part := range strings.Split(s, ",") {
		if id := strings.TrimSpace(part); id != "" {
			set[id] = true
		}
	}
	return set
}
