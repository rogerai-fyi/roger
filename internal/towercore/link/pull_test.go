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

// Bound is Heartbeat's check without its write: a peer adopts the session, the shared record is
// not touched, and a stale or foreign session is refused.
func TestBoundChecksTheSessionWithoutWriting(t *testing.T) {
	m := NewMemMirror()
	a, b := New(mirrorCfg(m)), New(mirrorCfg(m))
	sess := openPull(t, a, "tw-1", "")
	before, _, _ := m.Get("tw-1")
	require.NoError(t, b.Bound(sess, "tw-1"), "a peer adopts it from the shared record")
	require.NoError(t, a.Bound(sess, "tw-1"))
	after, _, _ := m.Get("tw-1")
	require.Equal(t, before.LastSeen, after.LastSeen, "Bound wrote the shared record")
	require.ErrorIs(t, a.Bound("", "tw-1"), ErrNoSession)
	require.ErrorIs(t, a.Bound(sess, "tw-2"), ErrNoSession)
	require.ErrorIs(t, a.Bound("sess-unknown", "tw-1"), ErrNoSession)
	a.offset = a.cfg.Freshness * 2
	require.ErrorIs(t, a.Bound(sess, "tw-1"), ErrNoSession, "a session past its freshness cannot take work")
}

// Audit finding: an instance that missed the Tower's heartbeats (they went to the other instance)
// must not refuse its pulls while the shared record is fresh.
func TestBoundTrustsAFreshSharedRecordOverAStaleLocalCopy(t *testing.T) {
	m := NewMemMirror()
	a, b := New(mirrorCfg(m)), New(mirrorCfg(m))
	sess := openPull(t, a, "tw-1", "")
	require.NoError(t, b.Bound(sess, "tw-1")) // b adopts a copy
	b.offset = b.cfg.Freshness * 2            // b's copy is stale by b's clock...
	a.offset = b.offset
	require.NoError(t, a.Heartbeat(sess, "tw-1")) // ...while the Tower heartbeats a
	require.NoError(t, b.Bound(sess, "tw-1"), "a fresh shared record keeps the session bound")
	m.FailForTest(true)
	b.offset += b.cfg.Freshness * 2
	require.ErrorIs(t, b.Bound(sess, "tw-1"), ErrNoSession, "no shared record to vouch: refused")
	m.FailForTest(false)
	c := New(mirrorCfg(nil))
	s2 := openPull(t, c, "tw-2", "")
	c.offset = c.cfg.Freshness * 2
	require.ErrorIs(t, c.Bound(s2, "tw-2"), ErrNoSession)
}

// Review finding: during a rolling deploy an older instance writes the shared record without the
// pull, hub class and network fields, leaving the PREVIOUS session's values in them. Those fields
// carry the session they belong to, and fields left from another session read as unmeasured, so
// the sweep measures the hub again rather than trusting a stale pull flag or home network.
func TestFieldsLeftByAnotherSessionReadAsUnmeasured(t *testing.T) {
	m := pgMirror(t)
	a := New(mirrorCfg(m))
	sess := openPull(t, a, "tw-1", "192.168.1.20:8444")
	loc := placement.Locality{Bucket: "198.51.100.0/24", Continent: "NA", Home: "tag-home"}
	require.True(t, a.SetLinkLocality("tw-1", sess, loc))
	require.True(t, a.SetLocality("tw-1", sess, loc))
	require.True(t, a.SetHubClass("tw-1", sess, placement.HubPrivate))
	// An older instance opens the next session and writes only the columns it knows.
	_, err := m.db.Exec(`INSERT INTO rogerai.tower_link_mirror (tower_id, session_id, version, last_seen, endpoint,
            tls_spki, loc_bucket, loc_continent, loc_known, loc_link) VALUES ('tw-1','sess-old',1,now(),'10.0.0.9:8444','','','',false,'')
        ON CONFLICT (tower_id) DO UPDATE SET session_id = EXCLUDED.session_id, version = EXCLUDED.version,
            last_seen = EXCLUDED.last_seen, endpoint = EXCLUDED.endpoint, tls_spki = EXCLUDED.tls_spki,
            loc_bucket = EXCLUDED.loc_bucket, loc_continent = EXCLUDED.loc_continent,
            loc_known = EXCLUDED.loc_known, loc_link = EXCLUDED.loc_link`)
	require.NoError(t, err)
	for _, rec := range []Record{mustGet(t, m, "tw-1"), mustAll(t, m)["tw-1"]} {
		require.Equal(t, "sess-old", rec.SessionID)
		require.False(t, rec.Pull, "a pull flag from another session was trusted")
		require.Equal(t, placement.HubNone, rec.HubClass, "a hub class from another session was trusted")
		require.False(t, rec.LinkKnown)
		require.Empty(t, rec.Locality.Home, "a home network from another session was trusted")
		require.Equal(t, placement.Locality{}, rec.Link)
	}
}

// A Tower that holds a pull is a Tower that pulls, whatever an older instance recorded: the
// session takes the capability once, and every instance reads it.
func TestAPullingTowerIsMarkedAsPulling(t *testing.T) {
	m := NewMemMirror()
	a, b := New(mirrorCfg(m)), New(mirrorCfg(m))
	sess := openPull(t, a, "tw-1", "")
	require.True(t, a.SetPull("tw-1", sess, false)) // as an older instance would have left it
	require.False(t, b.Pulls("tw-1"))
	require.True(t, a.SetPull("tw-1", sess, true))
	require.True(t, b.Pulls("tw-1"), "the capability did not reach the other instance")
	require.False(t, a.SetPull("tw-1", "sess-other", true))
}
