package main

// concierge_provenlive_test.go is the EXHAUSTIVE unit table for the concierge pick's
// fail-fast gate conciergeProvenLiveLocked - every permutation of the proven-live predicate
// the free-station + grant picks share, including the defensive corners (verified-but-no-
// schedule, probe disabled). The .feature covers the behaviour through the real picks/relay;
// this pins the predicate itself branch-by-branch. No mocks: it drives the real trustState +
// probeState + probeConfig.measurementStale seams.

import (
	"testing"
	"time"
)

func TestConciergeProvenLiveLocked(t *testing.T) {
	const ceiling = 15 * time.Minute
	now := time.Now()

	// probeEnabled builds a broker with the active probe on (so the gate is LIVE), one
	// node's trust set by tq, a successCount (a quality-validated real relay is proven-live
	// too), and (when withSched) a probeState stamped at measuredAt.
	newB := func(probeOn bool, tq trustState, successCount int, withSched bool, measuredAt time.Time) *broker {
		b := &broker{
			trust:        map[string]trustState{"n": tq},
			successCount: map[string]int{"n": successCount},
			probeSched:   map[string]*probeState{},
		}
		if probeOn {
			b.probe = probeConfig{interval: 30 * time.Second, ceiling: ceiling}
		} // else zero probeConfig => enabled()==false
		if withSched {
			b.probeSched["n"] = &probeState{lastMeasured: measuredAt}
		}
		return b
	}

	verified := trustState{probed: true, probeOK: true, probeFails: 0} // verifiedServing()==true

	cases := []struct {
		name       string
		probeOn    bool
		tq         trustState
		success    int // quality-validated real relays (successCount)
		withSched  bool
		measuredAt time.Time
		want       bool
	}{
		{
			name:    "probe disabled: inert, legacy heartbeat pick (proven-live true regardless of trust)",
			probeOn: false, tq: trustState{}, withSched: false,
			want: true,
		},
		{
			name:    "probe disabled with a dead node: still true (gate is a no-op when probing is off)",
			probeOn: false, tq: trustState{probed: true, probeOK: false, probeFails: 9}, withSched: false,
			want: true,
		},
		{
			name:    "never probed: not verified -> not proven-live",
			probeOn: true, tq: trustState{}, withSched: false,
			want: false,
		},
		{
			name:    "probed but last canary FAILED: not verified -> not proven-live",
			probeOn: true, tq: trustState{probed: true, probeOK: false, probeFails: 0}, withSched: true, measuredAt: now,
			want: false,
		},
		{
			name:    "verified reading but a NON-ZERO failure streak: not proven-live",
			probeOn: true, tq: trustState{probed: true, probeOK: true, probeFails: 2}, withSched: true, measuredAt: now,
			want: false,
		},
		{
			name:    "verified but NO probe schedule entry (defensive): not proven-live",
			probeOn: true, tq: verified, withSched: false,
			want: false,
		},
		{
			name:    "verified but STALE (measured two ceilings ago): not proven-live",
			probeOn: true, tq: verified, withSched: true, measuredAt: now.Add(-2 * ceiling),
			want: false,
		},
		{
			name:    "verified AND fresh (measured just now): PROVEN-LIVE",
			probeOn: true, tq: verified, withSched: true, measuredAt: now,
			want: true,
		},
		{
			name:    "verified AND exactly at the ceiling edge (not yet stale): PROVEN-LIVE",
			probeOn: true, tq: verified, withSched: true, measuredAt: now.Add(-ceiling),
			want: true,
		},
		{
			// The false-skip guard: a busy node serving real paid traffic but NOT yet
			// canary-probed is DEMONSTRABLY alive (successCount>0 + fresh markMeasured stamp).
			name:    "never canary-probed but a FRESH successful real relay: PROVEN-LIVE",
			probeOn: true, tq: trustState{}, success: 1, withSched: true, measuredAt: now,
			want: true,
		},
		{
			name:    "successful real relay but STALE (measured two ceilings ago): not proven-live",
			probeOn: true, tq: trustState{}, success: 3, withSched: true, measuredAt: now.Add(-2 * ceiling),
			want: false,
		},
		{
			name:    "successful real relay but NO schedule entry (defensive): not proven-live",
			probeOn: true, tq: trustState{}, success: 2, withSched: false,
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := newB(tc.probeOn, tc.tq, tc.success, tc.withSched, tc.measuredAt)
			b.mu.Lock() // the gate documents the caller holds b.mu; hold it as the picks do
			got := b.conciergeProvenLiveLocked("n", now)
			b.mu.Unlock()
			if got != tc.want {
				t.Errorf("conciergeProvenLiveLocked = %v, want %v", got, tc.want)
			}
		})
	}
}
