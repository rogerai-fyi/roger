package main

// concierge_failfast_bdd_test.go makes features/routing/concierge_failfast.feature
// EXECUTABLE, driving the REAL concierge dogfood PICK gate (pickFreeStation +
// pickGrantStation) and the free-station relay (dogfoodRelay) against the REAL probe/trust
// machinery (trustState.verifiedServing + probeState.lastMeasured + probe.measurementStale)
// — no mocks. It proves the fail-fast contract: a heartbeat-fresh but NOT-proven-live
// station is skipped AT THE PICK (so the relay misses in milliseconds, never after the
// ~30s relay wait), while a proven-live station (canary-passed AND fresh) is still picked
// and a slow-but-live node still gets its full relay headroom. When the active probe is
// DISABLED the gate is inert and the legacy heartbeat-only pick is preserved, and the
// dogfood->Groq graceful-degrade contract still holds. It reuses the concierge test harness
// (newConciergeBroker / grantConciergeBroker / postConcierge) so it exercises the same real
// pick/relay path as production.

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

type ffState struct {
	t *testing.T

	b     *broker
	now   time.Time
	node  string
	model string

	// grant path
	grantSecret string
	grantNode   string

	// pick results
	pickedNode string
	pickedOK   bool

	// relay results
	relayReply   string
	relayServed  bool
	relayElapsed time.Duration
	relayWait    time.Duration

	// handler result
	handlerReply string
	handlerVia   string
}

// reset builds a concierge broker with the active probe ENABLED (30s floor / 15m ceiling)
// and the trust/probeSched maps initialised, so the proven-live pick gate has real state to
// read. A single free chat node ("free") heartbeats fresh but starts NOT proven-live (no
// probe recorded). Scenarios promote it to proven-live (a passed canary within the ceiling)
// or leave it dead to exercise the fail-fast skip.
func (s *ffState) reset(t *testing.T) {
	s.t = t
	s.now = time.Now()
	s.node, s.model = "free", "free-m"

	b := newConciergeBroker()
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	b.priv = brokerPriv // dogfoodRelay's pseudonym() needs a broker key
	b.trust = map[string]trustState{}
	b.probeSched = map[string]*probeState{}
	b.probe = probeConfig{interval: 30 * time.Second, ceiling: 15 * time.Minute}

	nodePub, _, _ := ed25519.GenerateKey(nil)
	b.nodes[s.node] = protocol.NodeRegistration{
		NodeID: s.node, PubKey: hex.EncodeToString(nodePub),
		Offers: []protocol.ModelOffer{{Model: s.model}}, // zero price => free
	}
	b.lastSeen[s.node] = s.now
	b.tunnels[s.node] = &nodeTunnel{jobs: make(chan protocol.Job, 1), waiters: map[string]chan protocol.JobResult{}}

	s.b = b
	s.grantSecret, s.grantNode = "", ""
	s.pickedNode, s.pickedOK = "", false
	s.relayReply, s.relayServed, s.relayElapsed, s.relayWait = "", false, 0, 0
	s.handlerReply, s.handlerVia = "", ""
}

// setTrust mutates a node's trustState in place (trust is a value map).
func (s *ffState) setTrust(node string, mut func(*trustState)) {
	tq := s.b.trust[node]
	mut(&tq)
	s.b.trust[node] = tq
}

// stampMeasured sets a node's last-measurement time (drives probe.measurementStale).
func (s *ffState) stampMeasured(node string, at time.Time) {
	st := s.b.probeSched[node]
	if st == nil {
		st = &probeState{}
		s.b.probeSched[node] = st
	}
	st.lastMeasured = at
}

// --- Background / probe toggle -------------------------------------------------

func (s *ffState) probeEnabled() error { return nil } // reset() already enables it

func (s *ffState) probeDisabled() error {
	s.b.probe = probeConfig{} // interval 0 => enabled()==false
	return nil
}

// --- free-station givens -------------------------------------------------------

func (s *ffState) freeStationFresh() error {
	s.b.lastSeen[s.node] = time.Now() // heartbeat-fresh
	return nil
}

func (s *ffState) neverProbed() error {
	// default zero trustState => probed=false => verifiedServing()==false. Explicit for clarity.
	s.setTrust(s.node, func(t *trustState) { t.probed = false; t.probeOK = false; t.probeFails = 0 })
	delete(s.b.probeSched, s.node)
	return nil
}

func (s *ffState) passedCanaryFresh() error {
	// A healthy proven-live node: a passed canary that also ran to COMPLETION (probeCompleted),
	// so verifiedServing() holds after the completion-proven tightening.
	s.setTrust(s.node, func(t *trustState) { t.probed = true; t.probeOK = true; t.probeFails = 0; t.probeCompleted = true })
	s.stampMeasured(s.node, time.Now()) // fresh: within the ceiling
	return nil
}

func (s *ffState) passedCanaryStale() error {
	s.setTrust(s.node, func(t *trustState) { t.probed = true; t.probeOK = true; t.probeFails = 0 })
	// two ceilings ago => measurementStale == true (older than the ceiling horizon).
	s.stampMeasured(s.node, time.Now().Add(-2*s.b.probe.ceiling))
	return nil
}

func (s *ffState) passedButFailingStreak() error {
	// A recent PASS timestamp but a non-zero streak: verifiedServing()==false (streak != 0).
	s.setTrust(s.node, func(t *trustState) { t.probed = true; t.probeOK = true; t.probeFails = 2 })
	s.stampMeasured(s.node, time.Now())
	return nil
}

func (s *ffState) servedRealRequestNow() error {
	s.b.markMeasured(s.node) // the relay's free-measurement hook: stamps lastMeasured now
	return nil
}

// recordCanary drives ONE canary result through the REAL evalCanary -> recordProbe path
// (exactly as probeNode does), for the node currently under test. completionTokens carries
// the node's self-reported output tokens on the receipt: >0 means the canary ran to
// COMPLETION (probeCompleted set true), ==0 is the stall-after-first-token case (a 2xx with
// a reasoning channel but no counted answer — ALIVE but NOT completed). No mocks: the same
// JobResult -> evalCanary -> recordProbe seams production uses.
func (s *ffState) recordCanary(node string, completionTokens int) {
	fp := nextCanary(0) // deterministic challenge; expected token "banana"
	// A stall-only reply (no counted output) still RESPONDS on the reasoning channel, so it
	// reads ALIVE (probeOK) but never completes. A completed reply carries counted tokens.
	var body []byte
	if completionTokens > 0 {
		body = []byte(`{"choices":[{"message":{"content":"` + fp.expect + `"}}]}`)
	} else {
		// Empty visible content + a reasoning channel: the exact gpt-oss stall symptom.
		body = []byte(`{"choices":[{"message":{"content":"","reasoning":"thinking about it, still going"}}]}`)
	}
	res := protocol.JobResult{Status: 200, Body: body, Receipt: protocol.UsageReceipt{CompletionTokens: completionTokens}}
	outcome, tps, matched, completed := s.b.evalCanary(res, 50*time.Millisecond, fp)
	s.b.recordProbe(node, outcome, 100, tps, matched, completed)
}

// passedStallCanary: a passed LIVENESS canary that did NOT complete a generation (2xx +
// reasoning channel, zero counted output). verifiedServing() must be FALSE after the fix.
func (s *ffState) passedStallCanary() error {
	s.recordCanary(s.node, 0)
	return nil
}

// passedCompletingCanaryFresh: a canary that ran to COMPLETION within the ceiling.
// recordProbe stamps lastMeasured=now on the alive branch, so it is fresh already.
func (s *ffState) passedCompletingCanaryFresh() error {
	s.recordCanary(s.node, 5)
	return nil
}

// stallCanaryButCompletedRelay: a stall-only canary (not completion-proven) BUT a real
// COMPLETED relay just now (successCount>0 + fresh markMeasured stamp) — the busy-node
// case conciergeProvenLiveLocked admits without a canary.
func (s *ffState) stallCanaryButCompletedRelay() error {
	s.recordCanary(s.node, 0) // canary alive but not completed
	if s.b.successCount == nil {
		s.b.successCount = map[string]int{}
	}
	s.b.successCount[s.node] = 1 // a quality-validated completed relay
	s.b.markMeasured(s.node)     // fresh measurement stamp from the served request
	return nil
}

// passedCompletingCanaryStale: a completion-proven canary whose measurement then aged past
// the ceiling — completion-proven but STALE, so the pick must skip it.
func (s *ffState) passedCompletingCanaryStale() error {
	s.recordCanary(s.node, 5)
	s.stampMeasured(s.node, time.Now().Add(-2*s.b.probe.ceiling)) // age the measurement out
	return nil
}

// completingCanaryRecords is the WHEN for the bootstrap scenario: a fresh node's FIRST
// canary lands and it COMPLETES, so it becomes pickable immediately.
func (s *ffState) completingCanaryRecords() error {
	s.recordCanary(s.node, 5)
	return nil
}

// --- grant-station givens ------------------------------------------------------

func (s *ffState) grantNodeFresh() error {
	// Rebuild as a grant-configured broker (Mem store + bound node + grant), probe enabled.
	b, secret, node := grantConciergeBroker(s.t, []string{"gpt-oss-120b"})
	b.trust = map[string]trustState{}
	b.probeSched = map[string]*probeState{}
	b.probe = probeConfig{interval: 30 * time.Second, ceiling: 15 * time.Minute}
	b.lastSeen[node] = time.Now() // on air
	s.b, s.grantSecret, s.grantNode = b, secret, node
	s.node, s.model = node, "gpt-oss-120b"
	return nil
}

// --- relay-wait given ----------------------------------------------------------

func (s *ffState) relayWaitIs(secs int) error {
	s.b.concierge.relayTimeout = clampRelayTimeout(secs)
	s.relayWait = s.b.concierge.relayWait()
	return nil
}

// --- graceful-degrade givens ---------------------------------------------------

func (s *ffState) dogfoodMissesNotProvenLive() error {
	// The real dogfoodRelay wired in, against a never-probed node: it must MISS.
	s.b.concierge.dogfoodFn = s.b.dogfoodRelay
	s.neverProbed()
	return nil
}

func (s *ffState) groqAvailable() error {
	s.b.concierge.groqFn = func(_ []chatMsg) (string, bool) { return "answer from groq", true }
	return nil
}

// --- WHENs ---------------------------------------------------------------------

func (s *ffState) picksFreeStation() error {
	s.pickedNode, _, s.pickedOK = s.b.pickFreeStation()
	return nil
}

func (s *ffState) picksGrantStation() error {
	gc, ok, gerr := s.b.resolveGrantToken(s.grantSecret)
	if !ok || gerr != "" {
		return fmt.Errorf("grant did not resolve in the test setup: ok=%v err=%q", ok, gerr)
	}
	s.pickedNode, s.pickedOK = s.b.pickGrantStation(gc.nodeAllow, s.model)
	return nil
}

func (s *ffState) runsFreeRelay() error {
	start := time.Now()
	s.relayReply, s.relayServed = s.b.dogfoodRelay([]chatMsg{{Role: "user", Content: "hi ping"}})
	s.relayElapsed = time.Since(start)
	return nil
}

func (s *ffState) runsFreeRelayAgainstAnsweringNode() error {
	tun := s.b.tunnels[s.node]
	go func() {
		job := <-tun.jobs
		tun.mu.Lock()
		ch := tun.waiters[job.ID]
		tun.mu.Unlock()
		ch <- protocol.JobResult{ID: job.ID, Status: 200,
			Body: []byte(`{"choices":[{"message":{"role":"assistant","content":"You're on the air."}}]}`)}
	}()
	start := time.Now()
	s.relayReply, s.relayServed = s.b.dogfoodRelay([]chatMsg{{Role: "user", Content: "hi ping"}})
	s.relayElapsed = time.Since(start)
	return nil
}

func (s *ffState) visitorAsksPing() error {
	_, out := postConcierge(s.t, s.b, "10.0.0.1", "how do I tune in?")
	s.handlerReply, s.handlerVia = out["reply"], out["via"]
	return nil
}

// --- THENs ---------------------------------------------------------------------

func (s *ffState) noFreeStationPicked() error {
	if s.pickedOK {
		return fmt.Errorf("a free station was picked (%q), want NONE (node is not proven-live)", s.pickedNode)
	}
	return nil
}

func (s *ffState) thatFreeStationPicked() error {
	if !s.pickedOK || s.pickedNode != s.node {
		return fmt.Errorf("pickFreeStation = (%q,%v), want the proven-live node %q", s.pickedNode, s.pickedOK, s.node)
	}
	return nil
}

func (s *ffState) noGrantStationPicked() error {
	if s.pickedOK {
		return fmt.Errorf("a grant station was picked (%q), want NONE (node is not proven-live)", s.pickedNode)
	}
	return nil
}

func (s *ffState) thatGrantStationPicked() error {
	if !s.pickedOK || s.pickedNode != s.grantNode {
		return fmt.Errorf("pickGrantStation = (%q,%v), want the proven-live grant node %q", s.pickedNode, s.pickedOK, s.grantNode)
	}
	return nil
}

func (s *ffState) relayNotServed() error {
	if s.relayServed || s.relayReply != "" {
		return fmt.Errorf("relay served=%v reply=%q, want a clean miss", s.relayServed, s.relayReply)
	}
	return nil
}

func (s *ffState) relayReturnedWellUnderWait() error {
	// The pick gate skips the dead node, so dogfoodRelay returns before touching the tunnel:
	// milliseconds, nowhere near the relay wait. A generous 2s bound proves it did NOT wait
	// the ~30s ceiling (nor the 2s enqueue timeout) without wall-clock-sleeping 30s.
	if s.relayElapsed >= 2*time.Second {
		return fmt.Errorf("relay returned in %v — it did NOT fail fast (relay wait was %v)", s.relayElapsed, s.relayWait)
	}
	return nil
}

func (s *ffState) relayServesStationReply() error {
	if !s.relayServed {
		return fmt.Errorf("relay did not serve a proven-live node that answered")
	}
	if s.relayReply != "You're on the air." {
		return fmt.Errorf("relay reply = %q, want the station completion text", s.relayReply)
	}
	return nil
}

func (s *ffState) pingAnswersViaGroq() error {
	if s.handlerVia != "groq" || s.handlerReply != "answer from groq" {
		return fmt.Errorf("Ping answered via=%q reply=%q, want the Groq fallback", s.handlerVia, s.handlerReply)
	}
	return nil
}

func TestConciergeFailFastBDD(t *testing.T) {
	st := &ffState{}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset(t)
				return ctx, nil
			})
			sc.Step(`^the active liveness probe is enabled$`, st.probeEnabled)
			sc.Step(`^the active liveness probe is disabled$`, st.probeDisabled)

			sc.Step(`^a free on-air station that heartbeats fresh$`, st.freeStationFresh)
			sc.Step(`^that station has never passed a liveness probe$`, st.neverProbed)
			sc.Step(`^that station passed a canary within the probe ceiling$`, st.passedCanaryFresh)
			sc.Step(`^that station passed a canary but its last measurement is older than the probe ceiling$`, st.passedCanaryStale)
			sc.Step(`^that station has a recent passed probe but a non-zero failure streak$`, st.passedButFailingStreak)
			sc.Step(`^that station served a real request just now$`, st.servedRealRequestNow)

			// completion-proven gate (free + grant share the handlers; they act on s.node).
			sc.Step(`^that station passed a liveness canary but the canary did not complete a generation$`, st.passedStallCanary)
			sc.Step(`^that node passed a liveness canary but the canary did not complete a generation$`, st.passedStallCanary)
			sc.Step(`^that station passed a canary that ran to completion within the probe ceiling$`, st.passedCompletingCanaryFresh)
			sc.Step(`^that node passed a canary that ran to completion within the probe ceiling$`, st.passedCompletingCanaryFresh)
			sc.Step(`^that station has a stall-only canary but completed a real relay just now$`, st.stallCanaryButCompletedRelay)
			sc.Step(`^that station passed a completing canary but its last measurement is older than the probe ceiling$`, st.passedCompletingCanaryStale)
			sc.Step(`^a completing canary probe records for that station$`, st.completingCanaryRecords)

			sc.Step(`^the grant owner has an on-air node offering the granted model that heartbeats fresh$`, st.grantNodeFresh)
			sc.Step(`^that node has never passed a liveness probe$`, st.neverProbed)
			sc.Step(`^that node passed a canary within the probe ceiling$`, st.passedCanaryFresh)

			sc.Step(`^the concierge relay wait is (\d+) seconds$`, st.relayWaitIs)

			sc.Step(`^the free-station dogfood misses because no station is proven-live$`, st.dogfoodMissesNotProvenLive)
			sc.Step(`^Groq is available$`, st.groqAvailable)

			sc.Step(`^the concierge picks a free station$`, st.picksFreeStation)
			sc.Step(`^the concierge picks a grant station$`, st.picksGrantStation)
			sc.Step(`^the concierge runs the free-station dogfood relay$`, st.runsFreeRelay)
			sc.Step(`^the concierge runs the free-station dogfood relay against a node that answers$`, st.runsFreeRelayAgainstAnsweringNode)
			sc.Step(`^a visitor asks Ping a question$`, st.visitorAsksPing)

			sc.Step(`^no free station is picked$`, st.noFreeStationPicked)
			sc.Step(`^that free station is picked$`, st.thatFreeStationPicked)
			sc.Step(`^no grant station is picked$`, st.noGrantStationPicked)
			sc.Step(`^that grant station is picked$`, st.thatGrantStationPicked)
			sc.Step(`^the relay reports not served$`, st.relayNotServed)
			sc.Step(`^the relay returned in well under the relay wait$`, st.relayReturnedWellUnderWait)
			sc.Step(`^the relay serves the station reply$`, st.relayServesStationReply)
			sc.Step(`^Ping answers via Groq$`, st.pingAnswersViaGroq)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/routing/concierge_failfast.feature"},
			TestingT: t,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("concierge fail-fast feature scenarios failed")
	}
}

// TestConciergeWorstCaseLatencyFailsFast is the direct regression for the ~61s symptom:
// with BOTH real dogfood paths wired (grant-dogfood AND free-station) and the probe ENABLED,
// against nodes that are heartbeat-fresh but NOT proven-live, the whole /concierge handler
// must fall through to Groq in well under a second - not after the ~30s relay wait on each
// dogfood path (the old 61s). Both picks skip the dead nodes, so neither relay is ever
// entered. We assert the end-to-end handler latency AND that Groq served.
func TestConciergeWorstCaseLatencyFailsFast(t *testing.T) {
	// Grant-configured broker (grant path wired to the REAL relay) + probe enabled.
	b, _, gnode := grantConciergeBroker(t, []string{"gpt-oss-120b"})
	b.trust = map[string]trustState{}
	b.probeSched = map[string]*probeState{}
	b.probe = probeConfig{interval: 30 * time.Second, ceiling: 15 * time.Minute}
	b.concierge.relayTimeout = clampRelayTimeout(30) // the full production wait

	// The grant node: heartbeat-fresh but NEVER proven-live (no canary, no successful relay).
	b.lastSeen[gnode] = time.Now()

	// A free node: also heartbeat-fresh but never proven-live. Wire the REAL free-station relay.
	freePub, _, _ := ed25519.GenerateKey(nil)
	b.nodes["deadfree"] = protocol.NodeRegistration{
		NodeID: "deadfree", PubKey: hex.EncodeToString(freePub),
		Offers: []protocol.ModelOffer{{Model: "free-m"}}, // zero price => free
	}
	b.lastSeen["deadfree"] = time.Now()
	b.tunnels["deadfree"] = &nodeTunnel{jobs: make(chan protocol.Job, 1), waiters: map[string]chan protocol.JobResult{}}
	b.concierge.dogfoodFn = b.dogfoodRelay // real free-station path (grant path already real)

	// Groq is the graceful-degrade target.
	b.concierge.groqFn = func(_ []chatMsg) (string, bool) { return "answer from groq", true }

	start := time.Now()
	code, out := postConcierge(t, b, "10.9.8.7", "how do I tune in?")
	elapsed := time.Since(start)

	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if out["via"] != "groq" || out["reply"] != "answer from groq" {
		t.Fatalf("via=%q reply=%q, want the Groq fallback (both dogfood picks must have missed fast)", out["via"], out["reply"])
	}
	// The whole point: NOT ~30s per dogfood path (~61s total). A generous 2s ceiling proves
	// both picks skipped at the gate (each relay wait is 30s; the enqueue timeout alone is 2s).
	if elapsed >= 2*time.Second {
		t.Fatalf("/concierge took %v with nothing proven-live - it did NOT fail fast (was ~61s before the pick gate)", elapsed)
	}
	t.Logf("worst-case /concierge latency with nothing proven-live: %v (was ~61s)", elapsed)
}

// keep imports used even if a scenario set changes.
var _ = store.NewMem
var _ = http.StatusOK
