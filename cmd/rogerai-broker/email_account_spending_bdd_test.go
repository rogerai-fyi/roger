package main

// email_account_spending_bdd_test.go makes features/money/email_account_spending.feature
// EXECUTABLE against the REAL broker and store (in-memory, or Postgres when
// ROGERAI_TEST_DATABASE_URL is set), on the upstream_failover harness (foState): real stations
// behind real tunnels, the real emailed-code sign-in (/auth/email/start + /auth/email/verify
// with the captured mail), the real device approval (/auth/device/start signed by the CLI key +
// /auth/device/approve with the email session), the real relay, voice relay, dashboards, cap and
// remote-control resolvers. Nothing is mocked; the only stand-ins are the stations' upstream
// model servers and the mail provider (captured).
//
// Two observation notes, stated so nothing passes vacuously:
//   - "the bridge serves it and bills the account wallet" is observed through the real
//     /tower/edge/authorize on the production tower route table (towerTestBroker): the
//     authorize is where the bridge resolves the payer and places the hold. A full sealed
//     relay needs a running fabric the direct-path harness does not carry.
//   - "the failover plan was not trimmed to free stations" is observed as: the first paid
//     station was attempted and the second PAID station served, while a free station was on
//     air the whole time (a free-only plan would have served from it).

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
)

type eaState struct {
	*foState

	addr    string // the email account under test
	wallet  string // its account wallet
	session string // its web session cookie value
	// the identity a "the caller/the account" step relays with
	via       string // "session" | "key"
	key       ed25519.PrivateKey
	key2      ed25519.PrivateKey
	balStart  float64
	rcvdStart map[string]int

	// other accounts in the scenario
	ginaWallet  string
	ginaSession string
	newSession  string
	newWallet   string

	unboundKey ed25519.PrivateKey
	pendingKey ed25519.PrivateKey

	topupWallet string

	// outcomes
	resp     *httptest.ResponseRecorder
	resps    []*httptest.ResponseRecorder
	dashBody map[string]any
}

// --- harness ------------------------------------------------------------------------------

// u makes a scenario's address unique per scenario run (plus-addressing with the run nonce):
// the Postgres store persists across scenarios, so a fixed address would carry one scenario's
// wallet, spend and owner rows into the next. Every step maps the address it is given.
func (s *eaState) u(addr string) string {
	i := strings.Index(addr, "@")
	if i < 0 || addr == "" {
		return addr
	}
	return addr[:i] + "+" + s.nonce + addr[i:]
}

// gid is a fresh GitHub id per call, for the same reason.
func eaGID() int64 {
	n, _ := rand.Int(rand.Reader, big.NewInt(1<<40))
	return n.Int64() + 1_000_000
}

func (s *eaState) reset() error {
	if err := s.foState.reset(); err != nil {
		return err
	}
	s.b.seedFunds = 0.5 // the starter seed every account wallet receives once
	s.addr, s.wallet, s.session, s.via = "", "", "", "session"
	s.key, s.key2, s.unboundKey, s.pendingKey = nil, nil, nil, nil
	s.ginaWallet, s.ginaSession, s.newSession, s.newWallet = "", "", "", ""
	s.topupWallet = ""
	s.resp, s.resps, s.dashBody = nil, nil, nil
	s.rcvdStart = map[string]int{}
	return nil
}

// signInByEmail drives the REAL emailed-code sign-in and returns the session cookie value and
// the wallet the broker resolved for it.
func (s *eaState) signInByEmail(addr string) (string, error) {
	mark := s.mailCount()
	rec := s.postWeb(s.b.emailStart, "/auth/email/start", map[string]any{"email": addr}, "")
	if rec.Code != http.StatusOK {
		return "", fmt.Errorf("/auth/email/start = %d %s", rec.Code, rec.Body.String())
	}
	code, err := s.codeAfter(mark)
	if err != nil {
		return "", err
	}
	rec = s.postWeb(s.b.emailVerify, "/auth/email/verify", map[string]any{"email": addr, "code": code}, "")
	if rec.Code != http.StatusOK {
		return "", fmt.Errorf("/auth/email/verify = %d %s", rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			return c.Value, nil
		}
	}
	return "", fmt.Errorf("/auth/email/verify set no session cookie")
}

func (s *eaState) mailCount() int {
	s.mailMu.Lock()
	defer s.mailMu.Unlock()
	return len(s.mails)
}

var eaSixDigits = regexp.MustCompile(`\b(\d{6})\b`)

func (s *eaState) codeAfter(mark int) (string, error) {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s.mailMu.Lock()
		if len(s.mails) > mark {
			last := s.mails[len(s.mails)-1]
			s.mailMu.Unlock()
			if m := eaSixDigits.FindStringSubmatch(strings.ReplaceAll(last, `\n`, " ")); m != nil {
				return m[1], nil
			}
			return "", fmt.Errorf("no code in the mailed message")
		}
		s.mailMu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	return "", fmt.Errorf("no sign-in code was mailed")
}

// postWeb calls a browser-facing handler from the allowlisted web origin, with an optional
// session cookie.
func (s *eaState) postWeb(h http.HandlerFunc, path string, in any, session string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(in)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", testWebOrigin)
	if session != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

// approveDevice runs the REAL device login: the CLI key starts it (signed), the email session
// approves it, binding the key to the account.
func (s *eaState) approveDevice(session string) (ed25519.PrivateKey, error) {
	_, priv, _ := ed25519.GenerateKey(nil)
	body := []byte(`{}`)
	req := httptest.NewRequest(http.MethodPost, "/auth/device/start", bytes.NewReader(body))
	signReq(req, priv, body)
	rec := httptest.NewRecorder()
	s.b.deviceStart(rec, req)
	if rec.Code != http.StatusOK {
		return nil, fmt.Errorf("/auth/device/start = %d %s", rec.Code, rec.Body.String())
	}
	var start struct {
		UserCode string `json:"user_code"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &start)
	ap := s.postWeb(s.b.deviceApprove, "/auth/device/approve", map[string]any{"user_code": start.UserCode}, session)
	if ap.Code != http.StatusOK {
		return nil, fmt.Errorf("/auth/device/approve = %d %s", ap.Code, ap.Body.String())
	}
	return priv, nil
}

func (s *eaState) setBalance(wallet string, want float64) error {
	bal, err := s.db.BalanceOf(wallet, s.b.seedFunds) // creates the row the way production does
	if err != nil {
		return err
	}
	if d := want - bal; d != 0 {
		if _, err := s.db.AddCredits(wallet, d); err != nil {
			return err
		}
	}
	got, _ := s.db.PeekBalance(wallet)
	if !approxF(got, want) {
		return fmt.Errorf("could not set %s to $%.2f (is $%.6f)", wallet, want, got)
	}
	return nil
}

// chatBody is the relay body for the scenario model.
func (s *eaState) chatBody(model string, stream bool) []byte {
	m := map[string]any{"model": model, "messages": []map[string]string{{"role": "user", "content": utPrompt(s.tokens)}}}
	if stream {
		m["stream"] = true
	}
	b, _ := json.Marshal(m)
	return b
}

// relayAs sends one relay as the given identity: a session cookie (from the web origin), a
// signed key, or an unsigned request with extra headers.
func (s *eaState) relayAs(session string, key ed25519.PrivateKey, model, pin string, stream bool, hdr map[string]string) *httptest.ResponseRecorder {
	body := s.chatBody(model, stream)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	switch {
	case key != nil:
		signReq(req, key, body)
	case session != "":
		req.Header.Set("Origin", testWebOrigin)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
	}
	if pin != "" {
		req.Header.Set("X-Roger-Node", s.st(pin).id)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.b.relay(rec, req)
	s.resp = rec
	s.resps = append(s.resps, rec)
	return rec
}

func (s *eaState) relayAccount(model, pin string, stream bool) *httptest.ResponseRecorder {
	if s.via == "key" {
		return s.relayAs("", s.key, model, pin, stream, nil)
	}
	return s.relayAs(s.session, nil, model, pin, stream, nil)
}

// getDash GETs a dashboard route as the account's session.
func (s *eaState) getDash(h http.HandlerFunc, path, session string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Origin", testWebOrigin)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
	rec := httptest.NewRecorder()
	h(rec, req)
	s.resp = rec
	s.dashBody = nil
	_ = json.Unmarshal(rec.Body.Bytes(), &s.dashBody)
	return rec
}

func (s *eaState) upstreamHits(name string) int {
	st := s.st(name)
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.reqN
}

// settleSpend records `amount` of real spend for the wallet through the same store calls the
// relay's settle makes (hold, then finalize with a receipt).
func (s *eaState) settleSpend(wallet string, amount float64) error {
	reqID := "spend-" + utNonce()
	ok, err := s.db.HoldFor(wallet, reqID, amount)
	if err != nil || !ok {
		return fmt.Errorf("hold $%.2f on %s: ok=%v err=%v", amount, wallet, ok, err)
	}
	rec := protocol.UsageReceipt{RequestID: reqID, NodeID: s.st("n-1").id, Model: "qwen3-32b", PromptTokens: 100, CompletionTokens: 10, TS: time.Now().Unix()}
	if _, err = s.db.Finalize(wallet, s.st("n-1").id, amount, amount, amount*0.7, rec); err != nil {
		return err
	}
	s.b.recordMonthSpend(wallet, amount, time.Now()) // the settle's month-to-date accelerator bump
	return nil
}

func (s *eaState) seedEntries(wallet string) (int, error) {
	rows, err := s.db.LedgerOf(wallet, nil, 1000)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range rows {
		if r.IdemKey == "seed:"+wallet { // the one-time seed's idempotency key
			n++
		}
	}
	return n, nil
}

// --- Background ---------------------------------------------------------------------------

func (s *eaState) configuredMailer() error { return nil } // foState's broker always has one

func (s *eaState) paidStation(name, model, in, out string) error {
	pin, _ := strconv.ParseFloat(in, 64)
	pout, _ := strconv.ParseFloat(out, 64)
	s.standUp(name, stationOpts{model: model, priceIn: pin, priceOut: pout})
	return nil
}

func (s *eaState) freeStation(name, model string) error {
	s.standUp(name, stationOpts{model: model})
	return nil
}

func (s *eaState) signedInWithCode(addr string) error {
	addr = s.u(addr)
	sess, err := s.signInByEmail(addr)
	if err != nil {
		return err
	}
	s.addr, s.session, s.wallet = addr, sess, walletForEmail(addr)
	return nil
}

func (s *eaState) walletHolds(addr, amt string) error {
	v, _ := strconv.ParseFloat(amt, 64)
	if err := s.setBalance(walletForEmail(s.u(addr)), v); err != nil {
		return err
	}
	s.balStart = v
	return nil
}

// --- the spend gate -----------------------------------------------------------------------

func (s *eaState) callsThrough(addr, identity string) error {
	switch identity {
	case "the browser session minted by the emailed code":
		s.via = "session"
	case "a CLI device key bound to the account by device approval":
		k, err := s.approveDevice(s.session)
		if err != nil {
			return err
		}
		s.key, s.via = k, "key"
	case "a second device key bound to the same account":
		if _, err := s.approveDevice(s.session); err != nil {
			return err
		}
		k, err := s.approveDevice(s.session)
		if err != nil {
			return err
		}
		s.key, s.via = k, "key"
	default:
		return fmt.Errorf("unknown identity %q", identity)
	}
	return nil
}

func (s *eaState) callerRelaysPinned(model, pin string) error {
	s.rcvdStart[pin] = s.upstreamHits(pin)
	s.relayAccount(model, pin, false)
	return nil
}

func (s *eaState) responseFrom(code int, name string) error {
	if s.resp.Code != code {
		return fmt.Errorf("status %d, want %d (%s)", s.resp.Code, code, s.resp.Body.String())
	}
	if got := s.resp.Header().Get("X-RogerAI-Provider"); got != s.st(name).id {
		return fmt.Errorf("served by %q, want %q", got, s.st(name).id)
	}
	return nil
}

func (s *eaState) response200From(name string) error { return s.responseFrom(200, name) }

func (s *eaState) receiptRecorded() error {
	if s.resp.Header().Get("X-RogerAI-Receipt") == "" {
		return fmt.Errorf("no X-RogerAI-Receipt on the response")
	}
	return nil
}

func (s *eaState) costDebitedFrom(addr string) error {
	cost, err := strconv.ParseFloat(s.resp.Header().Get("X-RogerAI-Cost"), 64)
	if err != nil || cost <= 0 {
		return fmt.Errorf("X-RogerAI-Cost %q, want a positive cost", s.resp.Header().Get("X-RogerAI-Cost"))
	}
	bal, _ := s.db.PeekBalance(walletForEmail(s.u(addr)))
	if !approxF(s.balStart-bal, cost) {
		return fmt.Errorf("%s's wallet moved $%.6f, want the cost $%.6f", addr, s.balStart-bal, cost)
	}
	return nil
}

func (s *eaState) no401LogIn() error {
	if s.resp.Code == http.StatusUnauthorized || strings.Contains(s.resp.Body.String(), "log in to spend") {
		return fmt.Errorf("got the log-in refusal: %d %s", s.resp.Code, s.resp.Body.String())
	}
	return nil
}

func (s *eaState) accountRelaysPinned(model, pin string) error {
	s.rcvdStart[pin] = s.upstreamHits(pin)
	s.relayAccount(model, pin, false)
	return nil
}

func (s *eaState) response402Code(code string) error {
	if s.resp.Code != http.StatusPaymentRequired {
		return fmt.Errorf("status %d, want 402 (%s)", s.resp.Code, s.resp.Body.String())
	}
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(s.resp.Body.Bytes(), &e)
	if e.Error.Code != code {
		return fmt.Errorf("error.code %q, want %q (%s)", e.Error.Code, code, s.resp.Body.String())
	}
	return nil
}

func (s *eaState) responseNot401() error {
	if s.resp.Code == http.StatusUnauthorized {
		return fmt.Errorf("got 401: %s", s.resp.Body.String())
	}
	return nil
}

func (s *eaState) costHeaderIs(v string) error {
	if got := s.resp.Header().Get("X-RogerAI-Cost"); got != v {
		return fmt.Errorf("X-RogerAI-Cost %q, want %q", got, v)
	}
	return nil
}

func (s *eaState) accountStreamsPinned(model, pin string) error {
	s.relayAccount(model, pin, true)
	return nil
}

func (s *eaState) streamUsageChunk() error {
	body := s.resp.Body.String()
	if s.resp.Code != 200 {
		return fmt.Errorf("status %d: %s", s.resp.Code, body)
	}
	// The broker-owned final usage chunk: {"choices":[],"usage":{...,"rogerai":{"receipt":...}}}
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: {") {
			continue
		}
		var c struct {
			Choices []any `json:"choices"`
			Usage   struct {
				Rogerai struct {
					Receipt string `json:"receipt"`
				} `json:"rogerai"`
			} `json:"usage"`
		}
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &c) == nil && len(c.Choices) == 0 && c.Usage.Rogerai.Receipt != "" {
			return nil
		}
	}
	return fmt.Errorf("no broker usage chunk carrying the receipt in the stream")
}

func (s *eaState) streamDebited(addr string) error {
	// a stream carries the settled cost in its trailing meter; the wallet must have moved by it
	bal, _ := s.db.PeekBalance(walletForEmail(s.u(addr)))
	if !(bal < s.balStart) {
		return fmt.Errorf("%s's wallet did not move on the stream (still $%.6f)", addr, bal)
	}
	return nil
}

func (s *eaState) secondPaidStation(name, model string) error {
	s.standUp(name, stationOpts{model: model, priceIn: 0.5, priceOut: 1.5})
	return nil
}

func (s *eaState) answers429Next(name string) error {
	st := s.st(name)
	st.set(func(n int, w http.ResponseWriter, _ *http.Request) {
		if n == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "5")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(utDefaultBody(429)))
			return
		}
		utRealCompletion(w)
	})
	// pick order: n-1 first (Tier A), then n-2 ahead of the free station
	ordered := []string{name}
	for _, n := range s.order {
		if n != name && n != "n-free" {
			ordered = append(ordered, n)
		}
	}
	if _, ok := s.stations["n-free"]; ok {
		ordered = append(ordered, "n-free")
	}
	s.order = ordered
	s.landOrder()
	return nil
}

func (s *eaState) accountRelays(model string) error {
	s.rcvdStart["n-1"] = s.upstreamHits("n-1")
	s.relayAccount(model, "", false)
	return nil
}

func (s *eaState) planNotTrimmedToFree() error {
	if s.upstreamHits("n-1")-s.rcvdStart["n-1"] < 1 {
		return fmt.Errorf("the paid first pick n-1 was never attempted")
	}
	if got := s.resp.Header().Get("X-RogerAI-Provider"); got != s.st("n-2").id {
		return fmt.Errorf("served by %q, want the paid follower n-2", got)
	}
	return nil
}

// --- voice --------------------------------------------------------------------------------

func (s *eaState) paidVoiceStation(name, model string) error {
	pub, priv, _ := ed25519.GenerateKey(nil)
	id := name + "-" + s.nonce
	s.b.nodes[id] = protocol.NodeRegistration{NodeID: id, PubKey: hex.EncodeToString(pub),
		Offers: []protocol.ModelOffer{{Model: model, Modality: protocol.ModalityTTS, PriceIn: 15}}}
	s.b.lastSeen[id] = time.Now()
	tun := &nodeTunnel{jobs: make(chan protocol.Job, 4), waiters: map[string]chan protocol.JobResult{}}
	s.b.tunnels[id] = tun
	if err := s.db.BindNode(id, "voice-op-"+s.nonce); err != nil {
		return err
	}
	stop := s.stationStop
	s.stationWG.Add(1)
	go func() {
		defer s.stationWG.Done()
		for {
			select {
			case <-stop:
				return
			case job := <-tun.jobs:
				rec := protocol.UsageReceipt{RequestID: job.ID, NodeID: id, Model: model, TS: time.Now().Unix()}
				rec.SignNode(priv)
				res := protocol.JobResult{ID: job.ID, Status: 200, Body: []byte("~audio~"), Receipt: rec}
				tun.mu.Lock()
				ch := tun.waiters[job.ID]
				tun.mu.Unlock()
				if ch != nil {
					ch <- res
				}
			}
		}
	}()
	return nil
}

func (s *eaState) requestsSpeech(model string) error {
	body := []byte(fmt.Sprintf(`{"model":%q,"voice":%q,"input":"hello there","response_format":"mp3"}`, model, model))
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", bytes.NewReader(body))
	if s.key == nil {
		k, err := s.approveDevice(s.session)
		if err != nil {
			return err
		}
		s.key = k
	}
	signReq(req, s.key, body)
	rec := httptest.NewRecorder()
	s.b.audioRelay(rec, req)
	s.resp = rec
	return nil
}

func (s *eaState) response200() error {
	if s.resp.Code != 200 {
		return fmt.Errorf("status %d, want 200 (%s)", s.resp.Code, s.resp.Body.String())
	}
	return nil
}

func (s *eaState) no403Voice() error {
	if s.resp.Code == http.StatusForbidden || strings.Contains(s.resp.Body.String(), "sign in to use this voice model") {
		return fmt.Errorf("got the voice sign-in refusal: %d %s", s.resp.Code, s.resp.Body.String())
	}
	return nil
}

// --- Tower parity -------------------------------------------------------------------------

func (s *eaState) towerServesOnly(model, out string) error { return nil } // built in the Then (own tower broker)

func (s *eaState) bridgeBills(addr string) error {
	tb, srv := towerTestBroker(s.t)
	op := signedInOperator(s.t, tb, "tw-op-"+s.nonce)
	tw := enrolledTower(s.t, tb, op.login)
	attachStation(s.t, tb, "st-1", tw.id, ownerPubkeyOf(s.t, tb, op.login))
	routableEdge(s.t, tb, tw.id, "st-1", "qwen3-32b", "203.0.113.7:8443")
	// the email account's CLI key on the tower broker: bound exactly as device approval binds it
	pub, priv, _ := ed25519.GenerateKey(nil)
	addr = s.u(addr)
	o := store.Owner{Pubkey: hex.EncodeToString(pub), Email: addr, EmailVerifiedAt: time.Now().Unix()}
	if err := tb.db.BindOwner(o); err != nil {
		return err
	}
	w := walletForEmail(addr)
	if _, err := tb.db.AddCredits(w, 5); err != nil {
		return err
	}
	before, _ := tb.db.PeekBalance(w)
	code, out := consumerCall(s.t, srv, priv, "/tower/edge/authorize", map[string]any{"model": "qwen3-32b", "consumer_env_key": testEnvKeyHex(s.t)})
	if code != http.StatusOK {
		return fmt.Errorf("the bridge refused the email account: %d %v", code, out)
	}
	after, _ := tb.db.PeekBalance(w)
	if !(after < before) {
		return fmt.Errorf("the bridge placed no hold on %s's account wallet ($%.6f -> $%.6f)", addr, before, after)
	}
	return nil
}

func (s *eaState) directServesToo(model string) error {
	if s.key == nil {
		k, err := s.approveDevice(s.session)
		if err != nil {
			return err
		}
		s.key = k
	}
	rec := s.relayAs("", s.key, model, "n-1", false, nil)
	if rec.Code != 200 {
		return fmt.Errorf("the direct path refused the same account: %d %s", rec.Code, rec.Body.String())
	}
	return nil
}

// --- dashboards ---------------------------------------------------------------------------

func (s *eaState) accountGETs(path string) error {
	switch path {
	case "/me":
		s.getDash(s.b.me, path, s.session)
	case "/balance":
		s.getDash(s.b.balance, path, s.session)
	case "/usage":
		s.getDash(s.b.usage, path, s.session)
	case "/console":
		s.getDash(s.b.console, path, s.session)
	default:
		return fmt.Errorf("no route %s in this runner", path)
	}
	return nil
}

func (s *eaState) saysLoggedIn() error {
	if v, _ := s.dashBody["logged_in"].(bool); !v {
		return fmt.Errorf("logged_in is not true: %d %s", s.resp.Code, s.resp.Body.String())
	}
	return nil
}

func (s *eaState) showsBalance(amt string) error {
	want, _ := strconv.ParseFloat(amt, 64)
	got, _ := s.dashBody["balance"].(float64)
	if !approxF(got, want) {
		return fmt.Errorf("balance %v, want %v (%s)", s.dashBody["balance"], want, s.resp.Body.String())
	}
	return nil
}

func (s *eaState) spentThisMonth(amt string) error {
	v, _ := strconv.ParseFloat(amt, 64)
	return s.settleSpend(s.wallet, v)
}

func (s *eaState) usage200Spend(amt string) error {
	if s.resp.Code != 200 {
		return fmt.Errorf("/usage %d: %s", s.resp.Code, s.resp.Body.String())
	}
	want, _ := strconv.ParseFloat(amt, 64)
	got := 0.0 // /usage groups this month's spend into buckets; their costs sum to the total
	bks, _ := s.dashBody["buckets"].([]any)
	for _, b := range bks {
		m, _ := b.(map[string]any)
		c, _ := m["cost"].(float64)
		got += c
	}
	if !approxF(got, want) {
		return fmt.Errorf("/usage spend %v, want %v (%s)", got, want, s.resp.Body.String())
	}
	return nil
}

func (s *eaState) madePaidRequest() error {
	rec := s.relayAccount("qwen3-32b", "n-1", false)
	if rec.Code != 200 {
		return fmt.Errorf("the paid request was not served: %d %s", rec.Code, rec.Body.String())
	}
	return nil
}

func (s *eaState) consoleListsRequest() error {
	if s.resp.Code != 200 {
		return fmt.Errorf("/console %d: %s", s.resp.Code, s.resp.Body.String())
	}
	evs, _ := s.dashBody["events"].([]any)
	if len(evs) == 0 {
		return fmt.Errorf("/console lists no events for the account: %s", s.resp.Body.String())
	}
	return nil
}

// --- limits -------------------------------------------------------------------------------

func (s *eaState) setsMonthlyLimit(amt string) error {
	v, _ := strconv.ParseFloat(amt, 64)
	body, _ := json.Marshal(map[string]any{"monthly_cap": v})
	req := httptest.NewRequest(http.MethodPatch, "/account/limit", bytes.NewReader(body))
	req.Header.Set("Origin", testWebOrigin)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: s.session})
	rec := httptest.NewRecorder()
	s.b.accountLimit(rec, req)
	s.resp = rec
	return nil
}

func (s *eaState) limitStoredFor(addr string) error {
	if s.resp.Code != 200 {
		return fmt.Errorf("PATCH /account/limit = %d %s", s.resp.Code, s.resp.Body.String())
	}
	cap, err := s.db.MonthlyCapOf(walletForEmail(s.u(addr)))
	if err != nil || cap <= 0 {
		return fmt.Errorf("no cap stored on %s's wallet (cap=%v err=%v)", addr, cap, err)
	}
	return nil
}

func (s *eaState) spentAndRelays(amt string) error {
	v, _ := strconv.ParseFloat(amt, 64)
	if err := s.settleSpend(s.wallet, v); err != nil {
		return err
	}
	s.relayAccount("qwen3-32b", "n-1", false)
	return nil
}

func (s *eaState) response402Monthly() error {
	if s.resp.Code != http.StatusPaymentRequired || !strings.Contains(s.resp.Body.String(), "monthly spend limit") {
		return fmt.Errorf("status %d %s, want 402 naming the monthly spend limit", s.resp.Code, s.resp.Body.String())
	}
	return nil
}

func (s *eaState) perIdentityRateLimit(n, burst int) error {
	s.b.rl = &rateLimiter{buckets: map[string]*tokenBucket{}, rpm: float64(n), burst: float64(burst)}
	return nil
}

func (s *eaState) sessionThenKey(nSess, nKey int) error {
	k, err := s.approveDevice(s.session)
	if err != nil {
		return err
	}
	s.resps = nil
	for i := 0; i < nSess; i++ {
		s.relayAs(s.session, nil, "qwen3-32b", "n-free", false, nil)
	}
	for i := 0; i < nKey; i++ {
		s.relayAs("", k, "qwen3-32b", "n-free", false, nil)
	}
	return nil
}

func (s *eaState) thirdIs429() error {
	if len(s.resps) < 3 {
		return fmt.Errorf("only %d requests sent", len(s.resps))
	}
	r := s.resps[2]
	if r.Code != http.StatusTooManyRequests || !strings.Contains(r.Body.String(), "rate limit exceeded") {
		return fmt.Errorf("third request %d %s, want 429 rate limit exceeded (first two: %d, %d)", r.Code, r.Body.String(), s.resps[0].Code, s.resps[1].Code)
	}
	return nil
}

func (s *eaState) keyCallsRCOwner() error {
	k, err := s.approveDevice(s.session)
	if err != nil {
		return err
	}
	req := httptest.NewRequest(http.MethodGet, "/rc/sessions", nil)
	signReq(req, k, nil)
	w, _, ok := s.b.rcOwnerWallet(req, nil)
	if !ok {
		s.topupWallet = ""
		return nil
	}
	s.topupWallet = w
	return nil
}

func (s *eaState) rcAcceptedFor(addr string) error {
	if s.topupWallet != walletForEmail(s.u(addr)) {
		return fmt.Errorf("the remote-control owner resolver returned %q, want %s's account wallet", s.topupWallet, addr)
	}
	return nil
}

// --- money in, money out ------------------------------------------------------------------

func (s *eaState) sessionReq() *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/billing/checkout", nil)
	req.Header.Set("Origin", testWebOrigin)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: s.session})
	return req
}

func (s *eaState) completesTopup(amt string) error {
	v, _ := strconv.ParseFloat(amt, 64)
	w, ok := s.b.checkoutWallet(s.sessionReq(), nil)
	if !ok {
		return fmt.Errorf("checkout resolved no wallet for the session")
	}
	s.topupWallet = w
	// the webhook credits the wallet the checkout session named
	if _, err := s.db.AddCredits(w, v); err != nil {
		return err
	}
	return nil
}

func (s *eaState) balanceReads(amt string) error {
	s.getDash(s.b.balance, "/balance", s.session)
	return s.showsBalance(amt)
}

func (s *eaState) paidServedDebited() error {
	before, _ := s.db.PeekBalance(s.topupWallet)
	rec := s.relayAccount("qwen3-32b", "n-1", false)
	if rec.Code != 200 {
		return fmt.Errorf("the paid request was not served: %d %s", rec.Code, rec.Body.String())
	}
	after, _ := s.db.PeekBalance(s.topupWallet)
	if !(after < before) {
		return fmt.Errorf("the topped-up wallet was not debited ($%.6f -> $%.6f)", before, after)
	}
	return nil
}

func (s *eaState) topupThenRelay() error {
	w, ok := s.b.checkoutWallet(s.sessionReq(), nil)
	if !ok {
		return fmt.Errorf("checkout resolved no wallet")
	}
	s.topupWallet = w
	s.balStart, _ = s.db.PeekBalance(s.wallet)
	s.relayAccount("qwen3-32b", "n-1", false)
	return nil
}

func (s *eaState) sameWallet() error {
	if s.resp.Code != 200 {
		return fmt.Errorf("the relay was not served: %d %s", s.resp.Code, s.resp.Body.String())
	}
	after, _ := s.db.PeekBalance(s.topupWallet)
	if !(after < s.balStart) || s.topupWallet != s.wallet {
		return fmt.Errorf("top-up wallet %q, debited wallet %q (moved %v)", s.topupWallet, s.wallet, s.balStart-after)
	}
	return nil
}

// --- the starter seed ---------------------------------------------------------------------

func (s *eaState) brandNewEmail(addr string) error {
	addr = s.u(addr)
	sess, err := s.signInByEmail(addr)
	if err != nil {
		return err
	}
	s.newSession, s.newWallet = sess, walletForEmail(addr)
	return nil
}

func (s *eaState) relaysUnderSeed() error {
	s.relayAs(s.newSession, nil, "qwen3-32b", "n-1", false, nil)
	return nil
}

func (s *eaState) exactlyOneSeed() error {
	n, err := s.seedEntries(s.newWallet)
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("%d seed ledger entries for %s, want exactly 1", n, s.newWallet)
	}
	return nil
}

func (s *eaState) relaysSecond() error {
	s.relayAs(s.newSession, nil, "qwen3-32b", "n-1", false, nil)
	return nil
}

func (s *eaState) ginaGitHubLinked(addr string) error {
	addr = s.u(addr)
	pub, _, _ := ed25519.GenerateKey(nil)
	gid := eaGID()
	if err := s.db.BindOwner(store.Owner{GitHubID: gid, Login: "gina-" + s.nonce, Pubkey: hex.EncodeToString(pub), Email: addr, EmailVerifiedAt: time.Now().Unix()}); err != nil {
		return err
	}
	s.ginaWallet = "u_gh_" + strconv.FormatInt(gid, 10)
	if _, err := s.db.AddCredits(s.ginaWallet, 5); err != nil {
		return err
	}
	sess, err := s.signInByEmail(addr)
	if err != nil {
		return err
	}
	s.ginaSession = sess
	s.balStart, _ = s.db.PeekBalance(s.ginaWallet)
	return nil
}

func (s *eaState) accountRelaysPaid() error {
	sess := s.session
	if s.ginaSession != "" {
		sess = s.ginaSession
	}
	s.relayAs(sess, nil, "qwen3-32b", "n-1", false, nil)
	return nil
}

func (s *eaState) githubIsPayer() error {
	if s.resp.Code != 200 {
		return fmt.Errorf("status %d %s", s.resp.Code, s.resp.Body.String())
	}
	after, _ := s.db.PeekBalance(s.ginaWallet)
	if !(after < s.balStart) {
		return fmt.Errorf("the GitHub wallet was not debited")
	}
	return nil
}

func (s *eaState) emailNamespaceUnused() error {
	rows, _ := s.db.LedgerOf(walletForEmail(s.u("gina@example.com")), nil, 100)
	if len(rows) != 0 {
		return fmt.Errorf("%d ledger rows on the email namespace wallet", len(rows))
	}
	return nil
}

// --- nothing else becomes able to spend ---------------------------------------------------

func (s *eaState) unboundKeypair() error {
	_, s.unboundKey, _ = ed25519.GenerateKey(nil)
	return nil
}

func (s *eaState) itRelaysPinned(model, pin string) error {
	k := s.unboundKey
	if k == nil {
		k = s.pendingKey
	}
	s.relayAs("", k, model, pin, false, nil)
	return nil
}

func (s *eaState) response401(msg string) error {
	if s.resp.Code != http.StatusUnauthorized || !strings.Contains(s.resp.Body.String(), msg) {
		return fmt.Errorf("status %d %s, want 401 %q", s.resp.Code, s.resp.Body.String(), msg)
	}
	return nil
}

func (s *eaState) claimsWallet(claimed, channel string) error {
	if _, ok := s.stations["n-1"]; ok {
		s.rcvdStart["n-1"] = s.upstreamHits("n-1")
	}
	switch channel {
	case "the X-Roger-User header":
		s.relayAs("", nil, "qwen3-32b", "n-1", false, map[string]string{protocol.HeaderUser: claimed, "Origin": testWebOrigin})
	case "a forged session cookie":
		// a well-formed payload naming the wallet, with a signature the broker never minted
		payload := "erin@example.com|0|" + claimed + "|" + strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
		forged := base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
		s.relayAs(forged, nil, "qwen3-32b", "n-1", false, nil)
	default:
		return fmt.Errorf("unknown channel %q", channel)
	}
	return nil
}

func (s *eaState) notServedBy(name string) error {
	if s.resp.Code == 200 {
		return fmt.Errorf("served (200): %s", s.resp.Body.String())
	}
	if s.upstreamHits(name)-s.rcvdStart[name] != 0 {
		return fmt.Errorf("%s was dispatched to", name)
	}
	return nil
}

func (s *eaState) noWalletDebited(w string) error {
	rows, _ := s.db.LedgerOf(w, nil, 100)
	for _, r := range rows {
		if r.Amount < 0 {
			return fmt.Errorf("wallet %s has a debit row %+v", w, r)
		}
	}
	return nil
}

func (s *eaState) pendingDeviceKey() error {
	_, priv, _ := ed25519.GenerateKey(nil)
	body := []byte(`{}`)
	req := httptest.NewRequest(http.MethodPost, "/auth/device/start", bytes.NewReader(body))
	signReq(req, priv, body)
	rec := httptest.NewRecorder()
	s.b.deviceStart(rec, req)
	if rec.Code != 200 {
		return fmt.Errorf("/auth/device/start = %d", rec.Code)
	}
	s.pendingKey = priv
	return nil
}

func (s *eaState) balanceUnchanged(addr string) error {
	bal, _ := s.db.PeekBalance(walletForEmail(s.u(addr)))
	if !approxF(bal, s.balStart) {
		return fmt.Errorf("%s's balance moved: $%.6f -> $%.6f", addr, s.balStart, bal)
	}
	return nil
}

func (s *eaState) deletedAndAnonymized(addr string) error {
	if err := s.setBalance(walletForEmail(s.u(addr)), 0); err != nil {
		return err
	}
	k, err := s.approveDevice(s.session)
	if err != nil {
		return err
	}
	body := []byte(`{"confirm":true}`)
	req := httptest.NewRequest(http.MethodPost, "/account/delete", bytes.NewReader(body))
	signReq(req, k, body)
	rec := httptest.NewRecorder()
	s.b.accountDelete(rec, req)
	if rec.Code != 200 {
		return fmt.Errorf("/account/delete = %d %s", rec.Code, rec.Body.String())
	}
	return nil
}

func (s *eaState) oldSessionRelays() error {
	s.balStart, _ = s.db.PeekBalance(s.wallet)
	s.relayAs(s.session, nil, "qwen3-32b", "n-1", false, nil)
	return nil
}

func (s *eaState) notServedFromOldWallet() error {
	if s.resp.Code == 200 {
		return fmt.Errorf("served from the deleted account's wallet: %s", s.resp.Body.String())
	}
	after, _ := s.db.PeekBalance(s.wallet)
	if after < s.balStart {
		return fmt.Errorf("the deleted account's wallet was debited")
	}
	return nil
}

func (s *eaState) unverifiedOwnerRow(addr string) error {
	pub, priv, _ := ed25519.GenerateKey(nil)
	if err := s.db.BindOwner(store.Owner{Pubkey: hex.EncodeToString(pub), Email: s.u(addr)}); err != nil {
		return err
	}
	s.unboundKey = priv
	return nil
}

func (s *eaState) keyBoundRelaysPaid() error {
	s.relayAs("", s.unboundKey, "qwen3-32b", "n-1", false, nil)
	return nil
}

// --- runner -------------------------------------------------------------------------------

func TestEmailAccountSpendingBDD(t *testing.T) {
	st := &eaState{foState: &foState{t: t, logs: &utLog{}}}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) { return ctx, st.reset() })
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				st.teardown()
				return ctx, nil
			})

			sc.Step(`^a broker with a configured mailer$`, st.configuredMailer)
			sc.Step(`^a paid station "([^"]+)" on air for "([^"]+)" at in \$([0-9.]+) out \$([0-9.]+) per 1M$`, st.paidStation)
			sc.Step(`^a free station "([^"]+)" on air for "([^"]+)" at in \$0 out \$0$`, st.freeStation)
			sc.Step(`^"([^"]+)" has signed in with an emailed code$`, st.signedInWithCode)
			sc.Step(`^"([^"]+)"'s account wallet holds \$([0-9.]+)$`, st.walletHolds)

			sc.Step(`^"([^"]+)" calls through (.+)$`, st.callsThrough)
			sc.Step(`^the caller relays a chat completion for "([^"]+)" pinned to "([^"]+)"$`, st.callerRelaysPinned)
			sc.Step(`^the response is 200 from "([^"]+)"$`, st.response200From)
			sc.Step(`^a receipt is recorded for the request$`, st.receiptRecorded)
			sc.Step(`^the cost is debited from "([^"]+)"'s account wallet$`, st.costDebitedFrom)
			sc.Step(`^no 401 "log in to spend" is returned$`, st.no401LogIn)
			sc.Step(`^the account relays a chat completion for "([^"]+)" pinned to "([^"]+)"$`, st.accountRelaysPinned)
			sc.Step(`^the response is 402 with error code "([^"]+)"$`, st.response402Code)
			sc.Step(`^the response is not 401$`, st.responseNot401)
			sc.Step(`^X-RogerAI-Cost is "([^"]+)"$`, st.costHeaderIs)
			sc.Step(`^the account relays a streaming chat completion for "([^"]+)" pinned to "([^"]+)"$`, st.accountStreamsPinned)
			sc.Step(`^the stream ends with the broker's usage chunk carrying the receipt$`, st.streamUsageChunk)
			sc.Step(`^a second paid station "([^"]+)" on air for "([^"]+)"$`, st.secondPaidStation)
			sc.Step(`^"([^"]+)" answers the next request with an upstream 429$`, st.answers429Next)
			sc.Step(`^the account relays a chat completion for "([^"]+)"$`, st.accountRelays)
			sc.Step(`^the failover plan was not trimmed to free stations$`, st.planNotTrimmedToFree)
			sc.Step(`^a paid voice station "([^"]+)" on air for "([^"]+)"$`, st.paidVoiceStation)
			sc.Step(`^the account requests speech from "([^"]+)"$`, st.requestsSpeech)
			sc.Step(`^the response is 200$`, st.response200)
			sc.Step(`^no 403 "sign in to use this voice model" is returned$`, st.no403Voice)
			sc.Step(`^an approved Tower serves "([^"]+)" at out \$([0-9.]+) and no direct station serves it$`, st.towerServesOnly)
			sc.Step(`^the bridge serves it and bills "([^"]+)"'s account wallet$`, st.bridgeBills)
			sc.Step(`^when a direct paid station also serves "([^"]+)", the direct path serves the account too$`, st.directServesToo)

			sc.Step(`^the account GETs (/me|/balance|/usage|/console)$`, st.accountGETs)
			sc.Step(`^the response says logged_in true$`, st.saysLoggedIn)
			sc.Step(`^it shows the account wallet's balance \$([0-9.]+)$`, st.showsBalance)
			sc.Step(`^the account has spent \$([0-9.]+) this month$`, st.spentThisMonth)
			sc.Step(`^the response is 200 with this month's spend \$([0-9.]+)$`, st.usage200Spend)
			sc.Step(`^the account made a paid request$`, st.madePaidRequest)
			sc.Step(`^the consumer view lists that request$`, st.consoleListsRequest)

			sc.Step(`^the account sets a monthly spend limit of \$([0-9.]+)$`, st.setsMonthlyLimit)
			sc.Step(`^the limit is stored for "([^"]+)"'s account wallet$`, st.limitStoredFor)
			sc.Step(`^the account has spent \$([0-9.]+) this month and relays a paid request$`, st.spentAndRelays)
			sc.Step(`^the response is 402 naming the monthly spend limit$`, st.response402Monthly)
			sc.Step(`^the per-identity rate limit is (\d+) requests with burst (\d+)$`, st.perIdentityRateLimit)
			sc.Step(`^the browser session sends (\d+) requests and a bound CLI key sends (\d+) more$`, st.sessionThenKey)
			sc.Step(`^the third request is 429 "rate limit exceeded"$`, st.thirdIs429)
			sc.Step(`^the account's bound CLI key calls the remote-control owner endpoint$`, st.keyCallsRCOwner)
			sc.Step(`^the call is accepted for "([^"]+)"'s account wallet$`, st.rcAcceptedFor)

			sc.Step(`^the account completes a \$([0-9.]+) top-up$`, st.completesTopup)
			sc.Step(`^its balance reads \$([0-9.]+)$`, st.balanceReads)
			sc.Step(`^a paid request is served and debited from that balance$`, st.paidServedDebited)
			sc.Step(`^the account starts a top-up and then relays a paid request$`, st.topupThenRelay)
			sc.Step(`^the wallet credited by the top-up and the wallet debited by the relay are the same$`, st.sameWallet)

			sc.Step(`^a brand-new email account "([^"]+)" with no top-up$`, st.brandNewEmail)
			sc.Step(`^it relays a paid request that costs less than the starter seed$`, st.relaysUnderSeed)
			sc.Step(`^exactly one seed ledger entry exists for its wallet$`, st.exactlyOneSeed)
			sc.Step(`^it relays a second paid request$`, st.relaysSecond)
			sc.Step(`^"([^"]+)" is verified on an account that also has a GitHub link$`, st.ginaGitHubLinked)
			sc.Step(`^the account relays a paid request$`, st.accountRelaysPaid)
			sc.Step(`^the GitHub account wallet is the payer$`, st.githubIsPayer)
			sc.Step(`^the email wallet namespace is not used$`, st.emailNamespaceUnused)

			sc.Step(`^a signed keypair bound to no account$`, st.unboundKeypair)
			sc.Step(`^it relays a chat completion for "([^"]+)" pinned to "([^"]+)"$`, st.itRelaysPinned)
			sc.Step(`^the response is 401 "([^"]+)"$`, st.response401)
			sc.Step(`^an unauthenticated request claims the wallet "([^"]+)" through (the X-Roger-User header|a forged session cookie)$`, st.claimsWallet)
			sc.Step(`^it is not served by "([^"]+)"$`, st.notServedBy)
			sc.Step(`^no wallet named "([^"]+)" is debited$`, st.noWalletDebited)
			sc.Step(`^a signed keypair that started a device login but was never approved$`, st.pendingDeviceKey)
			sc.Step(`^"([^"]+)"'s balance is unchanged$`, st.balanceUnchanged)
			sc.Step(`^"([^"]+)"'s account has been deleted and anonymized$`, st.deletedAndAnonymized)
			sc.Step(`^its old browser session relays a paid request$`, st.oldSessionRelays)
			sc.Step(`^the request is not served from the old wallet$`, st.notServedFromOldWallet)
			sc.Step(`^an owner row carries "([^"]+)" with no verification time$`, st.unverifiedOwnerRow)
			sc.Step(`^a key bound to that owner relays a paid request$`, st.keyBoundRelaysPaid)
		},
		Options: &godog.Options{
			Format: "pretty", Paths: []string{"../../features/money/email_account_spending.feature"},
			Tags: "~@needs-pr125", TestingT: t, Strict: true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("email_account_spending.feature failed")
	}
}
