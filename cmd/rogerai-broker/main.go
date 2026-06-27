// rogerai-broker - the central broker (the only public component).
//
// Connectivity: nodes DIAL OUT and long-poll GET /agent/poll for relayed jobs,
// then POST /agent/result back. No inbound connection to the node, no tunnel
// dependency (no Cloudflare/Tailscale). The broker holds a per-node job queue +
// result waiters; the OpenAI-compatible relay enqueues a job and awaits its
// result, verifies the node-signed lineage receipt, co-signs it, and settles the
// wallet.
//
// State is in-memory for now behind a small surface that is straightforward to
// back with Postgres (see DEPLOY.md) - kept modular so the DB can change.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
	"golang.org/x/sync/singleflight"
)

// version is the broker's reported version (also in ServiceInfo + logs).
const version = "0.1.0"

// openapiSpec is the served API contract (see openapi.yaml). Single source of
// truth for the broker's HTTP surface.
//
//go:embed openapi.yaml
var openapiSpec string

type broker struct {
	mu           sync.Mutex
	nodes        map[string]protocol.NodeRegistration
	tunnels      map[string]*nodeTunnel
	lastSeen     map[string]time.Time
	confidential map[string]bool
	private      map[string]bool      // node id -> hidden from /discover+/market, freq-code-only routing
	bandOf       map[string]string    // node id -> band id (the private channel it serves)
	attestedAt   map[string]time.Time // when each node last passed TEE attestation (for re-attest lapse)
	attest       *attestRegistry      // TEE attestation policy + backends + nonce store
	tps          map[string]float64   // EWMA output tokens/sec per node (measured)
	quotes       map[string]priceQuote
	metricsMu    sync.Mutex           // guards the per-node market metrics below
	lastPersist  map[string]time.Time // last time a node's last_seen was flushed to the store (throttle)
	// lastSharedSeen throttles the shared-state (Valkey) liveness write-through per
	// node, on its own clock so it works even when b.db is nil. Guarded by metricsMu.
	lastSharedSeen map[string]time.Time
	inflight       map[string]int        // in-flight (active) requests per node
	success        map[string]float64    // EWMA success rate per node (0..1)
	trust          map[string]trustState // L1 re-count + probe trust/quality per node
	// successCount is the count of QUALITY-VALIDATED served completions per node (a
	// non-empty body with output tokens, status<500), feeding the UCB exploration
	// radius (smart-router v2): it is the evidence for the reward dimension that only
	// real traffic exercises, so it is weighted higher than probes/recounts in N.
	// Guarded by metricsMu.
	successCount map[string]int
	// concurrentTPS is an EWMA of served tok/s recorded ONLY while inflight>=2 at the
	// time the request settled - capacity derived UNDER LOAD, not from the idle probe
	// canary. It is the incentive-compatible capacity input for the load factor: a
	// node cannot win a larger concurrency allotment by being fast on an idle probe
	// then queueing real traffic. Guarded by metricsMu. 0 = never observed under load
	// (capacity falls back to a conservative hw-class prior).
	concurrentTPS map[string]float64
	// totalReqs is a broker-wide relay counter for the UCB exploration radius
	// (ln(1+totalReqs)). Atomic so the hot relay path bumps it without metricsMu.
	totalReqs atomic.Int64
	// startTime is the process boot instant, set once in main, read by the admin HEALTH
	// tile for uptime. Read-only after startup (no lock needed).
	startTime time.Time
	// probeSched is the per-node ADAPTIVE performance-probe schedule (next-due +
	// exponential backoff level + last-measured). Guarded by metricsMu. It makes IDLE
	// performance probing lazy (floor -> doubling -> ceiling) while real traffic and
	// fresh demand pull it back to the floor; liveness/heartbeat is untouched. See
	// probe.go (probeState). Reset-on-restart is fine (cold-start re-probes at floor).
	probeSched  map[string]*probeState
	streamMu    sync.Mutex
	streams     map[string]*streamSink // jobID -> waiting client (streaming)
	authMu      sync.Mutex
	pubOfUser   map[string]string // TOFU: verified user id -> first pubkey that claimed it
	db          store.Store
	priv        ed25519.PrivateKey
	feeRate     float64
	seedFunds   float64
	lockWin     time.Duration
	bill        billing
	conn        connect
	mod         moderation
	mail        *mailer  // flag-gated (RESEND_API_KEY) transactional email; nil-safe no-op when disabled
	payoutLocks sync.Map // accountID -> *sync.Mutex: single-flight per account around payout
	rl          *rateLimiter
	grantRL     *rateLimiter // per-grant-key bucket (GRANT-KEYS-DESIGN section 3.5)
	// anonRL is a SEPARATE per-IP token bucket for the UNAUTHENTICATED public surfaces
	// (the free/anon relay, /discover). identityOf collapses all unauthenticated callers
	// to the single id "anon", so the per-identity b.rl bucket would be ONE shared bucket
	// for the entire public surface - a single abuser could starve every anon caller, and
	// no abuser is individually bounded. anonRL is keyed on the validated CF-Connecting-IP
	// (clientIP), giving each source IP its own bucket. This extends the same per-IP
	// discipline the concierge already uses (concierge.rl) to the other anon surfaces.
	anonRL    *rateLimiter
	concierge *concierge    // "Ping" homepage chatbot (public LLM surface)
	recount   recountConfig // L1 independent token re-count (tokenizer-sidecar)
	probe     probeConfig   // active canary + latency probe

	// shared is the optional cross-instance state layer (DO Valkey via
	// ROGERAI_REDIS_URL). nil = the default + the fallback: purely in-memory, ZERO
	// behavior change. When set, the SAFE state is mirrored to Valkey so multiple
	// broker instances share it: the anon/concierge rate-limit buckets and node
	// LIVENESS (lastSeen). Money/correctness-critical state (credit Hold/Finalize,
	// the job/result/stream rendezvous, inflight) stays in-memory - Stage 2. The
	// in-memory maps remain the authoritative hot-read path; the shared layer only
	// write-throughs liveness and feeds a background merge loop. See sharedstore.go.
	shared sharedStore

	// multiInstance turns on the PRE-SCALE Stage 2 cross-instance job/result/stream
	// RENDEZVOUS bus (sharedstore.go): a job picked on THIS instance can be served by a
	// provider long-polling a PEER instance, and the result/stream flows back over the
	// Valkey bus to this (originating) instance, which relays it to the waiting consumer.
	// It is gated behind ROGERAI_MULTI_INSTANCE=1 AND requires a wired shared backend
	// (ROGERAI_REDIS_URL); UNSET (the default + the DO single-instance deploy) leaves it
	// false and EVERY relay/poll/stream path uses the in-memory channels EXACTLY as
	// today (byte-for-byte, zero allocation). When true, the relay dispatch, the poll,
	// the non-stream result, and the SSE stream all additionally go over the bus so the
	// rendezvous works across instances. The pre-dispatch credit Hold and the Postgres
	// Finalize are unchanged (already durable/shared) - the bus only carries the
	// transient handoff, and a bus error fails the request cleanly (never double-charge).
	multiInstance bool

	// instanceID identifies THIS broker process in the shared inflight hash (each
	// instance write-throughs its own count under this field; a peer sums the others).
	// Random per process - reset-on-restart is fine (a crashed instance's stale field
	// ages out via inflightTTL). Empty when multi-instance is off.
	instanceID string

	// peerInflight is the merged SUM of OTHER instances' in-flight counts per node
	// (cross-instance capacity), refreshed on the same background loop as liveness via
	// mergeSharedInflight. pickFor adds it to this instance's exact local b.inflight so
	// the load factor is capacity-aware across instances. Guarded by metricsMu. Empty /
	// unused when multi-instance is off (zero behavior change).
	peerInflight map[string]int

	// cacheFlight collapses a CONCURRENT cache miss/expiry on a single hot key into ONE
	// compute (a dogpile/thundering-herd guard for serveCachedJSON). Without it, every
	// in-flight request on the one hot key (e.g. the single discover:/market: entry)
	// recomputes the full market under b.mu when the TTL window rolls; the singleflight
	// makes the herd share one recompute and one cache populate. It is allocated lazily
	// so a flag-OFF broker (shared == nil) is byte-for-byte unchanged. See serveCachedJSON.
	cacheFlight singleflight.Group

	// banned is the in-memory ejected-node set (node id -> true), guarded by metricsMu.
	// Re-hydrated from the store at startup and updated on a ban; pick/discover/market
	// consult it so a reported/banned node is never routed to (reuses the probe-eject
	// idea: a banned node is treated as not-serving). reportEjectAt is the per-node
	// report threshold that auto-bans (0 disables auto-eject).
	banned        map[string]bool
	reportEjectAt int
	// reportDecayDays is the trailing window the auto-eject counts DISTINCT corroborating
	// reporters over (so stale reports age out + a fixed node recovers); nodeBanDays is the
	// auto-lift window for a report-origin suspension (a report-eject is a time-boxed
	// suspension, not a permanent ban - permanent bans come only from admin/crypto-verified
	// abuse). Env ROGERAI_REPORT_DECAY_DAYS / ROGERAI_NODE_BAN_DAYS.
	reportDecayDays int
	nodeBanDays     int

	// bannedOwners is the in-memory DURABLE owner-ban set (owner pubkey -> true),
	// guarded by metricsMu. Re-hydrated from the store at startup and refreshed on a
	// ban. Unlike `banned` (node_id, a cheap callsign), this binds to the owner account
	// so a banned operator can't return under a fresh node id / callsign / grant key;
	// consulted at register, relay pick, and settle. strikeWarnAt/strikeBanAt are the
	// owner-strike escalation thresholds (warn, then ban) for the accumulating signals.
	bannedOwners map[string]bool
	strikeWarnAt int
	strikeBanAt  int
	// strikeDecayDays / strikeCorroborateKinds harden the ban decision against false
	// positives (audit 3.2): DECAY counts only strikes inside the trailing window toward a
	// ban (stale noise ages out, the evidence row is still kept), and CORROBORATION
	// requires strikes across >1 distinct signal class before an accumulating ban (one
	// noisy class can never auto-ban alone). The zero-doubt impossible-input arithmetic
	// proof bypasses both. Env ROGERAI_STRIKE_DECAY_DAYS / ROGERAI_STRIKE_CORROBORATE_KINDS.
	strikeDecayDays        int
	strikeCorroborateKinds int

	// recountHoldDays is the auto-expiry window for a recount hold (OPERATOR RECOURSE):
	// a node/account hold placed pending review auto-clears after this many days IF no
	// further discrepancy re-arms it, so a false positive never freezes an honest
	// operator's earnings forever. A fresh discrepancy refreshes the hold's timestamp,
	// so an actually-abusive operator stays held. Env ROGERAI_RECOUNT_HOLD_DAYS (default
	// 7). <=0 disables auto-expiry (holds clear only via the admin-reviewed unhold).
	recountHoldDays int

	// adminKey gates the admin-reviewed recount unhold (and any future admin op). It is
	// the broker's stable signing seed in hex (the BROKER_PRIVATE_KEY operator secret),
	// presented in the X-Roger-Admin header. Empty (ephemeral key / not configured) =>
	// the admin surface is CLOSED (every admin request 403s) so it can't be hit without
	// the real operator secret. See requireAdmin.
	adminKey string

	// adminGitHubID is the SINGLE super-admin (founder) GitHub numeric id. When set
	// (ADMIN_GITHUB_ID), a web session whose github_id matches it passes requireAdmin, so
	// the founder drives the admin portal by just logging in - no key paste in the
	// browser. 0 = no session-admin (the admin portal is then key-only / disabled in the
	// browser). An ordinary logged-in owner is NEVER an admin (the id must match exactly).
	adminGitHubID int64

	// freeRegMu guards freeRegByIP: the per-CF-IP sliding-window record of FREE (anon,
	// no-owner) node registrations used for the Sybil ceiling. A free node has no owner
	// account, so the per-owner cap (maxNodesPerOwner) does not apply to it; without a
	// separate ceiling an attacker could flood /discover + the pick candidate set with
	// throwaway free node ids from one host. freeRegByIP[ip] holds the timestamps of
	// that IP's recent NEW free registrations (older than freeRegWindow are pruned).
	freeRegMu     sync.Mutex
	freeRegByIP   map[string][]time.Time
	freeRegPerIP  int           // max NEW free node registrations per CF-IP per window (0 disables)
	freeRegWindow time.Duration // the sliding window for the per-IP free-reg cap

	// maxNodesPerOwner is the HARD server backstop: the max number of SIMULTANEOUSLY
	// on-air nodes a single owner account may have live (within nodeTTL) across all of
	// their machines. Enforced at register (the (limit+1)th owner-bound node is
	// rejected) so one account can't overwhelm the broker. An idempotent re-register of
	// an existing node never counts as a new one. 0 disables the cap. Env
	// ROGERAI_MAX_NODES_PER_OWNER (default 20).
	maxNodesPerOwner int
}

// priceQuote pins the price a user first saw for a (node, model) so an owner's
// later price change can't surprise them mid-engagement. See lockedPrice.
type priceQuote struct {
	in, out float64
	until   time.Time
}

func main() {
	addr := flag.String("addr", "127.0.0.1:7070", "listen address")
	fee := flag.Float64("fee", 0.30, "platform take rate")
	seed := flag.Float64("seed-credits", 100.0, "starting credits per new user (until Stripe)")
	lock := flag.Duration("price-lock", 24*time.Hour, "how long a quoted price is honored per user+node+model")
	flag.Parse()
	// DO App Platform sets $PORT; bind all interfaces there.
	if p := os.Getenv("PORT"); p != "" {
		*addr = "0.0.0.0:" + p
	}
	if v := os.Getenv("ROGERAI_FEE"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			*fee = f
		}
	}
	if v := os.Getenv("ROGERAI_SEED_CREDITS"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			*seed = f
		}
	}

	var db store.Store = store.NewMem()
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		pg, err := store.NewPostgres(dsn)
		if err != nil {
			log.Fatalf("postgres: %v", err)
		}
		db = pg
		log.Printf("store: postgres")
	} else {
		log.Printf("store: in-memory (set DATABASE_URL for postgres)")
	}

	// Seed cap: bound total free-credit liability. Only the first ROGERAI_SEED_LIMIT
	// distinct wallets get the starter seed; after that new wallets are created at 0.
	// Default 1000 (limit*seed = the max free credits ever minted). <=0 disables it.
	seedLimit := 1000
	if v := os.Getenv("ROGERAI_SEED_LIMIT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			seedLimit = n
		}
	}
	db.SetSeedLimit(seedLimit)
	log.Printf("seed: %g credits/new user, capped at %d seeded users (max %g free credits)", *seed, seedLimit, *seed*float64(seedLimit))

	priv, err := resolveBrokerKey(os.Getenv("BROKER_PRIVATE_KEY"), requireBrokerKey())
	if err != nil {
		// Fail-closed (ROGERAI_REQUIRE_BROKER_KEY set): the seed signs receipts,
		// derives pseudonyms, AND keys the session-cookie HMAC, so an ephemeral
		// fallback silently breaks all three across a restart. Refuse to boot.
		log.Fatalf("broker identity: %v (ROGERAI_REQUIRE_BROKER_KEY is set - refusing to boot with an ephemeral key)", err)
	}
	b := &broker{
		nodes: map[string]protocol.NodeRegistration{}, tunnels: map[string]*nodeTunnel{},
		lastSeen: map[string]time.Time{}, confidential: map[string]bool{},
		private: map[string]bool{}, bandOf: map[string]string{}, tps: map[string]float64{},
		attestedAt: map[string]time.Time{}, attest: loadAttestRegistry(),
		quotes: map[string]priceQuote{}, streams: map[string]*streamSink{}, db: db,
		pubOfUser: map[string]string{},
		inflight:  map[string]int{}, success: map[string]float64{}, trust: map[string]trustState{},
		successCount: map[string]int{}, concurrentTPS: map[string]float64{},
		probeSched:  map[string]*probeState{},
		lastPersist: map[string]time.Time{},
		priv:        priv, feeRate: *fee, seedFunds: *seed, lockWin: *lock,
		banned:                 map[string]bool{},
		reportEjectAt:          reportEjectThreshold(),
		reportDecayDays:        reportDecayDays(),
		nodeBanDays:            nodeBanDays(),
		maxNodesPerOwner:       maxNodesPerOwnerLimit(),
		freeRegByIP:            map[string][]time.Time{},
		freeRegPerIP:           freeRegPerIPLimit(),
		freeRegWindow:          freeRegWindowDur(),
		bannedOwners:           map[string]bool{},
		strikeWarnAt:           strikeWarnAt(),
		strikeBanAt:            strikeBanAt(),
		strikeDecayDays:        strikeDecayDays(),
		strikeCorroborateKinds: strikeCorroborateKinds(),
		recountHoldDays:        recountHoldDays(),
		// Admin surface is gated on the STABLE broker secret (BROKER_PRIVATE_KEY hex). An
		// ephemeral/unset key leaves adminKey empty => the key path is CLOSED.
		adminKey: validAdminKey(os.Getenv("BROKER_PRIVATE_KEY")),
		// The single super-admin (founder) GitHub id: a matching web session passes the
		// admin gate so the founder uses the portal by just logging in. Unset => 0 (the
		// browser admin path is off; only the broker key works). See requireAdmin.
		adminGitHubID: adminGitHubID(),
		startTime:     time.Now(),
	}
	b.rehydrateBans()
	b.rehydrateOwnerBans()
	// Re-hydrate the in-memory node registry from the store so a restart/redeploy
	// does NOT wipe registrations: a still-running provider reappears once its next
	// heartbeat re-confirms liveness, instead of being gone until a manual restart.
	b.rehydrateNodes()
	b.bill = loadBilling()
	b.conn = loadConnect()
	b.mod = loadModeration()
	b.mail = loadMailer()
	b.rl = loadRateLimiter()
	b.grantRL = loadRateLimiter() // independent bucket map keyed by grant id
	b.anonRL = loadAnonRateLimiter()
	b.recount = loadRecount()
	b.probe = loadProbe()
	b.concierge = loadConcierge()
	// PRE-SCALE Stage 1: wire the optional shared-state layer. UNSET ROGERAI_REDIS_URL
	// => b.shared stays nil and everything below is a no-op (in-memory, unchanged). A
	// connect failure already degraded to nil inside openSharedStore (logged warning,
	// no crash). When set, only the SAFE limiters get the shared bucket (anon +
	// concierge); the per-identity (b.rl) and per-grant (b.grantRL) limiters stay
	// local in this stage. Liveness sharing is handled by markSeen + syncLiveness.
	b.shared = openSharedStore()
	if b.shared != nil {
		// name each shared limiter so anon + concierge (both keyed on the client IP)
		// get DISTINCT Valkey buckets (rogerai:rl:anon:<ip> vs rogerai:rl:concierge:<ip>)
		// rather than colliding on one key with mismatched rpm/burst.
		b.anonRL.name, b.anonRL.shared = "anon", b.shared
		b.concierge.rl.name, b.concierge.rl.shared = "concierge", b.shared
		go b.syncLiveness()
		// PRE-SCALE Stage 2: the cross-instance rendezvous bus is OPT-IN on top of the
		// shared backend. ROGERAI_MULTI_INSTANCE=1 turns it on; it HARD-REQUIRES a wired
		// Valkey backend (the only place jobs/results/chunks can rendezvous across
		// instances), so it is only ever enabled when b.shared is non-nil. Unset = the
		// in-memory single-instance fast-path, byte-for-byte unchanged. The DO spec stays
		// instance_count:1, so this is off in production until we deliberately scale out.
		if multiInstanceEnabled() {
			b.multiInstance = true
			b.instanceID = newInstanceID()
			b.peerInflight = map[string]int{}
			go b.syncInflight() // merge peer inflight on the same cadence as liveness
			log.Printf("multi-instance: ON (ROGERAI_MULTI_INSTANCE, instance %s) - job/result/stream rendezvous over the Valkey bus across instances", b.instanceID)
		}
	} else if multiInstanceEnabled() {
		// Fail SAFE, not closed: the flag was set but there is no shared backend to
		// rendezvous over, so we CANNOT do cross-instance handoff. Stay single-instance
		// in-memory (the correct behavior for one instance) and warn loudly rather than
		// half-enabling a broken bus. This keeps a misconfig from silently dropping jobs.
		log.Printf("multi-instance: ROGERAI_MULTI_INSTANCE set but ROGERAI_REDIS_URL is not wired - staying single-instance in-memory (set ROGERAI_REDIS_URL to enable the cross-instance bus)")
	}
	// Bind the concierge's serving paths to this broker (grant dogfood, then a free
	// station, then Groq). Stored as fields so tests can stub each branch
	// independently. grantDogfoodFn stays nil (path disabled) unless CONCIERGE_GRANT_KEY
	// is set, so the handler skips it cleanly when there is no grant key.
	if b.concierge.grantKey != "" {
		b.concierge.grantDogfoodFn = b.dogfoodGrantRelay
	}
	b.concierge.dogfoodFn = b.dogfoodRelay
	b.concierge.groqFn = b.groqCall
	log.Printf("price-lock: quoted prices honored for %s per user+node+model", *lock)

	mux := http.NewServeMux()
	mux.HandleFunc("/nodes/register", b.register)
	mux.HandleFunc("/nodes/challenge", b.attestChallenge) // TEE attestation nonce (anti-replay binding)
	mux.HandleFunc("/nodes/heartbeat", b.heartbeat)
	mux.HandleFunc("/agent/poll", b.agentPoll)     // node dials out, long-polls for jobs
	mux.HandleFunc("/agent/result", b.agentResult) // node posts the served result
	mux.HandleFunc("/agent/stream", b.agentStream) // node streams SSE chunks (streaming)
	mux.HandleFunc("/discover", b.discover)
	mux.HandleFunc("/balance", b.balance)
	mux.HandleFunc("/me", b.me)                                   // consumer dashboard: balance, spend, recent
	mux.HandleFunc("/earnings", b.earnings)                       // owner dashboard: accrued earnings, recent
	mux.HandleFunc("/market", b.market)                           // per-model market metrics + signal
	mux.HandleFunc("/promo", b.promo)                             // public: free-credit seed promo state (seeds_remaining; auto-hide at 0)
	mux.HandleFunc("/auth/github", b.authGitHub)                  // bind a GitHub owner to the signing pubkey (CLI device flow)
	mux.HandleFunc("/auth/github/login", b.authGitHubLogin)       // web: 302 to GitHub authorize
	mux.HandleFunc("/auth/github/callback", b.authGitHubCallback) // web: code exchange + session cookie
	mux.HandleFunc("/auth/logout", b.authLogout)                  // web: clear the session cookie
	mux.HandleFunc("/account", b.account)                         // web: account hub (GET profile+balances, PATCH email)
	mux.HandleFunc("/account/limit", b.accountLimit)              // GET/PATCH the per-account monthly spend cap (budget limit)
	mux.HandleFunc("/account/export", b.accountExport)            // GDPR/CCPA data dump
	mux.HandleFunc("/account/delete", b.accountDelete)            // soft-delete + anonymize (retention-safe)
	mux.HandleFunc("/billing", b.billing)                         // money-in view: balance + top-up history
	mux.HandleFunc("/billing/checkout", b.checkout)               // Stripe top-up -> credits
	mux.HandleFunc("/billing/webhook", b.webhook)                 // Stripe payment + dispute webhook
	mux.HandleFunc("/usage", b.usage)                             // consumer spend by model|day
	mux.HandleFunc("/connect/onboard", b.connectOnboard)          // Stripe Connect Express onboarding link
	mux.HandleFunc("/connect/status", b.connectStatus)            // Connect capability status (KYC gate)
	mux.HandleFunc("/payouts/request", b.payoutsRequest)          // request a payout (KYC + min gated)
	mux.HandleFunc("/payouts/history", b.payoutsHistory)          // payout + clawback history
	mux.HandleFunc("/payouts/earnings", b.payoutsEarnings)        // earnings split + dated release ladder + rollups
	mux.HandleFunc("/payouts/", b.payoutsSubtree)                 // /payouts/{id}/lots: a payout's funding lineage
	mux.HandleFunc("/metrics/provider", b.metricsProvider)        // per-model SERVE metrics (free/paid + earnings)
	mux.HandleFunc("/metrics/usage", b.metricsUsage)              // per-model CONSUME metrics (free/paid + spend)
	mux.HandleFunc("/metrics/series", b.metricsSeries)            // per-day(+hourly) time-series + savings-vs-frontier (Dashboard/Metrics charts)
	mux.HandleFunc("/console", b.console)                         // recent lineage feed + live counters (Console page)
	mux.HandleFunc("/activity", b.console)                        // alias for /console
	mux.HandleFunc("/provider/models", b.providerModels)          // owner: per-model price + time-of-use schedule (Console pricing manager)
	mux.HandleFunc("/grants", b.grants)                           // owner grant keys: create + list
	mux.HandleFunc("/grants/", b.grants)                          // owner grant keys: show/edit/revoke by id
	mux.HandleFunc("/bands", b.bands)                             // owner private bands: list + revoke by id
	mux.HandleFunc("/bands/", b.bandsByID)                        // /bands/{id} revoke; /bands/resolve = public freq lookup
	mux.HandleFunc("/bands/resolve", b.bandResolve)               // PUBLIC: resolve a frequency code -> offers (constant-work)
	mux.HandleFunc("/v1/chat/completions", b.relay)
	mux.HandleFunc("/concierge", b.conciergeHandler)                                                  // "Ping" homepage chatbot (public)
	mux.HandleFunc("/report", b.report)                                                               // public abuse/quality report + node-ban flow
	mux.HandleFunc("/owner/strikes", b.ownerStrikes)                                                  // owner-authed: the caller's own strikes + evidence + node-ban status (operator recourse)
	mux.HandleFunc("/owner/appeal", b.ownerAppeal)                                                    // owner-authed: file a self-serve appeal (GET = the caller's appeals/status)
	mux.HandleFunc("/admin/unhold", b.adminUnhold)                                                    // admin-authed (broker-key): clear a recount hold + forgive strikes after review
	mux.HandleFunc("/admin/unban-node", b.adminUnbanNode)                                             // admin-authed: lift a node ban (the node recovery path)
	mux.HandleFunc("/admin/appeals", b.adminAppeals)                                                  // admin-authed: the open self-serve appeal review queue
	mux.HandleFunc("/admin/whoami", b.adminWhoami)                                                    // admin-authed: is-this-caller-an-admin probe (the portal gates on this)
	mux.HandleFunc("/admin/overview", b.adminOverview)                                                // admin-authed: HEALTH + MARKETPLACE + REVENUE rollup
	mux.HandleFunc("/admin/payouts", b.adminPayouts)                                                  // admin-authed: payout queue + history + open reversals + policy
	mux.HandleFunc("/admin/abuse", b.adminAbuse)                                                      // admin-authed: banned owners, strikes, CSAM queue, disputes (counts only)
	mux.HandleFunc("/admin/activity", b.adminActivity)                                                // admin-authed: recent cross-account ledger event stream (lineage)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }) // cheap liveness: the process is up
	mux.HandleFunc("/ready", b.ready)                                                                 // real readiness: DB + shared store reachable (503 if not)
	mux.HandleFunc("/openapi.yaml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write([]byte(openapiSpec))
	})
	mux.HandleFunc("/", b.root) // service descriptor - the broker is API-only (no website)

	if b.probe.enabled() {
		go b.proberLoop()
	}
	go b.reattestSweep()        // drop verified-confidential status that has lapsed its re-attest cadence
	go b.recountHoldSweep()     // auto-expire recount holds past the review window (operator recourse)
	go b.nodeBanSweep()         // auto-lift report-origin node suspensions past the review window (reversible bans)
	go b.reversalRetrySweep()   // re-attempt failed Stripe transfer-reversals (silent-money-leak guard)
	go b.pruneStaleNodesSweep() // remove long-dead node registrations (old hostname ids that never re-register)

	log.Printf("rogerai-broker %s: addr=%s fee=%.0f%% (node-dials-out long-poll tunnel)", version, *addr, *fee*100)

	// Tuned server (replaces the bare http.ListenAndServe). The timeouts that are
	// SAFE for every route live here; the per-route write/response bound is applied
	// selectively in streamSafeHandler so the long-lived routes are never capped.
	//
	//   ReadHeaderTimeout - slow-loris guard: bound how long a client may dribble
	//     request headers. Safe on every route (including streams/long-poll).
	//   ReadTimeout       - bound the time to read the whole request (headers+body).
	//     Safe everywhere: our request bodies are small + bounded (LimitReader); the
	//     LONG wait is on the RESPONSE side (long-poll/stream), which ReadTimeout does
	//     not touch.
	//   IdleTimeout       - reap idle keep-alive connections.
	//   MaxHeaderBytes    - cap header size (cheap DoS guard).
	//
	// DELIBERATELY NO global WriteTimeout. A blanket WriteTimeout fires from the
	// moment the handler is invoked and would KILL the long-lived surfaces:
	//   - /agent/poll holds a connection open up to 25s waiting for a job (tunnel.go),
	//   - /v1/chat/completions (stream:true) + /agent/stream pump SSE for up to 300s,
	//   - /concierge can wait on an upstream model.
	// Those MUST stay open. Instead the NON-streaming routes are individually bounded
	// with http.TimeoutHandler (streamSafeHandler) so a stuck non-stream handler can
	// never pin a connection, while the streaming/poll routes keep their long windows.
	srv := &http.Server{
		Addr:              *addr,
		Handler:           streamSafeHandler(mux),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 16, // 64 KiB
	}
	log.Fatal(srv.ListenAndServe())
}

// streamRoutes are the paths that MUST keep a long-lived response open and therefore
// must NOT be wrapped in a response/write deadline:
//
//   - /agent/poll   - the node long-poll, held up to 25s for a job (tunnel.go).
//   - /agent/stream - the node pipes SSE chunks here for the life of a stream.
//   - /agent/result - the node POSTs a completed result; can arrive late on a slow
//     CPU-MoE provider, so it must not be cut by a non-stream write deadline.
//   - /v1/chat/completions - may be stream:true (SSE, up to 300s) OR a non-stream
//     relay that itself waits on the provider; it does its OWN Cloudflare-aware
//     bounding internally (relay caps the non-stream wait below CF's ~100s proxy
//     limit, see tunnel.go), so a blanket TimeoutHandler here would double-bound it
//     and could truncate a legitimate SSE stream.
//   - /concierge - the public Ping chat may wait on an upstream model.
//
// Every OTHER route is non-streaming and gets bounded by http.TimeoutHandler.
var streamRoutes = map[string]bool{
	"/agent/poll":          true,
	"/agent/stream":        true,
	"/agent/result":        true,
	"/v1/chat/completions": true,
	"/concierge":           true,
}

// nonStreamTimeout is the response deadline applied to every NON-streaming route. It
// caps how long a single non-stream handler may take to produce its full response so
// a stuck handler can never pin a connection, WITHOUT touching the long-lived
// streaming/long-poll routes (those are excluded via streamRoutes). Comfortably
// below Cloudflare's ~100s proxy cap so a slow bounded handler returns a real 503
// before CF would emit an opaque 524.
const nonStreamTimeout = 30 * time.Second

// streamSafeHandler wraps the mux so NON-streaming routes get a response deadline
// (http.TimeoutHandler) while the streaming/long-poll routes in streamRoutes pass
// through unbounded. This is the per-handler discipline that replaces a global
// WriteTimeout: short routes are capped, long-lived routes stay open.
func streamSafeHandler(mux http.Handler) http.Handler {
	bounded := http.TimeoutHandler(mux, nonStreamTimeout, `{"error":{"message":"request timed out"}}`)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if streamRoutes[r.URL.Path] {
			mux.ServeHTTP(w, r) // long-lived: no response deadline
			return
		}
		bounded.ServeHTTP(w, r)
	})
}

// lockedPrice returns the price to BILL for this user+node+model. The first time
// a user hits an offer, the current price is quoted and pinned for lockWin (24h).
// Within that window an owner cannot charge MORE than the quoted price; if they
// LOWER it, the user gets the lower price (we bill min(quoted, current)). Fair to
// both: stable/predictable for users, and owners can always cut prices to compete.
func (b *broker) lockedPrice(user, node, model string, curIn, curOut float64) (in, out float64, until time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	key := user + "|" + node + "|" + model
	now := time.Now()

	// MULTI-INSTANCE (Stage 2): the 24h price-lock must be honored on ANY instance, so
	// the quote is shared in Valkey. Read the SHARED quote first (a quote locked on a
	// peer instance must win here); fall back to the local in-memory quote on a miss or
	// any bus error (graceful degrade to per-instance locking - never blocks the
	// request). The in-memory b.quotes stays the authoritative path when the flag is off
	// (b.shared==nil), so the single-instance behavior is byte-for-byte unchanged.
	if b.multiInstance && b.shared != nil {
		if sq, ok := b.sharedQuoteGet(key); ok && now.Before(sq.until) {
			b.quotes[key] = sq // mirror locally so a later bus outage still honors it
			return min(sq.in, curIn), min(sq.out, curOut), sq.until
		}
	}

	q, ok := b.quotes[key]
	if !ok || now.After(q.until) {
		q = priceQuote{in: curIn, out: curOut, until: now.Add(b.lockWin)}
		b.quotes[key] = q
		// Write the new lock through to the shared store so peers honor it. Best-effort:
		// a failure just means a peer mints its own (equal) quote until the next write.
		if b.multiInstance && b.shared != nil {
			b.sharedQuoteSet(key, q)
		}
	}
	return min(q.in, curIn), min(q.out, curOut), q.until
}

// sharedQuoteKey namespaces a shared price-lock under the cache keyspace (distinct from
// the market/metrics cache via the "quote:" infix). The quote is small + JSON-encoded.
func sharedQuoteKey(key string) string { return "quote:" + key }

// sharedQuoteGet reads a cross-instance price-lock. Any miss/bus error returns ok=false
// so the caller falls back to the local quote (never fails the request).
func (b *broker) sharedQuoteGet(key string) (priceQuote, bool) {
	val, found, err := b.shared.cacheGet(sharedQuoteKey(key))
	if err != nil || !found {
		return priceQuote{}, false
	}
	var w struct {
		In, Out float64
		Until   int64
	}
	if json.Unmarshal(val, &w) != nil {
		return priceQuote{}, false
	}
	return priceQuote{in: w.In, out: w.Out, until: time.Unix(w.Until, 0)}, true
}

// sharedQuoteSet write-throughs a price-lock with a TTL == the remaining lock window, so
// the shared entry expires exactly when the lock would. Best-effort (non-fatal).
func (b *broker) sharedQuoteSet(key string, q priceQuote) {
	ttl := time.Until(q.until)
	if ttl <= 0 {
		return
	}
	w := struct {
		In, Out float64
		Until   int64
	}{q.in, q.out, q.until.Unix()}
	if body, err := json.Marshal(w); err == nil {
		_ = b.shared.cacheSet(sharedQuoteKey(key), body, ttl)
	}
}

// requireBrokerKey mirrors requireLive (see billing.go): when set on the live broker
// it makes the signing identity FAIL CLOSED - the broker refuses to boot with an
// ephemeral key rather than silently breaking receipts/pseudonyms/session cookies on
// the next restart. Off by default so dev/local runs still come up with an ephemeral
// key. Accepts 1/true/yes/on.
func requireBrokerKey() bool {
	switch strings.ToLower(os.Getenv("ROGERAI_REQUIRE_BROKER_KEY")) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// multiInstanceEnabled reports whether ROGERAI_MULTI_INSTANCE requests the Stage 2
// cross-instance rendezvous bus. It is the SECOND gate (after a wired shared backend):
// main only sets b.multiInstance when this is true AND b.shared != nil. Off by default
// (the in-memory single-instance fast-path). Accepts 1/true/yes/on.
func multiInstanceEnabled() bool {
	switch strings.ToLower(os.Getenv("ROGERAI_MULTI_INSTANCE")) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// newInstanceID returns a random per-process id used as this instance's field in the
// shared inflight hash. Derived from the broker key seed is unnecessary (it is not a
// secret), so a fresh random hex is enough; reset-on-restart is fine (a crashed
// instance's stale inflight field ages out via inflightTTL).
func newInstanceID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b[:])
}

// resolveBrokerKey returns the broker's stable signing identity from the hex
// BROKER_PRIVATE_KEY seed. The seed signs lineage receipts, derives the per-(user,node)
// pseudonyms, AND keys the web session-cookie HMAC, so it MUST stay stable across
// restarts/redeploys or all three silently break. Posture:
//
//   - valid seed set                -> load it (stable identity).
//   - unset/invalid, requireKey=true -> return an error: the caller REFUSES TO BOOT
//     (fail-closed), instead of silently downgrading to an ephemeral key.
//   - unset/invalid, requireKey=false -> generate an ephemeral key (dev), logged loud.
//
// Returns (key, nil) on success or a fresh ephemeral key, and (nil, err) only in the
// fail-closed case so main can log + exit non-zero.
func resolveBrokerKey(h string, requireKey bool) (ed25519.PrivateKey, error) {
	if h != "" {
		if seed, err := hex.DecodeString(h); err == nil && len(seed) == ed25519.SeedSize {
			log.Printf("broker identity: loaded from BROKER_PRIVATE_KEY")
			return ed25519.NewKeyFromSeed(seed), nil
		}
		if requireKey {
			return nil, fmt.Errorf("BROKER_PRIVATE_KEY invalid (want %d-byte hex seed)", ed25519.SeedSize)
		}
		log.Printf("BROKER_PRIVATE_KEY invalid (want %d-byte hex seed) - using ephemeral key", ed25519.SeedSize)
	} else {
		if requireKey {
			return nil, fmt.Errorf("BROKER_PRIVATE_KEY unset")
		}
		log.Printf("BROKER_PRIVATE_KEY unset - using ephemeral key (receipts won't verify across restarts)")
	}
	_, priv, _ := ed25519.GenerateKey(nil)
	return priv, nil
}

// pseudonym derives an opaque, per-(user,node) id from a broker-held secret.
// Stable for repeat-customer stats; not reversible to the real user and not the
// same across nodes (so providers can't collude to re-identify someone).
func (b *broker) pseudonym(user, node string) string {
	h := sha256.Sum256(append(b.priv.Seed(), []byte(user+"|"+node)...))
	return "u_" + hex.EncodeToString(h[:8])
}

// identityOf resolves the caller's wallet identity for a request, given the exact
// request body (nil for a bodyless GET). It is the P0 replacement for the old
// trust-the-header userOf: when the signing headers are present it VERIFIES the
// signature against the pubkey, checks timestamp freshness, derives a stable id
// from the pubkey, and TOFU-binds id<->pubkey. Returns:
//
//	id     - the wallet identity to use
//	authed - true only when the request was cryptographically verified
//	ok     - false when a signature was PRESENT but INVALID, OR an unsigned legacy
//	         header impersonates the reserved pubkey-derived id space (caller 401s);
//	         a plain unsigned request returns ok=true, authed=false (legacy mode)
//
// Two layers keep an unsigned request from EVER spending a signed user's wallet:
//  1. the pubkey-derived id space ("u_"+16hex) is reserved - an unsigned legacy
//     header claiming such an id is rejected here (looksLikeDerivedID), so a public
//     pubkey can't be turned into a spendable impersonation; and
//  2. spend handlers additionally require authed==true (see relay), so even a
//     non-derived legacy id can never spend.
func (b *broker) identityOf(r *http.Request, body []byte) (id string, authed, ok bool) {
	pub := r.Header.Get(protocol.HeaderPubkey)
	sig := r.Header.Get(protocol.HeaderSig)
	tsStr := r.Header.Get(protocol.HeaderTS)
	if pub != "" || sig != "" || tsStr != "" {
		// A signature was offered - it MUST verify, or the request is rejected.
		ts, err := strconv.ParseInt(tsStr, 10, 64)
		if err != nil {
			return "", false, false
		}
		uid, vok := protocol.VerifyRequest(pub, sig, ts, r.Method, r.URL.Path, body)
		if !vok {
			return "", false, false
		}
		b.bindUserPub(uid, pub)
		return uid, true, true
	}
	// Unsigned: legacy, unauthenticated. Used for reads + backward compatibility;
	// such a caller can never be treated as a verified (signed) wallet. The
	// pubkey-derived id space ("u_"+16hex) is RESERVED for verified callers: the
	// pubkey travels in cleartext (it is public), so without this guard an attacker
	// who learns a victim's pubkey could compute the victim's id and present it in a
	// plain X-Roger-User header to spend an unsigned-but-impersonating request. Reject
	// any legacy header that looks like a derived id so the reservation holds.
	if u := r.Header.Get(protocol.HeaderUser); u != "" {
		if reservedID(u) {
			return "", false, false
		}
		return u, false, true
	}
	if a := r.Header.Get("Authorization"); len(a) > 7 && a[:7] == "Bearer " {
		if reservedID(a[7:]) {
			return "", false, false
		}
		return a[7:], false, true
	}
	return "anon", false, true
}

// walletOf maps a VERIFIED (signed) request's pubkey-derived id to the wallet that
// actually holds the money. The unification rule (founder-approved): a keypair that
// has logged in (its pubkey is bound to a non-anonymized GitHub owner) resolves to
// the SAME "u_gh_<githubID>" wallet the web session uses - so the CLI and the web
// read/spend ONE wallet. An unbound keypair (not logged in) keeps its pubkey-derived
// id, which is an ANONYMOUS, no-seed wallet (see loggedInWallet for the spend gate).
//
// The signed `id` is still used directly for self-use ownership checks (ownsNode
// compares the pubkey-derived id to the node's owner pubkey); only the MONEY key is
// remapped here. Requires the pubkey header (a verified request always carries it).
func (b *broker) walletOf(r *http.Request, id string) string {
	pub := r.Header.Get(protocol.HeaderPubkey)
	if pub == "" {
		return id
	}
	// W1: cache the (immutable per session) pubkey->github-wallet mapping behind the
	// flag, so the per-request OwnerByPubkey point read collapses to one Redis GET on a
	// hit. Postgres stays authoritative on a miss/flag-off (resolve below); the bind
	// write (auth.go) invalidates the entry so a re-login is reflected at once. A non-
	// logged-in/anon pubkey is cached as a negative result so it doesn't re-hit Postgres.
	if w, ok := b.cachedOwnerWallet(pub, func() (string, bool) {
		if o, ok, err := b.db.OwnerByPubkey(pub); err == nil && ok && !o.Anonymized && o.GitHubID != 0 {
			return "u_gh_" + strconv.FormatInt(o.GitHubID, 10), true
		}
		return "", false
	}); ok {
		return w
	}
	return id
}

// loggedInWallet resolves the GitHub-scoped wallet for a request that proves it owns
// a logged-in keypair: the request must be cryptographically SIGNED (authed) AND its
// pubkey bound to a non-anonymized owner. Returns ok=false for an anonymous/unbound
// keypair (no wallet, no balance - free models + grant keys only). This is the gate
// the spend path uses to reject a paid request from a not-logged-in caller, and the
// dashboard uses to hide the balance when anonymous. It never seeds.
func (b *broker) loggedInWallet(r *http.Request, body []byte) (wallet string, ok bool) {
	id, authed, iok := b.identityOf(r, body)
	if !iok || !authed {
		return "", false
	}
	w := b.walletOf(r, id)
	if strings.HasPrefix(w, "u_gh_") {
		return w, true
	}
	return "", false
}

// reservedID reports whether an id belongs to a namespace that an UNSIGNED legacy
// header must never be allowed to claim: the pubkey-derived wallet ("u_"+16hex,
// owned by a signed caller), the github-scoped web wallet ("u_gh_<id>", owned by
// a session-cookie holder), OR a grant wallet ("g_<id>", owned server-side by a
// grant secret). All are guessable from public info (a pubkey, a GitHub numeric
// id, or a grant id), so the unsigned path must reject them or it leaks another
// caller's balance/spend/recent, or lets someone claim a grant without its secret.
// See identityOf.
func reservedID(s string) bool {
	return looksLikeDerivedID(s) || strings.HasPrefix(s, "u_gh_") || strings.HasPrefix(s, "g_")
}

// looksLikeDerivedID reports whether s is shaped like a pubkey-derived wallet id
// ("u_" + 16 lowercase hex). That id space is reserved for VERIFIED (signed)
// callers; an unsigned legacy header claiming such an id is an impersonation
// attempt and must be rejected (see identityOf).
func looksLikeDerivedID(s string) bool {
	if len(s) != 18 || s[0] != 'u' || s[1] != '_' {
		return false
	}
	for _, c := range s[2:] {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// bindUserPub records the first pubkey seen for a verified user id (TOFU). Because
// the id is derived from the pubkey this is effectively a no-op for honest callers,
// but it makes the id<->key relationship explicit and auditable.
func (b *broker) bindUserPub(id, pub string) {
	b.authMu.Lock()
	if b.pubOfUser == nil {
		b.pubOfUser = map[string]string{}
	}
	if _, ok := b.pubOfUser[id]; !ok {
		b.pubOfUser[id] = pub
	}
	b.authMu.Unlock()
}

func round6(f float64) float64 {
	return float64(int64(f*1e6+0.5)) / 1e6
}

// root (GET /) is a minimal service descriptor. The broker is an API, not a
// website; clients read /openapi.yaml for the contract.
func (b *broker) root(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		jsonErr(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"service": "rogerai-broker", "version": version, "spec": "/openapi.yaml"})
}
