package edge

import (
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/store"
)

// seed builds a fleet with the given nodes enrolled verbatim (Enroll preserves Presence, Instances
// and Transports), so ServingPeer can be tested against exact fleet states.
func seedFleet(t *testing.T, nodes ...store.EdgeNode) *Fleet {
	t.Helper()
	f := NewFleet(store.NewMem(), "acct-1")
	for _, n := range nodes {
		_, err := f.Enroll(n)
		require.NoError(t, err, "seed %s", n.Name)
	}
	return f
}

func lanT(addr string) []store.EdgeTransport {
	return []store.EdgeTransport{{Kind: "lan", Addr: addr, Fingerprint: "cd" + addr}}
}

// a serving instance: serve VERIFIED for the given bands.
func serveInst(name string, bands ...string) Instance {
	return Instance{Name: name, Bands: bands, Caps: []store.EdgeCap{{Name: string(Serve), State: string(Verified)}}}
}

func srvNode(name, kind, addr string, presence Presence, insts ...Instance) store.EdgeNode {
	return store.EdgeNode{ID: "n_" + name, Name: name, Kind: kind, Presence: string(presence),
		Transports: lanT(addr), Instances: insts}
}

func TestServingPeerResolvesAndRefuses(t *testing.T) {
	// A node serving exactly one band, addressed by name.
	t.Run("node serving one band", func(t *testing.T) {
		f := seedFleet(t, srvNode("jetson", "host", "192.168.1.9:8443", PresenceVerified, serveInst("serve", "qwen3-8b")))
		loc, band, err := f.ServingPeer("jetson", "")
		require.NoError(t, err)
		require.Equal(t, "jetson", loc.Node.Name)
		require.Equal(t, "qwen3-8b", band)
	})

	t.Run("node/instance addresses a specific instance", func(t *testing.T) {
		f := seedFleet(t, srvNode("ws", "host", "192.168.1.10:8443", PresenceVerified,
			serveInst("big", "qwen3-72b"), serveInst("small", "qwen3-8b")))
		loc, band, err := f.ServingPeer("ws/small", "")
		require.NoError(t, err)
		require.Equal(t, "small", loc.Instance.Name)
		require.Equal(t, "qwen3-8b", band)
	})

	t.Run("node + model pins the band across instances", func(t *testing.T) {
		f := seedFleet(t, srvNode("ws", "host", "192.168.1.10:8443", PresenceVerified,
			serveInst("big", "qwen3-72b"), serveInst("small", "qwen3-8b")))
		loc, band, err := f.ServingPeer("ws", "qwen3-72b")
		require.NoError(t, err)
		require.Equal(t, "big", loc.Instance.Name)
		require.Equal(t, "qwen3-72b", band)
	})

	t.Run("multiple bands, no model -> ErrMultipleModels", func(t *testing.T) {
		f := seedFleet(t, srvNode("ws", "host", "192.168.1.10:8443", PresenceVerified,
			serveInst("big", "qwen3-72b"), serveInst("small", "qwen3-8b")))
		_, _, err := f.ServingPeer("ws", "")
		require.ErrorIs(t, err, ErrMultipleModels)
	})

	t.Run("no such node -> ErrNoSuchNode", func(t *testing.T) {
		f := seedFleet(t, srvNode("jetson", "host", "192.168.1.9:8443", PresenceVerified, serveInst("serve", "qwen3-8b")))
		_, _, err := f.ServingPeer("nope", "")
		require.ErrorIs(t, err, ErrNoSuchNode)
	})

	t.Run("serves nothing -> ErrServesNoModel", func(t *testing.T) {
		f := seedFleet(t, srvNode("box", "host", "192.168.1.9:8443", PresenceVerified,
			Instance{Name: "i", Bands: nil, Caps: nil}))
		_, _, err := f.ServingPeer("box", "")
		require.ErrorIs(t, err, ErrServesNoModel)
	})

	t.Run("serve claimed not verified -> ErrServeUnverified", func(t *testing.T) {
		f := seedFleet(t, srvNode("pi", "host", "192.168.1.9:8443", PresenceVerified,
			Instance{Name: "i", Bands: []string{"qwen3-8b"}, Caps: []store.EdgeCap{{Name: string(Serve), State: string(Claimed)}}}))
		_, _, err := f.ServingPeer("pi", "")
		require.ErrorIs(t, err, ErrServeUnverified)
	})

	t.Run("dark node -> ErrNodeDark", func(t *testing.T) {
		f := seedFleet(t, srvNode("jetson", "host", "192.168.1.9:8443", PresenceDark, serveInst("serve", "qwen3-8b")))
		_, _, err := f.ServingPeer("jetson", "")
		require.ErrorIs(t, err, ErrNodeDark)
	})

	t.Run("no LAN transport -> ErrNoLANReach", func(t *testing.T) {
		n := srvNode("jetson", "host", "192.168.1.9:8443", PresenceVerified, serveInst("serve", "qwen3-8b"))
		n.Transports = nil // no LAN
		f := seedFleet(t, n)
		_, _, err := f.ServingPeer("jetson", "")
		require.ErrorIs(t, err, ErrNoLANReach)
	})

	t.Run("wrong model -> ErrModelNotServed", func(t *testing.T) {
		f := seedFleet(t, srvNode("jetson", "host", "192.168.1.9:8443", PresenceVerified, serveInst("serve", "qwen3-8b")))
		_, _, err := f.ServingPeer("jetson", "gpt-5")
		require.ErrorIs(t, err, ErrModelNotServed)
	})

	t.Run("no such instance -> ErrNoSuchInstance", func(t *testing.T) {
		f := seedFleet(t, srvNode("ws", "host", "192.168.1.10:8443", PresenceVerified, serveInst("big", "qwen3-72b")))
		_, _, err := f.ServingPeer("ws/ghost", "")
		require.ErrorIs(t, err, ErrNoSuchInstance)
	})
}
