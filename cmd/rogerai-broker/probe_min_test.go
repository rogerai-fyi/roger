package main

// probe_min_test.go: the OPERATOR-DECLARED MINIMUM PROBE INTERVAL (registration field
// probe_min_s). An operator whose upstream bills real money per request opts in to being
// probed no more often than they declare, capped at ROGERAI_PROBE_MIN_CAP (24h default).
// The cost of declaring it is the anti-gaming core: verification is never extended, so
// between probes the node reads as NOT currently verified, and the measurement staleness
// discount keeps ranking it below fresh nodes. Real dependencies only (miniredis for the
// shared verdict store), no mocks.

import (
	"bytes"
	"log"
	"math"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/protocol"
)

const sixHours = 6 * 3600 // a declared probe_min_s

// probeMinBroker is adaptiveBroker plus the maps probeOnce, pickFor and the market read
// touch, with the default 24h cap.
func probeMinBroker() *broker {
	b := adaptiveBroker(30*time.Second, 15*time.Minute)
	b.probe.minCap = defaultProbeMinCap
	b.tunnels = map[string]*nodeTunnel{}
	b.successCount = map[string]int{}
	b.concurrentTPS = map[string]float64{}
	return b
}

func addNode(b *broker, id string, probeMinS int) {
	b.nodes[id] = protocol.NodeRegistration{NodeID: id, ProbeMinSeconds: probeMinS,
		Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 1, PriceOut: 1}}}
	b.lastSeen[id] = time.Now()
	// A passed, completed canary: the node is verified-serving.
	b.trust[id] = trustState{probed: true, probeOK: true, probeCompleted: true, probes: 1}
}

func lastProbeOf(b *broker, id string) time.Time {
	b.metricsMu.Lock()
	defer b.metricsMu.Unlock()
	if st := b.probeSched[id]; st != nil {
		return st.lastProbe
	}
	return time.Time{}
}

// --- the clamp ------------------------------------------------------------------------

func TestEffectiveProbeMinClamp(t *testing.T) {
	day := 24 * time.Hour
	for _, tc := range []struct {
		name     string
		cap      time.Duration
		declared int
		want     time.Duration
		clamped  bool
	}{
		{"undeclared", day, 0, 0, false},
		{"negative is zero", day, -1, 0, false},
		{"very negative is zero", day, math.MinInt, 0, false},
		{"one second", day, 1, time.Second, false},
		{"six hours", day, sixHours, 6 * time.Hour, false},
		{"exactly the cap", day, 86400, day, false},
		{"one past the cap", day, 86401, day, true},
		{"a year", day, 365 * 86400, day, true},
		{"overflow bait", day, math.MaxInt, day, true},
		{"a lower cap binds", time.Hour, sixHours, time.Hour, true},
		{"cap zero turns the lane off", 0, sixHours, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, clamped := probeConfig{minCap: tc.cap}.effectiveProbeMin(tc.declared)
			require.Equal(t, tc.want, got)
			require.Equal(t, tc.clamped, clamped)
		})
	}
}

func TestLoadProbeMinCap(t *testing.T) {
	for _, tc := range []struct {
		env  string
		want time.Duration
	}{
		{"", 24 * time.Hour},
		{"3600", time.Hour},
		{"0", 0},
		{"-5", 24 * time.Hour},
		{"six hours", 24 * time.Hour},
		{"99999999999999999", 24 * time.Hour}, // would overflow time.Duration: ignored
	} {
		t.Run("env="+tc.env, func(t *testing.T) {
			t.Setenv("ROGERAI_PROBE_INTERVAL", "30")
			t.Setenv("ROGERAI_PROBE_MIN_CAP", tc.env)
			require.Equal(t, tc.want, loadProbe().minCap)
		})
	}
}

// --- scheduling: never sooner than the declared minimum, by ANY path -------------------

func TestProbeMinHoldsAgainstEveryPath(t *testing.T) {
	paths := []struct {
		name string
		run  func(b *broker, id string)
	}{
		{"adaptive backoff (due on the floor)", func(b *broker, id string) {}},
		{"market browse demand", func(b *broker, id string) {
			b.metricsMu.Lock()
			for i := 0; i < 50; i++ {
				b.demandProbeSoonLocked(id, time.Now())
			}
			b.metricsMu.Unlock()
		}},
		{"stale market read", func(b *broker, id string) { _ = b.computeMarket() }},
		{"stale discover read", func(b *broker, id string) {
			b.mu.Lock()
			_ = b.enrichOffersForNode(nil, b.nodes[id], time.Now(), nil, true)
			b.mu.Unlock()
		}},
		{"stale-candidate pick", func(b *broker, id string) {
			b.mu.Lock()
			_, _, ok := b.pickFor("m", false, 0, 0, 0, "", nil, nil, nil, pickReq{})
			b.mu.Unlock()
			require.True(t, ok)
		}},
		{"transient tool canary retry", func(b *broker, id string) {
			b.recordToolProbe(id, "m", false, true /*transient*/, true)
		}},
	}
	for _, p := range paths {
		for _, tc := range []struct {
			since  time.Duration
			probed bool
		}{
			{time.Hour, false},    // inside the declared 6h: held
			{7 * time.Hour, true}, // past it: the ordinary schedule applies
			{5*time.Hour + 59*time.Minute, false},
		} {
			t.Run(p.name+"/last probe "+tc.since.String()+" ago", func(t *testing.T) {
				b := probeMinBroker()
				addNode(b, "n", sixHours)
				now := time.Now()
				last := now.Add(-tc.since)
				// Stale measurement and a due, floor-level schedule: every lever that could
				// pull a probe in is pulled.
				b.probeSched["n"] = &probeState{lastProbe: last, lastMeasured: last, nextDue: now.Add(-time.Second)}

				p.run(b, "n")
				b.probeOnce()

				if tc.probed {
					require.True(t, lastProbeOf(b, "n").After(last), "past the declared minimum the node must be probed")
				} else {
					require.Equal(t, last, lastProbeOf(b, "n"), "probed %s after the last probe against a declared 6h minimum", tc.since)
				}
			})
		}
	}
}

func TestProbeMinFirstSightIsStillProbed(t *testing.T) {
	b := probeMinBroker()
	addNode(b, "n", sixHours)
	b.trust["n"] = trustState{} // never probed: the first canary is what earns verification

	start := time.Now()
	b.probeOnce()
	first := lastProbeOf(b, "n")
	require.False(t, first.Before(start), "the first-sight probe must fire")

	b.metricsMu.Lock()
	st := b.probeSched["n"]
	require.Equal(t, 6*time.Hour, st.probeMin, "the effective minimum is stamped from the registration")
	require.False(t, st.nextDue.Before(first.Add(6*time.Hour)), "the next probe is scheduled no sooner than the declared minimum, not on the 30s floor")
	b.metricsMu.Unlock()

	// A failed first canary does not reopen the floor either: the minimum is per probe, not per pass.
	b.trust["n"] = trustState{probed: true, probeFails: 1}
	b.probeOnce()
	require.Equal(t, first, lastProbeOf(b, "n"))
}

func TestProbeMinNextDueRespectsTheLongerOfBackoffAndMinimum(t *testing.T) {
	for _, tc := range []struct {
		name     string
		declared int
		want     time.Duration
	}{
		{"undeclared rides the adaptive floor", 0, 30 * time.Second},
		{"a minimum below the floor changes nothing", 10, 30 * time.Second},
		{"a 6h minimum pushes the next probe out", sixHours, 6 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := probeMinBroker()
			addNode(b, "n", tc.declared)
			b.probeOnce()
			b.metricsMu.Lock()
			st := b.probeSched["n"]
			got := st.nextDue.Sub(st.lastProbe)
			b.metricsMu.Unlock()
			require.Equal(t, tc.want, got)
		})
	}
}

func TestProbeMinAboveTheCapIsClampedAndLoggedOncePerNode(t *testing.T) {
	var buf bytes.Buffer
	var mu sync.Mutex
	prev := log.Writer()
	log.SetOutput(&lockedWriter{w: &buf, mu: &mu})
	t.Cleanup(func() { log.SetOutput(prev) })

	b := probeMinBroker()
	addNode(b, "greedy", 30*86400) // declares a month
	addNode(b, "other", 400000)
	now := time.Now()
	// Probed 25h ago: past the 24h cap, so the clamped value lets it through ...
	b.probeSched["greedy"] = &probeState{lastProbe: now.Add(-25 * time.Hour)}
	// ... while 23h ago is still inside it.
	b.probeSched["other"] = &probeState{lastProbe: now.Add(-23 * time.Hour)}
	for i := 0; i < 3; i++ {
		b.probeOnce()
	}
	require.True(t, lastProbeOf(b, "greedy").After(now.Add(-time.Minute)), "a declared month is clamped to the 24h cap, not honoured")
	require.Equal(t, now.Add(-23*time.Hour), lastProbeOf(b, "other"))

	mu.Lock()
	out := buf.String()
	mu.Unlock()
	require.Equal(t, 1, strings.Count(out, "node=greedy probe_min_s=2592000 clamped"), out)
	require.Equal(t, 1, strings.Count(out, "node=other probe_min_s=400000 clamped"), out)
}

type lockedWriter struct {
	w  *bytes.Buffer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// --- the cost: verification lapses, is never extended, and the discount still applies ---

func TestProbeMinVerifiedLapsesOnTheNormalWindow(t *testing.T) {
	for _, tc := range []struct {
		name     string
		declared int
		age      time.Duration
		served   bool // a real served request after the probe
		want     bool
	}{
		{"declared, freshly probed", sixHours, time.Minute, false, true},
		{"declared, just inside the window", sixHours, 14 * time.Minute, false, true},
		{"declared, past the window", sixHours, 16 * time.Minute, false, false},
		{"declared, hours later", sixHours, 5 * time.Hour, false, false},
		{"declared, real traffic refreshes it as today", sixHours, 5 * time.Hour, true, true},
		{"undeclared node keeps today's behaviour", 0, 5 * time.Hour, false, true},
		// A minimum at or under the ceiling is probed inside the normal window anyway, so the
		// result-arrival jitter must not cost it the mark every cycle.
		{"declared at the ceiling does not flap", 900, 16 * time.Minute, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := probeMinBroker()
			addNode(b, "n", tc.declared)
			b.probeOnce() // stamps the effective minimum from the registration
			b.metricsMu.Lock()
			st := b.probeSched["n"]
			st.lastMeasured = time.Now().Add(-tc.age)
			st.lastProbe = st.lastMeasured
			b.metricsMu.Unlock()
			if tc.served {
				b.markMeasured("n")
			}

			b.mu.Lock()
			views := b.enrichOffersForNode(nil, b.nodes["n"], time.Now(), nil, false)
			b.mu.Unlock()
			require.Len(t, views, 1)
			require.Equal(t, tc.want, views[0].Verified, "discover verified mark")

			mkt := b.computeMarket().(map[string]any)["market"].([]marketView)
			require.Len(t, mkt, 1)
			require.Equal(t, tc.want, mkt[0].Verified, "market verified mark")
		})
	}
}

func TestProbeMinRealTrafficRefreshesNoMoreThanToday(t *testing.T) {
	b := probeMinBroker()
	addNode(b, "n", sixHours)
	last := time.Now().Add(-time.Hour)
	b.probeSched["n"] = &probeState{probeMin: 6 * time.Hour, lastProbe: last, lastMeasured: last, backoff: 3}

	b.markMeasured("n")
	b.probeOnce()

	b.metricsMu.Lock()
	defer b.metricsMu.Unlock()
	st := b.probeSched["n"]
	require.Equal(t, last, st.lastProbe, "served traffic must not trigger a probe inside the minimum")
	require.Equal(t, 3, st.backoff, "served traffic keeps the backoff level, as it does today")
}

func TestProbeMinNeverExtendsToolsVerification(t *testing.T) {
	t.Run("the TTL itself is unchanged", func(t *testing.T) {
		require.Equal(t, 45*time.Minute, toolsVerifiedTTL)
	})

	t.Run("single instance: the tools bit lapses on the TTL", func(t *testing.T) {
		for _, tc := range []struct {
			name     string
			declared int
			age      time.Duration
			want     bool
		}{
			{"declared, inside the TTL", sixHours, 44 * time.Minute, true},
			{"declared, past the TTL", sixHours, 46 * time.Minute, false},
			{"undeclared, past the TTL (unchanged)", 0, 46 * time.Minute, true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				b := probeMinBroker()
				addNode(b, "n", tc.declared)
				b.toolsOK = map[string]bool{toolKey("n", "m"): true}
				b.probeOnce()
				b.metricsMu.Lock()
				b.probeSched["n"].lastMeasured = time.Now().Add(-tc.age)
				got := b.toolsVerifiedForLocked("n", "m")
				b.metricsMu.Unlock()
				require.Equal(t, tc.want, got)
			})
		}
	})

	t.Run("multi instance: a held node's shared mark is not re-asserted", func(t *testing.T) {
		// The curated lane re-asserts its shared tools mark while it sleeps; the probe-min
		// lane must NOT, or a held node would keep a verdict nothing re-proves.
		for _, tc := range []struct {
			name      string
			curated   bool
			declared  int
			refreshed bool
		}{
			{"probe-min node is left to age out", false, sixHours, false},
			{"curated control is refreshed (the probe can see a refresh)", true, 0, true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				mr := miniredis.RunT(t)
				b := probeMinBroker()
				vs, err := newValkeyStore("redis://" + mr.Addr())
				require.NoError(t, err)
				b.shared = vs
				addNode(b, "n", tc.declared)
				n := b.nodes["n"]
				n.Curated = tc.curated
				b.nodes["n"] = n
				b.toolsOK = map[string]bool{toolKey("n", "m"): true}
				last := time.Now().Add(-time.Hour)
				b.probeSched["n"] = &probeState{curated: tc.curated, lastProbe: last, lastMeasured: last}
				old := strconv.FormatInt(time.Now().Add(-40*time.Minute).UnixMilli(), 10)
				mr.HSet(toolsKey(), toolKey("n", "m"), old)

				b.probeOnce()

				require.Equal(t, last, lastProbeOf(b, "n"), "the node must be held, not probed")
				require.Equal(t, !tc.refreshed, mr.HGet(toolsKey(), toolKey("n", "m")) == old)
			})
		}
	})
}

func TestProbeMinStalenessDiscountStillApplies(t *testing.T) {
	b := probeMinBroker()
	addNode(b, "rare", sixHours)
	addNode(b, "fresh", 0)
	now := time.Now()
	b.probeSched["rare"] = &probeState{probeMin: 6 * time.Hour, lastProbe: now.Add(-2 * time.Hour), lastMeasured: now.Add(-2 * time.Hour)}
	b.probeSched["fresh"] = &probeState{lastProbe: now.Add(-time.Minute), lastMeasured: now.Add(-time.Minute)}

	b.metricsMu.Lock()
	rare := b.measurementStalenessLocked("rare", now)
	fresh := b.measurementStalenessLocked("fresh", now)
	b.metricsMu.Unlock()
	require.InDelta(t, 0.7, rare, 1e-9, "two hours unmeasured against a 15m ceiling reads at the 0.7 floor")
	require.Equal(t, 1.0, fresh)

	// Same model, same measured speed: the rarely-probed node ranks below the fresh one.
	b.tps["rare"], b.tps["fresh"] = 50, 50
	b.mu.Lock()
	views := b.enrichOffersForNode(nil, b.nodes["rare"], now, nil, false)
	views = b.enrichOffersForNode(views, b.nodes["fresh"], now, nil, false)
	b.mu.Unlock()
	require.Len(t, views, 2)
	require.Less(t, views[0].Signal, views[1].Signal)
}
