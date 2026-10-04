package main

// shared_store_readiness_bdd_test.go makes features/ops/shared_store_readiness.feature
// EXECUTABLE. "The broker boots" calls main's own shared-store boot on the harness broker
// (broker.bootShared: connect, or not-ready + retry loop + readiness gate), and every request
// goes through the broker's real mux (b.routes()).
//
// The shared-store address "not answering yet" is a TCP listener that accepts and closes, so
// every connection attempt is counted; "starts answering" replaces it with a miniredis at the
// same address. Waits are real time and scaled (seconds, not minutes) through the retry's
// clock seam (sharedRetrySleep).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/deviceauth"
	"rogerai.fm/roger/v6/internal/emailauth"
)

type rdState struct {
	*psState
	addr     string
	ln       net.Listener
	mu       sync.Mutex
	attempts []time.Time
	live     *miniredis.Miniredis
	envs     []string
	booted   bool
	resp     psResult
	readyAt  time.Time
	peer     *broker
	retryRes psResult

	pendingDevice deviceauth.Pending
	emailCode     string
	wiring        rdWiring
}

// rdWiring is the shared wiring as it stood when the broker became ready.
type rdWiring struct {
	shared  sharedStore
	devices *deviceauth.Flow
	emails  *emailauth.Flow
}

func (s *rdState) rdReset() error {
	if err := s.psReset(); err != nil {
		return err
	}
	s.addr, s.ln, s.attempts, s.live, s.envs, s.booted = "", nil, nil, nil, nil, false
	s.resp, s.peer, s.retryRes = psResult{}, nil, psResult{}
	s.pendingDevice, s.emailCode, s.wiring = deviceauth.Pending{}, "", rdWiring{}
	return nil
}

func (s *rdState) rdTeardown() {
	for _, b := range []*broker{s.b, s.peer} {
		if b != nil {
			b.stopSharedRetry()
		}
	}
	if s.ln != nil {
		_ = s.ln.Close()
	}
	if s.live != nil {
		s.live.Close()
	}
	for _, e := range s.envs {
		_ = os.Unsetenv(e)
	}
}

func (s *rdState) setenv(k, v string) {
	_ = os.Setenv(k, v)
	s.envs = append(s.envs, k)
}

// --- Background --------------------------------------------------------------------------

func (s *rdState) durableOK() error { return nil }

func (s *rdState) notAnswering() error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	s.ln, s.addr = ln, ln.Addr().String()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.attempts = append(s.attempts, time.Now())
			s.mu.Unlock()
			_ = c.Close()
		}
	}()
	return nil
}

// --- configuration ---------------------------------------------------------------------------

func (s *rdState) configured(setting string) error {
	url := "redis://" + s.addr
	switch {
	case strings.HasPrefix(setting, "ROGERAI_MULTI_INSTANCE=1 and ROGERAI_REDIS_URL"):
		s.setenv("ROGERAI_MULTI_INSTANCE", "1")
		s.setenv("ROGERAI_REDIS_URL", url)
	case strings.HasPrefix(setting, "ROGERAI_REDIS_URL"):
		s.setenv("ROGERAI_REDIS_URL", url)
	case strings.HasPrefix(setting, "ROGERAI_REDIS_RING"):
		s.setenv("ROGERAI_REDIS_RING", url)
	case strings.HasPrefix(setting, "ROGERAI_REDIS_CLUSTER"):
		s.setenv("ROGERAI_REDIS_CLUSTER", url)
	default:
		return fmt.Errorf("unknown setting %q", setting)
	}
	return nil
}

func (s *rdState) configuredURL() error {
	return s.configured("ROGERAI_REDIS_URL pointing at the address")
}

func (s *rdState) multiNoAddress() error {
	s.setenv("ROGERAI_MULTI_INSTANCE", "1")
	return nil
}

func (s *rdState) noSetting() error { return nil }

func (s *rdState) secretURL(url string) error {
	s.setenv("ROGERAI_REDIS_URL", url)
	return nil
}

// asMainBuilds puts b's shared-store-related state where buildBroker leaves it before the
// boot: no shared store yet (the harness pre-wires one) and the production limiters (the
// harness's identity limiter is unlimited).
func asMainBuilds(b *broker) {
	b.shared = nil
	b.rl = loadRateLimiter()
	if b.anonRL == nil {
		b.anonRL = &rateLimiter{buckets: map[string]*tokenBucket{}}
	}
}

func (s *rdState) boots() error {
	asMainBuilds(s.b)
	s.b.bootShared()
	s.booted = true
	return nil
}

func (s *rdState) get(path string) psResult {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	s.b.routes().ServeHTTP(w, r)
	return psResult{code: w.Code, body: w.Body.Bytes(), hdr: w.Header()}
}

func (s *rdState) readyIs(code int) error {
	s.resp = s.get("/ready")
	if s.resp.code != code {
		return fmt.Errorf("GET /ready = %d %s, want %d", s.resp.code, s.resp.body, code)
	}
	return nil
}

func (s *rdState) readyBody() map[string]any {
	var m map[string]any
	_ = json.Unmarshal(s.resp.body, &m)
	return m
}

func (s *rdState) bodyNotReadyConnecting() error {
	m := s.readyBody()
	if m["ready"] != false || m["shared"] != "connecting" {
		return fmt.Errorf("readiness body %s, want ready false and shared \"connecting\"", s.resp.body)
	}
	return nil
}

func (s *rdState) bodySharedIs(v string) error {
	m := s.readyBody()
	if m["shared"] != v {
		return fmt.Errorf("readiness body %s, want shared %q", s.resp.body, v)
	}
	return nil
}

func (s *rdState) ready200True() error {
	if err := s.readyIs(200); err != nil {
		return err
	}
	if s.readyBody()["ready"] != true {
		return fmt.Errorf("readiness body %s, want ready true", s.resp.body)
	}
	return nil
}

func (s *rdState) noSharedKey() error {
	if _, ok := s.readyBody()["shared"]; ok {
		return fmt.Errorf("readiness body %s carries a shared key", s.resp.body)
	}
	return nil
}

func (s *rdState) signedServed() error {
	if err := s.ensureFunded(); err != nil {
		return err
	}
	s.standUp("s1", stationOpts{model: "m"})
	r := s.relayMux("m")
	if r.code != 200 {
		return fmt.Errorf("signed chat request = %d %s, want 200", r.code, r.body)
	}
	return nil
}

func (s *rdState) healthOK() error {
	r := s.get("/health")
	if r.code != 200 || string(r.body) != "ok" {
		return fmt.Errorf("GET /health = %d %q, want 200 \"ok\"", r.code, r.body)
	}
	return nil
}

// bootsAndStaysDown boots with the retry's clock seam scaled 1/100 (the 30 s cap is 300 ms of
// real time), so 4 s of real time covers well over the 2 minutes the scenario names; the
// backoff schedule itself is pinned in virtual time by TestSharedRetryBackoffSchedule.
func (s *rdState) bootsAndStaysDown(int, string) error {
	saved := sharedRetrySleep
	sharedRetrySleep = func(d time.Duration) { time.Sleep(d / 100) }
	err := s.boots() // the loop captures the seam at boot
	sharedRetrySleep = saved
	if err != nil {
		return err
	}
	time.Sleep(4 * time.Second)
	return nil
}

func (s *rdState) attemptsMoreThan(n int) error {
	s.mu.Lock()
	got := len(s.attempts)
	s.mu.Unlock()
	if got <= n {
		return fmt.Errorf("the broker attempted the shared-store connection %d time(s) after boot, want more than %d (it must keep retrying)", got, n)
	}
	return nil
}

func (s *rdState) gapsAtMost30() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := 1; i < len(s.attempts); i++ {
		if s.attempts[i].Sub(s.attempts[i-1]) > 30*time.Second {
			return fmt.Errorf("attempts %d and %d were %s apart", i, i+1, s.attempts[i].Sub(s.attempts[i-1]))
		}
	}
	return nil
}

// loggedOnce counts this boot's shared-state lines. The log sink is process-global, so in a
// full package run other tests' leftover shared stores add their own rate-limited runtime
// lines ("shared-state: valkey <op> failed, using in-memory fallback"); the boot path never
// writes that line (its connect attempts do not go through noteErr), so those are excluded.
func (s *rdState) loggedOnce() error {
	n := 0
	for _, l := range strings.Split(s.logs.String(), "\n") {
		if strings.Contains(l, "shared-state") && !strings.Contains(l, "failed, using in-memory fallback") {
			n++
		}
	}
	if n != 1 {
		return fmt.Errorf("%d shared-state log lines, want exactly one for the outage", n)
	}
	return nil
}

func (s *rdState) stillRetrying() error {
	s.mu.Lock()
	before := len(s.attempts)
	s.mu.Unlock()
	time.Sleep(3 * time.Second)
	s.mu.Lock()
	after := len(s.attempts)
	s.mu.Unlock()
	if after <= before {
		return fmt.Errorf("no connection attempt in the last 3 s (%d total): the broker stopped retrying", after)
	}
	return nil
}

func (s *rdState) still503() error { return s.readyIs(503) }

// --- endpoints while not ready ----------------------------------------------------------------

func (s *rdState) call(endpoint string) psResult {
	var method, path string
	parts := strings.SplitN(endpoint, " ", 3)
	method, path = parts[0], parts[1]
	var body []byte
	switch {
	case path == "/v1/chat/completions":
		body = s.bodyFor("m", false)
	case path == "/v1/audio/speech":
		body = []byte(`{"model":"v","voice":"v","input":"hello"}`)
	case path == "/auth/email/start":
		body = []byte(`{"email":"x@example.com"}`)
	case path == "/nodes/register":
		body = []byte(`{}`)
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	if strings.Contains(endpoint, "funded signature") || strings.Contains(endpoint, "registered node") {
		signReq(r, s.consumerPriv, body)
	}
	if strings.Contains(endpoint, "admin key") {
		r.Header.Set("X-Roger-Admin", s.b.adminKey)
	}
	w := httptest.NewRecorder()
	s.b.routes().ServeHTTP(w, r)
	return psResult{code: w.Code, body: w.Body.Bytes(), hdr: w.Header()}
}

func (s *rdState) clientCalls(endpoint string) error {
	if err := s.ensureFunded(); err != nil {
		return err
	}
	s.holdsBeforeCall()
	s.resp = s.call(endpoint)
	return nil
}

var rdHoldsBefore int

func (s *rdState) holdsBeforeCall() {
	h, _, _, _, _, _ := s.ledgerRows()
	rdHoldsBefore = h
}

func (s *rdState) is503Unavailable() error {
	if s.resp.code != 503 || errCode(s.resp.body) != "shared_store_unavailable" {
		return fmt.Errorf("response %d %s, want 503 code \"shared_store_unavailable\"", s.resp.code, s.resp.body)
	}
	return nil
}

func (s *rdState) retryAfterAtMost30() error {
	ra := s.resp.hdr.Get("Retry-After")
	var n int
	if _, err := fmt.Sscanf(ra, "%d", &n); err != nil || n <= 0 || n > 30 {
		return fmt.Errorf("Retry-After %q, want 1..30", ra)
	}
	return nil
}

func (s *rdState) cost0() error {
	if got := s.resp.hdr.Get("X-RogerAI-Cost"); got != "0" {
		return fmt.Errorf("X-RogerAI-Cost %q, want \"0\"", got)
	}
	return nil
}

func (s *rdState) noWork() error {
	h, _, _, _, _, _ := s.ledgerRows()
	if h > rdHoldsBefore {
		return fmt.Errorf("a hold was placed while the broker was not ready")
	}
	for _, st := range s.stations {
		if st.upstreamCount() > 0 {
			return fmt.Errorf("a request was dispatched to %q while the broker was not ready", st.name)
		}
	}
	return nil
}

func (s *rdState) anonFree() error {
	s.standUp("f1", stationOpts{model: "fm"})
	body := s.bodyFor("fm", false)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.b.routes().ServeHTTP(w, r)
	s.resp = psResult{code: w.Code, body: w.Body.Bytes(), hdr: w.Header()}
	return nil
}

// relayMux fires one signed relay for model through the broker's real mux.
func (s *rdState) relayMux(model string) psResult {
	body := s.bodyFor(model, false)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	signReq(r, s.consumerPriv, body)
	w := &recWriter{ResponseRecorder: httptest.NewRecorder()}
	s.b.routes().ServeHTTP(w, r)
	return psResult{code: w.Code, body: w.Body.Bytes(), hdr: w.Header()}
}

func (s *rdState) signedPost() error {
	s.resp = s.relayMux("m")
	return nil
}

func (s *rdState) bodyLacks(v string) error {
	if bodyHas(s.resp.body, v) {
		return fmt.Errorf("the response body contains %q", v)
	}
	return nil
}

// --- becoming ready ---------------------------------------------------------------------------

func (s *rdState) startsAnswering() error {
	if s.ln != nil {
		_ = s.ln.Close()
		s.ln = nil
	}
	mr := miniredis.NewMiniRedis()
	if err := mr.StartAddr(s.addr); err != nil {
		return err
	}
	s.live = mr
	return nil
}

func (s *rdState) readyWithin(int) error {
	deadline := time.Now().Add(8 * time.Second) // scaled; see the file header
	for {
		s.resp = s.get("/ready")
		m := s.readyBody()
		if s.resp.code == 200 && m["ready"] == true && m["shared"] == "ok" {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("GET /ready = %d %s after the shared store started answering, want 200 ready true shared \"ok\" without a restart", s.resp.code, s.resp.body)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (s *rdState) notRestarted() error { return nil } // the same broker object served every probe

// stationOnM stands up a PRICED station: the billing scenarios count a non-zero spend row,
// which a free station (a $0 settle row) never writes.
func (s *rdState) stationOnM() error {
	s.standUp("s1", stationOpts{model: "m", priceIn: 0.1, priceOut: 0.3})
	return nil
}

// becomesReady boots the broker first when the scenario has not (the "is answering" Givens
// describe a broker that came up against a live shared store).
func (s *rdState) becomesReady() error {
	if !s.booted {
		if err := s.boots(); err != nil {
			return err
		}
	}
	if err := s.readyWithin(35); err != nil {
		return err
	}
	s.wiring = rdWiring{shared: s.b.shared, devices: s.b.devices, emails: s.b.emails}
	return nil
}

func (s *rdState) fundedRelaysM() error {
	if err := s.ensureFunded(); err != nil {
		return err
	}
	s.resp = s.relayMux("m")
	return nil
}

func (s *rdState) is200() error {
	if s.resp.code != 200 {
		return fmt.Errorf("response %d %s, want 200", s.resp.code, s.resp.body)
	}
	return nil
}

func (s *rdState) holdAndSettle() error {
	h, _, sp, _, _, err := s.ledgerRows()
	if err != nil {
		return err
	}
	if h < 1 || sp < 1 {
		return fmt.Errorf("ledger shows %d hold(s) and %d spend(s), want a hold and a settle", h, sp)
	}
	return nil
}

// peerInstance is a second broker over the same durable store, booted against the same
// shared-store address the way main boots (so it is wired exactly as a production peer).
func (s *rdState) peerInstance() error {
	p := s.newBroker()
	asMainBuilds(p)
	p.bootShared()
	if p.shared == nil || !p.shared.healthy() {
		return fmt.Errorf("the peer could not reach the shared store at the address")
	}
	s.peer = p
	return nil
}

// sharedBucket drains one signed identity's bucket on this instance, then asks the peer: one
// shared bucket means the peer refuses too.
func (s *rdState) sharedBucket() error {
	key := "rd-identity"
	n := 0
	for ; n < 100000; n++ {
		if ok, _ := s.b.rl.allow(key); !ok {
			break
		}
	}
	if n == 100000 {
		return fmt.Errorf("this instance's identity limiter never refused")
	}
	if ok, _ := s.peer.rl.allow(key); ok {
		return fmt.Errorf("the peer admitted the identity after this instance exhausted its bucket (%d requests): two buckets, not one", n)
	}
	return nil
}

var rdDevicePub = strings.Repeat("ab", 32)

func (s *rdState) deviceStarted() error {
	p, err := s.b.devices.Start(rdDevicePub)
	if err != nil {
		return err
	}
	s.pendingDevice = p
	return nil
}

func (s *rdState) devicePeer() error {
	if err := s.peer.devices.Approve(s.pendingDevice.UserCode, "acct-rd"); err != nil {
		return fmt.Errorf("the peer cannot approve a login started on this instance: %v", err)
	}
	res, err := s.peer.devices.Poll(s.pendingDevice.DeviceCode, rdDevicePub)
	if err != nil || res.Status != deviceauth.StatusApproved {
		return fmt.Errorf("the peer's poll = %+v, %v, want approved", res, err)
	}
	return nil
}

func (s *rdState) emailRequested() error {
	code, err := s.b.emails.Request("rd@example.com", "198.51.100.7")
	if err != nil {
		return err
	}
	s.emailCode = code
	return nil
}

func (s *rdState) emailPeer() error {
	if _, err := s.peer.emails.Submit("rd@example.com", s.emailCode, "198.51.100.7"); err != nil {
		return fmt.Errorf("the code sent from this instance does not verify on the peer: %v", err)
	}
	return nil
}

func (s *rdState) peerCools() error {
	s.stationOnM()
	s.peer.coolStation(s.st("s1").id, "m", 60)
	s.b.syncLivenessOnce()
	return nil
}

func (s *rdState) notPicked() error {
	if !s.isCooling("s1") {
		return fmt.Errorf("this instance does not see the cooldown its peer set on s1")
	}
	return nil
}

func (s *rdState) fundedGets503() error {
	if err := s.ensureFunded(); err != nil {
		return err
	}
	s.stationOnM()
	s.resp = s.relayMux("m")
	return s.is503Unavailable()
}

func (s *rdState) retriesSame() error {
	s.retryRes = s.relayMux("m")
	return nil
}

func (s *rdState) billedOnce() error {
	if s.retryRes.code != 200 {
		return fmt.Errorf("the retry = %d %s, want 200", s.retryRes.code, s.retryRes.body)
	}
	_, _, sp, _, _, _ := s.ledgerRows()
	if sp != 1 {
		return fmt.Errorf("%d spend rows, want exactly one", sp)
	}
	return nil
}

func (s *rdState) readyThenRestartedDown() error {
	if err := s.startsAnswering(); err != nil {
		return err
	}
	if err := s.boots(); err != nil {
		return err
	}
	if err := s.becomesReady(); err != nil {
		return err
	}
	s.live.Close()
	s.live = nil
	s.restart()
	return s.boots()
}

func (s *rdState) ready503Connecting() error {
	if err := s.readyIs(503); err != nil {
		return err
	}
	return s.bodySharedIs("connecting")
}

func (s *rdState) signed503() error {
	if err := s.fundedGets503(); err != nil {
		return err
	}
	return nil
}

func (s *rdState) answering() error { return s.startsAnswering() }

// stopsAnswering stops the shared store; the broker's next shared operation (here a probe
// read, in production any request) observes the outage, which is what readiness reports.
func (s *rdState) stopsAnswering() error {
	if s.live != nil {
		s.live.Close()
		s.live = nil
	}
	_, _, _ = s.b.shared.counterGet("rd:probe")
	return nil
}

func (s *rdState) degraded() error {
	if err := s.ready200True(); err != nil {
		return err
	}
	return s.bodySharedIs("degraded")
}

func (s *rdState) stopsAndAnswersAgain() error {
	if s.live != nil {
		s.live.Close()
		s.live = nil
	}
	return s.startsAnswering()
}

func (s *rdState) wiringIntact() error {
	w := s.wiring
	if s.b.shared != w.shared || s.b.devices != w.devices || s.b.emails != w.emails || s.b.rl.shared != w.shared {
		return fmt.Errorf("the shared wiring done at readiness was replaced or undone by an outage")
	}
	if newValkeyDeviceStore(s.b.shared) == nil || newValkeyEmailStore(s.b.shared) == nil {
		return fmt.Errorf("the shared store is no longer the device/email backend")
	}
	return nil
}

func (s *rdState) durableStops() error {
	if s.pg != nil {
		return s.pg.Close()
	}
	s.b.db = failingStore{Store: s.db}
	return nil
}

func (s *rdState) dbDown() error {
	if err := s.readyIs(503); err != nil {
		return err
	}
	if s.readyBody()["db"] != "down" {
		return fmt.Errorf("readiness body %s, want db \"down\"", s.resp.body)
	}
	return nil
}

func TestSharedStoreReadinessBDD(t *testing.T) {
	fo := &foState{t: t, logs: &utLog{}}
	st := &rdState{psState: newPSState(fo)}
	prev := log.Writer()
	log.SetOutput(fo.logs)
	t.Cleanup(func() { log.SetOutput(prev); fo.teardown() })
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) { return ctx, st.rdReset() })
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				st.rdTeardown()
				fo.teardown()
				return ctx, nil
			})
			sc.Step(`^a durable store that is reachable$`, st.durableOK)
			sc.Step(`^a shared-store address that is not answering yet$`, st.notAnswering)
			sc.Step(`^the broker is configured with (ROGERAI_[A-Z_]+(?:=1 and ROGERAI_REDIS_URL)? pointing at the address)$`, st.configured)
			sc.Step(`^the broker is configured with ROGERAI_MULTI_INSTANCE=1 and no shared-store address$`, st.multiNoAddress)
			sc.Step(`^the broker is configured with no shared-store setting at all$`, st.noSetting)
			sc.Step(`^the broker is configured with ROGERAI_REDIS_URL "([^"]+)" that is not answering$`, st.secretURL)
			sc.Step(`^the broker boots$`, st.boots)
			sc.Step(`^GET /ready answers (\d+)$`, st.readyIs)
			sc.Step(`^the readiness body says ready false and shared "connecting"$`, st.bodyNotReadyConnecting)
			sc.Step(`^the readiness body says shared "([^"]+)"$`, st.bodySharedIs)
			sc.Step(`^GET /ready answers 200 with ready true$`, st.ready200True)
			sc.Step(`^the readiness body has no "shared" key$`, st.noSharedKey)
			sc.Step(`^a signed chat request is served as it is today$`, st.signedServed)
			sc.Step(`^GET /health answers 200 "ok"$`, st.healthOK)
			sc.Step(`^the broker boots and the address stays down for (\d+) (minutes|hour)$`, st.bootsAndStaysDown)
			sc.Step(`^the broker has attempted the connection more than (\d+) times$`, st.attemptsMoreThan)
			sc.Step(`^no two consecutive attempts were more than 30 seconds apart$`, st.gapsAtMost30)
			sc.Step(`^it logged the outage once, not once per attempt$`, st.loggedOnce)
			sc.Step(`^the broker is still retrying$`, st.stillRetrying)
			sc.Step(`^GET /ready still answers 503$`, st.still503)
			sc.Step(`^a client calls (.+)$`, st.clientCalls)
			sc.Step(`^the response is 503 with error code "shared_store_unavailable"$`, st.is503Unavailable)
			sc.Step(`^it carries a Retry-After of at most 30 seconds$`, st.retryAfterAtMost30)
			sc.Step(`^it carries X-RogerAI-Cost "0"$`, st.cost0)
			sc.Step(`^no hold, ledger row, dispatch, login record or request record was written$`, st.noWork)
			sc.Step(`^an anonymous client posts a chat completion for a free station$`, st.anonFree)
			sc.Step(`^a client posts a signed chat completion$`, st.signedPost)
			sc.Step(`^the response body does not contain "([^"]+)"$`, st.bodyLacks)
			sc.Step(`^the shared store starts answering at the address$`, st.startsAnswering)
			sc.Step(`^within (\d+) seconds GET /ready answers 200 with ready true and shared "ok"$`, st.readyWithin)
			sc.Step(`^the broker was not restarted$`, st.notRestarted)
			sc.Step(`^a station is on air for "m"$`, st.stationOnM)
			sc.Step(`^the broker becomes ready$`, st.becomesReady)
			sc.Step(`^a funded consumer relays to "m"$`, st.fundedRelaysM)
			sc.Step(`^the response is 200$`, st.is200)
			sc.Step(`^the consumer was billed through a hold and a settle$`, st.holdAndSettle)
			sc.Step(`^a second instance runs over the same shared store$`, st.peerInstance)
			sc.Step(`^a signed identity's requests on both instances draw from one rate-limit bucket$`, st.sharedBucket)
			sc.Step(`^a device login is started on this instance$`, st.deviceStarted)
			sc.Step(`^the login can be approved and polled on the second instance$`, st.devicePeer)
			sc.Step(`^an email login code is requested on this instance$`, st.emailRequested)
			sc.Step(`^the code verifies on the second instance$`, st.emailPeer)
			sc.Step(`^the second instance cools station "s1"$`, st.peerCools)
			sc.Step(`^this instance does not pick "s1" while it cools$`, st.notPicked)
			sc.Step(`^a funded consumer posts a chat completion and gets 503 "shared_store_unavailable"$`, st.fundedGets503)
			sc.Step(`^the consumer retries the same request$`, st.retriesSame)
			sc.Step(`^the retry is served and billed exactly once$`, st.billedOnce)
			sc.Step(`^the broker was ready and then restarted while the shared store was down$`, st.readyThenRestartedDown)
			sc.Step(`^GET /ready answers 503 with shared "connecting"$`, st.ready503Connecting)
			sc.Step(`^a signed chat request answers 503 "shared_store_unavailable"$`, st.signed503)
			sc.Step(`^the shared store is answering$`, st.answering)
			sc.Step(`^the shared store stops answering$`, st.stopsAnswering)
			sc.Step(`^GET /ready answers 200 with ready true and shared "degraded"$`, st.degraded)
			sc.Step(`^the shared store stops answering and then answers again$`, st.stopsAndAnswersAgain)
			sc.Step(`^the device flow, the email flow and the rate limiters still use the shared store$`, st.wiringIntact)
			sc.Step(`^the durable store stops answering$`, st.durableStops)
			sc.Step(`^GET /ready answers 503 with db "down"$`, st.dbDown)
		},
		Options: &godog.Options{Format: "pretty", Paths: []string{"../../features/ops/shared_store_readiness.feature"}, TestingT: t, Strict: true},
	}
	if suite.Run() != 0 {
		t.Fatal("shared_store_readiness.feature failed")
	}
}
