package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// openScan gives a test fresh scopes and opens one of kind k, as the [3] EDGE screen (edge) or
// the LAN dial (lan) does while it shows. It is closed when the test ends.
func openScan(t *testing.T, k scanKind) {
	t.Helper()
	prev := edgeScans
	edgeScans = newScanScopes()
	end := edgeScans.Begin(k)
	t.Cleanup(func() { end(); edgeScans = prev })
}

func TestScanScopesHeldAndLeased(t *testing.T) {
	s := newScanScopes()
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }
	require.False(t, s.Active(scanEdge), "nothing open")

	end := s.Begin(scanEdge)
	require.True(t, s.Active(scanEdge))
	require.False(t, s.Active(scanLAN), "an Edge scan is not a LAN scan")
	end()
	end() // idempotent: a second close does not close somebody else's scope
	require.False(t, s.Active(scanEdge))

	a, b := s.Begin(scanLAN), s.Begin(scanLAN)
	a()
	require.True(t, s.Active(scanLAN), "two holders: one closing leaves the other")
	b()
	require.False(t, s.Active(scanLAN))

	s.Lease(scanEdge, 5*time.Second)
	require.True(t, s.Active(scanEdge))
	now = now.Add(4 * time.Second)
	s.Lease(scanEdge, 5*time.Second) // a renewal moves the end out
	now = now.Add(4 * time.Second)
	require.True(t, s.Active(scanEdge), "renewed")
	now = now.Add(2 * time.Second)
	require.False(t, s.Active(scanEdge), "a lease nobody renews runs out")
}

func TestEdgeMayBrowseOnlyInAnEdgeOrOnRequest(t *testing.T) {
	useTempConfig(t)
	prev := edgeScans
	edgeScans = newScanScopes()
	t.Cleanup(func() { edgeScans = prev })
	require.False(t, edgeMayBrowse(scanEdge), "a fresh install never browses on its own")
	end := edgeScans.Begin(scanEdge)
	require.True(t, edgeMayBrowse(scanEdge), "it browses on request")
	end()
	runEdgeCLI(t, "edge", "authority", "local", "shed")
	require.True(t, edgeMayBrowse(scanEdge), "an Edge's authority browses as before")
	require.True(t, edgeMayBrowse(scanLAN), "and its LAN dial does too")
}
