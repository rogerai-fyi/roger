package main

// genrecord.go - GET /generation: one request's routing and billing history, scoped to the
// identity that made it (features/routing/generation_lookup.feature, contract §7).
//
// A record is assembled while the relay runs (an in-flight entry keyed by request id that the
// relay's attempt, settle and void points feed) and written ONCE when the relay returns, so an
// in-flight request is not yet readable. Records live in the shared store (readable from any
// instance); with no shared backend they stay in this instance's bounded map. A failed write is
// logged and never fails the relay; an unreachable shared store fails the lookup closed (503).

import (
	"encoding/json"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"rogerai.fm/roger/v6/internal/protocol"
)

// genRecordTTL is how long a record stays readable. The contract ties readability to the
// console lineage, which has no window of its own; a shared-store key needs one, so the
// record outlives any realistic support lookup and is then dropped.
const genRecordTTL = 30 * 24 * time.Hour

// genLocalCap bounds the per-instance fallback map (no shared backend).
const genLocalCap = 20000

type genServed struct {
	Node  string `json:"node"`
	Model string `json:"model"`
	Relay string `json:"relay,omitempty"`
}

type genModeration struct {
	Mode      string `json:"mode"`
	Verdict   string `json:"verdict"`
	LatencyMs int64  `json:"latency_ms"`
}

type genAttempt struct {
	N           int    `json:"n"`
	Node        string `json:"node"`
	Model       string `json:"model"`
	Status      int    `json:"status"`
	ErrorCode   string `json:"error_code,omitempty"`
	DurationMs  int64  `json:"duration_ms"`
	RetryAfterS int    `json:"retry_after_s,omitempty"`
	VoidReason  string `json:"void_reason,omitempty"`

	start time.Time
}

// genRecord is the record as the CONSUMER reads it. The owner view is derived from it.
type genRecord struct {
	ID             string        `json:"id"`
	CreatedAt      int64         `json:"created_at"`
	ModelRequested string        `json:"model_requested"`
	Models         []string      `json:"models"`
	Streamed       bool          `json:"streamed"`
	Cancelled      bool          `json:"cancelled"`
	Status         int           `json:"status"`
	ErrorCode      *string       `json:"error_code"`
	RetryAfterS    int           `json:"retry_after_s,omitempty"`
	Served         *genServed    `json:"served"`
	Cost           float64       `json:"cost"`
	TokensIn       int           `json:"tokens_in"`
	TokensOut      int           `json:"tokens_out"`
	TPS            float64       `json:"tps"`
	TTFTMs         float64       `json:"ttft_ms"`
	LatencyMs      float64       `json:"latency_ms"`
	Moderation     genModeration `json:"moderation"`
	KeyID          *string       `json:"key_id"`
	Attempts       []genAttempt  `json:"attempts"`
	Receipt        *string       `json:"receipt"`
}

// genStored is what the store holds: the record plus who may read it (never served).
type genStored struct {
	Rec        genRecord `json:"rec"`
	Wallet     string    `json:"wallet,omitempty"`      // the money key that made the request
	GrantID    string    `json:"grant_id,omitempty"`    // the grant that made it
	GrantOwner string    `json:"grant_owner,omitempty"` // the pubkey that minted that grant
}

// genLive is a record being assembled while its relay runs.
type genLive struct {
	mu         sync.Mutex
	st         genStored
	start      time.Time
	firstFrame time.Time
	written    bool
}

// genRegistry holds the in-flight records and the no-shared-store fallback.
type genRegistry struct {
	live sync.Map // request id -> *genLive

	mu    sync.Mutex
	local map[string][]byte
	order []string
}

func (b *broker) genLiveOf(requestID string) *genLive {
	if v, ok := b.gens.live.Load(requestID); ok {
		return v.(*genLive)
	}
	return nil
}

// genOpen starts a record for a relay that just minted its request id.
func (b *broker) genOpen(requestID string, now time.Time) *genLive {
	mode := b.mod.mode
	if mode == "" {
		mode = modeOff
	}
	g := &genLive{start: now, st: genStored{Rec: genRecord{ID: requestID, CreatedAt: now.Unix(), Attempts: []genAttempt{},
		Moderation: genModeration{Mode: mode, Verdict: "none"}}}}
	b.gens.live.Store(requestID, g)
	return g
}

// with2 runs f on the live record itself (nil-safe), under its lock.
func (g *genLive) with2(f func(g *genLive)) {
	if g == nil {
		return
	}
	g.mu.Lock()
	if g.firstFrame.IsZero() {
		f(g)
	}
	g.mu.Unlock()
}

func (g *genLive) with(f func(st *genStored)) {
	if g == nil {
		return
	}
	g.mu.Lock()
	f(&g.st)
	g.mu.Unlock()
}

// splitAttemptID maps an attempt id ("<id>" or "<id>-n") to its request id and n.
func splitAttemptID(attempt string) (string, int) {
	if i := strings.LastIndexByte(attempt, '-'); i > 0 {
		if n, err := strconv.Atoi(attempt[i+1:]); err == nil {
			return attempt[:i], n
		}
	}
	return attempt, 1
}

// attemptOf returns attempt n of the record, appending it if absent.
func (st *genStored) attemptOf(n int) *genAttempt {
	for i := range st.Rec.Attempts {
		if st.Rec.Attempts[i].N == n {
			return &st.Rec.Attempts[i]
		}
	}
	st.Rec.Attempts = append(st.Rec.Attempts, genAttempt{N: n})
	return &st.Rec.Attempts[len(st.Rec.Attempts)-1]
}

// genAttemptStart records that attempt n was dispatched to node for model.
func (b *broker) genAttemptStart(requestID string, n int, node, model string) {
	b.genLiveOf(requestID).with(func(st *genStored) {
		a := st.attemptOf(n)
		a.Node, a.Model, a.start = node, model, time.Now()
	})
}

// genAttemptEnd records how attempt (by its attempt id) ended without serving.
func (b *broker) genAttemptEnd(attempt, node string, status int, void string, retryAfter int) {
	id, n := splitAttemptID(attempt)
	b.genAttemptEndN(id, n, node, status, void, retryAfter)
}

func (b *broker) genAttemptEndN(requestID string, n int, node string, status int, void string, retryAfter int) {
	b.genLiveOf(requestID).with(func(st *genStored) {
		a := st.attemptOf(n)
		if node != "" {
			a.Node = node
		}
		a.Status, a.VoidReason, a.RetryAfterS = status, void, retryAfter
		if !a.start.IsZero() {
			a.DurationMs = time.Since(a.start).Milliseconds()
		}
	})
}

// genServe records the attempt that served and what it billed.
func (b *broker) genServe(requestID string, n int, served genServed, cost float64, tin, tout int, tps float64, receipt string) {
	b.genLiveOf(requestID).with(func(st *genStored) {
		a := st.attemptOf(n)
		a.Node, a.Model, a.Status = served.Node, served.Model, http.StatusOK
		if !a.start.IsZero() {
			a.DurationMs = time.Since(a.start).Milliseconds()
		}
		st.Rec.Served = &served
		st.Rec.Cost, st.Rec.TokensIn, st.Rec.TokensOut, st.Rec.TPS = cost, tin, tout, tps
		st.Rec.Receipt = &receipt
	})
}

// genVerdictLater updates a record's moderation verdict that landed after the relay (the
// off-path screener): in flight, or already written.
func (b *broker) genVerdictLater(requestID, verdict string) {
	if g := b.genLiveOf(requestID); g != nil {
		g.mu.Lock()
		if !g.written {
			g.st.Rec.Moderation.Verdict = verdict
			g.mu.Unlock()
			return
		}
		g.mu.Unlock()
	}
	blob, found, err := b.genGet(requestID)
	if err != nil || !found {
		return
	}
	var st genStored
	if json.Unmarshal(blob, &st) != nil {
		return
	}
	st.Rec.Moderation.Verdict = verdict
	b.genPut(requestID, st)
}

// genWriter captures what the relay answered: its status and an error body's code.
type genWriter struct {
	http.ResponseWriter
	status int
	errBuf []byte
}

func (w *genWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *genWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.status >= 400 && len(w.errBuf) < 8192 {
		w.errBuf = append(w.errBuf, p...)
	}
	return w.ResponseWriter.Write(p)
}

func (w *genWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *genWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// genClose finishes the record when the relay returns and writes it.
func (b *broker) genClose(g *genLive, w *genWriter, r *http.Request) {
	b.gens.live.Delete(g.st.Rec.ID)
	g.mu.Lock()
	rec := &g.st.Rec
	rec.Status = w.status
	if rec.Status == 0 {
		rec.Status = http.StatusOK
	}
	if rec.Status >= 400 {
		var e struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if json.Unmarshal(w.errBuf, &e) == nil && e.Error.Code != "" {
			code := e.Error.Code
			rec.ErrorCode = &code
		}
		rec.RetryAfterS, _ = strconv.Atoi(w.Header().Get("Retry-After"))
	}
	for i := range rec.Attempts {
		if rec.Attempts[i].Status == 0 { // dispatched, never answered: the relay's answer
			rec.Attempts[i].Status = rec.Status
			if !rec.Attempts[i].start.IsZero() {
				rec.Attempts[i].DurationMs = time.Since(rec.Attempts[i].start).Milliseconds()
			}
		}
	}
	rec.LatencyMs = msOf(time.Since(g.start))
	if rec.Streamed && !g.firstFrame.IsZero() {
		rec.TTFTMs = msOf(g.firstFrame.Sub(g.start))
	}
	rec.Cancelled = r.Context().Err() != nil
	g.written = true
	st := g.st
	g.mu.Unlock()
	b.genPut(rec.ID, st)
}

// genPut writes a record: the shared store, else this instance's bounded map. A failure is
// logged and swallowed - the relay already answered.
func (b *broker) genPut(id string, st genStored) {
	blob, err := json.Marshal(st)
	if err != nil {
		return
	}
	if b.shared != nil {
		err := b.shared.putGen(id, blob, genRecordTTL)
		if err == nil {
			return
		}
		if err != errNoSharedStore {
			log.Printf("generation record write FAILED request=%s: %v", id, err)
			return
		}
	}
	b.gens.mu.Lock()
	defer b.gens.mu.Unlock()
	if b.gens.local == nil {
		b.gens.local = map[string][]byte{}
	}
	if _, ok := b.gens.local[id]; !ok {
		b.gens.order = append(b.gens.order, id)
	}
	b.gens.local[id] = blob
	for len(b.gens.order) > genLocalCap {
		delete(b.gens.local, b.gens.order[0])
		b.gens.order = b.gens.order[1:]
	}
}

// genGet reads a record (one shared-store read, hit or miss).
func (b *broker) genGet(id string) ([]byte, bool, error) {
	if b.shared != nil {
		blob, found, err := b.shared.getGen(id)
		if err != errNoSharedStore {
			return blob, found, err
		}
	}
	b.gens.mu.Lock()
	defer b.gens.mu.Unlock()
	blob, ok := b.gens.local[id]
	return blob, ok, nil
}

var requestIDPattern = regexp.MustCompile(`^[0-9a-f]{16}$`)

// generation handles GET /generation?id=<request id>.
func (b *broker) generation(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodGet) {
		return
	}
	corsCreds(w, r)
	b.stats.generationLookups.Add(1)
	id := r.URL.Query().Get("id")
	if !requestIDPattern.MatchString(id) {
		jsonErrCode(w, http.StatusBadRequest, "invalid_request_id", "id must be a request id: 16 lowercase hex characters")
		return
	}
	// The reader's identities: the signed wallet or browser session, the grant token, and the
	// payout owner. Resolved for every lookup, hit or miss (constant work).
	var wallet, grantID string
	if gc, gok, _ := b.resolveGrant(r); gok {
		grantID = gc.grant.ID
	} else if _, sw, sok := b.webSession(r); sok {
		wallet = sw
	} else if rid, authed, iok := b.identityOf(r, nil); iok && authed {
		// walletOf without its shared-store cache: the lookup's shared-store work must not
		// depend on whether this reader's wallet happened to be cached (constant work).
		wallet = rid
		if pub := r.Header.Get(protocol.HeaderPubkey); pub != "" {
			if o, ok, err := b.db.OwnerByPubkey(pub); err == nil && ok {
				if aw, ok := accountWalletForOwner(o); ok {
					wallet = aw
				}
			}
		}
	}
	_, owner, ownerOK := b.payoutOwner(r, nil)
	limitKey := wallet
	if limitKey == "" {
		limitKey = "gen-ip:" + clientIP(r)
	}
	if grantID != "" {
		limitKey = "gen-grant:" + grantID
	}
	if ok, retry := b.rl.allow(limitKey); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		jsonErr(w, http.StatusTooManyRequests, "rate limit exceeded - slow down")
		return
	}
	blob, found, err := b.genGet(id)
	if err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "generation records are unavailable right now - retry shortly")
		return
	}
	var st genStored
	if found && json.Unmarshal(blob, &st) != nil {
		found = false
	}
	consumer := found && ((wallet != "" && wallet != "anon" && wallet == st.Wallet) || (grantID != "" && grantID == st.GrantID))
	var view any
	if consumer {
		view = st.Rec
	} else if ownerOK && owner.Pubkey != "" {
		view = b.genOwnerView(st, owner.Pubkey, found)
	}
	if view == nil {
		jsonErr(w, http.StatusNotFound, "not found")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(view)
}

// genOwnerView is the record as a station owner reads it: only that owner's own attempts, the
// served station and the bill only when the reader's station served, and never the receipt
// (its user is the per-(user, node) pseudonym) or anything of the consumer's. nil = not theirs.
func (b *broker) genOwnerView(st genStored, ownerPub string, found bool) any {
	mine := func(node string) bool {
		if node == "" {
			return false
		}
		acct, ok, _ := b.db.AccountOfNode(node)
		same, _ := b.sameAccount(acct, ownerPub)
		return ok && same
	}
	minted := false
	if found && st.GrantOwner != "" {
		minted, _ = b.sameAccount(st.GrantOwner, ownerPub)
	}
	rec := st.Rec
	var attempts []genAttempt
	for _, a := range rec.Attempts {
		if minted || mine(a.Node) {
			attempts = append(attempts, a)
		}
	}
	if !found || (!minted && len(attempts) == 0) {
		return nil
	}
	view := map[string]any{
		"id": rec.ID, "created_at": rec.CreatedAt, "model_requested": rec.ModelRequested, "models": rec.Models,
		"streamed": rec.Streamed, "cancelled": rec.Cancelled, "status": rec.Status, "error_code": rec.ErrorCode,
		"moderation": rec.Moderation, "key_id": rec.KeyID, "attempts": attempts,
	}
	if rec.Served != nil && (minted || mine(rec.Served.Node)) {
		view["served"] = rec.Served
		view["cost"], view["tokens_in"], view["tokens_out"], view["tps"] = rec.Cost, rec.TokensIn, rec.TokensOut, rec.TPS
		view["ttft_ms"], view["latency_ms"] = rec.TTFTMs, rec.LatencyMs
	}
	return view
}

// msOf is a duration in milliseconds with microsecond resolution (a fast local relay is
// sub-millisecond, and 0 would read as "not measured").
func msOf(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// genServedFromReceipt is the served block of a direct attempt.
func genServedFromReceipt(node string, rec protocol.UsageReceipt) genServed {
	return genServed{Node: node, Model: rec.ServedModel()}
}
