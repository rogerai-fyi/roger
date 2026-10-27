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
		"no power report": {ID: "n_pi", Name: "pi", Kind: "host", Presence: string(PresenceVerified)},
		"a stale one": func() NodeHealth {
			s := powered("n_pi", "host", 80, false, "battery", false)
			s.Name, s.PowerAge = "pi", 2*DefaultTTL
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
