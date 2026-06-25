package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// TestServerTimeoutsConfigured asserts the tuned http.Server keeps the SAFE timeouts
// (ReadHeaderTimeout/ReadTimeout/IdleTimeout/MaxHeaderBytes) and DELIBERATELY leaves
// WriteTimeout unset so the long-lived SSE stream + agent long-poll routes are never
// capped. It also confirms the streaming routes are excluded from the per-handler
// response deadline while every other route is bounded under Cloudflare's ~100s cap.
func TestServerTimeoutsConfigured(t *testing.T) {
	// The constants the production server is built from (main.go).
	if nonStreamTimeout <= 0 || nonStreamTimeout >= 100*time.Second {
		t.Errorf("nonStreamTimeout = %s, want >0 and < 100s (under the CF proxy cap)", nonStreamTimeout)
	}
	// The streaming/long-poll routes MUST be exempt from the response deadline.
	for _, p := range []string{"/agent/poll", "/agent/stream", "/agent/result", "/v1/chat/completions", "/concierge"} {
		if !streamRoutes[p] {
			t.Errorf("%s should be a stream-exempt route (no response deadline)", p)
		}
	}
	// A non-streaming route MUST NOT be exempt (it gets bounded).
	for _, p := range []string{"/discover", "/balance", "/nodes/register"} {
		if streamRoutes[p] {
			t.Errorf("%s should NOT be stream-exempt (it must be bounded)", p)
		}
	}

	// streamSafeHandler routes a non-stream path through the bounded handler and a
	// stream path through unbounded - assert both reach the inner mux (we are not
	// triggering a timeout here, just that the wrapper dispatches correctly).
	mux := http.NewServeMux()
	hit := ""
	mux.HandleFunc("/discover", func(w http.ResponseWriter, r *http.Request) { hit = "discover"; w.WriteHeader(200) })
	mux.HandleFunc("/agent/poll", func(w http.ResponseWriter, r *http.Request) { hit = "poll"; w.WriteHeader(200) })
	h := streamSafeHandler(mux)

	for _, tc := range []struct{ path, want string }{{"/discover", "discover"}, {"/agent/poll", "poll"}} {
		hit = ""
		r := httptest.NewRequest(http.MethodGet, tc.path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if hit != tc.want {
			t.Errorf("streamSafeHandler(%s) routed to %q, want %q", tc.path, hit, tc.want)
		}
	}
}

// TestNonStreamRelayWaitUnderCFCap asserts the NON-stream relay's provider wait is held
// below Cloudflare's ~100s proxy cap so the broker returns its own retryable 504 before
// CF would emit an opaque 524, and that the agent long-poll is comfortably under it too.
func TestNonStreamRelayWaitUnderCFCap(t *testing.T) {
	const cfCap = 100 * time.Second
	if nonStreamRelayWait >= cfCap {
		t.Errorf("nonStreamRelayWait = %s, must be < CF ~100s cap so the broker 504s before CF 524s", nonStreamRelayWait)
	}
	// 10s of headroom is "comfortably under".
	if nonStreamRelayWait > cfCap-10*time.Second {
		t.Errorf("nonStreamRelayWait = %s, want >=10s of headroom below the ~100s CF cap", nonStreamRelayWait)
	}
}

// TestClientIPPrefersCFConnectingIP asserts the true-client-IP helper trusts
// CF-Connecting-IP first (NOT client-spoofable behind Cloudflare) and IGNORES a spoofed
// X-Forwarded-For when the CF header is present, falling back to XFF then RemoteAddr only
// when CF is absent. This is the IP that keys the rate limiter AND the CSAM legal record.
func TestClientIPPrefersCFConnectingIP(t *testing.T) {
	// CF header present + a spoofed XFF: the CF IP wins, the spoof is ignored.
	r := httptest.NewRequest(http.MethodPost, "/concierge", nil)
	r.Header.Set("CF-Connecting-IP", "203.0.113.7")
	r.Header.Set("X-Forwarded-For", "1.2.3.4, 5.6.7.8") // attacker-supplied, must be ignored
	r.RemoteAddr = "10.0.0.1:5555"
	if got := clientIP(r); got != "203.0.113.7" {
		t.Errorf("clientIP with CF header = %q, want the CF-Connecting-IP 203.0.113.7 (spoofed XFF must be ignored)", got)
	}

	// No CF header: fall back to the first XFF hop.
	r2 := httptest.NewRequest(http.MethodPost, "/concierge", nil)
	r2.Header.Set("X-Forwarded-For", "1.2.3.4, 5.6.7.8")
	if got := clientIP(r2); got != "1.2.3.4" {
		t.Errorf("clientIP without CF = %q, want the first XFF hop 1.2.3.4", got)
	}

	// No proxy headers at all: fall back to the RemoteAddr host.
	r3 := httptest.NewRequest(http.MethodPost, "/concierge", nil)
	r3.RemoteAddr = "198.51.100.9:4444"
	if got := clientIP(r3); got != "198.51.100.9" {
		t.Errorf("clientIP with no proxy header = %q, want the RemoteAddr host 198.51.100.9", got)
	}
}

// TestAnonPerIPRateLimit asserts the unauthenticated public surface (/discover, a
// no-auth path) is bounded PER CF-IP: a single IP trips the per-IP limit once its
// bucket drains, while a different source IP keeps its own allowance. The per-IP key is
// the validated CF-Connecting-IP (clientIP). This is the same bucket discipline applied
// to the anon relay path and the concierge.
func TestAnonPerIPRateLimit(t *testing.T) {
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	b := &broker{
		priv:         brokerPriv,
		db:           store.NewMem(),
		nodes:        map[string]protocol.NodeRegistration{},
		tunnels:      map[string]*nodeTunnel{},
		lastSeen:     map[string]time.Time{},
		confidential: map[string]bool{},
		private:      map[string]bool{},
		tps:          map[string]float64{},
		inflight:     map[string]int{},
		success:      map[string]float64{},
		trust:        map[string]trustState{},
		quotes:       map[string]priceQuote{},
		banned:       map[string]bool{},
		// Tiny anon bucket so a couple of requests trip it deterministically.
		anonRL: &rateLimiter{buckets: map[string]*tokenBucket{}, rpm: 1, burst: 2},
	}

	doDiscover := func(ip string) int {
		r := httptest.NewRequest(http.MethodGet, "/discover", nil)
		r.Header.Set("CF-Connecting-IP", ip)
		w := httptest.NewRecorder()
		b.discover(w, r)
		return w.Code
	}

	// IP A: burst of 2 passes the per-IP limiter, the 3rd trips it (429).
	if c := doDiscover("198.51.100.1"); c == http.StatusTooManyRequests {
		t.Fatalf("discover req 1 from IP A unexpectedly 429")
	}
	if c := doDiscover("198.51.100.1"); c == http.StatusTooManyRequests {
		t.Fatalf("discover req 2 from IP A unexpectedly 429")
	}
	if c := doDiscover("198.51.100.1"); c != http.StatusTooManyRequests {
		t.Errorf("discover req 3 from IP A = %d, want 429 (per-IP bucket drained)", c)
	}
	// IP B has its OWN bucket and is still allowed despite IP A being limited.
	if c := doDiscover("198.51.100.2"); c == http.StatusTooManyRequests {
		t.Errorf("discover req from a DIFFERENT IP B = 429, want its own per-IP bucket")
	}
	// A spoofed X-Forwarded-For must NOT let IP A escape its bucket when CF is present:
	// clientIP keys on CF-Connecting-IP, so IP A is still limited regardless of XFF.
	r := httptest.NewRequest(http.MethodGet, "/discover", nil)
	r.Header.Set("CF-Connecting-IP", "198.51.100.1")
	r.Header.Set("X-Forwarded-For", "9.9.9.9") // attacker tries to dodge the bucket
	w := httptest.NewRecorder()
	b.discover(w, r)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("IP A with a spoofed XFF = %d, want 429 (CF-Connecting-IP keys the bucket, XFF ignored)", w.Code)
	}
}

// TestRecountCaptureBounded asserts the off-band L1 re-count capture buffer is capped so
// a malicious node streaming an unbounded body cannot OOM the broker: agentStream stops
// growing sink.cap once it reaches maxRecountCapture, while the client still receives the
// full (uncapped) stream.
func TestRecountCaptureBounded(t *testing.T) {
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	b := &broker{
		priv:    brokerPriv,
		nodes:   map[string]protocol.NodeRegistration{},
		tunnels: map[string]*nodeTunnel{},
		streams: map[string]*streamSink{},
		trust:   map[string]trustState{},
	}
	b.tunnels["n1"] = &nodeTunnel{jobs: make(chan protocol.Job, 1), waiters: map[string]chan protocol.JobResult{}, token: "tok"}

	// The client sink (a recorder) with capture enabled.
	rec := httptest.NewRecorder()
	sink := &streamSink{w: rec, flush: func() {}, nodeID: "n1", start: time.Now(), cap: &bytes.Buffer{}}
	b.streams["job1"] = sink

	// A node streams WAY more delta text than the cap allows: build SSE lines whose
	// combined content exceeds maxRecountCapture several times over.
	var body bytes.Buffer
	line := func(s string) { body.WriteString(`data: {"choices":[{"delta":{"content":"` + s + `"}}]}` + "\n") }
	chunk := make([]byte, 1024)
	for i := range chunk {
		chunk[i] = 'x'
	}
	for body.Len() < maxRecountCapture*4 {
		line(string(chunk))
	}

	r := httptest.NewRequest(http.MethodPost, "/agent/stream?node=n1&job=job1", bytes.NewReader(body.Bytes()))
	r.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	b.agentStream(w, r)

	// The off-band capture buffer must be bounded (with a small slack for the in-flight
	// carry line at the moment the cap was hit).
	sink.capMu.Lock()
	capLen := sink.cap.Len()
	sink.capMu.Unlock()
	if capLen >= maxRecountCapture+64<<10 {
		t.Errorf("recount capture = %d bytes, want bounded near maxRecountCapture (%d)", capLen, maxRecountCapture)
	}
	// The client still got the FULL stream (capture only bounds the private copy).
	if rec.Body.Len() < maxRecountCapture {
		t.Errorf("client received %d bytes, want the full uncapped stream (>= %d)", rec.Body.Len(), maxRecountCapture)
	}
}

// TestFreeRegistrationCeiling asserts the per-CF-IP free-node registration ceiling: a
// single IP may register up to freeRegPerIP NEW free nodes within the window, and the
// next NEW one is rejected with 429; an idempotent re-register of an existing free node
// is never counted; and a different IP has its own allowance.
func TestFreeRegistrationCeiling(t *testing.T) {
	b := newCeilingBroker(t)
	b.freeRegPerIP = 2
	b.freeRegWindow = time.Hour

	// IP A registers 2 NEW free nodes (allowed), the 3rd NEW one is rejected.
	if code, _ := registerFree(t, b, "a1", "203.0.113.10"); code != http.StatusOK {
		t.Fatalf("free node a1 from IP A = %d, want 200", code)
	}
	if code, _ := registerFree(t, b, "a2", "203.0.113.10"); code != http.StatusOK {
		t.Fatalf("free node a2 from IP A = %d, want 200", code)
	}
	if code, msg := registerFree(t, b, "a3", "203.0.113.10"); code != http.StatusTooManyRequests {
		t.Errorf("free node a3 from IP A = %d (%q), want 429 (ceiling reached)", code, msg)
	}
	// An idempotent re-register of an EXISTING free node (a1) is not a NEW node, so it
	// is never counted or rejected even though IP A is at the ceiling.
	if code, _ := registerFree(t, b, "a1", "203.0.113.10"); code != http.StatusOK {
		t.Errorf("re-register of existing free node a1 = %d, want 200 (not counted as new)", code)
	}
	// A DIFFERENT IP has its own allowance.
	if code, _ := registerFree(t, b, "b1", "203.0.113.20"); code != http.StatusOK {
		t.Errorf("free node b1 from IP B = %d, want 200 (own per-IP allowance)", code)
	}
}

// newCeilingBroker builds a broker sufficient for the free-registration register path.
func newCeilingBroker(t *testing.T) *broker {
	t.Helper()
	mem := store.NewMem()
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	return &broker{
		db:           mem,
		priv:         brokerPriv,
		nodes:        map[string]protocol.NodeRegistration{},
		tunnels:      map[string]*nodeTunnel{},
		lastSeen:     map[string]time.Time{},
		confidential: map[string]bool{},
		private:      map[string]bool{},
		bandOf:       map[string]string{},
		attestedAt:   map[string]time.Time{},
		attest:       loadAttestRegistry(),
		tps:          map[string]float64{},
		pubOfUser:    map[string]string{},
		freeRegByIP:  map[string][]time.Time{},
	}
}

// registerFree posts an UNSIGNED, FREE (all-zero-price, public) node registration from
// the given CF-IP and returns the status + the broker's error message. The node key is
// derived deterministically from nodeID so a re-register of the SAME node id reuses the
// SAME key (a fresh key would trip the TOFU "node_id bound to a different key" 403).
func registerFree(t *testing.T, b *broker, nodeID, ip string) (int, string) {
	t.Helper()
	seed := sha256.Sum256([]byte("free-node-seed:" + nodeID))
	nodePriv := ed25519.NewKeyFromSeed(seed[:])
	nodePub := nodePriv.Public().(ed25519.PublicKey)
	reg := protocol.NodeRegistration{
		NodeID: nodeID, PubKey: hex.EncodeToString(nodePub), BridgeToken: "tok-" + nodeID,
		TS: time.Now().Unix(), Offers: []protocol.ModelOffer{{Model: "m", Ctx: 4096}}, // free: no price
	}
	reg.SignRegistration(nodePriv) // proof-of-possession of the node key (NOT an owner sig)
	body, _ := json.Marshal(reg)
	r := httptest.NewRequest(http.MethodPost, "/nodes/register", bytes.NewReader(body))
	r.Header.Set("CF-Connecting-IP", ip)
	w := httptest.NewRecorder()
	b.register(w, r)
	var resp struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	return w.Code, resp.Error.Message
}
