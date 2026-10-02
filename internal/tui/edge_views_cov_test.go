package tui

// WHAT THE [3] EDGE SCREEN SAYS about the rogers on this machine and about one selected node: the
// RUNNING HERE block of the graph view, and the node page (enter) for an instance with a job, a dark
// persistent agent, and a member with a confirmed actuator. Rendered for real and read as text.

import (
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

func TestTheGraphViewListsWhatIsRunningHere(t *testing.T) {
	self := edge.SelfStatus{Root: edge.RootLocal, Name: "desk", Discovery: edge.DiscoveryScanning, IntervalS: 30}
	m := networkModel(t, self, nodeOf("b", "pi-greenhouse", "board"))
	m.hooks.EdgeHousehold = func() []edge.Instance {
		return []edge.Instance{
			{Name: "desk", Agent: true, Bands: []string{"qwen-3"}},
			{Name: "worker", Bands: []string{"pico"}},
			{Name: "idle-one"},
		}
	}
	m.hooks.EdgePersistentAgents = func() []edge.PersistentAgent {
		return []edge.PersistentAgent{{Name: "desk"}, {Name: "ghost"}}
	}
	out := stripANSI(m.edgeView(120))
	for _, want := range []string{
		"RUNNING HERE",
		"agent · can operate on other nodes · qwen-3",
		"serving pico",
		"idle-one",
		"◇ ghost",
		"resume: roger edge agent resume ghost",
	} {
		require.Contains(t, out, want)
	}
	require.NotContains(t, out, "◇ desk", "a persistent agent that is live is not drawn twice")
}

func TestTheGraphViewWithNothingRunningHereDrawsNoBlock(t *testing.T) {
	self := edge.SelfStatus{Root: edge.RootLocal, Name: "desk", Discovery: edge.DiscoveryScanning, IntervalS: 30}
	m := networkModel(t, self, nodeOf("b", "pi-greenhouse", "board"))
	require.NotContains(t, stripANSI(m.edgeView(120)), "RUNNING HERE")
}

func nodePage(t *testing.T, m model, pick func(edgeNetItem) bool) string {
	t.Helper()
	m = selectNet(t, m, pick)
	m.edge.netDetail = true
	return stripANSI(m.edgeView(120))
}

func TestTheNodePageOfAnInstanceShowsItsJobAndResources(t *testing.T) {
	m := edgeActionsRig(t)
	m.hooks.EdgeHousehold = func() []edge.Instance {
		return []edge.Instance{
			{Name: "desk", Agent: true},
			{Name: "worker", Bands: []string{"pico"}, Port: 8792,
				Caps:      []store.EdgeCap{{Name: string(edge.Serve), State: string(edge.Verified)}},
				Resources: &store.EdgeResources{GPU: "RTX 4090", GPUMemUsed: 0.426, RAMUsedGB: 12, RAMTotalGB: 31.5}},
			{Name: "bare"},
		}
	}
	m.hooks.EdgeAgentJobs = func() []edge.AgentJob {
		return []edge.AgentJob{
			{Name: "worker", Model: "qwen-3", Use: true, Share: true, Job: "watch", Targets: []string{"attic-pi", "bare-pi"}},
			{Name: "desk", Model: "pico", Use: true},
		}
	}
	out := nodePage(t, m, named("worker"))
	for _, want := range []string{
		"serves a model to the Edge",
		"watch · attic-pi, bare-pi",
		"qwen-3, pico",
		"draws on this for its own work",
		"RTX 4090",
		"43% mem",
		"12 / 31.5 GB",
		"this machine, port 8792",
	} {
		require.Contains(t, out, want)
	}

	out = nodePage(t, m, named("bare"))
	require.Contains(t, out, "on the Edge · not an agent, not serving")
	require.Contains(t, out, "none assigned · press b to bind")
	require.Contains(t, out, "nothing on air")
	require.Contains(t, out, "no capabilities declared")

	out = nodePage(t, m, func(it edgeNetItem) bool { return it.self && !it.machine })
	require.Contains(t, out, "agent · this roger")
	require.Contains(t, out, "pico")
}

func TestTheNodePageOfADarkAgentOffersResumeAndRemove(t *testing.T) {
	out := nodePage(t, edgeActionsRig(t), named("ghost"))
	for _, want := range []string{"persistent agent · offline", "offline - its process is not running",
		"to relaunch it here", "to forget it"} {
		require.Contains(t, out, want)
	}
	require.NotContains(t, out, "JOB", "a dark agent has no job to show")
}

func TestTheNodePageNamesWhoConfirmedAnActuator(t *testing.T) {
	m, _ := actuateModel(t, edge.Verified)
	rows := m.edge.rows
	for i := range rows {
		for j := range rows[i].n.Caps {
			if rows[i].n.Caps[j].Name == string(edge.Actuate) {
				rows[i].n.Caps[j].ConfirmedBy = "desk-owner"
			}
		}
	}
	out := nodePage(t, m, named("shed-door"))
	require.Contains(t, out, "confirmed by desk-owner")
}

func TestEdgeJobLabels(t *testing.T) {
	for _, tc := range []struct {
		j    edge.AgentJob
		want string
	}{
		{edge.AgentJob{Job: "serve", Model: "qwen"}, "serving qwen"},
		{edge.AgentJob{Job: "relay"}, "relay"},
		{edge.AgentJob{Model: "qwen", Share: true}, "sharing qwen"},
		{edge.AgentJob{Model: "qwen", Use: true}, "using qwen"},
		{edge.AgentJob{}, ""},
	} {
		require.Equal(t, tc.want, edgeJobLabel(tc.j))
	}
	require.Equal(t, "43%", fmtPct(0.426))
	require.Equal(t, "100%", fmtPct(1))
}
