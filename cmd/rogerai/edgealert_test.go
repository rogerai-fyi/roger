package main

// features/edge/health.feature @cli: `roger edge alert` lists the rules, their scope and whether
// they are firing, and names where alerts go without printing a secret; `alert set` stores a rule
// the next health read evaluates; `health <node>` shows the rules firing for it; `health --json`
// is honest JSON with absent metrics absent.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/edge"
	"rogerai.fm/roger/v7/internal/store"
)

func TestEdgeAlertCLIRulesFireOnTheNextHealthRead(t *testing.T) {
	useTempConfig(t)
	edgeWriteAuth(t, "owner")
	now := time.Now()
	temp := 61.5
	edgeSeed(t,
		store.EdgeNode{ID: "n_jetson", Name: "jetson", Kind: "host", Presence: string(edge.PresenceVerified),
			LastSeen: now.Unix(),
			Instances: []store.EdgeInstance{{Name: "agent", Resources: &store.EdgeResources{
				RAMUsedGB: 8, RAMTotalGB: 32, CPUTempC: &temp,
				Disks:  []store.EdgeDisk{{Mount: "/", UsedGB: 40, TotalGB: 100}},
				ReadAt: now.Unix()}}}},
		store.EdgeNode{ID: "n_attic", Name: "attic", Kind: "host", Presence: string(edge.PresenceDark), LastSeen: now.Add(-2 * time.Hour).Unix()},
	)

	out, code := edgeRun(t, "edge", "alert")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "cpu_temp", "the defaults are listed")
	require.Contains(t, out, "default")
	require.Contains(t, out, "fleet", "with their scope")
	require.Contains(t, out, "sinks", "and where alerts go")
	require.Contains(t, out, "none configured", "nowhere, by default")

	out, code = edgeRun(t, "edge", "alert", "set", "cpu_temp", "gt", "60", "--scope", "fleet")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "cpu_temp > 60")

	// The next health read evaluates it: jetson at 61.5 is over 60.
	out, code = edgeRun(t, "edge", "health", "jetson")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "alerts")
	require.Contains(t, out, "cpu_temp > 60", "the rule firing for it")
	require.Contains(t, out, "61.5")

	out, _ = edgeRun(t, "edge", "alert")
	require.Contains(t, out, "firing", "the list now shows it firing")
	require.Contains(t, out, "jetson")
	require.Contains(t, out, "attic", "and the offline default is firing for the dark node")

	// A per-node rule names the node, never a made-up id.
	out, code = edgeRun(t, "edge", "alert", "set", "disk", "gt", "30", "--scope", "jetson")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "jetson")
	out, code = edgeRun(t, "edge", "alert", "set", "disk", "gt", "35", "--scope", "n_jet") // the same node by id prefix
	require.Equal(t, 0, code, out)
	rules, err := edgeAlertLog().Rules()
	require.NoError(t, err)
	scoped := 0
	for _, r := range rules {
		if r.Scope == "n_jetson" && r.Metric == edge.MetricDisk {
			scoped++
			require.Equal(t, 35.0, r.Value, "the second spelling REPLACED the first: one node, one rule per metric")
		}
	}
	require.Equal(t, 1, scoped)
	out, code = edgeRun(t, "edge", "alert", "set", "offline", "lt", "5")
	require.NotEqual(t, 0, code, "an offline rule only counts upwards")
	out, code = edgeRun(t, "edge", "alert", "set", "disk", "gt", "30", "--scope", "nobody")
	require.NotEqual(t, 0, code, "an unknown node scope is refused")

	out, code = edgeRun(t, "edge", "alert", "set", "warp", "gt", "1")
	require.NotEqual(t, 0, code, "an unknown metric is refused")
	require.Contains(t, out, "cpu_temp", "and the usage names the metrics it knows")

	out, code = edgeRun(t, "edge", "alert", "off", "offline")
	require.Equal(t, 0, code, out)
	out, _ = edgeRun(t, "edge", "alert")
	require.Contains(t, out, "off")

	out, code = edgeRun(t, "edge", "alert", "test", "discord")
	require.NotEqual(t, 0, code)
	require.Contains(t, out, "member", "the only sink is a member's Roger app; nothing pretends a message went to a name nobody has")
}

func TestEdgeHealthJSONIsHonestAboutAbsentMetrics(t *testing.T) {
	useTempConfig(t)
	edgeWriteAuth(t, "owner")
	now := time.Now()
	temp := 61.5
	edgeSeed(t,
		store.EdgeNode{ID: "n_jetson", Name: "jetson", Kind: "host", Presence: string(edge.PresenceVerified),
			LastSeen: now.Unix(),
			Instances: []store.EdgeInstance{{Name: "agent", Resources: &store.EdgeResources{
				RAMUsedGB: 8, RAMTotalGB: 32, CPUTempC: &temp, ReadAt: now.Unix()}}}},
		store.EdgeNode{ID: "n_quiet", Name: "quiet", Kind: "mobile", Presence: string(edge.PresenceVerified), LastSeen: now.Unix()},
	)
	out, code := edgeRun(t, "edge", "health", "--json")
	require.Equal(t, 0, code, out)
	var got []map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &got), out)
	require.Len(t, got, 2)
	byName := map[string]map[string]any{}
	for _, n := range got {
		byName[n["name"].(string)] = n
	}
	rd := byName["jetson"]["readings"].(map[string]any)
	require.Equal(t, 61.5, rd["cpu_temp_c"])
	_, hasLoad := rd["cpu_load1"]
	require.False(t, hasLoad, "an absent metric is absent, not zero")
	require.Nil(t, byName["quiet"]["readings"], "a node reporting nothing has no readings, not zeros")
	require.Contains(t, byName["jetson"], "alerts")
}

// health.feature §4: offline is decided by the always-on AUTHORITY. A host pass on a machine that is
// not the authority does not judge - otherwise every member would fire (and, once sinks land,
// deliver) the same alert.
func TestOnlyTheAuthorityJudgesOnItsPass(t *testing.T) {
	useTempConfig(t)
	edgeWriteAuth(t, "owner")
	now := time.Now()
	edgeSeed(t, store.EdgeNode{ID: "n_attic", Name: "attic", Kind: "host", Presence: string(edge.PresenceDark), LastSeen: now.Add(-2 * time.Hour).Unix()})
	h, err := newEdgeHost("roger-desk")
	require.NoError(t, err)
	h.judgeHealth(now)
	st, err := edgeAlertLog().State()
	require.NoError(t, err)
	require.Empty(t, st, "a member machine's pass does not judge the fleet")
}

// trouble_feed.feature @cli: once the phone has read the feed, `roger edge alert` names it as the
// sink and when it last looked; `alert test <member>` puts one clearly-marked test call in the
// feed without touching any rule.
func TestEdgeAlertNamesThePhoneAsTheSinkAndSendsATestCall(t *testing.T) {
	useTempConfig(t)
	edgeWriteAuth(t, "owner")
	now := pinEdgeRenderNow(t)
	edgeSeed(t, store.EdgeNode{ID: "n_ibex", Name: "gentle-ibex-14", Kind: "mobile", Presence: string(edge.PresenceVerified), LastSeen: now.Unix()})
	out, _ := edgeRun(t, "edge", "alert")
	require.Contains(t, out, "none configured", "before any phone has read the feed")

	require.NoError(t, edgeAlertLog().RecordRead("n_ibex", now.Add(-time.Minute)))
	out, code := edgeRun(t, "edge", "alert")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "the Roger app on gentle-ibex-14")
	require.Contains(t, out, "1m ago")

	out, code = edgeRun(t, "edge", "alert", "test", "gentle-ibex-14")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "test call")
	feed, err := edgeAlertLog().Feed(time.Time{}, now.Add(time.Second), func(id string) string { return map[string]string{"n_ibex": "gentle-ibex-14"}[id] })
	require.NoError(t, err)
	tests := 0
	for _, tr := range feed.Transitions {
		if tr.Test {
			tests++
		}
	}
	require.Equal(t, 1, tests)
	require.Empty(t, feed.Standing, "a test call is not a standing call")

	out, code = edgeRun(t, "edge", "alert", "test", "nobody")
	require.NotEqual(t, 0, code, "a test call goes to a member, not to a name nobody has")
}
