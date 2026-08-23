package link

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
)

// The mirror guards the BDD suite cannot isolate: it only ever drives honest towers, so
// deleting the session-id check or the freshness check on adoption left it green. These
// drive the seams directly.

func mirrorCfg(m Mirror) Config {
	return Config{Network: PublicNetwork, Versions: []int{1},
		Heartbeat: 50 * time.Millisecond, Freshness: 400 * time.Millisecond, Mirror: m}
}

func openOn(t *testing.T, s *Sessions, towerID, endpoint string) string {
	t.Helper()
	acc, err := s.Open(Hello{Network: PublicNetwork, Versions: []int{1}, TowerID: towerID,
		Capabilities: []string{CapIntegrity, CapInnerSession}, RelayEndpoint: endpoint}, towerID)
	require.NoError(t, err)
	return acc.SessionID
}

// Adoption is the one write path a peer instance has into this instance's session map,
// and the exact session id is what authenticates it: the record was written by the
// opening instance only after the Tower's signed request was verified there. A guessed
// or stale id must buy nothing.
func TestAdoptionRequiresTheExactSessionID(t *testing.T) {
	m := NewMemMirror()
	a, b := New(mirrorCfg(m)), New(mirrorCfg(m))
	sess := openOn(t, a, "tw-1", "")

	require.ErrorIs(t, b.Heartbeat("guessed-session-id", "tw-1"), ErrNoSession,
		"a wrong session id must not adopt the shared record")
	require.ErrorIs(t, b.Heartbeat(sess, "tw-other"), ErrNoSession,
		"the right id for the wrong tower must not adopt either")
	require.NoError(t, b.Heartbeat(sess, "tw-1"), "the exact pair adopts")
}

// A stale shared record is a session nobody has kept alive; adopting it would resurrect
// a link past its freshness window.
func TestAdoptionRefusesAStaleRecord(t *testing.T) {
	m := NewMemMirror()
	a, b := New(mirrorCfg(m)), New(mirrorCfg(m))
	sess := openOn(t, a, "tw-1", "")

	rec, ok, err := m.Get("tw-1")
	require.NoError(t, err)
	require.True(t, ok)
	rec.LastSeen = time.Now().Add(-time.Hour)
	require.NoError(t, m.Put("tw-1", rec))

	require.ErrorIs(t, b.Heartbeat(sess, "tw-1"), ErrNoSession,
		"a lapsed shared record must not be adopted back to life")
}

// errMirror answers every read with an error AND a plausible-looking record, which is
// what isolates "the error must gate the answer": a mirror whose failure mode returned
// ok=false would let the err check be deleted invisibly.
type errMirror struct{ rec Record }

func (e errMirror) Put(string, Record) error { return errors.New("down") }
func (e errMirror) Get(string) (Record, bool, error) {
	return e.rec, true, errors.New("down")
}
func (e errMirror) Del(string, string) error        { return errors.New("down") }
func (e errMirror) All() (map[string]Record, error) { return nil, errors.New("down") }

func TestAFailingMirrorNeverSuppliesLiveness(t *testing.T) {
	m := errMirror{rec: Record{SessionID: "s", LastSeen: time.Now(), Relay: RelayPlane{Endpoint: "h:1"}}}
	s := New(mirrorCfg(m))

	require.False(t, s.Live("tw-1"),
		"a record that rode in beside an error is not evidence; an instance never invents liveness")
	_, has := s.RelayPlane("tw-1")
	require.False(t, has, "nor a relay plane")
	require.ErrorIs(t, s.Heartbeat("s", "tw-1"), ErrNoSession,
		"nor an adopted session")
}

// The err==nil half of the tombstone: a mirror that ERRORS must not tear down a session
// this instance is actually holding.
func TestAFailingMirrorDoesNotKillALocalSession(t *testing.T) {
	m := NewMemMirror()
	s := New(mirrorCfg(m))
	openOn(t, s, "tw-1", "hub:1")
	m.FailForTest(true)

	require.True(t, s.Live("tw-1"), "the local session is what this instance can actually see")
	_, has := s.RelayPlane("tw-1")
	require.True(t, has)
}

// The mem mirror's own surface, exercised in this package: the BDD suite drives it
// through two brokers, but this package's floor is measured on its own tests.
func TestMemMirrorSurface(t *testing.T) {
	m := NewMemMirror()
	rec := Record{SessionID: "s1", Version: 1, LastSeen: time.Now(), Relay: RelayPlane{Endpoint: "h:1"}}
	require.NoError(t, m.Put("tw-1", rec))
	require.NoError(t, m.Put("tw-2", rec))

	all, err := m.All()
	require.NoError(t, err)
	require.Len(t, all, 2)

	require.NoError(t, m.Del("tw-2", "s1"))
	_, ok, err := m.Get("tw-2")
	require.NoError(t, err)
	require.False(t, ok)
	require.NoError(t, m.Del("tw-2", "s1"), "deleting an absent record is not an error")

	// The failure switch fails EVERY operation - a store that half-fails would let one
	// path trust it while another cannot.
	m.FailForTest(true)
	require.ErrorIs(t, m.Put("tw-1", rec), ErrMirrorDown)
	_, _, err = m.Get("tw-1")
	require.ErrorIs(t, err, ErrMirrorDown)
	require.ErrorIs(t, m.Del("tw-1", "s1"), ErrMirrorDown)
	_, err = m.All()
	require.ErrorIs(t, err, ErrMirrorDown)
}

// LiveTowers is the fleet-wide set: local sessions plus every fresh peer record, deduped,
// stale records excluded, and the local set alone when the mirror cannot answer.
func TestLiveTowersUnionsLocalAndMirror(t *testing.T) {
	m := NewMemMirror()
	a, b := New(mirrorCfg(m)), New(mirrorCfg(m))
	openOn(t, a, "tw-a", "")
	openOn(t, b, "tw-b", "")
	require.NoError(t, m.Put("tw-stale", Record{SessionID: "x", LastSeen: time.Now().Add(-time.Hour)}))

	got := a.LiveTowers()
	require.ElementsMatch(t, []string{"tw-a", "tw-b"}, got,
		"the union, deduped, without the stale peer record")

	m.FailForTest(true)
	require.ElementsMatch(t, []string{"tw-a"}, a.LiveTowers(),
		"mirror down: the local set is what this instance can actually see")
}

// A session closed by its own tower cannot be closed AGAIN by someone else's frame, and
// closing cleans the shared record exactly once.
func TestCloseIsOwnerScopedAndCleansTheMirror(t *testing.T) {
	m := NewMemMirror()
	s := New(mirrorCfg(m))
	sess := openOn(t, s, "tw-1", "")

	s.Close(sess, "tw-other") // not their session: nothing happens
	require.True(t, s.Live("tw-1"))

	s.Close(sess, "tw-1")
	require.False(t, s.Live("tw-1"))
	_, ok, err := m.Get("tw-1")
	require.NoError(t, err)
	require.False(t, ok, "the close's tombstone")
}

// A zero Config still yields a safe session layer: this constructor guards the floor
// beneath every window the spec's freshness scenarios rely on.
func TestNewDefaultsAreSafe(t *testing.T) {
	s := New(Config{})
	require.Equal(t, PublicNetwork, s.cfg.Network)
	require.NotEmpty(t, s.cfg.Versions)
	require.Positive(t, s.cfg.Heartbeat)
	require.Greater(t, s.cfg.Freshness, s.cfg.Heartbeat,
		"a freshness window at or below the heartbeat drops a healthy Tower on one lost frame")
	require.Positive(t, s.cfg.MaxPerTower)
}

func TestAdoptRefusesAnIDAlreadyInUse(t *testing.T) {
	m := NewMemMirror()
	s := New(mirrorCfg(m))
	sess := openOn(t, s, "tw-1", "")
	require.Error(t, s.Adopt(sess, "tw-2"), "a session identity in use is not adoptable")
	require.NoError(t, s.Adopt("some-new-id", "tw-2"))
}

// Every PGMirror operation against a dead handle surfaces its error rather than reading
// as empty state - a mirror that half-fails would let one path trust it while another
// cannot. A closed pool needs no live database, so this runs everywhere.
func TestPGMirrorSurfacesEveryFailure(t *testing.T) {
	db, err := sql.Open("pgx", "postgres://u:p@127.0.0.1:1/none")
	require.NoError(t, err)
	require.NoError(t, db.Close())

	_, err = NewPGMirror(db)
	require.Error(t, err, "a schema that cannot be applied must refuse the mirror")

	m := &PGMirror{db: db}
	require.Error(t, m.Put("tw-1", Record{}))
	_, _, err = m.Get("tw-1")
	require.Error(t, err)
	require.Error(t, m.Del("tw-1", "s1"))
	_, err = m.All()
	require.Error(t, err)
}

// The compare half of compare-and-delete: a stale close quoting a superseded session id
// must not wipe the newer session's row and leave the Tower transiently dark.
func TestAStaleCloseCannotDimTheNewerSession(t *testing.T) {
	m := NewMemMirror()
	a := New(mirrorCfg(m))
	old := openOn(t, a, "tw-1", "")
	fresh := openOn(t, a, "tw-1", "") // supersedes; the mirror row now names this one

	a.Close(old, "tw-1")
	require.True(t, a.Live("tw-1"), "closing the superseded session must not touch the live one")

	rec, ok, err := m.Get("tw-1")
	require.NoError(t, err)
	require.True(t, ok, "the newer row survives the stale close")
	require.Equal(t, fresh, rec.SessionID)

	a.Close(fresh, "tw-1")
	require.False(t, a.Live("tw-1"))
}

// The close reaching an instance that never held the session - at two instances, half of
// all closes. The mirror is how it lands.
func TestACloseOnTheOtherInstanceStillCloses(t *testing.T) {
	m := NewMemMirror()
	a, b := New(mirrorCfg(m)), New(mirrorCfg(m))
	sess := openOn(t, a, "tw-1", "")
	require.True(t, b.Live("tw-1"))

	b.Close(sess, "tw-1") // b never held it
	require.False(t, a.Live("tw-1"), "the tombstone reaches the holder")
	require.False(t, b.Live("tw-1"))

	// And a guessed session id closes nothing.
	sess2 := openOn(t, a, "tw-1", "")
	b.Close("guessed", "tw-1")
	require.True(t, a.Live("tw-1"))
	_ = sess2
}
