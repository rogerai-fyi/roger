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
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
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
	metricsMu    sync.Mutex            // guards the per-node market metrics below
	lastPersist  map[string]time.Time  // last time a node's last_seen was flushed to the store (throttle)
	inflight     map[string]int        // in-flight (active) requests per node
	success      map[string]float64    // EWMA success rate per node (0..1)
	trust        map[string]trustState // L1 re-count + probe trust/quality per node
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
	payoutLocks sync.Map // accountID -> *sync.Mutex: single-flight per account around payout
	rl          *rateLimiter
	grantRL     *rateLimiter  // per-grant-key bucket (GRANT-KEYS-DESIGN section 3.5)
	concierge   *concierge    // "Ping" homepage chatbot (public LLM surface)
	recount     recountConfig // L1 independent token re-count (tokenizer-sidecar)
	probe       probeConfig   // active canary + latency probe

	// banned is the in-memory ejected-node set (node id -> true), guarded by metricsMu.
	// Re-hydrated from the store at startup and updated on a ban; pick/discover/market
	// consult it so a reported/banned node is never routed to (reuses the probe-eject
	// idea: a banned node is treated as not-serving). reportEjectAt is the per-node
	// report threshold that auto-bans (0 disables auto-eject).
	banned        map[string]bool
	reportEjectAt int

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
		banned:           map[string]bool{},
		reportEjectAt:    reportEjectThreshold(),
		maxNodesPerOwner: maxNodesPerOwnerLimit(),
	}
	b.rehydrateBans()
	// Re-hydrate the in-memory node registry from the store so a restart/redeploy
	// does NOT wipe registrations: a still-running provider reappears once its next
	// heartbeat re-confirms liveness, instead of being gone until a manual restart.
	b.rehydrateNodes()
	b.bill = loadBilling()
	b.conn = loadConnect()
	b.mod = loadModeration()
	b.rl = loadRateLimiter()
	b.grantRL = loadRateLimiter() // independent bucket map keyed by grant id
	b.recount = loadRecount()
	b.probe = loadProbe()
	b.concierge = loadConcierge()
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
	mux.HandleFunc("/metrics/provider", b.metricsProvider)        // per-model SERVE metrics (free/paid + earnings)
	mux.HandleFunc("/metrics/usage", b.metricsUsage)              // per-model CONSUME metrics (free/paid + spend)
	mux.HandleFunc("/metrics/series", b.metricsSeries)            // per-day(+hourly) time-series + savings-vs-frontier (Dashboard/Metrics charts)
	mux.HandleFunc("/console", b.console)                         // recent lineage feed + live counters (Console page)
	mux.HandleFunc("/activity", b.console)                        // alias for /console
	mux.HandleFunc("/grants", b.grants)                           // owner grant keys: create + list
	mux.HandleFunc("/grants/", b.grants)                          // owner grant keys: show/edit/revoke by id
	mux.HandleFunc("/bands", b.bands)                             // owner private bands: list + revoke by id
	mux.HandleFunc("/bands/", b.bandsByID)                        // /bands/{id} revoke; /bands/resolve = public freq lookup
	mux.HandleFunc("/bands/resolve", b.bandResolve)               // PUBLIC: resolve a frequency code -> offers (constant-work)
	mux.HandleFunc("/v1/chat/completions", b.relay)
	mux.HandleFunc("/concierge", b.conciergeHandler) // "Ping" homepage chatbot (public)
	mux.HandleFunc("/report", b.report)              // public abuse/quality report + node-ban flow
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("/openapi.yaml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write([]byte(openapiSpec))
	})
	mux.HandleFunc("/", b.root) // service descriptor - the broker is API-only (no website)

	if b.probe.enabled() {
		go b.proberLoop()
	}
	go b.reattestSweep() // drop verified-confidential status that has lapsed its re-attest cadence

	log.Printf("rogerai-broker %s: addr=%s fee=%.0f%% (node-dials-out long-poll tunnel)", version, *addr, *fee*100)
	log.Fatal(http.ListenAndServe(*addr, mux))
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
	q, ok := b.quotes[key]
	if !ok || now.After(q.until) {
		q = priceQuote{in: curIn, out: curOut, until: now.Add(b.lockWin)}
		b.quotes[key] = q
	}
	return min(q.in, curIn), min(q.out, curOut), q.until
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
	if o, ok, err := b.db.OwnerByPubkey(pub); err == nil && ok && !o.Anonymized && o.GitHubID != 0 {
		return "u_gh_" + strconv.FormatInt(o.GitHubID, 10)
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
