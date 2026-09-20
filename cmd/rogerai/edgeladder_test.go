package main

// Unit spec for the production dispatch-ladder wiring (edgeladder.go) and the peer-serving
// side (edgeinstance.go): the peer list is built from the fleet record, only VERIFIED
// LAN-direct serving instances on live nodes, never this machine itself; the caller
// certificate and the receipt check are real; and applyEdgeLadder leaves an unenrolled
// machine with no preference untouched.

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
)

func enrollThisMachine(t *testing.T, name string) string {
	t.Helper()
	runEdgeCLI(t, "edge", "authority", "local", name)
	runEdgeCLI(t, "edge", "enroll", name)
	return nodeIDOf(t)
}

// peerNode adds a peer to this machine's fleet cache: a serving instance, LAN-direct, with
// serve at the given state and the node at the given presence.
func peerNode(t *testing.T, name, band, capState, presence, addr, pin string) {
	t.Helper()
	st, err := loadEdgeState()
	require.NoError(t, err)
	n := store.EdgeNode{ID: "n_" + name + strings.Repeat("0", 24), Account: st.fleet.Account(), Name: name, Kind: "host",
		Presence: presence, LastSeen: time.Now().Unix(),
		Transports: []store.EdgeTransport{{Kind: "lan", Addr: addr, Fingerprint: pin}},
		Instances: []store.EdgeInstance{{Name: "serve", Bands: []string{band},
			Caps: []store.EdgeCap{{Name: "serve", State: capState}}}}}
	_, err = st.db.EnrollEdgeNode(n)
	require.NoError(t, err)
	require.NoError(t, st.save())
}

func TestEdgePeersForPicksOnlyServableLANPeers(t *testing.T) {
	useTempConfig(t)
	t.Setenv("ROGER_BROKER", "http://127.0.0.1:1")
	enrollThisMachine(t, "workshop")

	// A verified LAN-direct serving peer is a rung.
	peerNode(t, "jetson", "qwen-3.8-27b", "VERIFIED", string(edge.PresenceVerified), "192.168.1.20:7", strings.Repeat("ab", 32))
	// A claimed serve is not.
	peerNode(t, "benchx", "qwen-3.8-27b", "CLAIMED", string(edge.PresenceVerified), "192.168.1.21:7", strings.Repeat("cd", 32))
	// A dark node is not.
	peerNode(t, "atticx", "qwen-3.8-27b", "VERIFIED", string(edge.PresenceDark), "192.168.1.22:7", strings.Repeat("ef", 32))

	peers := edgePeersFor("qwen-3.8-27b")
	require.Len(t, peers, 1, "only the verified, live, LAN-direct peer is a rung: %+v", peers)
	require.Equal(t, "jetson", peers[0].Node)
	require.Equal(t, "serve", peers[0].Instance)
	require.Equal(t, "192.168.1.20:7", peers[0].Addr)
	require.Equal(t, strings.Repeat("ab", 32), peers[0].Pin)

	// A band nobody serves has no peers: the ladder falls through to the market.
	require.Empty(t, edgePeersFor("gpt-oss-120b"))
}

func TestEdgeCallerCertAndVerify(t *testing.T) {
	useTempConfig(t)
	t.Setenv("ROGER_BROKER", "http://127.0.0.1:1")
	require.Nil(t, edgeCallerCert(), "a machine that has not enrolled presents no certificate")
	enrollThisMachine(t, "workshop")
	crt := edgeCallerCert()
	require.NotNil(t, crt)
	require.NotEmpty(t, crt.Certificate)

	var po client.ProxyOptions
	applyEdgeLadder(config{EdgePrefer: "local"}, &po)
	require.Equal(t, "local", po.Prefer)
	require.NotNil(t, po.EdgePeers)
	require.NotNil(t, po.EdgeCert)
	require.NotNil(t, po.EdgeVerify)
	// EdgeVerify rejects a receipt that is not local, and one with no serving cert.
	require.False(t, po.EdgeVerify(protocol.UsageReceipt{}, nil))
}

func TestApplyEdgeLadderLeavesAnUnenrolledMachineAlone(t *testing.T) {
	useTempConfig(t)
	t.Setenv("ROGER_BROKER", "http://127.0.0.1:1")
	var po client.ProxyOptions
	applyEdgeLadder(config{}, &po) // not enrolled, no preference
	require.Nil(t, po.EdgePeers, "an unenrolled machine with no preference gets no ladder")
	require.Empty(t, po.Prefer)
}

func TestPeerServingIsWiredWhenEnrolled(t *testing.T) {
	useTempConfig(t)
	t.Setenv("ROGER_BROKER", "http://127.0.0.1:1")
	nodeID := enrollThisMachine(t, "workshop")
	h, err := newEdgeHost("workshop")
	require.NoError(t, err)

	id, key, ok, err := edgeIdentityStore().LoadIdentity()
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, nodeID, id.NodeID)

	edgeAttachUpstream(func(band string) (string, string, bool) {
		if band == "qwen-3.8-27b" {
			return "http://127.0.0.1:9/v1/chat/completions", "", true
		}
		return "", "", false
	})
	t.Cleanup(func() { edgeUpstreamOf = nil })

	sv, ok := h.peerServing(key, id.NodeID)
	require.True(t, ok, "an enrolled machine can serve peers")
	require.NotNil(t, sv.Trust)
	require.Equal(t, id.NodeID, sv.NodeID)
	require.True(t, sv.Member(id.NodeID), "this node is a member of its own Edge")
	require.False(t, sv.Member("n_stranger00000000000000"), "a stranger is not")
	u, _, inst, ok := sv.Upstream("qwen-3.8-27b")
	require.True(t, ok)
	require.Contains(t, u, "127.0.0.1:9")
	_ = inst
	_, _, _, ok = sv.Upstream("gpt-oss-120b")
	require.False(t, ok, "a band nothing here serves has no upstream")
}
