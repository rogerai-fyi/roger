package main

// `roger edge fleet` opens the Limoni fleet screen over the real Edge; its actions are the same
// code the CLI's adopt/name/forget/prefer run, so what the screen does the CLI could have done.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/edgeauth"
	"rogerai.fm/roger/v6/internal/fleetview"
	"rogerai.fm/roger/v6/internal/store"
)

func TestEdgeFleetScreenReadsAndManagesTheRealEdge(t *testing.T) {
	useTempConfig(t)
	edgeWriteAuth(t, "owner")
	now := time.Now()
	edgeSeed(t,
		store.EdgeNode{ID: "n_nas", Name: "nas", Kind: "host", Presence: string(edge.PresenceVerified), LastSeen: now.Unix()},
		store.EdgeNode{ID: "n_ibex", Name: "gentle-ibex-14", Kind: "mobile", Presence: string(edge.PresenceDark), LastSeen: now.Add(-2 * time.Hour).Unix()},
	)
	var src fleetview.FleetSource
	var acts fleetview.FleetActions
	prev := edgeRunFleetScreen
	edgeRunFleetScreen = func(ctx context.Context, s fleetview.FleetSource, a fleetview.FleetActions) error {
		src, acts = s, a
		return nil
	}
	t.Cleanup(func() { edgeRunFleetScreen = prev })
	out, code := edgeRun(t, "edge", "fleet")
	require.Equal(t, 0, code, out)
	require.NotNil(t, src)

	snap, err := src()
	require.NoError(t, err)
	names := map[string]fleetview.FleetItem{}
	for _, it := range snap.Items {
		names[it.Name] = it
	}
	require.Equal(t, fleetview.ItemMember, names["nas"].Kind)
	require.True(t, names["gentle-ibex-14"].Dark)
	require.Contains(t, names["gentle-ibex-14"].Presence, "offline")
	require.NotEmpty(t, snap.Self.Root)

	// A node whose record name IS its id was never named by its owner, and the fleet screen calls it
	// by a short id rather than drawing 48 characters as somebody's name (edge.Label).
	longID := "n_" + strings.Repeat("cd", 24)
	edgeSeed(t, store.EdgeNode{ID: longID, Name: longID, Kind: "mobile",
		Presence: string(edge.PresenceVerified), LastSeen: now.Unix()})
	snap, err = src()
	require.NoError(t, err)
	var found *fleetview.FleetItem
	for i, it := range snap.Items {
		if it.ID == longID {
			found = &snap.Items[i]
		}
	}
	require.NotNil(t, found, "the id-named node is missing from the screen")
	require.Equal(t, edge.ShortID(longID), found.Label, "the screen prints a short label")
	require.Equal(t, longID, found.Name, "and still carries the record name, which is what an action takes")

	// Rename and forget act on the real state, the way the CLI does.
	require.NoError(t, acts.Rename("n_nas", "nas-2"))
	require.NoError(t, acts.Forget("n_ibex"))
	st, err := loadEdgeState()
	require.NoError(t, err)
	n, ok, err := st.fleet.Get("n_nas")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "nas-2", n.Name)
	_, ok, err = st.fleet.Get("n_ibex")
	require.NoError(t, err)
	require.False(t, ok, "forgotten")

	require.NoError(t, acts.SetPrefer("market"))
	require.Equal(t, "market", loadConfig().EdgePrefer)
	require.Error(t, acts.SetPrefer("everywhere"), "an unknown preference is refused")

	_, err = acts.Adopt("n_nobody", "nobody")
	require.Error(t, err, "adopting a candidate that was never seen is refused")

	// The GRANTS tab lists what the grant store holds, worded by node name, and revokes through
	// the CLI's own path; SESSIONS is the ledger (empty here, never nil).
	require.NoError(t, edgeGrantStore().Put(store.Grant{ID: "grant_t1", Caller: "n_nas", Nodes: []string{"n_nas"}, Caps: []string{"operate"},
		ExpiresAt: now.Add(time.Hour).Unix()}))
	snap, err = src()
	require.NoError(t, err)
	require.Len(t, snap.Grants, 1)
	require.Equal(t, "nas-2", snap.Grants[0].Callee, "the callee is named, not an id")
	require.Equal(t, "live", snap.Grants[0].State)
	require.NoError(t, acts.RevokeGrant("grant_t1"))
	snap, _ = src()
	require.Equal(t, "REVOKED", snap.Grants[0].State, "a revoked grant is listed as revoked, never hidden")
	require.Error(t, acts.RevokeGrant("grant_nope"))
	require.NotNil(t, snap.Sessions)

	require.False(t, snap.Self.AgentActive)
	require.NoError(t, acts.SetAgent(true))
	snap, _ = src()
	require.True(t, snap.Self.AgentActive, "agent on is read back from the config")
}

// A change made by another process (the fleet screen, or `roger edge forget` in a second
// terminal) must not be undone by the running host's next save: the host adopts what changed on
// disk before it writes.
func TestHostAdoptsEditsMadeByAnotherProcess(t *testing.T) {
	useTempConfig(t)
	edgeWriteAuth(t, "owner")
	now := time.Now()
	edgeSeed(t,
		store.EdgeNode{ID: "n_nas", Name: "nas", Kind: "host", Presence: string(edge.PresenceVerified), LastSeen: now.Unix()},
		store.EdgeNode{ID: "n_ibex", Name: "gentle-ibex-14", Kind: "mobile", Presence: string(edge.PresenceDark), LastSeen: now.Unix()},
	)
	h, err := newEdgeHost("roger-desk")
	require.NoError(t, err)
	require.NoError(t, h.st.save()) // the host's own write; what follows is someone else's
	time.Sleep(20 * time.Millisecond)

	other, err := loadEdgeState()
	require.NoError(t, err)
	require.NoError(t, other.fleet.Rename("n_nas", "nas-2"))
	require.NoError(t, other.fleet.Forget("n_ibex"))
	require.NoError(t, other.save())

	// Meanwhile the host itself learned a node (discovery enrolled it in memory, not yet saved).
	_, err = h.st.fleet.Enroll(store.EdgeNode{ID: "n_new", Name: "new-pi", Kind: "board", Presence: string(edge.PresenceVerified), LastSeen: now.Unix()})
	require.NoError(t, err)

	require.NoError(t, h.st.save()) // the host's next ordinary save (observeSelf, adopt, a pass): adoption happens IN save
	n, ok, err := h.st.fleet.Get("n_nas")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "nas-2", n.Name, "the rename made elsewhere is adopted")
	_, ok, err = h.st.fleet.Get("n_ibex")
	require.NoError(t, err)
	require.False(t, ok, "the forget made elsewhere is adopted")
	again, err := loadEdgeState()
	require.NoError(t, err)
	_, ok, _ = again.fleet.Get("n_ibex")
	require.False(t, ok, "and the host's save does not resurrect it")
	_, ok, _ = again.fleet.Get("n_new")
	require.True(t, ok, "a node the host added in memory is not mistaken for a forget made elsewhere")

	// A node ADDED on disk by another process (a serving-path adopt from the screen) is adopted too.
	other, err = loadEdgeState()
	require.NoError(t, err)
	_, err = other.fleet.Enroll(store.EdgeNode{ID: "n_added", Name: "added-pi", Kind: "board", Presence: string(edge.PresenceVerified), LastSeen: now.Unix()})
	require.NoError(t, err)
	time.Sleep(20 * time.Millisecond)
	require.NoError(t, other.save())
	require.NoError(t, h.st.save())
	_, ok, _ = h.st.fleet.Get("n_added")
	require.True(t, ok, "the node adopted elsewhere is in the host's memory")
}

// A dark member re-advertising as a candidate is ONE unit on the screen (asking to re-join), and a
// standing claim grant is told on it with the authority's port.
func TestEdgeFleetScreenTellsAReJoinOnOneUnit(t *testing.T) {
	useTempConfig(t)
	edgeWriteAuth(t, "owner")
	t.Setenv(edge.EnvDiscovery, "0")
	_, code := edgeRun(t, "edge", "authority", "local", "shed")
	require.Equal(t, 0, code)
	now := time.Now()
	edgeSeed(t, store.EdgeNode{ID: "n_ibex", Name: "gentle-ibex-14", Kind: "mobile", Presence: string(edge.PresenceDark), LastSeen: now.Add(-2 * time.Hour).Unix()})
	st, err := loadEdgeState()
	require.NoError(t, err)
	st.candidates = []store.EdgeNode{{ID: "n_ibex", Name: "gentle-ibex-14", Kind: "mobile"}}
	require.NoError(t, st.save())
	local, ok, err := edgeauth.OpenLocal(edgeAuthDir())
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, local.GrantClaim("n_ibex", "gentle-ibex-14"))

	snap, err := edgeFleetSource()
	require.NoError(t, err)
	seen := 0
	for _, it := range snap.Items {
		if it.Name != "gentle-ibex-14" {
			continue
		}
		seen++
		require.Equal(t, fleetview.ItemMember, it.Kind)
		require.True(t, it.Asking, "the candidate advert is told on the member row")
		require.True(t, it.ClaimStanding, "the standing grant is told")
		require.Empty(t, it.AuthPort, "no host has ever listened here: the screen must not name a port")
	}
	require.Equal(t, 1, seen, "one unit, not a member and a candidate")
}

// A unit over a rule's line is a standing TROUBLE call on the screen, with its value; k says
// roger through the alert log, and the call stays until the condition clears.
func TestEdgeFleetScreenStandingCallsAndRoger(t *testing.T) {
	useTempConfig(t)
	edgeWriteAuth(t, "owner")
	now := time.Now()
	temp := 99.0
	edgeSeed(t, store.EdgeNode{ID: "n_hot", Name: "hot-pi", Kind: "board", Presence: string(edge.PresenceVerified), LastSeen: now.Unix(),
		Instances: []store.EdgeInstance{{Name: "sensor", Resources: &store.EdgeResources{CPUTempC: &temp, ReadAt: now.Unix()}}}})
	snap, err := edgeFleetSource()
	require.NoError(t, err)
	require.Len(t, snap.Alarms, 1)
	require.Equal(t, "hot-pi", snap.Alarms[0].Node)
	require.Equal(t, "cpu_temp > 85", snap.Alarms[0].When)
	require.Equal(t, "99.0°C", snap.Alarms[0].Value)
	require.False(t, snap.Alarms[0].Acked)
	require.NoError(t, edgeFleetActions().Roger("cpu_temp", "n_hot"))
	snap, _ = edgeFleetSource()
	require.True(t, snap.Alarms[0].Acked)
	require.NotEmpty(t, snap.Alarms[0].AckBy)
	require.Error(t, edgeFleetActions().Roger("disk", "n_hot"), "only a standing call can be acknowledged")
}

// b binds a model and a job to a roger on this machine through the same record the CLI writes;
// c clears it; s puts a standing call on stand by for an hour.
func TestEdgeFleetScreenBindsJobsAndStandsBy(t *testing.T) {
	useTempConfig(t)
	edgeWriteAuth(t, "owner")
	acts := edgeFleetActions()
	require.NoError(t, acts.BindJob("watcher", "qwen3-8b", "serve"))
	j, ok := edge.AgentJobOf(edgeJobsDir(), "watcher")
	require.True(t, ok)
	require.Equal(t, "qwen3-8b", j.Model)
	require.Equal(t, "serve", j.Job)
	require.True(t, j.Share, "serve shares the model")
	require.Error(t, acts.BindJob("watcher", "", "dance"), "a job is serve, watch or relay")
	require.NoError(t, acts.ClearJob("watcher"))
	_, ok = edge.AgentJobOf(edgeJobsDir(), "watcher")
	require.False(t, ok)

	now := time.Now()
	temp := 99.0
	edgeSeed(t, store.EdgeNode{ID: "n_hot", Name: "hot-pi", Kind: "board", Presence: string(edge.PresenceVerified), LastSeen: now.Unix(),
		Instances: []store.EdgeInstance{{Name: "sensor", Resources: &store.EdgeResources{CPUTempC: &temp, ReadAt: now.Unix()}}}})
	snap, err := edgeFleetSource()
	require.NoError(t, err)
	require.Len(t, snap.Alarms, 1)
	require.NoError(t, acts.StandBy("cpu_temp", "n_hot", time.Hour))
	snap, _ = edgeFleetSource()
	require.True(t, snap.Alarms[0].Shelved)
	require.WithinDuration(t, now.Add(time.Hour), snap.Alarms[0].StandByUntil, 5*time.Second)
}

// Issuing a grant and editing rules from the screens run the CLI's own code paths.
func TestEdgeFleetScreenIssuesGrantsAndEditsRules(t *testing.T) {
	useTempConfig(t)
	edgeWriteAuth(t, "owner")
	t.Setenv(edge.EnvDiscovery, "0")
	_, code := edgeRun(t, "edge", "authority", "local", "shed")
	require.Equal(t, 0, code)
	now := time.Now()
	edgeSeed(t, store.EdgeNode{ID: "n_nas", Name: "nas", Kind: "host", Presence: string(edge.PresenceVerified), LastSeen: now.Unix()})
	acts := edgeFleetActions()
	msg, err := acts.IssueGrant("self", "operate", "nas", "7d")
	require.NoError(t, err)
	require.Contains(t, msg, "issued")
	gs, err := edgeGrantStore().List()
	require.NoError(t, err)
	require.Len(t, gs, 1)
	require.Equal(t, []string{"n_nas"}, gs[0].Nodes)
	_, err = acts.IssueGrant("self", "dance", "nas", "7d")
	require.Error(t, err, "an unknown capability is refused")

	hacts := edgeHealthActions()
	msg, err = hacts.SetRule("memory", "gt", "90", "nas")
	require.NoError(t, err)
	require.Contains(t, msg, "memory")
	rules, err := edgeAlertLog().Rules()
	require.NoError(t, err)
	found := false
	for _, r := range rules {
		if r.Metric == edge.MetricMemory && r.Scope == "n_nas" && r.Value == 90 {
			found = true
		}
	}
	require.True(t, found, "the rule is stored for the resolved node")
	require.NoError(t, hacts.RuleOff("cpu_temp", true))
	rules, _ = edgeAlertLog().Rules()
	for _, r := range rules {
		if r.ID == "cpu_temp" {
			require.True(t, r.Off)
		}
	}
	require.Error(t, hacts.RemoveRule("cpu_temp"), "a default is turned off, never removed")
	require.NoError(t, hacts.RemoveRule("memory-n_nas"))
	_, err = hacts.SetRule("warp", "gt", "1", "")
	require.Error(t, err)
}
