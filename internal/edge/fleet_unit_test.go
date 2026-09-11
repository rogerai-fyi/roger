package edge_test

// The error paths of the fleet: the refusals the scenarios rely on but do not each walk
// individually, plus Observe and Sweep, which discovery drives.

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

func fleetFixture(t *testing.T) (*edge.Fleet, store.Store, ed25519.PublicKey) {
	t.Helper()
	db := store.NewMem()
	f := edge.NewFleet(db, "acct-1")
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return f, db, pub
}

func TestEnrollRefusals(t *testing.T) {
	f, _, pub := fleetFixture(t)
	require.Equal(t, "acct-1", f.Account())

	_, err := f.Enroll(edge.NewNode(pub, "", edge.Host))
	require.Error(t, err, "an unnamed node")

	_, err = f.Enroll(edge.NewNode(pub, "box", edge.Kind("toaster")))
	require.Error(t, err, "a kind that is not a kind")

	_, err = f.Enroll(edge.NewNode(pub, "box", edge.Board, edge.Serve))
	require.Error(t, err, "a board cannot serve a model")

	_, err = f.Enroll(edge.NewNode(pub, "box", edge.Host, edge.Capability("root")))
	require.Error(t, err, "a capability that is not in the vocabulary")

	n, err := f.Enroll(edge.NewNode(pub, "box", edge.Host))
	require.NoError(t, err)
	require.Equal(t, string(edge.PresenceVerified), n.Presence)
	require.NotZero(t, n.LastSeen)
	require.Len(t, n.History, 1)
}

func TestRenameForgetAndGetRefusals(t *testing.T) {
	f, _, pub := fleetFixture(t)
	n, err := f.Enroll(edge.NewNode(pub, "box", edge.Host))
	require.NoError(t, err)

	require.Error(t, f.Rename(n.ID, "bad name"))
	require.ErrorIs(t, f.Rename("n_ghost", "fine"), edge.ErrNoSuchNode)
	require.ErrorIs(t, f.Forget("n_ghost"), edge.ErrNoSuchNode)
	require.ErrorIs(t, f.Declare("n_ghost", nil), edge.ErrNoSuchNode)
	require.ErrorIs(t, f.RecordEvent("n_ghost", "x", ""), edge.ErrNoSuchNode)
	require.ErrorIs(t, f.RecordProbe("n_ghost", edge.Serve, true), edge.ErrNoSuchNode)
	require.ErrorIs(t, f.ConfirmActuate("n_ghost", "me"), edge.ErrNoSuchNode)

	_, err = f.VerificationMethod("n_ghost", edge.Serve)
	require.ErrorIs(t, err, edge.ErrNoSuchNode)
	_, err = f.VerificationMethod(n.ID, edge.Serve)
	require.Error(t, err, "the node does not declare serve")

	_, ok, err := f.ByName("nobody")
	require.NoError(t, err)
	require.False(t, ok)
	_, ok, err = f.Get("n_ghost")
	require.NoError(t, err)
	require.False(t, ok)
}

func TestProbeAndConfirmRefusals(t *testing.T) {
	f, _, pub := fleetFixture(t)
	n, err := f.Enroll(edge.NewNode(pub, "box", edge.Host, edge.Sense))
	require.NoError(t, err)

	require.Error(t, f.RecordProbe(n.ID, edge.Actuate, true),
		"actuate is never probed; that is the whole rule")
	require.Error(t, f.RecordProbe(n.ID, edge.Serve, true), "serve is not declared")
	require.NoError(t, f.RecordProbe(n.ID, edge.Sense, true))
	got, _, _ := f.Get(n.ID)
	require.True(t, edge.Routable(got, edge.Sense))
	require.NoError(t, f.RecordProbe(n.ID, edge.Sense, false))
	got, _, _ = f.Get(n.ID)
	require.False(t, edge.Routable(got, edge.Sense), "a failed probe demotes the claim")

	require.Error(t, f.ConfirmActuate(n.ID, ""), "a confirmation records who gave it")
	require.Error(t, f.ConfirmActuate(n.ID, "me"), "the node does not declare actuate")
	require.Error(t, f.Declare(n.ID, []edge.Capability{edge.Capability("root")}))
}

func TestAuthorizeInvokeRefusals(t *testing.T) {
	f, db, pub := fleetFixture(t)
	n, err := f.Enroll(edge.NewNode(pub, "box", edge.Host, edge.Sense))
	require.NoError(t, err)
	require.NoError(t, f.RecordProbe(n.ID, edge.Sense, true))

	ok := store.Grant{ID: "g", Owner: "acct-1", Nodes: []string{n.ID}}
	require.NoError(t, f.AuthorizeInvoke(ok, n.ID, edge.Sense))
	require.NoError(t, f.AuthorizeInvoke(store.Grant{ID: "g", Owner: "acct-1"}, n.ID, ""),
		"an unscoped grant addresses all of this owner's nodes")

	require.Error(t, f.AuthorizeInvoke(ok, "n_ghost", ""))
	require.Error(t, f.AuthorizeInvoke(store.Grant{ID: "g", Owner: "acct-2", Nodes: []string{n.ID}}, n.ID, ""))
	revoked := ok
	revoked.Revoked = true
	require.Error(t, f.AuthorizeInvoke(revoked, n.ID, ""))
	expired := ok
	expired.ExpiresAt = time.Now().Add(-time.Hour).Unix()
	require.Error(t, f.AuthorizeInvoke(expired, n.ID, ""))
	elsewhere := ok
	elsewhere.Nodes = []string{"n_other"}
	require.Error(t, f.AuthorizeInvoke(elsewhere, n.ID, ""))
	require.ErrorIs(t, f.AuthorizeInvoke(ok, n.ID, edge.Serve), edge.ErrNotVerified)
	_ = db
}

func TestObserveCreatesThenUpdatesInPlace(t *testing.T) {
	f, db, pub := fleetFixture(t)
	clock := time.Unix(1_700_000_000, 0)
	f.SetClock(func() time.Time { return clock })
	id := edge.NodeID(pub)

	n, err := f.Observe(id, edge.Observation{
		Name: id, Caps: []edge.Capability{edge.Sense}, Addr: "192.168.1.5:9443", Fingerprint: "aa"})
	require.NoError(t, err)
	require.Equal(t, string(edge.Host), n.Kind, "a node that says nothing is a host")
	require.Equal(t, "aa", n.Pin)
	require.Equal(t, "192.168.1.5:9443", edge.LANAddr(n))

	// A second sighting at a NEW address updates the same row: no duplicate, and a
	// state already EARNED for a capability it still declares is carried across.
	require.NoError(t, f.RecordProbe(id, edge.Sense, true))
	clock = clock.Add(time.Minute)
	n, err = f.Observe(id, edge.Observation{
		Kind: "host", Caps: []edge.Capability{edge.Sense}, Addr: "192.168.1.6:9443", Fingerprint: "bb"})
	require.NoError(t, err)
	require.Equal(t, "192.168.1.6:9443", edge.LANAddr(n))
	require.Equal(t, "aa", n.Pin, "the pin is the certificate the owner accepted, not the latest one")
	require.True(t, edge.Routable(n, edge.Sense), "a passed probe survives the next sighting")
	rows, err := db.EdgeNodesOfAccount("acct-1")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Empty(t, edge.LANAddr(store.EdgeNode{}))
}

func TestSweepDarkensAndKeeps(t *testing.T) {
	f, _, pub := fleetFixture(t)
	clock := time.Unix(1_700_000_000, 0)
	f.SetClock(func() time.Time { return clock })
	n, err := f.Enroll(edge.NewNode(pub, "box", edge.Host))
	require.NoError(t, err)

	darkened, err := f.Sweep(5 * time.Minute)
	require.NoError(t, err)
	require.Zero(t, darkened, "a node seen just now is not dark")

	clock = clock.Add(10 * time.Minute)
	darkened, err = f.Sweep(5 * time.Minute)
	require.NoError(t, err)
	require.Equal(t, 1, darkened)
	got, ok, err := f.Get(n.ID)
	require.NoError(t, err)
	require.True(t, ok, "a dark node is KEPT")
	require.Equal(t, string(edge.PresenceDark), got.Presence)
	require.Equal(t, n.LastSeen, got.LastSeen, "the last-seen time is preserved")

	// Sweeping again does not re-darken (and does not append a second history line).
	before := len(got.History)
	darkened, err = f.Sweep(5 * time.Minute)
	require.NoError(t, err)
	require.Zero(t, darkened)
	got, _, _ = f.Get(n.ID)
	require.Len(t, got.History, before)
}

func TestEnrollErrorSaysNothingAboutTheOtherAccount(t *testing.T) {
	db := store.NewMem()
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	a := edge.NewFleet(db, "acct-1")
	b := edge.NewFleet(db, "acct-2")
	_, err := a.Enroll(edge.NewNode(pub, "mine", edge.Host))
	require.NoError(t, err)
	_, err = b.Enroll(edge.NewNode(pub, "poached", edge.Host))
	var ee *edge.EnrollError
	require.True(t, errors.As(err, &ee))
	require.Equal(t, "acct-1", ee.Existing)
	require.NotContains(t, ee.Error(), "acct-1")
}
