package main

// upstream_throttle_strikes_bdd_test.go makes features/safety/upstream_throttle_not_a_strike.feature
// EXECUTABLE against the REAL relay: a real broker (relayBroker) over a real store (the in-memory
// reference, or the real Postgres when ROGERAI_TEST_DATABASE_URL is set - the repo's Postgres
// test pattern), a real httptest UPSTREAM scripted per scenario, and a real station loop that
// drains the node's job channel, serves each job against that upstream over HTTP, signs the
// receipt with the node key, pipes a streamed body through the real /agent/stream ingress
// (b.agentStream) and returns every result through the real /agent/result handler
// (b.agentResult). No mocks: every consequence is read back from the store (receipts, entries,
// strikes, holds, ledger, earnings) or from the broker's own in-memory health state.
//
// Postgres mode: ids are nonce-suffixed (bindings persist across runs there, exactly as
// cross_instance_bdd_test.go does); the scenario names "st1"/"op1" map onto the real ids.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
)

// utLog is a concurrency-safe log sink (the station goroutine and the relay both log).
type utLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *utLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *utLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func (l *utLog) Reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf.Reset()
}

type utState struct {
	t   *testing.T
	db  store.Store
	mem *store.Mem
	pg  *store.Postgres

	b            *broker
	brokerPubHex string
	logs         *utLog

	// the scripted upstream: script answers request n (1-based, per scenario)
	upstream *httptest.Server
	sidecar  *httptest.Server
	scriptMu sync.Mutex
	script   func(n int, w http.ResponseWriter, r *http.Request)
	reqN     int

	// the owned station "st1" (owner "op1") serving "m"
	node       string
	nodePriv   ed25519.PrivateKey
	nodePubHex string
	token      string
	acct       string // op1 = the owner's pubkey hex (the durable account id)
	ownerPriv  ed25519.PrivateKey
	tun        *nodeTunnel
	freeNode   string // "free1": a $0 offer on model "mfree" (the free-band relay)

	// the consumer
	consumerPriv ed25519.PrivateKey
	wallet       string
	funded       bool
	start        float64 // balance before the relays
	maxTokens    int     // when > 0, the relay body carries max_tokens (hold sizing)

	// station bookkeeping
	stationStop chan struct{}
	stationWG   sync.WaitGroup
	jobsMu      sync.Mutex
	jobIDs      []string
	lastRes     protocol.JobResult

	// last relay outcome
	lastCode int
	lastBody []byte

	freeReq string

	// endpoint responses
	ownerResp map[string]any
	adminResp map[string]any

	// captured transactional mail
	mailMu sync.Mutex
	mails  []string
}

func utNonce() string {
	n, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	return strconv.FormatInt(n.Int64(), 36)
}

// --- harness -----------------------------------------------------------------

func (s *utState) reset() error {
	s.teardown()
	s.logs.Reset()
	nonce := utNonce()
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
	s.b = relayBroker(s.db)
	s.b.rl = &rateLimiter{buckets: map[string]*tokenBucket{}} // rpm 0 = unlimited (the replay fires 228 relays)
	s.b.strikeWarnAt, s.b.strikeBanAt = defaultStrikeWarnAt, defaultStrikeBanAt
	s.b.strikeCorroborateKinds, s.b.strikeDecayDays = defaultStrikeCorroborateKinds, defaultStrikeDecayDays
	s.b.adminKey = hex.EncodeToString(s.b.priv.Seed())
	s.brokerPubHex = hex.EncodeToString(s.b.priv.Public().(ed25519.PublicKey))
	s.mails = nil
	s.b.mail = enabledMailer(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		s.mailMu.Lock()
		s.mails = append(s.mails, string(body))
		s.mailMu.Unlock()
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"id":"m"}`))}, nil
	})

	// L1 re-count sidecar stub: an exact count far above any claim, so the settled paths
	// bill the node's claim and never see an over-report; its presence turns stream capture
	// ON (sink.cap non-nil), the default path. A scenario turns it off explicitly.
	s.sidecar = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"tokens": 1_000_000, "exact": true})
	}))
	s.b.recount = recountConfig{url: s.sidecar.URL, tolerance: 0.02, strikeTolerance: 0.25, client: &http.Client{Timeout: 4 * time.Second}}

	// The scripted upstream.
	s.reqN = 0
	s.script = nil
	s.upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.scriptMu.Lock()
		s.reqN++
		n, script := s.reqN, s.script
		s.scriptMu.Unlock()
		if script == nil {
			utRealCompletion(w)
			return
		}
		script(n, w, r)
	}))

	// Owner op1 (a verified GitHub identity with an email on file, so the warn ladder can
	// mail it) and the station st1 it runs.
	_, s.ownerPriv, _ = ed25519.GenerateKey(nil)
	s.acct = hex.EncodeToString(s.ownerPriv.Public().(ed25519.PublicKey))
	gid, _ := rand.Int(rand.Reader, big.NewInt(1<<31))
	if err := s.db.BindOwner(store.Owner{GitHubID: gid.Int64() + 10, Login: "op1-" + nonce, Pubkey: s.acct, Email: "op1@example.com"}); err != nil {
		return err
	}
	s.node = "st1-" + nonce
	s.token = "tok-" + nonce
	s.tun = s.standUpNode(s.node, "m", 1.0, 1.0)
	s.freeNode = ""

	// The consumer: a signed, logged-in wallet distinct from the operator.
	_, s.consumerPriv, _ = ed25519.GenerateKey(nil)
	cpub := hex.EncodeToString(s.consumerPriv.Public().(ed25519.PublicKey))
	cgid, _ := rand.Int(rand.Reader, big.NewInt(1<<31))
	if err := s.db.BindOwner(store.Owner{GitHubID: cgid.Int64() + 10, Login: "buyer-" + nonce, Pubkey: cpub}); err != nil {
		return err
	}
	s.wallet = "u_gh_" + strconv.FormatInt(cgid.Int64()+10, 10)
	s.funded, s.start, s.maxTokens = false, 0, 0
	s.jobIDs = nil
	s.lastRes = protocol.JobResult{}
	s.lastCode, s.lastBody, s.freeReq = 0, nil, ""
	s.ownerResp, s.adminResp = nil, nil
	return nil
}

func (s *utState) teardown() {
	if s.stationStop != nil {
		close(s.stationStop)
		s.stationWG.Wait()
		s.stationStop = nil
	}
	if s.upstream != nil {
		s.upstream.Close()
		s.upstream = nil
	}
	if s.sidecar != nil {
		s.sidecar.Close()
		s.sidecar = nil
	}
	if s.pg != nil {
		_ = s.pg.Close()
		s.pg = nil
	}
}

// standUpNode registers a node on the broker's live maps under owner op1, binds it in the
// store, and starts its station loop. Returns the node's tunnel.
func (s *utState) standUpNode(id, model string, priceIn, priceOut float64) *nodeTunnel {
	pub, priv, _ := ed25519.GenerateKey(nil)
	if id == s.node {
		s.nodePriv, s.nodePubHex = priv, hex.EncodeToString(pub)
	}
	s.b.nodes[id] = protocol.NodeRegistration{
		NodeID: id, PubKey: hex.EncodeToString(pub),
		Offers: []protocol.ModelOffer{{Model: model, PriceIn: priceIn, PriceOut: priceOut, Ctx: 32768}},
	}
	s.b.lastSeen[id] = time.Now()
	tun := &nodeTunnel{jobs: make(chan protocol.Job, 8), waiters: map[string]chan protocol.JobResult{}, token: s.token}
	s.b.tunnels[id] = tun
	if err := s.db.BindNode(id, s.acct); err != nil {
		s.t.Fatalf("bind node: %v", err)
	}
	if s.stationStop == nil {
		s.stationStop = make(chan struct{})
	}
	s.stationWG.Add(1)
	go s.station(tun, id, model, priv, priceIn, priceOut, s.stationStop)
	return tun
}

// station is the REAL return path of a node: drain the job channel, serve each job against
// the upstream over HTTP, pipe a stream through /agent/stream, sign the receipt with the
// node key, and return the JobResult through /agent/result.
func (s *utState) station(tun *nodeTunnel, id, model string, priv ed25519.PrivateKey, priceIn, priceOut float64, stop <-chan struct{}) {
	defer s.stationWG.Done()
	for {
		select {
		case <-stop:
			return
		case job := <-tun.jobs:
			s.jobsMu.Lock()
			s.jobIDs = append(s.jobIDs, job.ID)
			s.jobsMu.Unlock()
			var req struct {
				Stream bool `json:"stream"`
			}
			_ = json.Unmarshal(job.Body, &req)
			status, body := http.StatusBadGateway, []byte(`{"error":"upstream unreachable"}`)
			var pt, ct int
			if resp, err := http.Post(s.upstream.URL, "application/json", bytes.NewReader(job.Body)); err == nil {
				status = resp.StatusCode
				if req.Stream {
					// serveStream's pipe: every upstream line flows to the broker's stream
					// ingress as it arrives; the usage chunk is scanned on the way through.
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
					sreq := httptest.NewRequest(http.MethodPost, "/agent/stream?node="+id+"&job="+job.ID, pr)
					sreq.Header.Set("Authorization", "Bearer "+s.token)
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
				RequestID: job.ID, NodeID: id, User: job.User, Model: model,
				PromptTokens: pt, CompletionTokens: ct, PriceIn: priceIn, PriceOut: priceOut,
				TS: time.Now().Unix(), LineageMethod: "p0-upstream-usage",
			}
			rec.SignNode(priv)
			res := protocol.JobResult{ID: job.ID, Status: status, Body: body, Receipt: rec}
			s.jobsMu.Lock()
			s.lastRes = res
			s.jobsMu.Unlock()
			s.deliver(id, res)
		}
	}
}

// deliver returns a JobResult over the REAL /agent/result handler (json.Marshal + POST).
func (s *utState) deliver(id string, res protocol.JobResult) {
	wire, _ := json.Marshal(res)
	r := httptest.NewRequest(http.MethodPost, "/agent/result?node="+id, bytes.NewReader(wire))
	r.Header.Set("Authorization", "Bearer "+s.token)
	s.b.agentResult(httptest.NewRecorder(), r)
}

func utRealCompletion(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"The answer is 4."}}],"usage":{"prompt_tokens":5000,"completion_tokens":20}}`))
}

func (s *utState) scriptStatus(status int, body string) {
	s.scriptMu.Lock()
	defer s.scriptMu.Unlock()
	s.script = func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

func utDefaultBody(status int) string {
	if status == http.StatusTooManyRequests {
		return `{"error":{"message":"rate limit exceeded"}}`
	}
	return fmt.Sprintf(`{"error":"upstream status %d"}`, status)
}

func (s *utState) fund(amount float64) error {
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

func (s *utState) ensureFunded() error {
	if s.funded {
		return nil
	}
	return s.fund(10)
}

func utPrompt(tokens int) string {
	return strings.Repeat("word ", tokens*4/5) // ~chars/4 tokens
}

func (s *utState) relayBody(model string, tokens int, stream bool) []byte {
	m := map[string]any{
		"model":    model,
		"messages": []map[string]string{{"role": "user", "content": utPrompt(tokens)}},
	}
	if stream {
		m["stream"] = true
	}
	if s.maxTokens > 0 {
		m["max_tokens"] = s.maxTokens
	}
	b, _ := json.Marshal(m)
	return b
}

func (s *utState) relay(body []byte) {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	signReq(r, s.consumerPriv, body)
	w := httptest.NewRecorder()
	s.b.relay(w, r)
	s.lastCode, s.lastBody = w.Code, w.Body.Bytes()
}

func (s *utState) lastReqID() string {
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	if len(s.jobIDs) == 0 {
		return ""
	}
	return s.jobIDs[len(s.jobIDs)-1]
}

func (s *utState) balance() (float64, error) { return s.db.PeekBalance(s.wallet) }

func (s *utState) entries() ([]store.Entry, error) { return s.db.RecentByUser(s.wallet, 1000) }

func (s *utState) entryFor(reqID string) (store.Entry, bool, error) {
	es, err := s.entries()
	if err != nil {
		return store.Entry{}, false, err
	}
	for _, e := range es {
		if e.RequestID == reqID {
			return e, true, nil
		}
	}
	return store.Entry{}, false, nil
}

// storedReceipt reads the receipt the broker stored for a request: the JSONB column on
// Postgres, the retained receipt on the in-memory store.
func (s *utState) storedReceipt(reqID string) (protocol.UsageReceipt, map[string]any, error) {
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

func (s *utState) strikes() ([]store.Strike, error) { return s.db.StrikesByOwner(s.acct, 0) }

func (s *utState) held() (bool, error) { return s.db.AccountRecountHeld(s.acct) }

func (s *utState) preStrikes(n int, at int64) error {
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("pre-%d-%s", i, utNonce())
		if s.pg != nil {
			if _, err := s.pg.OwnerStrike(s.acct, store.StrikeEmptyOutput, `{"note":"pre"}`, key); err != nil {
				return err
			}
			if _, err := s.pg.DB().Exec(`UPDATE rogerai.owner_strikes SET created_at=$1 WHERE account_id=$2 AND idem_key=$3`, at, s.acct, "strike:"+key); err != nil {
				return err
			}
			continue
		}
		s.mem.SeedStrikesForTest([]store.Strike{{AccountID: s.acct, Kind: store.StrikeEmptyOutput, Evidence: `{"note":"pre"}`, CreatedAt: at}})
	}
	return nil
}

// --- Background --------------------------------------------------------------

func (s *utState) brokerWithStation() error { return nil } // reset() stood it up
func (s *utState) scriptedUpstream() error  { return nil } // reset() started it

// --- Given: upstream scripts -------------------------------------------------

func (s *utState) upstreamStatusBody(status, body string) error {
	n, err := strconv.Atoi(status)
	if err != nil {
		return err
	}
	s.scriptStatus(n, body)
	return nil
}

func (s *utState) upstream429NoSSE() error {
	s.scriptStatus(429, utDefaultBody(429))
	return nil
}

func (s *utState) upstreamStatus(status string) error {
	n, err := strconv.Atoi(status)
	if err != nil {
		return err
	}
	s.scriptStatus(n, utDefaultBody(n))
	return nil
}

func (s *utState) upstream429WithChoices(content string) error {
	s.scriptStatus(429, fmt.Sprintf(`{"error":"rate limit exceeded","choices":[{"message":{"content":%q}}]}`, content))
	return nil
}

func (s *utState) upstream429WithPromptTokens(n string) error {
	s.scriptStatus(429, fmt.Sprintf(`{"error":{"message":"rate limit exceeded"},"usage":{"prompt_tokens":%s,"completion_tokens":0}}`, n))
	return nil
}

func (s *utState) upstream200Real() error {
	s.scriptMu.Lock()
	defer s.scriptMu.Unlock()
	s.script = func(_ int, w http.ResponseWriter, _ *http.Request) { utRealCompletion(w) }
	return nil
}

func (s *utState) upstream200Empty() error {
	s.scriptStatus(200, `{"choices":[],"usage":{"prompt_tokens":5000,"completion_tokens":0}}`)
	return nil
}

func (s *utState) upstream429Everything() error {
	s.scriptStatus(429, utDefaultBody(429))
	return nil
}

func (s *utState) upstreamBurst(throttled, total string) error {
	k, err := strconv.Atoi(throttled)
	if err != nil {
		return err
	}
	n, err := strconv.Atoi(total)
	if err != nil {
		return err
	}
	s.scriptMu.Lock()
	defer s.scriptMu.Unlock()
	// Exactly k of the n requests answer 429, spread across the burst (not front-loaded),
	// which is what the Sep 7 minute-by-minute throttling looked like.
	s.script = func(i int, w http.ResponseWriter, _ *http.Request) {
		if i <= n && (i*k)/n != ((i-1)*k)/n {
			w.WriteHeader(429)
			_, _ = w.Write([]byte(utDefaultBody(429)))
			return
		}
		utRealCompletion(w)
	}
	return nil
}

func (s *utState) noStreamCapture() error {
	s.b.recount.url = "" // recount off => sink.cap stays nil
	return nil
}

func (s *utState) stationClaimsImpossibleInput() error {
	// A small body, a claim beyond body bytes + impossibleInputBanMargin: arithmetic proof.
	s.scriptStatus(200, `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":100000,"completion_tokens":2}}`)
	return nil
}

// --- Given: consumer / owner state ------------------------------------------

func (s *utState) fundedConsumer(v string) error {
	f, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return s.fund(f)
}

func (s *utState) fundedConsumerWithHold(v, hold string) error {
	f, err := feParseFloat(v)
	if err != nil {
		return err
	}
	h, err := feParseFloat(hold)
	if err != nil {
		return err
	}
	// Size the pre-auth exactly on the prompt side: estimateMaxCost = promptEst * price_in
	// / 1e6 when price_out is 0 (an out-price large enough to reserve 0.3 on its own would
	// trip the consumer default out-cap and no station would be picked), and promptEst is
	// len(body)/4+1 for the very body relayPrompt sends.
	promptEst := len(s.relayBody("m", 50, false))/4 + 1
	reg := s.b.nodes[s.node]
	reg.Offers[0].PriceIn, reg.Offers[0].PriceOut = h*1e6/float64(promptEst), 0
	s.b.nodes[s.node] = reg
	return s.fund(f)
}

func (s *utState) ownerHasNoStrikes() error {
	st, err := s.strikes()
	if err != nil {
		return err
	}
	if len(st) != 0 {
		return fmt.Errorf("op1 already has %d strikes", len(st))
	}
	return nil
}

func (s *utState) ownerHasStrikesInWindow(n string) error {
	k, err := strconv.Atoi(n)
	if err != nil {
		return err
	}
	return s.preStrikes(k, time.Now().Unix())
}

func (s *utState) ownerHasStrikesDaysAgo(n, days string) error {
	k, err := strconv.Atoi(n)
	if err != nil {
		return err
	}
	d, err := strconv.Atoi(days)
	if err != nil {
		return err
	}
	return s.preStrikes(k, time.Now().Add(-time.Duration(d)*24*time.Hour).Unix())
}

func (s *utState) ownerHeldExpiredOneStrike() error {
	if err := s.db.SetAccountRecountHold(s.acct, true); err != nil {
		return err
	}
	if _, err := s.db.ExpireRecountHolds(time.Now().Add(time.Second)); err != nil {
		return err
	}
	if h, _ := s.held(); h {
		return fmt.Errorf("the hold did not expire")
	}
	return s.preStrikes(1, time.Now().Unix())
}

func (s *utState) ownerHeldForDiscrepancy() error { return s.db.SetAccountRecountHold(s.acct, true) }

func (s *utState) ownerHasOneStrikeFrom503() error {
	s.scriptStatus(503, utDefaultBody(503))
	return s.relayPrompt()
}

func (s *utState) throttledToday(n string) error {
	k, err := strconv.Atoi(n)
	if err != nil {
		return err
	}
	s.scriptStatus(429, utDefaultBody(429))
	return s.relayN(k)
}

func (s *utState) nodeHadThrottlesAndStrikes(throttles, strikes string) error {
	k, err := strconv.Atoi(throttles)
	if err != nil {
		return err
	}
	g, err := strconv.Atoi(strikes)
	if err != nil {
		return err
	}
	s.scriptStatus(429, utDefaultBody(429))
	if err := s.relayN(k); err != nil {
		return err
	}
	_ = s.upstream200Empty() // a genuine empty reply: the strike this signal exists for
	return s.relayN(g)
}

func (s *utState) successAvg(v string) error {
	f, err := feParseFloat(v)
	if err != nil {
		return err
	}
	s.b.metricsMu.Lock()
	s.b.success[s.node] = f
	s.b.metricsMu.Unlock()
	return nil
}

func (s *utState) freeRelaySettled() error {
	s.freeNode = "free1-" + utNonce()
	s.standUpNode(s.freeNode, "mfree", 0, 0)
	if err := s.ensureFunded(); err != nil {
		return err
	}
	_ = s.upstream200Real()
	s.relay(s.relayBody("mfree", 50, false))
	if s.lastCode != 200 {
		return fmt.Errorf("free relay = %d: %s", s.lastCode, s.lastBody)
	}
	s.freeReq = s.lastReqID()
	return nil
}

func (s *utState) throttledRelayVoided() error {
	s.scriptStatus(429, utDefaultBody(429))
	return s.relayPrompt()
}

// --- When --------------------------------------------------------------------

func (s *utState) relayPrompt() error {
	if err := s.ensureFunded(); err != nil {
		return err
	}
	s.relay(s.relayBody("m", 50, false))
	return nil
}

func (s *utState) relayN(n int) error {
	if err := s.ensureFunded(); err != nil {
		return err
	}
	for i := 0; i < n; i++ {
		s.relay(s.relayBody("m", 50, false))
	}
	return nil
}

func (s *utState) relayTokensNonStream(tokens, model string) error {
	n, err := strconv.Atoi(tokens)
	if err != nil {
		return err
	}
	if err := s.ensureFunded(); err != nil {
		return err
	}
	s.relay(s.relayBody(model, n, false))
	return nil
}

func (s *utState) relayStream() error {
	if err := s.ensureFunded(); err != nil {
		return err
	}
	s.relay(s.relayBody("m", 50, true))
	return nil
}

func (s *utState) relayCount(n string) error {
	k, err := strconv.Atoi(n)
	if err != nil {
		return err
	}
	return s.relayN(k)
}

func (s *utState) relayCountOnModel(n, _ string) error { return s.relayCount(n) }

func (s *utState) resultDeliveredTwice() error {
	// The multi-instance bus can redeliver a result; the second copy arrives after the relay
	// already consumed the first (the waiter is gone) - it must settle nothing new.
	s.jobsMu.Lock()
	res := s.lastRes
	s.jobsMu.Unlock()
	if res.ID == "" {
		return fmt.Errorf("no job result to replay")
	}
	s.deliver(s.node, res)
	return nil
}

func (s *utState) ownerCallsStrikes() error {
	r := signedReq(http.MethodGet, "/owner/strikes", nil, s.ownerPriv)
	w := httptest.NewRecorder()
	s.b.ownerStrikes(w, r)
	if w.Code != 200 {
		return fmt.Errorf("GET /owner/strikes = %d: %s", w.Code, w.Body.String())
	}
	s.ownerResp = map[string]any{}
	return json.Unmarshal(w.Body.Bytes(), &s.ownerResp)
}

func (s *utState) adminCallsNode(_ string) error {
	r := httptest.NewRequest(http.MethodGet, "/admin/node/"+s.node, nil)
	r.Header.Set("X-Roger-Admin", s.b.adminKey)
	w := httptest.NewRecorder()
	s.b.adminNode(w, r)
	if w.Code != 200 {
		return fmt.Errorf("GET /admin/node/%s = %d: %s", s.node, w.Code, w.Body.String())
	}
	s.adminResp = map[string]any{}
	return json.Unmarshal(w.Body.Bytes(), &s.adminResp)
}

func (s *utState) probeRounds(n string) error {
	k, err := strconv.Atoi(n)
	if err != nil {
		return err
	}
	reg := s.b.nodes[s.node]
	for i := 0; i < k; i++ {
		s.b.probeNode(reg, "m", nextCanary(uint64(i)))
	}
	return nil
}

func (s *utState) founderListsReceipts() error { return nil } // read in the Then

// --- Then: money / receipts --------------------------------------------------

func (s *utState) receivesUpstreamError(status string) error {
	n, _ := strconv.Atoi(status)
	if s.lastCode != n {
		return fmt.Errorf("consumer got %d, want %d (body %s)", s.lastCode, n, s.lastBody)
	}
	if !bytes.Contains(s.lastBody, []byte("rate limit exceeded")) {
		return fmt.Errorf("the upstream error body was not passed through: %s", s.lastBody)
	}
	return nil
}

func (s *utState) chargedZero() error {
	bal, err := s.balance()
	if err != nil {
		return err
	}
	if err := feApprox(bal, s.start); err != nil {
		return fmt.Errorf("consumer charged: balance %.6f, start %.6f", bal, s.start)
	}
	return nil
}

func (s *utState) receiptZero() error {
	e, ok, err := s.entryFor(s.lastReqID())
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no receipt for request %s", s.lastReqID())
	}
	if e.Cost != 0 || e.OwnerShare != 0 {
		return fmt.Errorf("receipt cost=%g owner_share=%g, want 0/0", e.Cost, e.OwnerShare)
	}
	return nil
}

func (s *utState) voided() error { return s.receiptZero() }

func (s *utState) voidedAndZero() error {
	if err := s.receiptZero(); err != nil {
		return err
	}
	return s.chargedZero()
}

func (s *utState) noStrikes(string) error {
	st, err := s.strikes()
	if err != nil {
		return err
	}
	if len(st) != 0 {
		return fmt.Errorf("op1 has %d strike(s), want none: %+v", len(st), st)
	}
	return nil
}

func (s *utState) notHeld(string) error {
	h, err := s.held()
	if err != nil {
		return err
	}
	if h {
		return fmt.Errorf("op1 is in account_recount_holds (payouts frozen)")
	}
	return nil
}

func (s *utState) isHeld(string) error {
	h, err := s.held()
	if err != nil {
		return err
	}
	if !h {
		return fmt.Errorf("op1 is NOT in account_recount_holds")
	}
	return nil
}

func (s *utState) throttledLogLine(string) error {
	logs := s.logs.String()
	if !strings.Contains(logs, "THROTTLED upstream-429 user=") || !strings.Contains(logs, " node="+s.node+" - $0, hold refunded, not a strike") {
		return fmt.Errorf("no THROTTLED log line; logs:\n%s", logs)
	}
	return nil
}

func (s *utState) logSays(want string) error {
	if !strings.Contains(s.logs.String(), want) {
		return fmt.Errorf("log does not contain %q; logs:\n%s", want, s.logs.String())
	}
	return nil
}

func (s *utState) streamCarriesError() error {
	if s.lastCode != 200 {
		return fmt.Errorf("stream status %d, want 200 (SSE headers)", s.lastCode)
	}
	if !bytes.Contains(s.lastBody, []byte("rate limit exceeded")) {
		return fmt.Errorf("the stream did not carry the upstream error: %q", s.lastBody)
	}
	return nil
}

func (s *utState) oneStrikeWithStatus(kind, status string) error {
	want, _ := strconv.Atoi(status)
	st, err := s.strikes()
	if err != nil {
		return err
	}
	var hits []store.Strike
	for _, k := range st {
		if k.Kind == kind {
			hits = append(hits, k)
		}
	}
	if len(hits) != 1 {
		return fmt.Errorf("%d %s strike(s), want exactly 1", len(hits), kind)
	}
	var ev struct {
		Status int `json:"status"`
	}
	if err := json.Unmarshal([]byte(hits[0].Evidence), &ev); err != nil {
		return fmt.Errorf("evidence not JSON: %s", hits[0].Evidence)
	}
	if ev.Status != want {
		return fmt.Errorf("evidence.status=%d, want %d", ev.Status, want)
	}
	return nil
}

func (s *utState) receiptPromptZero() error {
	e, ok, err := s.entryFor(s.lastReqID())
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no receipt for %s", s.lastReqID())
	}
	if e.PromptTokens != 0 {
		return fmt.Errorf("throttled receipt records prompt_tokens=%d, want 0", e.PromptTokens)
	}
	return nil
}

func (s *utState) exactlyOneReceipt() error {
	es, err := s.entries()
	if err != nil {
		return err
	}
	n := 0
	for _, e := range es {
		if e.RequestID == s.lastReqID() {
			n++
		}
	}
	if n != 1 {
		return fmt.Errorf("%d receipts for %s, want exactly 1", n, s.lastReqID())
	}
	return nil
}

func (s *utState) receiptHasVoid(reason, status string) error {
	want, _ := strconv.Atoi(status)
	_, keys, err := s.storedReceipt(s.lastReqID())
	if err != nil {
		return err
	}
	if got, _ := keys["void_reason"].(string); got != reason {
		return fmt.Errorf("void_reason=%q, want %q (receipt %v)", got, reason, keys)
	}
	if got, _ := keys["upstream_status"].(float64); int(got) != want {
		return fmt.Errorf("upstream_status=%v, want %d", keys["upstream_status"], want)
	}
	return nil
}

func (s *utState) receiptHasNoVoid() error {
	_, keys, err := s.storedReceipt(s.lastReqID())
	if err != nil {
		return err
	}
	if _, ok := keys["void_reason"]; ok {
		return fmt.Errorf("settled receipt carries void_reason: %v", keys)
	}
	if _, ok := keys["upstream_status"]; ok {
		return fmt.Errorf("settled receipt carries upstream_status: %v", keys)
	}
	return nil
}

func (s *utState) nodeSigVerifies() error {
	rec, _, err := s.storedReceipt(s.lastReqID())
	if err != nil {
		return err
	}
	if rec.VoidReason == "" {
		return fmt.Errorf("the stored receipt carries no void_reason to test against")
	}
	if !rec.VerifyNode(s.nodePubHex) {
		return fmt.Errorf("node_sig no longer verifies once the broker stamped the void fields")
	}
	return nil
}

func (s *utState) brokerSigCoversVoid() error {
	rec, _, err := s.storedReceipt(s.lastReqID())
	if err != nil {
		return err
	}
	ok, covers := rec.VerifyBrokerCoverage(s.brokerPubHex)
	if !ok || !covers {
		return fmt.Errorf("broker_sig does not verify (ok=%v covers=%v)", ok, covers)
	}
	rec.VoidReason = "tampered"
	if ok, _ := rec.VerifyBrokerCoverage(s.brokerPubHex); ok {
		return fmt.Errorf("broker_sig still verifies after tampering with void_reason")
	}
	return nil
}

func (s *utState) freeVsThrottled() error {
	_, freeKeys, err := s.storedReceipt(s.freeReq)
	if err != nil {
		return err
	}
	if _, ok := freeKeys["void_reason"]; ok {
		return fmt.Errorf("the free $0 relay carries a void_reason: %v", freeKeys)
	}
	_, thrKeys, err := s.storedReceipt(s.lastReqID())
	if err != nil {
		return err
	}
	if got, _ := thrKeys["void_reason"].(string); got != "upstream-throttled" {
		return fmt.Errorf("throttled receipt void_reason=%q", got)
	}
	return nil
}

// --- Then: endpoints ---------------------------------------------------------

func (s *utState) ownerJSONHeld(v string) error {
	got, ok := s.ownerResp["held"].(bool)
	if !ok {
		return fmt.Errorf(`"held" missing or not a bool: %v`, s.ownerResp["held"])
	}
	if got != (v == "true") {
		return fmt.Errorf(`"held"=%v, want %s`, got, v)
	}
	return nil
}

func (s *utState) ownerJSONThrottled(n string) error {
	want, _ := strconv.Atoi(n)
	got, ok := s.ownerResp["throttled_24h"].(float64)
	if !ok || int(got) != want {
		return fmt.Errorf(`"throttled_24h"=%v, want %d`, s.ownerResp["throttled_24h"], want)
	}
	note, _ := s.ownerResp["throttle_note"].(string)
	if !strings.Contains(strings.ToLower(note), "not strikes") {
		return fmt.Errorf(`no note that throttles are not strikes: %q`, note)
	}
	return nil
}

func (s *utState) ownerJSONHeldReflects() error {
	got, ok := s.ownerResp["held"].(bool)
	if !ok {
		return fmt.Errorf(`"held" missing or not a bool: %v`, s.ownerResp["held"])
	}
	h, err := s.held()
	if err != nil {
		return err
	}
	if got != h {
		return fmt.Errorf(`"held"=%v but account_recount_holds says %v`, got, h)
	}
	return nil
}

func (s *utState) adminCounters(strikes, throttled string) error {
	ws, _ := strconv.Atoi(strikes)
	wt, _ := strconv.Atoi(throttled)
	gs, _ := s.adminResp["strikes"].(float64)
	gt, _ := s.adminResp["throttled"].(float64)
	if int(gs) != ws || int(gt) != wt {
		return fmt.Errorf("admin node view strikes=%v throttled=%v, want %d/%d (%v)", s.adminResp["strikes"], s.adminResp["throttled"], ws, wt, s.adminResp)
	}
	return nil
}

// --- Then: health ------------------------------------------------------------

func (s *utState) successStill(v string) error {
	want, _ := feParseFloat(v)
	s.b.metricsMu.Lock()
	got := s.b.success[s.node]
	s.b.metricsMu.Unlock()
	if got != want {
		return fmt.Errorf("success average moved to %.4f, want %.4f untouched", got, want)
	}
	return nil
}

func (s *utState) successBelowOne() error {
	s.b.metricsMu.Lock()
	got := s.b.success[s.node]
	s.b.metricsMu.Unlock()
	if !(got < 1.0) {
		return fmt.Errorf("success average %.4f did not drop on a 5xx", got)
	}
	return nil
}

func (s *utState) inflightZero() error {
	if n := s.b.inflightOf(s.node); n != 0 {
		return fmt.Errorf("in_flight=%d after the request, want 0", n)
	}
	return nil
}

func (s *utState) inflightMirrorAgrees() error {
	// Single-instance: the mirror publisher must have nothing left to publish (a dirty
	// node would mean the count moved without the mirror being told).
	if s.b.loadPub.pending() {
		return fmt.Errorf("the shared-load mirror still has unpublished in-flight changes")
	}
	return s.inflightZero()
}

// --- Then: Rule 4 ------------------------------------------------------------

func (s *utState) exactlyOneStrike(string) error {
	st, err := s.strikes()
	if err != nil {
		return err
	}
	if len(st) != 1 {
		return fmt.Errorf("%d strikes, want exactly 1", len(st))
	}
	return nil
}

func (s *utState) warningEmailSent() error {
	deadline := time.Now().Add(3 * time.Second)
	for {
		s.mailMu.Lock()
		for _, m := range s.mails {
			if strings.Contains(m, "Account warning") {
				s.mailMu.Unlock()
				return nil
			}
		}
		n := len(s.mails)
		s.mailMu.Unlock()
		if time.Now().After(deadline) {
			return fmt.Errorf("no account-warning email captured (%d mails)", n)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (s *utState) bannedAndHeld(string) error {
	banned, _, err := s.db.IsOwnerBanned(s.acct)
	if err != nil {
		return err
	}
	if !banned {
		return fmt.Errorf("op1 not banned on a zero-doubt strike")
	}
	return s.isHeld("")
}

// --- Then: adversarial -------------------------------------------------------

func (s *utState) excludedFromPick(string) error {
	s.b.mu.Lock()
	_, _, ok := s.b.pickFor("m", false, 0, 0, 0, "", nil, nil, nil, pickReq{rng: seededRand("ut-pick")})
	s.b.mu.Unlock()
	if ok {
		return fmt.Errorf("a probe-dead station is still picked")
	}
	return nil
}

func (s *utState) zeroEarningsZeroStrikes(string) error {
	e, err := s.db.EarningsOf(s.node)
	if err != nil {
		return err
	}
	if e != 0 {
		return fmt.Errorf("earnings %.6f, want 0", e)
	}
	return s.noStrikes("")
}

func (s *utState) marketOffline(string) error {
	s.b.mu.Lock()
	views := s.b.enrichOffersForNode(nil, s.b.nodes[s.node], time.Now(), nil, false)
	s.b.mu.Unlock()
	if len(views) == 0 {
		return fmt.Errorf("no market view for the station")
	}
	if views[0].Online {
		return fmt.Errorf("the market still lists the station online")
	}
	return nil
}

func (s *utState) voidedAndStruck() error {
	if err := s.receiptZero(); err != nil {
		return err
	}
	return s.oneStrikeWithStatus(store.StrikeEmptyOutput, "200")
}

func (s *utState) balanceExactly(v string) error {
	want, _ := feParseFloat(v)
	bal, err := s.balance()
	if err != nil {
		return err
	}
	if err := feApprox(bal, want); err != nil {
		return fmt.Errorf("balance %.9f, want exactly %.6f", bal, want)
	}
	return nil
}

func (s *utState) ledgerHoldAndRelease() error {
	rows, err := s.db.LedgerOf(s.wallet, []string{store.KindHold, store.KindHoldRelease}, 100)
	if err != nil {
		return err
	}
	var holds, releases int
	var hAmt, rAmt float64
	for _, r := range rows {
		switch r.Kind {
		case store.KindHold:
			holds++
			hAmt += -r.Amount
		case store.KindHoldRelease:
			releases++
			rAmt += r.Amount
		}
	}
	if holds != 1 || releases != 1 {
		return fmt.Errorf("ledger has %d hold(s) and %d release(s), want 1/1", holds, releases)
	}
	if err := feApprox(hAmt, rAmt); err != nil {
		return fmt.Errorf("hold %.6f != release %.6f", hAmt, rAmt)
	}
	earns, err := s.db.LedgerOf(s.acct, []string{store.KindEarn}, 100)
	if err != nil {
		return err
	}
	if len(earns) != 0 {
		return fmt.Errorf("an earn row was minted for a throttled request")
	}
	return nil
}

func (s *utState) stillHeld(string) error { return s.isHeld("") }

func (s *utState) noStrikesAdded() error { return s.noStrikes("") }

// --- Then: Sep 7 replay ------------------------------------------------------

func (s *utState) burstReceipts(settled, voided, reason string) error {
	ws, _ := strconv.Atoi(settled)
	wv, _ := strconv.Atoi(voided)
	es, err := s.entries()
	if err != nil {
		return err
	}
	var gs, gv int
	for _, e := range es {
		if e.Cost > 0 {
			gs++
			continue
		}
		_, keys, err := s.storedReceipt(e.RequestID)
		if err != nil {
			return err
		}
		if got, _ := keys["void_reason"].(string); got == reason {
			gv++
		} else {
			return fmt.Errorf("$0 receipt %s has void_reason %q, want %q", e.RequestID, got, reason)
		}
	}
	if gs != ws || gv != wv {
		return fmt.Errorf("settled=%d voided=%d, want %d/%d", gs, gv, ws, wv)
	}
	return nil
}

func (s *utState) earningsEqualShares(string) error {
	es, err := s.entries()
	if err != nil {
		return err
	}
	var sum float64
	for _, e := range es {
		sum += e.OwnerShare
	}
	if sum <= 0 {
		return fmt.Errorf("no settled owner shares to compare")
	}
	earn, err := s.db.EarningsOf(s.node)
	if err != nil {
		return err
	}
	return feApprox(earn, sum)
}

func (s *utState) balanceMinusCosts(string) error {
	es, err := s.entries()
	if err != nil {
		return err
	}
	var sum float64
	for _, e := range es {
		sum += e.Cost
	}
	bal, err := s.balance()
	if err != nil {
		return err
	}
	return feApprox(bal, s.start-sum)
}

func TestUpstreamThrottleNotAStrikeBDD(t *testing.T) {
	st := &utState{t: t, logs: &utLog{}}
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

			// Background
			sc.Step(`^a broker with a real store and one owned station "st1" \(owner "op1"\) serving "m"$`, st.brokerWithStation)
			sc.Step(`^the station's upstream is a scripted HTTP server$`, st.scriptedUpstream)

			// upstream scripts
			sc.Step(`^the upstream is scripted to return (\d+) with body (.+)$`, st.upstreamStatusBody)
			sc.Step(`^the upstream is scripted to return (\d+) with (\{.+)$`, st.upstreamStatusBody)
			sc.Step(`^the upstream is scripted to return 429 with a JSON error body \(no SSE\)$`, st.upstream429NoSSE)
			sc.Step(`^the upstream is scripted to return (\d+)$`, st.upstreamStatus)
			sc.Step(`^the upstream is scripted to return 429 with a body containing choices\[0\]\.message\.content "([^"]*)"$`, st.upstream429WithChoices)
			sc.Step(`^the upstream is scripted to return 429 with usage\.prompt_tokens (\d+)$`, st.upstream429WithPromptTokens)
			sc.Step(`^the upstream is scripted to return 200 with a real completion$`, st.upstream200Real)
			sc.Step(`^the upstream is scripted to return 200 with an empty completion$`, st.upstream200Empty)
			sc.Step(`^the upstream is scripted to return 429 to every request including probes$`, st.upstream429Everything)
			sc.Step(`^the upstream is scripted to return 429 for (\d+) of (\d+) requests and 200 with real completions otherwise$`, st.upstreamBurst)
			sc.Step(`^the broker runs without stream capture \(sink\.cap is nil\)$`, st.noStreamCapture)
			sc.Step(`^the station claims more prompt tokens than the request could contain$`, st.stationClaimsImpossibleInput)

			// consumer / owner state
			sc.Step(`^a funded consumer with a ([\d.]+) credit balance$`, st.fundedConsumer)
			sc.Step(`^a funded consumer with a ([\d.]+) credit balance and a ([\d.]+) hold$`, st.fundedConsumerWithHold)
			sc.Step(`^"op1" has no strikes$`, st.ownerHasNoStrikes)
			sc.Step(`^"op1" has (\d+) empty-output strikes inside the decay window$`, st.ownerHasStrikesInWindow)
			sc.Step(`^"op1" has (\d+) empty-output strikes dated (\d+) days ago$`, st.ownerHasStrikesDaysAgo)
			sc.Step(`^"op1" was held, the hold expired, and 1 strike remains inside the window$`, st.ownerHeldExpiredOneStrike)
			sc.Step(`^"op1" is held for a genuine recount discrepancy$`, st.ownerHeldForDiscrepancy)
			sc.Step(`^"op1" has one empty-output strike from a 503$`, st.ownerHasOneStrikeFrom503)
			sc.Step(`^(\d+) requests to "st1" were throttled upstream today$`, st.throttledToday)
			sc.Step(`^"st1" had (\d+) upstream 429s and (\d+) genuine empty-output today$`, st.nodeHadThrottlesAndStrikes)
			sc.Step(`^"st1" has a measured success average of ([\d.]+)$`, st.successAvg)
			sc.Step(`^a free band relay that settled normally at \$0$`, st.freeRelaySettled)
			sc.Step(`^a throttled relay voided at \$0$`, st.throttledRelayVoided)

			// When
			sc.Step(`^the consumer relays a (\d+)-token prompt on "([^"]*)" \(non-stream\)$`, st.relayTokensNonStream)
			sc.Step(`^a funded consumer relays with "stream": true$`, st.relayStream)
			sc.Step(`^a funded consumer relays a prompt$`, st.relayPrompt)
			sc.Step(`^the request settles$`, st.relayPrompt)
			sc.Step(`^(\d+) funded relays are throttled$`, st.relayCount)
			sc.Step(`^one funded consumer relays (\d+) prompts on "([^"]*)"$`, st.relayCountOnModel)
			sc.Step(`^the same job result is delivered twice \(multi-instance bus replay\)$`, st.resultDeliveredTwice)
			sc.Step(`^the owner calls GET /owner/strikes$`, st.ownerCallsStrikes)
			sc.Step(`^the founder calls GET /admin/node/([^ ]+) with the admin token$`, st.adminCallsNode)
			sc.Step(`^(\d+) probe rounds run$`, st.probeRounds)
			sc.Step(`^the founder lists receipts for the consumer$`, st.founderListsReceipts)

			// Then: money / receipts
			sc.Step(`^the consumer receives status (\d+) with the upstream error body$`, st.receivesUpstreamError)
			sc.Step(`^the consumer is charged 0\.000000 credits and the hold is refunded in full$`, st.chargedZero)
			sc.Step(`^the consumer is charged 0\.000000 credits$`, st.chargedZero)
			sc.Step(`^a receipt exists for the request with cost 0 and owner_share 0$`, st.receiptZero)
			sc.Step(`^NO owner_strikes rows? exists? for "([^"]*)"$`, st.noStrikes)
			sc.Step(`^"([^"]*)" is NOT in account_recount_holds$`, st.notHeld)
			sc.Step(`^"([^"]*)" IS in account_recount_holds$`, st.isHeld)
			sc.Step(`^the log line is "(THROTTLED upstream-429 [^"]*)"$`, st.throttledLogLine)
			sc.Step(`^the log says "([^"]*)"$`, st.logSays)
			sc.Step(`^the stream carries the upstream error and ends$`, st.streamCarriesError)
			sc.Step(`^the request is voided$`, st.voided)
			sc.Step(`^the request is voided and the consumer is charged 0\.000000 credits$`, st.voidedAndZero)
			sc.Step(`^exactly one owner_strikes row of kind "([^"]*)" exists for "op1" with evidence\.status (\d+)$`, st.oneStrikeWithStatus)
			sc.Step(`^the receipt records prompt_tokens 0 \(a throttled request consumed nothing upstream\)$`, st.receiptPromptZero)
			sc.Step(`^exactly one receipt exists for the request$`, st.exactlyOneReceipt)
			sc.Step(`^the stored receipt JSON has "void_reason": "([^"]*)" and "upstream_status": (\d+)$`, st.receiptHasVoid)
			sc.Step(`^the stored receipt JSON has no "void_reason" and no "upstream_status" key$`, st.receiptHasNoVoid)
			sc.Step(`^the stored receipt's node_sig still verifies against the node's key$`, st.nodeSigVerifies)
			sc.Step(`^the broker_sig covers the void fields \(tampering with void_reason fails broker verification\)$`, st.brokerSigCoversVoid)
			sc.Step(`^the free relay shows no void_reason and the throttled one shows "upstream-throttled"$`, st.freeVsThrottled)

			// Then: endpoints
			sc.Step(`^the JSON has "held": (true|false)$`, st.ownerJSONHeld)
			sc.Step(`^"throttled_24h": (\d+) with the note that throttles are not strikes$`, st.ownerJSONThrottled)
			sc.Step(`^the JSON has a boolean "held" key that reflects account_recount_holds$`, st.ownerJSONHeldReflects)
			sc.Step(`^it reports strikes=(\d+) and throttled=(\d+) as distinct counters$`, st.adminCounters)

			// Then: health
			sc.Step(`^the success average is still ([\d.]+)(?: \(.*\))?$`, st.successStill)
			sc.Step(`^the success average is below 1\.00$`, st.successBelowOne)
			sc.Step(`^the station's in_flight count returns to 0$`, st.inflightZero)
			sc.Step(`^the shared-store in-flight mirror agrees$`, st.inflightMirrorAgrees)

			// Then: Rule 4
			sc.Step(`^exactly one owner_strikes row exists for "([^"]*)"$`, st.exactlyOneStrike)
			sc.Step(`^the warning email is sent as today$`, st.warningEmailSent)
			sc.Step(`^"([^"]*)" is banned and in account_recount_holds \(unchanged\)$`, st.bannedAndHeld)

			// Then: adversarial
			sc.Step(`^"([^"]*)" is excluded from pickFor \(probe dead streak\)$`, st.excludedFromPick)
			sc.Step(`^"([^"]*)" has zero earnings and zero strikes$`, st.zeroEarningsZeroStrikes)
			sc.Step(`^the market lists "([^"]*)" as offline$`, st.marketOffline)
			sc.Step(`^the request is voided AND struck \(the 200-empty path is untouched\)$`, st.voidedAndStruck)
			sc.Step(`^the consumer's balance is exactly ([\d.]+) \(the hold released, nothing more\)$`, st.balanceExactly)
			sc.Step(`^the ledger has one hold and one hold_release for the request and no earn row$`, st.ledgerHoldAndRelease)
			sc.Step(`^"([^"]*)" is still in account_recount_holds$`, st.stillHeld)
			sc.Step(`^no strike rows were added$`, st.noStrikesAdded)

			// Then: Sep 7 replay
			sc.Step(`^(\d+) receipts settled with cost > 0 and (\d+) voided with void_reason "([^"]*)"$`, st.burstReceipts)
			sc.Step(`^the operator's pending earnings equal the sum of the (\d+) settled owner shares$`, st.earningsEqualShares)
			sc.Step(`^the consumer's balance equals the start balance minus the (\d+) settled costs$`, st.balanceMinusCosts)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/safety/upstream_throttle_not_a_strike.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("safety/upstream_throttle_not_a_strike scenarios failed (see godog output above)")
	}
}
