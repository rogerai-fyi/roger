package edge_test

// features/edge/health.feature §3 (rules), §4 (offline) and §5 (the alert itself): defaults that
// work out of the box, per-node rules that override fleet-wide ones, one state change per
// crossing, a recovery margin so a wobble does not spam, and an alert that names the node, the
// metric, the real value and the threshold.

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

func healthOf(name string, now time.Time, temp float64, diskPct float64) edge.NodeHealth {
	t := temp
	return edge.NodeHealth{ID: "n_" + name, Name: name, Kind: "host", Presence: string(edge.PresenceVerified),
		LastHeard: now,
		Readings: &store.EdgeResources{CPUTempC: &t, RAMUsedGB: 4, RAMTotalGB: 8,
			Disks: []store.EdgeDisk{{Mount: "/", UsedGB: diskPct, TotalGB: 100}}, ReadAt: now.Unix()},
		ReadAge: 0}
}

func TestDefaultRulesWorkOutOfTheBoxAndCanBeTurnedOff(t *testing.T) {
	defaults := edge.DefaultRules()
	byMetric := map[edge.Metric]edge.Rule{}
	for _, r := range defaults {
		require.True(t, r.Default, "a default rule says it is one")
		require.Empty(t, r.Scope, "defaults are fleet-wide")
		byMetric[r.Metric] = r
	}
	require.Contains(t, byMetric, edge.MetricCPUTemp, "a default warns on a hot CPU")
	require.Contains(t, byMetric, edge.MetricDisk, "a default warns on a nearly full disk")
	require.Contains(t, byMetric, edge.MetricOffline, "a default warns on a node offline past the liveness window")
	require.InDelta(t, edge.DefaultTTL.Minutes(), byMetric[edge.MetricOffline].Value, 0.01,
		"the offline default IS the liveness window")

	now := time.Now()
	hot := healthOf("pi", now, byMetric[edge.MetricCPUTemp].Value+2, 10)
	_, alerts := edge.Evaluate(defaults, []edge.NodeHealth{hot}, nil, now)
	require.Len(t, alerts, 1)
	require.Equal(t, edge.MetricCPUTemp, alerts[0].Metric)

	off := edge.RuleSet(defaults).Off(byMetric[edge.MetricCPUTemp].ID)
	_, alerts = edge.Evaluate(off, []edge.NodeHealth{hot}, nil, now)
	require.Empty(t, alerts, "a default that was turned off does not fire")
}

func TestANodeRuleOverridesTheFleetRuleForThatNodeOnly(t *testing.T) {
	now := time.Now()
	rules := edge.RuleSet{
		{ID: "disk-fleet", Metric: edge.MetricDisk, Op: edge.OpGT, Value: 90},
		{ID: "disk-nas", Metric: edge.MetricDisk, Op: edge.OpGT, Value: 75, Scope: "n_nas"},
	}
	nas := healthOf("nas", now, 40, 80)
	pi := healthOf("pi", now, 40, 80)
	_, alerts := edge.Evaluate(rules, []edge.NodeHealth{nas, pi}, nil, now)
	require.Len(t, alerts, 1, "only nas trips: it uses the stricter 75 rule, pi the fleet 90 rule")
	require.Equal(t, "nas", alerts[0].Node)
	require.Equal(t, 75.0, alerts[0].Threshold)
}

func TestARuleFiresOnceWhenCrossedAndClearsOnceWhenRecovered(t *testing.T) {
	now := time.Now()
	rules := edge.RuleSet{{ID: "hot", Metric: edge.MetricCPUTemp, Op: edge.OpGT, Value: 80}}
	var st edge.AlertState
	var alerts []edge.Alert

	st, alerts = edge.Evaluate(rules, []edge.NodeHealth{healthOf("pi", now, 82, 10)}, st, now)
	require.Len(t, alerts, 1)
	require.True(t, alerts[0].Firing)
	require.True(t, st.Firing("hot", "n_pi"))

	// Still over: NO new alert - a stream while it stays over is what §3 forbids.
	st, alerts = edge.Evaluate(rules, []edge.NodeHealth{healthOf("pi", now, 84, 10)}, st, now)
	require.Empty(t, alerts)
	require.True(t, st.Firing("hot", "n_pi"))

	st, alerts = edge.Evaluate(rules, []edge.NodeHealth{healthOf("pi", now, 76, 10)}, st, now)
	require.Len(t, alerts, 1)
	require.False(t, alerts[0].Firing, "recovery is a clearing alert")
	require.False(t, st.Firing("hot", "n_pi"))

	st, alerts = edge.Evaluate(rules, []edge.NodeHealth{healthOf("pi", now, 70, 10)}, st, now)
	require.Empty(t, alerts, "staying recovered is not news")
}

func TestAWobbleAcrossTheThresholdHoldsUntilItSettles(t *testing.T) {
	now := time.Now()
	rules := edge.RuleSet{{ID: "hot", Metric: edge.MetricCPUTemp, Op: edge.OpGT, Value: 80, Margin: 2}}
	var st edge.AlertState
	changes := 0
	for i, temp := range []float64{81, 79.5, 81, 79.5, 81, 79.5, 79.5, 79.5} {
		var alerts []edge.Alert
		st, alerts = edge.Evaluate(rules, []edge.NodeHealth{healthOf("pi", now, temp, 10)}, st, now.Add(time.Duration(i)*time.Second))
		changes += len(alerts)
	}
	require.Equal(t, 1, changes, "one fire, and it holds: a wobble inside the margin is not a clear")
	require.True(t, st.Firing("hot", "n_pi"))
	// Once it really recovers, past the margin, it clears - once.
	var alerts []edge.Alert
	st, alerts = edge.Evaluate(rules, []edge.NodeHealth{healthOf("pi", now, 77, 10)}, st, now)
	require.Len(t, alerts, 1)
	require.False(t, alerts[0].Firing)
}

func TestAnAbsentOrStaleMetricIsNotJudged(t *testing.T) {
	now := time.Now()
	rules := edge.RuleSet{{ID: "hot", Metric: edge.MetricCPUTemp, Op: edge.OpGT, Value: 80}}
	quiet := edge.NodeHealth{ID: "n_q", Name: "quiet", Presence: string(edge.PresenceVerified), LastHeard: now}
	st, alerts := edge.Evaluate(rules, []edge.NodeHealth{quiet}, nil, now)
	require.Empty(t, alerts, "no reading is no verdict, never a zero judged against 80")

	stale := healthOf("pi", now, 95, 10)
	stale.Readings.ReadAt = now.Add(-2 * edge.DefaultTTL).Unix()
	stale.ReadAge = 2 * edge.DefaultTTL
	_, alerts = edge.Evaluate(rules, []edge.NodeHealth{stale}, st, now)
	require.Empty(t, alerts, "a reading older than the liveness window is not presented as current")
}

func TestOfflineIsJudgedByTheAuthorityAndTheAlertSaysForHowLong(t *testing.T) {
	now := time.Now()
	rules := edge.RuleSet{{ID: "off", Metric: edge.MetricOffline, Op: edge.OpGT, Value: edge.DefaultTTL.Minutes()}}
	dark := edge.NodeHealth{ID: "n_pi", Name: "pi", Presence: string(edge.PresenceDark), Dark: true,
		DarkFor: 12 * time.Minute, LastHeard: now.Add(-12 * time.Minute)}
	st, alerts := edge.Evaluate(rules, []edge.NodeHealth{dark}, nil, now)
	require.Len(t, alerts, 1, "the authority fires it from the missing heartbeat - the dead node sent nothing")
	a := alerts[0]
	require.True(t, a.Firing)
	require.Equal(t, edge.MetricOffline, a.Metric)
	s := a.String()
	require.Contains(t, s, "pi")
	require.Contains(t, s, "offline")
	require.Contains(t, s, "12m", "for how long")
	require.Equal(t, dark.LastHeard.Unix(), a.LastHeard, "and when it was last heard from")

	back := edge.NodeHealth{ID: "n_pi", Name: "pi", Presence: string(edge.PresenceVerified), LastHeard: now}
	st, alerts = edge.Evaluate(rules, []edge.NodeHealth{back}, st, now)
	require.Len(t, alerts, 1, "coming back is one clearing change")
	require.False(t, alerts[0].Firing)
	_, alerts = edge.Evaluate(rules, []edge.NodeHealth{back}, st, now)
	require.Empty(t, alerts, "not a repeat of the history")
}

func TestAnAlertNamesTheNodeMetricValueAndThreshold(t *testing.T) {
	now := time.Now()
	rules := edge.RuleSet{{ID: "hot", Metric: edge.MetricCPUTemp, Op: edge.OpGT, Value: 80}}
	_, alerts := edge.Evaluate(rules, []edge.NodeHealth{healthOf("pi", now, 82, 10)}, nil, now)
	require.Len(t, alerts, 1)
	s := alerts[0].String()
	for _, want := range []string{"pi", "cpu temp", "82", "80", "firing"} {
		require.Contains(t, s, want)
	}
	st, _ := edge.Evaluate(rules, []edge.NodeHealth{healthOf("pi", now, 82, 10)}, nil, now)
	_, alerts = edge.Evaluate(rules, []edge.NodeHealth{healthOf("pi", now, 70, 10)}, st, now)
	require.Len(t, alerts, 1)
	require.True(t, strings.Contains(alerts[0].String(), "cleared"), alerts[0].String())
}

func TestTheAlertLogKeepsRulesStateAndHistoryOnDisk(t *testing.T) {
	dir := t.TempDir()
	log := edge.AlertLog{Dir: dir}
	rules, err := log.Rules()
	require.NoError(t, err)
	require.Equal(t, edge.RuleSet(edge.DefaultRules()), rules, "an owner who set no rules has the defaults")

	r := edge.Rule{ID: "disk-nas", Metric: edge.MetricDisk, Op: edge.OpGT, Value: 75, Scope: "n_nas"}
	require.NoError(t, log.SetRule(r))
	rules, err = log.Rules()
	require.NoError(t, err)
	require.Contains(t, rules, r)
	require.Len(t, rules, len(edge.DefaultRules())+1, "the defaults stay alongside the owner's rule")

	// Turning a default off survives a reload; removing an owner rule removes it.
	off := edge.DefaultRules()[1].ID // the disk default; cpu_temp stays on to fire below
	require.NoError(t, log.SetOff(off, true))
	rules, _ = log.Rules()
	for _, x := range rules {
		if x.ID == off {
			require.True(t, x.Off)
		}
	}
	require.NoError(t, log.Remove(r.ID))
	rules, _ = log.Rules()
	require.NotContains(t, rules, r)
	require.Error(t, log.Remove(off), "a default is turned off, not removed")

	now := time.Now()
	st, alerts := edge.Evaluate(rules, []edge.NodeHealth{healthOf("pi", now, 99, 10)}, nil, now)
	require.NoError(t, log.Record(st, alerts))
	got, err := log.State()
	require.NoError(t, err)
	require.True(t, got.Firing("cpu_temp", "n_pi"))
	recent, err := log.Recent(10)
	require.NoError(t, err)
	require.Len(t, recent, 1)
	require.Equal(t, "pi", recent[0].Node)
}

// Review round (2026-09-26): the corners the first cut left open.

func TestAnOfflineRuleOnlyCountsUpwardsSoItCannotFireOnEveryLiveNode(t *testing.T) {
	// "offline lt 5" would be true (0 < 5) for every node that is NOT dark: a false alarm on the
	// whole fleet. It is refused where a rule is made, not judged.
	err := edge.ValidateRule(edge.Rule{ID: "x", Metric: edge.MetricOffline, Op: edge.OpLT, Value: 5})
	require.Error(t, err)
	require.Contains(t, err.Error(), "offline")
	l := edge.AlertLog{Dir: t.TempDir()}
	require.Error(t, l.SetRule(edge.Rule{ID: "x", Metric: edge.MetricOffline, Op: edge.OpLT, Value: 5}))
	require.NoError(t, l.SetRule(edge.Rule{ID: "x", Metric: edge.MetricOffline, Op: edge.OpGT, Value: 30}))
}

func TestARuleTurnedOffOrANodeForgottenWhileFiringClearsOnce(t *testing.T) {
	now := time.Now()
	rules := edge.RuleSet{{ID: "hot", Metric: edge.MetricCPUTemp, Op: edge.OpGT, Value: 80}}
	st, alerts := edge.Evaluate(rules, []edge.NodeHealth{healthOf("pi", now, 90, 10)}, nil, now)
	require.Len(t, alerts, 1)

	// The owner turns the rule off: the log must close the firing, not leave it open forever.
	st2, alerts := edge.Evaluate(rules.Off("hot"), []edge.NodeHealth{healthOf("pi", now, 90, 10)}, st, now)
	require.Len(t, alerts, 1)
	require.False(t, alerts[0].Firing)
	require.Equal(t, "pi", alerts[0].Node)
	require.False(t, st2.Firing("hot", "n_pi"))

	// The node is forgotten while firing: same - one clearing change, then silence.
	st3, alerts := edge.Evaluate(rules, nil, st, now)
	require.Len(t, alerts, 1)
	require.False(t, alerts[0].Firing)
	require.Equal(t, "n_pi", alerts[0].NodeID)
	_, alerts = edge.Evaluate(rules, nil, st3, now)
	require.Empty(t, alerts)
}

func TestJudgeSerialisesAcrossWritersSoACrossingIsLoggedExactlyOnce(t *testing.T) {
	// The host judges every pass and every CLI read judges too, in another process. Judge takes
	// the log's lock around read-evaluate-write, so N concurrent judges of one crossing produce
	// ONE firing line, never two.
	dir := t.TempDir()
	now := time.Now()
	hot := []edge.NodeHealth{healthOf("pi", now, 99, 10)}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := edge.AlertLog{Dir: dir}.Judge(hot, now)
			require.NoError(t, err)
		}()
	}
	wg.Wait()
	recent, err := edge.AlertLog{Dir: dir}.Recent(100)
	require.NoError(t, err)
	require.Len(t, recent, 1, "sixteen judges of one crossing, one alert: %v", recent)
}

func TestAnAlertRecordIsHonestJSON(t *testing.T) {
	now := time.Now()
	rules := edge.RuleSet{{ID: "hot", Metric: edge.MetricCPUTemp, Op: edge.OpGT, Value: 80}}
	_, alerts := edge.Evaluate(rules, []edge.NodeHealth{healthOf("pi", now, 82, 10)}, nil, now)
	b, err := json.Marshal(alerts[0])
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	_, hasDark := m["dark_for_s"]
	require.False(t, hasDark, "a temperature alert carries no dark time: %s", b)
	require.NotContains(t, string(b), "0001-01-01", "no zero time is ever written: %s", b)
}
