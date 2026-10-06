package main

// cap_notice_emails_bdd_test.go makes features/ops/cap_notice_emails.feature EXECUTABLE against
// the REAL broker and store (in-memory, or Postgres when ROGERAI_TEST_DATABASE_URL is set), the
// shared store over miniredis, real stations, and a capturing mail provider: every message the
// mailer actually POSTs is recorded with its recipient and subject.
//
// The account kinds are built exactly as their sign-in paths build them: a GitHub- or
// Apple-linked owner row bound to a CLI key (the provider's reported address on the row), an
// email account signed in with the real emailed code (its CLI keys bound by the real device
// approval). Spend before the scenario is recorded through the same store calls a relay's
// settle makes (hold, then finalize with a receipt).
//
// Observation note: scenarios that move the calendar ("a new month", "the month boundary is
// UTC") drive the cap-notice entry point with the scenario clock, because the relay's cap check
// reads wall time and the ledger dates spend with wall time; what they pin is the once-per-
// threshold-per-month key, which only the notice path owns. Every other scenario crosses the
// threshold with a real paid relay.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/store"
)

type cnState struct {
	*eaState

	acctWallet  string
	acctAddr    string
	acctKey     ed25519.PrivateKey
	acctSess    string
	acctOwner   store.Owner
	grantSecret string

	sceneNow time.Time

	mailDelay  time.Duration
	mailStatus int
	posts      int64

	relayDur time.Duration
	logMark  int

	// mailGen is this scenario's capture generation. A mailer built for an earlier scenario can
	// still be delivering (its queue runs off the relay path and retries), and its sends must
	// never land in a later scenario's capture: they are tagged with the generation the mailer
	// was built in and dropped once the scenario has moved on.
	mailGen int64

	ownerReads int64 // owner lookups made by the last counted relay

	authorizeURL string // where the last web GitHub sign-in redirected
}

type cnMail struct{ to, subject, text string }

func (s *cnState) reset() error {
	if err := s.eaState.reset(); err != nil {
		return err
	}
	s.acctWallet, s.acctAddr, s.acctKey, s.acctSess = "", "", nil, ""
	s.acctOwner = store.Owner{}
	s.authorizeURL = ""
	s.sceneNow = time.Now()
	s.mailDelay, s.mailStatus = 0, 200
	atomic.StoreInt64(&s.posts, 0)
	s.relayDur = 0
	s.logMark = len(s.logs.String())
	atomic.AddInt64(&s.mailGen, 1)
	s.b.mail = s.capturingMailer()
	// a prompt large enough that the station's 5000-token claim is billed in full (the byte
	// floor never clamps it), so one relay costs about $0.20 at the Background's $40/1M in
	s.tokens = 6000
	return nil
}

// capturingMailer is an ENABLED mailer whose provider POSTs are recorded, with a scriptable
// latency and status.
func (s *cnState) capturingMailer() *mailer {
	m := s.capturingMailerRaw()
	m.retries = defaultEmailRetries // the production retry budget (loadMailer)
	return m
}

func (s *cnState) capturingMailerRaw() *mailer {
	gen := atomic.LoadInt64(&s.mailGen)
	return enabledMailer(func(r *http.Request) (*http.Response, error) {
		atomic.AddInt64(&s.posts, 1)
		if d := s.mailDelay; d > 0 {
			time.Sleep(d)
		}
		body, _ := io.ReadAll(r.Body)
		if s.mailStatus != 200 {
			return &http.Response{StatusCode: s.mailStatus, Body: http.NoBody, Header: http.Header{}}, nil
		}
		s.mailMu.Lock()
		if atomic.LoadInt64(&s.mailGen) == gen {
			s.mails = append(s.mails, string(body))
		}
		s.mailMu.Unlock()
		return &http.Response{StatusCode: 200, Body: http.NoBody, Header: http.Header{}}, nil
	})
}

// drainMail waits (bounded) for the scenario's mailer to finish what it queued, so a slow
// delivery is counted by the scenario that caused it rather than leaking into the next one.
func (s *cnState) drainMail() {
	if s.b == nil || s.b.mail == nil {
		return
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && !s.b.mail.idle() {
		time.Sleep(10 * time.Millisecond)
	}
}

func (s *cnState) sent() []cnMail {
	s.mailMu.Lock()
	defer s.mailMu.Unlock()
	var out []cnMail
	for _, raw := range s.mails {
		var p struct {
			To      []string `json:"to"`
			Subject string   `json:"subject"`
			Text    string   `json:"text"`
			HTML    string   `json:"html"`
		}
		if json.Unmarshal([]byte(raw), &p) != nil {
			continue
		}
		to := ""
		if len(p.To) > 0 {
			to = p.To[0]
		}
		out = append(out, cnMail{to: to, subject: p.Subject, text: p.Text + "\n" + p.HTML})
	}
	return out
}

// settled waits for the async sender to go idle so a count is final.
func (s *cnState) settled() {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !s.b.mail.idle() {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(30 * time.Millisecond)
}

func (s *cnState) countTo(subject, to string) int {
	s.settled()
	n := 0
	for _, m := range s.sent() {
		if m.subject == subject && (to == "" || m.to == to) {
			n++
		}
	}
	return n
}

// --- account construction ----------------------------------------------------------------

func (s *cnState) makeAccount(kind, addr string) error {
	addr = s.u(addr)
	pub, priv, _ := ed25519.GenerateKey(nil)
	ph := hex.EncodeToString(pub)
	gid := eaGID()
	switch kind {
	case "GitHub-linked":
		s.acctOwner = store.Owner{GitHubID: gid, Login: "gh-" + s.nonce, Pubkey: ph, Email: addr}
	case "Apple-linked":
		s.acctOwner = store.Owner{AppleSub: "apple-" + s.nonce, Pubkey: ph, Email: addr}
	case "GitHub-and-Apple-linked":
		s.acctOwner = store.Owner{GitHubID: gid, Login: "gha-" + s.nonce, AppleSub: "apple-" + s.nonce, Pubkey: ph, Email: addr}
	case "GitHub-linked with a separate verified email":
		s.acctOwner = store.Owner{GitHubID: gid, Login: "gh2-" + s.nonce, Pubkey: ph, Email: addr, EmailVerifiedAt: time.Now().Unix()}
	case "email-login":
		sess, err := s.signInByEmail(addr)
		if err != nil {
			return err
		}
		s.acctSess, s.session = sess, sess
		k, err := s.approveDevice(sess)
		if err != nil {
			return err
		}
		s.acctKey, s.acctAddr, s.acctWallet = k, addr, walletForEmail(addr)
		return s.fundAcct()
	default:
		return fmt.Errorf("unknown account kind %q", kind)
	}
	if err := s.db.BindOwner(s.acctOwner); err != nil {
		return err
	}
	w, ok := accountWalletForOwner(s.acctOwner)
	if !ok {
		return fmt.Errorf("no account wallet for the %s owner", kind)
	}
	s.acctKey, s.acctAddr, s.acctWallet = priv, addr, w
	return s.fundAcct()
}

func (s *cnState) fundAcct() error {
	if _, err := s.db.BalanceOf(s.acctWallet, s.b.seedFunds); err != nil {
		return err
	}
	_, err := s.db.AddCredits(s.acctWallet, 50)
	return err
}

func (s *cnState) setCap(amt string) error {
	v, _ := strconv.ParseFloat(amt, 64)
	return s.db.SetMonthlyCap(s.acctWallet, v)
}

func (s *cnState) spent(amt string) error {
	v, _ := strconv.ParseFloat(amt, 64)
	return s.settleSpend(s.acctWallet, v)
}

// relayPaid sends one paid relay for the account through its CLI key (or the named identity).
func (s *cnState) relayPaid(key ed25519.PrivateKey, session string) *httptest.ResponseRecorder {
	start := time.Now()
	rec := s.relayAs(session, key, "qwen3-32b", "n-1", false, nil)
	s.relayDur = time.Since(start)
	return rec
}

// --- Background ---------------------------------------------------------------------------

func (s *cnState) capturingMailerGiven() error { return nil }

func (s *cnState) paidStationOnAir(name, model string) error {
	// $40/1M in: one relay (5000 prompt tokens) costs about $0.20; a low out price keeps the
	// hold small so a request near the cap is still admitted.
	s.standUp(name, stationOpts{model: model, priceIn: 40, priceOut: 0.1, ctx: 8192})
	return nil
}

func (s *cnState) dateIs(day string) error {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		return err
	}
	s.sceneNow = t.Add(12 * time.Hour)
	return nil
}

// --- every account kind -------------------------------------------------------------------

func (s *cnState) accountWithCap(kind, addr, cap string) error {
	if err := s.makeAccount(kind, addr); err != nil {
		return err
	}
	return s.setCap(cap)
}

func (s *cnState) accountWithCapSpent(kind, addr, cap, spent string) error {
	if err := s.accountWithCap(kind, addr, cap); err != nil {
		return err
	}
	return s.spent(spent)
}

func (s *cnState) hasSpent(amt string) error { return s.spent(amt) }

func (s *cnState) relaysCrossing80() error {
	rec := s.relayPaid(s.acctKey, "")
	if rec.Code != 200 {
		return fmt.Errorf("the paid relay was not served: %d %s", rec.Code, rec.Body.String())
	}
	return nil
}

func (s *cnState) exactlyOneTo(subject, to string) error { return s.exactlyOneMapped(subject, s.u(to)) }

// exactlyOneMapped takes an address that is already the scenario's unique form.
func (s *cnState) exactlyOneMapped(subject, to string) error {
	if n := s.countTo(subject, to); n != 1 {
		return fmt.Errorf("%d %q message(s) to %s, want exactly 1 (all sent: %+v)", n, subject, to, s.subjects())
	}
	return nil
}

func (s *cnState) subjects() []string {
	var out []string
	for _, m := range s.sent() {
		out = append(out, m.subject+" -> "+m.to)
	}
	return out
}

func (s *cnState) statesSpendAndCap() error {
	for _, m := range s.sent() {
		if m.subject == "Monthly spend at 80%" && strings.Contains(m.text, "$10.00") {
			return nil
		}
	}
	return fmt.Errorf("no 80%% message states the $10.00 cap")
}

func (s *cnState) relaysPaid() error {
	s.relayPaid(s.acctKey, "")
	return nil
}

func (s *cnState) response402Monthly() error {
	if s.resp.Code != http.StatusPaymentRequired || !strings.Contains(s.resp.Body.String(), "monthly spend limit") {
		return fmt.Errorf("status %d %s, want 402 naming the monthly spend limit", s.resp.Code, s.resp.Body.String())
	}
	return nil
}

func (s *cnState) emailAccountCapSpent(addr, cap, spent string) error {
	return s.accountWithCapSpent("email-login", addr, cap, spent)
}

func (s *cnState) identityRelaysCrossing(identity string) error {
	var rec *httptest.ResponseRecorder
	switch identity {
	case "the account's browser session":
		rec = s.relayPaid(nil, s.acctSess)
	case "a CLI device key bound to the account":
		rec = s.relayPaid(s.acctKey, "")
	case "a second device key bound to the same account":
		k, err := s.approveDevice(s.acctSess)
		if err != nil {
			return err
		}
		rec = s.relayPaid(k, "")
	default:
		return fmt.Errorf("unknown identity %q", identity)
	}
	if rec.Code != 200 {
		return fmt.Errorf("the paid relay was not served: %d %s", rec.Code, rec.Body.String())
	}
	return nil
}

func (s *cnState) githubCapSpent(addr, cap, spent string) error {
	return s.accountWithCapSpent("GitHub-linked", addr, cap, spent)
}

func (s *cnState) paidVoiceOnAir(name, model string) error { return s.paidVoiceStation(name, model) }

func (s *cnState) speechCrossing() error {
	body := []byte(`{"model":"voice","voice":"voice","input":"` + strings.Repeat("a", 4000) + `","response_format":"mp3"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", bytes.NewReader(body))
	signReq(req, s.acctKey, body)
	rec := httptest.NewRecorder()
	s.b.audioRelay(rec, req)
	s.resp = rec
	if rec.Code != 200 {
		return fmt.Errorf("the paid speech request was not served: %d %s", rec.Code, rec.Body.String())
	}
	return nil
}

// --- once per threshold per month ---------------------------------------------------------

func (s *cnState) relaysNMore(n int) error {
	for i := 0; i < n; i++ {
		s.relayPaid(s.acctKey, "")
	}
	return nil
}

func (s *cnState) exactlyOneThisMonth(subject, to string) error { return s.exactlyOneTo(subject, to) }

func (s *cnState) crosses80Then100() error {
	if err := s.spent("7.90"); err != nil {
		return err
	}
	if rec := s.relayPaid(s.acctKey, ""); rec.Code != 200 {
		return fmt.Errorf("crossing 80%%: %d %s", rec.Code, rec.Body.String())
	}
	if err := s.spent("2.00"); err != nil {
		return err
	}
	s.relayPaid(s.acctKey, "")
	return nil
}

func (s *cnState) one80One100() error {
	if err := s.exactlyOneMapped("Monthly spend at 80%", s.acctAddr); err != nil {
		return err
	}
	return s.exactlyOneMapped("Monthly spend limit reached", s.acctAddr)
}

// noticeAt drives the cap-notice entry point for the account at a scenario clock (see header).
func (s *cnState) noticeAt(threshold string, now time.Time) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	signReq(req, s.acctKey, nil)
	cnNotice(s.b, req, s.acctWallet, threshold, 8.10, 10, now)
}

func (s *cnState) wasSentIn(addr, month string) error {
	if err := s.makeAccount("GitHub-linked", addr); err != nil {
		return err
	}
	if err := s.setCap("10"); err != nil {
		return err
	}
	t, err := time.Parse("January 2006", month)
	if err != nil {
		return err
	}
	s.noticeAt("80", t.Add(10*24*time.Hour))
	if n := s.countTo("Monthly spend at 80%", s.acctAddr); n != 1 {
		return fmt.Errorf("setup: %d 80%% messages for %s, want 1", n, month)
	}
	return nil
}

func (s *cnState) octoberSpendCrosses(month string) error {
	s.noticeAt("80", s.sceneNow)
	return nil
}

func (s *cnState) oneSentFor(month string) error {
	if n := s.countTo("Monthly spend at 80%", s.acctAddr); n != 2 {
		return fmt.Errorf("%d 80%% messages in total, want the earlier one plus exactly one for %s", n, month)
	}
	return nil
}

func (s *cnState) boundaryGiven(stamp, addr string) error {
	t, err := time.Parse("2006-01-02 15:04:05", stamp)
	if err != nil {
		return err
	}
	if err := s.makeAccount("GitHub-linked", addr); err != nil {
		return err
	}
	if err := s.setCap("10"); err != nil {
		return err
	}
	s.sceneNow = t.UTC()
	s.noticeAt("80", s.sceneNow)
	if n := s.countTo("Monthly spend at 80%", s.acctAddr); n != 1 {
		return fmt.Errorf("setup: %d October 80%% messages, want 1", n)
	}
	return nil
}

func (s *cnState) clockReachesNov(stamp string) error {
	t, err := time.Parse("2006-01-02 15:04:05", stamp)
	if err != nil {
		return err
	}
	s.noticeAt("80", t.UTC())
	return nil
}

func (s *cnState) oneForNovember() error {
	if n := s.countTo("Monthly spend at 80%", s.acctAddr); n != 2 {
		return fmt.Errorf("%d 80%% messages, want October's plus exactly one for November", n)
	}
	return nil
}

func (s *cnState) twoInstances() error { s.instanceB(); return nil }

func (s *cnState) eachInstanceAtOnce() error {
	b1, b2 := s.b, s.b2
	if b2 == nil {
		return fmt.Errorf("no instance B")
	}
	b2.mail = s.capturingMailer()
	done := make(chan *httptest.ResponseRecorder, 2)
	for _, bk := range []*broker{b1, b2} {
		go func(bk *broker) {
			body := s.chatBody("qwen3-32b", false)
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
			signReq(req, s.acctKey, body)
			req.Header.Set("X-Roger-Node", s.st("n-1").id)
			rec := httptest.NewRecorder()
			bk.relay(rec, req)
			done <- rec
		}(bk)
	}
	for i := 0; i < 2; i++ {
		if r := <-done; r.Code != 200 {
			return fmt.Errorf("a relay was not served: %d %s", r.Code, r.Body.String())
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !(b1.mail.idle() && b2.mail.idle()) {
		time.Sleep(10 * time.Millisecond)
	}
	return nil
}

func (s *cnState) sentThisMonthAlready(addr string) error {
	if err := s.accountWithCapSpent("GitHub-linked", addr, "10.00", "8.10"); err != nil {
		return err
	}
	if rec := s.relayPaid(s.acctKey, ""); rec.Code != 200 {
		return fmt.Errorf("setup relay: %d %s", rec.Code, rec.Body.String())
	}
	if n := s.countTo("Monthly spend at 80%", s.acctAddr); n != 1 {
		return fmt.Errorf("setup: %d 80%% messages, want 1", n)
	}
	return nil
}

func (s *cnState) restartsAndRelays() error {
	nb := s.newBroker() // a fresh process: same store and shared store, empty memory
	nb.recount = s.b.recount
	for id, n := range s.b.nodes {
		nb.nodes[id] = n
		nb.lastSeen[id] = time.Now()
		nb.tunnels[id] = s.b.tunnels[id]
	}
	nb.mail = s.capturingMailer()
	s.b = nb
	if rec := s.relayPaid(s.acctKey, ""); rec.Code != 200 {
		return fmt.Errorf("relay after restart: %d %s", rec.Code, rec.Body.String())
	}
	return nil
}

func (s *cnState) noSecond80() error {
	if n := s.countTo("Monthly spend at 80%", s.acctAddr); n != 1 {
		return fmt.Errorf("%d 80%% messages, want still exactly 1", n)
	}
	return nil
}

func (s *cnState) sharedUnreachable() error {
	if s.mr != nil {
		s.mr.Close()
		s.mr = nil
	}
	return nil
}

func (s *cnState) crosses80Twice(addr string) error {
	if err := s.accountWithCapSpent("GitHub-linked", addr, "10.00", "8.10"); err != nil {
		return err
	}
	for i := 0; i < 2; i++ {
		if rec := s.relayPaid(s.acctKey, ""); rec.Code != 200 {
			return fmt.Errorf("relay %d with the shared store down: %d %s", i+1, rec.Code, rec.Body.String())
		}
	}
	return nil
}

func (s *cnState) atMostOne() error {
	if n := s.countTo("Monthly spend at 80%", s.acctAddr); n > 1 {
		return fmt.Errorf("%d 80%% messages, want at most 1", n)
	}
	return nil
}

func (s *cnState) servedNormally() error {
	if s.resp == nil || s.resp.Code != 200 {
		return fmt.Errorf("the relay was not served normally")
	}
	return nil
}

// --- no address, no cap ------------------------------------------------------------------

func (s *cnState) appleNoEmailCapSpent(cap, spent string) error {
	if err := s.accountWithCap("Apple-linked", "", cap); err != nil {
		return err
	}
	return s.spent(spent)
}

func (s *cnState) noMessage() error {
	s.settled()
	for _, m := range s.sent() {
		if strings.HasPrefix(m.subject, "Monthly spend") {
			return fmt.Errorf("a cap notice was sent: %q to %s", m.subject, m.to)
		}
	}
	return nil
}

func (s *cnState) servedWithNearHeader() error {
	if s.resp.Code != 200 || s.resp.Header().Get("X-RogerAI-Monthly-Notice") == "" {
		return fmt.Errorf("status %d, notice header %q", s.resp.Code, s.resp.Header().Get("X-RogerAI-Monthly-Notice"))
	}
	return nil
}

func (s *cnState) unboundKeypair() error { return s.eaState.unboundKeypair() }

func (s *cnState) relaysToFree() error {
	if _, ok := s.stations["n-free"]; !ok {
		s.standUp("n-free", stationOpts{model: "qwen3-32b"})
	}
	for i := 0; i < 3; i++ {
		s.relayAs("", s.unboundKey, "qwen3-32b", "n-free", false, nil)
	}
	return nil
}

func (s *cnState) noCapNotice() error { return s.noMessage() }

func (s *cnState) deletedAfterCap(addr string) error {
	if err := s.accountWithCap("GitHub-linked", addr, "10.00"); err != nil {
		return err
	}
	if err := s.setBalance(s.acctWallet, 0); err != nil {
		return err
	}
	if ok, err := s.db.DeleteAccount(s.acctOwner.Login); err != nil || !ok {
		return fmt.Errorf("delete account: ok=%v err=%v", ok, err)
	}
	return nil
}

func (s *cnState) attributedToOldWallet() error {
	s.relayPaid(s.acctKey, "")
	s.noticeAt("80", time.Now())
	return nil
}

func (s *cnState) noMessageTo(addr string) error {
	addr = s.u(addr)
	s.settled()
	for _, m := range s.sent() {
		if m.to == addr {
			return fmt.Errorf("a message was sent to %s: %q", addr, m.subject)
		}
	}
	return nil
}

func (s *cnState) emailUnverifiedOnly() error {
	// An email-login account exists only by PROVING its address with the mailed code, so an
	// email-login account "whose only address on file is unverified" cannot be built through
	// any real path. Reported to the founder rather than faked.
	return fmt.Errorf("cannot construct: an email-login account always holds a code-verified address")
}

func (s *cnState) crosses80Cap() error { return nil }

// --- the notice never costs the relay anything -------------------------------------------

func (s *cnState) providerSlow(secs int) error {
	s.mailDelay = time.Duration(secs) * time.Second
	s.b.mail = s.capturingMailer()
	return nil
}

func (s *cnState) githubCrosses80() error {
	if err := s.accountWithCapSpent("GitHub-linked", "gh@example.com", "10.00", "7.90"); err != nil {
		return err
	}
	s.relayPaid(s.acctKey, "")
	return nil
}

func (s *cnState) returnedWithoutWaiting() error {
	if s.resp.Code != 200 {
		return fmt.Errorf("status %d %s", s.resp.Code, s.resp.Body.String())
	}
	if s.relayDur > 5*time.Second {
		return fmt.Errorf("the relay took %s, waiting on the mail provider", s.relayDur)
	}
	// the notice must still have been attempted (else this passes vacuously)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && atomic.LoadInt64(&s.posts) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if atomic.LoadInt64(&s.posts) == 0 {
		return fmt.Errorf("no notice was ever attempted")
	}
	return nil
}

func (s *cnState) provider500() error {
	s.mailStatus = 500
	s.b.mail = s.capturingMailer()
	return nil
}

func (s *cnState) retriedOnTransactional() error {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		st := s.b.mail.emailStats()
		if r, _ := st["email_retries"].(int64); r > 0 {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("the failed notice was never retried (posts=%d, stats=%v)", atomic.LoadInt64(&s.posts), s.b.mail.emailStats())
}

func (s *cnState) alertsQueued(n int) error {
	s.mailDelay = 0
	s.b.mail = s.capturingMailer()
	if err := s.makeAccount("GitHub-linked", "gh@example.com"); err != nil {
		return err
	}
	// the queue's test seam: the sender sits idle while paused, so the alerts are queued
	// before anything is delivered
	s.b.mail.q.mu.Lock()
	s.b.mail.q.paused = true
	s.b.mail.q.mu.Unlock()
	for i := 0; i < n; i++ {
		s.b.mail.sendAlertEmail("founder@example.com", fmt.Sprintf("alert %d", i), "", "x")
	}
	return nil
}

func (s *cnState) capNoticeEnqueued() error {
	if err := s.setCap("10"); err != nil {
		return err
	}
	s.noticeAt("80", time.Now())
	s.b.mail.q.mu.Lock()
	s.b.mail.q.paused = false
	s.b.mail.q.mu.Unlock()
	s.b.mail.kick()
	return nil
}

func (s *cnState) deliveredBeforeAlerts() error {
	s.settled()
	ms := s.sent()
	for _, m := range ms {
		if m.subject == "Monthly spend at 80%" {
			return nil
		}
		if strings.HasPrefix(m.subject, "alert ") {
			return fmt.Errorf("an ops alert was delivered before the cap notice (%v)", s.subjects())
		}
	}
	return fmt.Errorf("the cap notice was never delivered (%v)", s.subjects())
}

func (s *cnState) sentThe80(addr string) error {
	if err := s.accountWithCapSpent("GitHub-linked", addr, "10.00", "8.10"); err != nil {
		return err
	}
	if rec := s.relayPaid(s.acctKey, ""); rec.Code != 200 {
		return fmt.Errorf("relay: %d %s", rec.Code, rec.Body.String())
	}
	if n := s.countTo("Monthly spend at 80%", s.acctAddr); n != 1 {
		return fmt.Errorf("%d 80%% messages, want 1", n)
	}
	return nil
}

func (s *cnState) noIdentifiersInMessage() error {
	for _, m := range s.sent() {
		if m.subject != "Monthly spend at 80%" {
			continue
		}
		for _, bad := range []string{s.acctWallet, s.acctOwner.Pubkey} {
			if bad != "" && strings.Contains(m.text, bad) {
				return fmt.Errorf("the message contains %q", bad)
			}
		}
		return nil
	}
	return fmt.Errorf("no 80%% message to inspect")
}

func (s *cnState) addrNotLogged(addr string) error {
	if strings.Contains(s.logs.String()[s.logMark:], s.u(addr)) {
		return fmt.Errorf("a log line contains %s", addr)
	}
	return nil
}

func TestCapNoticeEmailsBDD(t *testing.T) {
	st := &cnState{eaState: &eaState{foState: &foState{t: t, logs: &utLog{}}}}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) { return ctx, st.reset() })
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				st.drainMail()
				st.teardown()
				return ctx, nil
			})
			sc.Step(`^a broker with a configured mailer that captures every message$`, st.capturingMailerGiven)
			sc.Step(`^a paid station "([^"]+)" on air for "([^"]+)"$`, st.paidStationOnAir)
			sc.Step(`^the date is (\d{4}-\d{2}-\d{2}) UTC$`, st.dateIs)

			sc.Step(`^a (GitHub-linked|Apple-linked|email-login|GitHub-linked with a separate verified email) account "([^"]+)" with a monthly cap of \$([0-9.]+)$`, st.accountWithCap)
			sc.Step(`^the account has spent \$([0-9.]+) this month$`, st.hasSpent)
			sc.Step(`^the account relays a paid request that brings its spend to at least \$([0-9.]+)$`, func(string) error { return st.relaysCrossing80() })
			sc.Step(`^exactly one "([^"]+)" message is sent to "([^"]+)"$`, st.exactlyOneTo)
			sc.Step(`^the message states the spend and the cap$`, st.statesSpendAndCap)
			sc.Step(`^the account relays a paid request$`, st.relaysPaid)
			sc.Step(`^the response is 402 naming the monthly spend limit$`, st.response402Monthly)
			sc.Step(`^an email-login account "([^"]+)" with a monthly cap of \$([0-9.]+) and \$([0-9.]+) spent$`, st.emailAccountCapSpent)
			sc.Step(`^(the account's browser session|a CLI device key bound to the account|a second device key bound to the same account) relays a paid request that crosses 80%$`, st.identityRelaysCrossing)
			sc.Step(`^a GitHub-linked account "([^"]+)" with a monthly cap of \$([0-9.]+) and \$([0-9.]+) spent$`, st.githubCapSpent)
			sc.Step(`^a paid voice station "([^"]+)" on air for "([^"]+)"$`, st.paidVoiceOnAir)
			sc.Step(`^a GitHub-linked station owner "([^"]+)" with a monthly cap of \$([0-9.]+) and \$([0-9.]+) spent$`, st.stationOwnerCapSpent)
			sc.Step(`^the owner issued a (custom-priced|free) grant$`, st.issueGrant)
			sc.Step(`^a bot relays a paid request with the grant that brings the owner's spend past \$[0-9.]+$`, func() error { return st.botRelaysWithGrant(true) })
			sc.Step(`^a bot relays a request with the grant$`, func() error { return st.botRelaysWithGrant(false) })
			sc.Step(`^a (GitHub-linked|Apple-linked|GitHub-and-Apple-linked) account "([^"]+)" whose address predates typed-address tracking, with a monthly cap of \$([0-9.]+) and \$([0-9.]+) spent$`, st.legacyAccountCapSpent)
			sc.Step(`^the account signs in with (GitHub|Apple) and the provider reports "([^"]+)"$`, st.signsInWith)
			sc.Step(`^the account signs in with (GitHub|Apple) and the provider reports no address$`, func(p string) error { return st.signsInWith(p, "") })
			sc.Step(`^the account requests speech that brings its spend past \$([0-9.]+)$`, func(string) error { return st.speechCrossing() })
			sc.Step(`^a visitor starts the web GitHub sign-in$`, st.startsWebGitHubSignIn)
			sc.Step(`^the GitHub authorize request asks for the scopes "([^"]+)"$`, st.authorizeAsksScopes)
			sc.Step(`^the account signs in with GitHub, which shows (no public address|the public address "[^"]+") and lists the addresses:$`, st.signsInWithGitHubList)
			sc.Step(`^the account signs in with GitHub, which shows (no public address|the public address "[^"]+") and answers the address list with status (\d+)$`, st.signsInWithGitHubListStatus)

			sc.Step(`^the account relays (\d+) more paid requests this month$`, st.relaysNMore)
			sc.Step(`^exactly one "([^"]+)" message has been sent to "([^"]+)" this month$`, st.exactlyOneThisMonth)
			sc.Step(`^the account's spend crosses 80% and later reaches 100% in the same month$`, st.crosses80Then100)
			sc.Step(`^exactly one 80% message and exactly one 100% message have been sent$`, st.one80One100)
			sc.Step(`^"([^"]+)" was sent the 80% notice in ([A-Z][a-z]+ \d{4})$`, st.wasSentIn)
			sc.Step(`^the account's ([A-Z][a-z]+) spend crosses 80%$`, st.octoberSpendCrosses)
			sc.Step(`^one "Monthly spend at 80%" message is sent for ([A-Z][a-z]+)$`, st.oneSentFor)
			sc.Step(`^the date is (\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}) UTC and "([^"]+)" was sent the 80% notice in October$`, st.boundaryGiven)
			sc.Step(`^the clock reaches (\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}) UTC and the account's November spend crosses 80%$`, st.clockReachesNov)
			sc.Step(`^one message is sent for November$`, st.oneForNovember)
			sc.Step(`^two broker instances share one store$`, st.twoInstances)
			sc.Step(`^the account sends one paid request to each instance at the same time, both crossing 80%$`, st.eachInstanceAtOnce)
			sc.Step(`^"([^"]+)" was sent the 80% notice this month$`, st.sentThisMonthAlready)
			sc.Step(`^the broker restarts and the account sends another paid request above 80%$`, st.restartsAndRelays)
			sc.Step(`^no second 80% message is sent$`, st.noSecond80)
			sc.Step(`^the shared store is unreachable$`, st.sharedUnreachable)
			sc.Step(`^a GitHub-linked account "([^"]+)" crosses 80% twice on one instance$`, st.crosses80Twice)
			sc.Step(`^at most one 80% message is sent from that instance$`, st.atMostOne)
			sc.Step(`^the relay is served normally$`, st.servedNormally)

			sc.Step(`^an Apple-linked account with no email on file and a monthly cap of \$([0-9.]+) and \$([0-9.]+) spent$`, st.appleNoEmailCapSpent)
			sc.Step(`^the account relays a paid request that crosses 80%$`, st.relaysCrossing80)
			sc.Step(`^no message is sent$`, st.noMessage)
			sc.Step(`^the response is served with the near-cap notice header set$`, st.servedWithNearHeader)
			sc.Step(`^a signed keypair bound to no account$`, st.unboundKeypair)
			sc.Step(`^it relays requests to a free station$`, st.relaysToFree)
			sc.Step(`^no cap notice is sent$`, st.noCapNotice)
			sc.Step(`^a GitHub-linked account "([^"]+)" was deleted and anonymized after setting a cap$`, st.deletedAfterCap)
			sc.Step(`^a request is attributed to its old wallet$`, st.attributedToOldWallet)
			sc.Step(`^no message is sent to "([^"]+)"$`, st.noMessageTo)
			sc.Step(`^an email-login account whose only address on file is unverified$`, st.emailUnverifiedOnly)
			sc.Step(`^it crosses 80% of its cap$`, st.crosses80Cap)

			sc.Step(`^the mail provider takes (\d+) seconds to answer$`, st.providerSlow)
			sc.Step(`^a GitHub-linked account crosses 80% with a paid request$`, st.githubCrosses80)
			sc.Step(`^the relay response is returned without waiting for the mail provider$`, st.returnedWithoutWaiting)
			sc.Step(`^the mail provider returns 500 for every message$`, st.provider500)
			sc.Step(`^the failure is retried on the transactional lane as features/ops/alert_delivery.feature says$`, st.retriedOnTransactional)
			sc.Step(`^(\d+) ops alerts are queued$`, st.alertsQueued)
			sc.Step(`^a cap notice is enqueued$`, st.capNoticeEnqueued)
			sc.Step(`^it is delivered before any of the alerts$`, st.deliveredBeforeAlerts)
			sc.Step(`^"([^"]+)" is sent the 80% notice$`, st.sentThe80)
			sc.Step(`^the message contains no wallet id, no device pubkey and no other address$`, st.noIdentifiersInMessage)
			sc.Step(`^no log line contains "([^"]+)"$`, st.addrNotLogged)
			st.registerNoticeRulingSteps(sc)
			st.registerNoAddrSteps(sc)
		},
		Options: &godog.Options{
			Format: "pretty", Paths: []string{"../../features/ops/cap_notice_emails.feature"},
			TestingT: t, Strict: true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("cap_notice_emails.feature failed")
	}
}

// cnNotice is the cap-notice entry point the calendar scenarios drive (see the file header).
func cnNotice(b *broker, r *http.Request, holder, threshold string, spend, cap float64, now time.Time) {
	b.emailCapNotice(b.capNoticeAddress(r, holder), holder, threshold, spend, cap, now)
}
