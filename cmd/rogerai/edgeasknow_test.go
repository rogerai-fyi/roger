package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

// features/edge/fleet_window.feature §3: ask a unit to report now, over its face, bounded.
func TestAskingAUnitToReportNow(t *testing.T) {
	useTempConfig(t)
	edgeWriteAuth(t, "owner")
	old := time.Now().Add(-10 * time.Minute)
	edgeSeed(t,
		store.EdgeNode{ID: "n_nas", Name: "nas", Kind: "host", Presence: string(edge.PresenceVerified), LastSeen: old.Unix(), Pin: "ab",
			Transports: []store.EdgeTransport{{Kind: "lan", Addr: "127.0.0.1:44190", Fingerprint: "ab"}}},
		store.EdgeNode{ID: "n_nina", Name: "nina", Kind: "mobile", Presence: string(edge.PresenceVerified), LastSeen: old.Unix()},
	)
	prev := edgeDialPeer
	t.Cleanup(func() {
		edgeDialPeer = prev
		edgeAsked.Lock()
		edgeAsked.at, edgeAsked.says = map[string]time.Time{}, map[string]string{}
		edgeAsked.Unlock()
	})
	dials := 0
	fp := "ab"
	var fail error
	edgeDialPeer = func(ctx context.Context, addr string) (*edge.Peer, error) {
		dials++
		if fail != nil {
			return nil, fail
		}
		return &edge.Peer{Fingerprint: fp, Describe: edge.Describe{Kind: "host",
			Instances: []edge.Instance{{Name: "nas", Bands: []string{"qwen3-4b"}}}}}, nil
	}
	now := time.Now()

	_, err := edgeAskNow("nina", now)
	require.ErrorContains(t, err, "can only be heard, not asked")
	_, err = edgeAskNow("nowhere", now)
	require.ErrorContains(t, err, "no such node")

	fail = errors.New("connection refused")
	_, err = edgeAskNow("nas", now)
	require.ErrorContains(t, err, "did not answer within 3s")
	fail = nil

	fp = "cd"
	_, err = edgeAskNow("nas", now)
	require.ErrorContains(t, err, "not the one it was adopted with")
	_, n, _, _ := edgeLookup("nas")
	require.Equal(t, old.Unix(), n.LastSeen, "a unit with the wrong certificate is not recorded")
	fp = "ab"

	says, err := edgeAskNow("nas", now)
	require.NoError(t, err)
	require.Contains(t, says, "nas answered in")
	_, n, _, _ = edgeLookup("nas")
	require.Greater(t, n.LastSeen, old.Unix(), "the fresh answer is recorded")
	require.Equal(t, []string{"qwen3-4b"}, n.Instances[0].Bands)

	// Asked again within the floor: answered from the last one, saying so, without dialing.
	before := dials
	says, err = edgeAskNow("nas", now.Add(2*time.Second))
	require.NoError(t, err)
	require.Contains(t, says, "asked 2s ago - nas answered in")
	require.Equal(t, before, dials, "the floor stops a monitor becoming the load")
	_, err = edgeAskNow("nas", now.Add(edgeAskFloor+time.Second))
	require.NoError(t, err)
	require.Equal(t, before+1, dials)
}
