package client

// GUEST OPERATORS — Phase 1 proxy hardening: RED table tests.
//
// These are the executable RED evidence for the founder-approvable specs under
// features/proxy/*.feature. They drive the REAL ProxyHandler against a stub broker
// (httptest), exactly like the existing failover_test.go / client_test.go pattern —
// NO mocks of business logic. Each test asserts the DESIRED hardened behavior and is
// RED today because that behavior is absent (the proxy is a single-route,
// unauthenticated, silently-truncating relay with plain-text errors).
//
// SCOPE OF THIS FILE (per CLAUDE.md: write the failing test, NO production code): every
// test here compiles against the CURRENT exported surface (ProxyHandler + ProxyOptions +
// httptest) so the package still builds and each failure is a clean RUNTIME red for the
// right reason. Three behaviors need a new seam before they can be exercised and their
// executable RED lands WITH that seam (post-approval), covered for now by the feature spec:
//   - per-session spend budget      -> needs ProxyOptions.Budget           (budget.feature)
//   - stream timeout (no 120s cut)  -> needs an injectable dial/header bound (stream_timeout.feature)
//   - live ProxyOptions on re-tune  -> needs a live options source          (live_options.feature)
//
// The repo uses the stdlib testing package (no testify in go.mod), so these do too.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// sandbox isolates signRequest's key-on-first-use side effect (identity.go) inside the test.
func sandbox(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}

// isOpenAIError reports whether body is an OpenAI-shaped error envelope:
// {"error":{"message":...,"type":...}}. Agents JSON-decode this on every non-2xx.
func isOpenAIError(body []byte) (typ string, ok bool) {
	var d struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &d); err != nil {
		return "", false
	}
	if d.Error.Message == "" && d.Error.Type == "" {
		return "", false
	}
	return d.Error.Type, true
}

// --- #1 /v1/models -----------------------------------------------------------------------

// TestProxyModelsEndpoint: an agent probes GET /v1/models at startup; the proxy must answer
// in OpenAI list shape reflecting the tuned band. RED today: the single-route mux 404s the
// probe with Go's plain text, which crashes opencode/Crush/Continue before prompt one.
func TestProxyModelsEndpoint(t *testing.T) {
	sandbox(t)
	h := ProxyHandler(ProxyOptions{Broker: "http://127.0.0.1:0", User: "u"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/models status = %d, want 200 (agents probe this at startup); body=%q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var d struct {
		Object string `json:"object"`
		Data   []struct {
			ID     string `json:"id"`
			Object string `json:"object"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatalf("body is not JSON (%v): %q", err, rec.Body.String())
	}
	if d.Object != "list" {
		t.Errorf("object = %q, want \"list\"", d.Object)
	}
	if len(d.Data) != 1 {
		t.Fatalf("data has %d entries, want exactly 1 (the tuned band)", len(d.Data))
	}
	if d.Data[0].Object != "model" {
		t.Errorf("data[0].object = %q, want \"model\"", d.Data[0].Object)
	}
}

// --- #2 model-field rewrite --------------------------------------------------------------

// TestProxyRewritesModelToBand: the proxy must rewrite the request body's model to the tuned
// band's model, so an agent's default ("gpt-4o") just works. RED today: the client's arbitrary
// model reaches the broker verbatim (client.go:397-402 forwards body.model unchanged), so the
// broker looks for a nonexistent "gpt-4o" station and the whole "no provider for gpt-4o" class
// bites. (The rewrite TARGET is the band model — a new ProxyOptions.Model seam; here we pin the
// invariant that the client's arbitrary model must NOT reach the broker unrewritten.)
func TestProxyRewritesModelToBand(t *testing.T) {
	sandbox(t)
	var gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		gotModel = b.Model
		w.Header().Set("X-RogerAI-Provider", "node-1")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"hi"}}]}`))
	}))
	defer srv.Close()

	// SEAM LANDED: ProxyOptions.Model carries the tuned band's model; the proxy rewrites the
	// client's arbitrary model to it before relay.
	h := ProxyHandler(ProxyOptions{Broker: srv.URL, User: "u", Model: "qwen3-32b-fp8"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	h.ServeHTTP(rec, req)

	if gotModel == "gpt-4o" {
		t.Fatalf("broker received the client's arbitrary model %q unrewritten; it must be rewritten to the tuned band's model", gotModel)
	}
	if gotModel == "" {
		t.Fatalf("broker received an empty model; the proxy must rewrite to the band's model")
	}
	if gotModel != "qwen3-32b-fp8" {
		t.Fatalf("broker received model %q, want the tuned band's model qwen3-32b-fp8", gotModel)
	}
}

// TestProxyMalformedBody400: a malformed JSON body must be rejected with an OpenAI-shaped 400
// BEFORE any relay/hold, so a broken client never spends. RED today: the Unmarshal error is
// ignored (client.go:400) and the garbage body is relayed to the broker.
func TestProxyMalformedBody400(t *testing.T) {
	sandbox(t)
	var brokerCalled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			brokerCalled = true
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	h := ProxyHandler(ProxyOptions{Broker: srv.URL, User: "u"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{ this is not json`))
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed body status = %d, want 400 (reject before spend)", rec.Code)
	}
	if typ, ok := isOpenAIError(rec.Body.Bytes()); !ok || typ != "invalid_request_error" {
		t.Errorf("malformed body error = %q ok=%v, want OpenAI-shaped type invalid_request_error; body=%q", typ, ok, rec.Body.String())
	}
	if brokerCalled {
		t.Errorf("a malformed body must NOT reach the broker (no hold, no spend)")
	}
}

// --- #3 per-session bearer auth ----------------------------------------------------------

// TestProxyRequiresBearer: the proxy must enforce a per-session bearer key. RED today: the
// endpoint is unauthenticated (no Authorization check), so a request with NO key is relayed and
// spends. (The positive right-key path needs the ProxyOptions.SessionKey seam and lands with it.)
func TestProxyRequiresBearer(t *testing.T) {
	sandbox(t)
	var brokerCalled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			brokerCalled = true
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"hi"}}]}`))
	}))
	defer srv.Close()

	// SEAM LANDED: ProxyOptions.SessionKey carries the per-session bearer secret enforced on
	// every route with a constant-time compare.
	const key = "s3cr3t-session-key"
	h := ProxyHandler(ProxyOptions{Broker: srv.URL, User: "u", Model: "m", SessionKey: key})

	cases := []struct{ name, auth string }{
		{"no header", ""},
		{"wrong key", "Bearer wrong-key"},
		{"decorative roger-local", "Bearer roger-local"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			brokerCalled = false
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
			if c.auth != "" {
				req.Header.Set("Authorization", c.auth)
			}
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("%s: status = %d, want 401 (per-session bearer must be enforced)", c.name, rec.Code)
			}
			if typ, ok := isOpenAIError(rec.Body.Bytes()); !ok || typ != "authentication_error" {
				t.Errorf("%s: error = %q ok=%v, want type authentication_error", c.name, typ, ok)
			}
			if brokerCalled {
				t.Errorf("%s: an unauthorized request must never reach the broker (auth precedes spend)", c.name)
			}
		})
	}

	// Positive path: the CORRECT session key is admitted and relays (auth.feature scenario 1).
	t.Run("correct key admitted", func(t *testing.T) {
		brokerCalled = false
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
		req.Header.Set("Authorization", "Bearer "+key)
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("correct key: status = %d, want 200 (the right session key is admitted); body=%q", rec.Code, rec.Body.String())
		}
		if !brokerCalled {
			t.Errorf("correct key: the request must reach the broker")
		}
	})
}

// --- #5 OpenAI-shaped errors on every path -----------------------------------------------

// TestProxyUnknownRouteJSON: an unknown route must return an OpenAI-shaped JSON 404, not Go's
// plain-text "404 page not found" that crashes SDK JSON decoders. RED today: the single-route
// mux falls through to http.NotFound.
func TestProxyUnknownRouteJSON(t *testing.T) {
	sandbox(t)
	h := ProxyHandler(ProxyOptions{Broker: "http://127.0.0.1:0", User: "u"})

	for _, path := range []string{"/v1/embeddings", "/v1/responses", "/", "/healthz"} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, path, nil)
			h.ServeHTTP(rec, req)

			if strings.Contains(rec.Body.String(), "404 page not found") {
				t.Fatalf("%s returned Go's plain-text 404 (crashes SDK JSON decoders): %q", path, rec.Body.String())
			}
			if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
				t.Errorf("%s Content-Type = %q, want application/json", path, ct)
			}
			if _, ok := isOpenAIError(rec.Body.Bytes()); !ok {
				t.Errorf("%s body is not an OpenAI error envelope: %q", path, rec.Body.String())
			}
		})
	}
}

// TestProxyRelayFailureJSON: when the relay exhausts (all stations fail), the proxy must return
// an OpenAI-shaped JSON 502, not a bare text line. RED today: relayWithFailover ends in
// http.Error (client.go:500) which writes text/plain.
func TestProxyRelayFailureJSON(t *testing.T) {
	sandbox(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/discover" {
			w.Write([]byte(`{"offers":[]}`)) // no alternative -> failover exhausts immediately
			return
		}
		w.Header().Set("X-RogerAI-Provider", "only")
		w.WriteHeader(http.StatusServiceUnavailable) // retryable, but nothing to fail over to
	}))
	defer srv.Close()

	h := ProxyHandler(ProxyOptions{Broker: srv.URL, User: "u"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("relay-exhausted status = %d, want 502", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("relay-failure Content-Type = %q, want application/json (SDKs JSON-decode the body)", ct)
	}
	if typ, ok := isOpenAIError(rec.Body.Bytes()); !ok || typ != "api_error" {
		t.Errorf("relay-failure error = %q ok=%v, want OpenAI-shaped type api_error; body=%q", typ, ok, rec.Body.String())
	}
}

// --- #7 Retry-After passthrough ----------------------------------------------------------

// TestProxyForwardsRetryAfter: a broker 429 must forward its Retry-After header so agents can
// back off. RED today: copyRelayResponse's allowlist (client.go:541) omits Retry-After and
// drops it. The safe meter headers (X-RogerAI-*) and Content-Type still pass (asserted green).
func TestProxyForwardsRetryAfter(t *testing.T) {
	sandbox(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "30")
		w.Header().Set("X-RogerAI-Provider", "node-7")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"message":"rate limited","type":"rate_limit_error"}}`))
	}))
	defer srv.Close()

	h := ProxyHandler(ProxyOptions{Broker: srv.URL, User: "u"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 passthrough", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "30" {
		t.Errorf("Retry-After = %q, want \"30\" (agents need it to back off; it must survive copyRelayResponse)", got)
	}
	// Regression guard (green today): the safe headers still pass through.
	if got := rec.Header().Get("X-RogerAI-Provider"); got != "node-7" {
		t.Errorf("X-RogerAI-Provider = %q, want node-7 (safe meter header must still pass)", got)
	}
}

// TestProxyDropsUnsafeHeaders: hop-by-hop / connection-scoped / cookie headers must NOT leak
// to the local client. This pins the current tight allowlist as a permanent regression guard
// (GREEN today) so adding Retry-After to the allowlist doesn't accidentally widen it.
func TestProxyDropsUnsafeHeaders(t *testing.T) {
	sandbox(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Set-Cookie", "sid=abc")
		w.Header().Set("Server", "broker/1.2.3")
		w.Header().Set("X-RogerAI-Provider", "node-7")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	h := ProxyHandler(ProxyOptions{Broker: srv.URL, User: "u"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
	h.ServeHTTP(rec, req)

	for _, unsafe := range []string{"Set-Cookie", "Server"} {
		if got := rec.Header().Get(unsafe); got != "" {
			t.Errorf("unsafe header %q leaked to the client with value %q; the allowlist must stay tight", unsafe, got)
		}
	}
}

// --- #8 413 instead of silent truncation -------------------------------------------------

// TestProxyOversizeBody413: a body over the 4MiB cap must return an OpenAI-shaped 413, never a
// silently-truncated forwarded body. RED today: io.LimitReader truncates at 4MiB and the read
// error is discarded (client.go:395), so a corrupt truncated body is relayed.
func TestProxyOversizeBody413(t *testing.T) {
	sandbox(t)
	var brokerCalled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			brokerCalled = true
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	// A valid-JSON body well over 4MiB (a big pad field).
	pad := strings.Repeat("a", 5<<20)
	body := `{"model":"m","messages":[{"role":"user","content":"` + pad + `"}]}`

	h := ProxyHandler(ProxyOptions{Broker: srv.URL, User: "u"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize body status = %d, want 413 (never silent truncation)", rec.Code)
	}
	if typ, ok := isOpenAIError(rec.Body.Bytes()); !ok || typ != "invalid_request_error" {
		t.Errorf("oversize body error = %q ok=%v, want OpenAI-shaped invalid_request_error", typ, ok)
	}
	if brokerCalled {
		t.Errorf("an oversize body must NOT be truncated-and-relayed (no spend on a corrupt body)")
	}
}

// --- #4 per-session spend budget (seam landed: ProxyOptions.Budget) ----------------------

// billingBroker is a stub broker that bills a fixed cost per response via X-RogerAI-Cost and
// counts how many times /v1/chat/completions was actually dispatched (the spend proxy).
func billingBroker(t *testing.T, costPerResponse string) (srv *httptest.Server, calls *int32) {
	t.Helper()
	var n int32
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			atomic.AddInt32(&n, 1)
		}
		w.Header().Set("Content-Type", "application/json")
		if costPerResponse != "" {
			w.Header().Set("X-RogerAI-Cost", costPerResponse)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"hi"}}]}`))
	}))
	return srv, &n
}

// TestProxySpendBudget: the proxy accumulates each response's X-RogerAI-Cost and hard-stops the
// NEXT request with an OpenAI-shaped 402 once the running total reaches the cap. RED before the
// Budget seam (every request 200s). No real money: the stub bills a fixed header cost.
func TestProxySpendBudget(t *testing.T) {
	sandbox(t)
	srv, calls := billingBroker(t, "0.25")
	defer srv.Close()

	h := ProxyHandler(ProxyOptions{Broker: srv.URL, User: "u", Model: "m", Budget: 1.00})
	post := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
		h.ServeHTTP(rec, req)
		return rec
	}

	// 4 requests spend exactly to the $1.00 cap; each is served 200.
	for i := 1; i <= 4; i++ {
		if rec := post(); rec.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200 (still under the $1.00 cap)", i, rec.Code)
		}
	}
	// The 5th reaches the cap (spent >= budget) and is hard-stopped 402, never dispatched.
	before := atomic.LoadInt32(calls)
	rec := post()
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("5th request: status = %d, want 402 (budget exhausted)", rec.Code)
	}
	if typ, ok := isOpenAIError(rec.Body.Bytes()); !ok || typ != "insufficient_quota" {
		t.Errorf("budget 402 error = %q ok=%v, want type insufficient_quota; body=%q", typ, ok, rec.Body.String())
	}
	if atomic.LoadInt32(calls) != before {
		t.Errorf("the refused 5th request reached the broker; the brake must precede dispatch (no further spend)")
	}
}

// TestProxyBudgetNoCostHeaderNoSpend: a 200 with NO X-RogerAI-Cost accumulates nothing
// (fail-safe: a missing meter never fails OPEN into free spend, and never wrongly brakes).
func TestProxyBudgetNoCostHeaderNoSpend(t *testing.T) {
	sandbox(t)
	srv, _ := billingBroker(t, "") // no cost header
	defer srv.Close()
	h := ProxyHandler(ProxyOptions{Broker: srv.URL, User: "u", Model: "m", Budget: 1.00})
	for i := 0; i < 10; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d with no cost header: status = %d, want 200 (nothing accumulates)", i, rec.Code)
		}
	}
}

// TestProxyBudgetConcurrentCannotRacePast: the CRUX invariant for parallel guest subagents -
// N concurrent requests cannot each read "under budget" and all slip through. With a $1.00 cap
// and $0.25 per response, at most 4 may be served 200 and the total served spend must never
// exceed $1.00. Runs under -race to prove the check+accumulate is atomic.
func TestProxyBudgetConcurrentCannotRacePast(t *testing.T) {
	sandbox(t)
	srv, calls := billingBroker(t, "0.25")
	defer srv.Close()
	h := ProxyHandler(ProxyOptions{Broker: srv.URL, User: "u", Model: "m", Budget: 1.00})

	const n = 20
	var served, refused int32
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
			h.ServeHTTP(rec, req)
			switch rec.Code {
			case http.StatusOK:
				atomic.AddInt32(&served, 1)
			case http.StatusPaymentRequired:
				atomic.AddInt32(&refused, 1)
			default:
				t.Errorf("unexpected status %d", rec.Code)
			}
		}()
	}
	wg.Wait()

	if served > 4 {
		t.Fatalf("served %d requests 200, want at most 4 ($0.25 each under a $1.00 cap); the budget check raced", served)
	}
	if served+refused != n {
		t.Fatalf("served %d + refused %d != %d requests", served, refused, n)
	}
	// Every served request also dispatched to the broker; the total spend is served*0.25.
	if got := atomic.LoadInt32(calls); got != served {
		t.Errorf("broker dispatched %d times but %d were served 200 (a refused request must not dispatch)", got, served)
	}
	if spent := float64(served) * 0.25; spent > 1.00 {
		t.Fatalf("total served spend $%.2f exceeds the $1.00 budget", spent)
	}
}

// --- #6 stream timeout: no blanket http.Client.Timeout (seam: injectable dial/header bounds) --

// TestProxyRelayClientHasNoBlanketTimeout: the relay client must NOT carry a blanket
// http.Client.Timeout (which covers the body read and cuts long streams). Bounds live on the
// Transport (dial + response-header) + the request context instead. RED before ruling 7.
func TestProxyRelayClientHasNoBlanketTimeout(t *testing.T) {
	c := newRelayClient()
	if c.Timeout != 0 {
		t.Fatalf("relay client Timeout = %v, want 0 (a blanket deadline cuts legitimate long streams); bound dial+header instead", c.Timeout)
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("relay client Transport = %T, want *http.Transport with dial+response-header bounds", c.Transport)
	}
	if tr.ResponseHeaderTimeout <= 0 {
		t.Errorf("Transport.ResponseHeaderTimeout = %v, want > 0 (a node that sends no header is dead)", tr.ResponseHeaderTimeout)
	}
}

// TestProxyStreamNotCutByBlanket: a slow stream that trickles response body past the OLD 120s
// blanket completes whole, because the client sets no blanket Timeout. The header timeout is
// injected small; headers arrive immediately, then the body trickles - and is NOT cut.
func TestProxyStreamNotCutByBlanket(t *testing.T) {
	sandbox(t)
	// Inject a small response-header bound so a DEAD (no-header) connection fails fast; a
	// HEALTHY stream that sends headers immediately then trickles the body is unaffected by it.
	defer func(d, hdr time.Duration) { proxyDialTimeout, proxyResponseHeaderTimeout = d, hdr }(proxyDialTimeout, proxyResponseHeaderTimeout)
	proxyResponseHeaderTimeout = 200 * time.Millisecond

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-RogerAI-Cost", "0.01")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		if fl != nil {
			fl.Flush()
		}
		// Trickle chunks whose total wall-clock exceeds the injected header bound many times
		// over - a blanket Timeout at ~header-bound would cut this; dial+header bounds do not.
		for i := 0; i < 6; i++ {
			time.Sleep(100 * time.Millisecond)
			_, _ = w.Write([]byte("data: chunk\n\n"))
			if fl != nil {
				fl.Flush()
			}
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	h := ProxyHandler(ProxyOptions{Broker: srv.URL, User: "u", Model: "m"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m","stream":true}`))
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("slow stream status = %d, want 200 (a healthy trickling stream must not be cut)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "[DONE]") {
		t.Fatalf("stream was cut before completion: body=%q", rec.Body.String())
	}
	if n := strings.Count(rec.Body.String(), "data: chunk"); n != 6 {
		t.Errorf("delivered %d/6 chunks; the stream was truncated", n)
	}
}

// TestProxyUncappedRelaysNotSerialized: an UNCAPPED session (Budget 0 - `roger use`, the TUI,
// parallel CLIs) must relay fully in parallel; the budget admission gate (which serializes the
// dial+header phase for budgeted sessions) must be skipped entirely. Regression for review
// HIGH #3: the gate was taken unconditionally, serializing every relay. The stub broker
// REQUIRES both requests to be in flight simultaneously before answering either - a
// serialized proxy deadlocks here and the test fails by timeout.
func TestProxyUncappedRelaysNotSerialized(t *testing.T) {
	sandbox(t)
	arrived := make(chan struct{}, 2)
	proceed := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arrived <- struct{}{}
		<-proceed
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-RogerAI-Cost", "0.10")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"hi"}}]}`))
	}))
	defer srv.Close()

	h := ProxyHandler(ProxyOptions{Broker: srv.URL, User: "u", Model: "m"}) // Budget 0 = uncapped
	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
			h.ServeHTTP(rec, req)
		}()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-arrived:
		case <-time.After(5 * time.Second):
			close(proceed) // unblock the in-flight request so cleanup can drain fast
			t.Fatal("uncapped relays are serialized: the 2nd request never reached the broker while the 1st was in flight (the budget gate must be skipped when Budget <= 0)")
		}
	}
	close(proceed)
	wg.Wait()
}

// --- #9 live ProxyOptions (seam landed: ProxyOptionsHolder) -------------------------------

// TestProxyLiveOptions: the handler reads its options LIVE from the holder, so a re-tune
// re-points the SAME endpoint atomically - /v1/models follows the new band, relays carry the
// new routing, and the session key is stable across the re-point. A disconnect leaves the
// proxy refusing to spend (ruling 5). RED before the live-source seam.
func TestProxyLiveOptions(t *testing.T) {
	sandbox(t)
	var gotFreq string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			gotFreq = r.Header.Get("X-Roger-Freq")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"hi"}}]}`))
	}))
	defer srv.Close()

	const key = "stable-session-key"
	holder := NewProxyOptionsHolder(ProxyOptions{Broker: srv.URL, User: "u", Model: "qwen3-32b-fp8", SessionKey: key})
	h := ProxyHandlerLive(holder)
	auth := func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+key) }

	// /v1/models reflects band A.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	auth(req)
	h.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "qwen3-32b-fp8") {
		t.Fatalf("/v1/models before re-tune = %q, want band A qwen3-32b-fp8", rec.Body.String())
	}

	// Re-tune to band B on a private freq, KEEPING the session key stable (ruling 6).
	holder.SetBand(ProxyOptions{Broker: srv.URL, User: "u", Model: "llama-3.3-70b", Freq: "SEVENTX"})
	if holder.Get().SessionKey != key {
		t.Fatalf("session key changed across a re-tune (%q); it must be stable so a running guest's config keeps working", holder.Get().SessionKey)
	}

	// /v1/models now follows band B (no stale model).
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	auth(req)
	h.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "llama-3.3-70b") || strings.Contains(rec.Body.String(), "qwen3-32b-fp8") {
		t.Fatalf("/v1/models after re-tune = %q, want band B llama-3.3-70b only (no stale A)", rec.Body.String())
	}

	// A relay now carries band B's private routing.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"x"}`))
	auth(req)
	h.ServeHTTP(rec, req)
	if gotFreq != "SEVENTX" {
		t.Fatalf("relay carried X-Roger-Freq %q, want band B's SEVENTX (live options, not stale A)", gotFreq)
	}

	// Disconnect: the proxy refuses to spend (no band tuned), broker never called.
	gotFreq = ""
	holder.Disconnect()
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"x"}`))
	auth(req)
	h.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatalf("a disconnected proxy served a relay (status 200); it must refuse to spend with a clean error")
	}
	if _, ok := isOpenAIError(rec.Body.Bytes()); !ok {
		t.Errorf("disconnected refusal body is not an OpenAI error: %q", rec.Body.String())
	}
	if gotFreq != "" {
		t.Errorf("a disconnected proxy reached the broker (no spend allowed when no band is tuned)")
	}
}
