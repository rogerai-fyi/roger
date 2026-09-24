package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

// A plain model (no node of that name) is NOT treated as a node address - the band ladder handles it.
func TestUseAddressPlainModelFallsThrough(t *testing.T) {
	useTempConfig(t)
	edgeWriteAuth(t, "owner")
	edgeSeed(t, store.EdgeNode{ID: "n_jetson", Name: "jetson", Kind: "host",
		Presence: string(edge.PresenceVerified),
		Transports: []store.EdgeTransport{{Kind: "lan", Addr: "192.168.1.9:8443", Fingerprint: "cdcd"}},
		Instances: []edge.Instance{{Name: "serve", Bands: []string{"qwen3-8b"},
			Caps: []store.EdgeCap{{Name: string(edge.Serve), State: string(edge.Verified)}}}}})

	_, _, isNode, err := edgeResolveAddressed("qwen3-8b", "")
	require.NoError(t, err)
	require.False(t, isNode, "a model name is not a node address")
}

// A node that serves one band resolves to a pinned peer + the band.
func TestUseAddressNodePins(t *testing.T) {
	useTempConfig(t)
	edgeWriteAuth(t, "owner")
	edgeSeed(t, store.EdgeNode{ID: "n_jetson", Name: "jetson", Kind: "host",
		Presence: string(edge.PresenceVerified),
		Transports: []store.EdgeTransport{{Kind: "lan", Addr: "192.168.1.9:8443", Fingerprint: "cdcd"}},
		Instances: []edge.Instance{{Name: "serve", Bands: []string{"qwen3-8b"},
			Caps: []store.EdgeCap{{Name: string(edge.Serve), State: string(edge.Verified)}}}}})

	peer, band, isNode, err := edgeResolveAddressed("jetson", "")
	require.NoError(t, err)
	require.True(t, isNode)
	require.Equal(t, "qwen3-8b", band)
	require.Equal(t, "192.168.1.9:8443", peer.Addr)
	require.Equal(t, "jetson", peer.Node)
}

// A node that serves nothing is a node address WITH a typed refusal (not a silent fall-through).
func TestUseAddressNodeServesNothingRefuses(t *testing.T) {
	useTempConfig(t)
	edgeWriteAuth(t, "owner")
	edgeSeed(t, store.EdgeNode{ID: "n_pi", Name: "pi", Kind: "host",
		Presence: string(edge.PresenceVerified),
		Transports: []store.EdgeTransport{{Kind: "lan", Addr: "192.168.1.8:8443", Fingerprint: "abab"}},
		Instances: []edge.Instance{{Name: "i", Bands: nil, Caps: nil}}})

	_, _, isNode, err := edgeResolveAddressed("pi", "")
	require.True(t, isNode, "pi is a node on this Edge")
	require.ErrorIs(t, err, edge.ErrServesNoModel)
}
