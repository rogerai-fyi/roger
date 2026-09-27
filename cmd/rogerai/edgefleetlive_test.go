package main

// `roger edge fleet` opens the Limoni fleet screen over the real Edge; its actions are the same
// code the CLI's adopt/name/forget/prefer run, so what the screen does the CLI could have done.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
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

	h.adoptExternalEdits()
	n, ok, err := h.st.fleet.Get("n_nas")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "nas-2", n.Name, "the rename made elsewhere is adopted")
	_, ok, err = h.st.fleet.Get("n_ibex")
	require.NoError(t, err)
	require.False(t, ok, "the forget made elsewhere is adopted")
	require.NoError(t, h.st.save())
	again, err := loadEdgeState()
	require.NoError(t, err)
	_, ok, _ = again.fleet.Get("n_ibex")
	require.False(t, ok, "and the host's save does not resurrect it")
}
