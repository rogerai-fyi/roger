package edge

// Executable pins for features/edge/events.feature: the merge, the ordering, the narrowing, and the
// history cap.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/store"
)

func eventFleet(now time.Time) []store.EdgeNode {
	return []store.EdgeNode{
		{ID: "n_desk", Name: "desk", History: []store.EdgeEvent{
			{At: now.Add(-3 * time.Hour).Unix(), What: "enrolled", Detail: "desk"},
			{At: now.Add(-2 * time.Hour).Unix(), What: "renamed", Detail: "desk -> desk"},
			{At: now.Add(-30 * time.Minute).Unix(), What: "dark", Detail: "unheard"},
		}},
		{ID: "n_shed", Name: "shed", History: []store.EdgeEvent{
			{At: now.Add(-90 * time.Minute).Unix(), What: "tagged", Detail: "production"},
		}},
	}
}

func TestEventsMergeTheHistoriesAndTheCrossings(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	alerts := []Alert{
		{At: now.Add(-100 * time.Minute), RuleID: "cpu", NodeID: "n_desk", Node: "desk", Metric: MetricCPUTemp,
			Value: 95, Threshold: 85, Op: OpGT, Firing: true},
		{At: now.Add(-10 * time.Minute), RuleID: "cpu", NodeID: "n_desk", Node: "desk", Metric: MetricCPUTemp,
			Value: 70, Threshold: 85, Op: OpGT, Firing: false},
	}
	got := Events(eventFleet(now), alerts, EventQuery{}, now)
	require.Len(t, got, 6)
	// Newest first.
	for i := 1; i < len(got); i++ {
		require.False(t, got[i].At.After(got[i-1].At), "newest first at %d", i)
	}
	require.Equal(t, EventAlert, got[0].Kind)
	require.Equal(t, "cleared cpu_temp", got[0].What)
	require.Contains(t, got[0].Detail, "back under")
	require.Equal(t, "dark", got[1].What)
	require.Equal(t, EventNode, got[1].Kind)
	// The raised line carries the reading and the line it crossed, in the trouble wording.
	var raised Event
	for _, e := range got {
		if e.What == "raised cpu_temp" {
			raised = e
		}
	}
	require.Equal(t, "desk", raised.Node)
	require.Contains(t, raised.Detail, "95")
	require.Contains(t, raised.Detail, "over")

	// An offline crossing reads in its own words, and a test call is marked.
	off := Events(nil, []Alert{
		{At: now, NodeID: "n_ibex", Node: "gentle-ibex-14", Metric: MetricOffline, Threshold: 5, Op: OpGT,
			Firing: true, DarkForS: 3600},
		{At: now.Add(-time.Minute), NodeID: "n_ibex", Node: "gentle-ibex-14", Metric: MetricOffline, Firing: false},
		{At: now.Add(-2 * time.Minute), NodeID: "n_desk", Node: "desk", Metric: MetricCPUTemp, Firing: true, Test: true},
	}, EventQuery{}, now)
	require.Len(t, off, 3)
	require.Contains(t, off[0].Detail, "unheard for")
	require.Equal(t, "heard again", off[1].Detail)
	require.Contains(t, off[2].What, "(test)")
}

func TestEventsOrderIsStableWithinASecond(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	nodes := []store.EdgeNode{{ID: "n_desk", Name: "desk", History: []store.EdgeEvent{
		{At: now.Unix(), What: "renamed"}, {At: now.Unix(), What: "dark"},
	}}}
	first := Events(nodes, nil, EventQuery{}, now)
	for range 5 {
		require.Equal(t, first, Events(nodes, nil, EventQuery{}, now))
	}
}

func TestEventsCanBeNarrowed(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	alerts := []Alert{{At: now.Add(-10 * time.Minute), NodeID: "n_desk", Node: "desk", Metric: MetricCPUTemp, Firing: true}}

	only := Events(eventFleet(now), alerts, EventQuery{NodeID: "n_shed"}, now)
	require.Len(t, only, 1)
	require.Equal(t, "shed", only[0].Node)

	// A node filter keeps that unit's crossings too.
	withAlert := Events(eventFleet(now), alerts, EventQuery{NodeID: "n_desk"}, now)
	require.Len(t, withAlert, 4)

	recent := Events(eventFleet(now), alerts, EventQuery{Since: time.Hour}, now)
	for _, e := range recent {
		require.False(t, e.At.Before(now.Add(-time.Hour)))
	}
	require.Len(t, recent, 2, "the dark line and the crossing")

	limited := Events(eventFleet(now), alerts, EventQuery{Limit: 2}, now)
	require.Len(t, limited, 2)
	require.Equal(t, recent[0].What, limited[0].What, "the newest, not the oldest")

	require.Len(t, Events(eventFleet(now), alerts, EventQuery{Limit: 0}, now), 5, "zero means the default")
	require.Empty(t, Events(nil, nil, EventQuery{}, now))
}

func TestNodeHistoryIsCapped(t *testing.T) {
	f := NewFleet(store.NewMem(), "acct-1")
	n := store.EdgeNode{ID: "n_desk", Name: "desk", Kind: string(Host)}
	for i := 0; i < MaxNodeHistory+40; i++ {
		n.History = append(n.History, store.EdgeEvent{At: int64(i), What: "seen", Detail: "filler"})
	}
	got, err := f.Enroll(n)
	require.NoError(t, err)
	require.Len(t, got.History, MaxNodeHistory, "the enroll path caps too")

	// And every write after it: five more events keep the newest, drop the oldest.
	for i := 0; i < 5; i++ {
		require.NoError(t, f.RecordEvent("n_desk", "probe", "later"))
	}
	after, ok, err := f.Get("n_desk")
	require.NoError(t, err)
	require.True(t, ok)
	require.Len(t, after.History, MaxNodeHistory)
	last := after.History[len(after.History)-5:]
	for _, e := range last {
		require.Equal(t, "later", e.Detail, "the newest five survived")
	}
	require.NotEqual(t, "filler", after.History[0].Detail+"x", "the oldest were dropped")
}

// A probe that changes nothing is not an event. The host re-runs the serving probe on every pass, so
// an unconditional write filled a node's history with "serve -> VERIFIED" every thirty seconds - the
// events timeline is what caught it.
func TestARepeatedProbeIsNotAnEvent(t *testing.T) {
	f := NewFleet(store.NewMem(), "acct-1")
	_, err := f.Enroll(store.EdgeNode{ID: "n_desk", Name: "desk", Kind: string(Host),
		Instances: []Instance{{Name: "desk", Bands: []string{"qwen"},
			Caps: []store.EdgeCap{{Name: string(Serve), State: string(Claimed)}}}}})
	require.NoError(t, err)

	require.NoError(t, f.RecordInstanceProbe("n_desk", "desk", Serve, true))
	after, _, err := f.Get("n_desk")
	require.NoError(t, err)
	events := len(after.History)
	require.Equal(t, "probe", after.History[events-1].What)

	for range 20 {
		require.NoError(t, f.RecordInstanceProbe("n_desk", "desk", Serve, true))
	}
	after, _, err = f.Get("n_desk")
	require.NoError(t, err)
	require.Len(t, after.History, events, "twenty more passes, and the record is unchanged")
	require.Equal(t, string(Verified), serveStateOf(t, f, "n_desk", "desk"))

	// A probe that FAILS after passing is a real change, and is recorded.
	require.NoError(t, f.RecordInstanceProbe("n_desk", "desk", Serve, false))
	after, _, err = f.Get("n_desk")
	require.NoError(t, err)
	require.Len(t, after.History, events+1)
	require.Equal(t, string(Claimed), serveStateOf(t, f, "n_desk", "desk"))
}

func TestSameBandsIgnoresOrder(t *testing.T) {
	require.True(t, SameBands([]string{"a", "b"}, []string{"b", "a"}))
	require.True(t, SameBands(nil, nil))
	require.False(t, SameBands([]string{"a"}, []string{"a", "b"}))
	require.False(t, SameBands([]string{"a", "a"}, []string{"a", "b"}))
}

// Every band must answer, and a SHRINKING band set keeps the verdict the probe earned: an instance
// that drops a model does not re-prove itself on every pass (features/edge/serving_probe.feature).
func TestBandsWithinDecidesWhenAVerdictCarries(t *testing.T) {
	require.True(t, BandsWithin([]string{"a"}, []string{"a", "b"}), "a subset of what was proven is proven")
	require.True(t, BandsWithin([]string{"b", "a"}, []string{"a", "b"}), "order does not matter")
	require.False(t, BandsWithin([]string{"a", "c"}, []string{"a", "b"}), "c was never proven")
	require.False(t, BandsWithin(nil, []string{"a"}), "advertising nothing is not being proven")
	require.False(t, BandsWithin([]string{"a"}, nil))
}

func TestAShrinkingBandSetKeepsItsVerdict(t *testing.T) {
	f := NewFleet(store.NewMem(), "acct-1")
	claimed := []store.EdgeCap{{Name: string(Serve), State: string(Claimed)}}
	obs := func(bands ...string) Observation {
		return Observation{Name: "desk", Kind: string(Host), Addr: "10.0.0.2:1", Fingerprint: "ab",
			Caps: []Capability{Serve}, Instances: []Instance{{Name: "serve", Bands: bands, Caps: claimed}}}
	}
	_, err := f.Observe("n_desk", obs("qwen", "mistral"))
	require.NoError(t, err)
	require.NoError(t, f.RecordInstanceProbe("n_desk", "serve", Serve, true))
	require.Equal(t, string(Verified), serveStateOf(t, f, "n_desk", "serve"))

	// It drops a model: still proven, because the probe answered for every band it marked.
	_, err = f.Observe("n_desk", obs("qwen"))
	require.NoError(t, err)
	require.Equal(t, string(Verified), serveStateOf(t, f, "n_desk", "serve"))

	// The dropped model comes back: nothing new, still within what was proven... except the record
	// now says only "qwen" was proven, so the wider set must be proven again.
	_, err = f.Observe("n_desk", obs("qwen", "mistral"))
	require.NoError(t, err)
	require.Equal(t, string(Claimed), serveStateOf(t, f, "n_desk", "serve"))

	// A band nobody ever proved always resets it.
	require.NoError(t, f.RecordInstanceProbe("n_desk", "serve", Serve, true))
	_, err = f.Observe("n_desk", obs("qwen", "mistral", "llama-3"))
	require.NoError(t, err)
	require.Equal(t, string(Claimed), serveStateOf(t, f, "n_desk", "serve"))
}

// A run of identical events is one line with a count: an activity view that prints one fact forty
// times hides the other thirty-nine (features/edge/events.feature).
func TestEventsCollapseRepeats(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	var hist []store.EdgeEvent
	for i := 0; i < 40; i++ {
		hist = append(hist, store.EdgeEvent{At: now.Add(-time.Duration(i) * time.Minute).Unix(),
			What: "probe", Detail: "serve -> VERIFIED"})
	}
	nodes := []store.EdgeNode{{ID: "n_desk", Name: "desk", History: hist}}
	got := Events(nodes, nil, EventQuery{}, now)
	require.Len(t, got, 1)
	require.Equal(t, 40, got[0].Count)
	require.Equal(t, now, got[0].At, "the newest of them")
	require.Equal(t, now.Add(-39*time.Minute), got[0].Since, "the oldest of them")

	// Something else between two runs keeps them apart. The renamed line sits between the probe at
	// -20m and the one at -21m, so the runs are 21 and 19.
	withGap := append([]store.EdgeEvent(nil), hist...)
	withGap = append(withGap, store.EdgeEvent{At: now.Add(-20*time.Minute - 30*time.Second).Unix(),
		What: "renamed", Detail: "desk"})
	nodes = []store.EdgeNode{{ID: "n_desk", Name: "desk", History: withGap}}
	got = Events(nodes, nil, EventQuery{}, now)
	require.Len(t, got, 3)
	require.Equal(t, 21, got[0].Count)
	require.Equal(t, "renamed", got[1].What)
	require.Equal(t, 0, got[1].Count, "a single event carries no count")
	require.Equal(t, 19, got[2].Count)

	// A crossing and a node event that happen to read alike are still separate lines.
	mixed := Events([]store.EdgeNode{{ID: "n_desk", Name: "desk", History: []store.EdgeEvent{
		{At: now.Unix(), What: "raised cpu_temp"}}}},
		[]Alert{{At: now, NodeID: "n_desk", Node: "desk", Metric: MetricCPUTemp, Firing: true}}, EventQuery{}, now)
	require.Len(t, mixed, 2, "kinds are not folded together")

	// The limit counts collapsed lines, so a flood cannot crowd out the rest.
	flood := Events(nodes, nil, EventQuery{Limit: 2}, now)
	require.Len(t, flood, 2)
	require.Equal(t, "renamed", flood[1].What)
}
