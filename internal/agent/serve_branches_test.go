package agent

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// TestRunReturnsRegisterError: Run surfaces a failed initial registration (it does NOT
// fall through to the block-forever serve loop). Covers Run's error branch.
func TestRunReturnsRegisterError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rejected", http.StatusForbidden)
	}))
	defer broker.Close()
	if err := Run(Config{Broker: broker.URL, NodeID: "n", Model: "m"}); err == nil {
		t.Fatal("Run should return the broker's registration rejection")
	}
}

// TestRunStartsThenBlocks: after a successful Start, Run calls the block-forever seam.
// We substitute the seam with a returning stub so Run's success path is observable
// without hanging; the seam being invoked proves Run reached the serve loop.
func TestRunStartsThenBlocks(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("HOME", tmp)

	var blocked atomic.Bool
	prev := serveForever
	serveForever = func() { blocked.Store(true) }
	defer func() { serveForever = prev }()
	defer swapHeartbeatInterval(time.Hour)() // keep background beats quiet during the test

	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/nodes/register"):
			_ = json.NewEncoder(w).Encode(registerResult{})
		case strings.HasPrefix(r.URL.Path, "/agent/poll"):
			w.WriteHeader(http.StatusNoContent) // idle: no work to serve
		default:
			_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		}
	}))
	defer broker.Close()

	if err := Run(Config{Broker: broker.URL, NodeID: "n-run", Model: "m", Parallel: 1}); err != nil {
		t.Fatalf("Run with a healthy broker should return nil (stubbed serve loop): %v", err)
	}
	if !blocked.Load() {
		t.Error("Run must invoke the block-forever serve loop after a successful Start")
	}
}

// TestStopIsIdempotent: a second Stop after the channel is already closed must not
// panic (covers the already-closed select arm of Stop).
func TestStopIsIdempotent(t *testing.T) {
	s := &Session{stop: make(chan struct{})}
	s.Stop()
	s.Stop() // must be a no-op, not a double-close panic
}

// TestStartConfidentialNoTEEFails: requesting the confidential tier on a host with no
// TEE device makes Start fail at attestation rather than registering with a fake claim.
func TestStartConfidentialNoTEEFails(t *testing.T) {
	if detectTEE() != "" {
		t.Skip("running on real TEE hardware; the no-TEE Start failure is not reachable")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(registerResult{})
	}))
	defer broker.Close()
	_, err := Start(Config{Broker: broker.URL, NodeID: "n", Model: "m", Confidential: true})
	if err == nil {
		t.Fatal("Start must fail when --confidential is requested on a non-TEE host")
	}
	if !strings.Contains(err.Error(), "confidential attestation") {
		t.Errorf("error should name the confidential attestation failure, got %v", err)
	}
}

// TestStartCarriesBandID: when the broker mints a private band, Start adopts the
// returned band_id/band_code/band_display onto the session.
func TestStartCarriesBandID(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("HOME", tmp)
	defer swapHeartbeatInterval(time.Hour)()

	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/nodes/register"):
			_ = json.NewEncoder(w).Encode(registerResult{
				BandID: "band_42", BandCode: "9X7Q-2M4K", BandDisplay: "147.520 MHz",
			})
		case strings.HasPrefix(r.URL.Path, "/agent/poll"):
			w.WriteHeader(http.StatusNoContent)
		default:
			_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		}
	}))
	defer broker.Close()

	sess, err := Start(Config{Broker: broker.URL, NodeID: "n-band", Model: "m", Private: true, Parallel: 1})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer sess.Stop()
	id, code, disp := sess.Band()
	if id != "band_42" || code != "9X7Q-2M4K" || disp != "147.520 MHz" {
		t.Errorf("Band() = %q/%q/%q, want band_42/9X7Q-2M4K/147.520 MHz", id, code, disp)
	}
}

// TestServeUpstreamUnreachable: when the local upstream cannot be reached, serve
// returns a 502 with an "upstream unreachable" error body (and no panic).
func TestServeUpstreamUnreachable(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	// 127.0.0.1:0 is never listening -> immediate connection refused.
	cfg := Config{Upstream: "http://127.0.0.1:0", NodeID: "n", Model: "m"}
	res := serve(cfg, protocol.ModelOffer{}, priv, &http.Client{Timeout: time.Second},
		protocol.Job{ID: "j", Body: json.RawMessage(`{"model":"m"}`)})
	if res.Status != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 when upstream is unreachable", res.Status)
	}
	if !strings.Contains(string(res.Body), "upstream unreachable") {
		t.Errorf("body = %s, want an 'upstream unreachable' error", res.Body)
	}
}

// TestServeStreamUpstreamUnreachable: a streaming job whose upstream is unreachable
// posts a 502 result to the broker and returns an empty (un-metered) receipt.
func TestServeStreamUpstreamUnreachable(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	var posted atomic.Int64
	var gotStatus atomic.Int64
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/agent/result") {
			body, _ := io.ReadAll(r.Body)
			var jr protocol.JobResult
			_ = json.Unmarshal(body, &jr)
			gotStatus.Store(int64(jr.Status))
			posted.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer broker.Close()

	cfg := Config{Upstream: "http://127.0.0.1:0", Broker: broker.URL, NodeID: "n", Model: "m"}
	rec := serveStream(cfg, protocol.ModelOffer{}, priv, "tok",
		protocol.Job{ID: "j", Body: json.RawMessage(`{"model":"m","stream":true}`)})
	if rec.RequestID != "" {
		t.Errorf("receipt should be empty when the stream upstream fails, got %+v", rec)
	}
	if posted.Load() != 1 {
		t.Fatalf("expected exactly one result POST, got %d", posted.Load())
	}
	if gotStatus.Load() != http.StatusBadGateway {
		t.Errorf("posted status = %d, want 502", gotStatus.Load())
	}
}

// TestRegisterTransportError: register surfaces a transport error (broker host not
// listening) rather than masking it as success.
func TestRegisterTransportError(t *testing.T) {
	_, err := register("http://127.0.0.1:0", protocol.NodeRegistration{NodeID: "n"})
	if err == nil {
		t.Fatal("register must return an error when the broker is unreachable")
	}
}

// TestLoadOrCreateKeyReloadsExisting: a valid persisted node key is reloaded verbatim
// (the key is stable across restarts), not regenerated.
func TestLoadOrCreateKeyReloadsExisting(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("HOME", tmp)

	dir, err := os.UserConfigDir()
	if err != nil {
		t.Fatalf("UserConfigDir: %v", err)
	}
	_, priv, _ := ed25519.GenerateKey(nil)
	keyDir := filepath.Join(dir, "rogerai")
	if err := os.MkdirAll(keyDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(keyDir, "node.key"), []byte(hex.EncodeToString(priv)), 0600); err != nil {
		t.Fatal(err)
	}

	got := loadOrCreateKey()
	if hex.EncodeToString(got) != hex.EncodeToString(priv) {
		t.Error("loadOrCreateKey must reload the persisted key verbatim, not regenerate it")
	}
}

// TestPollLoopServesStreamJob drives the real pollLoop down the streaming branch: the
// broker hands out one stream:true job, the worker relays the SSE to /agent/stream and
// folds the metered usage into the session counters.
func TestPollLoopServesStreamJob(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n")
		io.WriteString(w, "data: {\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":9}}\n")
		io.WriteString(w, "data: [DONE]\n")
	}))
	defer upstream.Close()

	var polls atomic.Int64
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/agent/poll"):
			if polls.Add(1) == 1 {
				_ = json.NewEncoder(w).Encode(protocol.Job{
					ID: "stream-job", User: "u", Body: json.RawMessage(`{"model":"m","stream":true}`),
				})
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default: // /agent/stream and anything else
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer broker.Close()

	_, priv, _ := ed25519.GenerateKey(nil)
	cfg := Config{Broker: broker.URL, Upstream: upstream.URL, NodeID: "n", Model: "m", PriceIn: 1, PriceOut: 2}
	rr := newReregistrar(broker.URL, protocol.NodeRegistration{NodeID: "n", BridgeToken: "tok"}, priv)
	sess := &Session{cfg: cfg, stop: make(chan struct{}), rereg: rr}
	go pollLoop(cfg, protocol.ModelOffer{PriceIn: 1, PriceOut: 2}, priv, sess)
	defer sess.Stop()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if reqs, toks := sess.Served(); reqs >= 1 && toks >= 9 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	reqs, toks := sess.Served()
	if reqs < 1 || toks != 9 {
		t.Fatalf("Served = %d reqs / %d toks, want 1 / 9 from the streamed usage chunk", reqs, toks)
	}
}

// TestPollLoopRetriesOnServerError drives pollLoop against a broker that returns a
// non-OK, non-204, non-forgot status (500): the worker closes the body and retries
// rather than re-registering or crashing.
func TestPollLoopRetriesOnServerError(t *testing.T) {
	var hits atomic.Int64
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer broker.Close()

	_, priv, _ := ed25519.GenerateKey(nil)
	cfg := Config{Broker: broker.URL, NodeID: "n", Model: "m"}
	rr := newReregistrar(broker.URL, protocol.NodeRegistration{NodeID: "n", BridgeToken: "tok"}, priv)
	sess := &Session{cfg: cfg, stop: make(chan struct{}), rereg: rr}
	go pollLoop(cfg, protocol.ModelOffer{}, priv, sess)
	defer sess.Stop()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if hits.Load() >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if hits.Load() < 1 {
		t.Fatal("pollLoop never reached the broker (500 retry branch not exercised)")
	}
	// No re-register: a 500 is a transient retry, not a 'broker forgot us'. The token
	// (and generation) must be unchanged.
	if _, gen := rr.curToken(); gen != 0 {
		t.Errorf("a 500 must not trigger re-registration; generation = %d, want 0", gen)
	}
}

// TestReregisterStopBeforeLoop: recover() entered with stop already closed returns at
// the FIRST loop guard without performing any registration.
func TestReregisterStopBeforeLoop(t *testing.T) {
	var regs atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		regs.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	rr := newTestReregistrar(srv.URL)
	_, gen0 := rr.curToken()
	stop := make(chan struct{})
	close(stop) // already stopped before recover runs

	rr.recover(gen0, stop)
	if regs.Load() != 0 {
		t.Errorf("recover must not register when stop is already closed, got %d registers", regs.Load())
	}
	if _, gen1 := rr.curToken(); gen1 != gen0 {
		t.Errorf("generation must not advance on the stop path, got %d", gen1)
	}
}

// TestReregisterConfidentialDropsClaimNoTEE: a confidential node re-registering on a
// host that lost (or never had) its TEE drops the confidential claim for that round
// rather than replaying a stale quote, and still re-registers successfully.
func TestReregisterConfidentialDropsClaimNoTEE(t *testing.T) {
	if detectTEE() != "" {
		t.Skip("running on real TEE hardware; the dropped-claim path is not reachable")
	}
	var sawConfidential atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/nodes/register") {
			body, _ := io.ReadAll(r.Body)
			var reg protocol.NodeRegistration
			_ = json.Unmarshal(body, &reg)
			if reg.Confidential {
				sawConfidential.Store(true)
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, priv, _ := ed25519.GenerateKey(nil)
	reg := protocol.NodeRegistration{NodeID: "n1", BridgeToken: "tok0", Confidential: true}
	reg.SignRegistration(priv)
	rr := newReregistrar(srv.URL, reg, priv)
	_, gen0 := rr.curToken()
	stop := make(chan struct{})

	rr.recover(gen0, stop)
	if _, gen1 := rr.curToken(); gen1 != gen0+1 {
		t.Errorf("re-register should succeed (as standard) even without a TEE; gen=%d", gen1)
	}
	if sawConfidential.Load() {
		t.Error("a no-TEE re-register must DROP the confidential claim, not send a stale one")
	}
}

// TestHeartbeatServerErrorIsReconnecting: a heartbeat that gets a non-OK, non-forgot
// status (500) drives the link to RECONNECTING via the default arm (the broker is up
// but not acknowledging us as on-air).
func TestHeartbeatServerErrorIsReconnecting(t *testing.T) {
	defer swapHeartbeatInterval(20 * time.Millisecond)()
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "server error", http.StatusInternalServerError)
	}))
	defer broker.Close()

	_, priv, _ := ed25519.GenerateKey(nil)
	rr := newReregistrar(broker.URL, protocol.NodeRegistration{NodeID: "n", BridgeToken: "tok"}, priv)
	sess := &Session{stop: make(chan struct{})}
	go heartbeatUntil(broker.URL, "n", rr, sess)
	defer sess.Stop()

	waitLink(t, sess, LinkReconnecting)
}
