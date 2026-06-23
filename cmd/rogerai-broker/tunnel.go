package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bownux/rogerai/internal/protocol"
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
type streamSink struct {
	w     http.ResponseWriter
	flush func()
}

// register handles POST /nodes/register: a node announces itself + its offers
// (and an optional confidential attestation). Idempotent; refreshes on reconnect.
func (b *broker) register(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodPost) {
		return
	}
	var reg protocol.NodeRegistration
	if err := json.NewDecoder(r.Body).Decode(&reg); err != nil {
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

// heartbeat handles POST /nodes/heartbeat: keeps a node marked online (~35s TTL).
func (b *broker) heartbeat(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodPost) {
		return
	}
	var m map[string]string
	_ = json.NewDecoder(r.Body).Decode(&m)
	b.mu.Lock()
	if id := m["node_id"]; id != "" {
		b.lastSeen[id] = time.Now()
	}
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
	user := userOf(r)
	body, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	var req struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	_ = json.Unmarshal(body, &req)

	confidentialOnly := r.Header.Get("X-Roger-Confidential") != ""
	minTPS := parseFloat(r.Header.Get("X-Roger-Min-TPS"))
	maxPrice := parseFloat(r.Header.Get("X-Roger-Max-Price"))
	// Client-side failover hints: pin to a specific node, and/or skip nodes that
	// just failed for this caller (comma-separated). These let the connector route
	// AROUND a dropped provider without the broker re-handing it the same one.
	pinNode := r.Header.Get("X-Roger-Node")
	exclude := parseNodeSet(r.Header.Get("X-Roger-Exclude-Nodes"))
	b.mu.Lock()
	node, offer, ok := b.pick(req.Model, confidentialOnly, minTPS, maxPrice, pinNode, exclude)
	t := b.tunnels[node.NodeID]
	b.mu.Unlock()
	if !ok || t == nil {
		msg := "no node offers " + req.Model
		if confidentialOnly {
			msg += " on a confidential node"
		}
		jsonErr(w, http.StatusServiceUnavailable, msg)
		return
	}

	// Pre-authorize an upper-bound cost (a "hold") BEFORE doing any work, so
	// concurrent requests can never drive a wallet negative (free inference). The
	// hold is captured (Finalize) or returned (ReleaseHold) on every exit path.
	holdIn, holdOut, _, _ := offer.ActivePrice(time.Now())
	maxCost := estimateMaxCost(body, holdIn, holdOut, offer.Ctx)
	_, _ = b.db.BalanceOf(user, b.seedFunds) // seed new users so the hold can land
	held, herr := b.db.Hold(user, maxCost)
	if herr != nil {
		jsonErr(w, http.StatusInternalServerError, "wallet error")
		return
	}
	if !held {
		jsonErr(w, http.StatusPaymentRequired, "insufficient credits")
		return
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
		b.relayStream(w, t, node, offer, user, req.Model, job, resCh, maxCost)
		return
	}

	settled := false
	defer func() {
		if !settled {
			b.db.ReleaseHold(user, maxCost) // refund the hold if we never captured it
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
			// Bill at the price the user was first quoted for this node+model
			// (honored for lockWin); owners can't raise mid-engagement.
			curIn, curOut, _, scheduled := offer.ActivePrice(time.Now())
			var pin, pout float64
			var until time.Time
			if scheduled {
				// published time-of-use / free price - charge as-is, never pin it
				// (otherwise first contact in a free window would lock $0 for 24h).
				pin, pout = curIn, curOut
			} else {
				// base price in effect - protect from owner hikes for the lock window
				pin, pout, until = b.lockedPrice(user, node.NodeID, req.Model, curIn, curOut)
			}
			rec.PriceIn, rec.PriceOut = pin, pout
			rec.SignBroker(b.priv)
			cost := rec.Cost()
			if cost > maxCost {
				cost = maxCost // never capture more than was authorized
			}
			newBal, ferr := b.db.Finalize(user, node.NodeID, maxCost, cost, cost*(1-b.feeRate), rec)
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
				log.Printf("relay user=%s node=%s in=%d out=%d price=%.3f/%.3f cost=%.6f tps=%.1f", user, node.NodeID, rec.PromptTokens, rec.CompletionTokens, pin, pout, cost, tps)
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
func (b *broker) relayStream(w http.ResponseWriter, t *nodeTunnel, node protocol.NodeRegistration, offer protocol.ModelOffer, user, model string, job protocol.Job, resCh chan protocol.JobResult, maxCost float64) {
	settled := false
	defer func() {
		if !settled {
			b.db.ReleaseHold(user, maxCost) // refund the hold if we never captured it
		}
	}()
	flusher, ok := w.(http.Flusher)
	if !ok {
		jsonErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	b.streamMu.Lock()
	b.streams[job.ID] = &streamSink{w: w, flush: flusher.Flush}
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
			curIn, curOut, _, scheduled := offer.ActivePrice(time.Now())
			pin, pout := curIn, curOut
			if !scheduled {
				pin, pout, _ = b.lockedPrice(user, node.NodeID, model, curIn, curOut)
			}
			rec.PriceIn, rec.PriceOut = pin, pout
			rec.SignBroker(b.priv)
			cost := rec.Cost()
			if cost > maxCost {
				cost = maxCost
			}
			if _, ferr := b.db.Finalize(user, node.NodeID, maxCost, cost, cost*(1-b.feeRate), rec); ferr != nil {
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
		}
		if err != nil {
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// pick: cheapest-RIGHT-NOW online node offering the model, ranked by the active
// (time-of-use) price; optionally restricted to confidential nodes. When pin is
// set, only that node is eligible (client failover pinning); nodes in exclude are
// skipped (the providers a client just saw fail). Caller holds lock.
func (b *broker) pick(model string, confidentialOnly bool, minTPS, maxPriceIn float64, pin string, exclude map[string]bool) (protocol.NodeRegistration, protocol.ModelOffer, bool) {
	var best protocol.NodeRegistration
	var bestOffer protocol.ModelOffer
	bestPrice := 0.0
	found := false
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
		for _, o := range n.Offers {
			if o.Model != model {
				continue
			}
			in, _, _, _ := o.ActivePrice(now)
			if maxPriceIn > 0 && in > maxPriceIn {
				continue
			}
			if !found || in < bestPrice {
				best, bestOffer, bestPrice, found = n, o, in, true
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
