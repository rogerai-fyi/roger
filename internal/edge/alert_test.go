package edge_test

// features/edge/health.feature §3 (rules), §4 (offline) and §5 (the alert itself): defaults that
// work out of the box, per-node rules that override fleet-wide ones, one state change per
// crossing, a recovery margin so a wobble does not spam, and an alert that names the node, the
// metric, the real value and the threshold.

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/edge"
	"rogerai.fm/roger/v7/internal/store"
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

// THE LOG (a factory's state timeline, the station's log): each judgement records every unit's
// state - on air, dark, trouble - once per minute, so the surfaces can draw the last hour and
// say how long a unit has been in its state without anyone having watched the screen.
func TestJudgeKeepsAStateLogOncePerMinute(t *testing.T) {
	dir := t.TempDir()
	l := edge.AlertLog{Dir: dir}
	t0 := time.Date(2026, 9, 26, 12, 0, 5, 0, time.UTC)
	hot := healthOf("pi", t0, 99, 10)
	dark := edge.NodeHealth{ID: "n_q", Name: "quiet", Presence: string(edge.PresenceDark), Dark: true, DarkFor: 10 * time.Minute, LastHeard: t0.Add(-10 * time.Minute)}
	fine := healthOf("nas", t0, 40, 10)
	_, _, err := l.Judge([]edge.NodeHealth{hot, dark, fine}, t0)
	require.NoError(t, err)
	_, _, err = l.Judge([]edge.NodeHealth{hot, dark, fine}, t0.Add(20*time.Second)) // same minute: no new line
	require.NoError(t, err)
	_, _, err = l.Judge([]edge.NodeHealth{healthOf("pi", t0, 70, 10), dark, fine}, t0.Add(2*time.Minute)) // pi cooled
	require.NoError(t, err)

	samples, err := l.States(t0.Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, samples, 2, "one sample per minute that was judged")
	require.Equal(t, edge.StateTrouble, samples[0].States["n_pi"])
	require.Equal(t, edge.StateDark, samples[0].States["n_q"])
	require.Equal(t, edge.StateAir, samples[0].States["n_nas"])
	require.Equal(t, edge.StateAir, samples[1].States["n_pi"], "cleared: on air again")

	// The last hour as 60 cells, oldest first, unknown where nothing was judged; and how long
	// the unit has been in its current state.
	cells, since, capped := edge.LogOf(samples, "n_pi", t0.Add(3*time.Minute))
	require.False(t, capped, "pi changed state inside the log, so the duration is exact")
	_, _, capped = edge.LogOf(samples, "n_q", t0.Add(3*time.Minute))
	require.True(t, capped, "quiet was dark from the first sample: the log cannot say since when")
	require.Len(t, cells, 60)
	require.Equal(t, edge.StateTrouble, cells[56], "t0 is 3 minutes before the end")
	require.Equal(t, edge.StateUnknown, cells[0])
	require.Equal(t, edge.StateAir, cells[58])
	require.InDelta(t, (1 * time.Minute).Minutes(), since.Minutes(), 0.6, "on air since the t0+2m sample")
}

// TROUBLE CALLS (ISA-18.2 by way of the station): what is standing, since when, and whether
// the operator has said ROGER to it. An acknowledgement quiets the call, never removes it; the
// call leaves when the condition clears, and its acknowledgement with it.
func TestStandingTroubleCallsAndRoger(t *testing.T) {
	dir := t.TempDir()
	l := edge.AlertLog{Dir: dir}
	t0 := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	_, _, err := l.Judge([]edge.NodeHealth{healthOf("pi", t0, 99, 10)}, t0)
	require.NoError(t, err)
	calls, err := l.Standing(t0.Add(7 * time.Minute))
	require.NoError(t, err)
	require.Len(t, calls, 1)
	c := calls[0]
	require.Equal(t, "cpu_temp", c.RuleID)
	require.Equal(t, "n_pi", c.NodeID)
	require.Equal(t, "pi", c.Node)
	require.InDelta(t, 7, c.Since.Minutes(), 0.1, "standing since it fired")
	require.False(t, c.Acked)

	require.NoError(t, l.Roger("cpu_temp", "n_pi", "luis", t0.Add(8*time.Minute)))
	calls, _ = l.Standing(t0.Add(9 * time.Minute))
	require.True(t, calls[0].Acked)
	require.Equal(t, "luis", calls[0].AckBy)
	require.Error(t, l.Roger("disk", "n_pi", "luis", t0), "only a standing call can be acknowledged")

	// It clears: the call is gone, and so is its acknowledgement (a new call needs a new roger).
	_, _, err = l.Judge([]edge.NodeHealth{healthOf("pi", t0, 70, 10)}, t0.Add(10*time.Minute))
	require.NoError(t, err)
	calls, _ = l.Standing(t0.Add(11 * time.Minute))
	require.Empty(t, calls)
	_, _, err = l.Judge([]edge.NodeHealth{healthOf("pi", t0, 99, 10)}, t0.Add(12*time.Minute))
	require.NoError(t, err)
	calls, _ = l.Standing(t0.Add(13 * time.Minute))
	require.Len(t, calls, 1)
	require.False(t, calls[0].Acked, "a fresh call is not pre-acknowledged")
}

// The history outlives the binary that wrote it: a line from before the alert record changed
// shape (a zero time for last_heard, a nanosecond dark_for) still reads, so a standing call keeps
// its age across an upgrade instead of showing as new.
func TestRecentReadsHistoryWrittenByAnOlderBinary(t *testing.T) {
	dir := t.TempDir()
	old := `{"at":"2026-09-26T10:43:00-07:00","rule":"cpu_temp","node_id":"n_pi","node":"pi","metric":"cpu_temp","value":95.5,"threshold":85,"op":"gt","firing":true,"last_heard":"0001-01-01T00:00:00Z"}` + "\n" +
		`{"at":"2026-09-26T10:43:00-07:00","rule":"offline","node_id":"n_q","node":"quiet","metric":"offline","value":1500,"threshold":5,"op":"gt","firing":true,"dark_for":90000000000000,"last_heard":"2026-09-24T13:23:00-07:00"}` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "log.jsonl"), []byte(old), 0o600))
	got, err := edge.AlertLog{Dir: dir}.Recent(10)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "pi", got[0].Node)
	require.Equal(t, 95.5, got[0].Value)
	require.True(t, got[1].Firing)
	require.NotZero(t, got[1].LastHeard, "a real last-heard time is kept")
}

// STAND BY (ISA-18.2 shelving): the operator quiets a call for a set time; it comes back on its
// own when the time is up. A call standing over a day is STALE - a sign the rule, not the unit,
// is wrong. A PILE-UP is ten calls in ten minutes.
func TestStandByStaleAndPileUp(t *testing.T) {
	dir := t.TempDir()
	l := edge.AlertLog{Dir: dir}
	t0 := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	_, _, err := l.Judge([]edge.NodeHealth{healthOf("pi", t0, 99, 10)}, t0)
	require.NoError(t, err)
	require.NoError(t, l.StandBy("cpu_temp", "n_pi", t0.Add(time.Hour)))
	calls, err := l.Standing(t0.Add(10 * time.Minute))
	require.NoError(t, err)
	require.True(t, calls[0].Shelved, "on stand by")
	require.Equal(t, t0.Add(time.Hour).Unix(), calls[0].StandByUntil.Unix())
	calls, _ = l.Standing(t0.Add(61 * time.Minute))
	require.False(t, calls[0].Shelved, "the time is up: the call is loud again")
	require.False(t, calls[0].Stale)
	calls, _ = l.Standing(t0.Add(25 * time.Hour))
	require.True(t, calls[0].Stale, "standing over a day")
	require.Error(t, l.StandBy("disk", "n_pi", t0.Add(time.Hour)), "only a standing call goes on stand by")

	// Ten crossings in ten minutes is a pile-up; the rate is what the header shows.
	for i := 0; i < 10; i++ {
		at := t0.Add(time.Duration(i) * 30 * time.Second)
		_, _, _ = l.Judge([]edge.NodeHealth{healthOf("pi", at, 70, 10)}, at) // clears
		_, _, _ = l.Judge([]edge.NodeHealth{healthOf("pi", at, 99, 10)}, at.Add(time.Second))
	}
	rate, err := l.Rate(t0.Add(6*time.Minute), 10*time.Minute)
	require.NoError(t, err)
	require.GreaterOrEqual(t, rate, 10)
}

// The SIGN-OFF report: what this shift held - calls raised and cleared, the loudest unit, and each
// unit's dead air - from the history and the state log, so an owner reads the shift in one line.
func TestSignOffReportSumsTheShift(t *testing.T) {
	dir := t.TempDir()
	l := edge.AlertLog{Dir: dir}
	t0 := time.Date(2026, 9, 26, 6, 0, 0, 0, time.UTC)
	dark := edge.NodeHealth{ID: "n_q", Name: "quiet", Presence: string(edge.PresenceDark), Dark: true, DarkFor: 10 * time.Minute, LastHeard: t0.Add(-10 * time.Minute)}
	for i := 0; i < 3; i++ { // pi runs hot three times this shift, cools twice
		at := t0.Add(time.Duration(i) * time.Hour)
		_, _, _ = l.Judge([]edge.NodeHealth{healthOf("pi", at, 99, 10), dark}, at)
		if i < 2 {
			_, _, _ = l.Judge([]edge.NodeHealth{healthOf("pi", at, 70, 10), dark}, at.Add(20*time.Minute))
		}
	}
	r, err := l.SignOff(t0.Add(3*time.Hour), 8*time.Hour)
	require.NoError(t, err)
	require.Equal(t, 4, r.Raised, "three hot calls and the dark call")
	require.Equal(t, 2, r.Cleared)
	require.Equal(t, "pi", r.Loudest)
	require.Equal(t, 3, r.LoudestCount)
	// Five judgements this shift, each a minute of dead air at most: a gap nobody judged is not
	// counted as dark - the report says what was seen, never more.
	require.InDelta(t, 5, r.DeadAir["n_q"].Minutes(), 0.1, "only the judged minutes count as dead air")
	require.Zero(t, r.DeadAir["n_pi"], "hot is not dead air")
}

// A roger pressed while the host is judging is never lost, and a cleared call's ack never
// resurrects: every read-modify-write of the log's files runs under the one lock.
func TestRogerAndJudgeSerialise(t *testing.T) {
	dir := t.TempDir()
	l := edge.AlertLog{Dir: dir}
	t0 := time.Now()
	hot := []edge.NodeHealth{healthOf("pi", t0, 99, 10)}
	_, _, err := l.Judge(hot, t0)
	require.NoError(t, err)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _, _ = edge.AlertLog{Dir: dir}.Judge(hot, t0.Add(time.Second)) }()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		require.NoError(t, edge.AlertLog{Dir: dir}.Roger("cpu_temp", "n_pi", "luis", t0.Add(time.Second)))
	}()
	wg.Wait()
	calls, err := l.Standing(t0.Add(2 * time.Second))
	require.NoError(t, err)
	require.Len(t, calls, 1)
	require.True(t, calls[0].Acked, "the roger survived eight concurrent judgements")
}

// A gap nobody judged ends a run: time in state counts from the first sample after the gap, and
// is exact (not capped) because the log saw it begin.
func TestLogOfDoesNotCountAnUnjudgedGapAsTimeInState(t *testing.T) {
	t0 := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	samples := []edge.StateSample{
		{At: t0.Unix(), States: map[string]edge.UnitState{"n_pi": edge.StateAir}},
		{At: t0.Add(time.Minute).Unix(), States: map[string]edge.UnitState{"n_pi": edge.StateAir}},
		{At: t0.Add(41 * time.Minute).Unix(), States: map[string]edge.UnitState{"n_pi": edge.StateAir}}, // a 40-minute hole
		{At: t0.Add(42 * time.Minute).Unix(), States: map[string]edge.UnitState{"n_pi": edge.StateAir}},
	}
	_, since, capped := edge.LogOf(samples, "n_pi", t0.Add(43*time.Minute))
	require.InDelta(t, 2, since.Minutes(), 0.1, "since the first sample after the hole")
	require.False(t, capped)
}

// The state log is pruned to a day, the history to ten thousand lines.
func TestTheLogsDoNotGrowWithoutEnd(t *testing.T) {
	dir := t.TempDir()
	l := edge.AlertLog{Dir: dir}
	t0 := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	_, _, err := l.Judge([]edge.NodeHealth{healthOf("pi", t0, 40, 10)}, t0.Add(-30*time.Hour))
	require.NoError(t, err)
	_, _, err = l.Judge([]edge.NodeHealth{healthOf("pi", t0, 40, 10)}, t0.Truncate(time.Hour)) // on the half hour: prunes
	require.NoError(t, err)
	samples, err := l.States(t0.Add(-48 * time.Hour))
	require.NoError(t, err)
	require.Len(t, samples, 1, "the sample from 30 hours ago is gone")

	var b strings.Builder
	for i := 0; i < 12001; i++ {
		b.WriteString(`{"at":"2026-09-26T10:00:00Z","rule":"cpu_temp","node_id":"n_x","node":"x","firing":true}` + "\n")
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "log.jsonl"), []byte(b.String()), 0o600))
	_, _, err = l.Judge([]edge.NodeHealth{healthOf("pi", t0, 99, 10)}, t0.Add(time.Minute))
	require.NoError(t, err)
	got, err := l.Recent(1 << 20)
	require.NoError(t, err)
	require.LessOrEqual(t, len(got), 10000)
	require.Equal(t, "pi", got[len(got)-1].Node, "the newest line is the one just written")
}

// A stand-by written by an older build (key -> until, no asker) still reads: the live machine's
// feed answered 500 the night the shape changed, until this.
func TestStandBysWrittenByAnOlderBuildStillRead(t *testing.T) {
	dir := t.TempDir()
	l := edge.AlertLog{Dir: dir}
	t0 := time.Now()
	_, _, err := l.Judge([]edge.NodeHealth{healthOf("pi", t0, 99, 10)}, t0)
	require.NoError(t, err)
	until := t0.Add(time.Hour).Unix()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "standby.json"), []byte(fmt.Sprintf(`{"cpu_temp|n_pi": %d}`, until)), 0o600))
	calls, err := l.Standing(t0.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, calls[0].Shelved)
	require.Equal(t, until, calls[0].StandByUntil.Unix())
	require.Empty(t, calls[0].StandByBy, "the old shape knew no asker")
	_, err = l.Feed(time.Time{}, t0.Add(time.Minute), nil)
	require.NoError(t, err)
}

// features/edge/health.feature: a throttled or undervolted Pi is surfaced, and a default rule warns
// on throttling that is happening NOW. A flag that only says "since boot" is shown, never paged.
func TestAThrottledPiIsSurfacedAndADefaultRuleWarns(t *testing.T) {
	now, since := edge.PowerWords(0x50005)
	require.Equal(t, []string{"under-voltage", "throttled"}, now)
	require.Equal(t, []string{"under-voltage", "throttled"}, since)
	now, since = edge.PowerWords(0x80000)
	require.Empty(t, now)
	require.Equal(t, []string{"soft temperature limit"}, since)
	now, since = edge.PowerWords(0)
	require.Empty(t, now)
	require.Empty(t, since)

	var rule edge.Rule
	for _, r := range edge.DefaultRules() {
		if r.Metric == edge.MetricThrottled {
			rule = r
		}
	}
	require.Equal(t, "throttled", rule.ID, "throttling has a default rule")
	require.True(t, rule.Default)

	at := time.Unix(1_700_000_000, 0)
	h := func(word uint32) edge.NodeHealth {
		return edge.NodeHealth{ID: "n_pi", Name: "pi", Readings: &store.EdgeResources{PiPower: &word, ReadAt: at.Unix()}}
	}
	v, ok := h(0x50005).Reading(edge.MetricThrottled)
	require.True(t, ok)
	require.Equal(t, 1.0, v)
	v, ok = h(0x50000).Reading(edge.MetricThrottled)
	require.True(t, ok)
	require.Zero(t, v, "since-boot alone is history, not a page")
	_, ok = edge.NodeHealth{ID: "n_pc", Readings: &store.EdgeResources{ReadAt: at.Unix()}}.Reading(edge.MetricThrottled)
	require.False(t, ok, "a machine with no Pi firmware is not judged at all")

	rules := edge.RuleSet(edge.DefaultRules())
	st, alerts := edge.Evaluate(rules, []edge.NodeHealth{h(0x50005)}, edge.AlertState{}, at)
	require.Len(t, alerts, 1)
	require.Equal(t, edge.MetricThrottled, alerts[0].Metric)
	require.True(t, alerts[0].Firing)
	_, alerts = edge.Evaluate(rules, []edge.NodeHealth{h(0x50000)}, st, at)
	require.Len(t, alerts, 1)
	require.False(t, alerts[0].Firing, "it clears when it stops, once")

	why, do := edge.WhyAndDo(edge.MetricThrottled)
	require.Contains(t, why, "power")
	require.Contains(t, do, "supply")
	require.Equal(t, "HI", edge.Priority(edge.MetricThrottled))
}

func TestAPisPowerIsSaidInWordsOnEverySurface(t *testing.T) {
	require.Equal(t, "good", edge.PowerLine(0))
	require.Equal(t, "under-voltage NOW · throttled since boot", edge.PowerLine(0x40001))
	require.Equal(t, "good now · under-voltage since boot", edge.PowerLine(0x10000))
	require.Equal(t, "now", edge.FormatValue(edge.MetricThrottled, 1))
	require.Equal(t, "clear", edge.FormatValue(edge.MetricThrottled, 0))
	require.Equal(t, "firing: pi is short of power or throttled right now",
		edge.Alert{Node: "pi", Metric: edge.MetricThrottled, Firing: true, Value: 1}.String())
	require.Equal(t, "cleared: pi is no longer throttled",
		edge.Alert{Node: "pi", Metric: edge.MetricThrottled}.String())
}

// features/edge/fleet_window.feature §6: the page draws the last hour as a line, so a spike looks like
// a spike - and a gap where the unit was dark is a gap, not interpolated.
func TestTheLastHourIsKeptAsALineWithGapsWhereItWasDark(t *testing.T) {
	l := edge.AlertLog{Dir: t.TempDir()}
	start := time.Unix(1_700_000_000, 0).Truncate(time.Minute)
	for m := 0; m < 6; m++ {
		at := start.Add(time.Duration(m) * time.Minute)
		temp, load := 50.0+float64(m*5), 0.5
		h := edge.NodeHealth{ID: "n_pi", Readings: &store.EdgeResources{CPUTempC: &temp, CPULoad1: &load,
			RAMUsedGB: 2, RAMTotalGB: 4, ReadAt: at.Unix()}}
		if m == 3 {
			h = edge.NodeHealth{ID: "n_pi", Dark: true} // dark this minute
		}
		_, _, err := l.Judge([]edge.NodeHealth{h}, at)
		require.NoError(t, err)
	}
	now := start.Add(5 * time.Minute)
	samples, err := l.States(now.Add(-time.Hour))
	require.NoError(t, err)
	temps := edge.VitalOf(samples, "n_pi", edge.VitalTemp, now)
	require.Len(t, temps, 60)
	got := temps[54:]
	require.Equal(t, 50.0, got[0])
	require.Equal(t, 60.0, got[2])
	require.True(t, math.IsNaN(got[3]), "the dark minute is a gap")
	require.Equal(t, 75.0, got[5])
	require.True(t, math.IsNaN(temps[0]), "before the log began is a gap too")
	mem := edge.VitalOf(samples, "n_pi", edge.VitalMemory, now)
	require.InDelta(t, 0.5, mem[59], 1e-9)
	require.True(t, math.IsNaN(edge.VitalOf(samples, "n_other", edge.VitalTemp, now)[59]))
}
