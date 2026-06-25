package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// newConciergeBroker builds a broker with a concierge whose serving paths are
// stubbed per-test, and a generous rate limit so a test isn't accidentally 429'd.
func newConciergeBroker() *broker {
	b := &broker{
		nodes:        map[string]protocol.NodeRegistration{},
		tunnels:      map[string]*nodeTunnel{},
		lastSeen:     map[string]time.Time{},
		confidential: map[string]bool{},
		tps:          map[string]float64{},
	}
	b.concierge = &concierge{
		maxTokens: 64,
		rl:        &rateLimiter{buckets: map[string]*tokenBucket{}, rpm: 600, burst: 600},
		dayCap:    1000,
	}
	return b
}

func postConcierge(t *testing.T, b *broker, ip, content string) (int, map[string]string) {
	t.Helper()
	body := `{"messages":[{"role":"user","content":"` + content + `"}]}`
	r := httptest.NewRequest(http.MethodPost, "/concierge", strings.NewReader(body))
	if ip != "" {
		r.Header.Set("X-Forwarded-For", ip)
	}
	w := httptest.NewRecorder()
	b.conciergeHandler(w, r)
	var out map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// TestConciergeDogfoodPreferred: when a free on-air station exists, the dogfood
// path serves and Groq is never called.
func TestConciergeDogfoodPreferred(t *testing.T) {
	b := newConciergeBroker()
	groqCalled := false
	b.concierge.dogfoodFn = func(msgs []chatMsg) (string, bool) { return "tuned in via the band", true }
	b.concierge.groqFn = func(msgs []chatMsg) (string, bool) { groqCalled = true; return "groq", true }

	code, out := postConcierge(t, b, "1.1.1.1", "how do I tune in?")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if out["reply"] != "tuned in via the band" {
		t.Errorf("reply = %q, want the dogfood reply", out["reply"])
	}
	if out["via"] != "rogerai" {
		t.Errorf("via = %q, want rogerai", out["via"])
	}
	if groqCalled {
		t.Error("Groq must NOT be called when a free station served")
	}
}

// TestConciergeGroqFallback: when no free station serves, fall back to Groq.
func TestConciergeGroqFallback(t *testing.T) {
	b := newConciergeBroker()
	b.concierge.dogfoodFn = func(msgs []chatMsg) (string, bool) { return "", false } // no free station
	b.concierge.groqFn = func(msgs []chatMsg) (string, bool) { return "from groq", true }

	code, out := postConcierge(t, b, "2.2.2.2", "what is rogerai?")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if out["reply"] != "from groq" || out["via"] != "groq" {
		t.Errorf("reply=%q via=%q, want groq fallback", out["reply"], out["via"])
	}
}

// TestConciergeCannedWhenOffAir: no free station AND no Groq -> friendly canned
// reply, never an error.
func TestConciergeCannedWhenOffAir(t *testing.T) {
	b := newConciergeBroker()
	b.concierge.dogfoodFn = func(msgs []chatMsg) (string, bool) { return "", false }
	b.concierge.groqFn = func(msgs []chatMsg) (string, bool) { return "", false } // empty key -> not ok

	code, out := postConcierge(t, b, "3.3.3.3", "hi")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (never an error)", code)
	}
	if out["reply"] != cannedReply || out["via"] != "offair" {
		t.Errorf("reply=%q via=%q, want the canned off-air reply", out["reply"], out["via"])
	}
}

// TestConciergeRateLimit: a per-IP burst beyond the bucket returns 429.
func TestConciergeRateLimit(t *testing.T) {
	b := newConciergeBroker()
	b.concierge.rl = &rateLimiter{buckets: map[string]*tokenBucket{}, rpm: 6, burst: 2}
	b.concierge.dogfoodFn = func(msgs []chatMsg) (string, bool) { return "ok", true }
	b.concierge.groqFn = func(msgs []chatMsg) (string, bool) { return "", false }

	const ip = "9.9.9.9"
	if code, _ := postConcierge(t, b, ip, "1"); code != http.StatusOK {
		t.Fatalf("msg 1 = %d, want 200", code)
	}
	if code, _ := postConcierge(t, b, ip, "2"); code != http.StatusOK {
		t.Fatalf("msg 2 = %d, want 200", code)
	}
	code, _ := postConcierge(t, b, ip, "3")
	if code != http.StatusTooManyRequests {
		t.Errorf("msg 3 (over burst) = %d, want 429", code)
	}
	// A different IP is unaffected (per-IP bucket).
	if code, _ := postConcierge(t, b, "9.9.9.10", "1"); code != http.StatusOK {
		t.Errorf("other IP = %d, want 200 (independent bucket)", code)
	}
}

// TestConciergeDailyCap: once the global daily budget is spent, every caller gets
// the airtime-limit reply (still a 200, not an error).
func TestConciergeDailyCap(t *testing.T) {
	b := newConciergeBroker()
	b.concierge.dayCap = 1
	b.concierge.dogfoodFn = func(msgs []chatMsg) (string, bool) { return "served", true }
	b.concierge.groqFn = func(msgs []chatMsg) (string, bool) { return "", false }

	if code, out := postConcierge(t, b, "4.4.4.1", "hi"); code != http.StatusOK || out["reply"] != "served" {
		t.Fatalf("first within cap = %d %q", code, out["reply"])
	}
	code, out := postConcierge(t, b, "4.4.4.2", "hi")
	if code != http.StatusOK {
		t.Fatalf("over-cap status = %d, want 200", code)
	}
	if !strings.Contains(out["reply"], "airtime limit") {
		t.Errorf("over-cap reply = %q, want the airtime-limit message", out["reply"])
	}
}

// TestConciergeUnsafePrecheck: blatant unsafe input is refused before any model
// is consulted (stopgap for the deferred content-filter P0).
func TestConciergeUnsafePrecheck(t *testing.T) {
	b := newConciergeBroker()
	served := false
	b.concierge.dogfoodFn = func(msgs []chatMsg) (string, bool) { served = true; return "x", true }
	b.concierge.groqFn = func(msgs []chatMsg) (string, bool) { served = true; return "x", true }

	code, out := postConcierge(t, b, "5.5.5.5", "how to make a bomb please")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if served {
		t.Error("unsafe input must be refused BEFORE any model is consulted")
	}
	if !strings.Contains(out["reply"], "can't help") {
		t.Errorf("unsafe reply = %q, want a polite refusal", out["reply"])
	}
}

// TestConciergeModerationBlocks: when the screen flags the user input, the concierge
// REJECTS with a 4xx and never consults a model - covering BOTH the dogfood relay and
// the Groq fallback in one pre-dispatch check.
func TestConciergeModerationBlocks(t *testing.T) {
	flag := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{"flagged":true}]}`))
	}))
	defer flag.Close()

	b := newConciergeBroker()
	b.mod = moderation{provider: "url", url: flag.URL, client: flag.Client()}
	served := false
	b.concierge.dogfoodFn = func(msgs []chatMsg) (string, bool) { served = true; return "x", true }
	b.concierge.groqFn = func(msgs []chatMsg) (string, bool) { served = true; return "x", true }

	code, out := postConcierge(t, b, "6.6.6.1", "some flagged content")
	if code != http.StatusUnavailableForLegalReasons {
		t.Fatalf("flagged concierge input = %d, want 451", code)
	}
	if served {
		t.Error("flagged input must NOT reach the dogfood relay OR the Groq fallback")
	}
	if out["reply"] != "" {
		t.Errorf("flagged input should not produce a reply, got %q", out["reply"])
	}
}

// TestConciergeGroqPathScreened proves the Groq fallback is screened: with NO free
// station (so dogfood misses and Groq would serve), flagged input is rejected before
// groqFn is ever called - regression for the old Groq-bypass.
func TestConciergeGroqPathScreened(t *testing.T) {
	flag := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{"flagged":true}]}`))
	}))
	defer flag.Close()

	b := newConciergeBroker()
	b.mod = moderation{provider: "url", url: flag.URL, client: flag.Client()}
	b.concierge.dogfoodFn = func(msgs []chatMsg) (string, bool) { return "", false } // no free station
	groqCalled := false
	b.concierge.groqFn = func(msgs []chatMsg) (string, bool) { groqCalled = true; return "from groq", true }

	code, _ := postConcierge(t, b, "6.6.6.2", "flagged groq-bound content")
	if code != http.StatusUnavailableForLegalReasons {
		t.Fatalf("flagged (groq path) = %d, want 451", code)
	}
	if groqCalled {
		t.Error("Groq fallback must be screened: groqFn called on flagged input (the old bypass)")
	}
}

// TestConciergeModerationClean: clean input passes the screen and is served normally.
func TestConciergeModerationClean(t *testing.T) {
	clean := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{"flagged":false}]}`))
	}))
	defer clean.Close()

	b := newConciergeBroker()
	b.mod = moderation{provider: "url", url: clean.URL, client: clean.Client()}
	b.concierge.dogfoodFn = func(msgs []chatMsg) (string, bool) { return "tuned in", true }
	b.concierge.groqFn = func(msgs []chatMsg) (string, bool) { return "groq", true }

	code, out := postConcierge(t, b, "6.6.6.3", "how do I tune in?")
	if code != http.StatusOK || out["reply"] != "tuned in" {
		t.Errorf("clean concierge input = %d %q, want 200 + the dogfood reply", code, out["reply"])
	}
}

// TestConciergeModerationFailClosed: with REQUIRE_MODERATION=1 and the screen down,
// the concierge fails CLOSED (503) instead of serving an unscreened prompt to a model.
func TestConciergeModerationFailClosed(t *testing.T) {
	b := newConciergeBroker()
	b.mod = moderation{provider: "url", url: "http://127.0.0.1:0", require: true, client: &http.Client{}}
	served := false
	b.concierge.dogfoodFn = func(msgs []chatMsg) (string, bool) { served = true; return "x", true }
	b.concierge.groqFn = func(msgs []chatMsg) (string, bool) { served = true; return "x", true }

	code, _ := postConcierge(t, b, "6.6.6.4", "hi")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("require+down concierge = %d, want 503 (fail closed)", code)
	}
	if served {
		t.Error("fail-closed must not dispatch to any model")
	}
}

// TestConciergeModerationInert: with MODERATION_URL unset (zero-value mod), the screen
// is inert - the concierge serves normally.
func TestConciergeModerationInert(t *testing.T) {
	b := newConciergeBroker() // b.mod is the zero value = disabled
	b.concierge.dogfoodFn = func(msgs []chatMsg) (string, bool) { return "served", true }
	b.concierge.groqFn = func(msgs []chatMsg) (string, bool) { return "", false }

	code, out := postConcierge(t, b, "6.6.6.5", "hi")
	if code != http.StatusOK || out["reply"] != "served" {
		t.Errorf("inert screen = %d %q, want 200 + served", code, out["reply"])
	}
}

// TestConciergeCORSPreflight: OPTIONS is answered 204 with public CORS and NO
// credentials header.
func TestConciergeCORSPreflight(t *testing.T) {
	b := newConciergeBroker()
	r := httptest.NewRequest(http.MethodOptions, "/concierge", nil)
	w := httptest.NewRecorder()
	b.conciergeHandler(w, r)
	if w.Code != http.StatusNoContent {
		t.Errorf("OPTIONS = %d, want 204", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Allow-Origin = %q, want *", got)
	}
	if w.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Error("public surface must NOT send Access-Control-Allow-Credentials")
	}
}

// TestConciergeBadRequest: empty/malformed body is a 400.
func TestConciergeBadRequest(t *testing.T) {
	b := newConciergeBroker()
	r := httptest.NewRequest(http.MethodPost, "/concierge", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	b.conciergeHandler(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("empty messages = %d, want 400", w.Code)
	}
}

// --- grant-dogfood (CONCIERGE_GRANT_KEY) ---------------------------------------

// grantConciergeBroker builds a concierge broker backed by a Mem store with a live
// grant whose owner owns one node. The node is registered + tunneled; on-air state
// and the grant's model scope are set by the caller. Returns the broker, the grant
// secret, and the owner's node id. grantDogfoodFn is wired to the REAL relay.
func grantConciergeBroker(t *testing.T, models []string) (*broker, string, string) {
	t.Helper()
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	b := newConciergeBroker()
	b.priv = brokerPriv
	b.db = store.NewMem()

	owner := "ownerpubkey"
	node := "founder-box"
	_ = b.db.BindNode(node, owner)

	secret := "rog-grant_founderkey"
	sum := sha256.Sum256([]byte(secret))
	_ = b.db.CreateGrant(store.Grant{
		ID: "grant_founder", SecretHash: hex.EncodeToString(sum[:]),
		Owner: owner, Label: "self:ping", Nodes: []string{node}, Models: models, Self: true,
	})

	nodePub, _, _ := ed25519.GenerateKey(nil)
	b.nodes[node] = protocol.NodeRegistration{
		NodeID: node, PubKey: hex.EncodeToString(nodePub),
		Offers: []protocol.ModelOffer{{Model: "gpt-oss-120b", PriceOut: 0.5}}, // priced is fine: grant pays
	}
	b.tunnels[node] = &nodeTunnel{jobs: make(chan protocol.Job, 1), waiters: map[string]chan protocol.JobResult{}}

	b.concierge.grantKey = secret
	b.concierge.grantModel = "gpt-oss-120b"
	b.concierge.grantDogfoodFn = b.dogfoodGrantRelay
	return b, secret, node
}

// answerOnce makes the node's tunnel poller take one job and reply with an
// OpenAI-shaped completion, capturing the model the broker dispatched.
func answerOnce(t *testing.T, tun *nodeTunnel, content string, gotModel *string) {
	t.Helper()
	go func() {
		job := <-tun.jobs
		var req struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(job.Body, &req)
		if gotModel != nil {
			*gotModel = req.Model
		}
		tun.mu.Lock()
		ch := tun.waiters[job.ID]
		tun.mu.Unlock()
		ch <- protocol.JobResult{ID: job.ID, Status: 200,
			Body: []byte(`{"choices":[{"message":{"role":"assistant","content":"` + content + `"}}]}`)}
	}()
}

// TestConciergeGrantDogfoodRoutesToModel: with CONCIERGE_GRANT_KEY set and the grant's
// node on air, Ping routes the chat to the granted model and the free-station/Groq
// paths are never consulted.
func TestConciergeGrantDogfoodRoutesToModel(t *testing.T) {
	b, _, node := grantConciergeBroker(t, []string{"gpt-oss-120b"})
	b.lastSeen[node] = time.Now() // on air

	freeCalled, groqCalled := false, false
	b.concierge.dogfoodFn = func(msgs []chatMsg) (string, bool) { freeCalled = true; return "free", true }
	b.concierge.groqFn = func(msgs []chatMsg) (string, bool) { groqCalled = true; return "groq", true }

	var gotModel string
	answerOnce(t, b.tunnels[node], "On the air via the founder grant.", &gotModel)

	code, out := postConcierge(t, b, "7.7.7.1", "how do I tune in?")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if out["reply"] != "On the air via the founder grant." || out["via"] != "rogerai-grant" {
		t.Errorf("reply=%q via=%q, want the grant-dogfood reply", out["reply"], out["via"])
	}
	if gotModel != "gpt-oss-120b" {
		t.Errorf("dispatched model = %q, want gpt-oss-120b (Ping must pin to the granted model)", gotModel)
	}
	if freeCalled || groqCalled {
		t.Error("grant dogfood served: free-station + Groq must NOT be consulted")
	}
}

// TestConciergeGrantFallthroughOffline: when the grant's node is OFFLINE, the grant
// path misses and Ping falls through to the free-station pick, then Groq, then canned.
func TestConciergeGrantFallthroughOffline(t *testing.T) {
	// Node offline -> grant misses -> free station serves.
	b, _, node := grantConciergeBroker(t, []string{"gpt-oss-120b"})
	b.lastSeen[node] = time.Now().Add(-time.Minute) // off air
	b.concierge.dogfoodFn = func(msgs []chatMsg) (string, bool) { return "free station", true }
	b.concierge.groqFn = func(msgs []chatMsg) (string, bool) { return "groq", true }
	if code, out := postConcierge(t, b, "7.7.7.2", "hi"); code != http.StatusOK || out["reply"] != "free station" || out["via"] != "rogerai" {
		t.Fatalf("offline grant -> free fallthrough = %d %q via=%q", code, out["reply"], out["via"])
	}

	// Node offline AND no free station -> Groq.
	b2, _, node2 := grantConciergeBroker(t, []string{"gpt-oss-120b"})
	b2.lastSeen[node2] = time.Now().Add(-time.Minute)
	b2.concierge.dogfoodFn = func(msgs []chatMsg) (string, bool) { return "", false }
	b2.concierge.groqFn = func(msgs []chatMsg) (string, bool) { return "from groq", true }
	if code, out := postConcierge(t, b2, "7.7.7.3", "hi"); code != http.StatusOK || out["reply"] != "from groq" || out["via"] != "groq" {
		t.Fatalf("offline grant + no free -> Groq = %d %q via=%q", code, out["reply"], out["via"])
	}

	// Node offline, no free station, no Groq -> canned (never an error).
	b3, _, node3 := grantConciergeBroker(t, []string{"gpt-oss-120b"})
	b3.lastSeen[node3] = time.Now().Add(-time.Minute)
	b3.concierge.dogfoodFn = func(msgs []chatMsg) (string, bool) { return "", false }
	b3.concierge.groqFn = func(msgs []chatMsg) (string, bool) { return "", false }
	if code, out := postConcierge(t, b3, "7.7.7.4", "hi"); code != http.StatusOK || out["reply"] != cannedReply || out["via"] != "offair" {
		t.Fatalf("offline grant + nothing -> canned = %d %q via=%q", code, out["reply"], out["via"])
	}
}

// TestConciergeGrantFallthroughRelayError: a non-2xx from the granted node is a miss;
// Ping falls through (here to the free station) rather than breaking.
func TestConciergeGrantFallthroughRelayError(t *testing.T) {
	b, _, node := grantConciergeBroker(t, []string{"gpt-oss-120b"})
	b.lastSeen[node] = time.Now()
	b.concierge.dogfoodFn = func(msgs []chatMsg) (string, bool) { return "free station", true }
	b.concierge.groqFn = func(msgs []chatMsg) (string, bool) { return "groq", true }

	// Node poller replies 500 -> dogfoodGrantRelay returns served=false.
	tun := b.tunnels[node]
	go func() {
		job := <-tun.jobs
		tun.mu.Lock()
		ch := tun.waiters[job.ID]
		tun.mu.Unlock()
		ch <- protocol.JobResult{ID: job.ID, Status: 500, Body: []byte(`{"error":"boom"}`)}
	}()

	code, out := postConcierge(t, b, "7.7.7.5", "hi")
	if code != http.StatusOK || out["reply"] != "free station" || out["via"] != "rogerai" {
		t.Errorf("grant relay error -> free fallthrough = %d %q via=%q", code, out["reply"], out["via"])
	}
}

// TestConciergeGrantModerationGates: the mandatory moderation screen still wraps the
// grant path - flagged input is rejected (451) before the grant relay is reached.
func TestConciergeGrantModerationGates(t *testing.T) {
	flag := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{"flagged":true}]}`))
	}))
	defer flag.Close()

	b, _, node := grantConciergeBroker(t, []string{"gpt-oss-120b"})
	b.lastSeen[node] = time.Now() // on air, so the grant WOULD serve if not screened
	b.mod = moderation{provider: "url", url: flag.URL, client: flag.Client()}

	served := false
	go func() { <-b.tunnels[node].jobs; served = true }() // must never fire

	code, out := postConcierge(t, b, "7.7.7.6", "flagged content")
	if code != http.StatusUnavailableForLegalReasons {
		t.Fatalf("flagged grant input = %d, want 451", code)
	}
	if served {
		t.Error("flagged input must NOT reach the grant relay")
	}
	if out["reply"] != "" {
		t.Errorf("flagged input should produce no reply, got %q", out["reply"])
	}
}

// TestConciergeGrantDailyCapGates: the global daily cap still wraps the grant path -
// once spent, every caller gets the airtime-limit reply, even with a grant configured.
func TestConciergeGrantDailyCapGates(t *testing.T) {
	b, _, node := grantConciergeBroker(t, []string{"gpt-oss-120b"})
	b.lastSeen[node] = time.Now()
	b.concierge.dayCap = 1
	answerOnce(t, b.tunnels[node], "served once", nil)

	if code, out := postConcierge(t, b, "7.7.7.7", "hi"); code != http.StatusOK || out["reply"] != "served once" {
		t.Fatalf("first within cap = %d %q", code, out["reply"])
	}
	code, out := postConcierge(t, b, "7.7.7.8", "hi")
	if code != http.StatusOK {
		t.Fatalf("over-cap status = %d, want 200", code)
	}
	if !strings.Contains(out["reply"], "airtime limit") {
		t.Errorf("over-cap reply = %q, want the airtime-limit message (cap must gate the grant path)", out["reply"])
	}
}

// TestConciergeGrantSecretNeverLogged: loadConcierge must announce that grant-dogfood
// is on and which model, but NEVER print the secret.
func TestConciergeGrantSecretNeverLogged(t *testing.T) {
	secret := "rog-grant_topsecretvalue"
	t.Setenv("CONCIERGE_GRANT_KEY", secret)
	t.Setenv("CONCIERGE_MODEL", "gpt-oss-120b")

	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)

	c := loadConcierge()
	if c.grantKey != secret {
		t.Fatalf("grantKey = %q, want it loaded from CONCIERGE_GRANT_KEY", c.grantKey)
	}
	if c.grantModel != "gpt-oss-120b" {
		t.Fatalf("grantModel = %q, want gpt-oss-120b", c.grantModel)
	}
	logs := buf.String()
	if strings.Contains(logs, secret) {
		t.Fatalf("the grant secret was logged: %q", logs)
	}
	if !strings.Contains(logs, "grant-dogfood enabled") || !strings.Contains(logs, "gpt-oss-120b") {
		t.Errorf("expected a grant-dogfood-enabled log naming the model, got: %q", logs)
	}
}

// TestConciergeModelDefault: CONCIERGE_MODEL defaults to gpt-oss-120b when unset.
func TestConciergeModelDefault(t *testing.T) {
	t.Setenv("CONCIERGE_GRANT_KEY", "")
	t.Setenv("CONCIERGE_MODEL", "")
	if c := loadConcierge(); c.grantModel != "gpt-oss-120b" {
		t.Errorf("default grantModel = %q, want gpt-oss-120b", c.grantModel)
	}
}

// TestDogfoodRelayThroughTunnel exercises the REAL dogfoodRelay against a free
// on-air station with a fake polling node, proving the server-side relay path
// (pick free -> enqueue job -> read result -> extract text) works end to end.
func TestDogfoodRelayThroughTunnel(t *testing.T) {
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	b := newConciergeBroker()
	b.priv = brokerPriv

	nodePub, _, _ := ed25519.GenerateKey(nil)
	b.nodes["freebie"] = protocol.NodeRegistration{
		NodeID: "freebie", PubKey: hex.EncodeToString(nodePub),
		Offers: []protocol.ModelOffer{{Model: "free-m"}}, // zero price = free
	}
	tun := &nodeTunnel{jobs: make(chan protocol.Job, 1), waiters: map[string]chan protocol.JobResult{}}
	b.tunnels["freebie"] = tun
	b.lastSeen["freebie"] = time.Now()

	// Fake node poller: take one job and answer with an OpenAI-shaped completion.
	go func() {
		job := <-tun.jobs
		tun.mu.Lock()
		ch := tun.waiters[job.ID]
		tun.mu.Unlock()
		respBody := []byte(`{"choices":[{"message":{"role":"assistant","content":"You're on the air."}}]}`)
		ch <- protocol.JobResult{ID: job.ID, Status: 200, Body: respBody}
	}()

	reply, served := b.dogfoodRelay([]chatMsg{{Role: "user", Content: "hi Ping"}})
	if !served {
		t.Fatal("dogfoodRelay should have served via the free station")
	}
	if reply != "You're on the air." {
		t.Errorf("reply = %q, want the station completion text", reply)
	}
}

// TestPickFreeStation: only an online, free (or zero-priced) station is picked.
func TestPickFreeStation(t *testing.T) {
	b := newConciergeBroker()
	nodePub, _, _ := ed25519.GenerateKey(nil)
	// A priced, online node must NOT be picked.
	b.nodes["paid"] = protocol.NodeRegistration{NodeID: "paid", PubKey: hex.EncodeToString(nodePub), Offers: []protocol.ModelOffer{{Model: "m", PriceOut: 0.5}}}
	b.lastSeen["paid"] = time.Now()
	if _, _, ok := b.pickFreeStation(); ok {
		t.Error("a priced station must not be picked for the free dogfood path")
	}
	// Add a free node -> picked.
	b.nodes["free"] = protocol.NodeRegistration{NodeID: "free", PubKey: hex.EncodeToString(nodePub), Offers: []protocol.ModelOffer{{Model: "free-m"}}}
	b.lastSeen["free"] = time.Now()
	node, model, ok := b.pickFreeStation()
	if !ok || node != "free" || model != "free-m" {
		t.Errorf("pickFreeStation = (%q,%q,%v), want (free,free-m,true)", node, model, ok)
	}
	// An offline free node is skipped.
	b.lastSeen["free"] = time.Now().Add(-time.Minute)
	if _, _, ok := b.pickFreeStation(); ok {
		t.Error("an offline free station must not be picked")
	}
}
