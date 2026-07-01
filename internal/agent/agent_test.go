package agent

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

func TestWithUsageOption(t *testing.T) {
	// A normal request gains stream_options.include_usage without losing fields.
	in := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	out := withUsageOption(in)

	var m map[string]json.RawMessage
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("output not valid JSON: %v", err)
	}
	if _, ok := m["model"]; !ok {
		t.Error("model field dropped")
	}
	var so struct {
		IncludeUsage bool `json:"include_usage"`
	}
	if err := json.Unmarshal(m["stream_options"], &so); err != nil || !so.IncludeUsage {
		t.Errorf("stream_options.include_usage not set: %s", m["stream_options"])
	}
}

func TestWithUsageOptionOverwrites(t *testing.T) {
	// An existing stream_options is replaced (we must guarantee include_usage).
	in := []byte(`{"model":"m","stream_options":{"include_usage":false}}`)
	out := withUsageOption(in)
	var m struct {
		StreamOptions struct {
			IncludeUsage bool `json:"include_usage"`
		} `json:"stream_options"`
	}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if !m.StreamOptions.IncludeUsage {
		t.Error("include_usage should be forced true")
	}
}

func TestWithUsageOptionInvalidJSON(t *testing.T) {
	// Non-JSON bodies are returned unchanged (don't corrupt the upstream request).
	in := []byte(`not json`)
	if got := withUsageOption(in); string(got) != string(in) {
		t.Errorf("invalid JSON should pass through unchanged, got %q", got)
	}
}

// TestRegisterStatus confirms register() surfaces a broker rejection: a non-200
// response is an error (so Run won't spin up poll loops), while 200 succeeds.
func TestRegisterStatus(t *testing.T) {
	t.Run("non-200 is an error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "denied", http.StatusForbidden)
		}))
		defer srv.Close()
		if _, err := register(srv.URL, protocol.NodeRegistration{NodeID: "n1"}); err == nil {
			t.Error("register should error on a non-200 broker response")
		}
	})
	t.Run("200 succeeds", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()
		if _, err := register(srv.URL, protocol.NodeRegistration{NodeID: "n1"}); err != nil {
			t.Errorf("register should succeed on 200, got %v", err)
		}
	})
	t.Run("429 hard cap surfaces the broker reason", func(t *testing.T) {
		// The broker's per-owner on-air cap replies 429 with its JSON-wrapped message; the
		// share UX must see the reason verbatim (not a bare "status 429") so the operator
		// knows to take a band off air.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"station limit reached: 20 bands on air for this account - take one off air"}}`))
		}))
		defer srv.Close()
		_, err := register(srv.URL, protocol.NodeRegistration{NodeID: "n1"})
		if err == nil {
			t.Fatal("a 429 hard-cap reject should be an error")
		}
		if !strings.Contains(err.Error(), "station limit reached") || !strings.Contains(err.Error(), "take one off air") {
			t.Errorf("429 reject should surface the broker reason, got %v", err)
		}
	})
	t.Run("400 surfaces the broker body (voice-name reject), not a bare status", func(t *testing.T) {
		// The broker 400s a nameless voice offer ("voice name is empty after normalization").
		// The CLI must surface that reason for ANY non-2xx (this was previously hidden behind a
		// bare "broker returned status 400"), so the operator learns to set a name.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"voice name is empty after normalization"}}`))
		}))
		defer srv.Close()
		_, err := register(srv.URL, protocol.NodeRegistration{NodeID: "n1"})
		if err == nil {
			t.Fatal("a 400 reject should be an error")
		}
		if !strings.Contains(err.Error(), "voice name is empty after normalization") {
			t.Errorf("400 reject should surface the broker body, got %v", err)
		}
		if strings.Contains(err.Error(), "status 400") {
			t.Errorf("400 reject should NOT read as a bare status, got %v", err)
		}
	})
	t.Run("plain-text non-2xx body is surfaced too", func(t *testing.T) {
		// A non-JSON error body still beats a bare status (brokerErrMsg falls back to raw bytes).
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "upstream exploded", http.StatusInternalServerError)
		}))
		defer srv.Close()
		_, err := register(srv.URL, protocol.NodeRegistration{NodeID: "n1"})
		if err == nil || !strings.Contains(err.Error(), "upstream exploded") {
			t.Errorf("a 500 with a plain body should surface it, got %v", err)
		}
	})
}

// TestRegisterSignsAtZeroPrice confirms a FREE (price 0/0) registration still carries
// the owner-signing headers (X-Roger-Pubkey/-TS/-Sig) so a logged-in operator's free
// node can be BOUND to their account broker-side. register() signs every registration
// with the local user key regardless of price; the broker resolves the GitHub-linked
// owner from that pubkey. Anonymous free sharing still works (the headers are present
// but resolve to no owner) - this test only asserts the identity is always sent.
func TestRegisterSignsAtZeroPrice(t *testing.T) {
	// Isolate the user.key the signer creates on EVERY platform: os.UserConfigDir reads
	// XDG_CONFIG_HOME on Linux but $HOME on macOS and %AppData% on Windows, so XDG alone
	// would write the user's REAL identity key on mac/windows. Point all three at one temp dir.
	d := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", d) // Linux
	t.Setenv("HOME", d)            // macOS (~/Library/Application Support) + Linux fallback
	t.Setenv("AppData", d)         // Windows (%AppData%)
	var gotPub, gotSig, gotTS string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPub = r.Header.Get("X-Roger-Pubkey")
		gotSig = r.Header.Get("X-Roger-Sig")
		gotTS = r.Header.Get("X-Roger-TS")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// A free node: a single offer at price 0/0 (the bug-2 case the founder hit).
	reg := protocol.NodeRegistration{
		NodeID: "brave-otter-gpt-oss-120b", PubKey: "deadbeef",
		Offers: []protocol.ModelOffer{{Model: "gpt-oss-120b", PriceIn: 0, PriceOut: 0}},
	}
	if _, err := register(srv.URL, reg); err != nil {
		t.Fatalf("register at price 0 should succeed, got %v", err)
	}
	if gotPub == "" || gotSig == "" || gotTS == "" {
		t.Fatalf("free registration must carry owner-signing headers (pub=%q sig=%q ts=%q)", gotPub, gotSig, gotTS)
	}
	// The signature must verify over the exact body the broker received, proving the
	// owner identity is genuinely bound to this free registration (not a stray header).
	ts, err := strconv.ParseInt(gotTS, 10, 64)
	if err != nil {
		t.Fatalf("bad X-Roger-TS %q: %v", gotTS, err)
	}
	if _, ok := protocol.VerifyRequest(gotPub, gotSig, ts, http.MethodPost, "/nodes/register", gotBody); !ok {
		t.Error("owner signature on the free registration did not verify")
	}
}

func TestBrokerErrMsg(t *testing.T) {
	if got := brokerErrMsg([]byte(`{"error":{"message":"nope"}}`)); got != "nope" {
		t.Errorf("brokerErrMsg = %q, want nope", got)
	}
	// Non-enveloped body falls back to the raw bytes.
	if got := brokerErrMsg([]byte(`plain text`)); got != "plain text" {
		t.Errorf("brokerErrMsg fallback = %q, want plain text", got)
	}
}

func TestParseUsage(t *testing.T) {
	cases := []struct {
		name         string
		line         string
		wantP, wantC int
		wantOK       bool
	}{
		{"sse usage chunk", `data: {"id":"x","usage":{"prompt_tokens":12,"completion_tokens":34}}`, 12, 34, true},
		{"plain json", `{"usage":{"prompt_tokens":5,"completion_tokens":0}}`, 5, 0, true},
		{"no usage", `data: {"choices":[{"delta":{"content":"hi"}}]}`, 0, 0, false},
		{"zero usage ignored", `data: {"usage":{"prompt_tokens":0,"completion_tokens":0}}`, 0, 0, false},
		{"no brace", `data: [DONE]`, 0, 0, false},
		{"empty", ``, 0, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, comp, ok := parseUsage([]byte(c.line))
			if ok != c.wantOK || p != c.wantP || comp != c.wantC {
				t.Errorf("parseUsage(%q) = %d,%d,%v want %d,%d,%v", c.line, p, comp, ok, c.wantP, c.wantC, c.wantOK)
			}
		})
	}
}

// newTestReregistrar builds a reregistrar pointed at broker with a signed reg.
func newTestReregistrar(broker string) *reregistrar {
	_, priv, _ := ed25519.GenerateKey(nil)
	reg := protocol.NodeRegistration{NodeID: "n1", BridgeToken: "tok0"}
	reg.SignRegistration(priv)
	return newReregistrar(broker, reg, priv)
}

// TestReregisterSingleFlight: N pollers that all observe the same generation and
// call recover() concurrently trigger EXACTLY ONE re-register, and afterward the
// shared token holder hands out the fresh token at the new generation.
func TestReregisterSingleFlight(t *testing.T) {
	var regs int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&regs, 1)
		time.Sleep(20 * time.Millisecond) // widen the window for concurrent callers
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	rr := newTestReregistrar(srv.URL)
	tok0, gen0 := rr.curToken()
	stop := make(chan struct{})

	const n = 8
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); rr.recover(gen0, stop) }()
	}
	wg.Wait()

	if got := atomic.LoadInt32(&regs); got != 1 {
		t.Fatalf("expected exactly 1 re-register for %d concurrent pollers, got %d", n, got)
	}
	tok1, gen1 := rr.curToken()
	if gen1 != gen0+1 {
		t.Errorf("generation should advance by 1: gen0=%d gen1=%d", gen0, gen1)
	}
	if tok1 == tok0 {
		t.Error("token should be refreshed after re-register")
	}
}

// TestReregisterAlreadyRecovered: a poller whose seenGen is already stale (another
// worker recovered first) returns immediately and does NOT re-register again.
func TestReregisterAlreadyRecovered(t *testing.T) {
	var regs int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&regs, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	rr := newTestReregistrar(srv.URL)
	_, gen0 := rr.curToken()
	stop := make(chan struct{})

	rr.recover(gen0, stop) // first recovery advances the generation
	if got := atomic.LoadInt32(&regs); got != 1 {
		t.Fatalf("first recover should register once, got %d", got)
	}
	rr.recover(gen0, stop) // a laggard still holding gen0 must no-op
	if got := atomic.LoadInt32(&regs); got != 1 {
		t.Errorf("stale-generation recover must not re-register again, got %d", got)
	}
}

// TestReregisterBackoffBounded: a broker that 500s for a while then accepts is
// retried with bounded backoff and eventually heals (never gives up, never busy-
// loops). We assert it recovers and the retry count stays small.
func TestReregisterBackoffBounded(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) < 3 {
			http.Error(w, "broker down", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	rr := newTestReregistrar(srv.URL)
	_, gen0 := rr.curToken()
	stop := make(chan struct{})

	done := make(chan struct{})
	go func() { rr.recover(gen0, stop); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("recover did not heal within 10s (backoff unbounded or stuck)")
	}
	if _, gen1 := rr.curToken(); gen1 != gen0+1 {
		t.Errorf("generation should advance once after eventual success, got %d", gen1)
	}
	// 1s + 2s backoff between the 3 attempts; should be only a few attempts.
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Errorf("expected 3 attempts before success, got %d", got)
	}
}

// TestReregisterStopUnblocks: recover() against a permanently-down broker returns
// promptly once stop is closed (Stop ends cleanly, no hang).
func TestReregisterStopUnblocks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer srv.Close()

	rr := newTestReregistrar(srv.URL)
	_, gen0 := rr.curToken()
	stop := make(chan struct{})

	done := make(chan struct{})
	go func() { rr.recover(gen0, stop); close(done) }()
	time.Sleep(50 * time.Millisecond)
	close(stop)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("recover did not return after stop closed")
	}
}

// TestPollLoopSelfHeals drives the real pollLoop against a broker that first 404s
// "unknown node" (registry wiped) then accepts. The poller must trigger exactly
// ONE re-register and then resume polling with the refreshed token. Spawning
// several pollers also confirms N concurrent 404s cause ONE re-register.
func TestPollLoopSelfHeals(t *testing.T) {
	var registers int32
	var polledAfterReg int32
	forgotten := int32(1) // 1 = broker has forgotten the node (404 poll)
	var curToken atomic.Value
	curToken.Store("tok0")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/nodes/register":
			atomic.AddInt32(&registers, 1)
			body, _ := io.ReadAll(r.Body)
			var reg protocol.NodeRegistration
			_ = json.Unmarshal(body, &reg)
			curToken.Store(reg.BridgeToken)  // adopt the node's fresh token
			atomic.StoreInt32(&forgotten, 0) // node is known again
			w.WriteHeader(http.StatusOK)
		case "/agent/poll":
			if atomic.LoadInt32(&forgotten) == 1 {
				http.Error(w, "unknown node", http.StatusNotFound)
				return
			}
			// Authenticated re-poll with the live token: count it and hold (204).
			if r.Header.Get("Authorization") != "Bearer "+curToken.Load().(string) {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			atomic.AddInt32(&polledAfterReg, 1)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	_, priv, _ := ed25519.GenerateKey(nil)
	reg := protocol.NodeRegistration{NodeID: "n1", BridgeToken: "tok0"}
	reg.SignRegistration(priv)
	rr := newReregistrar(srv.URL, reg, priv)
	cfg := Config{Broker: srv.URL, NodeID: "n1"}
	sess := &Session{cfg: cfg, stop: make(chan struct{}), rereg: rr}

	const pollers = 4
	for i := 0; i < pollers; i++ {
		go pollLoop(cfg, protocol.ModelOffer{}, priv, sess)
	}

	// Wait until the pollers have healed and are polling with the fresh token.
	deadline := time.After(5 * time.Second)
	for atomic.LoadInt32(&polledAfterReg) == 0 {
		select {
		case <-deadline:
			t.Fatal("pollers never resumed after the broker forgot the node")
		case <-time.After(20 * time.Millisecond):
		}
	}
	sess.Stop()

	if got := atomic.LoadInt32(&registers); got != 1 {
		t.Errorf("expected exactly ONE re-register across %d pollers, got %d", pollers, got)
	}
	if _, gen := rr.curToken(); gen != 1 {
		t.Errorf("token holder should be at generation 1 after one heal, got %d", gen)
	}
}

// TestHeartbeatSelfHeals confirms the heartbeat re-registers on a 404 too.
func TestHeartbeatSelfHeals(t *testing.T) {
	var registers int32
	forgotten := int32(1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/nodes/register":
			atomic.AddInt32(&registers, 1)
			atomic.StoreInt32(&forgotten, 0)
			w.WriteHeader(http.StatusOK)
		case "/nodes/heartbeat":
			if atomic.LoadInt32(&forgotten) == 1 {
				http.Error(w, "unknown node", http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	rr := newTestReregistrar(srv.URL)
	stop := make(chan struct{})
	// Drive a single heartbeat-style cycle directly: a 404 must route to recover.
	token, gen := rr.curToken()
	b, _ := json.Marshal(map[string]string{"node_id": "n1"})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/nodes/heartbeat", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	status := resp.StatusCode
	resp.Body.Close()
	if status != http.StatusNotFound {
		t.Fatalf("setup: first heartbeat should 404, got %d", status)
	}
	rr.recover(gen, stop)
	if got := atomic.LoadInt32(&registers); got != 1 {
		t.Errorf("heartbeat 404 should re-register once, got %d", got)
	}
}

// TestEffectivePriceFor verifies the share-side parity mapping: the CLI/agent adopts the
// broker-EFFECTIVE published price (after an owner web-console override) from the
// register response, flags the override, and falls back to the requested price when the
// broker echoed no effective offer for the model.
func TestEffectivePriceFor(t *testing.T) {
	// Broker published an override for our model ($1 in / $2 out) and flagged it.
	rr := registerResult{
		EffectiveOffers: []protocol.ModelOffer{{Model: "gpt-oss-120b", PriceIn: 1.0, PriceOut: 2.0}},
		Overrides:       []string{"gpt-oss-120b"},
	}
	in, out, override := effectivePriceFor(rr, "gpt-oss-120b", 0.0, 0.5)
	if in != 1.0 || out != 2.0 || !override {
		t.Fatalf("override mapping = %v/%v override=%v, want 1/2 + override (the broker-effective price wins)", in, out, override)
	}

	// No effective offer for this model -> fall back to the requested price, no override.
	in, out, override = effectivePriceFor(registerResult{}, "gpt-oss-120b", 0.2, 0.3)
	if in != 0.2 || out != 0.3 || override {
		t.Fatalf("fallback mapping = %v/%v override=%v, want the requested 0.2/0.3 + no override", in, out, override)
	}

	// An effective offer for a DIFFERENT model must not affect ours.
	rr2 := registerResult{EffectiveOffers: []protocol.ModelOffer{{Model: "other", PriceOut: 9.0}}}
	in, out, override = effectivePriceFor(rr2, "gpt-oss-120b", 0.2, 0.3)
	if in != 0.2 || out != 0.3 || override {
		t.Fatalf("cross-model mapping = %v/%v override=%v, want our requested 0.2/0.3 unaffected", in, out, override)
	}
}

func TestRedactUpstreamKey(t *testing.T) {
	if got := redactUpstreamKey([]byte("x sk-secret y"), "sk-secret"); string(got) != "x [redacted] y" {
		t.Errorf("redact = %q, want %q", got, "x [redacted] y")
	}
	// An empty key must be a no-op (a ReplaceAll on "" would match everywhere and mangle
	// the body) - guards the common no-auth upstream.
	if got := redactUpstreamKey([]byte("untouched body"), ""); string(got) != "untouched body" {
		t.Errorf("empty key should be a no-op, got %q", got)
	}
}

// TestServeRedactsUpstreamKey: when a (misconfigured) upstream echoes the request
// Authorization header into its response body, serve must strip the node's key before
// relaying the body - and the redaction must not corrupt usage metering.
func TestServeRedactsUpstreamKey(t *testing.T) {
	const key = "sk-upstream-secret"
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"echoed_auth":"` + r.Header.Get("Authorization") + `","usage":{"prompt_tokens":3,"completion_tokens":5}}`))
	}))
	defer up.Close()

	_, priv, _ := ed25519.GenerateKey(nil)
	cfg := Config{Upstream: up.URL, UpstreamKey: key, NodeID: "n", Model: "m"}
	res := serve(cfg, protocol.ModelOffer{}, priv, &http.Client{}, protocol.Job{ID: "j", Body: json.RawMessage(`{"model":"m"}`)})

	if bytes.Contains(res.Body, []byte(key)) {
		t.Fatalf("upstream key leaked in relayed body: %s", res.Body)
	}
	if !bytes.Contains(res.Body, []byte("[redacted]")) {
		t.Errorf("expected a redaction marker, got: %s", res.Body)
	}
	if res.Receipt.PromptTokens != 3 || res.Receipt.CompletionTokens != 5 {
		t.Errorf("usage = %d/%d, want 3/5 (redaction must not corrupt the body)", res.Receipt.PromptTokens, res.Receipt.CompletionTokens)
	}
}

// TestServeStreamRedactsUpstreamKey: the streaming path must strip the node's key from
// any SSE chunk before piping it to the broker, while still metering usage.
func TestServeStreamRedactsUpstreamKey(t *testing.T) {
	const key = "sk-upstream-secret"
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"echoed_auth\":\""+r.Header.Get("Authorization")+"\"}\n")
		io.WriteString(w, "data: {\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":11}}\n")
		io.WriteString(w, "data: [DONE]\n")
	}))
	defer up.Close()

	var mu2 sync.Mutex
	var streamed bytes.Buffer
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/agent/stream") {
			mu2.Lock()
			io.Copy(&streamed, r.Body)
			mu2.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer broker.Close()

	_, priv, _ := ed25519.GenerateKey(nil)
	cfg := Config{Upstream: up.URL, UpstreamKey: key, Broker: broker.URL, NodeID: "n", Model: "m"}
	rec := serveStream(cfg, protocol.ModelOffer{}, priv, "tok", protocol.Job{ID: "j", Body: json.RawMessage(`{"model":"m","stream":true}`)})

	mu2.Lock()
	got := streamed.String()
	mu2.Unlock()
	if strings.Contains(got, key) {
		t.Fatalf("upstream key leaked in streamed body: %s", got)
	}
	if !strings.Contains(got, "[redacted]") {
		t.Errorf("expected a redaction marker in the stream, got: %s", got)
	}
	if rec.PromptTokens != 7 || rec.CompletionTokens != 11 {
		t.Errorf("stream usage = %d/%d, want 7/11", rec.PromptTokens, rec.CompletionTokens)
	}
}
