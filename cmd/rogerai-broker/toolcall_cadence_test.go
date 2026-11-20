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

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/store"
)

// Earning the bit starts at once: a model's first tool-call check runs on its first round. After
// a check without a pass it waits out the back-off (probe_accounting.feature R3) instead of being
// checked every round for as long as it is online, which is what a model that cannot do tool
// calls, a phone's on-device model for one, used to cost.
func TestAnUnverifiedModelIsCheckedAtOnceThenBacksOff(t *testing.T) {
	b := adaptiveBroker(30*time.Second, 15*time.Minute)
	now := time.Now()
	if !b.toolProbeDue("n1", "m1", now) {
		t.Fatal("the first tool-call check of a model did not run at once")
	}
	// Due but never sent (a spent budget, no tunnel) is not a miss: still due.
	if !b.toolProbeDue("n1", "m1", now.Add(time.Second)) {
		t.Fatal("a check that was never sent counted as a miss")
	}
	b.toolCheckSent("n1", "m1", now)
	for i := 1; i < 5; i++ {
		if b.toolProbeDue("n1", "m1", now.Add(time.Duration(i)*30*time.Second)) {
			t.Fatalf("round %d: a model that just failed was checked again inside its back-off", i)
		}
	}
	if !b.toolProbeDue("n1", "m1", now.Add(toolProbeEvery)) {
		t.Fatal("the second check was not due after the 20 minute back-off")
	}
}

// Once earned, the full tool-call re-check runs once a day (founder ruling 2026-11-15, fitting the
// 60-a-day check budget); between re-checks each passed canary re-asserts the verified mark.
func TestAVerifiedModelIsReProvedOnceADay(t *testing.T) {
	b := adaptiveBroker(30*time.Second, 30*time.Minute)
	now := time.Now()

	b.metricsMu.Lock()
	b.toolsOK = map[string]bool{toolKey("n1", "m1"): true}
	b.metricsMu.Unlock()

	if !b.toolProbeDue("n1", "m1", now) {
		t.Fatal("the first probe after verification should still run (nothing stamped yet)")
	}
	if b.toolProbeDue("n1", "m1", now.Add(23*time.Hour)) {
		t.Error("a verified model was fully re-checked inside a day")
	}
	if !b.toolProbeDue("n1", "m1", now.Add(toolReverifyEvery)) {
		t.Error("a verified model was not re-checked after a day")
	}
}

// THE SAFETY BOUND. A verified model ages out of the union as UNDETERMINED unless something
// re-marks it within toolsVerifiedTTL. Each passed canary does, and an idle node's canaries come
// at least once a probe ceiling, so the ceiling must stay comfortably under the TTL.
func TestTheCanaryKeepsAVerifiedMarkFresh(t *testing.T) {
	if defaultProbeCeiling >= toolsVerifiedTTL {
		t.Fatalf("canaries come every %s but the verified mark ages out at %s - a healthy model "+
			"would silently lose its tools capability", defaultProbeCeiling, toolsVerifiedTTL)
	}
}

// The cost, on the terms of the check budget: an idle node fits in a day's budget with room.
func TestAnIdleNodeFitsTheDailyCheckBudget(t *testing.T) {
	const day = 24 * time.Hour
	rounds := int(day / defaultProbeCeiling) // an idle node at the ceiling
	for _, models := range []int{1, 2} {
		checks := rounds + int(day/toolReverifyEvery)*models
		if checks > checkBudgetDay {
			t.Errorf("%d model(s): an idle node needs %d checks a day, over the %d budget", models, checks, checkBudgetDay)
		}
		t.Logf("%d shared model(s): %d checks/day against a budget of %d", models, checks, checkBudgetDay)
	}
}

// A TRANSIENT NON-VERDICT MUST NOT CONSUME THE RE-VERIFICATION WINDOW.
//
// toolProbeDue stamps the attempt BEFORE the canary's outcome is known, so two rounds cannot
// probe the same model at once. That is right, but it means a 429 or a timeout would push the
// retry out by the full toolProbeEvery even though nothing was learned - and a verified model
// whose canary keeps timing out could age past toolsVerifiedTTL and flap to UNDETERMINED
// while perfectly healthy. recordToolProbe's contract is "transient -> retry next round", and
// the throttle has to honour it.
func TestATransientCanaryDoesNotBurnTheThrottleWindow(t *testing.T) {
	b := adaptiveBroker(30*time.Second, 15*time.Minute)
	now := time.Now()

	b.metricsMu.Lock()
	b.toolsOK = map[string]bool{toolKey("n1", "m1"): true}
	b.metricsMu.Unlock()

	if !b.toolProbeDue("n1", "m1", now) {
		t.Fatal("the first probe should run")
	}
	// The canary neither passed nor failed - no answer at all.
	b.recordToolProbe("n1", "m1", false, true /*transient*/, true)

	if !b.toolProbeDue("n1", "m1", now.Add(time.Second)) {
		t.Fatal("a transient non-verdict consumed the 20m throttle window: a healthy model " +
			"whose canary times out would stop being re-proven and could age out of the " +
			"verified union while nothing was ever actually learned about it")
	}
}

// A DEFINITIVE verdict does consume it - that is the whole point of the throttle.
func TestADefinitiveVerdictHoldsTheThrottleWindow(t *testing.T) {
	b := adaptiveBroker(30*time.Second, 15*time.Minute)
	now := time.Now()

	b.metricsMu.Lock()
	b.toolsOK = map[string]bool{toolKey("n1", "m1"): true}
	b.metricsMu.Unlock()

	b.toolProbeDue("n1", "m1", now)
	b.recordToolProbe("n1", "m1", true, false, true) // a clean pass

	if b.toolProbeDue("n1", "m1", now.Add(5*time.Minute)) {
		t.Error("a settled verdict was re-proven after 5m; the throttle is 20m")
	}
}

// A transient canary must not merely free the throttle - it must bring the retry FORWARD.
// The next probe round can be a full ceiling away, so clearing the stamp alone left the
// worst case at 50m against a 45m TTL.
func TestATransientCanaryPullsTheNextProbeForward(t *testing.T) {
	b := adaptiveBroker(30*time.Second, 15*time.Minute)
	now := time.Now()

	b.metricsMu.Lock()
	b.toolsOK = map[string]bool{toolKey("n1", "m1"): true}
	// Deeply backed off: without a pull-in the next round is a ceiling away.
	b.probeSched["n1"] = &probeState{backoff: 20, nextDue: now.Add(15 * time.Minute), lastProbe: now}
	b.metricsMu.Unlock()

	b.toolProbeDue("n1", "m1", now)
	b.recordToolProbe("n1", "m1", false, true /*transient*/, true)

	b.metricsMu.Lock()
	due := b.probeSched["n1"].nextDue
	b.metricsMu.Unlock()
	if due.After(now.Add(time.Second)) {
		t.Errorf("after a transient the next probe is still %s out; the retry has to come "+
			"forward or a verified model can age past toolsVerifiedTTL having learned nothing",
			due.Sub(now))
	}
}

// A tool-capable model that was cold on its first check is not made to wait out the back-off: its
// first transient miss is forgiven; a second one counts.
func TestAFirstTransientMissIsForgiven(t *testing.T) {
	b := adaptiveBroker(30*time.Second, 30*time.Minute)
	now := time.Now()
	b.toolProbeDue("n1", "m1", now)
	b.toolCheckSent("n1", "m1", now)
	b.recordToolProbe("n1", "m1", false, true /*transient*/, true)
	if !b.toolProbeDue("n1", "m1", now.Add(30*time.Second)) {
		t.Fatal("a first transient miss made a model wait out the back-off")
	}
	// Send, miss, send, miss: the forgiveness was used, so the second miss counts.
	b.toolCheckSent("n1", "m1", now.Add(30*time.Second))
	b.recordToolProbe("n1", "m1", false, true, true)
	if b.toolProbeDue("n1", "m1", now.Add(time.Minute)) {
		t.Fatal("repeated transient misses were all forgiven: the round-by-round loop is back")
	}
	// And a pass resets it, forgiveness included.
	b.recordToolProbe("n1", "m1", true, false, true)
	b.metricsMu.Lock()
	_, kept := b.toolFails[toolKey("n1", "m1")]
	b.metricsMu.Unlock()
	if kept {
		t.Fatal("a pass did not end the back-off")
	}
}

// A verified model's re-check the budget refused gives its window back: it was never sent.
func TestARefusedReCheckGivesItsWindowBack(t *testing.T) {
	b := relayBroker(store.NewMem())
	b.toolsOK = map[string]bool{toolKey("tnode", "m"): true}
	probeReg(b, "tnode")
	now := time.Now()
	for i := 0; i < checkBudgetHour; i++ {
		b.allowCheck("tnode", "canary")
	}
	require.True(t, b.toolProbeDue("tnode", "m", now))
	b.probeToolCall(b.nodes["tnode"], "m", true) // refused by the spent budget
	require.True(t, b.toolProbeDue("tnode", "m", now.Add(time.Minute)), "a refused re-check held the day's window")
}
