package edge_test

// The half of features/edge/enrollment.feature that lands inside internal/edge: what a
// renewal does to a node's row, what an authority migration does to the fleet, and the
// two verification rules enrollment added - a revoked certificate is refused AS REVOKED,
// and the identity a peer proves is its KEY, not the paper it is written on.
//
// Real certificates from the real towercore/cert authority throughout.

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
	"rogerai.fm/roger/v6/internal/towercore/cert"
)

func enrollFixture(t *testing.T) (*edge.Fleet, *cert.Authority, ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	db := store.NewMem()
	f := edge.NewFleet(db, "acct-1")
	a, err := cert.NewAuthority(cert.Config{TTL: time.Hour})
	require.NoError(t, err)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return f, a, pub, priv
}

func TestRepinFollowsARenewalAndLeavesEverythingElseAlone(t *testing.T) {
	f, _, pub, _ := enrollFixture(t)
	n := edge.NewNode(pub, "workshop", edge.Host, edge.Sense)
	n.Pin = "aa"
	got, err := f.Enroll(n)
	require.NoError(t, err)
	before := len(got.History)

	require.NoError(t, f.RepinNode(got.ID, "aa"), "repinning to the same certificate is a no-op")
	same, _, err := f.Get(got.ID)
	require.NoError(t, err)
	require.Len(t, same.History, before, "a no-op writes no history")

	require.NoError(t, f.RepinNode(got.ID, "bb"))
	after, _, err := f.Get(got.ID)
	require.NoError(t, err)
	require.Equal(t, "bb", after.Pin)
	require.Equal(t, got.ID, after.ID, "a renewal is not a new node")
	require.Equal(t, "workshop", after.Name)
	require.Len(t, after.History, before+1)
	require.Equal(t, "renewed", after.History[len(after.History)-1].What)

	require.ErrorIs(t, f.RepinNode("n_nobody", "cc"), edge.ErrNoSuchNode)
}

func TestChangingAuthorityMarksEveryMemberAndDropsNone(t *testing.T) {
	f, _, pub, _ := enrollFixture(t)
	marked, err := f.RequireReenrollment("the Edge authority moved")
	require.NoError(t, err)
	require.Zero(t, marked, "an empty Edge has nobody to tell")

	first, err := f.Enroll(edge.NewNode(pub, "workshop", edge.Host))
	require.NoError(t, err)
	otherPub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	second, err := f.Enroll(edge.NewNode(otherPub, "bench", edge.Host))
	require.NoError(t, err)

	marked, err = f.RequireReenrollment("the Edge authority moved from Core to the shed")
	require.NoError(t, err)
	require.Equal(t, 2, marked)

	list, err := f.List()
	require.NoError(t, err)
	require.Len(t, list, 2, "members are not silently dropped")
	for _, n := range list {
		require.Equal(t, string(edge.PresenceReenroll), n.Presence)
		last := n.History[len(n.History)-1]
		require.Equal(t, "re-enrollment required", last.What)
		require.Contains(t, last.Detail, "moved from Core to the shed", "with the reason")
	}
	require.NotEqual(t, first.ID, second.ID)
}

func TestVerifyPeerRefusesRevocationAsRevocationAndBindsIdentityToTheKey(t *testing.T) {
	_, a, pub, _ := enrollFixture(t)
	id := edge.NodeID(pub)
	leaf, err := a.Issue(id, pub)
	require.NoError(t, err)
	now := time.Now()
	ad := edge.Advert{NodeID: id, Fingerprint: edge.FingerprintOf(leaf)}
	peer := &edge.Peer{Cert: leaf, Fingerprint: ad.Fingerprint, Describe: edge.Describe{NodeID: id}}

	require.Empty(t, edge.VerifyPeer(ad, peer, a, ad.Fingerprint, now))
	require.Equal(t, edge.ReasonUnknownAuthority, edge.VerifyPeer(ad, peer, nil, ad.Fingerprint, now),
		"a machine that holds no Edge root verifies nobody, rather than everybody")

	t.Run("a renewed certificate still verifies against the pin a peer holds", func(t *testing.T) {
		// A NEW certificate for the SAME key: new paper, same identity. Certificates are
		// short-lived, so a fleet that broke on every reissue would break on schedule.
		renewed, err := a.Issue(id, pub)
		require.NoError(t, err)
		require.NotEqual(t, edge.FingerprintOf(leaf), edge.FingerprintOf(renewed))
		fresh := edge.Advert{NodeID: id, Fingerprint: edge.FingerprintOf(renewed)}
		require.Empty(t, edge.VerifyPeer(fresh,
			&edge.Peer{Cert: renewed, Fingerprint: fresh.Fingerprint}, a,
			edge.FingerprintOf(leaf), now))
	})

	t.Run("a certificate this authority signed for the wrong KEY is a mismatch", func(t *testing.T) {
		// It chains, it names the right node, and it is bound to somebody else's key:
		// the one failure that is correct all the way down and still wrong.
		impostorPub, _, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		misissued, err := a.Issue(id, impostorPub)
		require.NoError(t, err)
		bad := edge.Advert{NodeID: id, Fingerprint: edge.FingerprintOf(misissued)}
		require.Equal(t, edge.ReasonIdentityMismatch, edge.VerifyPeer(bad,
			&edge.Peer{Cert: misissued, Fingerprint: bad.Fingerprint}, a, "", now))
	})

	t.Run("a key no node id can be derived from falls back to the pin", func(t *testing.T) {
		other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		require.NoError(t, err)
		odd, err := a.Issue(id, other.Public())
		require.NoError(t, err)
		oddAd := edge.Advert{NodeID: id, Fingerprint: edge.FingerprintOf(odd)}
		oddPeer := &edge.Peer{Cert: odd, Fingerprint: oddAd.Fingerprint}
		require.Empty(t, edge.VerifyPeer(oddAd, oddPeer, a, oddAd.Fingerprint, now))
		require.Equal(t, edge.ReasonIdentityMismatch,
			edge.VerifyPeer(oddAd, oddPeer, a, "some-other-fingerprint", now))
		require.Empty(t, edge.VerifyPeer(oddAd, oddPeer, a, "", now))
	})

	t.Run("revocation is its own word, not expiry and not a chain problem", func(t *testing.T) {
		require.NoError(t, a.Revoke(leaf.SerialNumber))
		require.Equal(t, edge.ReasonRevoked, edge.VerifyPeer(ad, peer, a, ad.Fingerprint, now))

		// And it is the FIRST thing said: a revoked certificate that is also past its
		// window is still reported as revoked... unless it really is only expired.
		expired := edge.VerifyPeer(ad, peer, a, ad.Fingerprint, leaf.NotAfter.Add(time.Hour))
		require.Equal(t, edge.ReasonCertExpired, expired,
			"expiry is checked before the authority, so a lapsed credential says so")
	})
}
