package main

// HOW MUCH UNBILLED WORK DO WE ACTUALLY PUT ON AN OPERATOR'S GPU?
//
// A station reported 2,738 served requests against 48,001 output tokens - 17.5 tokens a
// request, which is a canary, not a conversation. Before changing any cadence, measure the
// one we have, using the REAL schedule arithmetic rather than a restatement of it.
//
// The unit that matters is REQUESTS THE NODE SEES, not probe rounds. Those are not the
// same number, and the gap is the finding: one round fires a liveness canary AND a
// tool-call canary for EVERY chat model the node offers (probe.go, end of probeOnce). A
// node sharing one model gets 2 requests per round; one sharing four gets 5.

import (
	"testing"
	"time"
)

// simulate replays the production scheduler over `span` and returns how many requests the
// node actually receives. The two mutating lines are copied verbatim from probeOnce's
// backoff-advance block, and the interval comes from the real backoffInterval - so this
// tracks the shipped behaviour rather than a description of it.
//
// trafficEvery models real traffic resetting the backoff (markMeasured); zero means a
// permanently idle node.
func simulate(cfg probeConfig, span, trafficEvery time.Duration, chatModels int) (rounds, requests int) {
	return simulateV(cfg, span, trafficEvery, chatModels, "reset")
}

// simulateV compares what real traffic should do to the schedule:
//
//	"reset"  - today: traffic zeroes the backoff, so the node returns to the 30s floor.
//	"defer"  - traffic pushes the next probe out by the current interval, backoff kept.
//	"counts" - traffic IS this round's measurement: push out AND advance the backoff,
//	           exactly as a probe would.
func simulateV(cfg probeConfig, span, trafficEvery time.Duration, chatModels int, mode string) (rounds, requests int) {
	now := time.Time{}.Add(time.Hour) // any non-zero base
	end := now.Add(span)
	st := &probeState{}
	lastTraffic := now
	lastProbe := now

	for t := now; t.Before(end); t = t.Add(cfg.interval) { // the loop ticks at the FLOOR
		if trafficEvery > 0 && t.Sub(lastTraffic) >= trafficEvery {
			switch mode {
			case "reset": // markMeasured today
				st.backoff = 0
				if due := t.Add(cfg.interval); due.After(st.nextDue) {
					st.nextDue = due
				}
			case "defer":
				if due := t.Add(cfg.backoffInterval(st.backoff)); due.After(st.nextDue) {
					st.nextDue = due
				}
			case "defer-capped":
				// Defer, but never past one ceiling since the last REAL probe, so the
				// tool-call verdict still refreshes on a bounded schedule.
				due := t.Add(cfg.backoffInterval(st.backoff))
				if cap := lastProbe.Add(cfg.ceiling); due.After(cap) {
					due = cap
				}
				if due.After(st.nextDue) {
					st.nextDue = due
				}
			case "counts":
				if due := t.Add(cfg.backoffInterval(st.backoff)); due.After(st.nextDue) {
					st.nextDue = due
				}
				if st.backoff < 64 {
					st.backoff++
				}
			}
			lastTraffic = t
		}
		if !st.nextDue.IsZero() && st.nextDue.After(t) {
			continue // backed off: not due this round
		}
		// --- verbatim from probeOnce ---
		st.nextDue = t.Add(cfg.backoffInterval(st.backoff))
		if st.backoff < 64 {
			st.backoff++
		}
		// --- end ---
		lastProbe = t
		rounds++
		requests += 1 + chatModels // liveness canary + one tool-call canary per chat model
	}
	return rounds, requests
}

// The headline measurement. Nothing is asserted about "too much" here - this prints the
// table so a cadence decision is made against numbers instead of intuition.
func TestMeasureProbeCostPerDay(t *testing.T) {
	cfg := probeConfig{interval: 30 * time.Second, ceiling: 15 * time.Minute}
	day := 24 * time.Hour

	t.Log("unbilled requests landing on ONE node per DAY (30s floor -> 15m ceiling):")
	t.Log("  traffic         models   rounds   requests")
	for _, tc := range []struct {
		name    string
		traffic time.Duration
		models  int
	}{
		{"idle", 0, 1},
		{"idle", 0, 4},
		{"used hourly", time.Hour, 1},
		{"used every 10m", 10 * time.Minute, 1},
		{"used every 10m", 10 * time.Minute, 4},
		{"used every 2m", 2 * time.Minute, 1},
	} {
		rounds, reqs := simulate(cfg, day, tc.traffic, tc.models)
		t.Logf("  %-15s %6d   %6d   %8d", tc.name, tc.models, rounds, reqs)
	}
}

// A PERMANENTLY IDLE node must actually reach the ceiling and stay there. If the backoff
// ever failed to advance, an idle rig would be probed at the 30s floor forever - 2,880
// rounds a day instead of ~96 - and this is the guard that would catch it.
func TestAnIdleNodeCollapsesToTheCeiling(t *testing.T) {
	cfg := probeConfig{interval: 30 * time.Second, ceiling: 15 * time.Minute}
	rounds, _ := simulate(cfg, 24*time.Hour, 0, 1)

	// 24h at a 15m ceiling is 96 rounds, plus the handful of fast rounds while the
	// backoff is still climbing (30s+60s+2m+4m+8m ~ 15m of warm-up).
	if rounds > 110 {
		t.Errorf("an idle node was probed %d times in a day; the 15m ceiling allows ~96. "+
			"The adaptive backoff is not reaching the ceiling.", rounds)
	}
	if rounds < 90 {
		t.Errorf("an idle node was probed only %d times in a day - liveness would be "+
			"detected too slowly", rounds)
	}
}

// THE FINDING, made permanent.
//
// The tool-call canary rides the liveness schedule and fires PER CHAT MODEL, so probe cost
// scales with how many models an operator shares. Sharing more models is the behaviour the
// product wants to encourage, and it is exactly what makes the unbilled load grow.
func TestProbeCostScalesWithTheNumberOfSharedModels(t *testing.T) {
	cfg := probeConfig{interval: 30 * time.Second, ceiling: 15 * time.Minute}

	_, one := simulate(cfg, 24*time.Hour, 0, 1)
	_, four := simulate(cfg, 24*time.Hour, 0, 4)

	if four <= one {
		t.Fatal("sharing more models did not change probe cost - the per-model tool canary is gone?")
	}
	t.Logf("sharing 1 model costs %d unbilled requests/day; sharing 4 costs %d (%.1fx)",
		one, four, float64(four)/float64(one))
}

// THE COMPARISON that decides the cadence change.
//
// Real traffic is the best measurement there is: the node did actual work, and the reading
// it produced is stamped by the same markMeasured call. Today that ALSO zeroes the probe
// backoff, so the node drops back to the 30s floor - we probe hardest exactly where we have
// the most real evidence, and an operator is charged unbilled GPU time for being useful.
//
// Liveness does not depend on this. A dead node goes offline within nodeTTL (45s) via the
// markSeen heartbeat, which is 20x tighter than the probe ceiling and entirely separate.
func TestRealTrafficShouldNotAccelerateProbing(t *testing.T) {
	cfg := probeConfig{interval: 30 * time.Second, ceiling: 15 * time.Minute}
	day := 24 * time.Hour

	t.Log("unbilled requests/day on a node with 1 shared model:")
	t.Log("  traffic          reset(today)   defer   counts")
	for _, traffic := range []time.Duration{0, time.Hour, 10 * time.Minute, 2 * time.Minute} {
		_, a := simulateV(cfg, day, traffic, 1, "reset")
		_, b := simulateV(cfg, day, traffic, 1, "defer")
		_, c := simulateV(cfg, day, traffic, 1, "counts")
		name := "idle"
		if traffic > 0 {
			name = "every " + traffic.String()
		}
		t.Logf("  %-14s %10d %8d %8d", name, a, b, c)
	}

	// The perverse result, stated as an assertion: being USED costs an operator more
	// unbilled work than sitting idle.
	_, idle := simulateV(cfg, day, 0, 1, "reset")
	_, used := simulateV(cfg, day, 10*time.Minute, 1, "reset")
	if used <= idle {
		t.Fatal("expected today's reset behaviour to probe a USED node harder than an idle one")
	}
	t.Logf("today: an idle node costs %d unbilled req/day, a node used every 10m costs %d (%.1fx MORE for being useful)",
		idle, used, float64(used)/float64(idle))
}

// The chosen policy has to be cheap AND bounded: a heavily-used node must still be probed
// on a schedule, because the tool-call verdict refreshes only inside a probe round and
// real traffic does not assert it.
func TestCappedDeferStaysBoundedUnderHeavyTraffic(t *testing.T) {
	cfg := probeConfig{interval: 30 * time.Second, ceiling: 15 * time.Minute}
	day := 24 * time.Hour

	t.Log("unbilled requests/day, 1 shared model:")
	t.Log("  traffic         reset(today)   defer   defer-capped")
	for _, traffic := range []time.Duration{0, time.Hour, 10 * time.Minute, 2 * time.Minute} {
		_, a := simulateV(cfg, day, traffic, 1, "reset")
		_, b := simulateV(cfg, day, traffic, 1, "defer")
		_, c := simulateV(cfg, day, traffic, 1, "defer-capped")
		name := "idle"
		if traffic > 0 {
			name = "every " + traffic.String()
		}
		t.Logf("  %-14s %10d %8d %12d", name, a, b, c)
	}

	// Bounded: even under constant traffic the node is still probed at the ceiling rate,
	// so the tool verdict cannot go unrefreshed forever.
	rounds, _ := simulateV(cfg, day, 2*time.Minute, 1, "defer-capped")
	if rounds < 90 {
		t.Errorf("a busy node was probed only %d times/day - the tool-call verdict would "+
			"go stale and never refresh", rounds)
	}
	// And cheaper than today.
	_, today := simulateV(cfg, day, 2*time.Minute, 1, "reset")
	_, capped := simulateV(cfg, day, 2*time.Minute, 1, "defer-capped")
	if capped >= today {
		t.Errorf("capped defer (%d) is no cheaper than today (%d)", capped, today)
	}
	t.Logf("busy node: %d -> %d unbilled req/day (%.1fx less)", today, capped, float64(today)/float64(capped))
}

// END-TO-END on the SHIPPED markMeasured, not the simulation: drive a day of traffic
// through the real function and count the probe rounds it leaves due.
//
// The simulation above models the arithmetic; this proves the deployed code agrees with it.
func TestShippedMarkMeasuredKeepsProbeCostFlatUnderTraffic(t *testing.T) {
	for _, traffic := range []time.Duration{time.Hour, 10 * time.Minute, 2 * time.Minute} {
		b := adaptiveBroker(30*time.Second, 15*time.Minute)
		id := "busy"
		start := time.Now()
		b.probeSched[id] = &probeState{lastProbe: start}

		rounds := 0
		for t0 := start; t0.Before(start.Add(24 * time.Hour)); t0 = t0.Add(30 * time.Second) {
			b.metricsMu.Lock()
			st := b.probeSched[id]
			due := st.nextDue
			if due.IsZero() || !due.After(t0) {
				// This is the round firing: mirror probeOnce's advance.
				st.lastProbe = t0
				st.nextDue = t0.Add(b.probe.backoffInterval(st.backoff))
				if st.backoff < 64 {
					st.backoff++
				}
				rounds++
			}
			b.metricsMu.Unlock()
			if int(t0.Sub(start)/traffic) != int(t0.Add(-30*time.Second).Sub(start)/traffic) {
				b.markMeasured(id) // the SHIPPED function
			}
		}

		// ~96/day is the ceiling rate. Before the fix a node used every 2m hit 1,440 rounds.
		if rounds > 110 {
			t.Errorf("traffic every %s -> %d probe rounds/day; the ceiling allows ~96. "+
				"Real traffic is accelerating probing again.", traffic, rounds)
		}
		t.Logf("traffic every %-8s -> %3d probe rounds/day (%d unbilled requests on a 1-model node)",
			traffic, rounds, rounds*2)
	}
}
