package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/store"
)

func TestEdgeSelectGrantPicksALiveMatch(t *testing.T) {
	now := time.Now()
	future := now.Add(time.Hour).Unix()
	grants := []store.Grant{
		{ID: "g_other", Caller: "n_someone", Nodes: []string{"n_ws"}, Caps: []string{"operate"}, ExpiresAt: future},
		{ID: "g_ok", Caller: "n_me", Nodes: []string{"n_ws"}, Caps: []string{"operate"}, ExpiresAt: future},
	}
	g, ok := edgeSelectGrant(grants, "n_me", "n_ws", "operate", now)
	require.True(t, ok)
	require.Equal(t, "g_ok", g.ID)
}

func TestEdgeSelectGrantSkipsRevokedAndExpired(t *testing.T) {
	now := time.Now()
	grants := []store.Grant{
		{ID: "g_revoked", Caller: "n_me", Nodes: []string{"n_ws"}, Caps: []string{"operate"}, ExpiresAt: now.Add(time.Hour).Unix(), Revoked: true},
		{ID: "g_expired", Caller: "n_me", Nodes: []string{"n_ws"}, Caps: []string{"operate"}, ExpiresAt: now.Add(-time.Hour).Unix()},
	}
	_, ok := edgeSelectGrant(grants, "n_me", "n_ws", "operate", now)
	require.False(t, ok, "no live grant remains")
}

func TestEdgeSelectGrantSelfGrantMatchesAnyCaller(t *testing.T) {
	now := time.Now()
	grants := []store.Grant{
		{ID: "g_self", Caller: "", Nodes: []string{"n_ws"}, Caps: []string{"operate"}, ExpiresAt: now.Add(time.Hour).Unix(), Self: true},
	}
	g, ok := edgeSelectGrant(grants, "n_whoever", "n_ws", "operate", now)
	require.True(t, ok, "an empty-caller Self grant matches any of the owner's own instances")
	require.Equal(t, "g_self", g.ID)
}

func TestEdgeSelectGrantRequiresTheCapability(t *testing.T) {
	now := time.Now()
	grants := []store.Grant{
		{ID: "g", Caller: "n_me", Nodes: []string{"n_ws"}, Caps: []string{"sense"}, ExpiresAt: now.Add(time.Hour).Unix()},
	}
	_, ok := edgeSelectGrant(grants, "n_me", "n_ws", "operate", now)
	require.False(t, ok, "a sense grant does not authorize operate")
}

func TestEdgeSelectGrantRequiresTheCallee(t *testing.T) {
	now := time.Now()
	grants := []store.Grant{
		{ID: "g", Caller: "n_me", Nodes: []string{"n_other"}, Caps: []string{"operate"}, ExpiresAt: now.Add(time.Hour).Unix()},
	}
	_, ok := edgeSelectGrant(grants, "n_me", "n_ws", "operate", now)
	require.False(t, ok, "a grant scoped to another node does not address this callee")
}

func TestEdgeSelectGrantEmptyNodesAddressesAny(t *testing.T) {
	now := time.Now()
	grants := []store.Grant{
		{ID: "g_all", Caller: "n_me", Nodes: nil, Caps: []string{"operate"}, ExpiresAt: now.Add(time.Hour).Unix()},
	}
	g, ok := edgeSelectGrant(grants, "n_me", "n_ws", "operate", now)
	require.True(t, ok, "an empty Nodes list means all of the owner's nodes")
	require.Equal(t, "g_all", g.ID)
}
