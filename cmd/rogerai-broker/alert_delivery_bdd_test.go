package main

// alert_delivery_bdd_test.go makes features/ops/alert_delivery.feature EXECUTABLE: every
// outbound email rides ONE paced two-lane queue per instance, retries with backoff, alerts
// coalesce into digests, the onset dedup lives in the shared store, a deploy is not an
// outage (grace + debounce), and a flapping key is muted then summarized.
//
// Fixtures are REAL: an httptest email provider that records every POST (with the clock's
// timestamp) and can be scripted to answer 429/5xx/close-the-connection; miniredis behind the
// real valkeyStore for the shared store; the real broker checker/adminAlert paths; the real
// mailer sender goroutine. The ONLY seam is the clock: the mailer and the broker's alert
// path take `now func() time.Time` + `after func(time.Duration) <-chan time.Time`, so the
// pacing, backoff, coalescing, flap and TTL scenarios step a fake clock through its timers
// deterministically instead of sleeping real seconds. No mocks of our own code.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
)

// ---- fake clock -----------------------------------------------------------------

// fakeClock is a manually advanced clock whose After() channels fire when the test moves
// time past their deadline. runUntil steps through every pending deadline IN ORDER,
// letting the goroutines that wake settle before the next step, so timestamps recorded by
// the provider are the exact instants the sender chose.
type fakeClock struct {
	mu      sync.Mutex
	t       time.Time
	waiters []*clockWaiter
}

type clockWaiter struct {
	at time.Time
	ch chan time.Time
}

// newFakeClock starts at the real wall clock: store.Mem stamps rows (CSAM incidents) with
// real time, so "25 hours later" on this clock must also be 25 hours after those stamps.
func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Now().Truncate(time.Second)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	c.mu.Lock()
	defer c.mu.Unlock()
	if d <= 0 {
		ch <- c.t
		return ch
	}
	c.waiters = append(c.waiters, &clockWaiter{at: c.t.Add(d), ch: ch})
	return ch
}

// set moves the clock to t and fires every waiter whose deadline has passed, earliest first.
func (c *fakeClock) set(t time.Time) {
	c.mu.Lock()
	if t.After(c.t) {
		c.t = t
	}
	sort.SliceStable(c.waiters, func(i, j int) bool { return c.waiters[i].at.Before(c.waiters[j].at) })
	var keep []*clockWaiter
	var fire []*clockWaiter
	for _, w := range c.waiters {
		if !w.at.After(c.t) {
			fire = append(fire, w)
		} else {
			keep = append(keep, w)
		}
	}
	c.waiters = keep
	now := c.t
	c.mu.Unlock()
	for _, w := range fire {
		w.ch <- now
	}
}

func (c *fakeClock) advance(d time.Duration) { c.set(c.Now().Add(d)) }

// earliest returns the earliest pending deadline.
func (c *fakeClock) earliest() (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var best time.Time
	ok := false
	for _, w := range c.waiters {
		if !ok || w.at.Before(best) {
			best, ok = w.at, true
		}
	}
	return best, ok
}

func (c *fakeClock) pendingCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.waiters)
}

// ---- recording email provider ----------------------------------------------------

type providerPost struct {
	at      time.Time // the fake clock's reading when the POST arrived
	to      string
	subject string
	html    string
	text    string
	status  int
	key     string // the email's idempotency key: every ATTEMPT of one email repeats it
}

type scriptedResp struct {
	status     int
	retryAfter string
	closeConn  bool
}

// emailProvider is the REAL HTTP surface the mailer POSTs to. It records every POST and
// answers from a script (consumed in order), then a default; it can also hang, cap at N per
// second (the 2026-09-07 provider), or 429 the first N POSTs.
type emailProvider struct {
	srv   *httptest.Server
	clock *fakeClock

	mu        sync.Mutex
	posts     []providerPost
	script    []scriptedResp
	def       scriptedResp
	hang      chan struct{} // non-nil: every POST blocks until closed
	capPerSec int           // >0: 429 when more than capPerSec POSTs landed in the last second
	first429  int           // >0: 429 the first N POSTs
	served    int
	loseFirst map[string]bool // non-nil: hang up on the FIRST attempt of each email
}

func newEmailProvider(clock *fakeClock) *emailProvider {
	p := &emailProvider{clock: clock, def: scriptedResp{status: 200}}
	p.srv = httptest.NewServer(http.HandlerFunc(p.handle))
	return p
}

func (p *emailProvider) handle(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var payload struct {
		To      []string `json:"to"`
		Subject string   `json:"subject"`
		HTML    string   `json:"html"`
		Text    string   `json:"text"`
	}
	_ = json.Unmarshal(raw, &payload)

	key := r.Header.Get("Idempotency-Key")
	p.mu.Lock()
	hang := p.hang
	// A lost response: the server HAS processed the POST (it is recorded below) but the
	// client never learns that, so the sender retries - the exact shape that made CI count
	// one page twice.
	lose := false
	if p.loseFirst != nil && !p.loseFirst[key] {
		p.loseFirst[key] = true
		lose = true
	}
	p.mu.Unlock()
	if hang != nil {
		<-hang
	}

	p.mu.Lock()
	now := p.clock.Now()
	resp := p.def
	switch {
	case len(p.script) > 0:
		resp = p.script[0]
		p.script = p.script[1:]
	case p.first429 > 0 && p.served < p.first429:
		resp = scriptedResp{status: 429}
	case p.capPerSec > 0:
		inWindow := 0
		for _, q := range p.posts {
			if q.status == 200 && now.Sub(q.at) < time.Second {
				inWindow++
			}
		}
		if inWindow >= p.capPerSec {
			resp = scriptedResp{status: 429}
		}
	}
	p.served++
	to := ""
	if len(payload.To) > 0 {
		to = payload.To[0]
	}
	if lose {
		resp = scriptedResp{status: 200, closeConn: true}
	}
	p.posts = append(p.posts, providerPost{at: now, to: to, subject: payload.Subject,
		html: payload.HTML, text: payload.Text, status: resp.status, key: key})
	p.mu.Unlock()

	if resp.closeConn {
		hj, ok := w.(http.Hijacker)
		if !ok {
			panic("httptest server does not support hijack")
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			panic(err)
		}
		_ = conn.Close()
		return
	}
	if resp.retryAfter != "" {
		w.Header().Set("Retry-After", resp.retryAfter)
	}
	w.WriteHeader(resp.status)
	if resp.status == 429 {
		_, _ = w.Write([]byte(`{"statusCode":429,"name":"rate_limit_exceeded","message":"Too many requests. You can only make 10 requests per second."}`))
	} else {
		_, _ = w.Write([]byte(`{"id":"em_ok"}`))
	}
}

// attemptSnapshot is every HTTP POST the provider received, retries included. Only the
// TRANSPORT scenarios (pacing windows, backoff gaps, "1 + 3 retries") assert on it.
func (p *emailProvider) attemptSnapshot() []providerPost {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]providerPost, len(p.posts))
	copy(out, p.posts)
	return out
}

func (p *emailProvider) attempts() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.posts)
}

// snapshot is the PAGE view: one entry per email, keyed by the idempotency key every
// attempt of that email repeats, so a delivery the sender had to retry counts once - what
// the recipient sees. Two genuinely separate pages carry two keys and are never collapsed,
// so every count assertion keeps its teeth. An attempt with no key (a mailer that predates
// the key, or a hand-built one) is always its own page.
func (p *emailProvider) snapshot() []providerPost {
	seen := map[string]bool{}
	var out []providerPost
	for _, post := range p.attemptSnapshot() {
		if post.key != "" && seen[post.key] {
			continue
		}
		seen[post.key] = true
		out = append(out, post)
	}
	return out
}

func (p *emailProvider) count() int { return len(p.snapshot()) }

// ---- log capture ---------------------------------------------------------------

type logSink struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logSink) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(b)
}

func (l *logSink) lines(substr string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, ln := range strings.Split(l.buf.String(), "\n") {
		if strings.Contains(ln, substr) {
			n++
		}
	}
	return n
}

// ---- scenario state --------------------------------------------------------------

type adState struct {
	t     *testing.T
	clock *fakeClock
	prov  *emailProvider
	mr    *miniredis.Miniredis
	logs  *logSink

	a, b    *broker // b is nil unless a scenario asks for two instances
	mailers []*mailer
	closers []func()

	recipients  []string
	hangRel     chan struct{}
	enqueueDur  []time.Duration
	tickDur     time.Duration
	relayDur    time.Duration
	relayCode   int
	live        map[string]any
	drainDone   chan struct{}
	markBefore  int // provider count before a "When" so a "Then" can look at the delta
	alertSubj   string
	digestKeys  []string
	settleStuck bool
}

func (s *adState) reset(t *testing.T) {
	s.t = t
	s.clock = newFakeClock()
	s.prov = newEmailProvider(s.clock)
	s.mr = miniredis.RunT(t)
	s.logs = &logSink{}
	log.SetOutput(s.logs)
	s.a, s.b = nil, nil
	s.mailers, s.closers = nil, nil
	s.recipients = []string{"ops1@example.com", "ops2@example.com", "ops3@example.com"}
	s.hangRel = nil
	s.enqueueDur = nil
	s.tickDur, s.relayDur, s.relayCode = 0, 0, 0
	s.live = nil
	s.drainDone = nil
	s.markBefore = 0
	s.alertSubj = ""
	s.digestKeys = nil
	s.settleStuck = false
}

func (s *adState) teardown() {
	if s.hangRel != nil {
		close(s.hangRel)
		s.hangRel = nil
	}
	for _, m := range s.mailers {
		m.drain(0)
	}
	for _, c := range s.closers {
		c()
	}
	s.prov.srv.Close()
	log.SetOutput(os.Stderr)
}

// newMailer builds an ENABLED mailer on the fake clock, POSTing to the recording provider.
func (s *adState) newMailer() *mailer {
	m := enabledMailer(nil)
	m.provider = providerResend
	m.endpoint = s.prov.srv.URL
	m.timeout = 5 * time.Second
	m.rate = 4
	m.queueCap = 1000
	m.retries = 3
	m.now = s.clock.Now
	m.after = s.clock.After
	s.mailers = append(s.mailers, m)
	return m
}

// newBroker builds a broker instance wired for alerting: three recipients, the production
// knob defaults, the fake clock, a valkeyStore on the shared miniredis, and a boot time an
// hour ago (so the startup grace has elapsed unless a scenario says otherwise).
func (s *adState) newBroker() *broker {
	b := relayBroker(store.NewMem())
	b.mail = s.newMailer()
	b.adminEmails = append([]string(nil), s.recipients...)
	b.alertFiring = map[string]bool{}
	b.alertOnAirSeen = map[string]bool{}
	b.csamSLAHours = 24
	b.alertCfg = alertConfig{
		coalesce: 5 * time.Second, grace: 120 * time.Second, debounceTicks: 2,
		flapCount: 3, flapWindow: time.Hour, flapQuiet: 30 * time.Minute, dedupTTL: 24 * time.Hour,
	}
	b.alertNow = s.clock.Now
	b.alertAfter = s.clock.After
	b.startTime = s.clock.Now().Add(-time.Hour)
	b.adminKey = "k"
	vs, err := newValkeyStore("redis://" + s.mr.Addr())
	if err != nil {
		s.t.Fatalf("newValkeyStore: %v", err)
	}
	s.closers = append(s.closers, func() { _ = vs.Close() })
	b.shared = vs
	return b
}

// quiescent reports whether every sender is parked (idle, waiting on the clock, or exited)
// and no digest flush is armed but unfired.
func (s *adState) quiescent() bool {
	for _, m := range s.mailers {
		if !m.idle() {
			return false
		}
	}
	for _, b := range []*broker{s.a, s.b} {
		if b == nil {
			continue
		}
		if b.alertInflight.Load() > 0 {
			return false
		}
		// A coalescing window whose timer has ALREADY fired (no waiter left on the clock)
		// but whose flush has not run yet - armed is cleared inside flushAlerts - is work
		// in flight, not quiescence. Without this, settle() could return between the timer
		// firing and the digest reaching the mail queue, and an exact-count assertion would
		// read the provider one page early.
		if s.clock.pendingCount() == 0 && flushArmed(b) {
			return false
		}
	}
	return true
}

// flushArmed reports whether a digest window is open on b.
func flushArmed(b *broker) bool {
	b.alertMu.Lock()
	defer b.alertMu.Unlock()
	return b.alertFlushArmed
}

// settle waits (real time, bounded) until the system is quiescent and stays so for a few
// polls, so a step that just woke a goroutine does not race its consequence.
func (s *adState) settle() {
	// Generous in wall time and demanding in stability: a loaded CI runner may take tens of
	// milliseconds to schedule a goroutine, and abandoning the wait early is what turns an
	// exact-count assertion into a mystery failure. The deadline is only ever reached when
	// something is genuinely stuck, and then it is recorded so the assertion says so.
	deadline := time.Now().Add(30 * time.Second)
	stable := 0
	lastPosts, lastWaiters := s.prov.attempts(), s.clock.pendingCount()
	for time.Now().Before(deadline) {
		posts, waiters := s.prov.attempts(), s.clock.pendingCount()
		if s.quiescent() && posts == lastPosts && waiters == lastWaiters {
			stable++
			if stable >= 6 {
				return
			}
		} else {
			stable = 0
		}
		lastPosts, lastWaiters = posts, waiters
		time.Sleep(2 * time.Millisecond)
	}
	s.settleStuck = true
	s.t.Logf("settle: system did not go quiescent within 30s (attempts=%d waiters=%d)", s.prov.attempts(), s.clock.pendingCount())
}

// postDigest summarizes what the provider actually received - how many POSTs per
// (recipient, subject) pair, plus the FIRED/MUTED/DEDUPED/retry tallies - so a count
// assertion that fails on a runner says WHAT doubled or vanished, not just by how much.
func (s *adState) postDigest() string {
	per := map[string]int{}
	for _, p := range s.prov.attemptSnapshot() {
		per[p.to+" | "+p.subject+" | "+strconv.Itoa(p.status)]++
	}
	keys := make([]string, 0, len(per))
	for k := range per {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	fmt.Fprintf(&b, "%d FIRED, %d MUTED, %d DEDUPED, %d fallbacks, %d sender retries; POSTs:",
		s.logs.lines("alert: FIRED"), s.logs.lines("alert: MUTED"), s.logs.lines("alert: DEDUPED"),
		s.logs.lines("alert: shared store unreachable"), s.logs.lines("email: retrying"))
	for _, k := range keys {
		fmt.Fprintf(&b, "\n    %dx %s", per[k], k)
	}
	return b.String()
}

// stuckNote is appended to a count assertion so a CI failure says whether the system was
// still working when the count was read.
func (s *adState) stuckNote() string {
	if s.settleStuck {
		return " [settle timed out: the system was still working when this was read]"
	}
	return ""
}

// runUntil steps the fake clock through every pending timer up to target, settling after
// each, then lands on target.
func (s *adState) runUntil(target time.Time) {
	for i := 0; i < 10000; i++ {
		s.settle()
		at, ok := s.clock.earliest()
		if !ok || at.After(target) {
			s.clock.set(target)
			s.settle()
			return
		}
		s.clock.set(at)
	}
	s.t.Fatal("runUntil: too many timer steps")
}

func (s *adState) runFor(d time.Duration) { s.runUntil(s.clock.Now().Add(d)) }

// windowMax returns the largest number of POSTs inside any 1-second window of the given
// timestamps (by the provider's clock reading).
func windowMax(ts []time.Time) int {
	sort.Slice(ts, func(i, j int) bool { return ts[i].Before(ts[j]) })
	best := 0
	for i := range ts {
		n := 0
		for j := i; j < len(ts) && ts[j].Sub(ts[i]) < time.Second; j++ {
			n++
		}
		if n > best {
			best = n
		}
	}
	return best
}

func (s *adState) postTimes(posts []providerPost) []time.Time {
	out := make([]time.Time, 0, len(posts))
	for _, p := range posts {
		out = append(out, p.at)
	}
	return out
}

func (s *adState) primary() *broker {
	if s.a == nil {
		s.a = s.newBroker()
	}
	return s.a
}

func (s *adState) mailer() *mailer { return s.primary().mail }

// onAir puts one live node offering model into b's registry.
func onAir(b *broker, node, model string) { registerLiveOffer(b, node, model) }

// offAir removes node from b's registry.
func offAir(b *broker, node string) {
	b.mu.Lock()
	delete(b.nodes, node)
	delete(b.lastSeen, node)
	b.mu.Unlock()
}

func (s *adState) tick(b *broker) { b.alertCheckOnce(s.clock.Now()) }

func isAlertSubject(subj string) bool { return strings.HasPrefix(subj, alertSubjectPrefix) }

// ---- Background --------------------------------------------------------------------

func (s *adState) brokerWithThreeRecipients() error {
	s.primary()
	return nil
}

func (s *adState) providerRecordsPosts() error {
	if s.prov == nil {
		return fmt.Errorf("provider not built")
	}
	return nil
}

// ---- 1. pacing -------------------------------------------------------------------

func (s *adState) emailRate(n int) error {
	s.mailer().rate = n
	return nil
}

func (s *adState) enqueueNSameMillisecond(n int) error {
	m := s.mailer()
	for i := 0; i < n; i++ {
		m.sendAlertEmail("ops1@example.com", fmt.Sprintf("[RogerAI ALERT] burst %d", i), "<p>b</p>", "b")
	}
	s.runFor(30 * time.Second)
	return nil
}

func (s *adState) providerReceivesAll(n int) error {
	if got := s.prov.count(); got != n {
		return fmt.Errorf("provider received %d POSTs, want %d", got, n)
	}
	return nil
}

func (s *adState) noWindowMoreThan(n int) error {
	if got := windowMax(s.postTimes(s.prov.attemptSnapshot())); got > n {
		return fmt.Errorf("a 1-second window contained %d POSTs, want <= %d", got, n)
	}
	return nil
}

func (s *adState) providerHangs() error {
	s.hangRel = make(chan struct{})
	s.prov.mu.Lock()
	s.prov.hang = s.hangRel
	s.prov.mu.Unlock()
	return nil
}

func (s *adState) enqueueFromRequestGoroutine(n int) error {
	m := s.mailer()
	done := make(chan []time.Duration)
	go func() {
		var durs []time.Duration
		for i := 0; i < n; i++ {
			st := time.Now()
			m.sendEmail("u@example.com", fmt.Sprintf("code %d", i), "", "t")
			durs = append(durs, time.Since(st))
		}
		done <- durs
	}()
	select {
	case s.enqueueDur = <-done:
	case <-time.After(2 * time.Second):
		return fmt.Errorf("the request goroutine was blocked on delivery for 2s")
	}
	return nil
}

// eachEnqueueUnder: the bound is ~100x a real enqueue (a channel send) and 20x under the
// failure mode it guards (the 2s hang the provider is scripted with), so it cannot flake
// under -race + load the way a 1ms bound did.
func (s *adState) eachEnqueueUnder(ms int) error {
	for i, d := range s.enqueueDur {
		if d > time.Duration(ms)*time.Millisecond {
			return fmt.Errorf("enqueue %d took %v, want < %dms", i, d, ms)
		}
	}
	if len(s.enqueueDur) == 0 {
		return fmt.Errorf("no enqueues recorded")
	}
	return nil
}

func (s *adState) requestGoroutineNeverBlocked() error {
	// The provider is still hanging; the enqueues already returned (previous step). The
	// sender is stuck in its one in-flight POST, which is exactly where the blocking lives.
	if s.hangRel == nil {
		return fmt.Errorf("fixture: the provider was not hanging")
	}
	return nil
}

func (s *adState) queueCapPaused(n int) error {
	m := s.mailer()
	m.queueCap = n
	setPaused(m, true)
	return nil
}

func (s *adState) opsAlertsQueued(n int) error {
	m := s.mailer()
	setPaused(m, true)
	for i := 0; i < n; i++ {
		m.sendAlertEmail("ops1@example.com", fmt.Sprintf("[RogerAI ALERT] queued %d", i), "<p>a</p>", "a")
	}
	return nil
}

func (s *adState) signInCodeEnqueued() error {
	s.markBefore = s.prov.count()
	s.mailer().sendSignInCode("u@example.com", "123456", 10)
	return nil
}

func (s *adState) signInQueuedOldestAlertEvicted() error {
	m := s.mailer()
	st := m.emailStats()
	depth := st["queue_depth"].(map[string]any)
	if depth["transactional"].(int) != 1 || depth["alert"].(int) != 4 {
		return fmt.Errorf("queue depth = %v, want transactional=1 alert=4", depth)
	}
	subs := m.queuedSubjects(laneAlert)
	for _, sj := range subs {
		if sj == "[RogerAI ALERT] queued 0" {
			return fmt.Errorf("the OLDEST alert must be the one evicted; still queued: %v", subs)
		}
	}
	if got := st["email_dropped"].(map[string]int64)["queue-full"]; got != 1 {
		return fmt.Errorf("email_dropped{queue-full} = %d, want 1", got)
	}
	return nil
}

func (s *adState) sixthAlertEnqueued() error {
	s.mailer().sendAlertEmail("ops1@example.com", "[RogerAI ALERT] queued 5", "<p>a</p>", "a")
	return nil
}

func (s *adState) droppedQueueFullCounter(n int) error {
	st := s.mailer().emailStats()
	if got := st["email_dropped"].(map[string]int64)["queue-full"]; got != int64(n) {
		return fmt.Errorf("email_dropped{queue-full} = %d, want %d", got, n)
	}
	depth := st["queue_depth"].(map[string]any)
	if depth["transactional"].(int) != 1 || depth["alert"].(int) != 4 {
		return fmt.Errorf("queue depth after the drop = %v, want transactional=1 alert=4", depth)
	}
	return nil
}

func (s *adState) twoInstancesEachAt(n int) error {
	s.a = s.newBroker()
	s.b = s.newBroker()
	s.a.mail.rate, s.b.mail.rate = n, n
	return nil
}

func (s *adState) bothBurst(n int) error {
	for i := 0; i < n; i++ {
		s.a.mail.sendAlertEmail("ops1@example.com", fmt.Sprintf("[RogerAI ALERT] A%d", i), "<p>a</p>", "a")
		s.b.mail.sendAlertEmail("ops1@example.com", fmt.Sprintf("[RogerAI ALERT] B%d", i), "<p>b</p>", "b")
	}
	s.runFor(30 * time.Second)
	if got := s.prov.count(); got != 2*n {
		return fmt.Errorf("provider received %d POSTs, want %d", got, 2*n)
	}
	return nil
}

func (s *adState) noEmailProviderKey() error {
	b := s.primary()
	b.mail = &mailer{from: "x", timeout: time.Second, sentCaps: map[string]bool{}} // no apiKey => disabled
	return nil
}

func (s *adState) alertsFire(n int) error {
	b := s.primary()
	for i := 0; i < n; i++ {
		b.adminAlert(fmt.Sprintf("cond:%d", i), fmt.Sprintf("thing %d broke", i), "Thing broke", nil, "body")
	}
	s.runFor(10 * time.Second)
	return nil
}

func (s *adState) nothingQueuedDisabledLoggedOnce() error {
	if s.prov.count() != 0 {
		return fmt.Errorf("provider received %d POSTs from a disabled mailer", s.prov.count())
	}
	st := s.primary().mail.emailStats()
	if st["email_queued"].(int64) != 0 {
		return fmt.Errorf("email_queued = %v, want 0", st["email_queued"])
	}
	if got := s.logs.lines("transactional email disabled"); got != 1 {
		return fmt.Errorf("%d 'transactional email disabled' lines, want exactly 1", got)
	}
	return nil
}

// ---- 2. lanes --------------------------------------------------------------------

func (s *adState) signInCodeIsNextPost() error {
	m := s.mailer()
	setPaused(m, false)
	m.kick()
	s.runFor(30 * time.Second)
	posts := s.prov.snapshot()
	if len(posts) <= s.markBefore {
		return fmt.Errorf("no POST after the sign-in code was enqueued")
	}
	if got := posts[s.markBefore].subject; isAlertSubject(got) || !strings.Contains(strings.ToLower(got), "sign-in") {
		return fmt.Errorf("next POST subject = %q, want the sign-in code", got)
	}
	return nil
}

func (s *adState) kindEnqueued(kind string) error {
	b := s.primary()
	mem := b.db.(*store.Mem)
	if err := mem.BindOwner(store.Owner{GitHubID: 7, Login: "op", Pubkey: "ownerpk", Email: "op@example.com"}); err != nil {
		return err
	}
	s.markBefore = s.prov.count()
	switch kind {
	case "sign-in code":
		b.mail.sendSignInCode("op@example.com", "654321", 10)
	case "top-up receipt":
		b.mail.sendEmail("op@example.com", "Top-up receipt", "<p>$10.00</p>", "$10.00")
	case "cap notice":
		b.emailCapNotice("ownerpk", "80", 8, 10, s.clock.Now())
	case "operator warning":
		b.emailAccountWarning("op@example.com", "empty_output", `{"x":1}`, 3, 5)
	case "operator ban notice":
		b.emailAccountBanned("op@example.com", "impossible_input", `{"x":1}`)
	case "payout notice":
		b.emailPayoutSent("op@example.com", 12.5, "tr_1")
	default:
		return fmt.Errorf("unknown transactional kind %q", kind)
	}
	return nil
}

func (s *adState) deliveredBeforeAlerts() error {
	m := s.mailer()
	setPaused(m, false)
	m.kick()
	s.runFor(30 * time.Second)
	posts := s.prov.snapshot()
	if len(posts) == 0 {
		return fmt.Errorf("nothing was delivered")
	}
	if isAlertSubject(posts[0].subject) {
		return fmt.Errorf("first POST was an alert %q; the transactional mail must go first", posts[0].subject)
	}
	n := 0
	for _, p := range posts {
		if isAlertSubject(p.subject) {
			n++
		}
	}
	if n != 10 {
		return fmt.Errorf("%d alerts delivered after it, want 10", n)
	}
	return nil
}

func (s *adState) alertsABC() error {
	m := s.mailer()
	for _, x := range []string{"A", "B", "C"} {
		m.sendAlertEmail("ops1@example.com", "[RogerAI ALERT] "+x, "<p>x</p>", x)
	}
	s.runFor(10 * time.Second)
	return nil
}

func (s *adState) receivesABC() error {
	posts := s.prov.snapshot()
	if len(posts) != 3 {
		return fmt.Errorf("provider received %d POSTs, want 3", len(posts))
	}
	for i, want := range []string{"A", "B", "C"} {
		if got := posts[i].subject; got != "[RogerAI ALERT] "+want {
			return fmt.Errorf("POST %d subject = %q, want %q", i, got, want)
		}
	}
	return nil
}

// ---- 3. retry --------------------------------------------------------------------

func (s *adState) provider429RetryAfterOnce(secs int) error {
	s.prov.mu.Lock()
	s.prov.script = []scriptedResp{{status: 429, retryAfter: strconv.Itoa(secs)}}
	s.prov.mu.Unlock()
	return nil
}

func (s *adState) provider429RetryAfterAlways(secs int) error {
	s.prov.mu.Lock()
	s.prov.def = scriptedResp{status: 429, retryAfter: strconv.Itoa(secs)}
	s.prov.mu.Unlock()
	return nil
}

func (s *adState) anAlertIsSent() error {
	s.alertSubj = "[RogerAI ALERT] single condition"
	s.mailer().sendAlertEmail("ops1@example.com", s.alertSubj, "<p>c</p>", "c")
	s.runFor(60 * time.Second)
	return nil
}

func (s *adState) postsAtLeastSecondsApart(n, secs int) error {
	posts := s.prov.attemptSnapshot()
	if len(posts) != n {
		return fmt.Errorf("provider received %d POSTs, want %d", len(posts), n)
	}
	if gap := posts[n-1].at.Sub(posts[0].at); gap < time.Duration(secs)*time.Second {
		return fmt.Errorf("POSTs were %v apart, want >= %ds", gap, secs)
	}
	return nil
}

func (s *adState) alertWasDelivered() error {
	for _, p := range s.prov.attemptSnapshot() {
		if p.status == 200 && p.subject == s.alertSubj {
			return nil
		}
	}
	return fmt.Errorf("the alert never got a 200 from the provider")
}

func (s *adState) counterRetries(n int) error {
	st := s.mailer().emailStats()
	if got := st["email_retries"].(int64); got != int64(n) {
		return fmt.Errorf("email_retries = %d, want %d", got, n)
	}
	return nil
}

func (s *adState) provider429NoRetryAfterThrice() error {
	s.prov.mu.Lock()
	s.prov.script = []scriptedResp{{status: 429}, {status: 429}, {status: 429}}
	s.prov.mu.Unlock()
	return nil
}

func (s *adState) gapsAbout124() error {
	posts := s.prov.attemptSnapshot()
	if len(posts) != 4 {
		return fmt.Errorf("provider received %d POSTs, want 4", len(posts))
	}
	for i, want := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second} {
		gap := posts[i+1].at.Sub(posts[i].at)
		lo, hi := want*3/4, want*5/4
		if gap < lo || gap > hi {
			return fmt.Errorf("gap %d = %v, want %v +-25%%", i+1, gap, want)
		}
	}
	return nil
}

func (s *adState) deliveredOnNthPost(n int) error {
	posts := s.prov.attemptSnapshot()
	if len(posts) != n {
		return fmt.Errorf("provider received %d POSTs, want %d", len(posts), n)
	}
	if posts[n-1].status != 200 {
		return fmt.Errorf("POST %d status = %d, want 200", n, posts[n-1].status)
	}
	for _, p := range posts[:n-1] {
		if p.status == 200 {
			return fmt.Errorf("an earlier POST already succeeded")
		}
	}
	return nil
}

func (s *adState) provider500Forever() error {
	s.prov.mu.Lock()
	s.prov.def = scriptedResp{status: 500}
	s.prov.mu.Unlock()
	return nil
}

func (s *adState) providerReceivedPosts(n int) error {
	if got := s.prov.attempts(); got != n {
		return fmt.Errorf("provider received %d POSTs, want %d", got, n)
	}
	return nil
}

func (s *adState) droppedLineLogged() error {
	if got := s.logs.lines("email: DROPPED after 3 retries (to=o***@example.com subj=" + strconv.Quote(s.alertSubj)); got != 1 {
		return fmt.Errorf("%d DROPPED lines for the alert, want exactly 1; log:\n%s", got, s.logs.buf.String())
	}
	return nil
}

func (s *adState) droppedRetriesCounter(n int) error {
	st := s.mailer().emailStats()
	if got := st["email_dropped"].(map[string]int64)["retries"]; got != int64(n) {
		return fmt.Errorf("email_dropped{retries} = %d, want %d", got, n)
	}
	return nil
}

func (s *adState) providerStatusOnce(status int) error {
	s.prov.mu.Lock()
	s.prov.script = []scriptedResp{{status: status}}
	s.prov.mu.Unlock()
	return nil
}

func (s *adState) providerClosesOnce() error {
	s.prov.mu.Lock()
	s.prov.script = []scriptedResp{{closeConn: true}}
	s.prov.mu.Unlock()
	return nil
}

func (s *adState) provider429FirstN(n int) error {
	s.prov.mu.Lock()
	s.prov.first429 = n
	s.prov.mu.Unlock()
	return nil
}

func (s *adState) alertsAreSent(n int) error {
	m := s.mailer()
	for i := 0; i < n; i++ {
		m.sendAlertEmail("ops1@example.com", fmt.Sprintf("[RogerAI ALERT] storm %d", i), "<p>s</p>", "s")
	}
	s.runFor(120 * time.Second)
	return nil
}

func (s *adState) noWindowMoreThanIncludingRetries(n int) error {
	// Pages, not attempts: a retried alert is still one alert delivered.
	if got := s.prov.count(); got != 20 {
		return fmt.Errorf("%d alerts delivered, want all 20: %s", got, s.postDigest())
	}
	return s.noWindowMoreThan(n)
}

func (s *adState) alertFiresFromChecker() error {
	b := s.primary()
	b.db = &toggleStore{Store: b.db, down: true}
	st := time.Now()
	s.tick(b)
	s.tickDur = time.Since(st)
	return nil
}

func (s *adState) tickUnder100ms() error {
	if s.tickDur > 100*time.Millisecond {
		return fmt.Errorf("checker tick took %v, want < 100ms", s.tickDur)
	}
	return nil
}

// ---- 4. coalescing ---------------------------------------------------------------

func (s *adState) coalescingWindow(secs int) error {
	s.primary().alertCfg.coalesce = time.Duration(secs) * time.Second
	return nil
}

var sixModels = []string{"gpt-oss-120b", "llama-3.3-70b", "mistral-small", "qwen3-32b", "whisper-large", "zephyr-7b"}

// dropModels puts the models on air (seen), takes them off, and ticks past the debounce so
// all of them fire in ONE checker tick.
func (s *adState) dropModels(b *broker, models []string) {
	for i, m := range models {
		onAir(b, fmt.Sprintf("n%d", i), m)
	}
	s.tick(b)
	for i := range models {
		offAir(b, fmt.Sprintf("n%d", i))
	}
	for i := 0; i < b.alertCfg.debounceTicks; i++ {
		s.tick(b)
	}
}

func (s *adState) sixNoprovidersFire(n int) error {
	s.digestKeys = sixModels[:n]
	s.dropModels(s.primary(), s.digestKeys)
	s.runFor(10 * time.Second)
	return nil
}

func (s *adState) eachRecipientOneEmail() error {
	posts := s.prov.snapshot()
	per := map[string]int{}
	for _, p := range posts {
		per[p.to]++
	}
	for _, r := range s.recipients {
		if per[r] != 1 {
			return fmt.Errorf("recipient %s got %d emails, want exactly 1 (all: %v)", r, per[r], per)
		}
	}
	return nil
}

func (s *adState) subjectIs(want string) error {
	posts := s.prov.snapshot()
	if len(posts) == 0 {
		return fmt.Errorf("no POSTs")
	}
	if got := posts[len(posts)-1].subject; got != want {
		return fmt.Errorf("subject = %q, want %q", got, want)
	}
	return nil
}

func (s *adState) bodyListsConditions(n int) error {
	posts := s.prov.snapshot()
	if len(posts) == 0 {
		return fmt.Errorf("no POSTs")
	}
	p := posts[len(posts)-1]
	for _, m := range s.digestKeys[:n] {
		if !strings.Contains(p.html, m) || !strings.Contains(p.text, m) {
			return fmt.Errorf("digest body is missing model %q", m)
		}
	}
	if c := strings.Count(p.text, "Providers: 0"); c != n {
		return fmt.Errorf("digest text lists %d 'Providers: 0' facts, want %d", c, n)
	}
	return nil
}

func (s *adState) providerReceivedTotalNot(n, not int) error {
	if got := s.prov.count(); got != n {
		return fmt.Errorf("provider received %d POSTs, want %d (not %d)", got, n, not)
	}
	return nil
}

func (s *adState) oneAlertFires() error {
	b := s.primary()
	s.markBefore = s.prov.count()
	b.adminAlert("cond:lone", "lone condition", "Lone condition", nil, "body")
	// Step the clock only through the window + pacing, never past it.
	s.runUntil(s.clock.Now().Add(b.alertCfg.coalesce + time.Second))
	return nil
}

func (s *adState) deliveredWithinWindowPlusPacing() error {
	posts := s.prov.snapshot()
	if len(posts)-s.markBefore != len(s.recipients) {
		return fmt.Errorf("%d POSTs within window+pacing, want %d", len(posts)-s.markBefore, len(s.recipients))
	}
	return nil
}

func (s *adState) csamAlongsideNoproviders(n int) error {
	b := s.primary()
	mem := b.db.(*store.Mem)
	if _, err := mem.PreserveCSAM(store.CSAMIncident{Pseudonym: "u_x", Category: "csam", ReportState: store.CSAMQueued}); err != nil {
		return err
	}
	s.digestKeys = sixModels[:n]
	for i, m := range s.digestKeys {
		onAir(b, fmt.Sprintf("n%d", i), m)
	}
	s.tick(b)
	for i := range s.digestKeys {
		offAir(b, fmt.Sprintf("n%d", i))
	}
	// Age the CSAM item past the SLA and tick past the debounce: the last tick fires
	// csam_sla AND the six noproviders together.
	s.clock.advance(25 * time.Hour)
	for i := 0; i < b.alertCfg.debounceTicks; i++ {
		s.tick(b)
	}
	s.settle()
	return nil
}

func (s *adState) csamSentImmediatelyOwnEmail() error {
	// Before the coalescing window elapses, the ONLY POSTs are the CSAM pages.
	posts := s.prov.snapshot()
	if len(posts) != len(s.recipients) {
		return fmt.Errorf("%d POSTs before the window elapsed, want %d CSAM pages", len(posts), len(s.recipients))
	}
	for _, p := range posts {
		if !strings.Contains(strings.ToUpper(p.subject), "CSAM") || strings.Contains(p.subject, "conditions:") {
			return fmt.Errorf("pre-window POST subject = %q, want a standalone CSAM page", p.subject)
		}
	}
	return nil
}

func (s *adState) othersBecomeOneDigest(n int) error {
	pre := s.prov.count() // the immediate CSAM pages already sent
	s.runFor(10 * time.Second)
	posts := s.prov.snapshot()
	if len(posts) != pre+len(s.recipients) {
		return fmt.Errorf("%d POSTs total, want %d (%d immediate + digest x%d)", len(posts), pre+len(s.recipients), pre, len(s.recipients))
	}
	for _, p := range posts[pre:] {
		if !strings.HasPrefix(p.subject, fmt.Sprintf("%s%d conditions:", alertSubjectPrefix, n)) {
			return fmt.Errorf("digest subject = %q, want %d conditions", p.subject, n)
		}
	}
	return nil
}

func (s *adState) conditionsFiredAsDigest(n int) error {
	return s.sixNoprovidersFire(n)
}

func (s *adState) allClear(n int) error {
	b := s.primary()
	for i, m := range s.digestKeys[:n] {
		onAir(b, fmt.Sprintf("n%d", i), m)
	}
	s.tick(b)
	return nil
}

func (s *adState) clearedLines(n int) error {
	if got := s.logs.lines("alert: CLEARED"); got != n {
		return fmt.Errorf("%d 'alert: CLEARED' lines, want %d", got, n)
	}
	return nil
}

// ---- 5. cross-instance dedup -------------------------------------------------------

func (s *adState) twoInstancesShareStore() error {
	s.a = s.newBroker()
	s.b = s.newBroker()
	return nil
}

func (s *adState) bothDetect(key string) error {
	model := strings.TrimPrefix(key, "noproviders:")
	for _, b := range []*broker{s.a, s.b} {
		onAir(b, "n1", model)
		s.tick(b)
		offAir(b, "n1")
	}
	for i := 0; i < s.a.alertCfg.debounceTicks; i++ {
		s.tick(s.a)
		s.settle() // A's onset goroutines claim their keys before B's tick (real ticks are minutes apart in phase)
		s.tick(s.b)
	}
	s.runFor(10 * time.Second)
	return nil
}

func (s *adState) exactlyOneDigestPerRecipient() error {
	return s.eachRecipientOneEmail()
}

func (s *adState) storeHoldsKeyWithTTL(key string) error {
	full := alertKeyPrefix + key
	if !s.mr.Exists(full) {
		return fmt.Errorf("store does not hold %s; keys: %v", full, s.mr.Keys())
	}
	if ttl := s.mr.TTL(full); ttl <= 0 {
		return fmt.Errorf("%s has no TTL", full)
	}
	return nil
}

func (s *adState) firedAndKeyExists(key string) error {
	b := s.primary()
	model := strings.TrimPrefix(key, "noproviders:")
	onAir(b, "n1", model)
	s.tick(b)
	offAir(b, "n1")
	for i := 0; i < b.alertCfg.debounceTicks; i++ {
		s.tick(b)
	}
	s.runFor(10 * time.Second)
	if s.prov.count() != len(s.recipients) {
		return fmt.Errorf("%d POSTs after the first onset, want %d", s.prov.count(), len(s.recipients))
	}
	return s.storeHoldsKeyWithTTL(key)
}

func (s *adState) modelReturnsAndClears() error {
	b := s.primary()
	onAir(b, "n1", "m")
	s.tick(b)
	return nil
}

func (s *adState) keyDeleted() error {
	if s.mr.Exists(alertKeyPrefix + "noproviders:m") {
		return fmt.Errorf("the dedup key still exists after CLEARED")
	}
	return nil
}

func (s *adState) modelDropsAgain() error {
	b := s.primary()
	s.markBefore = s.prov.count()
	offAir(b, "n1")
	for i := 0; i < b.alertCfg.debounceTicks; i++ {
		s.tick(b)
	}
	s.runFor(10 * time.Second)
	return nil
}

func (s *adState) pagesAgain() error {
	if got := s.prov.count() - s.markBefore; got != len(s.recipients) {
		return fmt.Errorf("%d POSTs on the re-onset, want %d", got, len(s.recipients))
	}
	return nil
}

func (s *adState) fired25hAgoStillFiring(key string) error {
	if err := s.firedAndKeyExists(key); err != nil {
		return err
	}
	s.markBefore = s.prov.count()
	s.clock.advance(25 * time.Hour)
	s.mr.FastForward(25 * time.Hour)
	s.tick(s.primary())
	s.runFor(10 * time.Second)
	return nil
}

func (s *adState) pagesOnceMore() error { return s.pagesAgain() }

func (s *adState) sharedStoreUnreachable() error {
	if s.b == nil {
		s.b = s.newBroker()
	}
	s.mr.Close()
	return nil
}

func (s *adState) firesOnBothInstances(key string) error {
	s.primary()
	if s.b == nil {
		s.b = s.newBroker()
	}
	for _, b := range []*broker{s.a, s.b} {
		b.adminAlert(key, "model m has 0 providers", "Model m dropped to 0 providers", nil, "body")
	}
	s.runFor(10 * time.Second)
	return nil
}

func (s *adState) eachInstancePagesOnceLogsFallbackOnce() error {
	if got := s.prov.count(); got != 2*len(s.recipients) {
		return fmt.Errorf("%d POSTs, want %d (each instance pages once): %s%s",
			got, 2*len(s.recipients), s.postDigest(), s.stuckNote())
	}
	if got := s.logs.lines("alert: shared store unreachable"); got != 2 {
		return fmt.Errorf("%d fallback lines, want exactly 1 per instance (2); log:\n%s", got, s.logs.buf.String())
	}
	return nil
}

func (s *adState) alertPrefixNamespaced() error {
	if alertKeyPrefix != "rogerai:alert:" {
		return fmt.Errorf("alertKeyPrefix = %q", alertKeyPrefix)
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		if name == "alertstore.go" {
			continue
		}
		// Code only: a comment may NAME the key layout; nothing but alertstore.go may build it.
		for _, ln := range strings.Split(string(src), "\n") {
			if strings.HasPrefix(strings.TrimSpace(ln), "//") {
				continue
			}
			if strings.Contains(ln, `"alert:"`) || strings.Contains(ln, `rogerai:alert:`) {
				return fmt.Errorf("%s writes under the alert namespace; only alertstore.go may: %s", name, strings.TrimSpace(ln))
			}
		}
	}
	return nil
}

// ---- 6. grace + debounce ------------------------------------------------------------

func (s *adState) bootedSecondsAgoRehydrated(secs, regs int) error {
	b := s.primary()
	b.startTime = s.clock.Now().Add(-time.Duration(secs) * time.Second)
	b.mu.Lock()
	for i := 0; i < regs; i++ {
		pub, _, _ := ed25519.GenerateKey(nil)
		id := fmt.Sprintf("rehydrated-%d", i)
		b.nodes[id] = protocol.NodeRegistration{NodeID: id, PubKey: hex.EncodeToString(pub),
			Offers: []protocol.ModelOffer{{Model: "m", PriceOut: 0.1}}}
		// Re-hydrated, not yet heard from: lastSeen is old, so none is on air yet.
		b.lastSeen[id] = s.clock.Now().Add(-time.Hour)
	}
	b.mu.Unlock()
	b.alertOnAirSeen["m"] = true // the model was on air before the deploy
	return nil
}

func (s *adState) checkerSeesZeroProviders(model string) error {
	b := s.primary()
	for i := 0; i < b.alertCfg.debounceTicks+1; i++ {
		s.tick(b)
	}
	s.runFor(10 * time.Second)
	return nil
}

func (s *adState) noAlertGraceLogged() error {
	if s.prov.count() != 0 {
		return fmt.Errorf("%d POSTs during the startup grace, want 0", s.prov.count())
	}
	if got := s.logs.lines("alerts: in startup grace"); got != 1 {
		return fmt.Errorf("%d 'alerts: in startup grace' lines, want exactly 1", got)
	}
	return nil
}

func (s *adState) graceElapsedModelOnAir(model string) error {
	b := s.primary()
	onAir(b, "n1", model)
	s.tick(b)
	return nil
}

func (s *adState) absentOneTickPresentNext(model string) error {
	b := s.primary()
	offAir(b, "n1")
	s.tick(b)
	onAir(b, "n1", model)
	s.tick(b)
	s.runFor(10 * time.Second)
	return nil
}

func (s *adState) noAlertFires() error {
	if s.prov.count() != 0 {
		return fmt.Errorf("%d POSTs, want 0", s.prov.count())
	}
	return nil
}

func (s *adState) absentTwoTicks(model string) error {
	b := s.primary()
	offAir(b, "n1")
	s.tick(b)
	s.tick(b)
	s.runFor(10 * time.Second)
	return nil
}

func (s *adState) firesOnce(key string) error {
	posts := s.prov.snapshot()
	if len(posts) != len(s.recipients) {
		return fmt.Errorf("%d POSTs, want %d (one page per recipient)", len(posts), len(s.recipients))
	}
	model := strings.TrimPrefix(key, "noproviders:")
	for _, p := range posts {
		if !strings.Contains(p.subject, "model "+model+" has 0 providers") {
			return fmt.Errorf("subject = %q, want the %s supply gap", p.subject, key)
		}
	}
	return nil
}

func (s *adState) durableStoreUnreachableOneTick() error {
	b := s.primary()
	b.db = &toggleStore{Store: b.db, down: true}
	s.tick(b)
	s.runFor(10 * time.Second)
	return nil
}

func (s *adState) dbDownFiresImmediately() error {
	posts := s.prov.snapshot()
	if len(posts) != len(s.recipients) {
		return fmt.Errorf("%d POSTs, want %d", len(posts), len(s.recipients))
	}
	for _, p := range posts {
		if !strings.Contains(strings.ToLower(p.subject), "database") {
			return fmt.Errorf("subject = %q, want the database alert", p.subject)
		}
	}
	return nil
}

func (s *adState) bootedSecondsAgo(secs int) error {
	// Age the world first so the CSAM item can be older than the SLA while the boot is fresh.
	s.clock.advance(25 * time.Hour)
	s.primary().startTime = s.clock.Now().Add(-time.Duration(secs) * time.Second)
	return nil
}

func (s *adState) csamQueuedPastSLA() error {
	mem := s.primary().db.(*store.Mem)
	_, err := mem.PreserveCSAM(store.CSAMIncident{Pseudonym: "u_x", Category: "csam", ReportState: store.CSAMQueued})
	return err
}

func (s *adState) checkerRuns() error {
	s.tick(s.primary())
	s.settle()
	return nil
}

func (s *adState) csamFiresImmediately() error {
	posts := s.prov.snapshot()
	if len(posts) != len(s.recipients) {
		return fmt.Errorf("%d POSTs, want %d (immediate, no coalescing wait)", len(posts), len(s.recipients))
	}
	for _, p := range posts {
		if !strings.Contains(strings.ToUpper(p.subject), "CSAM") {
			return fmt.Errorf("subject = %q, want the CSAM SLA alert", p.subject)
		}
	}
	return nil
}

// ---- 7. flap suppression ------------------------------------------------------------

func (s *adState) fireKey(b *broker, key string) {
	b.adminAlert(key, "model "+strings.TrimPrefix(key, "noproviders:")+" has 0 providers", "Supply gap", nil, "body")
	s.runFor(b.alertCfg.coalesce + time.Second)
}

func (s *adState) flapThreeTimes(key string, minutes int) error {
	b := s.primary()
	gap := time.Duration(minutes) * time.Minute / 4
	s.fireKey(b, key)
	s.clock.advance(gap)
	b.alertClear(key)
	s.clock.advance(gap)
	s.fireKey(b, key)
	s.clock.advance(gap)
	b.alertClear(key)
	s.clock.advance(gap)
	s.fireKey(b, key)
	return nil
}

func (s *adState) threePagesSent() error {
	if got := s.prov.count(); got != 3*len(s.recipients) {
		return fmt.Errorf("%d POSTs, want %d (three pages x recipients): %d FIRED, %d MUTED, %d shared-store fallbacks%s",
			got, 3*len(s.recipients), s.logs.lines("alert: FIRED"), s.logs.lines("alert: MUTED"),
			s.logs.lines("alert: shared store unreachable"), s.stuckNote())
	}
	return nil
}

func (s *adState) thirdPageSubjectEndsWith(suffix string) error {
	posts := s.prov.snapshot()
	if len(posts) < 3 {
		return fmt.Errorf("only %d POSTs", len(posts))
	}
	last := posts[len(posts)-1].subject
	if !strings.HasSuffix(last, suffix) {
		return fmt.Errorf("third page subject = %q, want suffix %q", last, suffix)
	}
	if first := posts[0].subject; strings.HasSuffix(first, suffix) {
		return fmt.Errorf("the FIRST page already carried the flapping suffix: %q", first)
	}
	return nil
}

func (s *adState) clearsAndFiresFourth(minutes int) error {
	b := s.primary()
	s.markBefore = s.prov.count()
	b.alertClear("noproviders:m")
	s.clock.advance(time.Duration(minutes) * time.Minute)
	s.fireKey(b, "noproviders:m")
	return nil
}

func (s *adState) noEmailMutedLogged(key string) error {
	if got := s.prov.count() - s.markBefore; got != 0 {
		return fmt.Errorf("%d POSTs for the muted onset, want 0", got)
	}
	if got := s.logs.lines("alert: MUTED (flapping) " + key); got != 1 {
		return fmt.Errorf("%d MUTED lines, want 1; log:\n%s", got, s.logs.buf.String())
	}
	return nil
}

func (s *adState) mutedAfterFlapping(key string, times int) error {
	b := s.primary()
	for i := 0; i < times; i++ {
		s.fireKey(b, key)
		s.clock.advance(time.Minute)
		b.alertClear(key)
		s.clock.advance(time.Minute)
	}
	if got := s.prov.count(); got != 3*len(s.recipients) {
		return fmt.Errorf("%d POSTs after %d flaps, want %d (pages stop at the third onset): %d FIRED, %d MUTED, %d shared-store fallbacks%s",
			got, times, 3*len(s.recipients), s.logs.lines("alert: FIRED"), s.logs.lines("alert: MUTED"),
			s.logs.lines("alert: shared store unreachable"), s.stuckNote())
	}
	s.markBefore = s.prov.count()
	return nil
}

// firesAndClearsN: n onset/clear cycles of key spread evenly over `minutes` (each onset paged
// on its own, as csam_sla is when the queue breaches, drains, and breaches again).
func (s *adState) firesAndClearsN(key string, n, minutes int) error {
	b := s.primary()
	gap := time.Duration(minutes) * time.Minute / time.Duration(2*n)
	for i := 0; i < n; i++ {
		s.fireKey(b, key)
		s.clock.advance(gap)
		b.alertClear(key)
		s.clock.advance(gap)
	}
	return nil
}

func (s *adState) nPagesNoFlapSuffix(n int) error {
	posts := s.prov.snapshot()
	if len(posts) != n*len(s.recipients) {
		return fmt.Errorf("%d POSTs, want %d (%d pages x %d recipients)", len(posts), n*len(s.recipients), n, len(s.recipients))
	}
	for _, p := range posts {
		if strings.Contains(p.subject, "flapping") {
			return fmt.Errorf("an urgent page carried the flapping suffix: %q", p.subject)
		}
	}
	return nil
}

func (s *adState) noMutedLine() error {
	if got := s.logs.lines("alert: MUTED (flapping)"); got != 0 {
		return fmt.Errorf("%d MUTED lines, want 0; log:\n%s", got, s.logs.buf.String())
	}
	return nil
}

func (s *adState) staysClearFor(minutes int) error {
	b := s.primary()
	s.clock.advance(time.Duration(minutes) * time.Minute)
	s.tick(b)
	s.runFor(10 * time.Second)
	return nil
}

func (s *adState) stabilizedEmailPerRecipient(key string, onsets int) error {
	posts := s.prov.snapshot()[s.markBefore:]
	if len(posts) != len(s.recipients) {
		return fmt.Errorf("%d summary POSTs, want %d", len(posts), len(s.recipients))
	}
	want := fmt.Sprintf("%s%s stabilized after %d onsets", alertSubjectPrefix, key, onsets)
	for _, p := range posts {
		if p.subject != want {
			return fmt.Errorf("summary subject = %q, want %q", p.subject, want)
		}
	}
	return nil
}

func (s *adState) muteLifted() error {
	b := s.primary()
	before := s.prov.count()
	s.fireKey(b, "noproviders:m")
	posts := s.prov.snapshot()
	if len(posts)-before != len(s.recipients) {
		return fmt.Errorf("%d POSTs after the mute lifted, want %d (pages normally)", len(posts)-before, len(s.recipients))
	}
	if strings.Contains(posts[len(posts)-1].subject, "flapping") {
		return fmt.Errorf("post-lift page still carries the flapping suffix: %q", posts[len(posts)-1].subject)
	}
	return nil
}

func (s *adState) keyIsMuted(key string) error {
	if err := s.flapThreeTimes(key, 40); err != nil {
		return err
	}
	s.markBefore = s.prov.count()
	return nil
}

func (s *adState) firesFirstTime(key string) error {
	s.fireKey(s.primary(), key)
	return nil
}

func (s *adState) pagesNormally() error {
	posts := s.prov.snapshot()[s.markBefore:]
	if len(posts) != len(s.recipients) {
		return fmt.Errorf("%d POSTs, want %d", len(posts), len(s.recipients))
	}
	if strings.Contains(posts[0].subject, "flapping") {
		return fmt.Errorf("a first onset carried the flapping suffix: %q", posts[0].subject)
	}
	return nil
}

func (s *adState) twoInstances() error { return s.twoInstancesShareStore() }

func (s *adState) flapsAcrossBoth(key string) error {
	// Onsets alternate A, B, A; each instance clears its own onset.
	s.fireKey(s.a, key)
	s.clock.advance(time.Minute)
	s.a.alertClear(key)
	s.fireKey(s.b, key)
	s.clock.advance(time.Minute)
	s.b.alertClear(key)
	s.fireKey(s.a, key)
	return nil
}

func (s *adState) countTowardOneTotal() error {
	posts := s.prov.snapshot()
	if len(posts) != 3*len(s.recipients) {
		return fmt.Errorf("%d POSTs, want %d", len(posts), 3*len(s.recipients))
	}
	if last := posts[len(posts)-1].subject; !strings.Contains(last, "flapping") {
		return fmt.Errorf("third onset across two instances = %q, want it counted as the third flap", last)
	}
	return nil
}

// ---- 8. observability + isolation --------------------------------------------------

func (s *adState) founderReadsAdminLive() error {
	b := s.primary()
	r := httptest.NewRequest(http.MethodGet, "/admin/live", nil)
	r.Header.Set("X-Roger-Admin", b.adminKey)
	w := httptest.NewRecorder()
	b.adminLive(w, r)
	if w.Code != http.StatusOK {
		return fmt.Errorf("/admin/live = %d: %s", w.Code, w.Body.String())
	}
	return json.Unmarshal(w.Body.Bytes(), &s.live)
}

func (s *adState) adminLiveShowsCounters() error {
	email, ok := s.live["email"].(map[string]any)
	if !ok {
		return fmt.Errorf("/admin/live has no email block: %v", s.live)
	}
	for _, k := range []string{"email_queued", "email_sent", "email_retries", "email_dropped", "queue_depth"} {
		if _, ok := email[k]; !ok {
			return fmt.Errorf("email block is missing %q: %v", k, email)
		}
	}
	depth, ok := email["queue_depth"].(map[string]any)
	if !ok {
		return fmt.Errorf("queue_depth is not per lane: %v", email["queue_depth"])
	}
	for _, lane := range []string{"transactional", "alert"} {
		if _, ok := depth[lane]; !ok {
			return fmt.Errorf("queue_depth is missing lane %q", lane)
		}
	}
	if _, ok := email["email_dropped"].(map[string]any); !ok {
		return fmt.Errorf("email_dropped is not by reason: %v", email["email_dropped"])
	}
	alerts, ok := s.live["alerts"].(map[string]any)
	if !ok {
		return fmt.Errorf("/admin/live has no alerts block")
	}
	for _, k := range []string{"alerts_coalesced", "alerts_deduped", "alerts_muted"} {
		if _, ok := alerts[k]; !ok {
			return fmt.Errorf("alerts block is missing %q: %v", k, alerts)
		}
	}
	return nil
}

func (s *adState) provider429ForeverAlertsFire(n int) error {
	b := s.primary()
	b.alertCfg.coalesce = 0 // every condition is its own page: the worst-case storm
	s.prov.mu.Lock()
	s.prov.def = scriptedResp{status: 429}
	s.prov.mu.Unlock()
	for i := 0; i < n; i++ {
		b.adminAlert(fmt.Sprintf("storm:%d", i), fmt.Sprintf("storm %d", i), "Storm", nil, "body")
	}
	return nil
}

func (s *adState) fundedConsumerRelays() error {
	b := s.primary()
	mem := b.db.(*store.Mem)
	nodePub, nodePriv, _ := ed25519.GenerateKey(nil)
	b.mu.Lock()
	b.nodes["st1"] = protocol.NodeRegistration{NodeID: "st1", PubKey: hex.EncodeToString(nodePub),
		Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 1.0, PriceOut: 1.0, Ctx: 4096}}}
	b.lastSeen["st1"] = time.Now()
	tun := &nodeTunnel{jobs: make(chan protocol.Job, 1), waiters: map[string]chan protocol.JobResult{}}
	b.tunnels["st1"] = tun
	b.mu.Unlock()
	if err := mem.BindNode("st1", "op1"); err != nil {
		return err
	}
	go func() {
		job, ok := <-tun.jobs
		if !ok {
			return
		}
		rec := protocol.UsageReceipt{RequestID: job.ID, NodeID: "st1", Model: "m",
			PromptTokens: 12, CompletionTokens: 40, PriceIn: 1.0, PriceOut: 1.0, TS: time.Now().Unix()}
		rec.SignNode(nodePriv)
		res := protocol.JobResult{ID: job.ID, Status: 200,
			Body: []byte(`{"choices":[{"message":{"role":"assistant","content":"answer"}}]}`), Receipt: rec}
		tun.mu.Lock()
		ch := tun.waiters[job.ID]
		tun.mu.Unlock()
		if ch != nil {
			ch <- res
		}
	}()
	_, userPriv, _ := ed25519.GenerateKey(nil)
	userPubHex := hex.EncodeToString(userPriv.Public().(ed25519.PublicKey))
	if err := mem.BindOwner(store.Owner{GitHubID: 7, Login: "alice", Pubkey: userPubHex}); err != nil {
		return err
	}
	if _, err := mem.AddCredits("u_gh_7", 100); err != nil {
		return err
	}
	body := []byte(`{"model":"m","max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	signReq(r, userPriv, body)
	w := httptest.NewRecorder()
	st := time.Now()
	b.relay(w, r)
	s.relayDur = time.Since(st)
	s.relayCode = w.Code
	return nil
}

func (s *adState) relayAtStationSpeed() error {
	if s.relayCode != http.StatusOK {
		return fmt.Errorf("relay = %d, want 200", s.relayCode)
	}
	if s.relayDur > time.Second {
		return fmt.Errorf("relay took %v under an alert storm, want the station's speed (well under 1s)", s.relayDur)
	}
	return nil
}

func (s *adState) codesAndAlertsQueued(codes, alerts int) error {
	m := s.mailer()
	setPaused(m, true)
	for i := 0; i < alerts; i++ {
		m.sendAlertEmail("ops1@example.com", fmt.Sprintf("[RogerAI ALERT] pending %d", i), "<p>a</p>", "a")
	}
	for i := 0; i < codes; i++ {
		m.sendSignInCode(fmt.Sprintf("u%d@example.com", i), "111111", 10)
	}
	return nil
}

func (s *adState) stopSignalWithDrainBudget(secs int) error {
	m := s.mailer()
	s.drainDone = make(chan struct{})
	go func() {
		m.drain(time.Duration(secs) * time.Second)
		close(s.drainDone)
	}()
	s.runFor(time.Duration(secs) * time.Second)
	select {
	case <-s.drainDone:
	case <-time.After(5 * time.Second):
		return fmt.Errorf("drain did not return within its budget")
	}
	return nil
}

func (s *adState) codesDeliveredFirst(n int) error {
	posts := s.prov.snapshot()
	if len(posts) < n {
		return fmt.Errorf("only %d POSTs, want at least the %d sign-in codes", len(posts), n)
	}
	for i := 0; i < n; i++ {
		if isAlertSubject(posts[i].subject) {
			return fmt.Errorf("POST %d was an alert %q; the sign-in codes must drain first", i, posts[i].subject)
		}
	}
	return nil
}

func (s *adState) undeliveredCountedShutdown() error {
	st := s.mailer().emailStats()
	dropped := st["email_dropped"].(map[string]int64)["shutdown"]
	sent := st["email_sent"].(int64)
	if dropped == 0 {
		return fmt.Errorf("nothing counted as dropped{shutdown}; stats=%v", st)
	}
	if sent+dropped != 53 {
		return fmt.Errorf("sent %d + dropped{shutdown} %d != 53 queued", sent, dropped)
	}
	return nil
}

// ---- 9. regression -------------------------------------------------------------------

func (s *adState) rigFlapFixture(n int) error {
	s.a = s.newBroker()
	s.b = s.newBroker()
	s.prov.mu.Lock()
	s.prov.capPerSec = 10
	s.prov.mu.Unlock()
	s.digestKeys = sixModels[:n]
	for _, b := range []*broker{s.a, s.b} {
		for i, m := range s.digestKeys {
			onAir(b, fmt.Sprintf("n%d", i), m)
		}
		s.tick(b)
		for i := range s.digestKeys {
			offAir(b, fmt.Sprintf("n%d", i))
		}
	}
	return nil
}

func (s *adState) checkerRunsBothAfterDebounce() error {
	for i := 0; i < s.a.alertCfg.debounceTicks; i++ {
		s.tick(s.a)
		s.settle() // A's tick completes (its onsets claim the keys) before B's, as unsynchronized tickers do
		s.tick(s.b)
	}
	s.runFor(10 * time.Second)
	return nil
}

func (s *adState) exactlyNPostsAll200(n int) error {
	posts := s.prov.attemptSnapshot() // attempts: the incident was about POSTs hitting the cap
	if len(posts) != n {
		return fmt.Errorf("provider received %d POSTs, want exactly %d: %s", len(posts), n, s.postDigest())
	}
	if r := s.retriesTaken(); r != 0 {
		return fmt.Errorf("%d sender retries: under the cap nothing should have needed one", r)
	}
	for _, p := range posts {
		if p.status != 200 {
			return fmt.Errorf("a POST got %d, want all 200", p.status)
		}
	}
	return nil
}

func (s *adState) zero429Lines() error {
	if got := s.logs.lines("resend error 429"); got != 0 {
		return fmt.Errorf("%d 'resend error 429' lines, want 0", got)
	}
	return nil
}

func (s *adState) eachRecipientOneDigestNaming(n int) error {
	if err := s.eachRecipientOneEmail(); err != nil {
		return err
	}
	for _, p := range s.prov.snapshot() {
		for _, m := range s.digestKeys[:n] {
			if !strings.Contains(p.text, m) {
				return fmt.Errorf("digest to %s does not name %s", p.to, m)
			}
		}
	}
	return nil
}

// ---- 10. regressions (76642e33 review) ---------------------------------------------

func (s *adState) storeFailsEveryCommand() error {
	s.mr.SetError("ERR injected fault")
	return nil
}

func (s *adState) storeRecovers() error {
	s.mr.SetError("")
	return nil
}

func (s *adState) clearPendingKeyStillExists() error {
	if !s.mr.Exists(alertKeyPrefix + "noproviders:m") {
		return fmt.Errorf("the shared key vanished although the DEL was failed")
	}
	if got := s.logs.lines("alert: CLEARED"); got != 1 {
		return fmt.Errorf("%d CLEARED lines, want 1 (the local clear still happens)", got)
	}
	if got := s.logs.lines(`pending retry`); got != 1 {
		return fmt.Errorf("%d 'pending retry' lines, want 1; log:\n%s", got, s.logs.buf.String())
	}
	return nil
}

func (s *adState) reOnsetBeforeRetry() error {
	s.markBefore = s.prov.count()
	s.fireKey(s.primary(), "noproviders:m")
	return nil
}

func (s *adState) keyClaimedAgainNoPending() error {
	if err := s.storeHoldsKeyWithTTL("noproviders:m"); err != nil {
		return err
	}
	return s.noClearPending()
}

func (s *adState) noClearPending() error {
	if got := s.logs.lines(`pending clear of "noproviders:m" resolved`); got != 1 {
		return fmt.Errorf("%d 'pending clear resolved' lines, want 1; log:\n%s", got, s.logs.buf.String())
	}
	return nil
}

func (s *adState) retriesKnob(n int) error {
	s.mailer().retries = n
	return nil
}

func (s *adState) eventuallyDroppedAfter(n int) error {
	s.runFor(6 * time.Hour)
	if got := s.prov.attempts(); got != n+1 {
		return fmt.Errorf("provider received %d POSTs, want %d (1 + %d retries)", got, n+1, n)
	}
	if got := s.logs.lines(fmt.Sprintf("email: DROPPED after %d retries", n)); got != 1 {
		return fmt.Errorf("%d DROPPED lines, want 1", got)
	}
	return nil
}

func (s *adState) senderStillAlive() error {
	s.prov.mu.Lock()
	s.prov.def = scriptedResp{status: 200}
	s.prov.mu.Unlock()
	before := s.prov.count()
	s.mailer().sendAlertEmail("ops1@example.com", "[RogerAI ALERT] after the storm", "<p>x</p>", "x")
	s.runFor(10 * time.Second)
	if got := s.prov.count(); got != before+1 {
		return fmt.Errorf("the sender did not deliver after the drop (posts %d -> %d): it is dead", before, got)
	}
	return nil
}

// storeHangs points the broker's shared store at a listener that accepts and never answers,
// so every command runs into the client's own timeout instead of an error.
func (s *adState) storeHangs() error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	stop := make(chan struct{})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				<-stop
				_ = c.Close()
			}()
		}
	}()
	s.closers = append(s.closers, func() { close(stop); _ = ln.Close() })
	vs, _ := newValkeyStore("redis://" + ln.Addr().String()) // the ping times out; the store is still usable
	if vs == nil {
		return fmt.Errorf("could not build a store on the black hole")
	}
	s.closers = append(s.closers, func() { _ = vs.Close() })
	s.primary().shared = vs
	return nil
}

func (s *adState) billingDriftFires() error {
	b := s.primary()
	st := time.Now()
	b.checkDriftAlert("u_gh_1", 10.0, 7.5)
	s.tickDur = time.Since(st)
	return nil
}

func (s *adState) checkUnder50ms() error {
	if s.tickDur > 50*time.Millisecond {
		return fmt.Errorf("the drift check took %v with the shared store hanging, want < 50ms", s.tickDur)
	}
	return nil
}

func (s *adState) pageStillSent() error {
	// The onset is finishing on its own goroutine against a store that only answers by
	// timing out (real time), so poll: step the fake clock, look, repeat.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && s.prov.count() < len(s.recipients) {
		s.runFor(10 * time.Second)
		time.Sleep(20 * time.Millisecond)
	}
	posts := s.prov.snapshot()
	if len(posts) != len(s.recipients) {
		return fmt.Errorf("%d POSTs after the store answered, want %d", len(posts), len(s.recipients))
	}
	if !strings.Contains(posts[0].subject, "u_gh_1") {
		return fmt.Errorf("subject = %q, want the drift alert", posts[0].subject)
	}
	return nil
}

func (s *adState) firesOnceAndClears(key string) error {
	b := s.primary()
	s.fireKey(b, key)
	b.alertClear(key)
	return nil
}

func (s *adState) flapWindowElapsesChecker() error {
	b := s.primary()
	s.clock.advance(b.alertCfg.flapWindow + time.Minute)
	s.tick(b)
	s.runFor(10 * time.Second)
	return nil
}

func (s *adState) flapTableNoLongerHolds(key string) error {
	b := s.primary()
	b.alertMu.Lock()
	defer b.alertMu.Unlock()
	if _, ok := b.alertFlap[key]; ok {
		return fmt.Errorf("alertFlap still holds %q after the window (%d entries)", key, len(b.alertFlap))
	}
	return nil
}

// ---- 11. regressions (claude-audit on the merged branch) ------------------------------

// pass moves BOTH clocks: the fake clock the broker/mailer read and miniredis's TTL clock,
// so a lease that nobody refreshes really does expire.
func (s *adState) pass(d time.Duration) {
	s.clock.advance(d)
	s.mr.FastForward(d)
}

func (s *adState) conditionsFireInsideWindow(n int) error {
	s.digestKeys = sixModels[:n]
	s.markBefore = s.prov.count()
	s.dropModels(s.primary(), s.digestKeys)
	s.settle() // the onset goroutines land in the digest window; the clock does NOT move
	return nil
}

// stopSignalLater is the production shutdown sequence (main.go) on a broker whose digest
// window is still open: the alert layer is quiesced, then the mailer drains.
func (s *adState) stopSignalLater(laterSecs, budgetSecs int) error {
	b := s.primary()
	s.runFor(time.Duration(laterSecs) * time.Second)
	budget := time.Duration(budgetSecs) * time.Second
	s.drainDone = make(chan struct{})
	go func() {
		b.shutdownAlerts(2 * sharedOpTimeout)
		b.mail.drain(budget)
		close(s.drainDone)
	}()
	s.runFor(budget)
	select {
	case <-s.drainDone:
	case <-time.After(5 * time.Second):
		return fmt.Errorf("drain did not return within its budget")
	}
	return nil
}

func (s *adState) conditionsReachProviderOrCountedShutdown(n int) error {
	posts := s.prov.snapshot()[s.markBefore:]
	per := map[string]int{}
	for _, p := range posts {
		if strings.HasPrefix(p.subject, fmt.Sprintf("%s%d conditions:", alertSubjectPrefix, n)) {
			per[p.to]++
		}
	}
	delivered := true
	for _, r := range s.recipients {
		if per[r] != 1 {
			delivered = false
		}
	}
	if delivered {
		return nil
	}
	st := s.mailer().emailStats()
	dropped := st["email_dropped"].(map[string]int64)["shutdown"]
	if dropped > 0 && s.logs.lines("shutdown") > 0 {
		return nil
	}
	return fmt.Errorf("%d conditions raised before shutdown vanished silently: digests per recipient=%v dropped{shutdown}=%d shutdown log lines=%d",
		n, per, dropped, s.logs.lines("shutdown"))
}

func (s *adState) everyEmailAccounted() error {
	st := s.mailer().emailStats()
	queued, sent := st["email_queued"].(int64), st["email_sent"].(int64)
	var dropped int64
	for _, v := range st["email_dropped"].(map[string]int64) {
		dropped += v
	}
	if queued != sent+dropped {
		return fmt.Errorf("email_queued %d != email_sent %d + email_dropped %d", queued, sent, dropped)
	}
	return nil
}

// instanceRestarts replaces the primary with a FRESH process on the same shared store: no
// local mirror, booted right now (so its startup grace is running).
func (s *adState) instanceRestarts() error {
	s.a = s.newBroker()
	s.a.startTime = s.clock.Now()
	return nil
}

func (s *adState) modelReturnsDuringGrace() error {
	b := s.primary()
	onAir(b, "n1", "m")
	s.clock.advance(30 * time.Second)
	s.tick(b)
	s.settle()
	if s.logs.lines("alerts: in startup grace") == 0 {
		return fmt.Errorf("the restarted instance was not in its startup grace")
	}
	return nil
}

// modelDropsAfterGraceAndDebounce: miniredis's clock is deliberately NOT advanced here, so
// it is the release on restore - not a lease expiry - that must un-silence the onset.
func (s *adState) modelDropsAfterGraceAndDebounce() error {
	b := s.primary()
	s.markBefore = s.prov.count()
	offAir(b, "n1")
	s.clock.advance(b.alertCfg.grace)
	for i := 0; i < b.alertCfg.debounceTicks; i++ {
		s.tick(b)
	}
	s.runFor(10 * time.Second)
	return nil
}

func (s *adState) ticksPassMinuteApart(n int) error {
	b := s.primary()
	for i := 0; i < n; i++ {
		s.pass(time.Minute)
		s.tick(b)
		s.settle()
	}
	return nil
}

func (s *adState) storeNoLongerHolds(key string) error {
	if full := alertKeyPrefix + key; s.mr.Exists(full) {
		return fmt.Errorf("%s still exists (TTL %s) though no instance is firing it", full, s.mr.TTL(full))
	}
	return nil
}

func (s *adState) hoursPassStillAbsentBoth(h int) error {
	s.markBefore = s.prov.count()
	s.pass(time.Duration(h) * time.Hour)
	s.tick(s.a)
	s.settle()
	s.tick(s.b)
	s.runFor(10 * time.Second)
	return nil
}

// raiseMilestone fires a milestone through its REAL caller.
func (s *adState) raiseMilestone(key string) error {
	b := s.primary()
	switch key {
	case "first_ban":
		b.alertFirstBan("account", "acct_x", "3 corroborated abuse reports")
	case "first_dispute":
		b.alertFirstDispute("dp_1", 10)
	case "first_report":
		b.alertFirstReport("abuse", "n1")
	case "first_live_topup":
		b.alertFirstLiveTopup("u_x", 10, 20)
	case "csam:first-report":
		b.preserveCSAM("u_x", "203.0.113.9", "csam", []byte("x"))
	default:
		return fmt.Errorf("unknown milestone %q", key)
	}
	return nil
}

func (s *adState) milestoneFired(key string) error {
	if err := s.raiseMilestone(key); err != nil {
		return err
	}
	s.runFor(10 * time.Second)
	if got := s.prov.count(); got != len(s.recipients) {
		return fmt.Errorf("%d POSTs after the milestone, want %d", got, len(s.recipients))
	}
	s.alertSubj = key
	return nil
}

func (s *adState) hoursPassMilestoneAgain(h int) error {
	s.markBefore = s.prov.count()
	s.pass(time.Duration(h) * time.Hour)
	if err := s.raiseMilestone(s.alertSubj); err != nil {
		return err
	}
	s.runFor(10 * time.Second)
	return nil
}

func (s *adState) noFurtherEmail() error {
	if got := s.prov.snapshot()[s.markBefore:]; len(got) != 0 {
		return fmt.Errorf("%d further POST(s), want 0; first subject %q", len(got), got[0].subject)
	}
	return nil
}

// bothCSAMAlongsideNoproviders: the SLA breach and a fresh preserved incident land in the
// same checker moment as six supply gaps.
func (s *adState) bothCSAMAlongsideNoproviders(n int) error {
	b := s.primary()
	mem := b.db.(*store.Mem)
	if _, err := mem.PreserveCSAM(store.CSAMIncident{Pseudonym: "u_x", Category: "csam", ReportState: store.CSAMQueued}); err != nil {
		return err
	}
	s.digestKeys = sixModels[:n]
	for i, m := range s.digestKeys {
		onAir(b, fmt.Sprintf("n%d", i), m)
	}
	s.tick(b)
	for i := range s.digestKeys {
		offAir(b, fmt.Sprintf("n%d", i))
	}
	s.clock.advance(25 * time.Hour)
	for i := 0; i < b.alertCfg.debounceTicks-1; i++ {
		s.tick(b)
	}
	b.preserveCSAM("u_y", "203.0.113.9", "csam", []byte("x")) // csam:first-report
	s.tick(b)                                                 // csam_sla + the six gaps
	s.settle()
	return nil
}

func (s *adState) eachCSAMImmediateOwnEmail() error {
	// Right now: every CSAM page is either already POSTed or waiting ONLY on the pacer in
	// the transactional lane; nothing sits in the alert lane (the gaps are in the window).
	m := s.mailer()
	if queued := m.queuedSubjects(laneAlert); len(queued) != 0 {
		return fmt.Errorf("alert lane holds %v before the window elapsed; a CSAM page must not ride it", queued)
	}
	if n := s.pagesAndQueued(laneTransactional); n != 2*len(s.recipients) {
		return fmt.Errorf("%d CSAM pages sent+queued on the priority lane, want %d: %s",
			n, 2*len(s.recipients), s.postDigest())
	}
	// Step only through pacing (4/s), never past the coalescing window.
	s.runUntil(s.clock.Now().Add(2 * time.Second))
	posts := s.prov.snapshot()
	if len(posts) != 2*len(s.recipients) {
		return fmt.Errorf("%d POSTs before the window elapsed, want %d (two CSAM pages per recipient)", len(posts), 2*len(s.recipients))
	}
	subjects := map[string]map[string]bool{}
	for _, p := range posts {
		if !strings.Contains(strings.ToUpper(p.subject), "CSAM") || strings.Contains(p.subject, "conditions:") {
			return fmt.Errorf("pre-window POST subject = %q, want a standalone CSAM page", p.subject)
		}
		if subjects[p.to] == nil {
			subjects[p.to] = map[string]bool{}
		}
		subjects[p.to][p.subject] = true
	}
	for _, r := range s.recipients {
		if len(subjects[r]) != 2 {
			return fmt.Errorf("recipient %s got CSAM subjects %v, want both the SLA page and the preserved-incident page", r, subjects[r])
		}
	}
	return nil
}

// firesOnceStep is the "<key> onsets once" Given: one onset, paged or not, fully settled.
func (s *adState) firesOnceStep(key string) error {
	s.fireKey(s.primary(), key)
	return nil
}

// flapsNMoreTimes clears and re-fires key n more times, as a bouncing station does.
func (s *adState) flapsNMoreTimes(key string, n int) error {
	b := s.primary()
	for i := 0; i < n; i++ {
		s.clock.advance(time.Minute)
		b.alertClear(key)
		s.clock.advance(time.Minute)
		s.fireKey(b, key)
	}
	return nil
}

// noSharedStore drops the shared layer entirely (a single-instance deploy), so every
// cross-instance decision falls back to this process.
func (s *adState) noSharedStore() error {
	s.primary().shared = nil
	return nil
}

// retriesTaken is the number of extra delivery attempts the senders made.
func (s *adState) retriesTaken() int64 {
	var n int64
	for _, m := range s.mailers {
		n += m.emailStats()["email_retries"].(int64)
	}
	return n
}

func (s *adState) providerLosesFirstResponse() error {
	s.prov.mu.Lock()
	s.prov.loseFirst = map[string]bool{}
	s.prov.mu.Unlock()
	return nil
}

func (s *adState) moreAttemptsThanEmails() error {
	if s.prov.attempts() <= s.prov.count() {
		return fmt.Errorf("fixture: %d attempts vs %d emails - no delivery was retried, so this scenario proves nothing",
			s.prov.attempts(), s.prov.count())
	}
	if r := s.retriesTaken(); r == 0 {
		return fmt.Errorf("the sender recorded no retries although the provider saw repeats")
	}
	return nil
}

func (s *adState) everyAttemptCarriesItsKey() error {
	byKey := map[string]string{}
	for _, p := range s.prov.attemptSnapshot() {
		if p.key == "" {
			return fmt.Errorf("an attempt (to=%s subj=%q) carried no idempotency key: a retry is indistinguishable from a second page",
				p.to, p.subject)
		}
		id := p.to + "|" + p.subject
		if prev, ok := byKey[p.key]; ok && prev != id {
			return fmt.Errorf("idempotency key %q was reused across two different emails (%s vs %s)", p.key, prev, id)
		}
		byKey[p.key] = id
	}
	return nil
}

// ---- suite -----------------------------------------------------------------------------

func TestAlertDeliveryFeature(t *testing.T) {
	s := &adState{}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				s.reset(t)
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				s.teardown()
				return ctx, nil
			})

			// Background
			sc.Step(`^a broker with ADMIN_EMAIL set to three recipients$`, s.brokerWithThreeRecipients)
			sc.Step(`^an httptest email provider that records every POST with its timestamp$`, s.providerRecordsPosts)

			// 1. pacing
			sc.Step(`^the email rate is (\d+) per second$`, s.emailRate)
			sc.Step(`^(\d+) emails are enqueued in the same millisecond$`, s.enqueueNSameMillisecond)
			sc.Step(`^the provider receives all (\d+)$`, s.providerReceivesAll)
			sc.Step(`^no 1-second window contains more than (\d+) POSTs$`, s.noWindowMoreThan)
			sc.Step(`^the provider hangs on every POST$`, s.providerHangs)
			sc.Step(`^(\d+) emails are enqueued from a request goroutine$`, s.enqueueFromRequestGoroutine)
			sc.Step(`^each enqueue returns in under (\d+) milliseconds$`, s.eachEnqueueUnder)
			sc.Step(`^the request goroutine is never blocked on delivery$`, s.requestGoroutineNeverBlocked)
			sc.Step(`^the email queue capacity is (\d+) and the sender is paused$`, s.queueCapPaused)
			sc.Step(`^(\d+) ops alerts are queued$`, s.opsAlertsQueued)
			sc.Step(`^a sign-in code is enqueued$`, s.signInCodeEnqueued)
			sc.Step(`^the sign-in code is queued and the oldest ops alert is evicted with reason "queue-full"$`, s.signInQueuedOldestAlertEvicted)
			sc.Step(`^a 6th ops alert is enqueued$`, s.sixthAlertEnqueued)
			sc.Step(`^it is dropped with reason "queue-full" and the counter email_dropped\{queue-full\} reads (\d+)$`, s.droppedQueueFullCounter)
			sc.Step(`^two broker instances each at (\d+) per second$`, s.twoInstancesEachAt)
			sc.Step(`^both burst (\d+) alerts at once$`, s.bothBurst)
			sc.Step(`^no 1-second window across both providers' logs contains more than (\d+) POSTs$`, s.noWindowMoreThan)
			sc.Step(`^no email provider key is set$`, s.noEmailProviderKey)
			sc.Step(`^(\d+) alerts fire$`, s.alertsFire)
			sc.Step(`^nothing is queued and one "transactional email disabled" line is logged$`, s.nothingQueuedDisabledLoggedOnce)

			// 2. lanes
			sc.Step(`^the sign-in code is the next POST the provider receives$`, s.signInCodeIsNextPost)
			sc.Step(`^a (sign-in code|top-up receipt|cap notice|operator warning|operator ban notice|payout notice) email is enqueued$`, s.kindEnqueued)
			sc.Step(`^it is delivered before any of the alerts$`, s.deliveredBeforeAlerts)
			sc.Step(`^alerts A, B, C are enqueued in that order$`, s.alertsABC)
			sc.Step(`^the provider receives A, B, C in that order$`, s.receivesABC)

			// 3. retry
			sc.Step(`^the provider returns 429 with "Retry-After: (\d+)" once, then 200$`, s.provider429RetryAfterOnce)
			sc.Step(`^the provider returns 429 with "Retry-After: (\d+)"$`, s.provider429RetryAfterAlways)
			sc.Step(`^an alert is sent$`, s.anAlertIsSent)
			sc.Step(`^the provider received (\d+) POSTs at least (\d+) seconds apart$`, s.postsAtLeastSecondsApart)
			sc.Step(`^the alert was delivered$`, s.alertWasDelivered)
			sc.Step(`^the counter email_retries reads (\d+)$`, s.counterRetries)
			sc.Step(`^the provider returns 429 with no Retry-After three times, then 200$`, s.provider429NoRetryAfterThrice)
			sc.Step(`^the gaps between POSTs are about 1s, 2s, 4s \(\+-25%\)$`, s.gapsAbout124)
			sc.Step(`^the alert was delivered on the (\d+)(?:st|nd|rd|th) POST$`, s.deliveredOnNthPost)
			sc.Step(`^the provider returns 500 forever$`, s.provider500Forever)
			sc.Step(`^the provider received (\d+) POSTs \(1 \+ 3 retries\)$`, s.providerReceivedPosts)
			sc.Step(`^one "email: DROPPED after 3 retries \(to=<masked> subj=\.\.\.\)" line is logged$`, s.droppedLineLogged)
			sc.Step(`^the counter email_dropped\{retries\} reads (\d+)$`, s.droppedRetriesCounter)
			sc.Step(`^the provider returns (\d+) once, then 200$`, s.providerStatusOnce)
			sc.Step(`^the provider received (\d+) POSTs$`, s.providerReceivedPosts)
			sc.Step(`^the provider closes the connection once, then 200$`, s.providerClosesOnce)
			sc.Step(`^the provider returns 429 for the first (\d+) POSTs$`, s.provider429FirstN)
			sc.Step(`^(\d+) alerts are sent$`, s.alertsAreSent)
			sc.Step(`^no 1-second window contains more than (\d+) POSTs including retries$`, s.noWindowMoreThanIncludingRetries)
			sc.Step(`^an alert fires from the alert checker$`, s.alertFiresFromChecker)
			sc.Step(`^the checker tick completes in under 100 milliseconds$`, s.tickUnder100ms)

			// 4. coalescing
			sc.Step(`^the coalescing window is (\d+) seconds$`, s.coalescingWindow)
			sc.Step(`^(\d+) "noproviders:<model>" conditions fire in the same checker tick$`, s.sixNoprovidersFire)
			sc.Step(`^each recipient receives exactly ONE email$`, s.eachRecipientOneEmail)
			sc.Step(`^its subject is "([^"]*)"$`, s.subjectIs)
			sc.Step(`^its body lists all (\d+) conditions with their facts$`, s.bodyListsConditions)
			sc.Step(`^the provider received (\d+) POSTs total, not (\d+)$`, s.providerReceivedTotalNot)
			sc.Step(`^one alert fires$`, s.oneAlertFires)
			sc.Step(`^it is delivered within the coalescing window plus pacing$`, s.deliveredWithinWindowPlusPacing)
			sc.Step(`^a "csam_sla" alert fires alongside (\d+) noproviders alerts$`, s.csamAlongsideNoproviders)
			sc.Step(`^the CSAM alert is sent immediately as its own email on the priority lane$`, s.csamSentImmediatelyOwnEmail)
			sc.Step(`^the (\d+) others become one digest$`, s.othersBecomeOneDigest)
			sc.Step(`^(\d+) conditions fired as one digest$`, s.conditionsFiredAsDigest)
			sc.Step(`^all (\d+) clear$`, s.allClear)
			sc.Step(`^(\d+) "alert: CLEARED" log lines exist$`, s.clearedLines)

			// 5. dedup
			sc.Step(`^two broker instances share the store$`, s.twoInstancesShareStore)
			sc.Step(`^both detect "([^"]*)" in the same minute$`, s.bothDetect)
			sc.Step(`^exactly one digest email per recipient is sent$`, s.exactlyOneDigestPerRecipient)
			sc.Step(`^the store holds rogerai:alert:(\S+) with a TTL$`, s.storeHoldsKeyWithTTL)
			sc.Step(`^"([^"]*)" fired and the key exists$`, s.firedAndKeyExists)
			sc.Step(`^the model returns on air and the checker clears it$`, s.modelReturnsAndClears)
			sc.Step(`^the key is deleted$`, s.keyDeleted)
			sc.Step(`^the model drops again after the debounce$`, s.modelDropsAgain)
			sc.Step(`^it pages again$`, s.pagesAgain)
			sc.Step(`^"([^"]*)" fired 25 hours ago and is still firing$`, s.fired25hAgoStillFiring)
			sc.Step(`^it pages once more \(TTL 24h elapsed\)$`, s.pagesOnceMore)
			sc.Step(`^the shared store is unreachable$`, s.sharedStoreUnreachable)
			sc.Step(`^"([^"]*)" fires on both instances$`, s.firesOnBothInstances)
			sc.Step(`^each instance pages once \(today's behavior\) and logs the fallback once$`, s.eachInstancePagesOnceLogsFallbackOnce)
			sc.Step(`^the alert key prefix is "rogerai:alert:" and no other subsystem writes under it$`, s.alertPrefixNamespaced)

			// 6. grace + debounce
			sc.Step(`^the broker booted (\d+) seconds ago and re-hydrated (\d+) registrations$`, s.bootedSecondsAgoRehydrated)
			sc.Step(`^the checker sees "([^"]*)" with 0 providers$`, s.checkerSeesZeroProviders)
			sc.Step(`^no alert fires and one "alerts: in startup grace" line is logged$`, s.noAlertGraceLogged)
			sc.Step(`^the grace has elapsed and "([^"]*)" was on air$`, s.graceElapsedModelOnAir)
			sc.Step(`^the checker sees "([^"]*)" absent on one tick and present on the next$`, s.absentOneTickPresentNext)
			sc.Step(`^no alert fires$`, s.noAlertFires)
			sc.Step(`^the checker sees "([^"]*)" absent on two consecutive ticks$`, s.absentTwoTicks)
			sc.Step(`^"(noproviders:[^"]*)" fires once$`, s.firesOnce)
			sc.Step(`^the checker sees the durable store unreachable on one tick$`, s.durableStoreUnreachableOneTick)
			sc.Step(`^"db_down" fires immediately$`, s.dbDownFiresImmediately)
			sc.Step(`^the broker booted (\d+) seconds ago$`, s.bootedSecondsAgo)
			sc.Step(`^a CSAM incident has been queued longer than the SLA$`, s.csamQueuedPastSLA)
			sc.Step(`^the checker runs$`, s.checkerRuns)
			sc.Step(`^"csam_sla" fires immediately$`, s.csamFiresImmediately)

			// 7. flap
			sc.Step(`^"([^"]*)" fires, clears, fires, clears, and fires again within (\d+) minutes$`, s.flapThreeTimes)
			sc.Step(`^three pages were sent$`, s.threePagesSent)
			sc.Step(`^the third page's subject ends with "([^"]*)"$`, s.thirdPageSubjectEndsWith)
			sc.Step(`^it clears and fires a fourth time (\d+) minutes later$`, s.clearsAndFiresFourth)
			sc.Step(`^no email is sent and "alert: MUTED \(flapping\) ([^"]*)" is logged$`, s.noEmailMutedLogged)
			sc.Step(`^"([^"]*)" is muted after flapping (\d+) times$`, s.mutedAfterFlapping)
			sc.Step(`^it stays clear for (\d+) minutes$`, s.staysClearFor)
			sc.Step(`^one "\[RogerAI ALERT\] (\S+) stabilized after (\d+) onsets" email is sent per recipient$`, s.stabilizedEmailPerRecipient)
			sc.Step(`^the mute is lifted$`, s.muteLifted)
			sc.Step(`^"([^"]*)" is muted$`, s.keyIsMuted)
			sc.Step(`^"([^"]*)" fires for the first time$`, s.firesFirstTime)
			sc.Step(`^it pages normally$`, s.pagesNormally)
			sc.Step(`^two instances$`, s.twoInstances)
			sc.Step(`^"([^"]*)" flaps across both instances$`, s.flapsAcrossBoth)
			sc.Step(`^"([^"]*)" fires and clears (\d+) times within (\d+) minutes$`, s.firesAndClearsN)
			sc.Step(`^(\d+) pages were sent, each without the flapping suffix$`, s.nPagesNoFlapSuffix)
			sc.Step(`^no "alert: MUTED \(flapping\)" line is logged$`, s.noMutedLine)
			sc.Step(`^they count toward one flap total$`, s.countTowardOneTotal)

			// 8. observability + isolation
			sc.Step(`^the founder reads /admin/live$`, s.founderReadsAdminLive)
			sc.Step(`^it shows email_queued, email_sent, email_retries, email_dropped by reason, alerts_coalesced, alerts_deduped, alerts_muted, and the queue depth per lane$`, s.adminLiveShowsCounters)
			sc.Step(`^the provider returns 429 forever and (\d+) alerts fire$`, s.provider429ForeverAlertsFire)
			sc.Step(`^a funded consumer relays a prompt$`, s.fundedConsumerRelays)
			sc.Step(`^the relay completes at the station's speed$`, s.relayAtStationSpeed)
			sc.Step(`^(\d+) sign-in codes and (\d+) alerts are queued$`, s.codesAndAlertsQueued)
			sc.Step(`^the broker receives a stop signal with a (\d+) second drain budget$`, s.stopSignalWithDrainBudget)
			sc.Step(`^the (\d+) sign-in codes are delivered first$`, s.codesDeliveredFirst)
			sc.Step(`^undelivered alerts are counted as dropped\{shutdown\}$`, s.undeliveredCountedShutdown)

			// 9. regression
			sc.Step(`^two instances, three recipients, a 10/s provider, and (\d+) models that drop in one tick$`, s.rigFlapFixture)
			sc.Step(`^the checker runs on both instances after the debounce$`, s.checkerRunsBothAfterDebounce)
			sc.Step(`^the provider received exactly (\d+) POSTs, all 200$`, s.exactlyNPostsAll200)
			sc.Step(`^zero "resend error 429" lines were logged$`, s.zero429Lines)
			sc.Step(`^each recipient received one digest naming all (\d+) models$`, s.eachRecipientOneDigestNaming)

			// 10. regressions
			sc.Step(`^the shared store fails every command$`, s.storeFailsEveryCommand)
			sc.Step(`^the shared store recovers$`, s.storeRecovers)
			sc.Step(`^the clear is pending and the key still exists$`, s.clearPendingKeyStillExists)
			sc.Step(`^the condition re-onsets before the checker retries the clear$`, s.reOnsetBeforeRetry)
			sc.Step(`^the key is claimed again and no clear is pending$`, s.keyClaimedAgainNoPending)
			sc.Step(`^no clear is pending$`, s.noClearPending)
			sc.Step(`^the email retries knob is (\d+)$`, s.retriesKnob)
			sc.Step(`^the email is eventually dropped after (\d+) retries$`, s.eventuallyDroppedAfter)
			sc.Step(`^the sender is still alive$`, s.senderStillAlive)
			sc.Step(`^the shared store hangs on every command$`, s.storeHangs)
			sc.Step(`^the /billing drift check fires an alert$`, s.billingDriftFires)
			sc.Step(`^the check returns in under 50 milliseconds$`, s.checkUnder50ms)
			sc.Step(`^the page is still sent once the store answers$`, s.pageStillSent)
			sc.Step(`^"([^"]*)" fires once and clears$`, s.firesOnceAndClears)
			sc.Step(`^"([^"]*)" onsets once$`, s.firesOnceStep)
			sc.Step(`^"([^"]*)" flaps (\d+) more times$`, s.flapsNMoreTimes)
			sc.Step(`^the broker has no shared store$`, s.noSharedStore)
			sc.Step(`^the provider loses the response to every first attempt$`, s.providerLosesFirstResponse)
			sc.Step(`^the provider saw more attempts than emails$`, s.moreAttemptsThanEmails)
			sc.Step(`^every attempt carried its email's idempotency key$`, s.everyAttemptCarriesItsKey)
			sc.Step(`^the flap window elapses and the checker runs$`, s.flapWindowElapsesChecker)
			sc.Step(`^the flap table no longer holds "([^"]*)"$`, s.flapTableNoLongerHolds)

			// 11. regressions (claude-audit on the merged branch)
			sc.Step(`^(\d+) "noproviders:<model>" conditions fire inside the coalescing window$`, s.conditionsFireInsideWindow)
			sc.Step(`^the broker receives a stop signal (\d+) seconds? later with a (\d+) second drain budget$`, s.stopSignalLater)
			sc.Step(`^all (\d+) conditions reach the provider as one digest per recipient, or are counted dropped\{shutdown\} with a log line$`, s.conditionsReachProviderOrCountedShutdown)
			sc.Step(`^every queued email is accounted for: email_queued equals email_sent plus email_dropped$`, s.everyEmailAccounted)
			sc.Step(`^the instance restarts$`, s.instanceRestarts)
			sc.Step(`^the model returns on air during the startup grace$`, s.modelReturnsDuringGrace)
			sc.Step(`^the model drops again after the grace and the debounce$`, s.modelDropsAfterGraceAndDebounce)
			sc.Step(`^it pages exactly once$`, s.pagesAgain)
			sc.Step(`^(\d+) checker ticks pass a minute apart with the model still absent$`, s.ticksPassMinuteApart)
			sc.Step(`^the store still holds rogerai:alert:(\S+) with a TTL$`, s.storeHoldsKeyWithTTL)
			sc.Step(`^(\d+) minutes of checker ticks pass on the restarted instance$`, s.ticksPassMinuteApart)
			sc.Step(`^the store no longer holds rogerai:alert:(\S+)$`, s.storeNoLongerHolds)
			sc.Step(`^(\d+) hours pass with the model still absent on both instances$`, s.hoursPassStillAbsentBoth)
			sc.Step(`^the "([^"]*)" milestone fired$`, s.milestoneFired)
			sc.Step(`^(\d+) hours pass and the same milestone is raised again$`, s.hoursPassMilestoneAgain)
			sc.Step(`^no further email is sent$`, s.noFurtherEmail)
			sc.Step(`^a "csam_sla" alert and a "csam:first-report" alert fire alongside (\d+) noproviders alerts$`, s.bothCSAMAlongsideNoproviders)
			sc.Step(`^each CSAM alert is sent immediately as its own email on the priority lane$`, s.eachCSAMImmediateOwnEmail)
		},
		Options: &godog.Options{
			Format: "pretty", Paths: []string{"../../features/ops/alert_delivery.feature"},
			TestingT: t, Strict: true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("alert_delivery.feature: scenarios failed")
	}
}

// setPaused flips the sender's test-only pause under the queue lock.
func setPaused(m *mailer, v bool) {
	m.q.mu.Lock()
	m.q.paused = v
	m.q.mu.Unlock()
}

// pagesAndQueued counts DISTINCT emails: those the provider has already received (by the
// idempotency key each attempt repeats) plus those still queued or awaiting a retry on the
// given lane (by the same id). An email being retried sits in BOTH places, so counting ids
// rather than adding two tallies keeps it one email.
func (s *adState) pagesAndQueued(lane emailLane) int {
	ids := map[string]bool{}
	for _, p := range s.prov.snapshot() {
		ids[p.key] = true
	}
	for _, m := range s.mailers {
		for _, id := range m.queuedIDs(lane) {
			ids[id] = true
		}
	}
	return len(ids)
}

// queuedIDs lists the idempotency keys still queued (fresh + retrying) on a lane.
func (m *mailer) queuedIDs(lane emailLane) []string {
	m.q.mu.Lock()
	defer m.q.mu.Unlock()
	var out []string
	for _, j := range m.q.lanes[lane] {
		out = append(out, j.id)
	}
	for _, j := range m.q.retrying[lane] {
		out = append(out, j.id)
	}
	return out
}

// queuedSubjects lists the subjects still queued (fresh + retrying) on a lane.
func (m *mailer) queuedSubjects(lane emailLane) []string {
	m.q.mu.Lock()
	defer m.q.mu.Unlock()
	var out []string
	for _, j := range m.q.lanes[lane] {
		out = append(out, j.subject)
	}
	for _, j := range m.q.retrying[lane] {
		out = append(out, j.subject)
	}
	return out
}
