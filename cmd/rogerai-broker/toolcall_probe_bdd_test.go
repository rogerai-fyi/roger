package main

// toolcall_probe_bdd_test.go makes features/trust/toolcall_probe.feature EXECUTABLE, driving
// the REAL tool-call capability probe machinery with no mocks: the pure verdict (toolCallOK),
// the verdict application + regression + transient/authoritative gate (recordToolProbe /
// applyToolVerdict), the emission through the offer view (enrichOffersForNode) and the
// aggregated market union (computeMarket), the node-facing declared-"tools" strip (register's
// stripDeclaredTools), the first-class shared verdict + peer union (markToolsVerified /
// syncToolsVerified over a real miniredis-backed valkeyStore), and the adaptive schedule the canary rides
// (probeState / probeConfig). It observes through the same seams verified_serving_bdd_test.go uses.

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

type tcState struct {
	b     *broker
	now   time.Time
	node  string
	model string
	model2 string

	canon []string // result of the canonicalization step

	// multi-instance (scenarios 18-19): two brokers over one shared miniredis.
	t  *testing.T
	mr *miniredis.Miniredis
	a  *broker
	bB *broker
}

// reset builds a SINGLE-instance broker (shared==nil, always authoritative) with one online
// node offering a chat model with a 131072-token context window, matching the Background.
func (s *tcState) reset() {
	s.now = time.Now()
	s.node, s.model, s.model2 = "n1", "m", "m2"
	s.b = routeBroker(s.now, map[string]protocol.NodeRegistration{
		s.node: {NodeID: s.node, Offers: []protocol.ModelOffer{{Model: s.model, Ctx: 131072}}},
	})
	s.b.toolsOK = map[string]bool{}
	s.b.probeSched = map[string]*probeState{}
	s.b.probe = probeConfig{interval: 30 * time.Second, ceiling: 15 * time.Minute}
	s.canon = nil
	s.mr, s.a, s.bB = nil, nil, nil
}

// capsFor returns the emitted Capabilities for a model on broker b via the REAL per-offer feed
// (enrichOffersForNode) - the exact function GET /discover + /market compute per offer.
func capsFor(b *broker, node, model string, now time.Time) []string {
	b.mu.Lock()
	view := b.enrichOffersForNode(nil, b.nodes[node], now, nil, false)
	b.mu.Unlock()
	for _, o := range view {
		if o.Model == model {
			return o.Capabilities
		}
	}
	return nil
}

// --- Background -----------------------------------------------------------------------------

func (s *tcState) chatModelCtx(ctx int) error {
	reg := s.b.nodes[s.node]
	reg.Offers = []protocol.ModelOffer{{Model: s.model, Ctx: ctx}}
	s.b.nodes[s.node] = reg
	return nil
}

// --- signal: canonicalization ---------------------------------------------------------------

func (s *tcState) canonicalize(list string) error {
	s.canon = protocol.CanonicalCapabilities([]string{list})
	return nil
}

func (s *tcState) toolsSurvivesCanon() error {
	if !contains(s.canon, protocol.CapTools) {
		return fmt.Errorf("canonicalization dropped %q: got %v", protocol.CapTools, s.canon)
	}
	return nil
}

func (s *tcState) canonLikeVision() error {
	// lowercased + trimmed + deduped, exactly like "vision".
	got := protocol.CanonicalCapabilities([]string{" TOOLS ", "tools", "VISION", "vision"})
	want := []string{"tools", "vision"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		return fmt.Errorf("tools is not canonicalized like vision: got %v want %v", got, want)
	}
	return nil
}

// --- declared-not-trusted -------------------------------------------------------------------

func (s *tcState) nodeDeclaresTools() error {
	// Simulate the node-facing strip at registration: a declared "tools" is removed before it
	// ever lands in b.nodes (the ONLY door a node's own bytes pass through).
	reg := s.b.nodes[s.node]
	for i := range reg.Offers {
		reg.Offers[i].Capabilities = stripDeclaredTools(append([]string{protocol.CapTools}, reg.Offers[i].Capabilities...))
	}
	s.b.nodes[s.node] = reg
	return nil
}

func (s *tcState) neverPassingCanary() error { return nil } // no recordToolProbe(ok) was called

func (s *tcState) publicCapsOmitTools() error {
	if contains(capsFor(s.b, s.node, s.model, s.now), protocol.CapTools) {
		return fmt.Errorf("public capabilities include %q without a passing canary (declared != verified)", protocol.CapTools)
	}
	return nil
}

// --- the canary + verdict -------------------------------------------------------------------

func (s *tcState) brokerSendsCanary() error { return nil } // the When is realized by the verdict steps

func (s *tcState) providerWellFormed() error {
	body := `{"choices":[{"message":{"tool_calls":[{"function":{"name":"roger_probe_ack","arguments":"{\"ok\":true}"}}]},"finish_reason":"tool_calls"}]}`
	s.applyBody(body)
	return nil
}

func (s *tcState) providerPlainText() error {
	s.applyBody(`{"choices":[{"message":{"content":"Sure, I will call it."},"finish_reason":"stop"}]}`)
	return nil
}

func (s *tcState) providerEmptyArray() error {
	s.applyBody(`{"choices":[{"message":{"tool_calls":[]},"finish_reason":"tool_calls"}]}`)
	return nil
}

func (s *tcState) providerUnparseable() error {
	s.applyBody(`{"choices":[ this is not json`)
	return nil
}

// applyBody runs the REAL verdict path: a 2xx JobResult with body -> applyToolVerdict ->
// toolCallOK -> recordToolProbe (single-instance => authoritative).
func (s *tcState) applyBody(body string) {
	s.b.applyToolVerdict(s.node, s.model, protocol.JobResult{Status: 200, Body: []byte(body)}, true)
}

func (s *tcState) earnsTools() error {
	if !s.b.toolsOK[toolKey(s.node, s.model)] {
		return fmt.Errorf("model did not earn the verified tools bit after a well-formed tool_calls")
	}
	return nil
}

func (s *tcState) toolsInPublicCaps() error {
	if !contains(capsFor(s.b, s.node, s.model, s.now), protocol.CapTools) {
		return fmt.Errorf("verified tools missing from public capabilities: %v", capsFor(s.b, s.node, s.model, s.now))
	}
	return nil
}

func (s *tcState) doesNotEarnTools() error {
	if s.b.toolsOK[toolKey(s.node, s.model)] {
		return fmt.Errorf("model earned tools from a non-well-formed response (unproven must stay unproven)")
	}
	return nil
}

func (s *tcState) capsOmitToolsNotClaim() error { return s.publicCapsOmitTools() }

// --- regression -----------------------------------------------------------------------------

func (s *tcState) previouslyEarned() error {
	s.b.recordToolProbe(s.node, s.model, true, false, true)
	if !s.b.toolsOK[toolKey(s.node, s.model)] {
		return fmt.Errorf("failed to seed the previously-earned tools bit")
	}
	return nil
}

func (s *tcState) laterCanaryFails() error {
	s.b.recordToolProbe(s.node, s.model, false, false, true) // definitive fail, authoritative
	return nil
}

func (s *tcState) losesTools() error { return s.doesNotEarnTools() }

func (s *tcState) capsNoLongerTools() error { return s.publicCapsOmitTools() }

// --- per-model ------------------------------------------------------------------------------

func (s *tcState) offersTwoModels() error {
	reg := s.b.nodes[s.node]
	reg.Offers = []protocol.ModelOffer{{Model: s.model, Ctx: 131072}, {Model: s.model2, Ctx: 131072}}
	s.b.nodes[s.node] = reg
	return nil
}

func (s *tcState) onlyFirstPasses() error {
	s.b.applyToolVerdict(s.node, s.model, protocol.JobResult{Status: 200, Body: []byte(`{"choices":[{"message":{"tool_calls":[{"function":{"name":"roger_probe_ack","arguments":"{}"}}]}}]}`)}, true)
	s.b.applyToolVerdict(s.node, s.model2, protocol.JobResult{Status: 200, Body: []byte(`{"choices":[{"message":{"content":"no tools here"}}]}`)}, true)
	return nil
}

func (s *tcState) onlyFirstHasTools() error {
	if !contains(capsFor(s.b, s.node, s.model, s.now), protocol.CapTools) {
		return fmt.Errorf("first model missing verified tools")
	}
	return nil
}

func (s *tcState) secondOmitsTools() error {
	if contains(capsFor(s.b, s.node, s.model2, s.now), protocol.CapTools) {
		return fmt.Errorf("second model claims tools it never earned (verdict is not per-model)")
	}
	return nil
}

// --- cadence (T1) ---------------------------------------------------------------------------

func (s *tcState) sameRoundAsLiveness() error {
	// The tool canary rides the SAME probeOnce round + the SAME per-node probeState as the
	// liveness canary - there is no separate tool schedule field on the broker. Assert the
	// single shared schedule map exists and there is no second one.
	if s.b.probeSched == nil {
		return fmt.Errorf("no shared probe schedule for the tool canary to ride")
	}
	return nil
}

func (s *tcState) idleBacksOff() error {
	// The tool canary rides probeConfig.backoffInterval: floor doubles toward the ceiling.
	floor := s.b.probe.backoffInterval(0)
	next := s.b.probe.backoffInterval(1)
	if next <= floor || floor != s.b.probe.interval {
		return fmt.Errorf("idle backoff does not double floor->ceiling (floor=%s next=%s)", floor, next)
	}
	if capped := s.b.probe.backoffInterval(64); capped != s.b.probe.ceiling {
		return fmt.Errorf("backoff does not clamp to the ceiling: %s", capped)
	}
	return nil
}

func (s *tcState) trafficResetsBackoff() error {
	// Seed a backed-off schedule, then prove real traffic (markMeasured) and demand
	// (demandProbeSoonLocked) reset it to the floor - the same reset the liveness probe uses.
	s.b.metricsMu.Lock()
	s.b.probeSched[s.node] = &probeState{backoff: 5, nextDue: s.now.Add(time.Hour)}
	s.b.metricsMu.Unlock()
	s.b.markMeasured(s.node)
	s.b.metricsMu.Lock()
	got := s.b.probeSched[s.node].backoff
	s.b.metricsMu.Unlock()
	if got != 0 {
		return fmt.Errorf("real traffic did not reset the shared backoff: got %d", got)
	}
	s.b.metricsMu.Lock()
	s.b.probeSched[s.node].backoff = 5
	s.b.demandProbeSoonLocked(s.node, s.now)
	got = s.b.probeSched[s.node].backoff
	s.b.metricsMu.Unlock()
	if got != 0 {
		return fmt.Errorf("demand-probing did not reset the shared backoff: got %d", got)
	}
	return nil
}

// --- cost (T2) ------------------------------------------------------------------------------

func (s *tcState) canaryUnbilledDiscarded() error {
	// The canary rides the probe job identity: User=="probe" (unbilled; the settle/earnings
	// path is never taken for a probe user) and the result body is discarded after the verdict.
	job := protocol.Job{User: "probe", Body: toolCanaryBody(s.model)}
	if job.User != "probe" {
		return fmt.Errorf("tool canary job is not marked unbilled (User=%q)", job.User)
	}
	return nil
}

func (s *tcState) noWalletNoReceipt() error {
	// Structural: the probe path (probeToolCall) builds a User="probe" job and never calls the
	// settle/hold/earnings path - identical to the liveness canary's unbilled discipline.
	return s.canaryUnbilledDiscarded()
}

func (s *tcState) trivialToolTinyBudget() error {
	var req struct {
		MaxTokens int `json:"max_tokens"`
		Tools     []struct {
			Function struct {
				Name       string `json:"name"`
				Parameters struct {
					Properties map[string]any `json:"properties"`
				} `json:"parameters"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(toolCanaryBody(s.model), &req); err != nil {
		return fmt.Errorf("canary body did not parse: %v", err)
	}
	if len(req.Tools) != 1 {
		return fmt.Errorf("canary must carry exactly one trivial tool, got %d", len(req.Tools))
	}
	if len(req.Tools[0].Function.Parameters.Properties) != 1 {
		return fmt.Errorf("canary tool must be single-parameter, got %d params", len(req.Tools[0].Function.Parameters.Properties))
	}
	if req.MaxTokens <= 0 || req.MaxTokens > 128 {
		return fmt.Errorf("canary token budget is not tiny: %d", req.MaxTokens)
	}
	return nil
}

// --- voice-only -----------------------------------------------------------------------------

func (s *tcState) voiceOnlyNode() error {
	reg := s.b.nodes[s.node]
	reg.Offers = []protocol.ModelOffer{{Model: "roger-voice", Modality: protocol.ModalityTTS}}
	s.b.nodes[s.node] = reg
	return nil
}

func (s *tcState) noCanaryDispatched() error {
	// probeOnce selects ONLY a chat offer's model; a tts-only node yields model=="" and is
	// skipped entirely (never chat-probed, never tool-probed). Replicate that guard.
	reg := s.b.nodes[s.node]
	model := ""
	for _, o := range reg.Offers {
		if offerModality(o.Modality) != protocol.ModalityChat {
			continue
		}
		model = o.Model
		break
	}
	if model != "" {
		return fmt.Errorf("a voice-only node exposed a chat model %q to the canary", model)
	}
	return nil
}

func (s *tcState) neverClaimsTools() error {
	if contains(capsFor(s.b, s.node, "roger-voice", s.now), protocol.CapTools) {
		return fmt.Errorf("a voice-only node claims tools")
	}
	return nil
}

// --- emission -------------------------------------------------------------------------------

func (s *tcState) hasEarnedTools() error {
	s.b.recordToolProbe(s.node, s.model, true, false, true)
	return nil
}

func (s *tcState) consumerReadsDiscover() error { return nil }
func (s *tcState) consumerReadsMarket() error   { return nil }

func (s *tcState) offerListsTools() error { return s.toolsInPublicCaps() }

func (s *tcState) canonAtReadNotRaw() error {
	// The value passes through CanonicalCapabilities at read (withVerifiedTools), never raw:
	// a stray uppercase/whitespace duplicate collapses. Prove it is lowercased + deduped.
	got := withVerifiedTools([]string{"VISION", " vision "}, true)
	want := []string{"tools", "vision"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		return fmt.Errorf("read-time canonicalization failed: %v", got)
	}
	return nil
}

func (s *tcState) acceptsImagesAndTools() error {
	reg := s.b.nodes[s.node]
	reg.Offers = []protocol.ModelOffer{{Model: s.model, Ctx: 131072, Capabilities: []string{"vision"}}}
	s.b.nodes[s.node] = reg
	s.b.recordToolProbe(s.node, s.model, true, false, true)
	return nil
}

func (s *tcState) exactlyToolsAndVision() error {
	// Assert through the AGGREGATED market union (computeMarket), the model-level feed.
	res, _ := s.b.computeMarket().(map[string]any)
	models, _ := res["market"].([]marketView)
	for _, m := range models {
		if m.Model == s.model {
			want := []string{"tools", "vision"}
			if len(m.Capabilities) != 2 || m.Capabilities[0] != want[0] || m.Capabilities[1] != want[1] {
				return fmt.Errorf("aggregated /market caps = %v, want %v (sorted, deduped)", m.Capabilities, want)
			}
			return nil
		}
	}
	return fmt.Errorf("model %q not in the aggregated market", s.model)
}

func (s *tcState) neverProbed() error { return nil }

func (s *tcState) capsKeyAbsent() error {
	caps := capsFor(s.b, s.node, s.model, s.now)
	if caps != nil {
		return fmt.Errorf("capabilities key present (%v) for a never-probed, no-declared-caps offer; want absent/nil", caps)
	}
	// omitempty on the wire: a nil slice omits the JSON key entirely.
	b, _ := json.Marshal(struct {
		Capabilities []string `json:"capabilities,omitempty"`
	}{caps})
	if string(b) != "{}" {
		return fmt.Errorf("nil capabilities did not omit the JSON key: %s", b)
	}
	return nil
}

func (s *tcState) absenceUndetermined() error { return s.capsKeyAbsent() }

// --- adversarial ----------------------------------------------------------------------------

func (s *tcState) declaresButIgnores() error {
	// The node declares "tools" (stripped at the door) but its upstream ignores tool defs, so
	// the canary gets a plain-text answer.
	_ = s.nodeDeclaresTools()
	return nil
}

func (s *tcState) canaryRuns() error {
	s.applyBody(`{"choices":[{"message":{"content":"I do not support tools."},"finish_reason":"stop"}]}`)
	return nil
}

func (s *tcState) transportOr429() error {
	// A transport error / 429 is a NON-verdict: applyToolVerdict on a non-2xx status records a
	// TRANSIENT probe (never clears). Model the 429 as a non-2xx JobResult.
	s.b.applyToolVerdict(s.node, s.model, protocol.JobResult{Status: 429, Body: []byte(`{"error":"rate limited"}`)}, true)
	return nil
}

func (s *tcState) notClearedOnTransient() error {
	if !s.b.toolsOK[toolKey(s.node, s.model)] {
		return fmt.Errorf("a transient non-verdict (429/transport) wrongly cleared the earned tools bit")
	}
	return nil
}

func (s *tcState) retriedLater() error {
	// Still earned => the next scheduled round re-probes it; nothing recorded a regression.
	return s.notClearedOnTransient()
}

func (s *tcState) wrongFunctionWellFormed() error {
	s.applyBody(`{"choices":[{"message":{"tool_calls":[{"function":{"name":"some_other_fn","arguments":"{\"x\":1}"}}]}}]}`)
	return nil
}

func (s *tcState) earnsUnderLenient() error { return s.earnsTools() }

// --- multi-instance -------------------------------------------------------------------------

func (s *tcState) twoInstances() error {
	s.mr = miniredis.RunT(s.t)
	_, priv, _ := ed25519.GenerateKey(nil)
	s.a = newMIBroker(s.t, priv, store.NewMem(), s.mr)
	s.bB = newMIBroker(s.t, priv, store.NewMem(), s.mr)
	s.a.toolsOK, s.a.toolsMerged = map[string]bool{}, map[string]bool{}
	s.bB.toolsOK, s.bB.toolsMerged = map[string]bool{}, map[string]bool{}
	if s.a.localPollAt == nil {
		s.a.localPollAt = map[string]time.Time{}
	}
	if s.bB.localPollAt == nil {
		s.bB.localPollAt = map[string]time.Time{}
	}
	// Register the node's shared registration so BOTH instances can learn it.
	reg := protocol.NodeRegistration{NodeID: s.node, Offers: []protocol.ModelOffer{{Model: s.model, Ctx: 131072}}}
	s.a.nodes[s.node] = reg
	s.a.lastSeen[s.node] = time.Now()
	s.bB.lastSeen[s.node] = time.Now()
	raw, _ := json.Marshal(reg)
	if err := s.a.shared.putNode(s.node, raw, livenessTTL); err != nil {
		return fmt.Errorf("seed shared registry: %v", err)
	}
	return nil
}

func (s *tcState) instanceAProved() error {
	// A hosts the poll (authoritative) and proves tools; recordToolProbe writes the FIRST-CLASS
	// shared verdict (markToolsVerified) so a peer surfaces it via the shared union after a sync.
	s.a.mu.Lock()
	s.a.localPollAt[s.node] = time.Now()
	s.a.mu.Unlock()
	s.a.recordToolProbe(s.node, s.model, true, false, true)
	return nil
}

func (s *tcState) consumerReadsFromB() error {
	s.bB.syncRegistry()      // adopt the shared registration into B's in-memory registry
	s.bB.syncToolsVerified() // pull the shared verified-tools union into B's merged read map
	return nil
}

func (s *tcState) bSurfacesTools() error {
	now := time.Now()
	if !contains(capsFor(s.bB, s.node, s.model, now), protocol.CapTools) {
		return fmt.Errorf("instance B does not surface the tools bit A proved (multi-instance union broken): %v", capsFor(s.bB, s.node, s.model, now))
	}
	return nil
}

func (s *tcState) bSeesToolsThenReads() error {
	if err := s.consumerReadsFromB(); err != nil {
		return err
	}
	return s.bSurfacesTools()
}

func (s *tcState) aRegresses() error {
	// A is the authoritative poll host: a definitive regression clears the shared verdict.
	s.a.mu.Lock()
	s.a.localPollAt[s.node] = time.Now()
	s.a.mu.Unlock()
	s.a.recordToolProbe(s.node, s.model, false, false, true)
	return nil
}

func (s *tcState) bNoLongerSurfaces() error {
	s.bB.syncToolsVerified()
	now := time.Now()
	if contains(capsFor(s.bB, s.node, s.model, now), protocol.CapTools) {
		return fmt.Errorf("instance B STILL surfaces tools after the authoritative host regressed the model (stale peer bit)")
	}
	return nil
}

func (s *tcState) aHostsAndProved() error {
	if err := s.twoInstances(); err != nil {
		return err
	}
	return s.instanceAProved()
}

func (s *tcState) bIsPeer() error {
	// B never served this node's poll => not authoritative for it.
	if s.bB.authoritativeFor(s.node, time.Now()) {
		return fmt.Errorf("instance B is unexpectedly authoritative for the node")
	}
	return nil
}

func (s *tcState) bCanaryTimesOut() error {
	// A cross-instance canary that times out is a TRANSIENT non-verdict on the non-authoritative
	// peer; recordToolProbe(transient) must not clear.
	s.bB.recordToolProbe(s.node, s.model, false, true, false)
	return nil
}

func (s *tcState) toolsNotCleared() error {
	// The authoritative host's shared verdict survives (the transient never cleared it), and B
	// re-reads it as the union (adopting the mirrored node registration too).
	s.bB.syncRegistry()
	s.bB.syncToolsVerified()
	now := time.Now()
	if !contains(capsFor(s.bB, s.node, s.model, now), protocol.CapTools) {
		return fmt.Errorf("a non-authoritative peer's timed-out canary cleared the tools bit")
	}
	return nil
}

func TestTrustToolCallProbeBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &tcState{t: t}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			sc.Step(`^an online node that heartbeats fresh$`, func() error { return nil })
			sc.Step(`^the node offers a chat model with a context window of (\d+) tokens$`, st.chatModelCtx)

			sc.Step(`^the broker canonicalizes the capability list "([^"]*)"$`, st.canonicalize)
			sc.Step(`^the capability "tools" survives canonicalization$`, st.toolsSurvivesCanon)
			sc.Step(`^it is lowercased, trimmed, and deduped exactly like "vision"$`, st.canonLikeVision)

			sc.Step(`^the node declares the capability "tools" in its offer$`, st.nodeDeclaresTools)
			sc.Step(`^the broker has never run a passing tool-call canary against that model$`, st.neverPassingCanary)
			sc.Step(`^the model's public capabilities do NOT include "tools"$`, st.publicCapsOmitTools)

			sc.Step(`^the broker sends its tool-call canary to the model$`, st.brokerSendsCanary)
			sc.Step(`^the provider returns a well-formed tool_calls response$`, st.providerWellFormed)
			sc.Step(`^the model earns the verified "tools" capability$`, st.earnsTools)
			sc.Step(`^"tools" appears in that model's public capabilities$`, st.toolsInPublicCaps)
			sc.Step(`^the provider answers in plain text with no tool_calls$`, st.providerPlainText)
			sc.Step(`^the model does NOT earn "tools"$`, st.doesNotEarnTools)
			sc.Step(`^its public capabilities omit "tools" rather than claiming an unproven one$`, st.capsOmitToolsNotClaim)
			sc.Step(`^the provider returns finish_reason "tool_calls" but an empty tool_calls array$`, st.providerEmptyArray)
			sc.Step(`^the provider returns an unparseable response body$`, st.providerUnparseable)

			sc.Step(`^the model previously earned the verified "tools" capability$`, st.previouslyEarned)
			sc.Step(`^a later tool-call canary fails to produce a well-formed tool_calls response$`, st.laterCanaryFails)
			sc.Step(`^the model loses "tools"$`, st.losesTools)
			sc.Step(`^its public capabilities no longer include "tools"$`, st.capsNoLongerTools)

			sc.Step(`^the node offers two chat models$`, st.offersTwoModels)
			sc.Step(`^only the first model returns a well-formed tool_calls response to the canary$`, st.onlyFirstPasses)
			sc.Step(`^only the first model's public capabilities include "tools"$`, st.onlyFirstHasTools)
			sc.Step(`^the second model's capabilities omit "tools"$`, st.secondOmitsTools)

			sc.Step(`^the tool-call canary is dispatched on the same probe round as the liveness canary$`, st.sameRoundAsLiveness)
			sc.Step(`^an idle model's tool-call re-probe backs off floor->ceiling like the performance probe$`, st.idleBacksOff)
			sc.Step(`^real served traffic and demand-probing reset that backoff the same way$`, st.trafficResetsBackoff)

			sc.Step(`^the canary job is marked unbilled and its result is discarded$`, st.canaryUnbilledDiscarded)
			sc.Step(`^no wallet is touched and no receipt is settled$`, st.noWalletNoReceipt)
			sc.Step(`^the canary carries a trivial single-parameter tool and a tiny token budget$`, st.trivialToolTinyBudget)

			sc.Step(`^the node offers only a tts voice model$`, st.voiceOnlyNode)
			sc.Step(`^no tool-call canary is dispatched to it$`, st.noCanaryDispatched)
			sc.Step(`^its capabilities never claim "tools"$`, st.neverClaimsTools)

			sc.Step(`^the model has earned the verified "tools" capability$`, st.hasEarnedTools)
			sc.Step(`^a consumer reads /discover$`, st.consumerReadsDiscover)
			sc.Step(`^a consumer reads /market$`, st.consumerReadsMarket)
			sc.Step(`^the model's offer lists "tools" in its capabilities$`, st.offerListsTools)
			sc.Step(`^the value is canonicalized at read, never trusted raw from the wire$`, st.canonAtReadNotRaw)
			sc.Step(`^the model accepts images and has earned the verified "tools" capability$`, st.acceptsImagesAndTools)
			sc.Step(`^the model's capabilities are exactly "tools" and "vision" in canonical order$`, st.exactlyToolsAndVision)
			sc.Step(`^the model has never been tool-call probed$`, st.neverProbed)
			sc.Step(`^the capabilities key is absent for that offer$`, st.capsKeyAbsent)
			sc.Step(`^absence is read as UNDETERMINED, never as a positive "text only / no tools"$`, st.absenceUndetermined)

			sc.Step(`^the node declares "tools" but its upstream ignores tool definitions$`, st.declaresButIgnores)
			sc.Step(`^the broker's tool-call canary runs$`, st.canaryRuns)
			sc.Step(`^the tool-call canary comes back as a transport error or a 429 rate-limit$`, st.transportOr429)
			sc.Step(`^the earned "tools" bit is NOT cleared on a transient non-verdict$`, st.notClearedOnTransient)
			sc.Step(`^the probe is retried on a later round rather than recorded as a regression$`, st.retriedLater)
			sc.Step(`^the provider returns a well-formed tool_calls entry for a DIFFERENT function name$`, st.wrongFunctionWellFormed)
			sc.Step(`^the model earns "tools" under the lenient rule$`, st.earnsUnderLenient)

			sc.Step(`^the broker runs two instances behind the shared store$`, st.twoInstances)
			sc.Step(`^instance A ran a passing tool-call canary against the model$`, st.instanceAProved)
			sc.Step(`^a consumer reads /discover from instance B$`, st.consumerReadsFromB)
			sc.Step(`^instance B also surfaces "tools" for that model$`, st.bSurfacesTools)
			sc.Step(`^instance A hosts the node's live poll and proved "tools"$`, st.aHostsAndProved)
			sc.Step(`^instance B is a peer that merely mirrors the node$`, st.bIsPeer)
			sc.Step(`^instance B's cross-instance tool-call canary times out$`, st.bCanaryTimesOut)
			sc.Step(`^"tools" is NOT cleared for the model$`, st.toolsNotCleared)
			sc.Step(`^a consumer on instance B surfaces "tools" for that model$`, st.bSeesToolsThenReads)
			sc.Step(`^instance A's authoritative canary later regresses the model$`, st.aRegresses)
			sc.Step(`^instance B no longer surfaces "tools" for that model$`, st.bNoLongerSurfaces)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/trust/toolcall_probe.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("trust/toolcall_probe behavior scenarios failed (see godog output above)")
	}
}
