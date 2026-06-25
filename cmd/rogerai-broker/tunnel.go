package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// nodeTTL is how long after a node's last heartbeat/poll it is still considered
// ON AIR. It is the single source of truth for liveness in pick + discover. Set a
// bit above the node's ~10s heartbeat cadence with headroom for a broker
// restart/redeploy window: a still-running provider keeps heartbeating every ~10s,
// so it re-confirms liveness against the re-hydrated registration within seconds of
// the broker coming back, WITHOUT re-registering. 45s tolerates ~4 missed beats /
// the redeploy gap while staying truthful (a genuinely dead node still ages out).
const nodeTTL = 45 * time.Second

// defaultMaxNodesPerOwner is the HARD per-owner on-air cap: how many nodes a single
// owner account may have SIMULTANEOUSLY on air (live within nodeTTL) across all of
// their machines. The server backstop so one account can't overwhelm the broker.
// Override with ROGERAI_MAX_NODES_PER_OWNER (0 disables the cap).
const defaultMaxNodesPerOwner = 20

// maxNodesPerOwnerLimit reads the per-owner on-air cap from the environment, falling
// back to the default. A negative value is ignored (keeps the default); 0 disables it.
func maxNodesPerOwnerLimit() int {
	if v := os.Getenv("ROGERAI_MAX_NODES_PER_OWNER"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return defaultMaxNodesPerOwner
}

// ownerOnAirCount counts how many of the owner's nodes are currently ON AIR (live
// within nodeTTL), EXCLUDING the node id `self` (so an idempotent re-register of an
// existing node is never counted as a new one). It resolves each live node's owner
// via the node_owner binding (b.db.AccountOfNode), so it spans all of the owner's
// machines. Caller holds b.mu.
func (b *broker) ownerOnAirCount(owner, self string) int {
	if owner == "" {
		return 0
	}
	n := 0
	now := time.Now()
	for id := range b.nodes {
		if id == self {
			continue // the node refreshing itself is not a NEW on-air node
		}
		if now.Sub(b.lastSeen[id]) >= nodeTTL {
			continue // aged out: no longer on air
		}
		if acct, ok, _ := b.db.AccountOfNode(id); ok && acct == owner {
			n++
		}
	}
	return n
}

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
	// Price-safety, operator side: a HARD ceiling on what a public station may charge,
	// so a fat-fingered or deterrent price can never land on the open market and burn a
	// consumer. Checked against EVERY offer's base AND scheduled-window prices. A station
	// that genuinely wants to be unreachable to the public should go --private (a hidden
	// freq-code band), not post an absurd public price - the copy steers there.
	if msg := registerPriceCeiling(reg.Offers); msg != "" {
		jsonErr(w, http.StatusBadRequest, msg)
		return
	}
	// Login-to-monetize / login-to-go-private: a node advertising a NONZERO price is
	// an earning node, AND a node going PRIVATE (its own discovery visibility is a
	// per-owner resource) both require a GitHub-linked owner bound to the signing key
	// on this request. Free PUBLIC supply (and unsigned registrations) are unaffected,
	// so the consume path and free public sharing never need login.
	var regOwner store.Owner // set when this register is owner-bound (priced OR private)
	if offersPriced(reg.Offers) || reg.Private {
		uid, authed, sok := b.identityOf(r, body)
		if !sok {
			jsonErr(w, http.StatusUnauthorized, "invalid request signature")
			return
		}
		if !authed {
			msg := "earning (priced) node registration requires `rogerai login` (a GitHub-linked owner)"
			if reg.Private {
				msg = "a private band requires `rogerai login` (anonymous private sharing is not allowed)"
			}
			jsonErr(w, http.StatusUnauthorized, msg)
			return
		}
		owner, ok := b.requireOwner(r)
		if !ok {
			msg := "earning (priced) node registration requires a GitHub-linked owner - run `rogerai login`"
			if reg.Private {
				msg = "a private band requires a GitHub-linked owner - run `rogerai login`"
			}
			jsonErr(w, http.StatusForbidden, msg)
			return
		}
		_ = uid
		regOwner = owner
		// Attribute this node's future earnings to the owner account (TOFU: the first
		// account to register a node id owns it), so earning lots + payouts resolve.
		_ = b.db.BindNode(reg.NodeID, owner.Pubkey)
	}
	// Real TEE attestation - done BEFORE taking b.mu so the signature-chain check and
	// (cached) AMD KDS fetch never hold the broker lock during network IO. A
	// confidential CLAIM is only honored after the quote's signature chain, single-use
	// nonce binding, and allowlisted launch measurement ALL verify. verifyRegistration
	// returns an error ONLY when ROGERAI_TEE_REQUIRE is set and a claimed quote fails -
	// then we reject the registration rather than silently downgrade it to standard.
	confidential, attErr := b.attest.verifyRegistration(r.Context(), reg)
	if attErr != nil {
		jsonErr(w, http.StatusForbidden, attErr.Error())
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
	// HARD per-owner on-air cap (the server backstop): an owner account may have at
	// most maxNodesPerOwner nodes SIMULTANEOUSLY on air across all their machines. Count
	// the owner's currently-live on-air nodes (within nodeTTL) EXCLUDING this node id, so
	// an idempotent re-register of an existing node never trips the cap (it is not a NEW
	// on-air node). Only owner-bound (priced OR private) registrations are attributable
	// and capped; free public supply has no owner and is not counted here. The
	// (limit+1)th node is rejected with a clear 4xx the share UX surfaces verbatim.
	if regOwner.Pubkey != "" && b.maxNodesPerOwner > 0 {
		if b.ownerOnAirCount(regOwner.Pubkey, reg.NodeID) >= b.maxNodesPerOwner {
			b.mu.Unlock()
			jsonErr(w, http.StatusTooManyRequests, fmt.Sprintf(
				"station limit reached: %d bands on air for this account - take one off air", b.maxNodesPerOwner))
			return
		}
	}
	now := time.Now()
	b.nodes[reg.NodeID] = reg
	b.lastSeen[reg.NodeID] = now
	b.confidential[reg.NodeID] = confidential
	// Re-apply the signed Private flag on EVERY register so it survives a broker
	// restart (the node re-asserts it) and a node can also go back PUBLIC by
	// re-registering with Private=false. The flag is part of regSigningBytes, so it
	// cannot be stripped/flipped by anyone but the node's own key. (Lazy-init the maps
	// so a minimally-constructed test broker doesn't panic on a nil map.)
	if b.private == nil {
		b.private = map[string]bool{}
	}
	if b.bandOf == nil {
		b.bandOf = map[string]string{}
	}
	b.private[reg.NodeID] = reg.Private
	if !reg.Private {
		delete(b.bandOf, reg.NodeID)
	}
	if b.attestedAt == nil {
		b.attestedAt = map[string]time.Time{}
	}
	if confidential {
		b.attestedAt[reg.NodeID] = now // start the re-attestation clock
	} else {
		delete(b.attestedAt, reg.NodeID)
	}
	if t := b.tunnels[reg.NodeID]; t == nil {
		b.tunnels[reg.NodeID] = &nodeTunnel{jobs: make(chan protocol.Job, 64), waiters: map[string]chan protocol.JobResult{}, token: reg.BridgeToken}
	} else {
		t.token = reg.BridgeToken
	}
	b.mu.Unlock()

	// Private band: ensure this node has a band (mint once, idempotent on re-register).
	// The secret frequency code is returned ONCE here, on the FIRST register that mints
	// it; every later register returns ONLY band_id (never the code again - this is what
	// makes the node's idempotent re-register safe to repeat without re-leaking). A free
	// cap of 1 active band per owner is enforced via CountActiveBands vs BandQuota inside
	// mintBandForNode. We never log the raw code (only band_id / cosmetic display).
	bandID, bandCode, bandDisplay := "", "", ""
	if reg.Private {
		existing, found, _ := b.db.BandByNode(reg.NodeID)
		if found && existing.Owner == regOwner.Pubkey && !existing.Revoked {
			bandID, bandDisplay = existing.ID, existing.CodeDisplay // re-register: id only, no code
		} else if found && existing.Owner != regOwner.Pubkey {
			jsonErr(w, http.StatusForbidden, "this node already has a private band owned by another account")
			return
		} else {
			band, code, cerr := b.mintBandForNode(regOwner, reg.NodeID)
			if cerr != "" {
				jsonErr(w, http.StatusForbidden, cerr)
				return
			}
			bandID, bandCode, bandDisplay = band.ID, code, band.CodeDisplay // shown ONCE
			log.Printf("minted private band %s for node %s (owner %s)", band.ID, reg.NodeID, regOwner.Login)
		}
		reg.BandID = bandID
		b.mu.Lock()
		b.bandOf[reg.NodeID] = bandID
		b.mu.Unlock()
	}

	// Persist the registration so a broker restart/redeploy RE-HYDRATES this node
	// instead of wiping it (older providers that don't auto-re-register would 404
	// forever otherwise). Best-effort: a persistence error must not fail the live
	// registration (the node is already serving from memory) - log and continue.
	if b.db != nil {
		if err := b.db.UpsertNode(store.NodeRecord{
			NodeID: reg.NodeID, Reg: reg, Confidential: confidential, LastSeen: now.Unix(),
		}); err != nil {
			log.Printf("persist node %s failed: %v (registration still live in memory)", reg.NodeID, err)
		}
	}
	log.Printf("registered node %s (%d offers, %s, private=%v)", reg.NodeID, len(reg.Offers), reg.HW, reg.Private)
	resp := map[string]any{"ok": true}
	if reg.Private {
		resp["band_id"] = bandID
		resp["band_display"] = bandDisplay // cosmetic, not secret
		if bandCode != "" {
			resp["band_code"] = bandCode // the SECRET, returned ONCE at mint only
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// attestChallenge handles POST /nodes/challenge: issues a single-use, short-lived
// nonce a node binds its TEE quote to. This is what makes the confidential tier
// replay-safe: the node must produce a quote whose report_data == hash(pubkey ||
// nonce), so a captured quote cannot be reused (the nonce is spent on the next
// register) nor presented by a different node (the pubkey is bound in).
func (b *broker) attestChallenge(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodPost) {
		return
	}
	writeJSON(w, http.StatusOK, b.attest.issueNonce())
}

// reattestSweep periodically drops verified-confidential status that has lapsed its
// re-attestation cadence: a node must present a FRESH nonce-bound quote (by
// re-registering) within reattestTTL or it loses the ◆ badge and the confidential
// route filter stops sending it traffic. This stops a one-time verification from
// granting the badge forever - the guarantee has to be re-proven on a cadence.
func (b *broker) reattestSweep() {
	ttl := b.attest.reattestTTL
	if ttl <= 0 {
		return
	}
	// Check at a fraction of the TTL so a lapse is caught promptly (min 1m).
	tick := ttl / 4
	if tick < time.Minute {
		tick = time.Minute
	}
	for range time.Tick(tick) {
		b.expireStaleAttestations(time.Now(), ttl)
	}
}

// expireStaleAttestations drops confidential status for any node whose last
// attestation is older than ttl. Split out so tests can drive it deterministically.
func (b *broker) expireStaleAttestations(now time.Time, ttl time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for node, at := range b.attestedAt {
		if now.Sub(at) > ttl {
			if b.confidential[node] {
				log.Printf("TEE: node %s re-attestation lapsed (>%s) - dropping confidential status", node, ttl)
			}
			b.confidential[node] = false
			delete(b.attestedAt, node)
		}
	}
}

// persistThrottle is how often a node's last_seen is flushed to the store from the
// hot heartbeat/poll path. The in-memory lastSeen is updated EVERY beat (liveness is
// always exact in memory); the durable copy only needs to be recent enough that a
// re-hydrate after a restart lands within the TTL grace, so we coalesce DB writes.
const persistThrottle = 20 * time.Second

// markSeen refreshes a node's liveness on a heartbeat/poll. The in-memory lastSeen
// is bumped every call (so pick/discover are always exact); the durable last_seen is
// flushed at most once per persistThrottle per node (TouchNode is a no-op for an
// unknown/unpersisted node), keeping the DB write rate low while still giving a
// re-hydrated node a recent last_seen across a restart window.
func (b *broker) markSeen(node string) {
	now := time.Now()
	b.mu.Lock()
	b.lastSeen[node] = now
	b.mu.Unlock()
	if b.db == nil {
		return // no durable store (e.g. a minimal test broker): in-memory liveness is enough
	}
	b.metricsMu.Lock()
	if b.lastPersist == nil {
		b.lastPersist = map[string]time.Time{}
	}
	flush := now.Sub(b.lastPersist[node]) >= persistThrottle
	if flush {
		b.lastPersist[node] = now
	}
	b.metricsMu.Unlock()
	if flush {
		if err := b.db.TouchNode(node, now); err != nil {
			log.Printf("touch node %s last_seen failed: %v", node, err)
		}
	}
}

// rehydrateNodes loads the persisted node registry into the in-memory maps at
// startup so a broker restart/redeploy does NOT lose registrations. Liveness stays
// TRUTHFUL: a re-hydrated node is seeded with its PERSISTED last_seen (not "now"),
// so it is only treated as on-air if that timestamp is still within nodeTTL - a node
// that was already dead before the restart does NOT come back as falsely on-air. A
// still-running provider keeps heartbeating (~10s), so it re-confirms liveness within
// seconds via markSeen WITHOUT re-registering. The tunnel is rebuilt with the stored
// bridge token so the node's ongoing heartbeat/poll still authenticates.
func (b *broker) rehydrateNodes() {
	recs, err := b.db.AllNodes()
	if err != nil {
		log.Printf("re-hydrate node registry failed: %v (starting with an empty registry)", err)
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.private == nil {
		b.private = map[string]bool{}
	}
	if b.bandOf == nil {
		b.bandOf = map[string]string{}
	}
	n := 0
	for _, rec := range recs {
		reg := rec.Reg
		if reg.NodeID == "" {
			reg.NodeID = rec.NodeID
		}
		b.nodes[reg.NodeID] = reg
		b.lastSeen[reg.NodeID] = time.Unix(rec.LastSeen, 0)
		b.confidential[reg.NodeID] = rec.Confidential
		// Re-hydrate the private/band-of state from the signed reg so a restart keeps
		// a private node hidden + freq-routable until it re-registers (and re-asserts
		// or drops Private). The band row itself lives in the store, so resolve still
		// works across a restart even before the node re-registers.
		b.private[reg.NodeID] = reg.Private
		if reg.Private && reg.BandID != "" {
			b.bandOf[reg.NodeID] = reg.BandID
		}
		if rec.Confidential {
			if b.attestedAt == nil {
				b.attestedAt = map[string]time.Time{}
			}
			// Seed the re-attest clock from the persisted last_seen, NOT "now": a node
			// that was verified-confidential before a restart keeps the badge only until
			// its re-attest cadence lapses, at which point the sweep drops it unless the
			// node re-registers with a fresh quote. (It cannot be re-verified across a
			// restart without a quote, so this stays honest rather than trusting forever.)
			b.attestedAt[reg.NodeID] = time.Unix(rec.LastSeen, 0)
		}
		if b.tunnels[reg.NodeID] == nil {
			b.tunnels[reg.NodeID] = &nodeTunnel{jobs: make(chan protocol.Job, 64), waiters: map[string]chan protocol.JobResult{}, token: reg.BridgeToken}
		} else {
			b.tunnels[reg.NodeID].token = reg.BridgeToken
		}
		n++
	}
	if n > 0 {
		log.Printf("re-hydrated %d node registration(s) from the store (liveness re-confirmed on next heartbeat)", n)
	}
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
	b.markSeen(m.NodeID)
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
	b.markSeen(node)
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
	if res := b.mod.screen(promptText(body)); !res.allow() {
		log.Printf("moderation reject model=%s status=%d: %s", req.Model, res.status, res.msg)
		// CSAM (child-exploitation) hit: do NOT discard. PRESERVE the offending request
		// (access-controlled, retention-limited) and QUEUE a CyberTipline report
		// obligation (18 USC 2258A). Non-CSAM unsafe content is the existing
		// reject-and-discard. The pseudonym keeps the preserved record un-reversible to
		// the real user while still distinguishing repeat offenders.
		if res.csam {
			b.preserveCSAM(b.pseudonym(user, "relay"), clientIP(r), res.category, body)
		}
		jsonErr(w, res.status, res.msg)
		return
	}

	confidentialOnly := r.Header.Get("X-Roger-Confidential") != ""
	// Private band tune-in: X-Roger-Freq carries the frequency code. Resolve it with
	// the SAME constant-work lookup as POST /bands/resolve (always hash, uniform on
	// any miss - no enumeration oracle). A valid live band yields privateAllow={node},
	// admitting ONLY that station into pick; a present-but-unresolvable code yields an
	// empty set and the uniform "no station on that frequency" error. The code is
	// discovery + routing ADMISSION only - it is NOT spend-auth (spending still needs
	// the signed wallet below; self-use stays $0 via ownsNode). Never logged raw.
	var privateAllow map[string]bool
	var freqBand store.Band
	if freq := r.Header.Get("X-Roger-Freq"); freq != "" {
		pa, bnd, _ := b.resolveFreqAllow(freq, time.Now())
		privateAllow, freqBand = pa, bnd
		if len(privateAllow) == 0 {
			jsonErr(w, http.StatusServiceUnavailable, "no station on that frequency (it may be off air) - check the code")
			return
		}
		if freqBand.ModelDenied(req.Model) {
			// Uniform with the no-station message: do not reveal that the band exists
			// but excludes this model (no oracle on a valid code's model list).
			jsonErr(w, http.StatusServiceUnavailable, "no station on that frequency (it may be off air) - check the code")
			return
		}
	}
	minTPS := parseFloat(r.Header.Get("X-Roger-Min-TPS"))
	maxPrice := parseFloat(r.Header.Get("X-Roger-Max-Price"))
	// Consumer out-price cap. Defense in depth: even if the client omits the header (a
	// hand-rolled API caller, not the first-party CLI/TUI which always injects it), the
	// broker applies the DEFAULT consumer out-cap server-side so no consume path can
	// silently bind to an exorbitant band. An explicit (higher) cap is honored as sent;
	// the operator ceiling at register already bounds the absolute max. This makes the
	// consumer cap GLOBAL across every relay path (public use, --freq, grant, agent
	// harness, in-channel chat) rather than only the interactive `use` prompt.
	maxPriceOut := effectiveRelayMaxOut(parseFloat(r.Header.Get("X-Roger-Max-Price-Out")))
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
	node, offer, ok := b.pick(req.Model, confidentialOnly, minTPS, maxPrice, maxPriceOut, pinNode, exclude, allow, privateAllow)
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
		// MONTHLY SPEND CAP (per-account budget limit): reject BEFORE dispatch if this
		// request's worst-case cost would push the month-to-date captured spend past the
		// account's cap. Global across every PAID path (this hold gate is the one all of
		// public use / --freq / grant / agent / chat funnel through). Free/self ($0) skip
		// the whole block, so they are never blocked. Sets near/at-cap notice headers.
		if st, msg := b.monthlyCapCheck(w, payer, maxCost, time.Now()); st != 0 {
			jsonErr(w, st, msg)
			return
		}
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
			// P0-2: settle on min(nodeClaim, brokerRecount) for the completion when an
			// exact broker re-count exists, so an over-reporting node is billed on the
			// verified count, not its unverified claim. settleRecount does the single
			// sidecar call + folds trust/promotion-hold off the hot path; the node-signed
			// receipt is left intact (we only change the billed completion, via CostWith).
			billedCompletion := b.settleRecount(node.NodeID, recountModel(rec, req.Model), completionText(res.Body), rec.CompletionTokens)
			cost := rec.CostWith(billedCompletion)
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
				// The L1 re-count (trust scoring + the P0-2 promotion-hold flag) already
				// ran via settleRecount above (single sidecar call), so it is not repeated
				// here - that also makes the billed completion the re-counted one.
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
			// P0-2: the stream has finished (the receipt arrived), so the captured
			// completion text is complete. Bill on min(nodeClaim, brokerRecount) when an
			// exact re-count exists; settleRecount also folds trust + the promotion-hold.
			completion := ""
			if sink.cap != nil {
				sink.capMu.Lock()
				completion = sink.cap.String()
				sink.capMu.Unlock()
			}
			billedCompletion := b.settleRecount(node.NodeID, recountModel(rec, model), completion, rec.CompletionTokens)
			cost := rec.CostWith(billedCompletion)
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
func (b *broker) pick(model string, confidentialOnly bool, minTPS, maxPriceIn, maxPriceOut float64, pin string, exclude, allow, privateAllow map[string]bool) (protocol.NodeRegistration, protocol.ModelOffer, bool) {
	var best protocol.NodeRegistration
	var bestOffer protocol.ModelOffer
	found := false
	var bestC pickCand // the running winner's composite (health gate + score + load)
	now := time.Now()

	// Snapshot the per-node market metrics once under metricsMu (rather than calling
	// the per-id helper methods in the loop, which each re-lock and would also race
	// across reads). isBanned / probeFailing are inlined off these snapshots.
	b.metricsMu.Lock()
	type metric struct {
		tps      float64
		inflight int
		success  float64
		sseen    bool
		trust    float64
		ttftMs   float64
		verified bool
		failing  bool
		banned   bool
	}
	mget := func(id string) metric {
		tq := b.trust[id]
		sr, sseen := b.success[id]
		return metric{
			tps: b.tps[id], inflight: b.inflight[id], success: sr, sseen: sseen,
			trust: tq.score(), ttftMs: tq.ttftMs, verified: tq.verifiedServing(),
			failing: tq.probeFails >= 3, banned: b.banned[id],
		}
	}

	for _, n := range b.nodes {
		if time.Since(b.lastSeen[n.NodeID]) >= nodeTTL {
			continue
		}
		m := mget(n.NodeID)
		// A reported/banned node is ejected from routing entirely (reuses the eject
		// idea: treated as not-serving), so a flagged node stops being handed out.
		if m.banned {
			continue
		}
		// A PRIVATE (freq-code) node is only eligible when the caller resolved its
		// band: privateAllow holds exactly the node(s) a valid X-Roger-Freq mapped to.
		// Without that, a private node is invisible to pick - so the public market path
		// (no freq) can never accidentally route to a hidden station.
		if b.private[n.NodeID] && !privateAllow[n.NodeID] {
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
		if minTPS > 0 && m.tps > 0 && m.tps < minTPS {
			continue
		}
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
			c := pickCand{
				failing:  m.failing,
				score:    offerQuality(m.tps, m.ttftMs, m.trust, m.success, m.sseen, m.verified),
				priceOut: out,
				inflight: m.inflight,
			}
			if !found || c.beats(bestC) {
				best, bestOffer, bestC, found = n, o, c, true
			}
		}
	}
	b.metricsMu.Unlock()
	return best, bestOffer, found
}

// pickCand is a candidate offer's routing composite: the absolute health gate
// (a probe-failing node never beats a healthy one), a 0..1 quality score
// (speed/latency/trust/reliability), the active OUTPUT price, and current load.
type pickCand struct {
	failing  bool
	score    float64 // 0..1 measured quality (offerQuality)
	priceOut float64 // active output price (the billed/quoted side)
	inflight int     // in-flight requests on this node right now
}

// valuePerCredit is the composite ranking key: quality earned per credit of output
// price. A node that is faster / better-verified / more reliable wins at equal
// price, and a cheaper node wins at equal quality - so pick is defensible on BOTH
// price and measured performance, not price alone. Free nodes (price 0) are scored
// on raw quality (no division by zero) and naturally sort to the top.
func (c pickCand) valuePerCredit() float64 {
	if c.priceOut <= 0 {
		return c.score + 1 // free: rank above any paid node, ordered by quality
	}
	return c.score / c.priceOut
}

// beats reports whether c should replace the running winner `cur`. Ordering:
//  1. HEALTH GATE (absolute): a healthy node always beats a probe-failing one,
//     regardless of price/score - a failing node is only chosen when nothing
//     healthy serves the model (so a transient probe blip never blanks a model).
//  2. value-per-credit: higher composite (quality per credit) wins.
//  3. LOAD tie-break: when value is within tolerance, the LEAST in-flight node wins
//     so concurrent traffic spreads instead of piling onto one node.
//  4. final tie-break: cheaper output price.
func (c pickCand) beats(cur pickCand) bool {
	if c.failing != cur.failing {
		return !c.failing // healthy beats failing
	}
	cv, curv := c.valuePerCredit(), cur.valuePerCredit()
	const tol = 0.02 // within ~2% value: treat as a tie, break on load
	if rel := relDiff(cv, curv); rel > tol {
		return cv > curv
	}
	if c.inflight != cur.inflight {
		return c.inflight < cur.inflight // spread load: least-loaded wins
	}
	return c.priceOut < cur.priceOut // final tie-break: cheaper
}

// relDiff is the relative gap between two non-negative values (0 when equal).
func relDiff(a, x float64) float64 {
	hi := a
	if x > hi {
		hi = x
	}
	if hi <= 0 {
		return 0
	}
	d := a - x
	if d < 0 {
		d = -d
	}
	return d / hi
}

// offerQuality is the per-node 0..1 measured-quality score pick ranks on (the same
// factors the market signal uses, minus supply/congestion which are channel-level).
// Unmeasured terms are NEUTRAL (0.5/verified-0) so a brand-new node still competes
// instead of scoring 0 - it just doesn't out-rank a proven-fast node until measured.
func offerQuality(tps, ttftMs, trust, success float64, sseen, verified bool) float64 {
	speed := 0.5 // neutral until measured
	if tps > 0 {
		speed = clamp01(tps / 300.0)
	}
	latency := 0.5 // neutral until probed
	if ttftMs > 0 {
		latency = 1 - clamp01(ttftMs/ttftCap)
	}
	rel := successFor(success, sseen, verified) // organic EWMA, else verified/neutral
	ver := 0.0
	if verified {
		ver = 1.0
	}
	// Re-use the signal's speed/latency/verified/success/trust split, renormalised to
	// 1.0 (supply/congestion are not per-node). Weights: speed .25, latency .15,
	// verified .25, success .15, trust .20.
	return clamp01(0.25*speed + 0.15*latency + 0.25*ver + 0.15*rel + 0.20*clamp01(trust))
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
