package main

// verified_serving_bdd_test.go makes features/trust/verified_serving.feature EXECUTABLE,
// driving the REAL probe/market trust machinery: the probe-dead streak that flips a
// heartbeat-fresh node OFFLINE (and the single OK probe that recovers it), the zero signal
// of an offline channel, the Verified (passed-canary) bit and its independence from the
// confidential badge, the success-evidence ladder (organic > probed-OK-no-traffic 0.9 >
// no-evidence neutral 0.6), the deterministic canary a node can't fake with garbage, the
// free measurement a served request grants (probe backoff reset), demand probing of a stale
// browsed node, and the measurement-staleness haircut on ONLY the measured signal terms.
//
// It observes through the real seams - enrichOffersForNode (the market offer view),
// successFor / computeSignal (the signal math), evalCanary + recordProbe (the canary
// verdict), markMeasured + measurementStale, and demandProbeSoonLocked - no mocks.

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/protocol"
)

type vsState struct {
	b     *broker
	now   time.Time
	node  string
	model string
	view  []offerView

	fp            canaryFingerprint
	outcome       probeOutcome
	failsBefore   int
	staleFactor   float64
	termsFresh    signalTerms
	termsStale    signalTerms
	termsBaseline signalTerms
}

// reset builds a broker with the active probe ENABLED (30s floor / 15m ceiling) and one
// online node that heartbeats fresh (lastSeen now, default trust => probeFails 0 => online).
func (s *vsState) reset() {
	s.now = time.Now()
	s.node, s.model = "n1", "m"
	s.b = routeBroker(s.now, map[string]protocol.NodeRegistration{
		s.node: {NodeID: s.node, Offers: []protocol.ModelOffer{{Model: s.model}}},
	})
	s.b.probeSched = map[string]*probeState{}
	s.b.probe = probeConfig{interval: 30 * time.Second, ceiling: 15 * time.Minute}
	s.view = nil
	s.fp, s.outcome, s.failsBefore, s.staleFactor = canaryFingerprint{}, probeDead, 0, 0
}

func (s *vsState) enrich(probeOnBrowse bool) {
	s.b.mu.Lock()
	s.view = s.b.enrichOffersForNode(nil, s.b.nodes[s.node], s.now, nil, probeOnBrowse)
	s.b.mu.Unlock()
}

func (s *vsState) offer() offerView { return s.view[0] }

func (s *vsState) setTrust(mut func(*trustState)) {
	tq := s.b.trust[s.node]
	mut(&tq)
	s.b.trust[s.node] = tq
}

// --- the probe-dead streak gates "online" ----------------------------------

func (s *vsState) failsDeadStreak() error {
	s.setTrust(func(t *trustState) { t.probeFails = probeDeadStreak })
	return nil
}

func (s *vsState) surfacedOffline() error {
	s.enrich(false)
	if s.offer().Online {
		return fmt.Errorf("node reads Online, want OFFLINE after a sustained probe-dead streak (it still heartbeats)")
	}
	return nil
}

func (s *vsState) offlineFromStreak() error {
	s.setTrust(func(t *trustState) { t.probeFails = probeDeadStreak })
	return nil
}

func (s *vsState) singleProbeSucceeds() error {
	s.b.recordProbe(s.node, probePass, 100, 50, true) // one OK probe resets the streak
	return nil
}

func (s *vsState) surfacedOnline() error {
	s.enrich(false)
	if !s.offer().Online {
		return fmt.Errorf("node reads OFFLINE, want online again after a single OK probe")
	}
	return nil
}

func (s *vsState) isOffline() error {
	s.setTrust(func(t *trustState) { t.probeFails = probeDeadStreak })
	return nil
}

func (s *vsState) signalIsZero() error {
	s.enrich(false)
	if s.offer().Signal != 0 {
		return fmt.Errorf("offline node signal = %d, want 0 (dead channel scores nothing)", s.offer().Signal)
	}
	return nil
}

// --- the Verified (probe-canary) bit ---------------------------------------

func (s *vsState) passesServingCanary() error {
	s.b.recordProbe(s.node, probePass, 100, 50, true)
	return nil
}

func (s *vsState) offerVerifiedTrue() error {
	s.enrich(false)
	if !s.offer().Verified {
		return fmt.Errorf("offer Verified=false, want true after a passed canary")
	}
	return nil
}

func (s *vsState) verifiedIndependentOfConfidential() error {
	// Toggle the confidential badge both ways; the Verified bit (passed canary) must not move.
	s.b.confidential[s.node] = true
	s.enrich(false)
	if !s.offer().Verified || !s.offer().Confidential {
		return fmt.Errorf("with confidential ON: Verified=%v Confidential=%v, want both true (independent bits)", s.offer().Verified, s.offer().Confidential)
	}
	s.b.confidential[s.node] = false
	s.enrich(false)
	if !s.offer().Verified || s.offer().Confidential {
		return fmt.Errorf("with confidential OFF: Verified=%v Confidential=%v, want Verified true, Confidential false", s.offer().Verified, s.offer().Confidential)
	}
	return nil
}

// --- success evidence ladder -----------------------------------------------

func (s *vsState) probedOKnoTraffic() error {
	s.b.recordProbe(s.node, probePass, 100, 50, true) // canary passed; no organic success recorded
	return nil
}

func (s *vsState) successPositiveNotPerfect() error {
	v := successFor(0, false, s.b.trust[s.node].verifiedServing()) // srSeen=false, verifiedOK=true
	if !(v > 0.6 && v < 1.0) {
		return fmt.Errorf("probed-OK-no-traffic success evidence = %.3f, want strongly positive but below proven-perfect (0.6 < v < 1.0)", v)
	}
	return nil
}

func (s *vsState) noEvidenceAtAll() error { return nil } // default trust: unprobed, no success

func (s *vsState) successNeutralNoData() error {
	v := successFor(0, false, s.b.trust[s.node].verifiedServing()) // verifiedServing=false
	if v != 0.6 {
		return fmt.Errorf("no-evidence success = %.3f, want the neutral 0.6 (not an assumed-perfect 1.0)", v)
	}
	if v == 1.0 {
		return fmt.Errorf("no-evidence success must not be an optimistic 1.0")
	}
	return nil
}

// --- the canary is deterministic + broker-originated -----------------------

func (s *vsState) brokerSendsCanary() error {
	s.fp = nextCanary(0) // deterministic challenge (expect "banana")
	return nil
}

func (s *vsState) returnsFingerprintFailure() error {
	s.failsBefore = s.b.trust[s.node].probeFails
	// A WRONG-FAMILY answer: a DIFFERENT canary's distinctive token, ours absent => probeWrong.
	body := json.RawMessage(`{"choices":[{"message":{"content":"penguin"}}]}`)
	res := protocol.JobResult{Status: 200, Body: body, Receipt: protocol.UsageReceipt{CompletionTokens: 1}}
	s.outcome, _, _ = s.b.evalCanary(res, 50*time.Millisecond, s.fp)
	s.b.recordProbe(s.node, s.outcome, 0, 0, false)
	return nil
}

func (s *vsState) probeRecordedFailed() error {
	if !s.outcome.failed() {
		return fmt.Errorf("garbage answer outcome = %v, want a failing outcome (cannot pass by garbage)", s.outcome)
	}
	if got := s.b.trust[s.node].probeFails; got <= s.failsBefore {
		return fmt.Errorf("probeFails did not increment on a failed canary: %d -> %d", s.failsBefore, got)
	}
	return nil
}

// --- real traffic is a free measurement ------------------------------------

func (s *vsState) servesQualityRequest() error {
	s.b.markMeasured(s.node) // the relay's free-measurement hook on a served request
	return nil
}

func (s *vsState) backoffResetFreshlyMeasured() error {
	st := s.b.probeSched[s.node]
	if st == nil {
		return fmt.Errorf("no probe schedule recorded after a served request")
	}
	if st.backoff != 0 {
		return fmt.Errorf("probe backoff = %d, want 0 (a served request resets it to the floor)", st.backoff)
	}
	if s.b.probe.measurementStale(st.lastMeasured, time.Now()) {
		return fmt.Errorf("node reads stale right after a free measurement")
	}
	return nil
}

// --- demand probing + measurement staleness --------------------------------

func (s *vsState) measurementIsStale() error {
	// A real but OLD last-measurement (two ceilings ago) so the staleness factor is < 1.0
	// and the future next-due will be pulled back by the browse.
	s.b.probeSched[s.node] = &probeState{
		lastMeasured: s.now.Add(-2 * s.b.probe.ceiling),
		nextDue:      s.now.Add(time.Hour),
		backoff:      3,
	}
	return nil
}

func (s *vsState) consumerBrowses() error {
	s.enrich(true) // probeOnBrowse=true => demand probing fires for a live, stale node
	return nil
}

func (s *vsState) nearTermProbeScheduled() error {
	st := s.b.probeSched[s.node]
	if st == nil {
		return fmt.Errorf("no probe schedule after browsing")
	}
	if st.backoff != 0 {
		return fmt.Errorf("demand probe must reset backoff to 0, got %d", st.backoff)
	}
	if st.nextDue.After(s.now) {
		return fmt.Errorf("next-due was not pulled back to now (still %v after now)", st.nextDue.Sub(s.now))
	}
	return nil
}

func (s *vsState) goneLongUnmeasured() error {
	s.staleFactor = 0.7 // a long-unmeasured node's staleness-confidence factor
	return nil
}

func (s *vsState) onlyMeasuredTermsDiscounted() error {
	base := signalInput{providers: 1, bestTPS: 200, ttftMs: 300, successRate: 0.9, trust: 1, recency: 1, verified: true}
	fresh, stale := base, base
	fresh.staleness, stale.staleness = 1.0, s.staleFactor
	s.termsFresh, s.termsStale = computeSignal(fresh), computeSignal(stale)
	s.termsBaseline = computeSignal(base) // staleness unset => full confidence
	if !(s.termsStale.Speed < s.termsFresh.Speed && s.termsStale.Latency < s.termsFresh.Latency && s.termsStale.Verified < s.termsFresh.Verified) {
		return fmt.Errorf("measured terms not discounted by staleness: stale{speed=%.2f lat=%.2f ver=%.2f} fresh{%.2f %.2f %.2f}",
			s.termsStale.Speed, s.termsStale.Latency, s.termsStale.Verified, s.termsFresh.Speed, s.termsFresh.Latency, s.termsFresh.Verified)
	}
	if s.termsStale.Supply != s.termsFresh.Supply {
		return fmt.Errorf("supply must NOT be discounted by measurement staleness: stale=%.3f fresh=%.3f", s.termsStale.Supply, s.termsFresh.Supply)
	}
	return nil
}

func (s *vsState) freshRestoresAtOnce() error {
	// A single fresh measurement (staleness 1.0) restores the measured terms to their full,
	// un-discounted value at once.
	if s.termsFresh.Speed != s.termsBaseline.Speed || s.termsFresh.Verified != s.termsBaseline.Verified {
		return fmt.Errorf("a fresh measurement did not restore full confidence: fresh{speed=%.3f ver=%.3f} baseline{%.3f %.3f}",
			s.termsFresh.Speed, s.termsFresh.Verified, s.termsBaseline.Speed, s.termsBaseline.Verified)
	}
	return nil
}

func TestTrustVerifiedServingBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &vsState{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			sc.Step(`^an online node that heartbeats fresh$`, func() error { return nil })
			sc.Step(`^the node fails liveness probes up to the dead-streak threshold$`, st.failsDeadStreak)
			sc.Step(`^the node is surfaced OFFLINE in the market even though it still heartbeats$`, st.surfacedOffline)
			sc.Step(`^the node is offline from a failed probe streak$`, st.offlineFromStreak)
			sc.Step(`^a single probe succeeds$`, st.singleProbeSucceeds)
			sc.Step(`^the node is surfaced online again$`, st.surfacedOnline)
			sc.Step(`^the node is offline$`, st.isOffline)
			sc.Step(`^its market signal is 0$`, st.signalIsZero)
			sc.Step(`^the node passes a serving canary$`, st.passesServingCanary)
			sc.Step(`^its offer carries Verified=true$`, st.offerVerifiedTrue)
			sc.Step(`^Verified is independent of the confidential \(◆\) badge$`, st.verifiedIndependentOfConfidential)
			sc.Step(`^the node has no organic traffic but a recent passed canary$`, st.probedOKnoTraffic)
			sc.Step(`^its success evidence is strongly positive but below a proven-perfect rate$`, st.successPositiveNotPerfect)
			sc.Step(`^the node has neither organic traffic nor a passed canary$`, st.noEvidenceAtAll)
			sc.Step(`^its success evidence is the neutral no-data value, not an assumed-perfect rate$`, st.successNeutralNoData)
			sc.Step(`^the broker sends its deterministic canary challenge$`, st.brokerSendsCanary)
			sc.Step(`^the node returns an answer that fails the fingerprint check$`, st.returnsFingerprintFailure)
			sc.Step(`^the probe is recorded as failed$`, st.probeRecordedFailed)
			sc.Step(`^the node serves a quality-validated request$`, st.servesQualityRequest)
			sc.Step(`^its probe backoff is reset and it reads as freshly measured$`, st.backoffResetFreshlyMeasured)
			sc.Step(`^the node's measurement is stale$`, st.measurementIsStale)
			sc.Step(`^a consumer browses the market for its model$`, st.consumerBrowses)
			sc.Step(`^a near-term probe is scheduled for that node$`, st.nearTermProbeScheduled)
			sc.Step(`^the node has gone long without a fresh measurement$`, st.goneLongUnmeasured)
			sc.Step(`^only its measured terms \(speed, latency, verified\) are discounted, not supply or liveness$`, st.onlyMeasuredTermsDiscounted)
			sc.Step(`^a fresh measurement restores full confidence at once$`, st.freshRestoresAtOnce)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/trust/verified_serving.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("trust/verified_serving behavior scenarios failed (see godog output above)")
	}
}
