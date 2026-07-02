package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
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

// Free-node registration ceiling (Sybil hygiene). A FREE (anon, no-owner) node is
// not attributable to an owner account, so the per-owner on-air cap cannot bound it.
// Without a ceiling, one host could flood /discover + the pick candidate set with
// throwaway free node ids. defaultFreeRegPerIP NEW free registrations per CF-IP within
// defaultFreeRegWindow are allowed; the next is rejected. Both are env-tunable; a
// per-IP limit <= 0 disables the ceiling entirely (e.g. for a trusted/dev deployment).
const (
	defaultFreeRegPerIP  = 10
	defaultFreeRegWindow = time.Hour
)

// freeRegPerIPLimit reads the per-CF-IP free-registration cap from the environment,
// falling back to the default. <0 is ignored (keeps default); 0 disables the ceiling.
func freeRegPerIPLimit() int {
	if v := os.Getenv("ROGERAI_FREE_REG_PER_IP"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return defaultFreeRegPerIP
}

// freeRegWindowDur reads the sliding window for the per-IP free-registration cap from
// the environment (seconds), falling back to the default. <=0 is ignored.
func freeRegWindowDur() time.Duration {
	if v := os.Getenv("ROGERAI_FREE_REG_WINDOW_SEC"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return defaultFreeRegWindow
}

// allowFreeReg records a NEW free (anon, no-owner) node registration from ip and
// reports whether it is within the per-IP ceiling. An idempotent re-register of an
// already-known node passes `isNew=false` and is NEVER counted or rejected (a running
// free node must be able to keep refreshing). Returns true (allowed) when the ceiling
// is disabled (freeRegPerIP <= 0) or ip is empty. The per-IP timestamp slice is pruned
// to the sliding window on each call so it cannot grow without bound.
func (b *broker) allowFreeReg(ip string, isNew bool) bool {
	if b.freeRegPerIP <= 0 || ip == "" || !isNew {
		return true
	}
	now := time.Now()
	b.freeRegMu.Lock()
	defer b.freeRegMu.Unlock()
	if b.freeRegByIP == nil {
		b.freeRegByIP = map[string][]time.Time{}
	}
	// Prune timestamps older than the window for this IP.
	cutoff := now.Add(-b.freeRegWindow)
	kept := b.freeRegByIP[ip][:0]
	for _, t := range b.freeRegByIP[ip] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= b.freeRegPerIP {
		b.freeRegByIP[ip] = kept
		return false
	}
	b.freeRegByIP[ip] = append(kept, now)
	return true
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

// maxRecountCapture bounds the off-band completion copy the broker keeps for the L1
// token re-count. Without this cap a malicious node could stream an unbounded body to
// OOM the broker (a 512MB box) via the private capture buffer, multiplied by every
// concurrent stream. 256 KiB is far more text than any legitimate completion needs
// for a representative re-count; capture stops once the buffer reaches this size while
// the client still receives the full, uncapped stream.
const maxRecountCapture = 256 << 10 // 256 KiB

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
	// Organic first-byte-latency capture (smart-router v2): nodeID + the dispatch
	// time so agentStream can fold time-to-first-MEANINGFUL-chunk into the node's
	// ttftMs EWMA. ttftDone guards a single sample per stream. A bare first chunk
	// (< MIN_FIRST_TOKENS of text) is NOT recorded - a node can't win TTFT by
	// streaming a space then stalling.
	nodeID   string
	start    time.Time
	ttftDone bool
	ttftSeen int // running count of meaningful chars observed before the sample lands
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
	// Price-safety, operator side: a HARD, GLOBAL ceiling on what ANY station may charge -
	// public, --private, AND confidential ALIKE. It runs UNCONDITIONALLY here (before
	// owner-binding, attestation, and the private-band mint below), so NO flag exempts it.
	// --private hides a station from the public market but is NOT a price-bypass: a private
	// (and a confidential) band is held to the SAME ceiling as a public one. This is a
	// deliberate safety max so a fat-fingered, deterrent, or abusive price can never land on
	// ANY band and burn a consumer. Checked against EVERY offer's base AND scheduled-window
	// prices. The rejection copy states the real remedy - lower the price below the ceiling -
	// and does NOT suggest --private as an escape (the ceiling is global; --private only hides
	// a station from the public market, it is not a price bypass). (Pinned by
	// TestRegisterCeilingGlobalAllBands + features/pricing/price_ceiling.feature.)
	if msg := registerPriceCeiling(reg.Offers); msg != "" {
		jsonErr(w, http.StatusBadRequest, msg)
		return
	}
	// Login-to-monetize / login-to-go-private: a node advertising a NONZERO price is
	// an earning node, AND a node going PRIVATE (its own discovery visibility is a
	// per-owner resource) both HARD-REQUIRE a GitHub-linked owner bound to the signing
	// key on this request (a missing/invalid owner sig is REJECTED). A FREE PUBLIC node
	// does NOT require login - but if it ARRIVES with a valid owner signature we BIND it
	// to that account anyway, so an authenticated owner's free supply is account-scoped
	// (account grant keys resolve a bound free node; earning lots + the per-owner cap
	// span it). Anonymous free supply (no/invalid owner sig) stays UNBOUND as before.
	gated := offersPriced(reg.Offers) || reg.Private // priced/private => login HARD-required
	var regOwner store.Owner                         // set when this register resolves to an owner (priced, private, OR a signed-in free owner)
	// Resolve owner identity once. A signature, when offered, MUST verify (identityOf
	// returns sok=false on an invalid one); `authed` means a VERIFIED owner-signed
	// request, and requireOwner then resolves it to a GitHub-linked owner account.
	uid, authed, sok := b.identityOf(r, body)
	if gated && !sok {
		jsonErr(w, http.StatusUnauthorized, "invalid request signature")
		return
	}
	owner, ownerOK := store.Owner{}, false
	if sok && authed {
		owner, ownerOK = b.requireOwner(r)
	}
	_ = uid
	if gated {
		// Priced/private MUST be a GitHub-linked owner: reject unsigned/unauthed/unlinked.
		if !authed {
			msg := "earning (priced) node registration requires `roger login` (a GitHub-linked owner)"
			if reg.Private {
				msg = "a private band requires `roger login` (anonymous private sharing is not allowed)"
			}
			jsonErr(w, http.StatusUnauthorized, msg)
			return
		}
		if !ownerOK {
			msg := "earning (priced) node registration requires a GitHub-linked owner - run `roger login`"
			if reg.Private {
				msg = "a private band requires a GitHub-linked owner - run `roger login`"
			}
			jsonErr(w, http.StatusForbidden, msg)
			return
		}
	}
	// OWNER-AUTHENTICATED => BIND (regardless of price/private). This is the fix: a FREE,
	// non-private node that arrives owner-signed is bound to its account too, so account
	// grant keys can find it. ownerOK is true only for a VERIFIED owner-signed request
	// that resolves to a GitHub-linked account; an anonymous free register leaves
	// regOwner zero and falls through to the UNBOUND path below.
	if ownerOK {
		regOwner = owner
		// DURABLE OWNER BAN (anti-rotation): a banned operator must not be able to return
		// under a fresh node id / callsign / grant key. Now that a free owner-signed node
		// is owner-resolved, this ban check correctly covers it too. Reject BEFORE binding;
		// the relay pick/settle gates are the in-flight backstop.
		if b.isOwnerBanned(owner.Pubkey) {
			jsonErr(w, http.StatusForbidden, "this account is banned from serving on RogerAI")
			return
		}
		// Attribute this node's future earnings to the owner account (TOFU: the first
		// account to register a node id owns it), so earning lots + payouts resolve.
		_ = b.db.BindNode(reg.NodeID, owner.Pubkey)
		// W1: drop any stale cached node->account binding so the (new) TOFU binding is
		// reflected at once rather than after the TTL.
		b.invalidateAccountOfNode(reg.NodeID)
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
	// P2-5: from here on, reg carries the broker's VERDICT, never the node's raw claim.
	// Everything downstream (b.nodes, the durable UpsertNode record, and above all the
	// MULTI-INSTANCE mirror put) stores this reg - if the claim survived here, a node
	// that FAILED attestation under require=0 would be mirrored Confidential=true and
	// a peer instance would grant it the ◆ tier. The signature was already verified
	// above, so mutating the struct after the check is safe.
	reg.Confidential = confidential
	// Owner-authored web-Console price/schedule overrides take PRECEDENCE over (seed)
	// the node-supplied offers, and survive this re-register because we re-apply them
	// here on every register, before reg.Offers lands in b.nodes + is persisted. Done
	// off the broker lock (it does a store read). Only an owner-bound node (regOwner
	// set) can carry overrides; ActivePrice then reads the overridden price at serve
	// time. (Past receipts/ledger are immutable - this changes only future pricing.)
	overriddenModels := b.applyOfferOverrides(regOwner.Pubkey, reg.NodeID, reg.Offers)
	// PUBLIC-VOICE REGISTER GUARD (off-lock: the content screen does network IO, which must
	// never run under b.mu). A voice is only PUBLICLY listable when it is owner-bound (Q2:
	// signed-in operators only), so the guard applies to an owner-bound (regOwner set) TTS
	// offer. For each such offer it (1) derives the namespaced voice-name SLUG from the
	// display Name and rejects an empty-after-normalize slug, (2) rejects a slug that
	// PREFIX-matches a chat-model family root (impersonation, Q3, env-overridable), and (3)
	// screens Name+slug+STATION through the EXISTING b.mod.screen at this new register-time
	// call site (honoring ROGERAI_REQUIRE_MODERATION fail-closed). The raw o.Model is left
	// untouched — the slug is a computed view, not a stored field. The station is the public
	// namespace handle (@<station>/…), normalized from the signed reg.Station. The duplicate-
	// within-operator + cross-owner station-uniqueness checks need b.nodes and run under the
	// lock below.
	station := slugStation(reg.Station)
	if regOwner.Pubkey != "" {
		if code, msg := b.screenVoiceOffers(reg.Offers, station); code != 0 {
			jsonErr(w, code, msg)
			return
		}
	}
	b.mu.Lock()
	// CROSS-OWNER STATION UNIQUENESS (anti-impersonation): a station is a PER-MACHINE public
	// broadcast callsign; the auto-generated one is ~unique but RENAMEABLE, so two DIFFERENT
	// owners could claim the same public @<station>. Reject a public-voice registration whose
	// station is already on air under a DIFFERENT owner's public (TTS) voice, so @<station> is
	// an unambiguous handle for attribution + routing. The SAME owner reusing their own station
	// (a second model, or an idempotent re-register) is fine — the check keys on a DIFFERENT
	// owner account. Only fires when this registration actually brings a public voice (a TTS
	// offer) under a station; a chat-only or station-less node reserves nothing.
	if regOwner.Pubkey != "" && station != "" && offersTTS(reg.Offers) {
		if other := b.stationClaimedByOther(station, regOwner.Pubkey); other != "" {
			b.mu.Unlock()
			jsonErr(w, http.StatusConflict, fmt.Sprintf("station %q is already in use by another operator - pick a different callsign with `share --node`", station))
			return
		}
	}
	// DUPLICATE VOICE-NAME (same operator): two of an operator's on-air voices may not
	// share a normalized slug (deterministic ids; an operator can't shadow themselves). Run
	// under the lock since it reads the owner's other live nodes. Excludes this node id so
	// an idempotent re-register is not a self-collision.
	if regOwner.Pubkey != "" {
		if msg := b.duplicateVoiceName(regOwner.Pubkey, reg.NodeID, reg.Offers); msg != "" {
			b.mu.Unlock()
			jsonErr(w, http.StatusConflict, msg)
			return
		}
	}
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
	// on-air node). Every OWNER-BOUND registration is attributable and capped here -
	// priced, private, AND a free node that arrived owner-signed (regOwner is set). Only
	// ANONYMOUS free supply (no owner) is not counted here. The (limit+1)th node is
	// rejected with a clear 4xx the share UX surfaces verbatim.
	// FREE-NODE REGISTRATION CEILING (Sybil hygiene): an ANONYMOUS free (no-owner)
	// registration is not attributable to an owner account, so the per-owner cap above
	// cannot bound it. Cap how many NEW free node ids one CF-IP may register within the
	// window so a single host can't flood /discover + the pick candidate set with
	// throwaway nodes. Only NEW free nodes count (`_, known := b.nodes[id]`): an
	// idempotent re-register of an existing free node refreshes without being rejected.
	// Owner-bound registers (priced/private/free-owner-signed) skip this - they are
	// bounded by the per-owner cap instead.
	if regOwner.Pubkey == "" {
		_, known := b.nodes[reg.NodeID]
		if !b.allowFreeReg(clientIP(r), !known) {
			b.mu.Unlock()
			jsonErr(w, http.StatusTooManyRequests,
				"too many new free stations from this address - slow down or `roger login` to register an owned station")
			return
		}
	}
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
	// Stamp a LOCAL (re)register so syncRegistry briefly trusts this fresh bridge token
	// over a possibly-stale shared read (the shared key is written just below, after this
	// unlock). After the grace, syncRegistry reconciles even this node from the shared
	// registry so a token rotated on ANOTHER instance reconverges here instead of pinning
	// a stale token forever -> 401s (the multi-instance token-oscillation bug).
	if b.localRegAt == nil {
		b.localRegAt = map[string]time.Time{}
	}
	b.localRegAt[reg.NodeID] = now
	b.mu.Unlock()
	// From here on reg (incl. its Offers array) is safe to read WITHOUT b.mu even
	// though b.nodes now aliases it: a concurrent web-console price PATCH never
	// mutates a published offers array in place - applyOverrideLive is copy-on-write
	// (race pinned by TestRaceRegisterMirrorVsLiveOverride).

	// SHARED registry mirror: publish this node's full registration (incl. BridgeToken)
	// to the shared store so PEER instances can pick it AND authenticate its poll/result
	// - the fix for the 2-instance break where a node that dialed instance A is invisible
	// (503) / un-pollable (404) on instance B. A PRIVATE band publishes to a SEPARATE
	// namespace (putPrivateNode) so a peer can resolve + route it WITHOUT it ever entering
	// the public allNodes()/discover mirror. Outside b.mu (network I/O); best-effort - the
	// registry sync re-pulls.
	//
	// GATED ON `b.shared != nil` ALONE - the SAME gate as the markSeen liveness
	// write-through - NOT on the ROGERAI_MULTI_INSTANCE bus flag (task #52 churn root
	// cause): with the flag OFF but a shared backend wired, liveness was mirrored while
	// registrations were NOT, so any second broker process (scale-down drift, rolling
	// deploy overlap) answered this node's heartbeat/poll 404 "unknown node" -> the node
	// re-registered with a ROTATED token every ~10s, forever, and each rotation
	// re-poisoned the other process (the alternating-401 ping-pong). Registration state
	// and liveness state now travel together under BOTH flag values; only the job/result/
	// stream DISPATCH bus stays behind the flag. Pinned by
	// features/multinode/liveness_churn.feature.
	if b.shared != nil {
		if raw, mErr := json.Marshal(reg); mErr == nil {
			// Clear any stale entry in the OTHER namespace first, so a private<->public flip
			// never leaves a mirror markSeen would keep alive (each node lives in EXACTLY one
			// namespace). Then publish to the correct one.
			_ = b.shared.dropSharedNode(reg.NodeID)
			if reg.Private {
				_ = b.shared.putPrivateNode(reg.NodeID, raw, livenessTTL)
			} else {
				_ = b.shared.putNode(reg.NodeID, raw, livenessTTL)
			}
		}
	}

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
	// Return the EFFECTIVE offers (reg.Offers was rewritten in place by
	// applyOfferOverrides), so the CLI/agent shows the broker-EFFECTIVE price - one
	// source of truth for the published price. `overrides` names which models carry an
	// active owner-authored web price, so `share` can note "broker override active".
	resp := map[string]any{"ok": true, "effective_offers": reg.Offers}
	// Echo whether the confidential ◆ badge was GRANTED this register, so a node that
	// CLAIMED confidential learns the outcome instead of being silently downgraded: in
	// fail-soft mode (require=0) a claim that fails attestation still registers as
	// standard, and only this echo lets `roger share` warn the operator (e.g. an
	// unblessed launch measurement). Always present so the absence of a badge is explicit.
	resp["confidential"] = confidential
	if len(overriddenModels) > 0 {
		resp["overrides"] = overriddenModels
	}
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
// stop is a test seam: main passes nil (the nil-channel select case never fires, so
// production waits on the ticker exactly as the old time.Tick loop did); a test passes
// a closeable channel to drive then halt the sweep deterministically.
func (b *broker) reattestSweep(stop <-chan struct{}) {
	ttl := b.attest.reattestTTL
	if ttl <= 0 {
		return
	}
	// Check at a fraction of the TTL so a lapse is caught promptly (min 1m).
	tick := ttl / 4
	if tick < time.Minute {
		tick = time.Minute
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			b.expireStaleAttestations(time.Now(), ttl)
		}
	}
}

// expireStaleAttestations drops confidential status for any node whose last
// attestation is older than ttl. Split out so tests can drive it deterministically.
func (b *broker) expireStaleAttestations(now time.Time, ttl time.Duration) {
	// P2-5: a lapsed node's downgrade must reach the MULTI-INSTANCE mirror too, or a
	// peer keeps granting the ◆ tier until the node happens to re-register. Collect the
	// re-publishes under the lock, put them outside it (network I/O).
	type republish struct {
		id      string
		raw     []byte
		private bool
	}
	var toPublish []republish
	b.mu.Lock()
	for node, at := range b.attestedAt {
		if now.Sub(at) > ttl {
			if b.confidential[node] {
				log.Printf("TEE: node %s re-attestation lapsed (>%s) - dropping confidential status", node, ttl)
			}
			b.confidential[node] = false
			delete(b.attestedAt, node)
			if reg, ok := b.nodes[node]; ok && reg.Confidential {
				reg.Confidential = false
				b.nodes[node] = reg
				// Same gate as register's publish: whenever a shared registry exists it
				// must carry the downgrade, or a peer keeps granting the ◆ tier.
				if b.shared != nil {
					if raw, err := json.Marshal(reg); err == nil {
						toPublish = append(toPublish, republish{node, raw, reg.Private})
					}
				}
			}
		}
	}
	b.mu.Unlock()
	for _, p := range toPublish {
		if p.private {
			_ = b.shared.putPrivateNode(p.id, p.raw, livenessTTL)
		} else {
			_ = b.shared.putNode(p.id, p.raw, livenessTTL)
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
	// Shared-state write-through (PRE-SCALE Stage 1): mirror the heartbeat to Valkey so
	// PEER broker instances can observe this node's freshness. Coalesced on the SAME
	// throttle gate as the durable DB touch below, so the shared write rate stays low.
	// Best-effort: a failure is logged+swallowed inside markSeen on the store and never
	// affects in-memory liveness (which remains exact + authoritative on this instance).
	flushShared := b.shared != nil && b.sharedFlushDue(node, now)
	if flushShared {
		_ = b.shared.markSeen(node, now)
	}
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

// sharedFlushDue coalesces the shared-state liveness write-through on its own
// per-node throttle (separate from lastPersist so it works even when b.db is nil,
// e.g. the in-memory store). It returns true at most once per persistThrottle per
// node, keeping the Valkey HSET rate low while still refreshing well inside nodeTTL.
func (b *broker) sharedFlushDue(node string, now time.Time) bool {
	b.metricsMu.Lock()
	defer b.metricsMu.Unlock()
	if b.lastSharedSeen == nil {
		b.lastSharedSeen = map[string]time.Time{}
	}
	if now.Sub(b.lastSharedSeen[node]) < persistThrottle {
		return false
	}
	b.lastSharedSeen[node] = now
	return true
}

// syncTickInterval is the cross-instance liveness/inflight sync cadence. It is a package
// var (not a const) ONLY so a test can shrink it to drive a sync tick deterministically;
// production reads the 5s default unchanged.
var syncTickInterval = 5 * time.Second

// syncLiveness runs only when a shared-state backend is wired in. It periodically
// pulls the cross-instance liveness snapshot from Valkey and merges any FRESHER
// peer timestamp into this instance's in-memory lastSeen map. This is what makes
// "any instance sees any node's freshness" true WITHOUT putting a Valkey round-trip
// on the hot pick/discover read path: those keep reading the in-memory map exactly
// as today. We only ever move a node's lastSeen FORWARD (max of local/shared), so a
// stale snapshot can never make a live node look dead. On a backend error we just
// skip the round and retry next tick (graceful degrade to local-only liveness).
// stop is a test seam (nil in production: the nil-channel case never fires, so the
// loop waits on the ticker exactly as before).
func (b *broker) syncLiveness(stop <-chan struct{}) {
	t := time.NewTicker(syncTickInterval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			if b.shared == nil {
				return
			}
			b.syncLivenessOnce()
		}
	}
}

// syncLivenessOnce pulls the shared liveness snapshot once and merges every peer's
// newer last_seen into this instance (and, in multi-instance mode, mirrors the shared
// registry). Split out of the ticker loop so the merge is testable deterministically.
func (b *broker) syncLivenessOnce() {
	// Cross-instance BAN propagation: re-pull the durable banned sets when a peer changed
	// them (cheap rev-counter check; no-op when unchanged). FIRST, before the liveness
	// early-return below, so bans still propagate on a tick where the liveness snapshot is
	// empty (e.g. no node has been markSeen to the shared store yet).
	b.syncBanRev()
	// SHARED registry mirror: pull every peer's published registration into this
	// instance's registry + tunnel stubs, so a node that dialed a DIFFERENT process is
	// still pickable + its poll/result authenticatable here. Two hard-won properties
	// (task #52, both pinned by features/multinode/liveness_churn.feature):
	//
	//  1. It runs whenever a shared backend is wired - NOT only under the
	//     ROGERAI_MULTI_INSTANCE bus flag - so registration state travels with liveness
	//     state under BOTH flag values (the flag=0 two-process churn root cause).
	//  2. It runs BEFORE the liveness early-return below. It used to run only after a
	//     NON-EMPTY liveness snapshot - but a node whose heartbeats are 401ing never
	//     reaches markSeen, so with every node churning (or the shared liveness wiped by
	//     a Valkey restart/eviction) the snapshot stayed empty, the registry never
	//     reconciled, and the token ping-pong became SELF-SUSTAINING: rotated tokens
	//     could never converge (the v5.0.0 flag=1 launch symptom).
	b.syncRegistry()
	snap, err := b.shared.liveness()
	if err != nil || len(snap) == 0 {
		return
	}
	b.mu.Lock()
	for node, ts := range snap {
		if cur, ok := b.lastSeen[node]; !ok || ts.After(cur) {
			b.lastSeen[node] = ts
		}
	}
	b.mu.Unlock()
}

// syncLocalRegisterGrace is how long after THIS instance (re)registers a node that
// syncRegistry leaves that node's token/offers alone, trusting the just-written local
// reg over the shared read (which the register publishes immediately after, so an
// in-flight sync must not clobber it back). It is a few sync ticks (interval 5s); after
// it lapses, even a locally-held node reconciles from the shared registry so a bridge
// token rotated on a PEER instance reconverges here. Bridge tokens are stable in steady
// state (they only rotate on a 404/401/403 recover, which the mirror makes rare), so the
// brief reconvergence delay never costs a healthy node.
const syncLocalRegisterGrace = 15 * time.Second

// syncRegistry mirrors the shared node registry into this instance's in-memory state so
// any node is pickable + its poll/result authenticatable on ANY instance (the bus then
// carries the actual job/result). It ADDS/refreshes peer nodes - never deletes (liveness
// + the prune sweep age a dead node out). A node THIS instance just (re)registered is left
// untouched for syncLocalRegisterGrace (so the fresh local token wins a race with a stale
// shared read); after that it too is reconciled from the shared registry, which is the
// source of truth for the bridge token (register publishes it on every register). PRIVATE
// bands are mirrored too, in a SECOND pass from the separate private namespace and flagged
// b.private=true, so they are resolvable + freq-routable on a peer yet never enter /discover.
//
// STALENESS GUARD (live /voices sample_url regression, 2026-07-02): registrations are
// TOTALLY ORDERED by their node-signed TS (register enforces freshness, the reregistrar
// stamps now on every re-register). A mirrored registration STRICTLY OLDER than the one
// this instance holds is NEVER adopted - without this, a stale shared copy (left behind
// when a register-time putNode write was lost to a Valkey blip, then kept alive
// indefinitely by heartbeat markSeen) silently REPLACED a fresh local registration once
// the grace lapsed: the offers regressed to the previous register (a newly-added
// sample_url vanished from /voices while its sibling name/language survived) and the
// whole fleet converged stale until the node happened to re-register. On detecting an
// older mirror we RE-PUBLISH the fresher local copy (outside b.mu - network I/O), so the
// shared registry and every peer HEAL to the newest registration instead. Equal-TS and
// newer mirrors keep flowing unchanged - the bridge-token reconvergence fix depends on
// adopting them. Pinned by sync_registry_staleness_test.go.
func (b *broker) syncRegistry() {
	if b.shared == nil {
		return
	}
	// Pull BOTH the public registry and the SEPARATE private-band namespace before taking
	// the lock (network I/O). Proceed if EITHER is non-empty - a fleet with only private
	// bands must still mirror them (don't early-return on an empty public registry).
	regs, _ := b.shared.allNodes()
	pregs, _ := b.shared.allPrivateNodes()
	if len(regs) == 0 && len(pregs) == 0 {
		return
	}
	// heals collects the fresher LOCAL registrations whose shared mirror was found stale,
	// marshaled UNDER the lock (the b.nodes read must hold b.mu anyway, and snapshotting
	// the bytes right there matches the attestation-lapse and rehydrate re-publishes;
	// published offers arrays themselves are immutable - applyOverrideLive is
	// copy-on-write) and re-published after the lock is dropped (network I/O), with
	// register's exact drop+put sequence.
	type heal struct {
		id      string
		raw     []byte
		private bool
	}
	var heals []heal
	b.mu.Lock()
	if b.private == nil {
		b.private = map[string]bool{}
	}
	for id, raw := range regs {
		// Trust our OWN just-(re)registered token over the shared read for a short grace
		// window: the shared key is written right after register's unlock, so an in-flight
		// sync that read the shared registry a moment before that write must not clobber the
		// fresh local token back to the previous one. After the grace we reconcile this node
		// from the shared registry like any other, so a token rotated on a PEER instance
		// (authority migration) reconverges here instead of pinning a stale token -> 401s.
		if at, ok := b.localRegAt[id]; ok && time.Since(at) < syncLocalRegisterGrace {
			continue
		}
		var reg protocol.NodeRegistration
		if json.Unmarshal(raw, &reg) != nil {
			continue
		}
		if reg.Private {
			continue
		}
		if reg.NodeID == "" {
			reg.NodeID = id
		}
		if cur, ok := b.nodes[id]; ok && cur.TS > reg.TS {
			// Strictly-older mirror: keep the fresher local reg + re-publish it (snapshotted here).
			if raw, err := json.Marshal(cur); err == nil {
				heals = append(heals, heal{id: id, raw: raw, private: cur.Private})
			}
			continue
		}
		b.nodes[id] = reg
		// This node is in the PUBLIC registry, so it is public: clear any stale private flag
		// from a prior mirror (a node that flipped private->public must stop being hidden).
		b.private[id] = false
		b.confidential[id] = reg.Confidential
		// Seed the re-attestation clock for mirrored confidential nodes, exactly as
		// register() does (tunnel.go:359). Without this the clock is zero on the mirror,
		// so confidential cross-instance routing would treat the node as never-attested.
		if reg.Confidential {
			if b.attestedAt == nil {
				b.attestedAt = map[string]time.Time{}
			}
			if _, ok := b.attestedAt[id]; !ok {
				b.attestedAt[id] = time.Now()
			}
		}
		if b.tunnels[id] == nil {
			b.tunnels[id] = &nodeTunnel{jobs: make(chan protocol.Job, 64), waiters: map[string]chan protocol.JobResult{}, token: reg.BridgeToken}
		} else {
			b.tunnels[id].token = reg.BridgeToken
		}
	}
	// PRIVATE band mirror: the SAME learn as above, but from the separate private namespace
	// and flagged b.private[id]=true so the node is resolvable + freq-routable on this
	// instance yet stays OUT of /discover + the public market + a public pick (those all gate
	// on b.private). The band CODE is never here - only the node reg (offers + bridge token).
	for id, raw := range pregs {
		if at, ok := b.localRegAt[id]; ok && time.Since(at) < syncLocalRegisterGrace {
			continue
		}
		var reg protocol.NodeRegistration
		if json.Unmarshal(raw, &reg) != nil || !reg.Private {
			continue // private namespace is private-only; ignore a mis-tagged entry defensively
		}
		if reg.NodeID == "" {
			reg.NodeID = id
		}
		if cur, ok := b.nodes[id]; ok && cur.TS > reg.TS {
			// Strictly-older mirror: keep the fresher local reg + re-publish it (snapshotted here).
			if raw, err := json.Marshal(cur); err == nil {
				heals = append(heals, heal{id: id, raw: raw, private: cur.Private})
			}
			continue
		}
		b.nodes[id] = reg
		b.private[id] = true
		b.confidential[id] = reg.Confidential
		if reg.Confidential {
			if b.attestedAt == nil {
				b.attestedAt = map[string]time.Time{}
			}
			if _, ok := b.attestedAt[id]; !ok {
				b.attestedAt[id] = time.Now()
			}
		}
		if b.tunnels[id] == nil {
			b.tunnels[id] = &nodeTunnel{jobs: make(chan protocol.Job, 64), waiters: map[string]chan protocol.JobResult{}, token: reg.BridgeToken}
		} else {
			b.tunnels[id].token = reg.BridgeToken
		}
	}
	b.mu.Unlock()
	// HEAL the shared registry outside the lock (network I/O): re-publish each fresher
	// LOCAL registration over its stale mirror with register's exact drop+put sequence,
	// so a private<->public flip never leaves a copy in the other namespace and every
	// peer's next sync adopts the newest registration instead of the stale one.
	for _, h := range heals {
		_ = b.shared.dropSharedNode(h.id)
		if h.private {
			_ = b.shared.putPrivateNode(h.id, h.raw, livenessTTL)
		} else {
			_ = b.shared.putNode(h.id, h.raw, livenessTTL)
		}
	}
}

// tunnelFor returns the node's tunnel + a SNAPSHOT of its bridge token, LAZILY
// learning it from the shared registry on a local miss (whenever a shared backend is
// wired - the same gate as the registry publish, NOT the bus flag; task #52). This is
// the re-registration-storm fix: a node's poll/heartbeat/result can land (via the
// load balancer) on a process that has not yet synced the registry; returning 404
// there makes the node misread it as "broker restarted" and re-register (rotating its
// token), over and over. Instead we fetch the node's published registration from the
// shared store on demand and build its tunnel stub right here, so the request
// succeeds and no re-register fires. Returns nil only when the node is unknown on
// EVERY instance. No shared store: pure local read.
//
// The token is returned (copied UNDER b.mu) rather than read off t.token by the
// caller: register/syncRegistry/rehydrate rewrite t.token under b.mu, so an unlocked
// caller read of the auth credential is a data race (pinned by
// TestRaceNodeTokenReadVsRegister).
func (b *broker) tunnelFor(node string) (*nodeTunnel, string) {
	b.mu.Lock()
	t := b.tunnels[node]
	tok := ""
	if t != nil {
		tok = t.token
	}
	b.mu.Unlock()
	if t != nil || b.shared == nil {
		return t, tok
	}
	raw, ok, err := b.shared.getNode(node)
	private := false
	if err != nil || !ok {
		// Public miss: try the SEPARATE private-band namespace. A --private/--freq node that
		// dialed a PEER must still be able to poll/result on THIS instance (no re-register
		// storm), so we learn it here too - flagged private so it stays out of /discover.
		praw, pok, perr := b.shared.getPrivateNode(node)
		if perr != nil || !pok {
			return nil, ""
		}
		raw, private = praw, true
	}
	var reg protocol.NodeRegistration
	if json.Unmarshal(raw, &reg) != nil {
		return nil, ""
	}
	if reg.NodeID == "" {
		reg.NodeID = node
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if t := b.tunnels[node]; t != nil {
		return t, t.token // another concurrent request just learned it
	}
	b.nodes[node] = reg
	b.confidential[node] = reg.Confidential
	if private || reg.Private {
		if b.private == nil {
			b.private = map[string]bool{}
		}
		b.private[node] = true // keep a lazily-learned band OUT of /discover + public pick
	}
	if reg.Confidential {
		if b.attestedAt == nil {
			b.attestedAt = map[string]time.Time{}
		}
		if _, ok := b.attestedAt[node]; !ok {
			b.attestedAt[node] = time.Now()
		}
	}
	nt := &nodeTunnel{jobs: make(chan protocol.Job, 64), waiters: map[string]chan protocol.JobResult{}, token: reg.BridgeToken}
	b.tunnels[node] = nt
	return nt, reg.BridgeToken
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
	if b.private == nil {
		b.private = map[string]bool{}
	}
	if b.bandOf == nil {
		b.bandOf = map[string]string{}
	}
	// Re-publish public registrations to the SHARED registry after a restart/redeploy, so
	// peer instances can re-learn a heartbeat-only node even if its shared reg key lapsed
	// while this instance was down. markSeen only EXTENDS an existing reg key (PExpire is a
	// no-op on a missing key), so without this a rehydrated node would stay invisible
	// cross-instance until it happened to re-register. Mirrors register()'s publish; the
	// 10m key self-expires for nodes that never come back, and peers gate picking on
	// liveness regardless. Collected here, published after the lock is dropped.
	type pubReg struct {
		id      string
		raw     []byte
		private bool
	}
	var toPublish []pubReg
	n := 0
	for _, rec := range recs {
		reg := rec.Reg
		if reg.NodeID == "" {
			reg.NodeID = rec.NodeID
		}
		// P2-5: the persisted Reg may predate the verdict normalization (or carry a raw
		// claim from an old release); rec.Confidential is the broker's stored VERDICT, so
		// re-hydrate memory + the mirror re-publish below with the verdict, never the claim.
		reg.Confidential = rec.Confidential
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
		// Same gate as register's publish (`shared != nil`, not the bus flag): a
		// restarted flag=0 process must re-publish too, or a peer process could
		// never re-learn its heartbeat-only nodes (task #52).
		if b.shared != nil {
			if raw, mErr := json.Marshal(reg); mErr == nil {
				// Public -> public registry; private -> the SEPARATE private namespace, so a
				// peer re-learns a band after a redeploy without it ever leaking into /discover.
				toPublish = append(toPublish, pubReg{reg.NodeID, raw, reg.Private})
			}
		}
		n++
	}
	if n > 0 {
		log.Printf("re-hydrated %d node registration(s) from the store (liveness re-confirmed on next heartbeat)", n)
	}
	b.mu.Unlock()
	// Publish OUTSIDE the lock: putNode is a Valkey round-trip and rehydrate runs at
	// startup; no need to hold b.mu across the network calls.
	for _, p := range toPublish {
		var err error
		if p.private {
			err = b.shared.putPrivateNode(p.id, p.raw, livenessTTL)
		} else {
			err = b.shared.putNode(p.id, p.raw, livenessTTL)
		}
		if err != nil {
			log.Printf("re-hydrate: shared registry re-publish of node %s failed: %v", p.id, err)
		}
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

// offersTTS reports whether any offer is a TTS (public voice) offer. Only a TTS offer becomes a
// public /voices entry, so the station-uniqueness reservation fires only for these — a chat/stt
// node under a station reserves no public callsign.
func offersTTS(offers []protocol.ModelOffer) bool {
	for _, o := range offers {
		if o.Modality == protocol.ModalityTTS {
			return true
		}
	}
	return false
}

// applyOfferOverrides re-seeds a node's offers IN PLACE from the owner-authored
// price/schedule overrides set on the web Console, so the owner's web-set price is the
// EFFECTIVE PUBLISHED price and SURVIVES node re-registration: register calls this on
// every register BEFORE the offers land in b.nodes (ActivePrice reads them at serve
// time) and BEFORE they are persisted (so a restart re-hydrates the overridden offers).
// Only an OWNER-BOUND node carries overrides (owner != ""); each override is applied
// only when its stored owner matches the node's resolved owner, so it can never shadow
// another account's node. Overrides were ceiling-validated when SET, so re-applying
// them here cannot land an out-of-bounds price. This sets only the PUBLISHED/future
// price - past receipts and ledger rows are immutable and untouched.
// It returns the model names whose offer was actually overridden, so the register
// RESPONSE can tell the node which of its prices the broker is now publishing on its
// behalf (the CLI surfaces "broker override active" off this list).
func (b *broker) applyOfferOverrides(owner, node string, offers []protocol.ModelOffer) []string {
	if b.db == nil || owner == "" {
		return nil
	}
	var overridden []string
	for i := range offers {
		ov, ok, err := b.db.OfferOverride(node, offers[i].Model)
		if err != nil || !ok || ov.Owner != owner {
			continue
		}
		offers[i].PriceIn = ov.PriceIn
		offers[i].PriceOut = ov.PriceOut
		offers[i].Schedule = ov.Schedule
		overridden = append(overridden, offers[i].Model)
	}
	return overridden
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
	t, tok := b.tunnelFor(m.NodeID)
	if t == nil {
		jsonErr(w, http.StatusNotFound, "unknown node")
		return
	}
	if !authNode(r, tok) {
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
	t, tok := b.tunnelFor(node)
	if t == nil {
		jsonErr(w, http.StatusNotFound, "unknown node")
		return
	}
	if !authNode(r, tok) {
		jsonErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	b.markSeen(node)

	// MULTI-INSTANCE (Stage 2): a job for this node may have been dispatched on a PEER
	// instance, so subscribe to the node's bus channel for the life of this long-poll.
	// In multi-instance mode the relay dispatches ONLY over the bus (single delivery
	// path - no double-serve), and a local poller receives its own instance's dispatch
	// over the same bus, so we wait on the bus channel here. On a bus subscribe error we
	// fall through to a 204 re-poll (the node simply re-polls; no job is lost because the
	// dispatcher's publish would have reported 0 subscribers and failed that relay
	// cleanly). The local t.jobs channel is still drained too, so a flag flip / mixed
	// fleet can never strand a job already sitting in the in-memory queue.
	if b.multiInstance && b.shared != nil {
		busJobs, cancel, err := b.shared.busSubscribeJobs(r.Context(), node)
		if err != nil {
			w.WriteHeader(http.StatusNoContent) // bus unavailable: re-poll
			return
		}
		defer cancel()
		select {
		case job := <-t.jobs: // drain any in-memory job (mixed-mode safety)
			_ = json.NewEncoder(w).Encode(job)
		case raw, ok := <-busJobs:
			if !ok {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			var job protocol.Job
			if json.Unmarshal(raw, &job) != nil {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			// SINGLE DELIVERY: busPublishJob is a fan-out PUBLISH, so every one of this node's
			// parallel pollers (across instances) just received this same job. Claim it so exactly
			// ONE poller serves it; a poller that loses the claim re-polls (204) instead of serving
			// a duplicate (N-fold billing + interleaved corrupted streams). On a claim-store error
			// we fall through and serve, degrading to today's fan-out on a rare outage rather than
			// stranding the job (no poller would serve it -> the consumer 504s).
			if won, cerr := b.shared.busClaimJob(job.ID); cerr == nil && !won {
				w.WriteHeader(http.StatusNoContent) // another poller won this job
				return
			}
			_ = json.NewEncoder(w).Encode(job)
		case <-time.After(25 * time.Second):
			w.WriteHeader(http.StatusNoContent) // re-poll
		}
		return
	}

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
	t, tok := b.tunnelFor(node)
	if t == nil {
		jsonErr(w, http.StatusNotFound, "unknown node")
		return
	}
	if !authNode(r, tok) {
		jsonErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	var res protocol.JobResult
	if err := json.Unmarshal(body, &res); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad result")
		return
	}
	// MULTI-INSTANCE (Stage 2): the relay awaiting this result may be on a PEER
	// instance, so publish the raw result bytes back on the per-job bus channel it is
	// subscribed to. In multi-instance mode the relay ALWAYS awaits over the bus (even
	// when it happens to be local), so this is the single delivery path - no
	// double-serve. A bus publish error is surfaced to the node (the relay's own timeout
	// is the backstop: it fails the request cleanly and refunds the hold).
	if b.multiInstance && b.shared != nil {
		if err := b.shared.busPublishResult(res.ID, body); err != nil {
			jsonErr(w, http.StatusServiceUnavailable, "result bus unavailable")
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
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
	} else {
		// Any UNAUTHENTICATED caller that resolves to the shared "anon" identity (no
		// signed/grant identity) would otherwise share ONE relay bucket for the whole
		// public surface, so enforce a SEPARATE per-IP limit first (keyed on the
		// validated CF-Connecting-IP). A signed caller has its own per-identity bucket
		// (keyed on its pubkey-derived id) and skips this. The relay spend gate below
		// already 401s a bare unsigned request, so this is the defense for any no-auth
		// relay path AND keeps the per-IP discipline uniform with /discover + concierge.
		// See loadAnonRateLimiter.
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
	// Smart-router v2 request shape: the user-preference knob (cheap/balanced/fast/
	// reliable; default balanced), and a prompt-size estimate that makes speedFit
	// request-size-aware (a long prompt evicts weak hardware). totalReqs feeds the UCB
	// exploration radius. None of these touch the hard filters.
	routePref := parsePref(r.Header.Get("X-Roger-Pref"))
	promptTokens := len(body)/4 + 1 // ~chars/4 tokens; over-estimates from JSON (safe)
	b.totalReqs.Add(1)
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
	// Request id is minted up front so the routing PRNG can be seeded from it
	// deterministically (the same id keys the relayed job below). Power-of-two-choices
	// spread is reproducible per request; a fixed pin / single candidate / cheap profile
	// still resolves to the deterministic best.
	requestID := protocol.NewRequestID()
	b.mu.Lock()
	node, offer, ok := b.pickFor(req.Model, confidentialOnly, minTPS, maxPrice, maxPriceOut, pinNode, exclude, allow, privateAllow,
		pickReq{pref: routePref, promptTokens: promptTokens, rng: seededRand(requestID)})
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
			jsonErr(w, http.StatusUnauthorized, "log in to spend on paid models - run `roger login` (free models and grant keys work without an account)")
			return
		}
	}

	// Pre-authorize an upper-bound cost (a "hold") BEFORE doing any work, so
	// concurrent requests can never drive a wallet negative (free inference). The
	// hold is captured (Finalize) or returned (ReleaseHold) on every exit path. A
	// $0 (free/self) request places no hold - there is nothing to protect.
	// Size the hold at the request's TRUE upper-bound price so the settle-time clamp
	// (cost > maxCost below) is a real ceiling, NOT a floor-to-~0. For a FIXED plan
	// (grant / self) pricing.in/out are already the billed price. For the PUBLIC-market
	// plan (fixed=false) they are zero - the relay applies the offer's active price only
	// at settle - so we MUST resolve that active price here too. Without this, every paid
	// public request holds ~$0, monthlyCapCheck never trips, and the clamp caps the real
	// cost down to ~$0: paid public inference would be effectively FREE and providers
	// would earn nothing. (C1.)
	holdIn, holdOut := pricing.in, pricing.out
	if !pricing.fixed {
		ain, aout, afree, _ := offer.ActivePrice(time.Now())
		holdIn, holdOut = ain, aout
		if afree {
			holdIn, holdOut = 0, 0
		}
	}
	maxCost := estimateMaxCost(body, holdIn, holdOut, offer.Ctx)
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
		// Seed new users so the hold can land (W4: skip the upsert tx for an already-
		// seeded wallet via the Redis seeded flag; Postgres ON-CONFLICT stays the real
		// guard, so a lost flag just re-runs the harmless no-op upsert). A seed-tx
		// failure is the SAME retryable store failure as a hold failure below - it must
		// never fall through to HoldFor, where the unseeded wallet would misread as a
		// 402 "insufficient balance" (features/money/seed_failure.feature).
		if serr := b.ensureSeeded(payer); serr != nil {
			jsonErr(w, http.StatusInternalServerError, "wallet error")
			return
		}
		held, herr := b.db.HoldFor(payer, requestID, maxCost) // tracked: the deploy-orphan sweep reclaims it if this relay is SIGKILLed mid-flight
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
	job := protocol.Job{ID: requestID, User: b.pseudonym(user, node.NodeID), Body: body}
	resCh := make(chan protocol.JobResult, 1)
	t.mu.Lock()
	t.waiters[job.ID] = resCh
	t.mu.Unlock()
	defer func() { t.mu.Lock(); delete(t.waiters, job.ID); t.mu.Unlock() }()

	if req.Stream {
		b.relayStream(w, t, node, offer, streamBill{user: payer, consumer: user, model: req.Model, pricing: pricing, grantID: grantID}, job, resCh, maxCost)
		return
	}

	settled := false
	defer func() {
		if !settled && maxCost > 0 {
			b.db.ReleaseHoldFor(payer, requestID) // refund + clear the tracked hold if we never captured it (idempotent vs the sweep)
		}
	}()

	start := time.Now()
	b.enterInflight(node.NodeID)
	// Concurrency at dispatch (includes self): drives the under-load capacity
	// measurement (concurrentTPS is only sampled when this is >= 2).
	concurrentAtDispatch := b.inflightOf(node.NodeID)

	// MULTI-INSTANCE (Stage 2): the poller for this node may be on a PEER instance, so
	// dispatch + await the result over the Valkey bus. Subscribe to the per-job result
	// channel BEFORE publishing the job so a fast peer result cannot race ahead of our
	// subscription. busDispatch returns the result channel; on any bus error it fails
	// the request cleanly (the deferred ReleaseHold refunds the pre-auth hold - never a
	// double-charge). delivered==0 means no poller is listening on ANY instance, exactly
	// like a full local job channel today -> "node busy".
	var busRes <-chan []byte
	if b.multiInstance && b.shared != nil {
		ch, cancel, derr := b.busDispatchJob(r.Context(), node.NodeID, job)
		if cancel != nil {
			defer cancel()
		}
		if derr != nil {
			b.exitInflight(node.NodeID, false)
			if derr == errNoPoller {
				b.stats.busNoPoller.Add(1)
				jsonErr(w, http.StatusServiceUnavailable, "node busy (no poller free)")
			} else {
				b.stats.busDispatchErr.Add(1)
				jsonErr(w, http.StatusServiceUnavailable, "dispatch bus unavailable")
			}
			return
		}
		b.stats.busDispatch.Add(1)
		busRes = ch
	} else {
		select {
		case t.jobs <- job:
			b.stats.localDispatch.Add(1)
		case <-time.After(3 * time.Second):
			b.exitInflight(node.NodeID, false)
			jsonErr(w, http.StatusServiceUnavailable, "node busy (no poller free)")
			return
		}
	}

	// Unify the local and bus result channels into one resCh the select below waits on.
	// In multi-instance mode a goroutine decodes the raw bus result and forwards it.
	if busRes != nil {
		go func() {
			raw, ok := <-busRes
			if !ok {
				return // bus closed; the timeout below fails the request cleanly
			}
			var br protocol.JobResult
			if json.Unmarshal(raw, &br) == nil {
				select {
				case resCh <- br:
				default:
				}
			}
		}()
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
			completion := completionText(res.Body)
			// VOID-ON-NO-OUTPUT (P0): a request that produced NO usable output must not
			// be charged and must mint no earning, regardless of input consumed. "No
			// usable output" = the node errored (status>=400), OR the completion is
			// empty/whitespace, OR it claimed completion tokens but emitted no text. We
			// leave settled=false so the deferred ReleaseHold refunds the consumer's
			// pre-auth hold in FULL, and flag the owner for evidence (Part 4). A $0
			// metering receipt is still recorded so the request is auditable.
			producedOutput := producedUsableOutput(res.Status, completion, rec.CompletionTokens)
			if !producedOutput {
				b.flagEmptyOutput(node.NodeID, rec, res.Status)
				log.Printf("VOID no-output user=%s node=%s status=%d claimIn=%d claimOut=%d - $0, hold refunded",
					user, node.NodeID, res.Status, rec.PromptTokens, rec.CompletionTokens)
				if b.db != nil {
					rec.SignBroker(b.priv)
					_, _ = b.db.Settle(payer, node.NodeID, 0, 0, rec) // $0 metering receipt for lineage
				}
				w.Header().Set("X-RogerAI-Cost", "0")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(res.Status)
				_, _ = w.Write(res.Body)
				return
			}
			// P0-2 (symmetric): settle on min(nodeClaim, brokerRecount) on BOTH axes when
			// an exact broker re-count exists, so an over-reporting node is billed (and
			// earns) on the verified counts, not its unverified claim. The input axis adds
			// a hard fail-closed byte floor (claimed prompt tokens > body bytes is
			// impossible -> clamp + strike). The node-signed receipt is left intact; we
			// only change the BILLED counts (via CostWith2 + the Broker*Tokens fields).
			billedPrompt := b.settleRecountPrompt(node.NodeID, rec.RequestID, recountModel(rec, req.Model), promptText(body), rec.PromptTokens, len(body))
			billedCompletion := b.settleRecount(node.NodeID, rec.RequestID, recountModel(rec, req.Model), completion, rec.CompletionTokens)
			rec.BrokerPromptTokens, rec.BrokerCompletionTokens = billedPrompt, billedCompletion
			// SignBroker is called AFTER the broker counts are assigned so the broker
			// counter-signature covers them (the node-sig excludes them via signingBytes).
			rec.SignBroker(b.priv)
			cost := rec.CostWith2(billedPrompt, billedCompletion)
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
				// Smart-router v2 reward + capacity evidence: a quality-validated completion
				// (status<500, non-empty, output tokens > 0) increments successCount (shrinks
				// the UCB radius) and - when served under load - folds tps into the capacity
				// estimate. A 200-with-empty-body does NOT count.
				qOK := res.Status < 500 && rec.CompletionTokens > 0 && qualityOK(res.Body)
				b.recordServed(node.NodeID, qOK, tps, concurrentAtDispatch)
				// We just measured this node for FREE off real traffic: reset its probe
				// backoff + push the next probe out, so an actively-used node is barely
				// probed (and reads as freshly verified, not stale).
				b.markMeasured(node.NodeID)
				w.Header().Set("X-RogerAI-Receipt", protocol.EncodeReceipt(rec))
				w.Header().Set("X-RogerAI-Provider", node.NodeID)
				// EXACT cost (not round6): a real sub-microcredit charge (e.g. a few output
				// tokens at $0.01/1M ~ $0.00000036) must reach the client nonzero so dollars()
				// shows the truth, never a bare $0.00 for a paid turn. See fmtCostHeader; the
				// LEDGER still settles `cost` at full precision (settleRequest above).
				w.Header().Set("X-RogerAI-Cost", fmtCostHeader(cost))
				// The BILLED token counts (the very prompt/completion counts the cost above was
				// computed from — min(claim, broker re-count) per axis, with the input byte
				// floor). Emitted as DISPLAY headers so a non-streaming consumer (the [0] AGENT
				// harness meter) can show an honest ↑in ↓out beside the cost. This exposes the
				// already-settled value; it does NOT touch billing (the ledger settled `cost`).
				w.Header().Set("X-RogerAI-Tokens-In", strconv.Itoa(billedPrompt))
				w.Header().Set("X-RogerAI-Tokens-Out", strconv.Itoa(billedCompletion))
				w.Header().Set("X-RogerAI-Balance", ftoa(round6(newBal)))
				lockedUntil := int64(0)
				if !until.IsZero() {
					lockedUntil = until.Unix()
				}
				w.Header().Set("X-RogerAI-Price", fmt.Sprintf("in=%.4f;out=%.4f;locked_until=%d", pin, pout, lockedUntil))
				w.Header().Set("X-RogerAI-TPS", fmt.Sprintf("%.1f", tps))
				w.Header().Set("X-RogerAI-Quality", ftoa(round6(b.trustScore(node.NodeID))))
				log.Printf("relay user=%s node=%s in=%d/%d out=%d/%d (billed/claim) price=%.3f/%.3f cost=%.6f tps=%.1f", user, node.NodeID, billedPrompt, rec.PromptTokens, billedCompletion, rec.CompletionTokens, pin, pout, cost, tps)
				// The L1 re-count (trust scoring + the P0-2 promotion-hold flag) already
				// ran via settleRecount above (single sidecar call), so it is not repeated
				// here - that also makes the billed completion the re-counted one.
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(res.Status)
		_, _ = w.Write(res.Body)
	case <-time.After(nonStreamRelayWait):
		// CLOUDFLARE ~100s PROXY CAP: CF aborts a proxied request that has produced NO
		// response bytes after ~100s with an opaque 524 the client cannot retry on
		// cleanly. This NON-stream branch writes nothing until the result arrives, so we
		// must return BEFORE CF's cap: nonStreamRelayWait (90s) is comfortably under it,
		// so the broker emits its own clean, retryable 504 ("node timed out") instead of
		// a CF 524. A genuinely slow provider should be consumed with stream:true (the
		// streaming branch flushes headers immediately, resetting CF's idle clock).
		b.exitInflight(node.NodeID, false)
		// Diagnosability (#2): a silent failover hid WHY a relay failed. Log the node that
		// produced no result within the window so "is the broker getting a clean response
		// from that model?" is answerable straight from the logs (pairs with the VOID
		// no-output line above, which logs a node's non-2xx/empty status).
		log.Printf("relay TIMEOUT user=%s node=%s model=%s - no result in %s (node slow/unresponsive); 504, failing over",
			user, node.NodeID, req.Model, nonStreamRelayWait)
		jsonErr(w, http.StatusGatewayTimeout, "node timed out (use stream:true for slow models)")
	}
}

// nonStreamRelayWait bounds how long the NON-stream relay waits for a provider result
// before returning a clean, retryable 504. It is held BELOW Cloudflare's ~100s proxy
// cap (CF emits an opaque 524 if a proxied request produces no bytes within ~100s) so
// the consumer always gets the broker's own 504 rather than CF's untyped 524. Slow
// providers should be consumed with stream:true, which flushes headers immediately and
// keeps the CF connection alive for the full 300s stream window.
// var (not const) so the error-passthrough BDD's timeout scenario can shorten it for one
// scenario instead of sleeping the full production window; production never mutates it.
var nonStreamRelayWait = 90 * time.Second

// errNoPoller is the dispatch sentinel for "no provider is long-polling this node on
// ANY instance right now" - the cross-instance equivalent of a full local job channel.
// The relay maps it to the same "node busy (no poller free)" 503 it returns today.
var errNoPoller = fmt.Errorf("no poller listening")

// busDispatchJob is the MULTI-INSTANCE non-stream dispatch: subscribe to the per-job
// RESULT channel FIRST (so a peer's fast result cannot be published before we are
// listening), then publish the job onto the node's bus channel. It returns the result
// channel + a cancel for the subscription. delivered==0 (no subscriber) returns
// errNoPoller so the relay reports "node busy" exactly as a full local queue would; any
// other bus error returns that error so the relay fails the request cleanly. On any
// error the subscription is torn down before returning.
func (b *broker) busDispatchJob(ctx context.Context, nodeID string, job protocol.Job) (<-chan []byte, func(), error) {
	raw, err := json.Marshal(job)
	if err != nil {
		return nil, nil, err
	}
	resCh, cancel, err := b.shared.busSubscribeResult(ctx, job.ID)
	if err != nil {
		return nil, nil, err
	}
	delivered, perr := b.shared.busPublishJob(nodeID, raw)
	if perr != nil {
		cancel()
		return nil, nil, perr
	}
	if delivered == 0 {
		cancel()
		return nil, nil, errNoPoller
	}
	return resCh, cancel, nil
}

// relayStream handles the streaming path of POST /v1/chat/completions: it sends SSE
// headers, registers the client as a sink, and enqueues the job. The node pipes
// chunks via /agent/stream straight to this client; when it finishes it posts a
// receipt (resCh) which settles the wallet. No metering headers (already streaming).
func (b *broker) relayStream(w http.ResponseWriter, t *nodeTunnel, node protocol.NodeRegistration, offer protocol.ModelOffer, bill streamBill, job protocol.Job, resCh chan protocol.JobResult, maxCost float64) {
	user, consumer, model, pricing, grantID := bill.user, bill.consumer, bill.model, bill.pricing, bill.grantID
	settled := false
	defer func() {
		if !settled && maxCost > 0 {
			b.db.ReleaseHoldFor(user, job.ID) // refund + clear the tracked hold if we never captured it (idempotent vs the sweep)
		}
	}()
	flusher, ok := w.(http.Flusher)
	if !ok {
		jsonErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	start := time.Now()
	sink := &streamSink{w: w, flush: flusher.Flush, nodeID: node.NodeID, start: start}
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

	b.enterInflight(node.NodeID)
	concurrentAtDispatch := b.inflightOf(node.NodeID)

	// MULTI-INSTANCE (Stage 2): the poller serving this stream may be on a PEER
	// instance, which pipes its SSE chunks over the per-job stream bus channel and the
	// final receipt over the per-job result channel. Subscribe to BOTH before dispatch
	// (so a fast peer cannot publish ahead of our subscription), then publish the job. A
	// pump goroutine writes each bus chunk to THIS client in order (and siphons a bounded
	// copy into sink.cap for the L1 re-count, exactly as agentStream does locally), so
	// the rest of this function - the receipt verify / void / settle block below - is
	// IDENTICAL on both paths. On any bus error we fail cleanly: headers are already
	// sent, so the client gets an empty/short stream and the deferred ReleaseHold refunds
	// the hold (never a double-charge).
	if b.multiInstance && b.shared != nil {
		streamCtx, streamCancel := context.WithCancel(context.Background())
		defer streamCancel()
		busStream, scancel, serr := b.shared.busSubscribeStream(streamCtx, job.ID)
		if serr != nil {
			b.stats.busDispatchErr.Add(1)
			b.exitInflight(node.NodeID, false)
			return
		}
		defer scancel()
		ch, rcancel, derr := b.busDispatchJob(streamCtx, node.NodeID, job)
		if rcancel != nil {
			defer rcancel()
		}
		if derr != nil {
			if derr == errNoPoller {
				b.stats.busNoPoller.Add(1)
			} else {
				b.stats.busDispatchErr.Add(1)
			}
			b.exitInflight(node.NodeID, false)
			return // headers already sent; the client gets an empty stream
		}
		b.stats.busDispatch.Add(1)
		// Pump bus chunks -> client (+ capture). Runs until the done marker or the bus
		// closes; relays each frame in order and flushes, mirroring agentStream's local
		// write+flush+capture so settlement reads the same captured completion. pumpDone is
		// closed when the pump exits; relayStream waits on it (bounded) BEFORE returning so
		// no goroutine writes the client ResponseWriter after the handler has returned (a
		// concurrent-write hazard on the real http.ResponseWriter, the same way the
		// single-instance path's writer - agentStream - finishes before the receipt-driven
		// return).
		pumpDone := make(chan struct{})
		defer func() {
			select {
			case <-pumpDone:
			case <-time.After(2 * time.Second):
				// The done marker never arrived (bus hiccup): cancel the subscription so the
				// pump's range over busStream ends, then it closes pumpDone.
				streamCancel()
				<-pumpDone
			}
		}()
		go func() {
			defer close(pumpDone)
			for fr := range busStream {
				if fr.isDone {
					return
				}
				sink.w.Write(fr.payload)
				sink.flush()
				if sink.cap != nil {
					sink.capMu.Lock()
					if sink.cap.Len()+sink.capRaw.Len() < maxRecountCapture {
						sink.capRaw.Write(fr.payload)
						drainSSEDeltas(&sink.capRaw, sink.cap)
					}
					sink.capMu.Unlock()
				}
			}
		}()
		// Forward the decoded bus result into resCh so the settlement select below is
		// shared with the single-instance path.
		go func() {
			raw, ok := <-ch
			if !ok {
				return
			}
			var br protocol.JobResult
			if json.Unmarshal(raw, &br) == nil {
				select {
				case resCh <- br:
				default:
				}
			}
		}()
	} else {
		select {
		case t.jobs <- job:
			b.stats.localDispatch.Add(1)
		case <-time.After(3 * time.Second):
			b.exitInflight(node.NodeID, false)
			return // headers already sent; the client just gets an empty stream
		}
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
					// Lock keyed on the SIGNED consumer identity (not the payer wallet) so the
					// streaming path shares the SAME 24h price-lock the non-stream relay mints -
					// otherwise a logged-in user's stream would dodge the lock (different key) and
					// eat an owner's mid-engagement hike. See streamBill.consumer.
					pin, pout, _ = b.lockedPrice(consumer, node.NodeID, model, curIn, curOut)
				}
			}
			rec.PriceIn, rec.PriceOut = pin, pout
			rec.GrantID = grantID
			// The stream has finished (the receipt arrived), so the captured completion
			// text is complete. (cap is non-nil only when the L1 re-count is enabled; on
			// a no-recount broker we fall back to the receipt's token count for the void
			// + reward signals.)
			completion := ""
			if sink.cap != nil {
				sink.capMu.Lock()
				completion = sink.cap.String()
				sink.capMu.Unlock()
			}
			// VOID-ON-NO-OUTPUT (P0), stream path. When capture is enabled we know the
			// stream was empty if the captured text is blank; without capture we fall
			// back to the receipt's claimed completion tokens + status. An errored or
			// no-output stream charges $0, mints no earning, and the deferred ReleaseHold
			// refunds the consumer's hold in full.
			var producedOutput bool
			if sink.cap != nil {
				// Capture on: use the same predicate as the relay path off the captured text.
				producedOutput = producedUsableOutput(res.Status, completion, rec.CompletionTokens)
			} else {
				// No capture: fall back to status + the receipt's claimed completion tokens.
				producedOutput = res.Status < 400 && rec.CompletionTokens > 0
			}
			if !producedOutput {
				b.flagEmptyOutput(node.NodeID, rec, res.Status)
				log.Printf("VOID no-output (stream) user=%s node=%s status=%d claimIn=%d claimOut=%d - $0, hold refunded",
					user, node.NodeID, res.Status, rec.PromptTokens, rec.CompletionTokens)
				if b.db != nil {
					rec.SignBroker(b.priv)
					_, _ = b.db.Settle(user, node.NodeID, 0, 0, rec) // $0 metering receipt
				}
				return // settled stays false -> deferred ReleaseHold refunds the hold
			}
			// P0-2 (symmetric): bill min(nodeClaim, brokerRecount) on BOTH axes. The
			// prompt text is the request body (job.Body), available on this path too, so
			// the input byte-floor + recount apply identically to the relay path.
			billedPrompt := b.settleRecountPrompt(node.NodeID, rec.RequestID, recountModel(rec, model), promptText(job.Body), rec.PromptTokens, len(job.Body))
			billedCompletion := b.settleRecount(node.NodeID, rec.RequestID, recountModel(rec, model), completion, rec.CompletionTokens)
			rec.BrokerPromptTokens, rec.BrokerCompletionTokens = billedPrompt, billedCompletion
			// SignBroker AFTER the broker counts are assigned (covers them).
			rec.SignBroker(b.priv)
			cost := rec.CostWith2(billedPrompt, billedCompletion)
			if maxCost > 0 && cost > maxCost {
				cost = maxCost
			}
			if _, ferr := b.settleRequest(user, node.NodeID, maxCost, cost, rec, grantID, pricing.free); ferr != nil {
				// settle failed - leave settled=false so the deferred ReleaseHold refunds
				log.Printf("stream settle FAILED user=%s node=%s: %v - releasing hold", user, node.NodeID, ferr)
			} else {
				settled = true
			}
			streamTPS := 0.0
			if rec.CompletionTokens > 0 {
				if el := time.Since(start).Seconds(); el > 0 {
					streamTPS = float64(rec.CompletionTokens) / el
					b.updateTPS(node.NodeID, streamTPS)
				}
			}
			// Smart-router v2 reward + capacity evidence (streamed). This block only runs
			// when producedOutput is true (an errored/empty stream returned above), so a
			// leech can never shrink its UCB radius off a no-output stream.
			qOK := rec.CompletionTokens > 0 && (completion == "" || qualityOKText(completion))
			b.recordServed(node.NodeID, qOK, streamTPS, concurrentAtDispatch)
			// Free measurement off real (streamed) traffic: reset the probe backoff so
			// an actively-used node is barely probed and reads as freshly verified.
			b.markMeasured(node.NodeID)
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
	// tunnelFor, not a bare map read: the LB can land the node's stream POST on a
	// process that has not yet synced the registry (the same window /agent/poll and
	// /agent/result already heal by lazily learning the node from the shared registry).
	// A bare local read 401'd the chunks there and the client got an empty stream.
	// Pinned by features/multinode/cross_instance_relay.feature ("stream chunks posted
	// to the instance that never saw the node").
	t, tok := b.tunnelFor(nodeID)
	if t == nil || !authNode(r, tok) {
		jsonErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	// MULTI-INSTANCE (Stage 2): the waiting client's stream sink may live on a PEER
	// instance (the relay that picked this node ran elsewhere), so forward each SSE chunk
	// over the per-job stream bus channel in order, then publish the terminal done
	// marker. Redis pub/sub preserves per-channel order from this single publisher, so
	// the originating instance writes the chunks to its client in the same order. We do
	// NOT also write a local sink in this mode: relayStream subscribes to the bus
	// (regardless of co-location), so the bus is the single ordered path - writing both
	// would double-deliver. A bus publish error ends the forward; the relay's stream
	// timeout is the backstop (it fails/closes the client stream cleanly).
	if b.multiInstance && b.shared != nil {
		buf := make([]byte, 8192)
		for {
			n, err := r.Body.Read(buf)
			if n > 0 {
				if perr := b.shared.busPublishStreamChunk(jobID, buf[:n]); perr != nil {
					break // bus down: stop forwarding; relay times out + closes cleanly
				}
			}
			if err != nil {
				break
			}
		}
		_ = b.shared.busPublishStreamDone(jobID)
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
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
			// Organic first-byte latency (smart-router v2): record time-to-first-token
			// the moment we have streamed at least minFirstTokens worth of MEANINGFUL
			// text - a node can't win TTFT by emitting a bare space then stalling. One
			// sample per stream, folded into the node's ttftMs EWMA (the same EWMA the
			// probe feeds), so a busy node's latency reads organically, not probe-only.
			sink.capMu.Lock()
			if !sink.ttftDone && !sink.start.IsZero() {
				sink.ttftSeen += meaningfulChars(buf[:n])
				if sink.ttftSeen >= minFirstTokens {
					sink.ttftDone = true
					ttftMs := float64(time.Since(sink.start).Microseconds()) / 1000.0
					b.observeOrganicTTFT(sink.nodeID, ttftMs)
				}
			}
			// Capture the streamed completion text (off-band, for the L1 re-count
			// at stream end). The bytes still go straight to the client above; this
			// only siphons a copy when capture is enabled. BOUNDED: a malicious node
			// could stream an unbounded body to OOM the broker (512MB box) via this
			// off-band copy, so we stop capturing once cap + the carry reach
			// maxRecountCapture. The L1 re-count needs a REPRESENTATIVE sample, not the
			// verbatim completion, so a prefix is sufficient; the client still receives
			// the full stream (the cap only bounds our private copy).
			if sink.cap != nil && sink.cap.Len()+sink.capRaw.Len() < maxRecountCapture {
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

// minFirstTokens is the meaningful-character floor a stream must emit before its
// time-to-first-token is recorded - a node can't game organic TTFT by streaming a
// bare space then stalling.
const minFirstTokens = 4

// meaningfulChars counts the non-whitespace assistant delta characters in a slab of
// SSE bytes (best-effort, line-split-tolerant). Used only as a guard for the organic
// TTFT sample, so an exact count is unnecessary.
func meaningfulChars(p []byte) int {
	n := 0
	for _, line := range bytes.Split(p, []byte{'\n'}) {
		for _, r := range sseDelta(line) {
			if r != ' ' && r != '\t' && r != '\n' && r != '\r' {
				n++
			}
		}
	}
	return n
}

// observeOrganicTTFT folds an organically-measured first-byte latency (ms) into the
// node's ttftMs EWMA - the same field the probe feeds - so a busy node's latency
// stays fresh from real traffic, not just idle probes. Same 0.3 weight as the probe.
func (b *broker) observeOrganicTTFT(nodeID string, ttftMs float64) {
	if ttftMs <= 0 {
		return
	}
	b.metricsMu.Lock()
	tq := b.trust[nodeID]
	if tq.ttftMs > 0 {
		tq.ttftMs = 0.3*ttftMs + 0.7*tq.ttftMs
	} else {
		tq.ttftMs = ttftMs
	}
	b.trust[nodeID] = tq
	b.metricsMu.Unlock()
}

// pickReq carries the request-shaped routing inputs the smart-router v2 score uses
// on top of the hard-filter args: the user-preference knob, the prompt size (drives
// the request-size-aware speedFit), and a seeded PRNG for power-of-two-choices
// spread. The zero value (prefBalanced, no prompt, nil rng) reproduces the legacy
// deterministic top-1 route, so callers/tests that don't supply it are unchanged.
// offerModality normalizes an offer's or request's modality: an empty value is the chat
// back-compat default, so a pre-voice node (no modality) still matches a chat request. Used by
// pickFor's isolation gate so voice + chat never cross-route.
func offerModality(m string) string {
	if m == "" {
		return protocol.ModalityChat
	}
	return m
}

type pickReq struct {
	pref         pref
	promptTokens int
	rng          *rand.Rand // nil => deterministic top-1 (no P2C spread)
	modality     string     // "" / "chat" match chat offers; "tts" / "stt" match voice offers
}

// pickFor is the smart-router v2 selection (the winning spec). For each ELIGIBLE
// candidate it computes
//
//	score = ucb( reliability * speedFit * priceMod ) * loadFactor
//
// with a multiplicative reliability spine (price can only nudge within the user's
// range), capacity-normalized load, and a UCB exploration radius for cold-start;
// then it selects with capacity-aware power-of-two-choices over a reliability-bounded
// top band (no all-to-one pile-up). A two-tier health gate is the absolute floor:
// only Tier-A (probeFails<2 and success>=0.55-or-unmeasured) candidates compete;
// Tier-B is used only when Tier-A is empty (a transient blip never blanks a model).
//
// All hard filters (price caps, min-tps, confidential, private/freq, banned, grant
// allow-list, pin/exclude) and the adaptive-probe refresh are PRESERVED unchanged.
// Caller holds b.mu.
// bannedOwnerNodeSet precomputes which on-air nodes resolve to a BANNED owner account, so
// the pick/score loop can drop them with an O(1) map lookup instead of a per-candidate
// AccountOfNode call under metricsMu. Returns nil when no owner is banned (the common case
// => zero work). The owner bindings are resolved via the cache OUTSIDE metricsMu: the ban
// set + on-air node ids are snapshotted under a brief lock, then released BEFORE the (cached)
// binding lookups, so a single banned owner no longer serializes every pick on N store
// round-trips under the global lock. A node that registers after the snapshot is caught by
// the settle-time owner-ban backstop (settleRequest), so nothing slips through unbilled-safe.
func (b *broker) bannedOwnerNodeSet() map[string]bool {
	b.metricsMu.Lock()
	if len(b.bannedOwners) == 0 {
		b.metricsMu.Unlock()
		return nil
	}
	owners := make(map[string]bool, len(b.bannedOwners))
	for o := range b.bannedOwners {
		owners[o] = true
	}
	ids := make([]string, 0, len(b.nodes))
	for id := range b.nodes {
		ids = append(ids, id)
	}
	b.metricsMu.Unlock()

	banned := make(map[string]bool)
	for _, id := range ids {
		if acct, found := b.cachedOwnerOf(id); found && owners[acct] {
			banned[id] = true
		}
	}
	return banned
}

func (b *broker) pickFor(model string, confidentialOnly bool, minTPS, maxPriceIn, maxPriceOut float64, pin string, exclude, allow, privateAllow map[string]bool, req pickReq) (protocol.NodeRegistration, protocol.ModelOffer, bool) {
	now := time.Now()
	w := req.pref.weights()

	// Owner-ban filter precomputed OUTSIDE metricsMu (nil when no owner is banned): the
	// scoring loop below then drops a banned owner's nodes with an O(1) lookup instead of a
	// store round-trip per candidate under the global lock. See bannedOwnerNodeSet.
	bannedNode := b.bannedOwnerNodeSet()

	// Per-candidate evidence collected during the single eligibility pass. We score
	// in a SECOND pass once rangeMin/rangeMax (the cheapest/dearest eligible out-price)
	// are known, since priceMod is range-relative.
	type cand struct {
		node     protocol.NodeRegistration
		offer    protocol.ModelOffer
		out      float64
		inflight int
		capacity int
		rel      float64 // reliability spine
		fit      float64 // speedFit
		radius   float64 // UCB exploration lift
		tierA    bool    // passes the two-tier health gate
	}

	b.metricsMu.Lock()
	totalReqs := b.totalReqs.Load()
	// Pre-size to the node count (the upper bound on candidates): the eligibility pass appends
	// one cand per surviving node, so a single right-sized allocation avoids the slice's
	// doubling reallocs (P2 - cuts allocs/op + B/op on the hot routing path). Same access as the
	// range below (both under metricsMu).
	cands := make([]cand, 0, len(b.nodes))
	rangeMin, rangeMax := 0.0, 0.0
	haveRange := false

	for _, n := range b.nodes {
		if time.Since(b.lastSeen[n.NodeID]) >= nodeTTL {
			continue
		}
		// --- HARD FILTERS (unchanged): banned, private/freq, pin, exclude, allow,
		// confidential, min-tps. None of these are score-able; they gate eligibility. ---
		if b.banned[n.NodeID] {
			continue
		}
		// DURABLE OWNER BAN (anti-rotation): drop nodes whose resolved owner account is
		// banned, so a banned operator's fresh node id / callsign is never routed to. The
		// banned-node set was precomputed via the cached binding OUTSIDE this lock (nil when
		// no owner is banned, the common case), so this is an O(1) map lookup - never a store
		// round-trip under metricsMu. See bannedOwnerNodeSet.
		if bannedNode[n.NodeID] {
			continue
		}
		if b.private[n.NodeID] && !privateAllow[n.NodeID] {
			continue
		}
		if pin != "" && n.NodeID != pin {
			continue
		}
		if exclude[n.NodeID] {
			continue
		}
		if allow != nil && !allow[n.NodeID] {
			continue
		}
		if confidentialOnly && !b.confidential[n.NodeID] {
			continue
		}
		tq := b.trust[n.NodeID]
		// NOT-SERVING gate: a node that has failed a sustained streak of liveness probes has
		// a dead/unloaded model upstream (it returns fast 5xx/empty). Exclude it entirely -
		// not even Tier-B probation - so a relay returns a clean "no station serving" rather
		// than dispatching into a 504. It still heartbeats, so a recovery (one OK probe)
		// resets the streak and it is eligible again on the next pick.
		if tq.probeFails >= probeDeadStreak {
			continue
		}
		tps := b.tps[n.NodeID]
		if minTPS > 0 && tps > 0 && tps < minTPS {
			continue
		}
		sr, sseen := b.success[n.NodeID]
		// Two-tier health gate (spec 1.4): Tier A = probeFails<2 AND (success unmeasured
		// OR >=0.55). Everything else still on-air is Tier B (probation), used only when
		// Tier A is empty. probeFails>=2 is the raised bar (was 3-strikes) but graded, not
		// a hard zero, inside the reliability spine.
		tierA := tq.probeFails < 2 && (!sseen || sr >= 0.55)
		rel := reliabilityFactor(tq.probed, tq.probeOK, tq.probeFails, sr, sseen, tq.score())
		fit := speedFit(tps, tq.ttftMs, req.promptTokens, w.speedMul)
		// UCB radius is GATED to canary-passed nodes (spec 1.1e): we explore honest-
		// capable nodes, never unproven-flaky ones.
		radius := explorationRadius(tq, w.c, totalReqs, b.successCount[n.NodeID])
		cap := capacityOf(b.concurrentTPS[n.NodeID], n.HW)

		for _, o := range n.Offers {
			if o.Model != model {
				continue
			}
			// Modality isolation: a tts/stt request routes ONLY to that modality's offers, a
			// chat request only to chat — never cross-modality (empty = chat, back-compat).
			if offerModality(o.Modality) != offerModality(req.modality) {
				continue
			}
			in, out, _, _ := o.ActivePrice(now)
			if maxPriceIn > 0 && in > maxPriceIn {
				continue
			}
			if maxPriceOut > 0 && out > maxPriceOut {
				continue
			}
			// Running min/max of the eligible OUTPUT price - the user's effective range
			// for priceMod (spec 1.1c: rangeMin is the cheapest eligible out-price, not
			// the market input-price min). Free (out<=0) offers don't move the min/max.
			rangeMin, rangeMax, haveRange = extendOutRange(out, rangeMin, rangeMax, haveRange)
			// Capacity-aware load is THIS instance's exact local inflight PLUS the merged
			// peer-instance load (Stage 2). peerInflight is the in-memory cross-instance
			// snapshot refreshed on the background loop; it is empty (adds 0) when
			// multi-instance is off, so the single-instance load factor is unchanged.
			inflight := b.inflight[n.NodeID] + b.peerInflight[n.NodeID]
			cands = append(cands, cand{
				node: n, offer: o, out: out, inflight: inflight,
				capacity: cap, rel: rel, fit: fit, radius: radius, tierA: tierA,
			})
		}
	}

	if len(cands) == 0 {
		b.metricsMu.Unlock()
		return protocol.NodeRegistration{}, protocol.ModelOffer{}, false
	}
	// User price cap (when given) widens the range ceiling so "I'll pay up to X but
	// reward me below it" is expressible; else the eligible max is the ceiling.
	rmax := priceCeiling(rangeMax, maxPriceOut)

	// Score each candidate; partition into Tier A (eligible) and Tier B (probation).
	var tierA, tierB []scoredCand
	for i, c := range cands {
		pm := priceMod(c.out, rangeMin, rmax, w.kPrice, w.priceExp)
		s := ucb(c.rel*c.fit*pm, c.radius) * loadFactor(c.inflight, c.capacity)
		load := float64(c.inflight) / float64(maxInt(c.capacity, 1))
		sc := scoredCand{idx: i, score: s, load: load}
		if c.tierA {
			tierA = append(tierA, sc)
		} else {
			tierB = append(tierB, sc)
		}
	}
	// Healthy-beats-failing as an absolute gate: select from Tier A; fall back to Tier
	// B ONLY when Tier A is empty (availability - a transient blip never blanks a model).
	pool := tierA
	if len(pool) == 0 {
		pool = tierB
	}
	chosen := selectP2C(pool, w.beta, req.rng)
	if chosen < 0 {
		b.metricsMu.Unlock()
		return protocol.NodeRegistration{}, protocol.ModelOffer{}, false
	}
	best := cands[chosen]

	// Demand-driven / just-in-time staleness refresh (PRESERVED unchanged): if the
	// routed node's reading is stale, schedule a near-term async probe so the NEXT
	// request routes on fresh data. This request still routes on the current reading.
	if b.probe.enabled() {
		if st := b.probeSched[best.node.NodeID]; st == nil || b.probe.measurementStale(st.lastMeasured, now) {
			b.demandProbeSoonLocked(best.node.NodeID, now)
		}
	}
	b.metricsMu.Unlock()
	return best.node, best.offer, true
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// enterInflight / exitInflight track active requests per node (concurrency-safe).
// exit also folds the outcome into the node's success-rate EWMA.
func (b *broker) enterInflight(node string) {
	b.metricsMu.Lock()
	b.inflight[node]++
	count := b.inflight[node]
	b.metricsMu.Unlock()
	b.writeThroughInflight(node, count)
}

func (b *broker) exitInflight(node string, ok bool) {
	b.metricsMu.Lock()
	if b.inflight[node] > 0 {
		b.inflight[node]--
	}
	count := b.inflight[node]
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
	b.writeThroughInflight(node, count)
}

// writeThroughInflight mirrors THIS instance's current inflight count for a node into
// the shared hash (Stage 2), so a peer instance's capacity-aware pick sees this
// instance's load. Best-effort + non-fatal: a failure only means a peer reads slightly
// stale capacity (it falls back to its last merged value), never blocking a request. A
// no-op when multi-instance is off (b.shared==nil / instanceID==""), so the
// single-instance path is byte-for-byte unchanged.
func (b *broker) writeThroughInflight(node string, count int) {
	if !b.multiInstance || b.shared == nil || b.instanceID == "" {
		return
	}
	_ = b.shared.markInflight(b.instanceID, node, count, time.Now())
}

// syncInflight runs only under multi-instance: it periodically pulls the cross-instance
// inflight snapshot (the SUM of OTHER instances' counts per node, self excluded) and
// swaps it into b.peerInflight, so the hot pick path reads a purely in-memory peer-load
// view (no Valkey hop) exactly as the liveness merge does. On a backend error it keeps
// the last merged value (degrade to local-only capacity) and retries next tick.
// stop is a test seam (nil in production: the nil-channel case never fires, so the
// loop waits on the ticker exactly as before).
func (b *broker) syncInflight(stop <-chan struct{}) {
	t := time.NewTicker(syncTickInterval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			if b.shared == nil || !b.multiInstance {
				return
			}
			b.mergeSharedInflight()
		}
	}
}

// mergeSharedInflight refreshes b.peerInflight from the shared store (one round). Split
// out so a test can drive it deterministically. On a snapshot error it leaves the prior
// peerInflight intact (graceful degrade).
func (b *broker) mergeSharedInflight() {
	if b.shared == nil {
		return
	}
	snap, err := b.shared.inflightByNode(b.instanceID)
	if err != nil {
		return
	}
	b.metricsMu.Lock()
	b.peerInflight = snap
	b.metricsMu.Unlock()
}

// recordServed folds the smart-router v2 reward + capacity evidence from one
// QUALITY-VALIDATED served request (spec 3): it increments successCount (the
// reward-dimension evidence the UCB radius shrinks on) ONLY when the completion
// passed quality validation (non-empty, output tokens > 0, status<500) - a
// 200-with-empty-body never counts, closing the leech where junk would shrink the
// exploration radius. When the request was served UNDER LOAD (concurrentAtDispatch
// >= 2) it also folds the served tok/s into the concurrentTPS EWMA, the
// incentive-compatible capacity input (a node can't win a bigger concurrency
// allotment from an idle canary). concurrentAtDispatch is the inflight count at
// dispatch time, captured before exitInflight decremented it.
func (b *broker) recordServed(node string, qualityOK bool, servedTPS float64, concurrentAtDispatch int) {
	b.metricsMu.Lock()
	if b.successCount == nil {
		b.successCount = map[string]int{}
	}
	if b.concurrentTPS == nil {
		b.concurrentTPS = map[string]float64{}
	}
	if qualityOK {
		b.successCount[node]++
	}
	// Capacity is measured UNDER LOAD only: fold the served throughput into the
	// concurrent-TPS EWMA when at least one other request shared the node at dispatch.
	if concurrentAtDispatch >= 2 && servedTPS > 0 {
		if cur, ok := b.concurrentTPS[node]; ok {
			b.concurrentTPS[node] = 0.3*servedTPS + 0.7*cur
		} else {
			b.concurrentTPS[node] = servedTPS
		}
	}
	b.metricsMu.Unlock()
}

// inflightOf reads the current in-flight count for a node (snapshot under
// metricsMu). Used to capture the concurrency at dispatch for the under-load
// capacity measurement.
func (b *broker) inflightOf(node string) int {
	b.metricsMu.Lock()
	n := b.inflight[node]
	b.metricsMu.Unlock()
	return n
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
