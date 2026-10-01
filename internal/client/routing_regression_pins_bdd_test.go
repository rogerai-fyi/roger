package client

// routing_regression_pins_bdd_test.go makes the @proxy scenarios of
// features/routing/regression_pins.feature EXECUTABLE (defect 8 of the 2026-09-29 routing
// audit: the client proxy's failover PINS the alternative via X-Roger-Node, which disables the
// broker's own 3-station 429 plan, and it never waits out a short band-cooling 503).
//
// It drives the REAL ProxyHandlerLive / NewProxyOptionsHolder / relayWithFailover over the
// wire, against a SCRIPTED stand-in broker stood up with httptest - the approved seam for
// proxy specs (proxy_bdd_test.go): the broker is a separate binary, and the proxy's contract
// is the HTTP it emits and how it reacts. The stand-in records EVERY attempt (headers, body,
// arrival time) and answers per attempt number. Nothing about the proxy is mocked.
//
// Background steps ("a broker with an empty in-memory node registry", the fee rate, the $10
// default out-cap, the $100 register ceiling) are BROKER-side givens; here they configure the
// stand-in's /discover feed and the expectations the relay must carry (the out-cap header),
// and are documented as such below.
//
// "on every attempt only TEE-attested nodes were candidates at the broker's routing pass" is
// a BROKER-side truth pinned by the broker runner (cmd/rogerai-broker); at this seam it is
// asserted as "every attempt carried the confidential constraint" (the only thing the proxy
// can guarantee), and the step says so in its comment.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cucumber/godog"
)

// rpAttempt is one recorded relay attempt as the stand-in broker saw it.
type rpAttempt struct {
	at      time.Time
	headers http.Header
	body    map[string]any
	raw     []byte
}

// rpAnswer scripts the stand-in's reply for one attempt (1-based).
type rpAnswer struct {
	status     int
	provider   string
	retryAfter string
	errCode    string // when set, the body is {"error":{"code":errCode,...}}
}

type rpState struct {
	t *testing.T

	srv     *httptest.Server
	offers  []map[string]any
	answers map[int]rpAnswer // attempt number -> scripted reply; missing = 200 from the first offer
	deflt   rpAnswer         // reply when no script matches (zero = 200)

	mu       sync.Mutex
	attempts []rpAttempt

	band       ProxyOptions
	sessionKey string
	holder     *ProxyOptionsHolder
	handler    http.Handler

	rec        *httptest.ResponseRecorder
	started    time.Time
	elapsed    time.Duration
	cancelIn   time.Duration
	logBuf     bytes.Buffer
	prevLog    io.Writer
	freqSecret string

	// broker-side givens carried as expectations at this seam
	expectMaxOut string
}

func (s *rpState) reset() {
	if s.srv != nil {
		s.srv.Close()
	}
	*s = rpState{t: s.t, answers: map[int]rpAnswer{}, prevLog: log.Writer()}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	s.band = ProxyOptions{Broker: s.srv.URL, User: "u", Model: "qwen3-32b"}
	s.sessionKey = NewSessionKey()
	s.band.SessionKey = s.sessionKey
	log.SetOutput(&s.logBuf)
}

func (s *rpState) cleanup() {
	if s.srv != nil {
		s.srv.Close()
		s.srv = nil
	}
	if s.prevLog != nil {
		log.SetOutput(s.prevLog)
	}
}

func (s *rpState) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/discover" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"offers": s.offers})
		return
	}
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	s.mu.Lock()
	s.attempts = append(s.attempts, rpAttempt{at: time.Now(), headers: r.Header.Clone(), body: body, raw: raw})
	n := len(s.attempts)
	ans, ok := s.answers[n]
	if !ok {
		ans = s.deflt
	}
	s.mu.Unlock()
	if ans.status == 0 {
		ans.status = http.StatusOK
	}
	if ans.provider == "" && len(s.offers) > 0 {
		ans.provider, _ = s.offers[0]["node_id"].(string)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-RogerAI-Provider", ans.provider)
	if ans.retryAfter != "" {
		w.Header().Set("Retry-After", ans.retryAfter)
	}
	if ans.status == http.StatusOK {
		w.Header().Set("X-RogerAI-Cost", "0.0001")
	}
	w.WriteHeader(ans.status)
	switch {
	case ans.errCode != "":
		fmt.Fprintf(w, `{"error":{"code":%q,"message":"band cooling - retry after %s s"}}`, ans.errCode, ans.retryAfter)
	case ans.status == http.StatusOK:
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"hi"}}]}`))
	default:
		fmt.Fprintf(w, `{"error":{"message":"upstream failed (%d)"}}`, ans.status)
	}
}

func (s *rpState) bind() {
	s.holder = NewProxyOptionsHolder(s.band)
	s.handler = ProxyHandlerLive(s.holder)
}

func (s *rpState) addOffer(node string, confidential bool, priceOut float64) {
	s.offers = append(s.offers, map[string]any{
		"node_id": node, "model": "qwen3-32b", "online": true, "price_in": 0.1, "price_out": priceOut,
		"tps": 30.0, "signal": 80, "confidential": confidential,
	})
}

func (s *rpState) attempt(n int) (rpAttempt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.attempts) < n {
		return rpAttempt{}, fmt.Errorf("only %d attempt(s) reached the broker, want at least %d", len(s.attempts), n)
	}
	return s.attempts[n-1], nil
}

func (s *rpState) nAttempts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.attempts)
}

// providerObj returns the body's "provider" object of an attempt (nil when absent).
func providerObj(a rpAttempt) map[string]any {
	p, _ := a.body["provider"].(map[string]any)
	return p
}

func rogerObj(a rpAttempt) map[string]any {
	p, _ := a.body["roger"].(map[string]any)
	return p
}

func stringList(v any) []string {
	arr, _ := v.([]any)
	out := make([]string, 0, len(arr))
	for _, x := range arr {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---- Background (broker-side givens, carried as stand-in configuration) ----

func (s *rpState) emptyRegistry() error {
	s.reset() // an empty /discover feed until nodes are put on air
	return nil
}

func (s *rpState) feeRate(pct int) error {
	// The fee is priced by the broker at settle; the stand-in bills a nominal cost and the
	// proxy never computes a fee. Recorded for the scenario's reading only.
	return nil
}

func (s *rpState) defaultOutCap(dollars float64) error {
	// The default out-cap is applied server-side by the real broker; the proxy's side of the
	// contract is to CARRY it on every attempt (checked in relayCarriesOutCap-style steps of
	// proxy_bdd_test.go). Kept as the expectation the attempts must satisfy.
	s.expectMaxOut = fmt.Sprintf("%g", dollars)
	return nil
}

func (s *rpState) registerCeiling(dollars float64) error {
	// A register-time ceiling is enforced by the broker; the stand-in's offers never exceed it.
	for _, o := range s.offers {
		if po, _ := o["price_out"].(float64); po > dollars {
			return fmt.Errorf("stand-in offer %v priced above the ceiling", o["node_id"])
		}
	}
	return nil
}

// ---- Givens ----

func (s *rpState) nodesOnAir2(a, b string) error {
	s.addOffer(a, false, 0.5)
	s.addOffer(b, false, 0.5)
	return nil
}

func (s *rpState) nodesOnAir3(a, b, c string) error {
	s.addOffer(a, false, 0.5)
	s.addOffer(b, false, 0.5)
	s.addOffer(c, false, 0.5)
	return nil
}

func (s *rpState) onlyStation(node, model string) error {
	if model != "qwen3-32b" {
		s.band.Model = model
	}
	s.offers = nil
	s.addOffer(node, false, 0.5)
	return nil
}

func (s *rpState) firstAttempt502ServedBy(node string) error {
	s.answers[1] = rpAnswer{status: http.StatusBadGateway, provider: node}
	return nil
}

func (s *rpState) firstAttempt502() error {
	prov := ""
	if len(s.offers) > 0 {
		prov, _ = s.offers[0]["node_id"].(string)
	}
	s.answers[1] = rpAnswer{status: http.StatusBadGateway, provider: prov}
	return nil
}

func (s *rpState) firstTwo502(a, b string) error {
	s.answers[1] = rpAnswer{status: http.StatusBadGateway, provider: a}
	s.answers[2] = rpAnswer{status: http.StatusBadGateway, provider: b}
	return nil
}

// "n-2" answers its next request with an upstream 429: at the REAL broker the 429 is
// absorbed by its 3-station plan and the consumer sees 200 from n-3. The stand-in plays that
// broker-side outcome for the proxy's SECOND attempt (the one that names n-2 as preferred).
func (s *rpState) nodeAnswers429Next(node string) error {
	s.answers[2] = rpAnswer{status: http.StatusOK, provider: "n-3"}
	return nil
}

func (s *rpState) coolingFor(node string, secs int) error {
	s.answers[1] = rpAnswer{status: http.StatusServiceUnavailable, provider: node, retryAfter: fmt.Sprint(secs), errCode: "band_cooling"}
	s.answers[2] = rpAnswer{status: http.StatusOK, provider: node}
	return nil
}

func (s *rpState) coolingTwice(node string, a, b int) error {
	s.answers[1] = rpAnswer{status: http.StatusServiceUnavailable, provider: node, retryAfter: fmt.Sprint(a), errCode: "band_cooling"}
	s.answers[2] = rpAnswer{status: http.StatusServiceUnavailable, provider: node, retryAfter: fmt.Sprint(b), errCode: "band_cooling"}
	s.answers[3] = rpAnswer{status: http.StatusOK, provider: node}
	return nil
}

func (s *rpState) callerCancelsAfter(secs int) error {
	s.cancelIn = time.Duration(secs) * time.Second
	return nil
}

func (s *rpState) broker429Limiter(ra string) error {
	s.deflt = rpAnswer{status: http.StatusTooManyRequests, retryAfter: ra}
	return nil
}

func (s *rpState) upstream429(node, ra string) error {
	s.deflt = rpAnswer{status: http.StatusTooManyRequests, provider: node, retryAfter: ra}
	return nil
}

func (s *rpState) every502() error {
	// Four distinct stations so /discover keeps offering an alternative and the proxy's
	// own 4-attempt budget is what ends the loop (not "nothing else fits").
	s.offers = nil
	for _, n := range []string{"n-1", "n-2", "n-3", "n-4"} {
		s.addOffer(n, false, 0.5)
	}
	for i := 1; i <= 4; i++ {
		s.answers[i] = rpAnswer{status: http.StatusBadGateway, provider: fmt.Sprintf("n-%d", i)}
	}
	s.deflt = rpAnswer{status: http.StatusBadGateway, provider: "n-x"}
	return nil
}

func (s *rpState) sessionConfidential() error {
	s.band.Confidential = true
	// A cheaper NON-attested alternative must exist so the pin/order choice is meaningful.
	s.offers = nil
	s.addOffer("n-1", true, 0.5)
	s.addOffer("n-plain", false, 0.1)
	s.addOffer("n-2", true, 0.6)
	return nil
}

func (s *rpState) sessionPrivateBand() error {
	s.freqSecret = "FREQ-SECRET-7Q2"
	s.band.Freq = s.freqSecret
	s.offers = nil // a private station is never on /discover
	return nil
}

// ---- When ----

func (s *rpState) chatThroughProxy() error {
	s.bind()
	body := `{"model":"anything","messages":[{"role":"user","content":"hi"}]}`
	ctx := context.Background()
	var cancel context.CancelFunc
	if s.cancelIn > 0 {
		ctx, cancel = context.WithTimeout(ctx, s.cancelIn)
		defer cancel()
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+s.sessionKey)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.started = time.Now()
	s.handler.ServeHTTP(rec, req)
	s.elapsed = time.Since(s.started)
	s.rec = rec
	return nil
}

// ---- Then: attempt shape ----

func (s *rpState) secondAttemptOrder(node string) error {
	a, err := s.attempt(2)
	if err != nil {
		return err
	}
	got := stringList(providerObj(a)["order"])
	if !sameStrings(got, []string{node}) {
		return fmt.Errorf("second attempt body provider.order = %v, want [%s]; X-Roger-Node=%q body=%s", got, node, a.headers.Get("X-Roger-Node"), a.raw)
	}
	return nil
}

func (s *rpState) secondAttemptNoNodeHeader() error {
	a, err := s.attempt(2)
	if err != nil {
		return err
	}
	if v := a.headers.Get("X-Roger-Node"); v != "" {
		return fmt.Errorf("second attempt carries X-Roger-Node %q (a pin disables the broker's own failover)", v)
	}
	return nil
}

func (s *rpState) secondAttemptNoAllowFallbacksFalse() error {
	a, err := s.attempt(2)
	if err != nil {
		return err
	}
	if v, ok := providerObj(a)["allow_fallbacks"].(bool); ok && !v {
		return fmt.Errorf("second attempt carries provider.allow_fallbacks false")
	}
	return nil
}

func (s *rpState) secondAttemptServedBy(node string, status int) error {
	a, err := s.attempt(2)
	if err != nil {
		return err
	}
	_ = a
	if s.rec.Code != status {
		return fmt.Errorf("status = %d, want %d; body=%s", s.rec.Code, status, s.rec.Body.String())
	}
	if got := s.rec.Header().Get("X-RogerAI-Provider"); got != node {
		return fmt.Errorf("served by %q, want %q", got, node)
	}
	return nil
}

func (s *rpState) consumerNever429() error {
	if s.rec.Code == http.StatusTooManyRequests {
		return fmt.Errorf("the consumer saw a 429")
	}
	if s.rec.Header().Get("Retry-After") != "" {
		return fmt.Errorf("the consumer saw a Retry-After on a served response")
	}
	return nil
}

func (s *rpState) thirdAttemptIgnore(a, b string) error {
	at, err := s.attempt(3)
	if err != nil {
		return err
	}
	got := stringList(providerObj(at)["ignore"])
	if !sameStrings(got, []string{a, b}) {
		return fmt.Errorf("third attempt body provider.ignore = %v, want [%s %s]; X-Roger-Exclude-Nodes=%q", got, a, b, at.headers.Get("X-Roger-Exclude-Nodes"))
	}
	// The header form must agree with the body form (the spec's title).
	if hdr := at.headers.Get("X-Roger-Exclude-Nodes"); hdr != "" {
		for _, n := range []string{a, b} {
			if !strings.Contains(hdr, n) {
				return fmt.Errorf("X-Roger-Exclude-Nodes %q disagrees with provider.ignore %v", hdr, got)
			}
		}
	}
	return nil
}

func (s *rpState) orderNamesNeither(a, b string) error {
	at, err := s.attempt(3)
	if err != nil {
		return err
	}
	for _, n := range stringList(providerObj(at)["order"]) {
		if n == a || n == b {
			return fmt.Errorf("provider.order names a failed station %q", n)
		}
	}
	return nil
}

// ---- Then: band cooling ----

func (s *rpState) proxyReceives503(code, ra string) error {
	a, err := s.attempt(1)
	if err != nil {
		return err
	}
	_ = a
	ans := s.answers[1]
	if ans.status != http.StatusServiceUnavailable || ans.errCode != code || ans.retryAfter != ra {
		return fmt.Errorf("stand-in did not answer 503 %s Retry-After %s on attempt 1", code, ra)
	}
	return nil
}

func (s *rpState) waitsAndRetriesOnceNoOrder(secs int) error {
	if err := s.waitsAndRetriesOnce(secs); err != nil {
		return err
	}
	a, _ := s.attempt(2)
	if o := stringList(providerObj(a)["order"]); len(o) > 0 {
		return fmt.Errorf("the retry re-pinned via provider.order %v", o)
	}
	if v := a.headers.Get("X-Roger-Node"); v != "" {
		return fmt.Errorf("the retry re-pinned via X-Roger-Node %q", v)
	}
	return nil
}

func (s *rpState) waitsAndRetriesOnce(secs int) error {
	if n := s.nAttempts(); n != 2 {
		return fmt.Errorf("attempts = %d, want exactly 2 (one wait, one retry); status=%d body=%s", n, s.rec.Code, s.rec.Body.String())
	}
	a1, _ := s.attempt(1)
	a2, _ := s.attempt(2)
	gap := a2.at.Sub(a1.at)
	want := time.Duration(secs) * time.Second
	if gap < want {
		return fmt.Errorf("the retry came after %v, want a wait of at least %v (the band-cooling Retry-After)", gap.Round(time.Millisecond), want)
	}
	if gap > want+1500*time.Millisecond {
		return fmt.Errorf("the retry came after %v, far past the %v Retry-After", gap.Round(time.Millisecond), want)
	}
	return nil
}

func (s *rpState) retryServed200() error {
	if s.rec.Code != http.StatusOK {
		return fmt.Errorf("status = %d, want 200; body=%s", s.rec.Code, s.rec.Body.String())
	}
	return nil
}

func (s *rpState) doesNotSleep() error {
	if s.elapsed > time.Second {
		return fmt.Errorf("the proxy took %v (it slept)", s.elapsed.Round(time.Millisecond))
	}
	return nil
}

func (s *rpState) clientSees503RetryAfter(ra string) error {
	if s.rec.Code != http.StatusServiceUnavailable {
		return fmt.Errorf("status = %d, want 503 passed through; body=%s", s.rec.Code, s.rec.Body.String())
	}
	if got := s.rec.Header().Get("Retry-After"); got != ra {
		return fmt.Errorf("Retry-After = %q, want %q", got, ra)
	}
	return nil
}

func (s *rpState) waitsOnce() error {
	n := s.nAttempts()
	if n != 2 {
		return fmt.Errorf("attempts = %d, want exactly 2 (the wait happens at most once)", n)
	}
	a1, _ := s.attempt(1)
	a2, _ := s.attempt(2)
	if gap := a2.at.Sub(a1.at); gap < 2*time.Second {
		return fmt.Errorf("the retry came after %v, want the first Retry-After (2 s) waited out", gap.Round(time.Millisecond))
	}
	return nil
}

func (s *rpState) clientSeesSecond503() error {
	if s.rec.Code != http.StatusServiceUnavailable {
		return fmt.Errorf("status = %d, want the second 503 passed through; body=%s", s.rec.Code, s.rec.Body.String())
	}
	if got := s.rec.Header().Get("Retry-After"); got != s.answers[2].retryAfter {
		return fmt.Errorf("Retry-After = %q, want the second 503's %q", got, s.answers[2].retryAfter)
	}
	return nil
}

func (s *rpState) stopsWaitingOnCancel() error {
	// The caller cancelled at 1 s of a 4 s wait: the proxy must have returned promptly AND
	// must not have synthesized a 502 "upstream_unavailable" without waiting at all (which is
	// what a proxy that never waits does today: 503 -> retryable -> no alternative -> 502).
	if s.elapsed > 2*time.Second {
		return fmt.Errorf("the proxy kept waiting for %v after the caller cancelled at 1 s", s.elapsed.Round(time.Millisecond))
	}
	if s.elapsed < 900*time.Millisecond {
		return fmt.Errorf("the proxy returned after %v - it never waited on the band-cooling Retry-After; body=%s", s.elapsed.Round(time.Millisecond), s.rec.Body.String())
	}
	return nil
}

func (s *rpState) noRetrySent() error {
	if n := s.nAttempts(); n != 1 {
		return fmt.Errorf("attempts = %d, want 1 (no retry)", n)
	}
	return nil
}

// ---- Then: unchanged pass-throughs ----

func (s *rpState) statusIs(code int) error {
	if s.rec.Code != code {
		return fmt.Errorf("status = %d, want %d; body=%s", s.rec.Code, code, s.rec.Body.String())
	}
	return nil
}

func (s *rpState) clientSeesRetryAfter(ra string) error {
	if got := s.rec.Header().Get("Retry-After"); got != ra {
		return fmt.Errorf("Retry-After = %q, want %q", got, ra)
	}
	return nil
}

func (s *rpState) proxyDoesNotRetry() error {
	return s.noRetrySent()
}

func (s *rpState) exactlyNAttempts(n int) error {
	if got := s.nAttempts(); got != n {
		return fmt.Errorf("attempts = %d, want %d", got, n)
	}
	return nil
}

func (s *rpState) backoffs(a, b, c int) error {
	want := []time.Duration{time.Duration(a) * time.Millisecond, time.Duration(b) * time.Millisecond, time.Duration(c) * time.Millisecond}
	for i, w := range want {
		p, err := s.attempt(i + 1)
		if err != nil {
			return err
		}
		n, err := s.attempt(i + 2)
		if err != nil {
			return err
		}
		gap := n.at.Sub(p.at)
		if gap < w || gap > w+400*time.Millisecond {
			return fmt.Errorf("backoff before attempt %d = %v, want ~%v", i+2, gap.Round(time.Millisecond), w)
		}
	}
	return nil
}

func (s *rpState) openAI502(code string) error {
	if s.rec.Code != http.StatusBadGateway {
		return fmt.Errorf("status = %d, want 502; body=%s", s.rec.Code, s.rec.Body.String())
	}
	var e struct {
		Error struct {
			Type string `json:"type"`
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(s.rec.Body.Bytes(), &e); err != nil {
		return fmt.Errorf("502 body is not OpenAI-shaped JSON: %v (%s)", err, s.rec.Body.String())
	}
	if e.Error.Code != code {
		return fmt.Errorf("error.code = %q, want %q", e.Error.Code, code)
	}
	return nil
}

// ---- Then: confidential + private band ----

func (s *rpState) everyAttemptConfidentialBody() error {
	n := s.nAttempts()
	if n < 2 {
		return fmt.Errorf("attempts = %d, want the failover retry too", n)
	}
	for i := 1; i <= n; i++ {
		a, _ := s.attempt(i)
		if v, ok := rogerObj(a)["confidential"].(bool); !ok || !v {
			return fmt.Errorf("attempt %d body carries no roger.confidential true (header X-Roger-Confidential=%q); body=%s", i, a.headers.Get("X-Roger-Confidential"), a.raw)
		}
	}
	return nil
}

// The broker-side truth ("only TEE-attested nodes were candidates") is pinned by the broker
// runner; at this seam the proxy can only guarantee that EVERY attempt carried the
// confidential constraint (body or header), which is what is asserted here.
func (s *rpState) onlyAttestedCandidates() error {
	n := s.nAttempts()
	for i := 1; i <= n; i++ {
		a, _ := s.attempt(i)
		v, _ := rogerObj(a)["confidential"].(bool)
		if !v && a.headers.Get("X-Roger-Confidential") == "" {
			return fmt.Errorf("attempt %d carried no confidential constraint at all", i)
		}
	}
	return nil
}

func (s *rpState) orderAlternativeIsConfidential() error {
	a, err := s.attempt(2)
	if err != nil {
		return err
	}
	o := stringList(providerObj(a)["order"])
	if len(o) == 0 {
		return fmt.Errorf("the retry names no alternative in provider.order (X-Roger-Node=%q)", a.headers.Get("X-Roger-Node"))
	}
	for _, off := range s.offers {
		if off["node_id"] == o[0] {
			if c, _ := off["confidential"].(bool); !c {
				return fmt.Errorf("provider.order names %q, which is not TEE-attested", o[0])
			}
			return nil
		}
	}
	return fmt.Errorf("provider.order names unknown node %q", o[0])
}

func (s *rpState) retryCarriesBandCodeNoOrder() error {
	a, err := s.attempt(2)
	if err != nil {
		return fmt.Errorf("%v (the band's own station is the only place to retry; the broker re-picks within the band)", err)
	}
	f, _ := rogerObj(a)["freq"].(string)
	if f != s.freqSecret && a.headers.Get("X-Roger-Freq") != s.freqSecret {
		return fmt.Errorf("the retry does not carry the band code")
	}
	if o := stringList(providerObj(a)["order"]); len(o) > 0 {
		return fmt.Errorf("the retry names a public alternative %v", o)
	}
	return nil
}

func (s *rpState) bandCodeNeverLogged() error {
	if strings.Contains(s.logBuf.String(), s.freqSecret) {
		return fmt.Errorf("the band code appears in a log line")
	}
	return nil
}

// ===================== runner =====================

func TestRoutingRegressionPinsProxy(t *testing.T) {
	st := &rpState{t: t, answers: map[int]rpAnswer{}}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				st.cleanup()
				return ctx, nil
			})

			// Background (broker-side givens at this seam; see the file header)
			sc.Step(`^a broker with an empty in-memory node registry$`, st.emptyRegistry)
			sc.Step(`^the fee rate is (\d+)%$`, st.feeRate)
			sc.Step(`^the consumer default out-cap is \$([0-9.]+)/1M$`, st.defaultOutCap)
			sc.Step(`^the register out-price ceiling is \$([0-9.]+)/1M$`, st.registerCeiling)

			// Givens
			sc.Step(`^nodes "([^"]*)" and "([^"]*)" are on air for "qwen3-32b"$`, st.nodesOnAir2)
			sc.Step(`^nodes "([^"]*)", "([^"]*)" and "([^"]*)" are on air for "qwen3-32b"$`, st.nodesOnAir3)
			sc.Step(`^node "([^"]*)" is the only station on air for "([^"]*)"$`, st.onlyStation)
			sc.Step(`^the broker answers the first proxy attempt with a 502 served by "([^"]*)"$`, st.firstAttempt502ServedBy)
			sc.Step(`^the broker answers the first proxy attempt with a 502$`, st.firstAttempt502)
			sc.Step(`^the broker answers the first two proxy attempts with 502s served by "([^"]*)" then "([^"]*)"$`, st.firstTwo502)
			sc.Step(`^"([^"]*)" answers its next request with an upstream 429$`, st.nodeAnswers429Next)
			sc.Step(`^"([^"]*)" is cooling for (\d+) more seconds, then cools again for (\d+) more on the retry$`, st.coolingTwice)
			sc.Step(`^"([^"]*)" is cooling for (\d+) more seconds$`, st.coolingFor)
			sc.Step(`^the caller cancels its request after (\d+) second$`, st.callerCancelsAfter)
			sc.Step(`^the broker returns 429 with Retry-After "([^"]*)" from its per-identity limiter$`, st.broker429Limiter)
			sc.Step(`^"([^"]*)" answers with an upstream 429 and Retry-After "([^"]*)"$`, st.upstream429)
			sc.Step(`^every broker attempt answers 502$`, st.every502)
			sc.Step(`^the proxy session is confidential$`, st.sessionConfidential)
			sc.Step(`^the proxy session is tuned to a private band by its code$`, st.sessionPrivateBand)

			// When
			sc.Step(`^a chat request goes through the local proxy$`, st.chatThroughProxy)

			// Then
			sc.Step(`^the proxy's second attempt carries body provider\.order \["([^"]*)"\]$`, st.secondAttemptOrder)
			sc.Step(`^the proxy's second attempt carries no X-Roger-Node header$`, st.secondAttemptNoNodeHeader)
			sc.Step(`^the proxy's second attempt carries no provider\.allow_fallbacks false$`, st.secondAttemptNoAllowFallbacksFalse)
			sc.Step(`^the proxy's second attempt is served by "([^"]*)" with status (\d+)$`, st.secondAttemptServedBy)
			sc.Step(`^the consumer never sees the 429$`, st.consumerNever429)
			sc.Step(`^the proxy's third attempt carries body provider\.ignore \["([^"]*)", "([^"]*)"\]$`, st.thirdAttemptIgnore)
			sc.Step(`^its provider\.order names neither "([^"]*)" nor "([^"]*)"$`, st.orderNamesNeither)
			sc.Step(`^the proxy receives 503 with error code "([^"]*)" and Retry-After "([^"]*)"$`, st.proxyReceives503)
			sc.Step(`^the proxy waits (\d+) seconds and retries exactly once with no provider\.order$`, st.waitsAndRetriesOnceNoOrder)
			sc.Step(`^the proxy waits (\d+) seconds and retries exactly once$`, st.waitsAndRetriesOnce)
			sc.Step(`^the retry is served with status 200$`, st.retryServed200)
			sc.Step(`^the proxy does not sleep$`, st.doesNotSleep)
			sc.Step(`^the client sees 503 with Retry-After "([^"]*)"$`, st.clientSees503RetryAfter)
			sc.Step(`^the proxy waits once$`, st.waitsOnce)
			sc.Step(`^the client sees the second 503 with its Retry-After$`, st.clientSeesSecond503)
			sc.Step(`^the proxy stops waiting when the caller cancels$`, st.stopsWaitingOnCancel)
			sc.Step(`^no retry is sent$`, st.noRetrySent)
			sc.Step(`^the status is (\d+)$`, st.statusIs)
			sc.Step(`^the client sees Retry-After "([^"]*)"$`, st.clientSeesRetryAfter)
			sc.Step(`^the proxy does not retry$`, st.proxyDoesNotRetry)
			sc.Step(`^exactly (\d+) attempts are made$`, st.exactlyNAttempts)
			sc.Step(`^the backoffs before attempts 2, 3 and 4 are (\d+)ms, (\d+)ms and (\d+)ms$`, st.backoffs)
			sc.Step(`^the client sees an OpenAI-shaped 502 "([^"]*)"$`, st.openAI502)
			sc.Step(`^every attempt carries body roger\.confidential true$`, st.everyAttemptConfidentialBody)
			sc.Step(`^on every attempt only TEE-attested nodes were candidates at the broker's routing pass$`, st.onlyAttestedCandidates)
			sc.Step(`^the alternative named in provider\.order is a confidential node$`, st.orderAlternativeIsConfidential)
			sc.Step(`^the retry carries the band code and no provider\.order$`, st.retryCarriesBandCodeNoOrder)
			sc.Step(`^the band code never appears in a log line$`, st.bandCodeNeverLogged)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/routing/regression_pins.feature"},
			Tags:     "@proxy",
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("routing regression pin (proxy) scenarios failed (see godog output above)")
	}
}
