package main

// upstream_failover_bdd_test.go makes features/routing/upstream_failover.feature EXECUTABLE
// against the REAL relay: a real broker (relayBroker) over a real store (the in-memory
// reference, or the real Postgres when ROGERAI_TEST_DATABASE_URL is set), a real shared store
// (a valkeyStore over miniredis), and N real stations, each with its OWN scripted httptest
// upstream and its own station loop (drain the job channel, serve the job over HTTP, pipe a
// stream through the real /agent/stream ingress, sign the receipt with the node key, return
// the JobResult through the real /agent/result handler). Nothing is mocked: every consequence
// is read back from the store, the shared store, the broker's own state, the response, or
// the log. The harness is the multi-station generalization of
// upstream_throttle_strikes_bdd_test.go.
//
// The clock: cooldowns are seconds-to-minutes long, so the scenarios drive the broker's clock
// seam (b.nowFn) forward instead of sleeping; the shared store's key TTLs are fast-forwarded
// in lock-step (miniredis.FastForward).

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
)

// fstation is one real station: its node identity, its owner, its scripted upstream and the
// bookkeeping the Then steps read (requests seen, rejected count, results posted).
type fstation struct {
	name, id        string
	priv            ed25519.PrivateKey
	pubHex          string
	ownerPriv       ed25519.PrivateKey
	acct            string
	model           string
	priceIn         float64
	priceOut        float64
	up              *httptest.Server
	mu              sync.Mutex
	script          func(n int, w http.ResponseWriter, r *http.Request)
	reqN            int
	rejected        int
	lastHash        string
	results         []map[string]any // decoded JSON of every JobResult this station posted
	forgeRetryAfter *int             // when set, the station posts retry_after_sec = *forgeRetryAfter
	tun             *nodeTunnel
	unreachable     bool
}

// statusWriter records the status an upstream script answered with (rejected counting).
type statusWriter struct {
	http.ResponseWriter
	code int
}

func (s *statusWriter) WriteHeader(c int) { s.code = c; s.ResponseWriter.WriteHeader(c) }
func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// recWriter is the consumer's response recorder: it also records EVERY status written and the
// wall time of the first SSE data frame (the streaming TTFT + single-200 assertions).
type recWriter struct {
	*httptest.ResponseRecorder
	mu        sync.Mutex
	statuses  []int
	firstData time.Time
}

func (r *recWriter) WriteHeader(c int) {
	r.mu.Lock()
	r.statuses = append(r.statuses, c)
	r.mu.Unlock()
	r.ResponseRecorder.WriteHeader(c)
}

func (r *recWriter) Write(p []byte) (int, error) {
	r.mu.Lock()
	if r.firstData.IsZero() && bytes.Contains(p, []byte("data:")) {
		r.firstData = time.Now()
	}
	r.mu.Unlock()
	return r.ResponseRecorder.Write(p)
}

type foState struct {
	t   *testing.T
	db  store.Store
	mem *store.Mem
	pg  *store.Postgres

	b     *broker
	b2    *broker // "instance B" when a scenario stands one up
	mr    *miniredis.Miniredis
	vs    *valkeyStore
	logs  *utLog
	nonce string

	sidecar *httptest.Server

	stations map[string]*fstation // by scenario name ("s1")
	order    []string

	clockBase   time.Time
	clockOffset time.Duration
	clockMu     sync.Mutex

	// consumer
	consumerPriv ed25519.PrivateKey
	wallet       string
	funded       bool
	start        float64
	anonPriv     ed25519.PrivateKey

	// request shaping
	maxPrice     string
	excludeNodes string
	pinNode      string
	confidential bool
	freq         string
	grantToken   string
	grantID      string
	tokens       int
	model        string

	// last relay outcome
	lastCode    int
	lastBody    []byte
	lastHdr     http.Header
	lastRec     *recWriter
	relayStart  time.Time
	relayEnd    time.Time
	codes       []int
	stationStop chan struct{}
	stationWG   sync.WaitGroup

	// snapshots for "no hold, no receipt" style assertions
	snapHolds    int
	snapReceipts int
	snapUpstream int

	// endpoint responses
	adminResp map[string]any

	// captured mail
	mailMu sync.Mutex
	mails  []string

	// every non-200 relay: did it carry a Retry-After? (Sep 7 no-sibling replay)
	non200RA []bool
	// stations were ordered s1-first for this scenario (see landOrder)
	ordered bool
	// upstream count of s1 before the prober ran
	probeBefore int
	// per-station upstream counts before a batch of relays
	countsBefore map[string]int

	// saved knobs
	savedWait time.Duration
}

var foRetryAfterNum = regexp.MustCompile(`^\d+$`)

// retryAfterSecsForTest mirrors what the station (internal/agent) does with an upstream
// Retry-After: delta-seconds or an HTTP-date, 0 for absent/garbage/past.
func retryAfterSecsForTest(v string, now time.Time) int {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if foRetryAfterNum.MatchString(v) {
		n, _ := strconv.Atoi(v)
		return n
	}
	if t, err := http.ParseTime(v); err == nil {
		d := t.Sub(now)
		if d <= 0 {
			return 0
		}
		return int(math.Ceil(d.Seconds()))
	}
	return 0
}

// --- clock -------------------------------------------------------------------

func (s *foState) now() time.Time {
	s.clockMu.Lock()
	defer s.clockMu.Unlock()
	return s.clockBase.Add(s.clockOffset)
}

func (s *foState) advance(d time.Duration) {
	s.clockMu.Lock()
	s.clockOffset += d
	s.clockMu.Unlock()
	if s.mr != nil {
		s.mr.FastForward(d)
	}
}

// --- harness -----------------------------------------------------------------

func (s *foState) reset() error {
	s.teardown()
	s.logs.Reset()
	s.nonce = utNonce()
	if dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL"); dsn != "" {
		pg, err := store.NewPostgres(dsn)
		if err != nil {
			return fmt.Errorf("postgres: %w", err)
		}
		s.pg, s.mem, s.db = pg, nil, pg
	} else {
		s.mem = store.NewMem()
		s.pg, s.db = nil, s.mem
	}
	s.mr = miniredis.NewMiniRedis()
	if err := s.mr.Start(); err != nil {
		return err
	}
	vs, err := newValkeyStore("redis://" + s.mr.Addr())
	if err != nil {
		return err
	}
	s.vs = vs
	s.b = s.newBroker()
	s.b2 = nil
	s.clockBase, s.clockOffset = time.Now(), 0

	s.sidecar = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"tokens": 1_000_000, "exact": true})
	}))
	s.b.recount = recountConfig{url: s.sidecar.URL, tolerance: 0.02, strikeTolerance: 0.25, client: &http.Client{Timeout: 4 * time.Second}}

	s.stations = map[string]*fstation{}
	s.order = nil
	s.stationStop = make(chan struct{})

	// The consumer: a signed, logged-in wallet.
	_, s.consumerPriv, _ = ed25519.GenerateKey(nil)
	cpub := hex.EncodeToString(s.consumerPriv.Public().(ed25519.PublicKey))
	cgid, _ := rand.Int(rand.Reader, big.NewInt(1<<31))
	if err := s.db.BindOwner(store.Owner{GitHubID: cgid.Int64() + 10, Login: "buyer-" + s.nonce, Pubkey: cpub}); err != nil {
		return err
	}
	s.wallet = "u_gh_" + strconv.FormatInt(cgid.Int64()+10, 10)
	_, s.anonPriv, _ = ed25519.GenerateKey(nil)
	s.funded, s.start = false, 0
	s.maxPrice, s.excludeNodes, s.pinNode, s.freq, s.grantToken, s.grantID = "", "", "", "", "", ""
	s.confidential = false
	s.tokens, s.model = 50, "m"
	s.lastCode, s.lastBody, s.lastHdr, s.lastRec = 0, nil, nil, nil
	s.codes = nil
	s.adminResp = nil
	s.mails = nil
	s.snapHolds, s.snapReceipts, s.snapUpstream = 0, 0, 0
	s.non200RA, s.ordered, s.probeBefore = nil, false, 0
	s.savedWait = nonStreamRelayWait
	return nil
}

func (s *foState) newBroker() *broker {
	b := relayBroker(s.db)
	b.rl = &rateLimiter{buckets: map[string]*tokenBucket{}}
	b.grantRL = &rateLimiter{buckets: map[string]*tokenBucket{}}
	b.strikeWarnAt, b.strikeBanAt = defaultStrikeWarnAt, defaultStrikeBanAt
	b.strikeCorroborateKinds, b.strikeDecayDays = defaultStrikeCorroborateKinds, defaultStrikeDecayDays
	b.adminKey = hex.EncodeToString(b.priv.Seed())
	b.shared = s.vs
	b.nowFn = s.now
	b.mail = enabledMailer(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		s.mailMu.Lock()
		s.mails = append(s.mails, string(body))
		s.mailMu.Unlock()
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"id":"m"}`))}, nil
	})
	return b
}

func (s *foState) teardown() {
	if s.stationStop != nil {
		close(s.stationStop)
		s.stationWG.Wait()
		s.stationStop = nil
	}
	for _, st := range s.stations {
		if st.up != nil {
			st.up.Close()
		}
	}
	if s.sidecar != nil {
		s.sidecar.Close()
		s.sidecar = nil
	}
	if s.vs != nil {
		_ = s.vs.Close()
		s.vs = nil
	}
	if s.mr != nil {
		s.mr.Close()
		s.mr = nil
	}
	if s.pg != nil {
		_ = s.pg.Close()
		s.pg = nil
	}
	if s.savedWait > 0 {
		nonStreamRelayWait = s.savedWait
	}
	_ = os.Unsetenv("ROGERAI_RELAY_FAILOVER")
}

type stationOpts struct {
	model     string
	priceIn   float64
	priceOut  float64
	ctx       int
	owner     *fstation // share this station's owner account (grant scenarios)
	ownerPriv ed25519.PrivateKey
}

// standUp registers a real station under a fresh owner (or a shared one) and starts its loop.
func (s *foState) standUp(name string, o stationOpts) *fstation {
	if st, ok := s.stations[name]; ok {
		return st
	}
	if o.model == "" {
		o.model = "m"
	}
	if o.ctx == 0 {
		o.ctx = 32768
	}
	st := &fstation{name: name, id: name + "-" + s.nonce, model: o.model, priceIn: o.priceIn, priceOut: o.priceOut}
	pub, priv, _ := ed25519.GenerateKey(nil)
	st.priv, st.pubHex = priv, hex.EncodeToString(pub)
	if o.owner != nil {
		st.ownerPriv, st.acct = o.owner.ownerPriv, o.owner.acct
	} else {
		if o.ownerPriv != nil {
			st.ownerPriv = o.ownerPriv
		} else {
			_, st.ownerPriv, _ = ed25519.GenerateKey(nil)
		}
		st.acct = hex.EncodeToString(st.ownerPriv.Public().(ed25519.PublicKey))
		gid, _ := rand.Int(rand.Reader, big.NewInt(1<<31))
		if err := s.db.BindOwner(store.Owner{GitHubID: gid.Int64() + 10, Login: "op-" + name + "-" + s.nonce, Pubkey: st.acct, Email: name + "@example.com"}); err != nil {
			s.t.Fatalf("bind owner: %v", err)
		}
	}
	st.up = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st.mu.Lock()
		st.reqN++
		n, script := st.reqN, st.script
		st.mu.Unlock()
		sw := &statusWriter{ResponseWriter: w, code: 200}
		defer func() {
			st.mu.Lock()
			if sw.code >= 400 {
				st.rejected++
			}
			st.mu.Unlock()
		}()
		if script == nil {
			utRealCompletion(sw)
			return
		}
		script(n, sw, r)
	}))
	s.b.nodes[st.id] = protocol.NodeRegistration{
		NodeID: st.id, PubKey: st.pubHex,
		Offers: []protocol.ModelOffer{{Model: o.model, PriceIn: o.priceIn, PriceOut: o.priceOut, Ctx: o.ctx}},
	}
	s.b.lastSeen[st.id] = time.Now()
	st.tun = &nodeTunnel{jobs: make(chan protocol.Job, 64), waiters: map[string]chan protocol.JobResult{}, token: "tok-" + st.id}
	s.b.tunnels[st.id] = st.tun
	if err := s.db.BindNode(st.id, st.acct); err != nil {
		s.t.Fatalf("bind node: %v", err)
	}
	s.stations[name] = st
	s.order = append(s.order, name)
	s.stationWG.Add(1)
	go s.station(st, s.stationStop)
	return st
}

func (s *foState) st(name string) *fstation {
	st, ok := s.stations[name]
	if !ok {
		s.t.Fatalf("no station %q in this scenario", name)
	}
	return st
}

// station is the REAL return path of a node (see upstream_throttle_strikes_bdd_test.go), with
// a per-node receipt chain (prev_hash) and the upstream Retry-After captured on a 429/503 the
// way internal/agent does.
func (s *foState) station(st *fstation, stop <-chan struct{}) {
	defer s.stationWG.Done()
	for {
		select {
		case <-stop:
			return
		case job := <-st.tun.jobs:
			var req struct {
				Stream bool `json:"stream"`
			}
			_ = json.Unmarshal(job.Body, &req)
			status, body := http.StatusBadGateway, []byte(`{"error":"upstream unreachable"}`)
			retryAfter := 0
			var pt, ct int
			st.mu.Lock()
			unreachable := st.unreachable
			st.mu.Unlock()
			var resp *http.Response
			var err error
			if unreachable {
				err = fmt.Errorf("connection refused")
			} else {
				resp, err = http.Post(st.up.URL, "application/json", bytes.NewReader(job.Body))
			}
			if err == nil {
				status = resp.StatusCode
				if status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable {
					retryAfter = retryAfterSecsForTest(resp.Header.Get("Retry-After"), time.Now())
				}
				if req.Stream {
					pr, pw := io.Pipe()
					go func() {
						sc := bufio.NewScanner(resp.Body)
						sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
						for sc.Scan() {
							line := sc.Bytes()
							_, _ = pw.Write(line)
							_, _ = pw.Write([]byte{'\n'})
							if bytes.Contains(line, []byte(`"usage"`)) {
								if i := bytes.IndexByte(line, '{'); i >= 0 {
									var d struct {
										Usage struct {
											PromptTokens     int `json:"prompt_tokens"`
											CompletionTokens int `json:"completion_tokens"`
										} `json:"usage"`
									}
									if json.Unmarshal(line[i:], &d) == nil {
										pt, ct = d.Usage.PromptTokens, d.Usage.CompletionTokens
									}
								}
							}
						}
						_ = pw.Close()
					}()
					sreq := httptest.NewRequest(http.MethodPost, "/agent/stream?node="+st.id+"&job="+job.ID, pr)
					sreq.Header.Set("Authorization", "Bearer "+st.tun.token)
					s.b.agentStream(httptest.NewRecorder(), sreq)
					body = nil
				} else {
					body, _ = io.ReadAll(resp.Body)
					var d struct {
						Usage struct {
							PromptTokens     int `json:"prompt_tokens"`
							CompletionTokens int `json:"completion_tokens"`
						} `json:"usage"`
					}
					_ = json.Unmarshal(body, &d)
					pt, ct = d.Usage.PromptTokens, d.Usage.CompletionTokens
				}
				resp.Body.Close()
			}
			rec := protocol.UsageReceipt{
				RequestID: job.ID, NodeID: st.id, User: job.User, Model: st.model,
				PromptTokens: pt, CompletionTokens: ct, PriceIn: st.priceIn, PriceOut: st.priceOut,
				TS: time.Now().Unix(), LineageMethod: "p0-upstream-usage",
			}
			st.mu.Lock()
			rec.PrevHash = st.lastHash
			rec.SignNode(st.priv)
			st.lastHash = rec.Hash()
			forge := st.forgeRetryAfter
			st.mu.Unlock()
			res := protocol.JobResult{ID: job.ID, Status: status, Body: body, Receipt: rec}
			wire, _ := json.Marshal(res)
			var m map[string]any
			_ = json.Unmarshal(wire, &m)
			if forge != nil {
				m["retry_after_sec"] = *forge
			} else if retryAfter > 0 {
				m["retry_after_sec"] = retryAfter
			}
			wire, _ = json.Marshal(m)
			var posted map[string]any
			_ = json.Unmarshal(wire, &posted)
			st.mu.Lock()
			st.results = append(st.results, posted)
			st.mu.Unlock()
			s.deliverRaw(st, wire)
		}
	}
}

func (s *foState) deliverRaw(st *fstation, wire []byte) {
	r := httptest.NewRequest(http.MethodPost, "/agent/result?node="+st.id, bytes.NewReader(wire))
	r.Header.Set("Authorization", "Bearer "+st.tun.token)
	s.b.agentResult(httptest.NewRecorder(), r)
}

// --- scripts -----------------------------------------------------------------

func (st *fstation) set(f func(n int, w http.ResponseWriter, r *http.Request)) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.script = f
}

func (st *fstation) scriptStatus(code int, body string, hdr map[string]string) {
	st.set(func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		for k, v := range hdr {
			w.Header().Set(k, v)
		}
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	})
}

func (st *fstation) script429(retryAfter string) {
	hdr := map[string]string{}
	if retryAfter != "" {
		hdr["Retry-After"] = retryAfter
	}
	st.scriptStatus(429, utDefaultBody(429), hdr)
}

func (st *fstation) scriptReal() {
	st.set(func(_ int, w http.ResponseWriter, _ *http.Request) { utRealCompletion(w) })
}

func (st *fstation) scriptStream(chunks int, die bool) {
	st.set(func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		f, _ := w.(http.Flusher)
		for i := 0; i < chunks; i++ {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"chunk%d from %s \"}}]}\n\n", i+1, st.name)
			if f != nil {
				f.Flush()
			}
		}
		if die {
			panic(http.ErrAbortHandler) // the connection dies mid-stream
		}
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":5000,\"completion_tokens\":20}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
}

func (st *fstation) upstreamCount() int {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.reqN
}

func (st *fstation) rejectedCount() int {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.rejected
}

func (st *fstation) postedResults() []map[string]any {
	st.mu.Lock()
	defer st.mu.Unlock()
	out := make([]map[string]any, len(st.results))
	copy(out, st.results)
	return out
}

// --- consumer plumbing -------------------------------------------------------

func (s *foState) fund(amount float64) error {
	if _, err := s.db.AddCredits(s.wallet, amount); err != nil {
		return err
	}
	bal, err := s.db.PeekBalance(s.wallet)
	if err != nil {
		return err
	}
	s.funded, s.start = true, bal
	return nil
}

func (s *foState) ensureFunded() error {
	if s.funded {
		return nil
	}
	return s.fund(10)
}

func (s *foState) body(stream bool) []byte {
	m := map[string]any{
		"model":    s.model,
		"messages": []map[string]string{{"role": "user", "content": utPrompt(s.tokens)}},
	}
	if stream {
		m["stream"] = true
	}
	b, _ := json.Marshal(m)
	return b
}

// relay fires one signed consumer relay with the scenario's request shaping.
func (s *foState) relay(stream bool, priv ed25519.PrivateKey) {
	body := s.body(stream)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	if s.grantToken != "" {
		r.Header.Set("Authorization", "Bearer "+s.grantToken)
	} else {
		signReq(r, priv, body)
	}
	if s.maxPrice != "" {
		r.Header.Set("X-Roger-Max-Price", s.maxPrice)
	}
	if s.excludeNodes != "" {
		r.Header.Set("X-Roger-Exclude-Nodes", s.excludeNodes)
	}
	if s.pinNode != "" {
		r.Header.Set("X-Roger-Node", s.pinNode)
	}
	if s.confidential {
		r.Header.Set("X-Roger-Confidential", "1")
	}
	if s.freq != "" {
		r.Header.Set("X-Roger-Freq", s.freq)
	}
	w := &recWriter{ResponseRecorder: httptest.NewRecorder()}
	s.relayStart = time.Now()
	s.b.relay(w, r)
	s.relayEnd = time.Now()
	s.lastRec = w
	s.lastCode, s.lastBody, s.lastHdr = w.Code, w.Body.Bytes(), w.Header()
	s.codes = append(s.codes, w.Code)
	if w.Code != 200 {
		s.non200RA = append(s.non200RA, w.Header().Get("Retry-After") != "")
	}
}

func (s *foState) fundedRelay() error {
	if err := s.ensureFunded(); err != nil {
		return err
	}
	s.landOrder()
	s.relay(false, s.consumerPriv)
	return nil
}

// landOrder makes the scenario's stand-up order the pick order: the first station stays
// Tier A; every later one is demoted to Tier B with a strictly decreasing success average,
// so the re-pick after a failure - Tier A empty, P2C over the probation tier - resolves to
// the next station by score (a two-member P2C pool is decided by score at equal load).
func (s *foState) landOrder() {
	if s.ordered || len(s.order) < 2 {
		return
	}
	s.ordered = true
	s.b.metricsMu.Lock()
	for i, n := range s.order[1:] {
		s.b.success[s.stations[n].id] = 0.5 - 0.05*float64(i)
	}
	s.b.metricsMu.Unlock()
}

func (s *foState) fundedStream() error {
	if err := s.ensureFunded(); err != nil {
		return err
	}
	s.relay(true, s.consumerPriv)
	return nil
}

// landOn demotes every station except `name` to Tier B (a measured success average below
// the 0.55 gate), so the FIRST pick is always `name` while the re-pick after excluding it
// falls to the probation tier - features/routing/scoring.feature "Tier B is used only when
// Tier A is empty".
func (s *foState) landOn(name string) {
	s.ordered = true
	s.b.metricsMu.Lock()
	for n, st := range s.stations {
		if n != name {
			s.b.success[st.id] = 0.3
		}
	}
	s.b.metricsMu.Unlock()
}

func (s *foState) pinnedRelay(name string) error {
	prev := s.pinNode
	s.pinNode = s.st(name).id
	err := s.fundedRelay()
	s.pinNode = prev
	return err
}

// cool makes station `name` cool for `secs` seconds through the REAL path: a pinned relay
// whose upstream answers 429 with that Retry-After. The station's script is restored after.
func (s *foState) cool(name string, secs int) error {
	st := s.st(name)
	st.mu.Lock()
	prev := st.script
	st.mu.Unlock()
	st.script429(strconv.Itoa(secs))
	if err := s.pinnedRelay(name); err != nil {
		return err
	}
	if s.lastCode != 429 {
		return fmt.Errorf("cooling %s: pinned relay = %d, want 429 (%s)", name, s.lastCode, s.lastBody)
	}
	st.set(prev)
	return nil
}

// --- reads -------------------------------------------------------------------

func (s *foState) balance() (float64, error) { return s.db.PeekBalance(s.wallet) }

func (s *foState) entries() ([]store.Entry, error) { return s.db.RecentByUser(s.wallet, 2000) }

func (s *foState) entriesOf(name string) ([]store.Entry, error) {
	es, err := s.entries()
	if err != nil {
		return nil, err
	}
	var out []store.Entry
	for _, e := range es {
		if e.Node == s.st(name).id {
			out = append(out, e)
		}
	}
	return out, nil
}

func (s *foState) storedReceipt(reqID string) (protocol.UsageReceipt, map[string]any, error) {
	var raw []byte
	if s.pg != nil {
		var txt string
		if err := s.pg.DB().QueryRow(`SELECT receipt::text FROM rogerai.receipts WHERE request_id=$1`, reqID).Scan(&txt); err != nil {
			return protocol.UsageReceipt{}, nil, fmt.Errorf("receipt %s: %w", reqID, err)
		}
		raw = []byte(txt)
	} else {
		rec, ok := s.mem.ReceiptOf(reqID)
		if !ok {
			return protocol.UsageReceipt{}, nil, fmt.Errorf("no stored receipt for %s", reqID)
		}
		raw, _ = json.Marshal(rec)
	}
	var rec protocol.UsageReceipt
	var keys map[string]any
	if err := json.Unmarshal(raw, &rec); err != nil {
		return rec, nil, err
	}
	if err := json.Unmarshal(raw, &keys); err != nil {
		return rec, nil, err
	}
	return rec, keys, nil
}

func (s *foState) voidReasonOf(name string) (string, string, error) {
	es, err := s.entriesOf(name)
	if err != nil {
		return "", "", err
	}
	if len(es) == 0 {
		return "", "", fmt.Errorf("no receipt for %s", name)
	}
	_, keys, err := s.storedReceipt(es[0].RequestID)
	if err != nil {
		return "", "", err
	}
	vr, _ := keys["void_reason"].(string)
	return vr, es[0].RequestID, nil
}

func (s *foState) holdRows() (holds, releases, spends int, holdAmt float64, err error) {
	rows, err := s.db.LedgerOf(s.wallet, []string{store.KindHold, store.KindHoldRelease, store.KindSpend}, 5000)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	for _, r := range rows {
		switch r.Kind {
		case store.KindHold:
			holds++
			holdAmt += -r.Amount
		case store.KindHoldRelease:
			releases++
		case store.KindSpend:
			if r.Amount != 0 { // a voided attempt's $0 metering row is not a charge
				spends++
			}
		}
	}
	return
}

func (s *foState) receiptCount() (int, error) {
	es, err := s.entries()
	return len(es), err
}

func (s *foState) pickOK(pin string, exclude, allow map[string]bool, b *broker) (protocol.NodeRegistration, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n, _, ok := b.pickFor(s.model, false, 0, 0, 0, pin, exclude, allow, allow, pickReq{rng: seededRand("fo-" + utNonce())})
	return n, ok
}

func (s *foState) isCooling(name string) bool {
	_, ok := s.pickOK(s.st(name).id, nil, nil, s.b)
	return !ok
}

// bandCoolingRetryAfter fires a relay and returns the Retry-After of the band-cooling 503 it
// expects (the wire-level readout of a station's remaining cooldown).
func (s *foState) bandCoolingRetryAfter(name string) (int, error) {
	before := s.st(name).upstreamCount()
	if err := s.pinnedRelay(name); err != nil {
		return 0, err
	}
	if s.lastCode != 503 || !bytes.Contains(s.lastBody, []byte("band cooling")) {
		return 0, fmt.Errorf("relay while %s cooling = %d %s, want 503 band cooling", name, s.lastCode, s.lastBody)
	}
	if s.st(name).upstreamCount() != before {
		return 0, fmt.Errorf("a band-cooling 503 still dispatched to %s", name)
	}
	ra, err := strconv.Atoi(s.lastHdr.Get("Retry-After"))
	if err != nil {
		return 0, fmt.Errorf("band-cooling 503 Retry-After %q is not a number", s.lastHdr.Get("Retry-After"))
	}
	return ra, nil
}

func (s *foState) snapshot() error {
	h, _, _, _, err := s.holdRows()
	if err != nil {
		return err
	}
	rc, err := s.receiptCount()
	if err != nil {
		return err
	}
	s.snapHolds, s.snapReceipts = h, rc
	total := 0
	for _, st := range s.stations {
		total += st.upstreamCount()
	}
	s.snapUpstream = total
	return nil
}

func (s *foState) strikesOf(name string) ([]store.Strike, error) {
	return s.db.StrikesByOwner(s.st(name).acct, 0)
}

func (s *foState) heldOwner(name string) (bool, error) {
	return s.db.AccountRecountHeld(s.st(name).acct)
}

// --- Background --------------------------------------------------------------

func (s *foState) background() error { return nil }

// --- Given: stations ---------------------------------------------------------

func (s *foState) twoSamePrice() error {
	s.standUp("s1", stationOpts{priceIn: 1, priceOut: 1})
	s.standUp("s2", stationOpts{priceIn: 1, priceOut: 1})
	return nil
}

func (s *foState) s1_429_s2Real() error {
	if err := s.twoSamePrice(); err != nil {
		return err
	}
	s.st("s1").script429("")
	s.st("s2").scriptReal()
	return nil
}

func (s *foState) s1FailureS2Real(failure string) error {
	if err := s.twoSamePrice(); err != nil {
		return err
	}
	s.st("s2").scriptReal()
	st := s.st("s1")
	switch {
	case failure == "returns 429":
		st.script429("")
	case strings.HasPrefix(failure, "returns 200 with an empty completion"):
		st.scriptStatus(200, `{"choices":[],"usage":{"prompt_tokens":5000,"completion_tokens":0}}`, nil)
	case strings.HasPrefix(failure, "is unreachable"):
		st.mu.Lock()
		st.unreachable = true
		st.mu.Unlock()
	case strings.HasPrefix(failure, "returns "):
		n, err := strconv.Atoi(strings.TrimPrefix(failure, "returns "))
		if err != nil {
			return err
		}
		st.scriptStatus(n, utDefaultBody(n), nil)
	default:
		return fmt.Errorf("unknown failure %q", failure)
	}
	return nil
}

func (s *foState) s1StatusBody(status, body string) error {
	if err := s.twoSamePrice(); err != nil {
		return err
	}
	n, err := strconv.Atoi(status)
	if err != nil {
		return err
	}
	s.st("s1").scriptStatus(n, body, nil)
	s.st("s2").scriptReal()
	return nil
}

func (s *foState) fourAll429() error {
	for _, n := range []string{"s1", "s2", "s3", "s4"} {
		s.standUp(n, stationOpts{priceIn: 1, priceOut: 1}).script429("")
	}
	return nil
}

func (s *foState) twoBoth500() error {
	if err := s.twoSamePrice(); err != nil {
		return err
	}
	s.st("s1").scriptStatus(500, utDefaultBody(500), nil)
	s.st("s2").scriptStatus(500, utDefaultBody(500), nil)
	return nil
}

// s1Holds85s: the 90s window is scaled to 2s for the test (nonStreamRelayWait is the
// production var) and the station holds 95% of it, exactly the 85-of-90 shape.
func (s *foState) s1Holds85s() error {
	if err := s.twoSamePrice(); err != nil {
		return err
	}
	nonStreamRelayWait = 2 * time.Second
	hold := 1900 * time.Millisecond
	s.st("s1").set(func(_ int, w http.ResponseWriter, _ *http.Request) {
		time.Sleep(hold)
		w.WriteHeader(500)
		_, _ = w.Write([]byte(utDefaultBody(500)))
	})
	s.st("s2").scriptReal()
	return nil
}

func (s *foState) threeS1_429() error {
	for _, n := range []string{"s1", "s2", "s3"} {
		s.standUp(n, stationOpts{priceIn: 1, priceOut: 1}).scriptReal()
	}
	s.st("s1").script429("")
	return nil
}

func (s *foState) cheapS1PriceyS2() error {
	s.standUp("s1", stationOpts{priceIn: 0.5, priceOut: 0.5}).script429("")
	s.standUp("s2", stationOpts{priceIn: 5, priceOut: 5}).scriptReal()
	s.maxPrice = "1.0"
	return nil
}

func (s *foState) confidentialTrio() error {
	for _, n := range []string{"s1", "s2", "s3"} {
		s.standUp(n, stationOpts{priceIn: 1, priceOut: 1}).scriptReal()
	}
	s.st("s1").script429("")
	s.b.confidential[s.st("s1").id] = true
	s.b.confidential[s.st("s2").id] = true
	return nil
}

func (s *foState) privateBand() error {
	for _, n := range []string{"s1", "s2", "s3"} {
		s.standUp(n, stationOpts{priceIn: 1, priceOut: 1}).scriptReal()
	}
	s.st("s1").script429("")
	s.b.private[s.st("s1").id] = true
	s.b.private[s.st("s2").id] = true
	code := "147.520 MHz · ABCD-" + strings.ToUpper(s.nonce[:4])
	s.freq = code
	return s.db.CreateBand(store.Band{ID: "band_" + s.nonce, CodeHash: protocol.BandCodeHash(code), CodeDisplay: "147.520 MHz · ••••-••••",
		Owner: s.st("s1").acct, NodeID: s.st("s1").id, CreatedAt: time.Now().Unix()})
}

func (s *foState) failoverKnob(v string) error { return os.Setenv("ROGERAI_RELAY_FAILOVER", v) }

func (s *foState) s1_429_s2Healthy() error { return s.s1_429_s2Real() }

// --- Given: money ------------------------------------------------------------

func (s *foState) s1At1S2At2() error {
	s.standUp("s1", stationOpts{priceIn: 1, priceOut: 1}).script429("")
	s.standUp("s2", stationOpts{priceIn: 2, priceOut: 2}).scriptReal()
	return nil
}

func (s *foState) s1At1S2At50() error {
	s.standUp("s1", stationOpts{priceIn: 1, priceOut: 1}).script429("")
	s.standUp("s2", stationOpts{priceIn: 50, priceOut: 50}).scriptReal()
	return nil
}

func (s *foState) balanceCoversS1NotS2() error {
	// max cost at 1.00/1.00 for the 50-token prompt is ~0.033; at 50.00/50.00 it is ~1.65.
	return s.fund(0.5)
}

func (s *foState) s1_429_s2_500_s3Serves() error {
	s.standUp("s1", stationOpts{priceIn: 1, priceOut: 1}).script429("")
	s.standUp("s2", stationOpts{priceIn: 1, priceOut: 1}).scriptStatus(500, utDefaultBody(500), nil)
	s.standUp("s3", stationOpts{priceIn: 1, priceOut: 1}).scriptReal()
	return nil
}

func (s *foState) s1_429_s2Serves() error { return s.s1_429_s2Real() }

func (s *foState) grantWithCap() error {
	owner := s.standUp("s1", stationOpts{priceIn: 1, priceOut: 1})
	owner.script429("")
	s.standUp("s2", stationOpts{priceIn: 1, priceOut: 1, owner: owner}).scriptReal()
	secret := "rog-grant_" + s.nonce
	sum := sha256.Sum256([]byte(secret))
	s.grantID = "grant_" + s.nonce
	s.grantToken = secret
	return s.db.CreateGrant(store.Grant{ID: s.grantID, SecretHash: hex.EncodeToString(sum[:]), Owner: owner.acct, Label: "cap", Free: true, DailyCap: 1_000_000, CreatedAt: time.Now().Unix()})
}

func (s *foState) freeAndPaid() error {
	s.standUp("f1", stationOpts{priceIn: 0, priceOut: 0}).script429("")
	s.standUp("f2", stationOpts{priceIn: 0, priceOut: 0}).scriptReal()
	s.standUp("p1", stationOpts{priceIn: 1, priceOut: 1}).scriptReal()
	// An anonymous wallet is seeded exactly as production seeds first use (free credits),
	// so the ~$0 free-offer hold can land; it is still NOT logged in and cannot spend.
	s.b.seedFunds = 0.5
	return nil
}

// --- Given: streaming --------------------------------------------------------

func (s *foState) s1_429_s2Streams() error {
	if err := s.twoSamePrice(); err != nil {
		return err
	}
	s.st("s1").script429("")
	s.st("s2").scriptStream(3, false)
	return nil
}

func (s *foState) s1DiesMidStream() error {
	if err := s.twoSamePrice(); err != nil {
		return err
	}
	s.st("s1").scriptStream(2, true)
	s.st("s2").scriptStream(3, false)
	return nil
}

func (s *foState) both429RetryAfter(n string) error {
	if err := s.twoSamePrice(); err != nil {
		return err
	}
	s.st("s1").script429(n)
	s.st("s2").script429(n)
	return nil
}

// --- Given: cooldown ---------------------------------------------------------

func (s *foState) only(name string) *fstation {
	return s.standUp(name, stationOpts{priceIn: 1, priceOut: 1})
}

func (s *foState) s1_429RetryAfter(value string) error {
	st := s.only("s1")
	if value == "an HTTP-date 45 seconds from now" {
		st.set(func(_ int, w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", time.Now().Add(45*time.Second).UTC().Format(http.TimeFormat))
			w.WriteHeader(429)
			_, _ = w.Write([]byte(utDefaultBody(429)))
		})
		return nil
	}
	st.script429(value)
	return nil
}

func (s *foState) s1_429NoRetryAfter() error {
	s.only("s1").script429("")
	return nil
}

func (s *foState) s1CoolingS2Healthy() error {
	if err := s.twoSamePrice(); err != nil {
		return err
	}
	s.st("s2").scriptReal()
	return s.cool("s1", 60)
}

func (s *foState) s1CooledFor(n string) error {
	secs, err := strconv.Atoi(n)
	if err != nil {
		return err
	}
	s.only("s1")
	return s.cool("s1", secs)
}

func (s *foState) secondsPass(n string) error {
	secs, err := strconv.Atoi(n)
	if err != nil {
		return err
	}
	s.advance(time.Duration(secs) * time.Second)
	return nil
}

// instanceB stands up a second broker over the SAME shared store, mirroring the stations.
func (s *foState) instanceB() *broker {
	b2 := s.newBroker()
	b2.recount = s.b.recount
	for id, n := range s.b.nodes {
		b2.nodes[id] = n
		b2.lastSeen[id] = time.Now()
		b2.tunnels[id] = s.b.tunnels[id]
	}
	s.b2 = b2
	return b2
}

func (s *foState) twoInstancesS1Cooled() error {
	if err := s.twoSamePrice(); err != nil {
		return err
	}
	s.st("s2").scriptReal()
	s.instanceB()
	return s.cool("s1", 60)
}

func (s *foState) sharedUnreachable() error {
	if err := s.twoSamePrice(); err != nil {
		return err
	}
	s.st("s2").scriptReal()
	s.mr.Close()
	return nil
}

func (s *foState) s1_429OnA() error { return s.cool("s1", 30) }

func (s *foState) s1_503() error {
	s.only("s1").scriptStatus(503, utDefaultBody(503), nil)
	return nil
}

// s1_429ThreeTimes: three CONCURRENT relays are all dispatched before any 429 lands (the
// only way a station can answer 429 three times in a row - once it is cooling nothing more
// is dispatched to it); the upstream staggers its answers so the LAST 429 lands last.
func (s *foState) s1_429ThreeTimes() error {
	st := s.only("s1")
	st.set(func(n int, w http.ResponseWriter, _ *http.Request) {
		time.Sleep(time.Duration(n) * 250 * time.Millisecond)
		w.Header().Set("Retry-After", "10")
		w.WriteHeader(429)
		_, _ = w.Write([]byte(utDefaultBody(429)))
	})
	if err := s.ensureFunded(); err != nil {
		return err
	}
	var wg sync.WaitGroup
	codes := make([]int, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := s.body(false)
			r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
			signReq(r, s.consumerPriv, body)
			r.Header.Set("X-Roger-Node", st.id)
			w := httptest.NewRecorder()
			s.b.relay(w, r)
			codes[i] = w.Code
		}(i)
	}
	wg.Wait()
	for _, c := range codes {
		if c != 429 {
			return fmt.Errorf("concurrent pinned relays = %v, want all 429", codes)
		}
	}
	if st.upstreamCount() != 3 {
		return fmt.Errorf("s1 answered %d requests, want 3", st.upstreamCount())
	}
	return nil
}

func (s *foState) s1Cooled20Times() error {
	s.only("s1").script429("")
	for i := 0; i < 20; i++ {
		if err := s.pinnedRelay("s1"); err != nil {
			return err
		}
		if s.lastCode != 429 {
			return fmt.Errorf("relay %d = %d, want 429", i, s.lastCode)
		}
		s.advance(16 * time.Second)
	}
	return nil
}

func (s *foState) forged429(n string) error {
	v, err := strconv.Atoi(n)
	if err != nil {
		return err
	}
	st := s.only("s1")
	st.script429("")
	st.mu.Lock()
	st.forgeRetryAfter = &v
	st.mu.Unlock()
	return s.fundedRelay()
}

func (s *foState) forged200(n string) error {
	v, err := strconv.Atoi(n)
	if err != nil {
		return err
	}
	st := s.only("s1")
	st.scriptReal()
	st.mu.Lock()
	st.forgeRetryAfter = &v
	st.mu.Unlock()
	return s.fundedRelay()
}

// --- Given: only station cooling ---------------------------------------------

func (s *foState) onlyS1CoolingFor(n string) error {
	secs, err := strconv.Atoi(n)
	if err != nil {
		return err
	}
	s.only("s1").scriptReal()
	if err := s.cool("s1", secs); err != nil {
		return err
	}
	return s.snapshot()
}

func (s *foState) s1_20_s2_5() error {
	if err := s.twoSamePrice(); err != nil {
		return err
	}
	if err := s.cool("s1", 20); err != nil {
		return err
	}
	return s.cool("s2", 5)
}

func (s *foState) onlyS1Cooling() error {
	s.only("s1").scriptReal()
	return s.cool("s1", 30)
}

func (s *foState) onlyS1CooldownExpired() error {
	s.only("s1").scriptReal()
	if err := s.cool("s1", 10); err != nil {
		return err
	}
	s.advance(10 * time.Second)
	return nil
}

// --- Given: Retry-After ------------------------------------------------------

func (s *foState) all429LastRA(n string) error {
	if err := s.twoSamePrice(); err != nil {
		return err
	}
	s.st("s1").script429(n)
	s.st("s2").script429(n)
	return nil
}

func (s *foState) all429NoRA() error { return s.all429LastRA("") }

func (s *foState) all503LastRA(n string) error {
	if err := s.twoSamePrice(); err != nil {
		return err
	}
	hdr := map[string]string{"Retry-After": n}
	s.st("s1").scriptStatus(503, utDefaultBody(503), hdr)
	s.st("s2").scriptStatus(503, utDefaultBody(503), hdr)
	return nil
}

func (s *foState) relaySucceeds() error {
	s.only("s1").scriptReal()
	return s.fundedRelay()
}

var foLegacyResult []byte

func (s *foState) legacyJobResult() error {
	foLegacyResult = []byte(`{"id":"j1","status":200,"body":"eyJvayI6dHJ1ZX0=","receipt":{"request_id":"j1","node_id":"n","user":"u","model":"m","prompt_tokens":3,"completion_tokens":4}}`)
	return nil
}

var foDecoded protocol.JobResult

func (s *foState) brokerDecodes() error { return json.Unmarshal(foLegacyResult, &foDecoded) }

func (s *foState) decodedAsBefore() error {
	if foDecoded.ID != "j1" || foDecoded.Status != 200 || string(foDecoded.Body) != `{"ok":true}` || foDecoded.Receipt.PromptTokens != 3 {
		return fmt.Errorf("legacy JobResult decoded wrong: %+v", foDecoded)
	}
	re, _ := json.Marshal(foDecoded)
	var keys map[string]any
	_ = json.Unmarshal(re, &keys)
	if v, ok := keys["retry_after_sec"]; ok {
		return fmt.Errorf("retry_after_sec=%v on a legacy result, want absent/0", v)
	}
	return nil
}

func (s *foState) upstreamSequence() error {
	st := s.only("s1")
	st.set(func(n int, w http.ResponseWriter, _ *http.Request) {
		switch n {
		case 1:
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(429)
			_, _ = w.Write([]byte(utDefaultBody(429)))
		case 2:
			w.Header().Set("Retry-After", "3")
			w.WriteHeader(503)
			_, _ = w.Write([]byte(utDefaultBody(503)))
		default:
			w.Header().Set("Retry-After", "99")
			utRealCompletion(w)
		}
	})
	for i := 0; i < 3; i++ {
		if err := s.pinnedRelay("s1"); err != nil {
			return err
		}
		s.advance(130 * time.Second) // past any cooldown between the three
	}
	return nil
}

func (s *foState) resultsCarry(a, b, c string) error {
	res := s.st("s1").postedResults()
	if len(res) != 3 {
		return fmt.Errorf("%d results posted, want 3", len(res))
	}
	want := []string{a, b, c}
	for i, m := range res {
		got := 0
		if v, ok := m["retry_after_sec"].(float64); ok {
			got = int(v)
		}
		w, _ := strconv.Atoi(want[i])
		if got != w {
			return fmt.Errorf("result %d retry_after_sec=%d, want %d (%v)", i+1, got, w, m)
		}
	}
	return nil
}

var foProxyUpstream *httptest.Server
var foProxyRec *httptest.ResponseRecorder

func (s *foState) brokerReturns429RA(n string) error {
	foProxyUpstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", n)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited","type":"rate_limit_error"}}`))
	}))
	return nil
}

func (s *foState) proxyRelays() error {
	defer foProxyUpstream.Close()
	s.t.Setenv("HOME", s.t.TempDir())
	s.t.Setenv("XDG_CONFIG_HOME", s.t.TempDir())
	h := client.ProxyHandler(client.ProxyOptions{Broker: foProxyUpstream.URL, User: "u"})
	foProxyRec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
	h.ServeHTTP(foProxyRec, req)
	return nil
}

func (s *foState) agentSeesRA(n string) error {
	if foProxyRec.Code != 429 || foProxyRec.Header().Get("Retry-After") != n {
		return fmt.Errorf("proxy relayed %d Retry-After %q, want 429 / %s", foProxyRec.Code, foProxyRec.Header().Get("Retry-After"), n)
	}
	return nil
}

// --- Given: probes -----------------------------------------------------------

func (s *foState) s1Cooling() error {
	s.only("s1").scriptReal()
	return s.cool("s1", 60)
}

func (s *foState) proberRuns() error {
	st := s.st("s1")
	s.probeBefore = st.upstreamCount()
	s.b.probeNode(s.b.nodes[st.id], st.model, nextCanary(0))
	return nil
}

func (s *foState) s1_429ToCanary() error {
	s.only("s1").script429("")
	return nil
}

func (s *foState) canaryDispatched(name string) error {
	if s.st(name).upstreamCount() != s.probeBefore+1 {
		return fmt.Errorf("the canary never reached %s (%d -> %d)", name, s.probeBefore, s.st(name).upstreamCount())
	}
	return nil
}

func (s *foState) probeFail1() error {
	s.b.metricsMu.Lock()
	tq := s.b.trust[s.st("s1").id]
	s.b.metricsMu.Unlock()
	if tq.probeFails != 1 {
		return fmt.Errorf("probeFails=%d, want 1", tq.probeFails)
	}
	return nil
}

func (s *foState) s1NotCoolingNoStrike() error {
	if s.isCooling("s1") {
		return fmt.Errorf("a probe 429 cooled s1")
	}
	return s.ownerNoStrikes("s1")
}

// --- Given: observability ----------------------------------------------------

func (s *foState) countersHappened() error {
	if err := s.s1_429_s2Real(); err != nil {
		return err
	}
	for i := 0; i < 4; i++ { // 4 failovers (each cools s1 once)
		s.landOn("s1")
		if err := s.fundedRelay(); err != nil {
			return err
		}
		if s.lastCode != 200 {
			return fmt.Errorf("failover relay %d = %d %s", i, s.lastCode, s.lastBody)
		}
		s.advance(20 * time.Second)
	}
	for i := 0; i < 2; i++ { // 2 more cooldowns, pinned (no failover); the 2nd leaves s1 cooling
		if err := s.pinnedRelay("s1"); err != nil {
			return err
		}
		if s.lastCode != 429 {
			return fmt.Errorf("pinned relay %d = %d", i, s.lastCode)
		}
		if i == 0 {
			s.advance(20 * time.Second)
		}
	}
	for i := 0; i < 2; i++ { // 2 band-cooling 503s against the cooling s1
		if err := s.pinnedRelay("s1"); err != nil {
			return err
		}
		if s.lastCode != 503 {
			return fmt.Errorf("cooling relay %d = %d %s", i, s.lastCode, s.lastBody)
		}
	}
	return nil
}

func (s *foState) readAdminLive() error {
	r := httptest.NewRequest(http.MethodGet, "/admin/live", nil)
	r.Header.Set("X-Roger-Admin", s.b.adminKey)
	w := httptest.NewRecorder()
	s.b.adminLive(w, r)
	if w.Code != 200 {
		return fmt.Errorf("GET /admin/live = %d: %s", w.Code, w.Body.String())
	}
	s.adminResp = map[string]any{}
	return json.Unmarshal(w.Body.Bytes(), &s.adminResp)
}

func (s *foState) adminCounters(f, c, b string) error {
	routing, _ := s.adminResp["routing"].(map[string]any)
	if routing == nil {
		return fmt.Errorf("/admin/live has no routing block: %v", s.adminResp)
	}
	for k, want := range map[string]string{"relay_failovers": f, "station_cooldowns": c, "band_cooling_503": b} {
		w, _ := strconv.Atoi(want)
		got, _ := routing[k].(float64)
		if int(got) != w {
			return fmt.Errorf("%s=%v, want %d (%v)", k, routing[k], w, routing)
		}
	}
	return nil
}

func (s *foState) adminStationTable() error {
	routing, _ := s.adminResp["routing"].(map[string]any)
	rows, _ := routing["stations"].([]any)
	for _, r := range rows {
		m, _ := r.(map[string]any)
		if m["node"] == s.st("s1").id {
			if v, _ := m["cooling_until"].(float64); v > 0 {
				return nil
			}
		}
	}
	return fmt.Errorf("no per-station row with cooling_until for s1: %v", routing)
}

func (s *foState) tuiRenders() error { return nil }

func (s *foState) stationRowCooling() error {
	// The dial renders /discover: the offer must be ONLINE and carry cooling_until in the
	// future (the seconds remaining the row shows). The glyph itself is pinned in
	// internal/tui (TestBandRowCoolingMarker).
	raw, _ := json.Marshal(s.b.computeDiscover())
	var d struct {
		Offers []map[string]any `json:"offers"`
	}
	_ = json.Unmarshal(raw, &d)
	for _, v := range d.Offers {
		if v["node_id"] == s.st("s1").id {
			if on, _ := v["online"].(bool); !on {
				return fmt.Errorf("a cooling station is shown OFF air: %v", v)
			}
			until, _ := v["cooling_until"].(float64)
			if int64(until) <= s.now().Unix() {
				return fmt.Errorf("offer carries no future cooling_until: %v", v)
			}
			return nil
		}
	}
	return fmt.Errorf("s1 missing from /discover")
}

func (s *foState) bandNeverDark() error {
	res, _ := s.b.computeMarket().(map[string]any)
	views, _ := res["market"].([]marketView)
	for _, v := range views {
		if v.Model == s.model {
			if v.Providers < 1 {
				return fmt.Errorf("market shows %d providers for a cooling-only band", v.Providers)
			}
			return nil
		}
	}
	return fmt.Errorf("model %s vanished from /market because of cooling", s.model)
}

func (s *foState) failoverLogged() error {
	logs := s.logs.String()
	want := "FAILOVER request="
	n := strings.Count(logs, want)
	if n != 1 {
		return fmt.Errorf("%d FAILOVER lines, want 1; logs:\n%s", n, logs)
	}
	line := logs[strings.Index(logs, want):]
	line = line[:strings.IndexByte(line, '\n')]
	if !strings.Contains(line, "from="+s.st("s1").id+" (upstream-throttled) to="+s.st("s2").id) {
		return fmt.Errorf("FAILOVER line %q lacks from=s1 (upstream-throttled) to=s2", line)
	}
	return nil
}

func (s *foState) s1CoolingTenMinutes() error {
	s.b.adminEmails = []string{"founder@example.com"}
	s.only("s1").script429("120")
	for i := 0; i < 6; i++ { // 6 x 120s = 12 minutes cumulative, each behind a real relay
		if err := s.pinnedRelay("s1"); err != nil {
			return err
		}
		if s.lastCode != 429 {
			return fmt.Errorf("relay %d = %d", i, s.lastCode)
		}
		s.advance(130 * time.Second)
	}
	s.b.alertCheckOnce(s.now())
	s.b.alertCheckOnce(s.now())
	return nil
}

func (s *foState) alertFiredOnce() error {
	key := "station_cooling:" + s.st("s1").id
	s.b.alertMu.Lock()
	firing := s.b.alertFiring[key]
	s.b.alertMu.Unlock()
	if !firing {
		return fmt.Errorf("alert %q is not firing", key)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		s.mailMu.Lock()
		n := 0
		var last string
		for _, m := range s.mails {
			if strings.Contains(m, s.st("s1").id) && strings.Contains(m, "cooling") {
				n++
				last = m
			}
		}
		s.mailMu.Unlock()
		if n == 1 {
			if !strings.Contains(last, s.model) || !strings.Contains(last, "6") {
				return fmt.Errorf("alert mail does not name the band and the count: %s", last)
			}
			return nil
		}
		if n > 1 || time.Now().After(deadline) {
			return fmt.Errorf("%d cooling alert mails, want exactly 1", n)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (s *foState) alertClears() error {
	s.advance(61 * time.Minute)
	s.b.alertCheckOnce(s.now())
	key := "station_cooling:" + s.st("s1").id
	s.b.alertMu.Lock()
	firing := s.b.alertFiring[key]
	s.b.alertMu.Unlock()
	if firing {
		return fmt.Errorf("alert %q still firing an hour after the last cooldown", key)
	}
	return nil
}

// --- Given: adversarial ------------------------------------------------------

func (s *foState) cannotSteer() error {
	s.st("s1").script429("")
	if err := s.pinnedRelay("s1"); err != nil {
		return err
	}
	if !s.isCooling("s1") {
		return fmt.Errorf("s1 did not cool on its own 429")
	}
	if s.isCooling("s2") {
		return fmt.Errorf("s1's 429 cooled s2")
	}
	return nil
}

func (s *foState) s1_429Everything() error {
	s.only("s1").script429("")
	return nil
}

func (s *foState) probeRounds(n string) error {
	k, err := strconv.Atoi(n)
	if err != nil {
		return err
	}
	st := s.st("s1")
	for i := 0; i < k; i++ {
		s.b.probeNode(s.b.nodes[st.id], st.model, nextCanary(uint64(i)))
	}
	return nil
}

func (s *foState) quarantined() error {
	if _, ok := s.pickOK("", nil, nil, s.b); ok {
		return fmt.Errorf("a probe-dead station is still picked")
	}
	e, err := s.db.EarningsOf(s.st("s1").id)
	if err != nil {
		return err
	}
	if e != 0 {
		return fmt.Errorf("earnings %.6f, want 0", e)
	}
	return s.ownerNoStrikes("s1")
}

func (s *foState) replayResult() error {
	s.landOn("s1")
	if err := s.fundedRelay(); err != nil {
		return err
	}
	if s.lastCode != 200 {
		return fmt.Errorf("failover relay = %d %s", s.lastCode, s.lastBody)
	}
	st := s.st("s2")
	res := st.postedResults()
	if len(res) == 0 {
		return fmt.Errorf("s2 posted no result to replay")
	}
	wire, _ := json.Marshal(res[len(res)-1])
	s.deliverRaw(st, wire)
	return nil
}

func (s *foState) oneSpendRow() error {
	_, _, spends, _, err := s.holdRows()
	if err != nil {
		return err
	}
	if spends != 1 {
		return fmt.Errorf("%d spend rows, want exactly 1", spends)
	}
	return nil
}

func (s *foState) s1_429NamingItself() error {
	if err := s.twoSamePrice(); err != nil {
		return err
	}
	s.st("s1").scriptStatus(429, `{"error":{"message":"rate limit exceeded on `+s.st("s1").id+`"}}`, nil)
	s.st("s2").scriptReal()
	return nil
}

func (s *foState) bodyExactlyS2() error {
	if s.lastCode != 200 {
		return fmt.Errorf("status %d", s.lastCode)
	}
	if !bytes.Equal(bytes.TrimSpace(s.lastBody), []byte(`{"choices":[{"message":{"role":"assistant","content":"The answer is 4."}}],"usage":{"prompt_tokens":5000,"completion_tokens":20}}`)) {
		return fmt.Errorf("body is not exactly s2's completion: %s", s.lastBody)
	}
	if bytes.Contains(s.lastBody, []byte(s.st("s1").id)) {
		return fmt.Errorf("s1's error body leaked: %s", s.lastBody)
	}
	return nil
}

// --- Given: Sep 7 ------------------------------------------------------------

func (s *foState) sep7Sibling() error {
	s.model = "qwen-3.8-27b"
	// Two healthy, fast stations, the throttling one a little CHEAPER: the router (P2C at
	// equal load routes to the better score; price is the one term serving cannot move)
	// keeps preferring cb, so every one of its 66 refusals is met on the first pick - the
	// worst case for the consumer, who must still see 200s.
	cb := s.standUp("cb", stationOpts{model: s.model, priceIn: 0.9, priceOut: 0.9})
	pw := s.standUp("pw", stationOpts{model: s.model, priceIn: 1, priceOut: 1})
	pw.scriptReal()
	s.b.metricsMu.Lock()
	s.b.success[cb.id], s.b.success[pw.id] = 1.0, 1.0
	s.b.metricsMu.Unlock()
	s.b.mu.Lock()
	s.b.tps[cb.id], s.b.tps[pw.id] = 1e6, 1e6 // both saturate the speed term (in-process serves measure ~1e5 tok/s)
	s.b.mu.Unlock()
	s.ordered = true
	cb.set(func(n int, w http.ResponseWriter, _ *http.Request) {
		if n <= 66 {
			w.WriteHeader(429)
			_, _ = w.Write([]byte(utDefaultBody(429)))
			return
		}
		utRealCompletion(w)
	})
	return nil
}

func (s *foState) relays228() error {
	if err := s.ensureFunded(); err != nil {
		return err
	}
	for i := 0; i < 228; i++ {
		s.relay(false, s.consumerPriv)
		s.advance(16 * time.Second) // the Sep 7 burst ran for an hour; each default cooldown lapses
	}
	return nil
}

func (s *foState) all228OK() error {
	if len(s.codes) != 228 {
		return fmt.Errorf("%d relays, want 228", len(s.codes))
	}
	for i, c := range s.codes {
		if c != 200 {
			return fmt.Errorf("relay %d = %d, want 200", i, c)
		}
	}
	return nil
}

func (s *foState) sep7Receipts() error {
	es, err := s.entries()
	if err != nil {
		return err
	}
	var settled, voided int
	for _, e := range es {
		if e.Cost > 0 {
			settled++
			continue
		}
		if e.Node != s.st("cb").id {
			return fmt.Errorf("a $0 receipt names %s, want only cb", e.Node)
		}
		_, keys, err := s.storedReceipt(e.RequestID)
		if err != nil {
			return err
		}
		if vr, _ := keys["void_reason"].(string); vr != "upstream-throttled" {
			return fmt.Errorf("void_reason %q", vr)
		}
		voided++
	}
	if settled != 228 || voided != 66 {
		return fmt.Errorf("settled=%d voided=%d, want 228/66", settled, voided)
	}
	return nil
}

func (s *foState) sep7Alone() error {
	s.model = "qwen-3.8-27b"
	cb := s.standUp("cb", stationOpts{model: s.model, priceIn: 1, priceOut: 1, ctx: 131072})
	var mu sync.Mutex
	used := map[int64]int{}
	cb.set(func(_ int, w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		tok := len(body) / 4
		minute := s.now().Unix() / 60
		mu.Lock()
		over := used[minute]+tok > 150000
		if !over {
			used[minute] += tok
		}
		mu.Unlock()
		if over {
			w.Header().Set("Retry-After", "15")
			w.WriteHeader(429)
			_, _ = w.Write([]byte(utDefaultBody(429)))
			return
		}
		utRealCompletion(w)
	})
	return nil
}

func (s *foState) relays30Big() error {
	if err := s.ensureFunded(); err != nil {
		return err
	}
	s.tokens = 83000
	for i := 0; i < 30; i++ {
		s.relay(false, s.consumerPriv)
		s.advance(6 * time.Second) // 30 prompts in 3 minutes
	}
	return nil
}

func (s *foState) every429HasRA() error {
	if len(s.non200RA) == 0 {
		return fmt.Errorf("no non-200 responses recorded")
	}
	for i, ok := range s.non200RA {
		if !ok {
			return fmt.Errorf("non-200 response %d carried no Retry-After", i)
		}
	}
	return nil
}

func (s *foState) coolingGets503() error {
	// Every 429 from the upstream must be followed, within its cooldown, by relays that
	// were answered 503 band-cooling WITHOUT an upstream call: the number of consumer
	// non-200s exceeds the number of upstream rejections.
	non200 := 0
	n503 := 0
	for _, c := range s.codes {
		if c != 200 {
			non200++
		}
		if c == 503 {
			n503++
		}
	}
	rej := s.st("cb").rejectedCount()
	if n503 == 0 || non200 <= rej {
		return fmt.Errorf("consumer non-200=%d (503s=%d) vs upstream rejections=%d: the band did not cool", non200, n503, rej)
	}
	return nil
}

func (s *foState) fewerThan10Rejected() error {
	if r := s.st("cb").rejectedCount(); r >= 10 {
		return fmt.Errorf("upstream received %d rejected requests, want < 10", r)
	}
	return nil
}

// --- When --------------------------------------------------------------------

func (s *foState) relayLandsOn(name string) error {
	s.landOn(name)
	return s.fundedRelay()
}

func (s *foState) relayExcluding(name string) error {
	s.landOn("s1")
	s.excludeNodes = s.st(name).id
	return s.fundedRelay()
}

func (s *foState) relayPinned(name string) error {
	s.excludeNodes = ""
	s.advance(cooldownDefault() + time.Second) // the first relay's 429 cooled s1; the pin test wants it dispatchable
	return s.pinnedRelay(name)
}

func (s *foState) confidentialRelay() error {
	s.landOn("s1")
	s.confidential = true
	return s.fundedRelay()
}

func (s *foState) bandRelay() error {
	s.landOn("s1")
	return s.fundedRelay()
}

func (s *foState) grantRelay() error {
	s.landOn("s1")
	s.relay(false, nil)
	if s.lastCode != 200 {
		return fmt.Errorf("grant relay = %d %s", s.lastCode, s.lastBody)
	}
	return nil
}

func (s *foState) anonRelay() error {
	s.landOn("f1")
	for i := 0; i < 5; i++ {
		s.relay(false, s.anonPriv)
		if s.lastCode != 200 {
			return fmt.Errorf("anon relay %d = %d %s", i, s.lastCode, s.lastBody)
		}
	}
	return nil
}

func (s *foState) streamLandsOn(name string) error {
	s.landOn(name)
	return s.fundedStream()
}

func (s *foState) relayHitsS1() error { return s.pinnedRelay("s1") }

func (s *foState) tenRelays() error {
	s.countsBefore = map[string]int{}
	for n, st := range s.stations {
		s.countsBefore[n] = st.upstreamCount()
	}
	for i := 0; i < 10; i++ {
		if err := s.fundedRelay(); err != nil {
			return err
		}
	}
	return nil
}

func (s *foState) instanceBPicks() error {
	s.b2.syncLivenessOnce() // the 5s sync tick: peers merge the shared cooldown set here
	return nil
}

func (s *foState) marketAndDiscover() error { return nil }

func (s *foState) alertChecker() error {
	s.b.adminEmails = []string{"founder@example.com"}
	s.b.alertCheckOnce(s.now())
	return nil
}

// --- Then: responses ---------------------------------------------------------

func (s *foState) is200From(name string) error {
	if s.lastCode != 200 {
		return fmt.Errorf("status %d, want 200 (%s)", s.lastCode, s.lastBody)
	}
	if !bytes.Contains(s.lastBody, []byte("The answer is 4.")) && !bytes.Contains(s.lastBody, []byte("from "+name)) {
		return fmt.Errorf("body is not %s's completion: %s", name, s.lastBody)
	}
	if p := s.lastHdr.Get("X-RogerAI-Provider"); p != s.st(name).id {
		return fmt.Errorf("X-RogerAI-Provider=%q, want %s", p, s.st(name).id)
	}
	return nil
}

func (s *foState) providerIs(name string) error {
	if p := s.lastHdr.Get("X-RogerAI-Provider"); p != s.st(name).id {
		return fmt.Errorf("X-RogerAI-Provider=%q, want %s", p, s.st(name).id)
	}
	return nil
}

func (s *foState) twoReceipts(reason string) error {
	vr, _, err := s.voidReasonOf("s1")
	if err != nil {
		return err
	}
	if vr != reason {
		return fmt.Errorf("s1 void_reason=%q, want %q", vr, reason)
	}
	es, err := s.entriesOf("s2")
	if err != nil {
		return err
	}
	if len(es) != 1 || es[0].Cost <= 0 {
		return fmt.Errorf("s2 receipts %+v, want one settled with cost > 0", es)
	}
	return nil
}

func (s *foState) chargedOnceS2() error {
	es, err := s.entriesOf("s2")
	if err != nil {
		return err
	}
	if len(es) != 1 {
		return fmt.Errorf("%d s2 receipts", len(es))
	}
	bal, err := s.balance()
	if err != nil {
		return err
	}
	if err := feApprox(bal, s.start-es[0].Cost); err != nil {
		return fmt.Errorf("balance %.6f, want start %.6f - cost %.6f", bal, s.start, es[0].Cost)
	}
	return s.oneSpendRow()
}

func (s *foState) s1VoidReason(reason string) error {
	vr, _, err := s.voidReasonOf("s1")
	if err != nil {
		return err
	}
	if vr != reason {
		return fmt.Errorf("s1 void_reason=%q, want %q", vr, reason)
	}
	return nil
}

func (s *foState) isStatusUpstreamBody(status string) error {
	n, _ := strconv.Atoi(status)
	if s.lastCode != n {
		return fmt.Errorf("status %d, want %d (%s)", s.lastCode, n, s.lastBody)
	}
	if !bytes.Contains(s.lastBody, []byte(`"error"`)) {
		return fmt.Errorf("upstream body not passed through: %s", s.lastBody)
	}
	if s.st("s1").upstreamCount() != 1 {
		return fmt.Errorf("s1 received %d requests, want 1", s.st("s1").upstreamCount())
	}
	vr, _, err := s.voidReasonOf("s1")
	if err != nil {
		return err
	}
	if vr != "upstream-error" {
		return fmt.Errorf("s1 void_reason=%q, want upstream-error", vr)
	}
	return nil
}

func (s *foState) receivedNothing(name string) error {
	if n := s.st(name).upstreamCount(); n != 0 {
		return fmt.Errorf("%s received %d request(s), want 0", name, n)
	}
	return nil
}

func (s *foState) exactlyNStations(n string) error {
	want, _ := strconv.Atoi(n)
	got := 0
	for _, st := range s.stations {
		if st.upstreamCount() > 0 {
			got++
		}
	}
	if got != want {
		return fmt.Errorf("%d stations received the request, want %d", got, want)
	}
	return nil
}

func (s *foState) is429LastBodyRA() error {
	if s.lastCode != 429 || !bytes.Contains(s.lastBody, []byte("rate limit exceeded")) {
		return fmt.Errorf("status %d body %s, want 429 + upstream body", s.lastCode, s.lastBody)
	}
	if s.lastHdr.Get("Retry-After") == "" {
		return fmt.Errorf("no Retry-After on the final 429")
	}
	return nil
}

func (s *foState) threeVoidedZero() error {
	es, err := s.entries()
	if err != nil {
		return err
	}
	voided := 0
	for _, e := range es {
		if e.Cost == 0 {
			voided++
		}
	}
	if voided != 3 {
		return fmt.Errorf("%d voided receipts, want 3", voided)
	}
	return s.chargedZero()
}

func (s *foState) chargedZero() error {
	bal, err := s.balance()
	if err != nil {
		return err
	}
	if err := feApprox(bal, s.start); err != nil {
		return fmt.Errorf("consumer charged: balance %.6f, start %.6f", bal, s.start)
	}
	return nil
}

func (s *foState) eachExactlyOne() error {
	for _, n := range []string{"s1", "s2"} {
		if c := s.st(n).upstreamCount(); c != 1 {
			return fmt.Errorf("%s received %d, want 1", n, c)
		}
	}
	return nil
}

func (s *foState) isStatus(status string) error {
	n, _ := strconv.Atoi(status)
	if s.lastCode != n {
		return fmt.Errorf("status %d, want %d (%s)", s.lastCode, n, s.lastBody)
	}
	return nil
}

func (s *foState) noSecondAttempt() error {
	if err := s.receivedNothing("s2"); err != nil {
		return err
	}
	if s.relayEnd.Sub(s.relayStart) > 3*time.Second {
		return fmt.Errorf("relay took %s, past the (scaled) deadline", s.relayEnd.Sub(s.relayStart))
	}
	return nil
}

func (s *foState) firstFailureVoided() error {
	if s.lastCode != 500 && s.lastCode != 504 {
		return fmt.Errorf("status %d, want the 500/504 first-failure outcome", s.lastCode)
	}
	return s.chargedZero()
}

func (s *foState) failoverTo(to string) error {
	if err := s.is200From(to); err != nil {
		return err
	}
	if s.st("s1").upstreamCount() != 1 {
		return fmt.Errorf("s1 received %d, want 1", s.st("s1").upstreamCount())
	}
	return nil
}

func (s *foState) failoverToNever(to, never string) error {
	if s.freq != "" {
		return s.bandFailover(to, never)
	}
	if to == "f2" {
		return s.anonFailover(to, never)
	}
	if err := s.failoverTo(to); err != nil {
		return err
	}
	return s.receivedNothing(never)
}

func (s *foState) pinnedNoFailover() error {
	if s.lastCode != 429 || s.lastHdr.Get("Retry-After") == "" {
		return fmt.Errorf("pinned relay = %d Retry-After %q, want 429 with Retry-After", s.lastCode, s.lastHdr.Get("Retry-After"))
	}
	if s.st("s1").upstreamCount() != 2 { // the earlier excluded relay + this pinned one
		return fmt.Errorf("s1 received %d, want 2", s.st("s1").upstreamCount())
	}
	return s.receivedNothing("s2")
}

func (s *foState) s2NotTried429() error {
	if err := s.receivedNothing("s2"); err != nil {
		return err
	}
	return s.is429WithRA()
}

func (s *foState) is429WithRA() error {
	if s.lastCode != 429 || s.lastHdr.Get("Retry-After") == "" {
		return fmt.Errorf("status %d Retry-After %q, want 429 with Retry-After (%s)", s.lastCode, s.lastHdr.Get("Retry-After"), s.lastBody)
	}
	return nil
}

func (s *foState) bandFailover(to, never string) error {
	// A private band is ONE station in this codebase (Band.NodeID; resolveFreqAllow admits
	// exactly that node), so the freq-routed relay cannot leave the band: it must not reach
	// the public s3, and with s1 the band's only station it answers 429 + Retry-After.
	if err := s.receivedNothing(never); err != nil {
		return err
	}
	if s.st("s1").upstreamCount() != 1 {
		return fmt.Errorf("s1 received %d, want 1", s.st("s1").upstreamCount())
	}
	if err := s.is429WithRA(); err != nil {
		return err
	}
	// The routing filter itself, for a hypothetical two-station band: admitting {s1,s2}
	// and excluding the failed s1 yields s2, never the public s3.
	allow := map[string]bool{s.st("s1").id: true, s.st(to).id: true}
	n, ok := s.pickOK("", map[string]bool{s.st("s1").id: true}, allow, s.b)
	if !ok || n.NodeID != s.st(to).id {
		return fmt.Errorf("band re-pick = %s ok=%v, want %s", n.NodeID, ok, s.st(to).id)
	}
	return nil
}

func (s *foState) oneAttempt429RA() error {
	if err := s.is429WithRA(); err != nil {
		return err
	}
	if s.st("s1").upstreamCount() != 1 {
		return fmt.Errorf("s1 received %d, want 1", s.st("s1").upstreamCount())
	}
	return s.receivedNothing("s2")
}

// --- Then: money -------------------------------------------------------------

func (s *foState) oneHoldCovers2() error {
	holds, _, _, amt, err := s.holdRows()
	if err != nil {
		return err
	}
	if holds != 1 {
		return fmt.Errorf("%d hold rows, want exactly 1", holds)
	}
	reg := s.b.nodes[s.st("s2").id]
	want := estimateMaxCost(s.body(false), 2, 2, reg.Offers[0].Ctx)
	if amt+1e-9 < want {
		return fmt.Errorf("hold %.6f < max cost at 2.00/2.00 %.6f", amt, want)
	}
	return nil
}

func (s *foState) oneReleaseOneSpend() error {
	_, releases, spends, _, err := s.holdRows()
	if err != nil {
		return err
	}
	if releases != 1 || spends != 1 {
		return fmt.Errorf("releases=%d spends=%d, want 1/1", releases, spends)
	}
	return nil
}

func (s *foState) balanceStartMinusS2() error {
	es, err := s.entriesOf("s2")
	if err != nil {
		return err
	}
	if len(es) != 1 {
		return fmt.Errorf("%d s2 receipts", len(es))
	}
	bal, err := s.balance()
	if err != nil {
		return err
	}
	return feApprox(bal, s.start-es[0].Cost)
}

func (s *foState) s2NotTriedFunds() error { return s.receivedNothing("s2") }

func (s *foState) is429RAChargedZero() error {
	if err := s.is429WithRA(); err != nil {
		return err
	}
	return s.chargedZero()
}

func (s *foState) chainedVoids() error {
	for _, n := range []string{"s1", "s2"} {
		vr, req, err := s.voidReasonOf(n)
		if err != nil {
			return err
		}
		if vr == "" {
			return fmt.Errorf("%s receipt is not voided", n)
		}
		rec, _, err := s.storedReceipt(req)
		if err != nil {
			return err
		}
		head, err := s.db.ChainHead(s.st(n).id)
		if err != nil {
			return err
		}
		if head != rec.Hash() {
			return fmt.Errorf("%s chain head %s != voided receipt hash %s (not chained)", n, head, rec.Hash())
		}
		if rec.PrevHash != "" {
			return fmt.Errorf("%s first receipt carries prev_hash %q, want the chain genesis", n, rec.PrevHash)
		}
	}
	return nil
}

func (s *foState) s3Settled() error {
	es, err := s.entriesOf("s3")
	if err != nil {
		return err
	}
	if len(es) != 1 || es[0].Cost <= 0 {
		return fmt.Errorf("s3 receipts %+v", es)
	}
	return nil
}

func (s *foState) lineage123() error {
	ids := map[string]string{}
	for _, n := range []string{"s1", "s2", "s3"} {
		es, err := s.entriesOf(n)
		if err != nil {
			return err
		}
		if len(es) != 1 {
			return fmt.Errorf("%s has %d receipts", n, len(es))
		}
		ids[n] = es[0].RequestID
	}
	base := ids["s1"]
	if !strings.HasPrefix(ids["s2"], base+"-") || !strings.HasPrefix(ids["s3"], base+"-") {
		return fmt.Errorf("attempt ids %v do not share the request lineage %s", ids, base)
	}
	if ids["s2"] != base+"-2" || ids["s3"] != base+"-3" {
		return fmt.Errorf("attempt ids %v, want %s-2 and %s-3", ids, base, base)
	}
	return nil
}

func (s *foState) ownerNoEarnNoStrike(name string) error {
	earns, err := s.db.LedgerOf(s.st(name).acct, []string{store.KindEarn}, 100)
	if err != nil {
		return err
	}
	if len(earns) != 0 {
		return fmt.Errorf("%s's owner has %d earn rows", name, len(earns))
	}
	return s.ownerNoStrikes(name)
}

func (s *foState) ownerNoStrikes(name string) error {
	st, err := s.strikesOf(name)
	if err != nil {
		return err
	}
	if len(st) != 0 {
		return fmt.Errorf("%s's owner has %d strike(s): %+v", name, len(st), st)
	}
	return nil
}

func (s *foState) ownerOneEarn(name string) error {
	earns, err := s.db.LedgerOf(s.st(name).acct, []string{store.KindEarn}, 100)
	if err != nil {
		return err
	}
	if len(earns) != 1 {
		return fmt.Errorf("%s's owner has %d earn rows, want 1", name, len(earns))
	}
	return nil
}

func (s *foState) grantDebitedOnce() error {
	u, err := s.db.GrantUsageOf(s.grantID, time.Now())
	if err != nil {
		return err
	}
	if u.DayTokens != 5020 {
		return fmt.Errorf("grant day tokens %d, want 5020 (the served attempt only)", u.DayTokens)
	}
	if s.st("s1").upstreamCount() < 1 || s.st("s2").upstreamCount() != 1 {
		return fmt.Errorf("s1=%d s2=%d upstream requests", s.st("s1").upstreamCount(), s.st("s2").upstreamCount())
	}
	return nil
}

func (s *foState) anonFailover(to, never string) error {
	if err := s.receivedNothing(never); err != nil {
		return err
	}
	if s.st(to).upstreamCount() != 5 {
		return fmt.Errorf("%s served %d, want 5", to, s.st(to).upstreamCount())
	}
	return s.providerIs(to)
}

// --- Then: streaming ---------------------------------------------------------

func (s *foState) sseOnlyS2() error {
	if s.lastCode != 200 {
		return fmt.Errorf("stream status %d (%s)", s.lastCode, s.lastBody)
	}
	if !bytes.Contains(s.lastBody, []byte("from s2")) {
		return fmt.Errorf("stream lacks s2's chunks: %s", s.lastBody)
	}
	if bytes.Contains(s.lastBody, []byte("rate limit")) || bytes.Contains(s.lastBody, []byte("from s1")) {
		return fmt.Errorf("stream carries s1's bytes: %s", s.lastBody)
	}
	return nil
}

func (s *foState) firstChunkFast() error {
	s.lastRec.mu.Lock()
	fd := s.lastRec.firstData
	s.lastRec.mu.Unlock()
	if fd.IsZero() {
		return fmt.Errorf("no data frame reached the consumer")
	}
	if d := fd.Sub(s.relayStart); d > 2*time.Second {
		return fmt.Errorf("first chunk after %s (s1 fails instantly; s2's TTFT is ~0)", d)
	}
	return nil
}

func (s *foState) streamEndsAsToday() error {
	if s.lastCode != 200 || !bytes.Contains(s.lastBody, []byte("chunk2 from s1")) {
		return fmt.Errorf("partial stream not delivered: %d %s", s.lastCode, s.lastBody)
	}
	return nil
}

func (s *foState) single200() error {
	if s.lastCode != 200 {
		return fmt.Errorf("status %d", s.lastCode)
	}
	s.lastRec.mu.Lock()
	st := append([]int(nil), s.lastRec.statuses...)
	s.lastRec.mu.Unlock()
	if len(st) != 1 || st[0] != 200 {
		return fmt.Errorf("statuses written %v, want exactly one 200", st)
	}
	if bytes.Contains(s.lastBody, []byte("rate limit")) {
		return fmt.Errorf("the 429 leaked into the stream: %s", s.lastBody)
	}
	return nil
}

func (s *foState) stream429RA(n string) error {
	if s.lastCode != 429 {
		return fmt.Errorf("stream status %d, want 429 (%s)", s.lastCode, s.lastBody)
	}
	if got := s.lastHdr.Get("Retry-After"); got != n {
		return fmt.Errorf("Retry-After %q, want %s", got, n)
	}
	return nil
}

// --- Then: cooldown ----------------------------------------------------------

func (s *foState) coolingFor(n string) error {
	want, _ := strconv.Atoi(n)
	if !s.isCooling("s1") {
		return fmt.Errorf("s1 is not cooling")
	}
	ra, err := s.bandCoolingRetryAfter("s1")
	if err != nil {
		return err
	}
	if ra != want && ra != want-1 { // the HTTP-date form may round a second down
		return fmt.Errorf("s1 cooling with Retry-After %d, want %d", ra, want)
	}
	return nil
}

func (s *foState) sharedKeyTTL(n string) error {
	key := "rogerai:cool:" + s.st("s1").id
	if !s.mr.Exists(key) {
		return fmt.Errorf("shared store has no %s", key)
	}
	want, _ := strconv.Atoi(n)
	ttl := s.mr.TTL(key)
	if ttl <= 0 || ttl > time.Duration(want)*time.Second || ttl < time.Duration(want-2)*time.Second {
		return fmt.Errorf("TTL of %s = %s, want about %ds", key, ttl, want)
	}
	return nil
}

func (s *foState) allLandOn(name string) error {
	if got := s.st(name).upstreamCount() - s.countsBefore[name]; got != 10 {
		return fmt.Errorf("%s served %d of 10", name, got)
	}
	for n, st := range s.stations {
		if got := st.upstreamCount() - s.countsBefore[n]; n != name && got != 0 {
			return fmt.Errorf("%s received %d while cooling", n, got)
		}
	}
	for _, c := range s.codes[len(s.codes)-10:] {
		if c != 200 {
			return fmt.Errorf("a relay while s1 cooled returned %d", c)
		}
	}
	return nil
}

func (s *foState) eligibleAgain() error {
	if s.isCooling("s1") {
		return fmt.Errorf("s1 still filtered after its cooldown expired")
	}
	return nil
}

func (s *foState) bSkipsS1() error {
	n, ok := s.pickOK("", nil, nil, s.b2)
	if !ok {
		return fmt.Errorf("instance B picked nothing")
	}
	if n.NodeID == s.st("s1").id {
		return fmt.Errorf("instance B picked the cooling s1")
	}
	if _, ok := s.pickOK(s.st("s1").id, nil, nil, s.b2); ok {
		return fmt.Errorf("instance B still admits s1 when pinned")
	}
	return nil
}

func (s *foState) fallbackOnce() error {
	if !s.isCooling("s1") {
		return fmt.Errorf("instance A did not cool s1 per-instance")
	}
	if n := strings.Count(s.logs.String(), "cooldown: shared store unavailable"); n != 1 {
		return fmt.Errorf("%d fallback log lines, want exactly 1; logs:\n%s", n, s.logs.String())
	}
	b2 := s.instanceB()
	if _, ok := s.pickOK(s.st("s1").id, nil, nil, b2); !ok {
		return fmt.Errorf("instance B cannot pick s1 although the shared store is down")
	}
	return nil
}

func (s *foState) notCoolingSuccessDropped() error {
	if s.isCooling("s1") {
		return fmt.Errorf("a 503 cooled s1")
	}
	s.b.metricsMu.Lock()
	got, seen := s.b.success[s.st("s1").id]
	s.b.metricsMu.Unlock()
	if !seen || !(got < 1.0) {
		return fmt.Errorf("success average %.3f seen=%v, want dropped below 1", got, seen)
	}
	return nil
}

func (s *foState) extendNotStack() error {
	ra, err := s.bandCoolingRetryAfter("s1")
	if err != nil {
		return err
	}
	if ra > 10 || ra < 9 {
		return fmt.Errorf("cooling Retry-After %d after three 429s, want 10 (not 30)", ra)
	}
	return nil
}

func (s *foState) zeroStrikesNotHeld(name string) error {
	if err := s.ownerNoStrikes(name); err != nil {
		return err
	}
	h, err := s.heldOwner(name)
	if err != nil {
		return err
	}
	if h {
		return fmt.Errorf("%s's owner is held", name)
	}
	return nil
}

func (s *foState) coolsAtMost120() error {
	if ra := s.lastHdr.Get("Retry-After"); ra != "120" {
		return fmt.Errorf("consumer Retry-After %q, want the 120 cap", ra)
	}
	ra, err := s.bandCoolingRetryAfter("s1")
	if err != nil {
		return err
	}
	if ra > 120 {
		return fmt.Errorf("cooling for %ds, want at most 120", ra)
	}
	return nil
}

func (s *foState) notCoolingNoRA() error {
	if s.lastCode != 200 {
		return fmt.Errorf("status %d", s.lastCode)
	}
	if s.lastHdr.Get("Retry-After") != "" {
		return fmt.Errorf("a 200 carries Retry-After %q", s.lastHdr.Get("Retry-After"))
	}
	if s.isCooling("s1") {
		return fmt.Errorf("a 200 with retry_after_sec cooled the station")
	}
	return nil
}

// --- Then: only station cooling ----------------------------------------------

func (s *foState) is503BandCooling(secs string) error {
	if s.lastCode != 503 {
		return fmt.Errorf("status %d (%s)", s.lastCode, s.lastBody)
	}
	want := fmt.Sprintf(`{"error":{"message":"band cooling - the station serving %s was rate limited upstream, retry after %ss"}}`, s.model, secs)
	if string(bytes.TrimSpace(s.lastBody)) != want {
		return fmt.Errorf("body %s, want %s", bytes.TrimSpace(s.lastBody), want)
	}
	return nil
}

func (s *foState) retryAfterIs(n string) error {
	if got := s.lastHdr.Get("Retry-After"); got != n {
		return fmt.Errorf("Retry-After %q, want %s", got, n)
	}
	return nil
}

func (s *foState) noHoldReceiptCall() error {
	h, _, _, _, err := s.holdRows()
	if err != nil {
		return err
	}
	rc, err := s.receiptCount()
	if err != nil {
		return err
	}
	total := 0
	for _, st := range s.stations {
		total += st.upstreamCount()
	}
	if h != s.snapHolds || rc != s.snapReceipts || total != s.snapUpstream {
		return fmt.Errorf("holds %d->%d receipts %d->%d upstream %d->%d: the cooling 503 did work", s.snapHolds, h, s.snapReceipts, rc, s.snapUpstream, total)
	}
	return nil
}

func (s *foState) is503RA(n string) error {
	if s.lastCode != 503 {
		return fmt.Errorf("status %d (%s)", s.lastCode, s.lastBody)
	}
	return s.retryAfterIs(n)
}

func (s *foState) marketOnAirCooling() error {
	if err := s.bandNeverDark(); err != nil {
		return err
	}
	return s.stationRowCooling()
}

func (s *foState) noProvidersAlert() error {
	key := "noproviders:" + s.model
	s.b.alertMu.Lock()
	firing := s.b.alertFiring[key]
	s.b.alertMu.Unlock()
	if firing {
		return fmt.Errorf("%q fired for a cooling-only band", key)
	}
	return nil
}

func (s *foState) dispatchedTo(name string) error {
	if s.lastCode != 200 || s.st(name).upstreamCount() != 2 { // the cooling 429 + this one
		return fmt.Errorf("status %d, %s received %d", s.lastCode, name, s.st(name).upstreamCount())
	}
	return nil
}

// --- Then: Retry-After -------------------------------------------------------

func (s *foState) is429BodyRA(n string) error {
	if err := s.is429LastBodyRA(); err != nil {
		return err
	}
	return s.retryAfterIs(n)
}

func (s *foState) hasRA(n string) error { return s.retryAfterIs(n) }

func (s *foState) noRA() error {
	if s.lastCode != 200 {
		return fmt.Errorf("status %d", s.lastCode)
	}
	if v := s.lastHdr.Get("Retry-After"); v != "" {
		return fmt.Errorf("a 200 carries Retry-After %q", v)
	}
	return nil
}

func TestUpstreamFailoverBDD(t *testing.T) {
	st := &foState{t: t, logs: &utLog{}}
	prev := log.Writer()
	log.SetOutput(st.logs)
	t.Cleanup(func() {
		log.SetOutput(prev)
		st.teardown()
	})
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				return ctx, st.reset()
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				st.teardown()
				return ctx, nil
			})
			sc.Step(`^a broker with a real store and a shared store$`, st.background)
			sc.Step(`^scripted upstreams behind real stations serving "m"$`, st.background)

			// 1. failover
			sc.Step(`^stations "s1" and "s2" serve "m" at the same price$`, st.twoSamePrice)
			sc.Step(`^a funded consumer relays a prompt \(non-stream\) and the pick lands on "([^"]*)"$`, st.relayLandsOn)
			sc.Step(`^the response is 200 with "([^"]*)"'s completion$`, st.is200From)
			sc.Step(`^X-RogerAI-Provider names "([^"]*)"$`, st.providerIs)
			sc.Step(`^two receipts exist: "s1" voided with void_reason "([^"]*)", "s2" settled with cost > 0$`, st.twoReceipts)
			sc.Step(`^the consumer was charged exactly "s2"'s metered cost, once$`, st.chargedOnceS2)
			sc.Step(`^stations "s1" and "s2" serve "m"$`, st.twoSamePrice)
			sc.Step(`^"s1"'s upstream (returns \d+|returns 200 with an empty completion|is unreachable \(station posts 502\)) and "s2"'s returns a real completion$`, st.s1FailureS2Real)
			sc.Step(`^a funded consumer relays and the pick lands on "([^"]*)"$`, st.relayLandsOn)
			sc.Step(`^the response is 200 from "([^"]*)"$`, st.is200From)
			sc.Step(`^"s1"'s receipt is voided with void_reason "([^"]*)"$`, st.s1VoidReason)
			sc.Step(`^"s1"'s upstream returns (\d+) with (\{.*\})$`, st.s1StatusBody)
			sc.Step(`^the response is (\d+) with the upstream body \(one attempt, voided as today\)$`, st.isStatusUpstreamBody)
			sc.Step(`^"([^"]*)"'s upstream received nothing$`, st.receivedNothing)
			sc.Step(`^stations "s1", "s2", "s3", "s4" serve "m" and all upstreams return 429$`, st.fourAll429)
			sc.Step(`^a funded consumer relays$`, st.fundedRelay)
			sc.Step(`^exactly (\d+) stations received the request$`, st.exactlyNStations)
			sc.Step(`^the response is 429 with the last upstream body and a Retry-After$`, st.is429LastBodyRA)
			sc.Step(`^three voided receipts exist and the consumer was charged 0$`, st.threeVoidedZero)
			sc.Step(`^stations "s1" and "s2" serve "m" and both upstreams return 500$`, st.twoBoth500)
			sc.Step(`^"s1" and "s2" each received exactly one request$`, st.eachExactlyOne)
			sc.Step(`^the response is (\d+)$`, st.isStatus)
			sc.Step(`^"s1"'s upstream holds the request for 85 seconds then returns 500$`, st.s1Holds85s)
			sc.Step(`^a funded consumer relays with the 90 second relay deadline$`, st.fundedRelay)
			sc.Step(`^no second attempt is started with less than the minimum attempt budget remaining$`, st.noSecondAttempt)
			sc.Step(`^the response is the timeout/first-failure outcome as today, voided$`, st.firstFailureVoided)
			sc.Step(`^stations "s1", "s2", "s3" serve "m" and "s1" 429s$`, st.threeS1_429)
			sc.Step(`^a funded consumer relays with X-Roger-Exclude-Nodes "([^"]*)"$`, st.relayExcluding)
			sc.Step(`^the failover goes to "([^"]*)", never "([^"]*)"$`, st.failoverToNever)
			sc.Step(`^a funded consumer relays with X-Roger-Node "([^"]*)"$`, st.relayPinned)
			sc.Step(`^a pinned request does NOT fail over: the response is 429 with Retry-After$`, st.pinnedNoFailover)
			sc.Step(`^"s1" \(cheap\) 429s and "s2" is priced above the consumer's X-Roger-Max-Price$`, st.cheapS1PriceyS2)
			sc.Step(`^"s2" is not tried and the response is 429 with Retry-After$`, st.s2NotTried429)
			sc.Step(`^"s1" \(confidential\) 429s, "s2" is confidential, "s3" is not$`, st.confidentialTrio)
			sc.Step(`^a confidential-only relay is made$`, st.confidentialRelay)
			sc.Step(`^the failover goes to "([^"]*)"$`, st.failoverTo)
			sc.Step(`^"s1" and "s2" are on private band B and "s3" is public; "s1" 429s$`, st.privateBand)
			sc.Step(`^a band-B relay is made$`, st.bandRelay)
			sc.Step(`^ROGERAI_RELAY_FAILOVER is "([^"]*)"$`, st.failoverKnob)
			sc.Step(`^"s1" 429s and "s2" is healthy$`, st.s1_429_s2Healthy)
			sc.Step(`^a funded consumer relays and lands on "([^"]*)"$`, st.relayLandsOn)
			sc.Step(`^the response is 429 \(one attempt\) with Retry-After$`, st.oneAttempt429RA)

			// 2. money
			sc.Step(`^"s1" at 1\.00/1\.00 429s and "s2" at 2\.00/2\.00 serves$`, st.s1At1S2At2)
			sc.Step(`^exactly one hold row exists, at least the max cost at 2\.00/2\.00$`, st.oneHoldCovers2)
			sc.Step(`^one hold_release and one spend for "s2"'s cost exist$`, st.oneReleaseOneSpend)
			sc.Step(`^the consumer's balance equals start minus "s2"'s cost$`, st.balanceStartMinusS2)
			sc.Step(`^"s1" at 1\.00/1\.00 429s and "s2" at 50\.00/50\.00 serves$`, st.s1At1S2At50)
			sc.Step(`^the consumer's balance covers "s1" but not "s2"$`, st.balanceCoversS1NotS2)
			sc.Step(`^"s2" is not tried \(insufficient funds for its max cost\)$`, st.s2NotTriedFunds)
			sc.Step(`^the response is 429 with Retry-After and the consumer was charged 0$`, st.is429RAChargedZero)
			sc.Step(`^"s1" 429s, "s2" 500s, "s3" serves$`, st.s1_429_s2_500_s3Serves)
			sc.Step(`^"s1" and "s2" each have a voided receipt chained to their prev_hash$`, st.chainedVoids)
			sc.Step(`^"s3" has a settled receipt$`, st.s3Settled)
			sc.Step(`^all three share the consumer's request id lineage \(attempt 1, 2, 3\)$`, st.lineage123)
			sc.Step(`^"s1" 429s and "s2" serves$`, st.s1_429_s2Serves)
			sc.Step(`^"([^"]*)"'s owner has no earn row and no strike$`, st.ownerNoEarnNoStrike)
			sc.Step(`^"([^"]*)"'s owner has one pending earn row$`, st.ownerOneEarn)
			sc.Step(`^a grant with a daily token cap and "s1" 429s, "s2" serves$`, st.grantWithCap)
			sc.Step(`^a grant relay fails over$`, st.grantRelay)
			sc.Step(`^the grant's cap is debited once, for the served attempt only$`, st.grantDebitedOnce)
			sc.Step(`^free stations "f1" \(429s\) and "f2" and a paid station "p1"$`, st.freeAndPaid)
			sc.Step(`^an anonymous relay is made$`, st.anonRelay)

			// 3. streaming
			sc.Step(`^"s1" 429s and "s2" streams a completion$`, st.s1_429_s2Streams)
			sc.Step(`^a funded consumer relays with "stream": true and lands on "([^"]*)"$`, st.streamLandsOn)
			sc.Step(`^the SSE stream carries only "s2"'s chunks$`, st.sseOnlyS2)
			sc.Step(`^the first chunk arrives within "s1"'s failure time plus "s2"'s TTFT$`, st.firstChunkFast)
			sc.Step(`^"s1" streams two chunks then dies mid-stream, "s2" is healthy$`, st.s1DiesMidStream)
			sc.Step(`^a funded consumer relays with "stream": true$`, func() error { st.landOn("s1"); return st.fundedStream() })
			sc.Step(`^the stream ends as today \(partial output, settled per today's mid-stream rule\)$`, st.streamEndsAsToday)
			sc.Step(`^the consumer receives a single 200 response \(no 429 leaked before the failover\)$`, st.single200)
			sc.Step(`^"s1" and "s2" both 429 with Retry-After (\d+)$`, st.both429RetryAfter)
			sc.Step(`^the response is 429 \(not a 200 with an error event\) with Retry-After (\d+)$`, st.stream429RA)

			// 4. cooldown
			sc.Step(`^"s1"'s upstream returns 429 with "Retry-After: (.+)"$`, st.s1_429RetryAfter)
			sc.Step(`^a relay hits "s1"$`, st.relayHitsS1)
			sc.Step(`^"s1" is cooling for (\d+) seconds$`, st.coolingFor)
			sc.Step(`^"s1" cools for (\d+) seconds$`, st.coolingFor)
			sc.Step(`^the shared store holds rogerai:cool:s1 with a TTL of about (\d+) seconds$`, st.sharedKeyTTL)
			sc.Step(`^"s1"'s upstream returns 429 with no Retry-After$`, st.s1_429NoRetryAfter)
			sc.Step(`^"s1" is cooling and "s2" is healthy$`, st.s1CoolingS2Healthy)
			sc.Step(`^10 relays are made for "m"$`, st.tenRelays)
			sc.Step(`^all 10 land on "([^"]*)"$`, st.allLandOn)
			sc.Step(`^"s1" cooled for (\d+) seconds$`, st.s1CooledFor)
			sc.Step(`^(\d+) seconds pass$`, st.secondsPass)
			sc.Step(`^"s1" is eligible in pickFor again$`, st.eligibleAgain)
			sc.Step(`^two broker instances share the store and "s1" cooled on instance A$`, st.twoInstancesS1Cooled)
			sc.Step(`^instance B picks for "m"$`, st.instanceBPicks)
			sc.Step(`^it skips "s1"$`, st.bSkipsS1)
			sc.Step(`^the shared store is unreachable$`, st.sharedUnreachable)
			sc.Step(`^"s1" 429s on instance A$`, st.s1_429OnA)
			sc.Step(`^instance A skips "s1" and logs the fallback once; instance B may still pick it$`, st.fallbackOnce)
			sc.Step(`^"s1"'s upstream returns 503$`, st.s1_503)
			sc.Step(`^"s1" is not cooling and its success average dropped as today$`, st.notCoolingSuccessDropped)
			sc.Step(`^"s1" 429s with Retry-After 10 three times in a row$`, st.s1_429ThreeTimes)
			sc.Step(`^"s1"'s cooling_until is 10 seconds after the LAST 429, not 30$`, st.extendNotStack)
			sc.Step(`^"s1" cooled 20 times today$`, st.s1Cooled20Times)
			sc.Step(`^"([^"]*)"'s owner has zero strikes and is not held$`, st.zeroStrikesNotHeld)
			sc.Step(`^a station posts a JobResult with status 429 and retry_after_sec (\d+)$`, st.forged429)
			sc.Step(`^it cools for 120 seconds at most$`, st.coolsAtMost120)
			sc.Step(`^a station posts a 200 JobResult with retry_after_sec (\d+)$`, st.forged200)
			sc.Step(`^the station is not cooling and the consumer sees no Retry-After$`, st.notCoolingNoRA)

			// 5. only station cooling
			sc.Step(`^"s1" is the only station for "m" and is cooling for (\d+) more seconds$`, st.onlyS1CoolingFor)
			sc.Step(`^the response is 503 \{"error":\{"message":"band cooling - the station serving m was rate limited upstream, retry after (\d+)s"\}\}$`, st.is503BandCooling)
			sc.Step(`^Retry-After is (\d+)$`, st.retryAfterIs)
			sc.Step(`^no hold, no receipt, no upstream call$`, st.noHoldReceiptCall)
			sc.Step(`^"s1" cools for 20s and "s2" for 5s and nothing else serves "m"$`, st.s1_20_s2_5)
			sc.Step(`^the response is 503 with Retry-After (\d+)$`, st.is503RA)
			sc.Step(`^"s1" is the only station for "m" and is cooling$`, st.onlyS1Cooling)
			sc.Step(`^a client reads /market and /discover$`, st.marketAndDiscover)
			sc.Step(`^"m" shows 1 provider, online true, and the offer carries "cooling_until"$`, st.marketOnAirCooling)
			sc.Step(`^the alert checker runs$`, st.alertChecker)
			sc.Step(`^no "noproviders:m" alert fires$`, st.noProvidersAlert)
			sc.Step(`^"s1" is the only station and its cooldown just expired$`, st.onlyS1CooldownExpired)
			sc.Step(`^it is dispatched to "([^"]*)"$`, st.dispatchedTo)

			// 6. Retry-After
			sc.Step(`^every station for "m" 429s, the last with "Retry-After: (\d+)"$`, st.all429LastRA)
			sc.Step(`^the response is 429 with the upstream body and Retry-After: (\d+)$`, st.is429BodyRA)
			sc.Step(`^every station for "m" 429s with no Retry-After$`, st.all429NoRA)
			sc.Step(`^the response has Retry-After: (\d+)$`, st.hasRA)
			sc.Step(`^every station for "m" 503s, the last with "Retry-After: (\d+)"$`, st.all503LastRA)
			sc.Step(`^the response is 503 with Retry-After: (\d+)$`, st.is503RA)
			sc.Step(`^a relay succeeds$`, st.relaySucceeds)
			sc.Step(`^the response has no Retry-After header$`, st.noRA)
			sc.Step(`^a JobResult JSON with no retry_after_sec$`, st.legacyJobResult)
			sc.Step(`^the broker decodes it$`, st.brokerDecodes)
			sc.Step(`^retry_after_sec is 0 and nothing else changes$`, st.decodedAsBefore)
			sc.Step(`^the upstream returns 429 with Retry-After 7, then 503 with Retry-After 3, then 200 with Retry-After 99$`, st.upstreamSequence)
			sc.Step(`^the JobResults carry retry_after_sec (\d+), (\d+), and (\d+) respectively$`, st.resultsCarry)
			sc.Step(`^the broker returns 429 with Retry-After: (\d+)$`, st.brokerReturns429RA)
			sc.Step(`^the local proxy relays it to the agent$`, st.proxyRelays)
			sc.Step(`^the agent sees Retry-After: (\d+)$`, st.agentSeesRA)

			// 7. probes
			sc.Step(`^"s1" is cooling$`, st.s1Cooling)
			sc.Step(`^the prober runs$`, st.proberRuns)
			sc.Step(`^the canary is dispatched to "([^"]*)" \(probes bypass the cooldown filter\)$`, st.canaryDispatched)
			sc.Step(`^"s1"'s upstream returns 429 to the canary$`, st.s1_429ToCanary)
			sc.Step(`^the probe records FAIL \(consecutive=1\) as today$`, st.probeFail1)
			sc.Step(`^"s1" is not cooling and its owner has no strike$`, st.s1NotCoolingNoStrike)

			// 8. observability
			sc.Step(`^4 failovers, 6 cooldowns, and 2 cooling-503s happened$`, st.countersHappened)
			sc.Step(`^the founder reads /admin/live$`, st.readAdminLive)
			sc.Step(`^it shows relay_failovers (\d+), station_cooldowns (\d+), band_cooling_503 (\d+)$`, st.adminCounters)
			sc.Step(`^a per-station table with cooling_until$`, st.adminStationTable)
			sc.Step(`^the TUI renders the band$`, st.tuiRenders)
			sc.Step(`^the station row is on air with a "cooling" marker and the seconds remaining$`, st.stationRowCooling)
			sc.Step(`^the band is never shown as dark because of cooling$`, st.bandNeverDark)
			sc.Step(`^one "FAILOVER request=\.\.\. from=s1 \(upstream-throttled\) to=s2" line is logged$`, st.failoverLogged)
			sc.Step(`^"s1" has been cooling for more than 10 minutes cumulative in the last hour with real demand behind it$`, st.s1CoolingTenMinutes)
			sc.Step(`^the founder alert "station_cooling:s1" fired once naming the band and the count$`, st.alertFiredOnce)
			sc.Step(`^it clears after an hour without a cooldown$`, st.alertClears)

			// 9. adversarial
			sc.Step(`^"s1" and "s2" serve "m"$`, st.twoSamePrice)
			sc.Step(`^"s1" cannot cause "s2" to cool: cooldown is keyed on the station that answered 429$`, st.cannotSteer)
			sc.Step(`^"s1"'s upstream returns 429 to all requests including canaries$`, st.s1_429Everything)
			sc.Step(`^(\d+) probe rounds run$`, st.probeRounds)
			sc.Step(`^"s1" is excluded by the probe dead streak and its owner has zero earnings and zero strikes$`, st.quarantined)
			sc.Step(`^the same request is replayed on the bus \(multi-instance duplicate result\)$`, st.replayResult)
			sc.Step(`^exactly one spend row exists for the request$`, st.oneSpendRow)
			sc.Step(`^"s1" 429s with a body naming "s1" and "s2" serves$`, st.s1_429NamingItself)
			sc.Step(`^the response body is exactly "s2"'s completion$`, st.bodyExactlyS2)

			// 10. Sep 7
			sc.Step(`^the Cerebras station "cb" 429s for 66 of 228 requests and a sibling "pw" serves "qwen-3\.8-27b"$`, st.sep7Sibling)
			sc.Step(`^one funded consumer relays 228 prompts$`, st.relays228)
			sc.Step(`^all 228 responses are 200$`, st.all228OK)
			sc.Step(`^66 voided receipts name "cb" with void_reason "upstream-throttled" and 228 settled receipts exist$`, st.sep7Receipts)
			sc.Step(`^the Cerebras station "cb" is the only station for "qwen-3\.8-27b" and its upstream 429s with Retry-After 15 for any minute over 150000 tokens$`, st.sep7Alone)
			sc.Step(`^one funded consumer relays 30 prompts of 83000 tokens within 3 minutes$`, st.relays30Big)
			sc.Step(`^every non-200 response carries a Retry-After$`, st.every429HasRA)
			sc.Step(`^after the first 429 in a minute the following requests in that cooldown get an immediate 503 band cooling without an upstream call$`, st.coolingGets503)
			sc.Step(`^the upstream received fewer than 10 rejected requests \(vs 66 on Sep 7\)$`, st.fewerThan10Rejected)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/routing/upstream_failover.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("routing/upstream_failover scenarios failed (see godog output above)")
	}
}
