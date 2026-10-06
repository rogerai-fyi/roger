package attach

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// THE RACES, LANDED ON PURPOSE.
//
// The concurrent tests in parity_test.go prove the outcome of a race, but not which branch
// settled it: whether a loser is caught at the bindings read, at the commit, or not at all
// depends on how the scheduler and the database happen to interleave that run. On a fast
// database the goroutines rarely overlap, so the paths that answer a LOSER were exercised
// only when the machine was slow enough to make them collide. A guarantee checked only by
// luck is not checked.
//
// interleavingStore puts the competing write at a chosen point instead. It delegates every
// call to the real store underneath (memory or Postgres) and, exactly once, runs `before`
// immediately ahead of the first call to the named operation. The competitor writes through
// the UNWRAPPED store, so what the loser meets is a genuinely committed winner, not a stub.
type interleavingStore struct {
	Store
	at     string
	before func()
	fired  bool
}

func (s *interleavingStore) fire(op string) {
	if s.at == op && !s.fired {
		s.fired = true
		s.before()
	}
}

func (s *interleavingStore) Admit(authID string, at Attachment) (bool, error) {
	s.fire("admit")
	return s.Store.Admit(authID, at)
}

func (s *interleavingStore) ByStation(id string) (Attachment, bool, error) {
	s.fire("bystation")
	return s.Store.ByStation(id)
}

// racedRegistry is a Registry over s that, at the named operation, first lets `competitor`
// run against the bare store.
func racedRegistry(t *testing.T, s Store, now time.Time, op string, competitor func()) (*Registry, *interleavingStore) {
	t.Helper()
	is := &interleavingStore{Store: s, at: op, before: competitor}
	return New(Config{Network: net, Now: func() time.Time { return now }}, is), is
}

// requireOneAttachment asserts the race produced exactly the winner's origin and spent the
// invitation on it once.
func requireOneAttachment(t *testing.T, s Store) Attachment {
	t.Helper()
	at, ok, err := s.ByStation(station)
	require.NoError(t, err)
	require.True(t, ok, "the winner's attachment must be recorded")
	require.Equal(t, int64(1), at.Epoch, "nobody may mint a second origin")
	require.Equal(t, authorID, at.AuthID)
	auth, ok, err := s.Authorization(authorID)
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, auth.Consumed, "the invitation is spent")
	require.Equal(t, station, auth.ConsumedBy, "and spent on the winner")
	return at
}

// A racer that passed every check and then lost the commit is not refused: it did nothing
// wrong, and its caller cannot tell a lost race from a lost reply. The store reports the
// invitation already consumed, and the loser answers with the winner's committed record.
func TestALoserAtTheCommitAnswersFromTheWinner(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store, r *Registry, now time.Time) {
		var winner Attachment
		loser, is := racedRegistry(t, s, now, "admit", func() {
			var err error
			winner, err = r.Admit(goodProof())
			require.NoError(t, err, "the competitor commits first")
		})

		got, err := loser.Admit(goodProof())
		require.True(t, is.fired, "the competitor must have run ahead of the loser's commit")
		require.NoError(t, err, "losing the commit to an identical proof is not a refusal")

		stored := requireOneAttachment(t, s)
		require.Equal(t, stored, got, "the loser answers with the committed record")
		require.Equal(t, winner.StationID, got.StationID)
		require.Equal(t, winner.Epoch, got.Epoch)
	})
}

// The store half of the same race, without the Registry in front: a second Admit of an
// already-spent authorization reports "lost" with no error and writes nothing, whichever
// store is underneath. This is the compare-and-set the whole one-origin guarantee rests on.
func TestASecondCommitOfOneAuthorizationLosesWithoutError(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store, r *Registry, now time.Time) {
		first, err := r.Admit(goodProof())
		require.NoError(t, err)

		again := first
		again.Epoch = first.Epoch + 1 // even a write that would otherwise be accepted
		won, err := s.Admit(authorID, again)
		require.NoError(t, err, "a lost compare-and-set is an answer, not an outage")
		require.False(t, won, "one authorization is consumed exactly once")

		requireOneAttachment(t, s)
	})
}

// A racer that read the invitation BEFORE the winner committed reaches the bindings check
// AFTER it did, and finds the Station ID already attached - by this very invitation. That is
// a retry, not a conflict: it goes on to the store, loses the commit, and answers with the
// committed outcome rather than turning a lost response into a permanent refusal.
func TestALoserAtTheBindingsCheckAnswersFromTheWinner(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store, r *Registry, now time.Time) {
		loser, is := racedRegistry(t, s, now, "bystation", func() {
			_, err := r.Admit(goodProof())
			require.NoError(t, err, "the competitor commits first")
		})

		got, err := loser.Admit(goodProof())
		require.True(t, is.fired, "the competitor must have run ahead of the bindings read")
		require.NoError(t, err,
			"an attachment made by the same invitation is the caller's own, not a conflict")

		stored := requireOneAttachment(t, s)
		require.Equal(t, stored, got)
	})
}

// A loser that cannot re-read the invitation it just lost is told the service is
// unavailable, never that it was refused: its keys were fine, and a refusal would send the
// operator to regenerate them.
func TestALoserWhoseReReadFailsReportsAnOutage(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store, r *Registry, now time.Time) {
		rs := &refusingStore{Store: s}
		is := &interleavingStore{Store: rs, at: "admit", before: func() {
			_, err := r.Admit(goodProof())
			require.NoError(t, err, "the competitor commits first")
			rs.fail = "authorization" // and then the re-read cannot be answered
		}}
		loser := New(Config{Network: net, Now: func() time.Time { return now }}, is)

		_, err := loser.Admit(goodProof())
		require.True(t, is.fired)
		require.ErrorIs(t, err, ErrUnavailable)
		require.NotErrorIs(t, err, ErrRejected)

		requireOneAttachment(t, s)
	})
}

// Two DISTINCT invitations naming one assertion key, racing. The bindings check reads before
// either commits, so both pass it; the STORE is what refuses the second. That refusal is
// permanent and must reach the caller as one - reported as an outage it invites a retry
// forever - and, rolled back, it must leave the loser's own invitation unspent.
func TestAKeyCollisionAtTheCommitIsAPermanentRefusal(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store, r *Registry, now time.Time) {
		second, secret, err := NewInvite(Authorization{
			ID: "auth-2", Network: net, StationID: "st-2", Owner: owner,
			Origin: Origin{Kind: OriginJoined, TowerID: tower},
			// A DIFFERENT Station, the SAME assertion key.
			AssertionKey: keyA, SessionKey: "K2",
		}, time.Hour, now.Add(-time.Minute))
		require.NoError(t, err)
		require.NoError(t, s.PutAuthorization(second))

		loser, is := racedRegistry(t, s, now, "admit", func() {
			_, err := r.Admit(Proof{
				AuthID: "auth-2", Secret: secret, Network: net, StationID: "st-2", Owner: owner,
				Origin: Origin{Kind: OriginJoined, TowerID: tower}, AssertionKey: keyA, SessionKey: "K2",
			})
			require.NoError(t, err, "the competitor takes the key first")
		})

		_, err = loser.Admit(goodProof())
		require.True(t, is.fired, "the competitor must have run ahead of the loser's commit")
		require.ErrorIs(t, err, ErrRejected, "a held key is a permanent answer")
		require.NotErrorIs(t, err, ErrUnavailable,
			"an outage invites a caller to retry forever against something that will never change")

		_, ok, err := s.ByStation(station)
		require.NoError(t, err)
		require.False(t, ok, "the refused Station left no attachment behind")
		auth, ok, err := s.Authorization(authorID)
		require.NoError(t, err)
		require.True(t, ok)
		require.False(t, auth.Consumed, "a refused attachment must not cost the owner their invitation")

		holder, ok, err := s.ByAssertionKey(keyA)
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, "st-2", holder.StationID, "the key stays with the competitor that won it")
	})
}
