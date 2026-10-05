package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/store"
)

// TestKeepLiveHoldsLiveNodesOnly pins the batch fixture: a node live when a batch starts
// stays live through it, and a node a scenario aged out is never revived.
func TestKeepLiveHoldsLiveNodesOnly(t *testing.T) {
	s := &rpState{foState: &foState{b: newBroker(store.NewMem())}}
	s.b.lastSeen["live"] = time.Now()
	s.b.lastSeen["gone"] = time.Now().Add(-2 * nodeTTL)
	refresh := s.keepLive()
	s.b.lastSeen["live"] = time.Now().Add(-2 * nodeTTL) // a batch slower than nodeTTL
	refresh()
	require.Less(t, time.Since(s.b.lastSeen["live"]), nodeTTL, "a node live at the start stays live")
	require.GreaterOrEqual(t, time.Since(s.b.lastSeen["gone"]), nodeTTL, "an aged-out node is not revived")
}
