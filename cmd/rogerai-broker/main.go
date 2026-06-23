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
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
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

type broker struct {
	mu           sync.Mutex
	nodes        map[string]protocol.NodeRegistration
	tunnels      map[string]*nodeTunnel
	lastSeen     map[string]time.Time
	confidential map[string]bool
	tps          map[string]float64 // EWMA output tokens/sec per node (measured)
	quotes       map[string]priceQuote
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
	b.mu.Lock()
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
		json.NewEncoder(w).Encode(job)
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
	b.mu.Lock()
	node, offer, ok := b.pick(req.Model, confidentialOnly, minTPS, maxPrice)
	t := b.tunnels[node.NodeID]
	b.mu.Unlock()
	bal, _ := b.db.BalanceOf(user, b.seedFunds)

	if !ok || t == nil {
		msg := "no node offers " + req.Model
		if confidentialOnly {
			msg += " on a confidential node"
		}
		jsonErr(w, http.StatusServiceUnavailable, msg)
		return
	}
	if bal <= 0 {
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
		b.relayStream(w, t, node, offer, user, req.Model, job, resCh)
		return
	}

	start := time.Now()
	select {
	case t.jobs <- job:
	case <-time.After(3 * time.Second):
		jsonErr(w, http.StatusServiceUnavailable, "node busy (no poller free)")
		return
	}

	select {
	case res := <-resCh:
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
			newBal, _ := b.db.Settle(user, node.NodeID, cost, cost*(1-b.feeRate), rec)
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
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(res.Status)
		_, _ = w.Write(res.Body)
	case <-time.After(120 * time.Second):
		jsonErr(w, http.StatusGatewayTimeout, "node timed out")
	}
}

// relayStream handles the streaming path of POST /v1/chat/completions: it sends SSE
// headers, registers the client as a sink, and enqueues the job. The node pipes
// chunks via /agent/stream straight to this client; when it finishes it posts a
// receipt (resCh) which settles the wallet. No metering headers (already streaming).
func (b *broker) relayStream(w http.ResponseWriter, t *nodeTunnel, node protocol.NodeRegistration, offer protocol.ModelOffer, user, model string, job protocol.Job, resCh chan protocol.JobResult) {
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
	select {
	case t.jobs <- job:
	case <-time.After(3 * time.Second):
		return // headers already sent; the client just gets an empty stream
	}
	select {
	case res := <-resCh:
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
			b.db.Settle(user, node.NodeID, cost, cost*(1-b.feeRate), rec)
			if rec.CompletionTokens > 0 {
				if el := time.Since(start).Seconds(); el > 0 {
					b.updateTPS(node.NodeID, float64(rec.CompletionTokens)/el)
				}
			}
			log.Printf("stream user=%s node=%s out=%d cost=%.6f", user, node.NodeID, rec.CompletionTokens, cost)
		}
	case <-time.After(300 * time.Second):
	}
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

type offerView struct {
	NodeID       string  `json:"node_id"`
	Region       string  `json:"region"`
	HW           string  `json:"hw"`
	Model        string  `json:"model"`
	In           float64 `json:"price_in"`  // active (time-of-use) price right now
	Out          float64 `json:"price_out"` // active price right now
	Ctx          int     `json:"ctx"`
	Online       bool    `json:"online"`
	Confidential bool    `json:"confidential"`
	FreeNow      bool    `json:"free_now"`
	Scheduled    bool    `json:"scheduled"`
	TPS          float64 `json:"tps"` // measured output tokens/sec (0 = not yet measured)
}

// discover handles GET /discover: all model offers with live status, measured
// throughput, and active (time-of-use) price, cheapest-now first.
func (b *broker) discover(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodGet) {
		return
	}
	b.mu.Lock()
	now := time.Now()
	var out []offerView
	for _, n := range b.nodes {
		online := time.Since(b.lastSeen[n.NodeID]) < 35*time.Second
		for _, o := range n.Offers {
			pin, pout, free, _ := o.ActivePrice(now)
			out = append(out, offerView{
				NodeID: n.NodeID, Region: n.Region, HW: n.HW, Model: o.Model,
				In: pin, Out: pout, Ctx: o.Ctx, Online: online,
				Confidential: b.confidential[n.NodeID], FreeNow: free, Scheduled: len(o.Schedule) > 0,
				TPS: b.tps[n.NodeID],
			})
		}
	}
	b.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].In < out[j].In })
	writeJSON(w, http.StatusOK, map[string]any{"offers": out})
}

// balance handles GET /balance: the caller's wallet credits (seeds new users).
func (b *broker) balance(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodGet) {
		return
	}
	user := userOf(r)
	bal, _ := b.db.BalanceOf(user, b.seedFunds)
	writeJSON(w, http.StatusOK, map[string]any{"user": user, "balance": bal})
}

// pick: cheapest-RIGHT-NOW online node offering the model, ranked by the active
// (time-of-use) price; optionally restricted to confidential nodes. Caller holds lock.
func (b *broker) pick(model string, confidentialOnly bool, minTPS, maxPriceIn float64) (protocol.NodeRegistration, protocol.ModelOffer, bool) {
	var best protocol.NodeRegistration
	var bestOffer protocol.ModelOffer
	bestPrice := 0.0
	found := false
	now := time.Now()
	for _, n := range b.nodes {
		if time.Since(b.lastSeen[n.NodeID]) >= 35*time.Second {
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

func parseFloat(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
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

func authNode(r *http.Request, token string) bool {
	return token != "" && r.Header.Get("Authorization") == "Bearer "+token
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

func ftoa(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
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

// writeJSON / jsonErr standardize every JSON response (content-type + error shape).
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func jsonErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": map[string]string{"message": msg}})
}

// allow guards a handler's HTTP method, writing 405 if it doesn't match.
func allow(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method != method {
		w.Header().Set("Allow", method)
		jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return false
	}
	return true
}
