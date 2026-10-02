package main

// Coverage for the fleet window's read and its keys: every kind of unit the window draws (the hub's
// rogers and their jobs, a dark persistent agent, members in each presence, a candidate), the
// authority's port, the newest-roger comparison, the timeline, and the actions' refusals.

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/fleetview"
	"rogerai.fm/roger/v6/internal/store"
)

func TestEdgeJobWords(t *testing.T) {
	for _, tc := range []struct {
		j    edge.AgentJob
		want string
	}{
		{edge.AgentJob{}, ""},
		{edge.AgentJob{Model: "qwen3-8b"}, "model qwen3-8b"},
		{edge.AgentJob{Job: "serve", Model: "qwen3-8b"}, "serve qwen3-8b"},
		{edge.AgentJob{Job: "watch", Targets: []string{"gate", "cam"}}, "watch gate, cam"},
		{edge.AgentJob{Job: "relay", Model: "tiny"}, "relay tiny"},
		{edge.AgentJob{Job: "watch"}, "watch"},
	} {
		require.Equal(t, tc.want, edgeJobWords(tc.j), "%+v", tc.j)
	}
}

func TestEdgeFleetWindowDrawsEveryKindOfUnit(t *testing.T) {
	self := newFacedAuthority(t, "desk")
	now := time.Now()
	edgeSaveAuthorityPort(45555)

	// A registered agent roger on this machine, running a newer roger than the Pi, with a job.
	reg, err := edge.Register(edgeInstancesDir(), self, edge.Instance{Name: "night", Agent: true,
		Software: &store.EdgeSoftware{Roger: "6.12.0"}}, now)
	require.NoError(t, err)
	t.Cleanup(func() { _ = reg.Deregister() })
	require.NoError(t, edge.SetAgentJob(edgeJobsDir(), edge.AgentJob{Name: "night", Node: self, Job: "serve", Model: "tiny"}, now))
	require.NoError(t, edge.MarkPersistent(edgePersistDir(), self, "sleeper", now))

	var hist []store.EdgeEvent
	for i := int64(3); i > 0; i-- {
		hist = append(hist, store.EdgeEvent{At: now.Unix() - i*60, What: "maintain", Detail: "restart roger"})
	}
	edgeSeed(t,
		store.EdgeNode{ID: "n_pi", Name: "pi", Kind: "board", Presence: string(edge.PresenceVerified), LastSeen: now.Unix(),
			Instances: []store.EdgeInstance{{Name: "roger", Bands: []string{"tiny"}, Software: &store.EdgeSoftware{Roger: "6.10.0"}}},
			History:   hist},
		store.EdgeNode{ID: "n_old", Name: "old", Kind: "board", Presence: string(edge.PresenceDark)},
		store.EdgeNode{ID: "n_re", Name: "re", Kind: "board", Presence: string(edge.PresenceReenroll), LastSeen: now.Unix()},
	)
	st, err := loadEdgeState()
	require.NoError(t, err)
	st.candidates = []store.EdgeNode{{ID: "n_cand", Name: "fresh", Kind: "board"}}
	require.NoError(t, st.save())
	// A unit never heard from carries no last-seen at all: written as the file holds it, since a save
	// merges and a merge never moves a last-seen backwards.
	b, err := os.ReadFile(edgeStatePath())
	require.NoError(t, err)
	var snapFile edgeSnapshot
	require.NoError(t, json.Unmarshal(b, &snapFile))
	for i := range snapFile.Nodes {
		if snapFile.Nodes[i].ID == "n_old" {
			snapFile.Nodes[i].LastSeen = 0
		}
	}
	b, err = json.Marshal(snapFile)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(edgeStatePath(), b, 0o600))
	require.NoError(t, edgeAlertLog().RecordRead("n_pi", now))

	snap, err := edgeFleetSource()
	require.NoError(t, err)
	by := map[string]fleetview.FleetItem{}
	for _, it := range snap.Items {
		by[it.Name] = it
	}
	require.Equal(t, fleetview.ItemHub, snap.Items[0].Kind)
	require.Equal(t, self, snap.Items[0].ID)
	require.Contains(t, snap.Items[0].Jobs, "night serve tiny", "the hub lists what its rogers are bound to")
	require.Equal(t, fleetview.ItemAgent, by["night"].Kind)
	require.Equal(t, "serve tiny", by["night"].Job)
	require.True(t, by["sleeper"].Dark, "a persistent agent that is not running is drawn dark")
	require.Equal(t, fleetview.ItemAgent, by["sleeper"].Kind)

	_, selfListed := by["desk"]
	require.True(t, selfListed || snap.Items[0].Name == "desk")
	for _, it := range snap.Items[1:] {
		require.NotEqual(t, self, it.ID, "this machine is the hub, never also a member")
	}

	require.Equal(t, "online", by["pi"].Presence)
	require.NotEmpty(t, by["pi"].Behind, "a roger older than the newest on the Edge is told")
	require.False(t, by["pi"].LastRead.IsZero(), "when its app last read the feed")
	require.Equal(t, "45555", by["pi"].AuthPort)
	require.Equal(t, "offline", by["old"].Presence, "dark and never heard: offline with no age")
	require.Equal(t, "needs re-enrollment", by["re"].Presence)
	require.Equal(t, fleetview.ItemCandidate, by["fresh"].Kind)
	require.Equal(t, "discovered", by["fresh"].Presence)

	var ev string
	for _, e := range snap.Events {
		if e.What == "maintain" {
			ev = e.Detail
		}
	}
	require.Contains(t, ev, "restart roger   ×3 over 2m")
}

func TestEdgeFleetWindowKeysRefuseWhatTheCLIRefuses(t *testing.T) {
	newFacedAuthority(t, "desk")
	edgeSeed(t, store.EdgeNode{ID: "n_pi", Name: "pi", Kind: "board"})
	acts := edgeFleetActions()

	require.ErrorContains(t, acts.Forget("ghost"), `no such node on this Edge: "ghost"`)
	require.ErrorContains(t, acts.Rename("ghost", "x"), `no such node on this Edge: "ghost"`)
	require.NoError(t, acts.Rename("pi", "attic-pi"))
	_, n, ok, err := edgeLookup("attic-pi")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "n_pi", n.ID)
	require.NoError(t, acts.Forget("attic-pi"))
	_, _, ok, err = edgeLookup("n_pi")
	require.NoError(t, err)
	require.False(t, ok)

	require.EqualError(t, acts.SetPrefer("cloud"), `prefer must be local, market or local-only, not "cloud"`)
	require.NoError(t, acts.SetPrefer("market"))
	require.Equal(t, "market", loadConfig().EdgePrefer)

	require.EqualError(t, acts.RevokeGrant("grant_nope"), `no such grant "grant_nope"`)
	said, err := acts.IssueGrant("self", "operate", ".", "1h")
	require.NoError(t, err)
	require.Contains(t, said, "issued grant_")
	require.Contains(t, said, "your own instances may operate desk until")
	_, err = acts.IssueGrant("self", "teleport", ".", "1h")
	require.ErrorContains(t, err, "is not an edge capability")
	gs, err := edgeGrantStore().List()
	require.NoError(t, err)
	require.NoError(t, acts.RevokeGrant(gs[0].ID))

	require.Error(t, acts.BindJob("not a name!", "", ""))
	require.EqualError(t, acts.BindJob("night", "", "mine"), `a job is serve, watch or relay, not "mine"`)
	require.NoError(t, acts.BindJob("night", "", "watch"))
	j, ok := edge.AgentJobOf(edgeJobsDir(), "night")
	require.True(t, ok)
	require.Equal(t, "watch", j.Job)
	require.False(t, j.Share, "only serve shares")
	require.NoError(t, acts.ClearJob("night"))
	_, ok = edge.AgentJobOf(edgeJobsDir(), "night")
	require.False(t, ok)
}

func TestEdgeFleetSelectNamesTheUnitOrRefuses(t *testing.T) {
	newFacedAuthority(t, "desk")
	edgeSeed(t, store.EdgeNode{ID: "n_pi", Name: "pi", Kind: "board"})
	prev := edgeRunFleetScreen
	t.Cleanup(func() { edgeRunFleetScreen = prev; edgeFleetSelect = "" })
	opened := 0
	edgeRunFleetScreen = func(context.Context, fleetview.FleetSource, fleetview.FleetActions) error { opened++; return nil }

	out, code := edgeRun(t, "edge", "fleet", "--select", "ghost")
	require.NotEqual(t, 0, code)
	require.Contains(t, out, `no such node on this Edge: "ghost"`)
	require.Zero(t, opened, "the window does not open on a unit that is not there")

	out, code = edgeRun(t, "edge", "fleet", "--select", "pi")
	require.Equal(t, 0, code, out)
	require.Equal(t, "n_pi", edgeFleetSelect, "the window opens on the unit by id")
	require.Equal(t, 1, opened)

	out, code = edgeRun(t, "edge", "fleet", "--bogus")
	require.NotEqual(t, 0, code, out)
}
