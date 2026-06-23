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
	"log"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/bownux/rogerai/internal/protocol"
	"github.com/bownux/rogerai/internal/store"
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
	tps          map[string]float64 // EWMA output tokens/sec per node (measured)
	quotes       map[string]priceQuote
	metricsMu    sync.Mutex         // guards the per-node market metrics below
	inflight     map[string]int     // in-flight (active) requests per node
	success      map[string]float64 // EWMA success rate per node (0..1)
	streamMu     sync.Mutex
	streams      map[string]*streamSink // jobID -> waiting client (streaming)
	db           store.Store
	priv         ed25519.PrivateKey
	feeRate      float64
	seedFunds    float64
	lockWin      time.Duration
	bill         billing
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

	priv := loadBrokerKey()
	b := &broker{
		nodes: map[string]protocol.NodeRegistration{}, tunnels: map[string]*nodeTunnel{},
		lastSeen: map[string]time.Time{}, confidential: map[string]bool{}, tps: map[string]float64{},
		quotes: map[string]priceQuote{}, streams: map[string]*streamSink{}, db: db,
		inflight: map[string]int{}, success: map[string]float64{},
		priv: priv, feeRate: *fee, seedFunds: *seed, lockWin: *lock,
	}
	b.bill = loadBilling()
	log.Printf("price-lock: quoted prices honored for %s per user+node+model", *lock)

	mux := http.NewServeMux()
	mux.HandleFunc("/nodes/register", b.register)
	mux.HandleFunc("/nodes/heartbeat", b.heartbeat)
	mux.HandleFunc("/agent/poll", b.agentPoll)     // node dials out, long-polls for jobs
	mux.HandleFunc("/agent/result", b.agentResult) // node posts the served result
	mux.HandleFunc("/agent/stream", b.agentStream) // node streams SSE chunks (streaming)
	mux.HandleFunc("/discover", b.discover)
	mux.HandleFunc("/balance", b.balance)
	mux.HandleFunc("/me", b.me)                     // consumer dashboard: balance, spend, recent
	mux.HandleFunc("/earnings", b.earnings)         // owner dashboard: accrued earnings, recent
	mux.HandleFunc("/market", b.market)             // per-model market metrics + signal
	mux.HandleFunc("/billing/checkout", b.checkout) // Stripe top-up -> credits
	mux.HandleFunc("/billing/webhook", b.webhook)   // Stripe payment webhook
	mux.HandleFunc("/v1/chat/completions", b.relay)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("/openapi.yaml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write([]byte(openapiSpec))
	})
	mux.HandleFunc("/", b.root) // service descriptor - the broker is API-only (no website)

	log.Printf("rogerai-broker %s: addr=%s fee=%.0f%% (node-dials-out long-poll tunnel)", version, *addr, *fee*100)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

// verifyAttestation is a STUB. Real TEE remote-attestation verification (NVIDIA
// Confidential Computing / AMD SEV-SNP / Intel TDX quote validation) is the deep
// follow-up. For now a node is treated as confidential only if it presents a
// non-trivial attestation - enough to wire the badge + route filter end-to-end,
// NOT yet a cryptographic guarantee. See PRIVACY.md.
func verifyAttestation(att string) bool {
	// Reject the obvious dev placeholder so the confidential badge isn't trivially
	// claimable; require a non-trivial blob. This is NOT yet real verification -
	// NVIDIA-CC/SEV-SNP/TDX quote validation is the follow-up before the badge is a
	// cryptographic guarantee (see PRIVACY.md).
	if att == "dev-placeholder-attestation" {
		return false
	}
	return len(att) >= 64
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

// loadBrokerKey returns the broker's stable signing identity. Set BROKER_PRIVATE_KEY
// (hex ed25519 seed) as a secret so lineage receipts stay verifiable and pseudonyms
// stay stable across restarts/redeploys; otherwise a fresh ephemeral key is used.
func loadBrokerKey() ed25519.PrivateKey {
	if h := os.Getenv("BROKER_PRIVATE_KEY"); h != "" {
		if seed, err := hex.DecodeString(h); err == nil && len(seed) == ed25519.SeedSize {
			log.Printf("broker identity: loaded from BROKER_PRIVATE_KEY")
			return ed25519.NewKeyFromSeed(seed)
		}
		log.Printf("BROKER_PRIVATE_KEY invalid (want %d-byte hex seed) - using ephemeral key", ed25519.SeedSize)
	} else {
		log.Printf("BROKER_PRIVATE_KEY unset - using ephemeral key (receipts won't verify across restarts)")
	}
	_, priv, _ := ed25519.GenerateKey(nil)
	return priv
}

// pseudonym derives an opaque, per-(user,node) id from a broker-held secret.
// Stable for repeat-customer stats; not reversible to the real user and not the
// same across nodes (so providers can't collude to re-identify someone).
func (b *broker) pseudonym(user, node string) string {
	h := sha256.Sum256(append(b.priv.Seed(), []byte(user+"|"+node)...))
	return "u_" + hex.EncodeToString(h[:8])
}

func userOf(r *http.Request) string {
	if u := r.Header.Get("X-Roger-User"); u != "" {
		return u
	}
	if a := r.Header.Get("Authorization"); len(a) > 7 && a[:7] == "Bearer " {
		return a[7:]
	}
	return "anon"
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
