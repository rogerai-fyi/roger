package link

// The pull capability, the hub class and the link's own locality ride the live session and the
// shared mirror, so every broker instance answers the same. Contracts:
// features/tower/tower_pull_dispatch.feature ("A Tower that can pull says so when it opens its
// session"), features/tower/private_hub_placement.feature ("A private-hub Tower's location comes
// from its link's observed address").

import (
	"testing"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v7/internal/towercore/placement"
)

func openPull(t *testing.T, s *Sessions, towerID, endpoint string) string {
	t.Helper()
	acc, err := s.Open(Hello{Network: PublicNetwork, Versions: []int{1}, TowerID: towerID,
		Capabilities: []string{CapIntegrity, CapInnerSession, CapPull}, RelayEndpoint: endpoint}, towerID)
	require.NoError(t, err)
	return acc.SessionID
}

func TestATowerThatCanPullSaysSoAndEveryInstanceReadsIt(t *testing.T) {
	m := NewMemMirror()
	a, b := New(mirrorCfg(m)), New(mirrorCfg(m))
	sess := openPull(t, a, "tw-1", "")
	require.True(t, a.Pulls("tw-1"))
	require.True(t, b.Pulls("tw-1"), "a peer reads it from the mirror")

	// An instance that adopts the session keeps it through its own heartbeats.
	require.NoError(t, b.Heartbeat(sess, "tw-1"))
	m.FailForTest(true)
	require.True(t, b.Pulls("tw-1"), "the adopted session holds it locally")
	m.FailForTest(false)

	openOn(t, a, "tw-1", "")
	require.False(t, a.Pulls("tw-1"), "a new session without the capability does not pull")
	require.False(t, New(mirrorCfg(m)).Pulls("tw-1"), "nor does a peer reading the new session")
	require.False(t, a.Pulls("tw-unknown"))
	require.False(t, New(mirrorCfg(nil)).Pulls("tw-1"))
}

func TestAPullCapabilityIsNotMandatory(t *testing.T) {
	a := New(mirrorCfg(nil))
	openOn(t, a, "tw-1", "") // an older Tower without it still opens
	require.False(t, a.Pulls("tw-1"))
	sess := openPull(t, a, "tw-2", "")
	require.NotEmpty(t, sess)
	require.True(t, a.Pulls("tw-2"))
}

func TestAStaleMirrorRecordDoesNotPull(t *testing.T) {
	m := NewMemMirror()
	a, b := New(mirrorCfg(m)), New(mirrorCfg(m))
	openPull(t, a, "tw-1", "")
	b.offset = a.cfg.Freshness * 2 // b's clock is past the window
	require.False(t, b.Pulls("tw-1"))
}

func TestTheHubClassRidesTheSession(t *testing.T) {
	m := NewMemMirror()
	a, b := New(mirrorCfg(m)), New(mirrorCfg(m))
	sess := openPull(t, a, "tw-1", "192.168.1.20:8444")
	require.Equal(t, placement.HubNone, a.HubClass("tw-1"), "unmeasured")
	require.True(t, a.SetHubClass("tw-1", sess, placement.HubPrivate))
	require.Equal(t, placement.HubPrivate, a.HubClass("tw-1"))
	require.Equal(t, placement.HubPrivate, b.HubClass("tw-1"))
	require.NoError(t, b.Heartbeat(sess, "tw-1"))
	m.FailForTest(true)
	require.Equal(t, placement.HubPrivate, b.HubClass("tw-1"), "adopted")
	m.FailForTest(false)
	openPull(t, a, "tw-1", "192.168.1.20:8444")
	require.Equal(t, placement.HubNone, a.HubClass("tw-1"), "a new session starts unmeasured")
	require.False(t, a.SetHubClass("tw-1", sess, placement.HubPrivate), "not for a replaced session")
	require.Equal(t, placement.HubNone, New(mirrorCfg(nil)).HubClass("tw-x"))
}

func TestTheLinkLocalityIsKeptWholeWithTheSession(t *testing.T) {
	m := NewMemMirror()
	a, b := New(mirrorCfg(m)), New(mirrorCfg(m))
	sess := openPull(t, a, "tw-1", "")
	_, known := a.LinkLocality("tw-1")
	require.False(t, known)
	loc := placement.Locality{Bucket: "198.51.100.0/24", Continent: "NA", Home: "198.51.100.40"}
	require.True(t, a.SetLinkLocality("tw-1", sess, loc))
	got, known := a.LinkLocality("tw-1")
	require.True(t, known)
	require.Equal(t, loc, got)
	got, known = b.LinkLocality("tw-1")
	require.True(t, known, "a peer reads it from the mirror")
	require.Equal(t, loc, got)
	require.Equal(t, "NA", b.LinkContinent("tw-1"))
	require.NoError(t, b.Heartbeat(sess, "tw-1"))
	m.FailForTest(true)
	got, known = b.LinkLocality("tw-1")
	m.FailForTest(false)
	require.True(t, known, "adopted")
	require.Equal(t, loc, got)
	require.False(t, a.SetLinkLocality("tw-x", sess, loc))
	_, known = New(mirrorCfg(nil)).LinkLocality("tw-x")
	require.False(t, known)
}

func TestTheHomeKeyOfAMeasuredLocalityReachesPeers(t *testing.T) {
	m := NewMemMirror()
	a, b := New(mirrorCfg(m)), New(mirrorCfg(m))
	sess := openPull(t, a, "tw-1", "")
	loc := placement.Locality{Bucket: "198.51.100.0/24", Continent: "NA", Home: "198.51.100.40"}
	require.True(t, a.SetLocality("tw-1", sess, loc))
	got, ok := b.Locality("tw-1")
	require.True(t, ok)
	require.Equal(t, loc, got)
}

func TestPGMirrorRoundTripsPullHubClassAndHome(t *testing.T) {
	m := pgMirror(t)
	a := New(mirrorCfg(m))
	sess := openPull(t, a, "tw-1", "192.168.1.20:8444")
	loc := placement.Locality{Bucket: "198.51.100.0/24", Continent: "NA", Home: "198.51.100.40"}
	require.True(t, a.SetLinkLocality("tw-1", sess, loc))
	require.True(t, a.SetLocality("tw-1", sess, loc))
	require.True(t, a.SetHubClass("tw-1", sess, placement.HubPrivate))
	for _, rec := range []Record{mustGet(t, m, "tw-1"), mustAll(t, m)["tw-1"]} {
		require.True(t, rec.Pull)
		require.Equal(t, placement.HubPrivate, rec.HubClass)
		require.Equal(t, loc, rec.Locality)
		require.Equal(t, loc, rec.Link)
		require.True(t, rec.LinkKnown)
	}
}

func mustGet(t *testing.T, m Mirror, id string) Record {
	t.Helper()
	rec, ok, err := m.Get(id)
	require.NoError(t, err)
	require.True(t, ok)
	return rec
}

func mustAll(t *testing.T, m Mirror) map[string]Record {
	t.Helper()
	all, err := m.All()
	require.NoError(t, err)
	return all
}
