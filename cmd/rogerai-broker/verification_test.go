package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

func newTrustBroker() *broker {
	return &broker{
		nodes:        map[string]protocol.NodeRegistration{},
		tunnels:      map[string]*nodeTunnel{},
		lastSeen:     map[string]time.Time{},
		confidential: map[string]bool{},
		attestedAt:   map[string]time.Time{},
		tps:          map[string]float64{},
		inflight:     map[string]int{},
		success:      map[string]float64{},
		trust:        map[string]trustState{},
	}
}

// TestObserveRecountTolerance: an EXACT re-count flags a discrepancy only when
// the node's claimed completion tokens exceed our count by MORE than the
// tolerance band; within-band (or under-reporting) does not flag.
func TestObserveRecountTolerance(t *testing.T) {
	b := newTrustBroker()
	b.recount = recountConfig{tolerance: 0.02}

	// Within 2%: 102 claimed vs 100 recount -> no discrepancy.
	b.observeRecount("n", "", 102, 100, true)
	if d := b.trust["n"].discrepancies; d != 0 {
		t.Errorf("within-band discrepancies = %d, want 0", d)
	}

	// Over 2%: 110 claimed vs 100 recount -> flagged.
	b.observeRecount("n", "", 110, 100, true)
	if d := b.trust["n"].discrepancies; d != 1 {
		t.Errorf("over-band discrepancies = %d, want 1", d)
	}

	// Under-reporting never flags (the node only hurts itself).
	b.observeRecount("n", "", 50, 100, true)
	if d := b.trust["n"].discrepancies; d != 1 {
		t.Errorf("under-report discrepancies = %d, want still 1", d)
	}

	// A HEURISTIC (non-exact) re-count never flags, even far over band.
	before := b.trust["n"].discrepancies
	b.observeRecount("n", "", 1000, 100, false)
	if d := b.trust["n"].discrepancies; d != before {
		t.Errorf("heuristic re-count flagged a discrepancy (%d->%d), want no change", before, d)
	}

	// Trust score dropped below 1 after a discrepancy.
	if s := b.trustScore("n"); s >= 1.0 {
		t.Errorf("trustScore = %v, want <1 after a discrepancy", s)
	}
}

// TestRecountAsyncDisabled: with no TOKENIZER_URL the re-count is a no-op and
// never touches trust state (settlement path stays untouched).
func TestRecountAsyncDisabled(t *testing.T) {
	b := newTrustBroker()
	b.recount = recountConfig{} // disabled (url == "")
	b.recountAsync("n", "gpt-4o", "some completion text", 100)
	if _, ok := b.trust["n"]; ok {
		t.Errorf("disabled re-count must not record trust state")
	}
}

// TestRecountAsyncViaSidecar: end-to-end against a stub sidecar - the broker
// posts the completion text, gets an exact count back, and reconciles it.
func TestRecountAsyncViaSidecar(t *testing.T) {
	var gotModel, gotText string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/count" {
			http.NotFound(w, r)
			return
		}
		var req struct{ Model, Text string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotModel, gotText = req.Model, req.Text
		// Pretend the true count is 100, far below a claimed 200.
		_ = json.NewEncoder(w).Encode(map[string]any{"tokens": 100, "exact": true})
	}))
	defer srv.Close()

	b := newTrustBroker()
	b.recount = loadRecountWith(srv.URL, 0.02)
	b.recountAsync("n", "gpt-4o", "hello", 200)

	if gotModel != "gpt-4o" || gotText != "hello" {
		t.Errorf("sidecar got model=%q text=%q, want gpt-4o/hello", gotModel, gotText)
	}
	if d := b.trust["n"].discrepancies; d != 1 {
		t.Errorf("discrepancies = %d, want 1 (200 claimed vs 100 recount)", d)
	}
}

// loadRecountWith builds a recountConfig for tests (loadRecount reads env).
func loadRecountWith(url string, tol float64) recountConfig {
	return recountConfig{url: url, tolerance: tol, client: &http.Client{Timeout: 2 * time.Second}}
}

// sidecarServer returns an httptest server that always answers /count with the given
// (tokens, exact). Each test phase uses its OWN fixed-value server so no handler vars
// are mutated while an in-flight request reads them.
func sidecarServer(tokens int, exact bool) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"tokens": tokens, "exact": exact})
	}))
}

// TestSettleRecountCapsInflatedCompletion locks P0-2 (a): when the broker's EXACT
// re-count is lower than the node's claimed completion tokens, settleRecount returns
// the smaller (verified) count to bill on; an under-reported / heuristic count is never
// inflated; a disabled re-count bills the claim unchanged. It ALSO flags the node for a
// promotion hold when the claim is over-reported past tolerance. Each phase uses a fresh
// broker + server so the goroutine-spawned observeRecount never races a config rewrite.
func TestSettleRecountCapsInflatedCompletion(t *testing.T) {
	// CAP: node claims 500 but the broker re-counts 100 (exact) -> bill 100, and the
	// over-reporting node is flagged for a promotion hold.
	t.Run("cap+hold", func(t *testing.T) {
		srv := sidecarServer(100, true)
		defer srv.Close()
		mem := store.NewMem()
		b := newTrustBroker()
		b.db = mem
		b.recount = loadRecountWith(srv.URL, 0.02)
		if got := b.settleRecount("n", "rq", "gpt-4o", "some completion", 500); got != 100 {
			t.Errorf("billed completion = %d, want 100 (min of claim 500 / recount 100)", got)
		}
		waitFor(t, func() bool { held, _ := mem.RecountHeldNodes(); return held["n"] })
	})

	// UNDER-REPORT: claims 80, recount 100 -> never inflate, bill 80.
	t.Run("under-report", func(t *testing.T) {
		srv := sidecarServer(100, true)
		defer srv.Close()
		b := newTrustBroker()
		b.db = store.NewMem()
		b.recount = loadRecountWith(srv.URL, 0.02)
		if got := b.settleRecount("n", "rq", "gpt-4o", "x", 80); got != 80 {
			t.Errorf("under-report billed = %d, want 80 (never inflate the claim)", got)
		}
	})

	// HEURISTIC (non-exact) count never caps, even far below the claim.
	t.Run("heuristic", func(t *testing.T) {
		srv := sidecarServer(10, false)
		defer srv.Close()
		b := newTrustBroker()
		b.db = store.NewMem()
		b.recount = loadRecountWith(srv.URL, 0.02)
		if got := b.settleRecount("n", "rq", "gpt-4o", "x", 300); got != 300 {
			t.Errorf("heuristic billed = %d, want 300 (too coarse to bill on)", got)
		}
	})

	// DISABLED re-count bills the claim unchanged (no sidecar call at all).
	t.Run("disabled", func(t *testing.T) {
		b := newTrustBroker()
		b.db = store.NewMem()
		b.recount = recountConfig{}
		if got := b.settleRecount("n", "rq", "gpt-4o", "x", 777); got != 777 {
			t.Errorf("disabled re-count billed = %d, want 777 (claim unchanged)", got)
		}
	})
}

// waitFor polls cond up to ~1s (the goroutine-fed promotion-hold flag).
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}

// TestCompletionText extracts the assistant content from a chat-completions body.
func TestCompletionText(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"content":"hello there"}}],"usage":{"completion_tokens":3}}`)
	if got := completionText(body); got != "hello there" {
		t.Errorf("completionText = %q, want 'hello there'", got)
	}
	// legacy completion shape (choices[].text)
	if got := completionText([]byte(`{"choices":[{"text":"abc"}]}`)); got != "abc" {
		t.Errorf("completionText(text) = %q, want abc", got)
	}
	if got := completionText([]byte(`not json`)); got != "" {
		t.Errorf("completionText(bad) = %q, want empty", got)
	}
}

// TestSSEDeltaCapture: drainSSEDeltas reconstructs the completion text from the
// streamed SSE chunks, including a line split across two reads.
func TestSSEDeltaCapture(t *testing.T) {
	sink := &streamSink{cap: &bytes.Buffer{}}
	// Two complete chunks then a partial line, then its completion.
	feed := func(s string) {
		sink.capRaw.WriteString(s)
		drainSSEDeltas(&sink.capRaw, sink.cap)
	}
	feed("data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n")
	feed("data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n")
	feed("data: {\"choices\":[{\"delta\":{\"content") // partial line, no newline yet
	feed("\":\" world\"}}]}\ndata: [DONE]\n")         // completes the line + DONE sentinel
	if got := sink.cap.String(); got != "Hello world" {
		t.Errorf("captured = %q, want 'Hello world'", got)
	}
}

// TestEvalCanary: the probe SEPARATES liveness from the fingerprint. A 2xx with
// non-empty content is ALIVE (verified, not a failure) whether or not the literal
// fingerprint lands - this is the core fix for reasoning models. Only a non-2xx, an
// empty body, or a clearly wrong-FAMILY answer (a different canary's token) fails.
func TestEvalCanary(t *testing.T) {
	b := newTrustBroker()
	fp := canaryFingerprint{prompt: "Reply with only the single word: BANANA", expect: "banana"}
	eval := func(body string, status int) (probeOutcome, float64, bool) {
		res := protocol.JobResult{Status: status, Body: json.RawMessage(body), Receipt: protocol.UsageReceipt{CompletionTokens: 1}}
		return b.evalCanary(res, 50*time.Millisecond, fp)
	}

	// Clean fingerprint extraction => probePass (a strong positive), matched=true.
	if o, _, m := eval(`{"choices":[{"message":{"content":"BANANA"}}]}`, 200); o != probePass || !m {
		t.Errorf("correct canary: outcome=%v matched=%v, want probePass+matched", o, m)
	}
	if o, _, m := eval(`{"choices":[{"message":{"content":"the answer is banana."}}]}`, 200); o != probePass || !m {
		t.Errorf("substring (case-insensitive): outcome=%v matched=%v, want probePass+matched", o, m)
	}

	// Responded with content but no fingerprint and no competing canary token =>
	// ALIVE (not a failure). An arbitrary off-script word is inconclusive, not wrong.
	if o, _, m := eval(`{"choices":[{"message":{"content":"APPLE"}}]}`, 200); o != probeAlive || m {
		t.Errorf("off-script responsive answer: outcome=%v matched=%v, want probeAlive", o, m)
	}
	if o := mustNotFail(eval(`{"choices":[{"message":{"content":"APPLE"}}]}`, 200)); o {
		t.Error("a responsive off-script answer must NOT count as a failure")
	}

	// Clearly WRONG-family: the content asserts a DIFFERENT canary's answer => fail.
	if o, _, _ := eval(`{"choices":[{"message":{"content":"PENGUIN"}}]}`, 200); o != probeWrong {
		t.Errorf("wrong-family answer: outcome=%v, want probeWrong", o)
	}

	// Empty / non-2xx => probeDead (real failure).
	if o, _, _ := eval(`{"choices":[{"message":{"content":""}}]}`, 200); o != probeDead {
		t.Errorf("empty completion: outcome=%v, want probeDead", o)
	}
	if o, _, _ := eval(`{"choices":[{"message":{"content":"BANANA"}}]}`, 500); o != probeDead {
		t.Errorf("non-2xx: outcome=%v, want probeDead", o)
	}
}

func mustNotFail(o probeOutcome, _ float64, _ bool) bool { return o.failed() }

// TestEvalCanaryReasoningModel: a gpt-oss-style reasoning response (a reasoning
// preamble that wanders, or reasoning that EXHAUSTS the budget leaving content
// empty) is ALIVE/verified, NOT a failure, with tok/s recorded. This is the bug the
// fix targets: a healthy reasoning flagship must verify, not flap to failing.
func TestEvalCanaryReasoningModel(t *testing.T) {
	b := newTrustBroker()
	fp := canaryFingerprint{prompt: "Reply with only the single word: BANANA", expect: "banana"}
	eval := func(body string) (probeOutcome, float64, bool) {
		res := protocol.JobResult{Status: 200, Body: json.RawMessage(body), Receipt: protocol.UsageReceipt{CompletionTokens: 200}}
		return b.evalCanary(res, 1*time.Second, fp)
	}

	// Reasoning preamble that later DOES emit the answer => probePass.
	withAnswer := `{"choices":[{"message":{"content":"Let me think. The user wants one word. Okay: BANANA"}}]}`
	if o, tps, m := eval(withAnswer); o != probePass || !m || tps <= 0 {
		t.Errorf("reasoning+answer: outcome=%v matched=%v tps=%v, want probePass+matched+tps>0", o, m, tps)
	}

	// Reasoning that wanders and never emits the bare token, but returns content =>
	// probeAlive (verified), tok/s still measured. NOT a failure.
	wander := `{"choices":[{"message":{"content":"The user is asking for a single word answer about a fruit, I should respond concisely with the requested item."}}]}`
	if o, tps, _ := eval(wander); o != probeAlive || tps <= 0 || o.failed() {
		t.Errorf("reasoning wander: outcome=%v tps=%v, want probeAlive (not failed), tps>0", o, tps)
	}

	// Reasoning that exhausts the budget: content empty but reasoning_content present
	// => still ALIVE (the node responded), tok/s measured.
	exhausted := `{"choices":[{"message":{"content":"","reasoning_content":"I need to output exactly one word and the user asked for a fruit so I will carefully consider the constraints..."}}]}`
	if o, tps, _ := eval(exhausted); o.failed() || tps <= 0 {
		t.Errorf("reasoning exhausted budget: outcome=%v tps=%v, want alive (not failed), tps>0", o, tps)
	}
}

// TestProbeSchedulingSkipsBusyAndStale: probeOnce probes only online, idle nodes
// (it skips a busy node and a stale one).
func TestProbeSchedulingSkipsBusyAndStale(t *testing.T) {
	now := time.Now()
	b := newTrustBroker()
	b.probe = probeConfig{interval: time.Second}
	b.nodes = map[string]protocol.NodeRegistration{
		"idle":  {NodeID: "idle", Offers: []protocol.ModelOffer{{Model: "m"}}},
		"busy":  {NodeID: "busy", Offers: []protocol.ModelOffer{{Model: "m"}}},
		"stale": {NodeID: "stale", Offers: []protocol.ModelOffer{{Model: "m"}}},
	}
	b.lastSeen = map[string]time.Time{"idle": now, "busy": now, "stale": now.Add(-time.Minute)}
	b.inflight = map[string]int{"busy": 1}
	// Give each node a tunnel whose jobs channel we can drain.
	for id := range b.nodes {
		b.tunnels[id] = &nodeTunnel{jobs: make(chan protocol.Job, 4), waiters: map[string]chan protocol.JobResult{}}
	}
	b.probeOnce()
	// probeOnce dispatches probes concurrently; wait briefly for the idle node's
	// canary job to land on its queue.
	deadline := time.Now().Add(2 * time.Second)
	for len(b.tunnels["idle"].jobs) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	// Only "idle" should have had a probe job enqueued.
	if len(b.tunnels["idle"].jobs) != 1 {
		t.Errorf("idle probe jobs = %d, want 1", len(b.tunnels["idle"].jobs))
	}
	if len(b.tunnels["busy"].jobs) != 0 {
		t.Errorf("busy node should be skipped (jobs = %d)", len(b.tunnels["busy"].jobs))
	}
	if len(b.tunnels["stale"].jobs) != 0 {
		t.Errorf("stale node should be skipped (jobs = %d)", len(b.tunnels["stale"].jobs))
	}
}

// TestRecordProbeStreakAndPick: repeated probe failures build a streak that
// makes pick deprioritize the failing node, but it is still chosen if it is the
// only one offering the model.
func TestRecordProbeStreakAndPick(t *testing.T) {
	now := time.Now()
	b := newTrustBroker()
	b.nodes = map[string]protocol.NodeRegistration{
		"good": {NodeID: "good", Offers: []protocol.ModelOffer{{Model: "m", PriceOut: 1.0}}},
		"bad":  {NodeID: "bad", Offers: []protocol.ModelOffer{{Model: "m", PriceOut: 0.1}}}, // cheaper
	}
	b.lastSeen = map[string]time.Time{"good": now, "bad": now}

	// "bad" fails three probes -> a streak that marks it failing.
	for i := 0; i < 3; i++ {
		b.recordProbe("bad", probeDead, 0, 0, false)
	}
	if !b.probeFailing("bad") {
		t.Fatal("bad node should be probe-failing after 3 consecutive fails")
	}

	// Despite being cheaper, the failing node loses to the healthy one.
	b.mu.Lock()
	node, _, ok := b.pick("m", false, 0, 0, 0, "", nil, nil, nil)
	b.mu.Unlock()
	if !ok || node.NodeID != "good" {
		t.Errorf("pick = %q (ok=%v), want healthy 'good' over cheaper failing 'bad'", node.NodeID, ok)
	}

	// If only the failing node offers a model, it is still chosen (availability).
	b.mu.Lock()
	node2, _, ok2 := b.pick("only-bad", false, 0, 0, 0, "", nil, nil, nil)
	b.mu.Unlock()
	_ = node2
	if ok2 {
		t.Errorf("no node offers 'only-bad', pick should fail")
	}
}

// TestMarketSurfacesTTFTAndQuality: a node with probe-measured ttft and a
// discrepancy surfaces ttft_ms + a sub-1.0 quality in /market and /discover.
func TestMarketSurfacesTTFTAndQuality(t *testing.T) {
	now := time.Now()
	b := newTrustBroker()
	b.nodes = map[string]protocol.NodeRegistration{
		"n": {NodeID: "n", Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 0.2}}},
	}
	b.lastSeen = map[string]time.Time{"n": now}
	b.recount = recountConfig{tolerance: 0.02}
	// One good probe (sets ttft) + one token discrepancy (drops quality).
	b.recordProbe("n", probePass, 120, 50, true)
	b.observeRecount("n", "", 200, 100, true)

	// /market
	rec := httptest.NewRecorder()
	b.market(rec, httptest.NewRequest(http.MethodGet, "/market", nil))
	var mresp struct {
		Market []marketView `json:"market"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &mresp)
	if len(mresp.Market) != 1 {
		t.Fatalf("market entries = %d, want 1", len(mresp.Market))
	}
	mv := mresp.Market[0]
	if mv.BestTTFTMs != 120 {
		t.Errorf("ttft_ms = %v, want 120", mv.BestTTFTMs)
	}
	if mv.Quality >= 1.0 {
		t.Errorf("quality = %v, want <1 after a discrepancy", mv.Quality)
	}

	// /discover
	rec = httptest.NewRecorder()
	b.discover(rec, httptest.NewRequest(http.MethodGet, "/discover", nil))
	var dresp struct {
		Offers []offerView `json:"offers"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &dresp)
	if len(dresp.Offers) != 1 || dresp.Offers[0].TTFTMs != 120 {
		t.Errorf("discover ttft_ms = %+v, want 120", dresp.Offers)
	}
	if dresp.Offers[0].Quality >= 1.0 {
		t.Errorf("discover quality = %v, want <1", dresp.Offers[0].Quality)
	}
}
