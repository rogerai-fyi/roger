package edge

// Power alerts and asleep phones (features/edge/fleet_window.feature and trouble_feed.feature, ruled
// 2026-10-13): a low battery that is not charging, a critical one, external power lost, energy saver
// on - for any kind of node - with hysteresis; and the DEFAULT offline rule skips a sleeping phone.

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/store"
)

func powered(id, kind string, pct int, charging bool, source string, low bool) NodeHealth {
	return NodeHealth{ID: id, Name: id, Kind: kind, Presence: string(PresenceVerified),
		Power: &store.EdgePower{BatteryPct: &pct, Charging: &charging, Source: source, LowPower: &low}}
}

func firingIDs(alerts []Alert) []string {
	var out []string
	for _, a := range alerts {
		if a.Firing {
			out = append(out, a.RuleID)
		}
	}
	return out
}

func TestALowBatteryFiresOnceAndClearsPastItsMargin(t *testing.T) {
	now := time.Now()
	rules := RuleSet(DefaultRules())
	st, alerts := Evaluate(rules, []NodeHealth{powered("n_pi", "host", 19, false, "battery", false)}, nil, now)
	require.ElementsMatch(t, []string{"battery", "on_battery"}, firingIDs(alerts))
	require.Contains(t, alerts[0].String()+alerts[1].String(), "battery is low at 19% and not charging")

	// 22%: still under the 25% it must clear past. No new alert.
	st, alerts = Evaluate(rules, []NodeHealth{powered("n_pi", "host", 22, false, "battery", false)}, st, now)
	require.Empty(t, alerts)
	require.True(t, st.Firing("battery", "n_pi"))

	// 9%: critical too.
	st, alerts = Evaluate(rules, []NodeHealth{powered("n_pi", "host", 9, false, "battery", false)}, st, now)
	require.Equal(t, []string{"battery_critical"}, firingIDs(alerts))

	// Plugged in: charging is never low, and power is back.
	_, alerts = Evaluate(rules, []NodeHealth{powered("n_pi", "host", 9, true, "ac", false)}, st, now)
	require.Len(t, alerts, 3)
	for _, a := range alerts {
		require.False(t, a.Firing, a.String())
	}
}

func TestAPhoneOnBatteryIsNotAnAlarmButLowPowerIs(t *testing.T) {
	now := time.Now()
	rules := RuleSet(DefaultRules())
	_, alerts := Evaluate(rules, []NodeHealth{powered("n_phone", "mobile", 80, false, "battery", true)}, nil, now)
	require.Equal(t, []string{"low_power"}, firingIDs(alerts), "a phone runs on battery by nature")
	require.Contains(t, alerts[0].String(), "is in low power mode")

	// The owner can opt phones in with a kind-scoped rule.
	withPhones := append(RuleSet(DefaultRules()), Rule{ID: "phones_unplugged", Metric: MetricOnBattery, Op: OpGT, Value: 0, Scope: "kind:mobile"})
	_, alerts = Evaluate(withPhones, []NodeHealth{powered("n_phone", "mobile", 80, false, "battery", false)}, nil, now)
	require.Equal(t, []string{"phones_unplugged"}, firingIDs(alerts))
	require.NoError(t, ValidateRule(withPhones[len(withPhones)-1]))
	require.Error(t, ValidateRule(Rule{ID: "x", Metric: MetricBattery, Op: OpLT, Value: 30, Scope: "kind:toaster"}))
	require.Error(t, ValidateRule(Rule{ID: "x", Metric: MetricOnBattery, Op: OpLT, Value: 1}))
}

// A node scoped threshold for its own battery replaces the fleet one, and a node that reports no power
// is never judged on it.
func TestPowerRulesAreOwnerConfigurableAndAbsentIsNotJudged(t *testing.T) {
	now := time.Now()
	rules := append(RuleSet(DefaultRules()), Rule{ID: "sensor_battery", Metric: MetricBattery, Op: OpLT, Value: 40, Scope: "n_sensor"})
	_, alerts := Evaluate(rules, []NodeHealth{powered("n_sensor", "board", 35, false, "battery", false)}, nil, now)
	require.Contains(t, firingIDs(alerts), "sensor_battery")
	require.NotContains(t, firingIDs(alerts), "battery")

	_, alerts = Evaluate(rules, []NodeHealth{{ID: "n_nas", Name: "nas", Kind: "host", Presence: string(PresenceVerified)}}, nil, now)
	require.Empty(t, alerts, "no power block: nothing is judged, mains is never assumed")

	stale := powered("n_pi", "host", 5, false, "battery", false)
	stale.PowerAge = 2 * DefaultTTL
	_, alerts = Evaluate(rules, []NodeHealth{stale}, nil, now)
	require.Empty(t, alerts, "a stale reading is never judged as current")
}

// Ruled 2026-10-13: the DEFAULT offline rule skips a phone whose last beat said its app went to the
// background; an owner's own offline rule still judges it.
func TestTheDefaultOfflineRuleSkipsASleepingPhone(t *testing.T) {
	now := time.Now()
	asleep := NodeHealth{ID: "n_phone", Name: "pocket", Kind: "mobile", Dark: true, DarkFor: time.Hour,
		LastHeard: now.Add(-time.Hour), Asleep: true}
	_, alerts := Evaluate(RuleSet(DefaultRules()), []NodeHealth{asleep}, nil, now)
	require.Empty(t, alerts)

	owner := append(RuleSet(DefaultRules()), Rule{ID: "pocket_offline", Metric: MetricOffline, Op: OpGT, Value: 30, Scope: "n_phone"})
	_, alerts = Evaluate(owner, []NodeHealth{asleep}, nil, now)
	require.Equal(t, []string{"pocket_offline"}, firingIDs(alerts))

	awake := asleep
	awake.Asleep = false
	_, alerts = Evaluate(RuleSet(DefaultRules()), []NodeHealth{awake}, nil, now)
	require.Equal(t, []string{"offline"}, firingIDs(alerts), "a phone that went silent in front is still offline")

	// A firing default offline call on a phone that then says it is asleep is closed, not left standing.
	st, _ := Evaluate(RuleSet(DefaultRules()), []NodeHealth{awake}, nil, now)
	_, alerts = Evaluate(RuleSet(DefaultRules()), []NodeHealth{asleep}, st, now)
	require.Len(t, alerts, 1)
	require.False(t, alerts[0].Firing)
}

func TestSupplyLineWordsAnyNodesPower(t *testing.T) {
	pct, no, yes, mins := 42, false, true, 75
	require.Equal(t, "battery 42% · not charging · on battery · low power mode · about 1h left",
		SupplyLine(&store.EdgePower{BatteryPct: &pct, Charging: &no, Source: "battery", LowPower: &yes, MinutesLeft: &mins}))
	require.Equal(t, "on external power", SupplyLine(&store.EdgePower{Source: "ac"}))
	require.Empty(t, SupplyLine(nil))
	require.False(t, strings.Contains(SupplyLine(&store.EdgePower{Source: "unknown"}), "unknown"))
}

// Audit 2026-10-27: a power call does not stand forever once the reading is gone. A node still heard
// with no power report (or a stale one) has the call closed, in words that do not claim the power is
// back; a dark node keeps it, as every other metric does.
func TestAPowerCallClosesWhenAHeardNodeStopsReportingIt(t *testing.T) {
	now := time.Now()
	rules := RuleSet(DefaultRules())
	st, alerts := Evaluate(rules, []NodeHealth{powered("n_pi", "host", 80, false, "battery", false)}, nil, now)
	require.Equal(t, []string{"on_battery"}, firingIDs(alerts))

	dark := NodeHealth{ID: "n_pi", Name: "pi", Kind: "host", Dark: true, DarkFor: time.Minute, LastHeard: now.Add(-time.Minute)}
	held, alerts := Evaluate(rules, []NodeHealth{dark}, st, now)
	require.True(t, held.Firing("on_battery", "n_pi"), "a dark node is no evidence either way")
	for _, a := range alerts {
		require.NotEqual(t, "on_battery", a.RuleID, "the power call is neither cleared nor repeated while dark")
	}

	for name, h := range map[string]NodeHealth{
		"no power report": {ID: "n_pi", Name: "pi", Kind: "host", Presence: string(PresenceVerified), LastHeard: now},
		"a stale one": func() NodeHealth {
			s := powered("n_pi", "host", 80, false, "battery", false)
			s.Name, s.PowerAge, s.LastHeard = "pi", 2*DefaultTTL, now
			return s
		}(),
	} {
		next, alerts := Evaluate(rules, []NodeHealth{h}, st, now)
		require.False(t, next.Firing("on_battery", "n_pi"), name)
		require.Len(t, alerts, 1, name)
		require.False(t, alerts[0].Firing, name)
		require.Contains(t, alerts[0].String(), "pi no longer reports its power", name)
		require.NotContains(t, alerts[0].String(), "back on external power", name)
	}
}

// Review 2026-10-27: the close for a reading that is no longer reported reads the same on every
// surface - the sinks (Discord, Telegram, a webhook) and the events timeline - and never as a value
// that went back under its line. And only a WHOLE power report that is gone or stale closes a call:
// a beat that leaves one field out is not a reason to page a close and then a re-fire.
func TestAnUnreportedCloseReadsTheSameEverywhere(t *testing.T) {
	a := Alert{At: time.Now(), RuleID: "on_battery", NodeID: "n_pi", Node: "pi", Metric: MetricOnBattery,
		Op: OpGT, Threshold: 0, Unreported: true}
	require.Contains(t, AlertWords(a), "pi no longer reports its power")
	require.NotContains(t, AlertWords(a), "on_battery 0")
	low := a
	low.Metric, low.Op, low.Threshold = MetricBattery, OpLT, 20
	require.NotContains(t, AlertWords(low), "battery 0")
	require.Contains(t, alertEventDetail(low), "no longer reports its power")
	require.NotContains(t, alertEventDetail(low), "back under")
	body, err := sinkBody(SinkWebhook, low)
	require.NoError(t, err)
	require.Contains(t, string(body), `"unreported":true`)
	require.NotContains(t, string(body), `"value":0`)
}

// Adversarial review 2026-10-27: holding a call while ONE field is missing from a fresh report made it
// stand forever (a battery taken out of a machine whose report still says its source; a UPS HAT whose
// report never names a source). A fresh report from a heard node that no longer carries the reading
// closes the call as unreported. A charging battery with no source named is on external power.
func TestAFreshReportWithoutTheReadingClosesTheCall(t *testing.T) {
	now := time.Now()
	rules := RuleSet(DefaultRules())
	st, _ := Evaluate(rules, []NodeHealth{powered("n_pi", "host", 15, false, "ac", false)}, nil, now)
	require.True(t, st.Firing("battery", "n_pi"))
	removed := NodeHealth{ID: "n_pi", Name: "pi", Kind: "host", Presence: string(PresenceVerified), LastHeard: now,
		Power: &store.EdgePower{Source: "ac"}}
	next, alerts := Evaluate(rules, []NodeHealth{removed}, st, now)
	require.False(t, next.Firing("battery", "n_pi"), "the battery is gone: nothing is left to call low")
	require.Len(t, alerts, 1)
	require.True(t, alerts[0].Unreported)
}

func TestAChargingBatteryWithNoSourceIsNotOnBattery(t *testing.T) {
	now := time.Now()
	rules := RuleSet(DefaultRules())
	st, _ := Evaluate(rules, []NodeHealth{powered("n_pi", "host", 50, false, "battery", false)}, nil, now)
	require.True(t, st.Firing("on_battery", "n_pi"))
	charging := powered("n_pi", "host", 90, true, "", false)
	charging.LastHeard = now
	next, alerts := Evaluate(rules, []NodeHealth{charging}, st, now)
	require.False(t, next.Firing("on_battery", "n_pi"))
	require.Len(t, alerts, 1)
	require.False(t, alerts[0].Unreported, "charging is external power: the call clears as power back, not as unreported")
	require.Contains(t, alerts[0].String(), "back on external power")
}

// Adversarial review 2026-10-27: a node that falls silent is not swept dark at once, but its power
// goes stale with it; that gap must not close the call. Only a node HEARD within the window closes one.
func TestASilentNodeKeepsItsPowerCallBeforeItIsSweptDark(t *testing.T) {
	t0 := time.Now()
	rules := RuleSet(DefaultRules())
	st, _ := Evaluate(rules, []NodeHealth{powered("n_pi", "host", 50, false, "battery", false)}, nil, t0)
	later := t0.Add(DefaultTTL + 20*time.Second)
	silent := powered("n_pi", "host", 50, false, "battery", false)
	silent.LastHeard, silent.PowerAge = t0, DefaultTTL+20*time.Second // not yet swept: Dark is false
	next, alerts := Evaluate(rules, []NodeHealth{silent}, st, later)
	require.True(t, next.Firing("on_battery", "n_pi"), "silence is no evidence the power came back")
	require.Empty(t, alerts)
}

// Adversarial review 2026-10-27: a dialed member's power report is dated by the AUTHORITY when it is
// recorded, never by the member's clock (one a day ahead was judged current for a day; one behind, a
// Pi with no clock before NTP, was never judged), and a reading outside the bounds presence enforces
// is dropped, not stored.
func TestADialedMembersPowerIsDatedHereAndBounded(t *testing.T) {
	now := time.Now()
	f := NewFleet(store.NewMem(), "acct-1")
	f.SetClock(func() time.Time { return now })
	observe := func(p *store.EdgePower) *store.EdgePower {
		_, err := f.Observe("n_pi", Observation{Name: "pi", Kind: "host", Addr: "192.168.1.9:1", Fingerprint: "ab",
			Instances: []Instance{{Name: "serve", Resources: &store.EdgeResources{ReadAt: now.Unix(), Power: p}}}})
		require.NoError(t, err)
		hs, err := FleetHealth(f, now)
		require.NoError(t, err)
		require.Len(t, hs, 1)
		return hs[0].Power
	}
	pct, no := 5, false
	for _, readAt := range []int64{now.Add(24 * time.Hour).Unix(), 3600} {
		got := observe(&store.EdgePower{BatteryPct: &pct, Charging: &no, Source: "battery", ReadAt: readAt})
		require.NotNil(t, got)
		require.Equal(t, now.Unix(), got.ReadAt, "dated by this authority, whatever the member's clock said")
	}
	neg, big, over := -30, MaxMinutesLeft+1, 101
	for name, p := range map[string]*store.EdgePower{
		"a negative time left": {BatteryPct: &pct, MinutesLeft: &neg},
		"over a year left":     {BatteryPct: &pct, MinutesLeft: &big},
		"a battery over 100":   {BatteryPct: &over},
		"a source not a word":  {BatteryPct: &pct, Source: "solar"},
	} {
		require.Nil(t, observe(p), name)
	}
}
