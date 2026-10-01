package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
	"rogerai.fm/roger/v6/internal/towercore/cert"
)

// features/edge/fleet_window.feature §3: ask a unit to report now, over its face, bounded - and
// recorded only after the same certificate verdict a discovery pass reaches (review 2026-10-01: a
// pin match alone redrew an expired or foreign certificate as ON AIR).
func TestAskingAUnitToReportNow(t *testing.T) {
	useTempConfig(t)
	edgeWriteAuth(t, "owner")
	auth, err := cert.NewAuthority(cert.Config{TTL: time.Hour})
	require.NoError(t, err)
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	id := edge.NodeID(pub)
	leaf, err := auth.Issue(id, pub)
	require.NoError(t, err)
	pin := edge.FingerprintOf(leaf)

	old := time.Now().Add(-10 * time.Minute)
	edgeSeed(t,
		store.EdgeNode{ID: id, Name: "nas", Kind: "host", Presence: string(edge.PresenceVerified), LastSeen: old.Unix(), Pin: pin,
			Transports: []store.EdgeTransport{{Kind: "lan", Addr: "127.0.0.1:44190", Fingerprint: pin}}},
		store.EdgeNode{ID: "n_nina", Name: "nina", Kind: "mobile", Presence: string(edge.PresenceVerified), LastSeen: old.Unix()},
		store.EdgeNode{ID: "n_nopin", Name: "nopin", Kind: "host", Presence: string(edge.PresenceVerified), LastSeen: old.Unix(),
			Transports: []store.EdgeTransport{{Kind: "lan", Addr: "127.0.0.1:44191", Fingerprint: "ab"}}},
	)
	prevDial, prevTrust := edgeDialPeer, edgeAskTrust
	t.Cleanup(func() {
		edgeDialPeer, edgeAskTrust = prevDial, prevTrust
		edgeAsked.Lock()
		edgeAsked.at, edgeAsked.says = map[string]time.Time{}, map[string]string{}
		edgeAsked.Unlock()
	})
	trust := auth
	edgeAskTrust = func(time.Time) (*cert.Authority, error) { return trust, nil }
	dials := 0
	answer := leaf
	var fail error
	edgeDialPeer = func(ctx context.Context, addr string) (*edge.Peer, error) {
		dials++
		if fail != nil {
			return nil, fail
		}
		return &edge.Peer{Cert: answer, Fingerprint: edge.FingerprintOf(answer), Describe: edge.Describe{NodeID: id, Kind: "host",
			Instances: []edge.Instance{{Name: "nas", Bands: []string{"qwen3-4b"}}}}}, nil
	}
	clock := time.Now()
	ask := func(who string) (string, error) {
		clock = clock.Add(edgeAskFloor + time.Second) // past the floor each time unless a test says otherwise
		return edgeAskNow(who, clock)
	}
	recorded := func() int64 { _, n, _, _ := edgeLookup(id); return n.LastSeen }

	_, err = ask("nina")
	require.ErrorContains(t, err, "can only be heard, not asked")
	_, err = ask("nopin")
	require.Error(t, err, "a unit whose certificate cannot be checked against a pin is never recorded by an ask")
	_, err = ask("nowhere")
	require.ErrorContains(t, err, "no such node")

	fail = errors.New("connection refused")
	_, err = ask("nas")
	require.ErrorContains(t, err, "did not answer within 3s")
	fail = nil

	// A certificate from another authority, with the right pin shape for its own key: refused.
	other, err := cert.NewAuthority(cert.Config{TTL: time.Hour})
	require.NoError(t, err)
	foreign, err := other.Issue(id, pub)
	require.NoError(t, err)
	answer = foreign
	_, err = ask("nas")
	require.ErrorContains(t, err, "did not check out")
	// A certificate this authority issued for a DIFFERENT node: refused.
	pub2, _, _ := ed25519.GenerateKey(rand.Reader)
	otherNode, err := auth.Issue(edge.NodeID(pub2), pub2)
	require.NoError(t, err)
	answer = otherNode
	_, err = ask("nas")
	require.ErrorContains(t, err, "did not check out")
	// Its own certificate, revoked: refused.
	answer = leaf
	require.NoError(t, auth.Revoke(leaf.SerialNumber))
	_, err = ask("nas")
	require.ErrorContains(t, err, "did not check out")
	require.Equal(t, old.Unix(), recorded(), "nothing was recorded from a certificate that did not check out")

	// A fresh certificate for the same key from this authority: answered and recorded.
	fresh, err := cert.NewAuthority(cert.Config{TTL: time.Hour})
	require.NoError(t, err)
	trust = fresh
	renewed, err := fresh.Issue(id, pub)
	require.NoError(t, err)
	answer = renewed
	setPin(t, id, edge.FingerprintOf(renewed))
	says, err := ask("nas")
	require.NoError(t, err)
	require.Contains(t, says, "nas answered in")
	require.Greater(t, recorded(), old.Unix())
	_, n, _, _ := edgeLookup(id)
	require.Equal(t, []string{"qwen3-4b"}, n.Instances[0].Bands)

	// Inside the floor: answered from the last attempt without dialling - answered or not.
	before := dials
	says, err = edgeAskNow("nas", clock.Add(2*time.Second))
	require.NoError(t, err)
	require.Contains(t, says, "asked 2s ago - nas answered in")
	fail = errors.New("down")
	_, _ = ask("nas")
	says, _ = edgeAskNow("nas", clock.Add(time.Second))
	require.Contains(t, says, "it did not answer", "a failed ask is remembered too, so a dead unit is not dialled again and again")
	require.Equal(t, before+1, dials)
}

func setPin(t *testing.T, id, pin string) {
	t.Helper()
	st, err := loadEdgeState()
	require.NoError(t, err)
	nodes, err := st.list()
	require.NoError(t, err)
	for _, n := range nodes {
		if n.ID == id {
			n.Pin = pin
			n.Transports[0].Fingerprint = pin
			require.NoError(t, st.db.UpdateEdgeNode(n))
		}
	}
	require.NoError(t, st.save())
}
