package main

// RE-PROVING A SETTLED VERDICT IS NOT THE SAME JOB AS EARNING IT.
//
// The tool canary rode the liveness round exactly - one canary per chat model, every round -
// so an operator's unbilled probe cost grew with the number of models they shared, which is
// the behaviour the product wants to encourage. Tool-calling is a near-static property of a
// model plus its runtime, so re-asserting it at liveness cadence bought nothing.

import (
	"testing"
	"time"
)

// Earning the bit must stay fast: a model that has never passed is probed every round, so it
// becomes publicly "tools"-capable as quickly as it always did.
func TestAnUnverifiedModelIsNotThrottled(t *testing.T) {
	b := adaptiveBroker(30*time.Second, 15*time.Minute)
	now := time.Now()

	for i := 0; i < 5; i++ {
		if !b.toolProbeDue("n1", "m1", now.Add(time.Duration(i)*30*time.Second)) {
			t.Fatalf("round %d: an unverified model was throttled - it would take longer to "+
				"earn the tools bit than before", i)
		}
	}
}

// Once earned, re-proving slows to toolProbeEvery.
func TestAVerifiedModelIsReProvedOnItsOwnSlowerCadence(t *testing.T) {
	b := adaptiveBroker(30*time.Second, 15*time.Minute)
	now := time.Now()

	b.metricsMu.Lock()
	b.toolsOK = map[string]bool{toolKey("n1", "m1"): true}
	b.metricsMu.Unlock()

	if !b.toolProbeDue("n1", "m1", now) {
		t.Fatal("the first probe after verification should still run (nothing stamped yet)")
	}
	if b.toolProbeDue("n1", "m1", now.Add(15*time.Minute)) {
		t.Error("a verified model was re-probed after 15m; the throttle is 20m")
	}
	if !b.toolProbeDue("n1", "m1", now.Add(21*time.Minute)) {
		t.Error("a verified model was not re-probed after 21m - past the throttle it must run")
	}
}

// THE SAFETY BOUND. A verified model ages out of the union as UNDETERMINED unless a passing
// canary re-marks it within toolsVerifiedTTL. The throttle must therefore stay comfortably
// under that, WITH room for rounds landing on the 15m ceiling rather than exactly on time.
func TestTheThrottleStaysUnderTheVerifiedTTL(t *testing.T) {
	// Worst case: the throttle expires just after a round, so the next chance is one full
	// ceiling later.
	worst := toolProbeEvery + 15*time.Minute
	if worst >= toolsVerifiedTTL {
		t.Fatalf("re-verification can take up to %s but the verified bit ages out at %s - "+
			"a healthy model would silently lose its tools capability", worst, toolsVerifiedTTL)
	}
	t.Logf("re-verification lands within %s, against a %s TTL (%s margin)",
		worst, toolsVerifiedTTL, toolsVerifiedTTL-worst)
}

// The saving, measured on the same terms as the liveness cadence work.
func TestToolThrottleCutsCostForAMultiModelNode(t *testing.T) {
	const day = 24 * time.Hour
	rounds := int(day / (15 * time.Minute)) // an idle node at the ceiling

	for _, models := range []int{1, 4} {
		before := rounds * (1 + models) // liveness + one tool canary per model, every round
		// After: a verified model is re-probed at most once per toolProbeEvery, which with
		// 15m rounds lands every other round.
		toolProbes := int(day/toolProbeEvery) * models
		if toolProbes > rounds*models {
			toolProbes = rounds * models
		}
		after := rounds + toolProbes
		if after >= before {
			t.Errorf("%d models: throttle saved nothing (%d -> %d)", models, before, after)
		}
		t.Logf("%d shared model(s): %d -> %d unbilled requests/day (%.0f%% less)",
			models, before, after, 100*(1-float64(after)/float64(before)))
	}
}
