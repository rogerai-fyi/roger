package attach

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// THE LOST RACE, DRIVEN BY HAND.
//
// Registry.Admit reads the invitation, checks the bindings, and only then asks the store to
// consume and commit. Between that first read and that commit another caller can win, and the
// code has a branch for each place it can find out: checkBindings meets an attachment that this
// very invitation produced (the racer short-circuit), the store reports the invitation already
// spent (the `!won` replay), or the store refuses the commit outright because the racer took the
// key. The two stores have the matching branch underneath - memStore.Admit's `a.Consumed` and
// PGStore.Admit's `consumed` after FOR UPDATE.
//
// Until this file those branches were reached ONLY by the goroutine races in
// TestParityOneAuthorizationSurvivesARace, TestConcurrentAttachmentConsumesOneAuthorizationOnce
// and TestParityConcurrentAttachmentsCannotShareAnAssertionKey, which start N racers and let the
// scheduler decide whether any of them reads before the winner commits. Usually one does;
// sometimes - under a loaded gate, with Postgres serialising on the row lock - the winner
// commits before any other racer gets its first read in, every racer takes the replay branch at
// the top of Admit, and the lost-race branches run zero times. The coverage gate measured this
// package at 90.9% and then 89.4% on an identical tree, against a 90% floor.
//
// The races stay: they are the only thing that proves the store's lock actually arbitrates. What
// changes is that the OUTCOME of losing is now asserted on a schedule the test controls, so both
// sides of every branch run every time. interposingStore is the seam the Tower admission
// registry already uses for the same job (internal/towercore/admit): a decorator over the REAL
// store that runs a hook inside the window a concurrent writer would occupy. Nothing is faked -
// the competing writer goes through the same store, lock and constraints as the caller.

// interposingStore runs each hook ONCE, at the point in Registry.Admit it is named for. The
// hooks clear themselves so the Registry's own follow-up reads (the `fresh` re-read on the
// `!won` path) see the store as it is.
type interposingStore struct {
	Store
	// afterAuthorization runs after the Registry's first read of the invitation - before
	// validate, before checkBindings, before the commit.
	afterAuthorization func()
	// beforeAdmit runs after checkBindings has passed and just before the Registry commits.
	beforeAdmit func()
}

func (i *interposingStore) Authorization(id string) (Authorization, bool, error) {
	a, ok, err := i.Store.Authorization(id)
	if h := i.afterAuthorization; h != nil {
		i.afterAuthorization = nil
		h()
	}
	return a, ok, err
}

func (i *interposingStore) Admit(authID string, at Attachment) (bool, error) {
	if h := i.beforeAdmit; h != nil {
		i.beforeAdmit = nil
		h()
	}
	return i.Store.Admit(authID, at)
}

// loserOver builds the Registry whose Admit will lose: it reads through `inter`, so the hooks
// fire inside its window, while the winner in each hook writes through the bare store.
func loserOver(inter Store, now time.Time) *Registry {
	return New(Config{Network: net, Now: func() time.Time { return now }}, inter)
}

// A RACER THAT READ THE INVITATION BEFORE THE WINNER COMMITTED is answered from the winner's
// record, in both stores. This is the shape every goroutine race in this package exists to
// produce and cannot guarantee: the read succeeds unconsumed, the winner commits, checkBindings
// meets an attachment carrying THIS invitation's id and lets it through (it is a retry, not a
// conflict), the store reports the invitation spent, and the `!won` path replays the committed
// outcome. The caller did nothing wrong and must not be refused.
func TestParityALoserThatReadBeforeTheWinnerCommittedIsAnsweredFromTheWinnersRecord(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store, winner *Registry, now time.Time) {
		inter := &interposingStore{Store: s}
		loser := loserOver(inter, now)

		var won Attachment
		inter.afterAuthorization = func() {
			var err error
			won, err = winner.Admit(goodProof())
			require.NoError(t, err)
		}

		got, err := loser.Admit(goodProof())
		require.NoError(t, err, "a racer that lost the commit is answered, not refused")
		require.Equal(t, won.StationID, got.StationID)
		require.Equal(t, won.AuthID, got.AuthID, "the answer is the winner's committed record")
		require.Equal(t, int64(1), got.Epoch, "nobody minted a second origin")
		require.Equal(t, StateQuarantine, got.State)

		auth, ok, err := s.Authorization(authorID)
		require.NoError(t, err)
		require.True(t, ok)
		require.True(t, auth.Consumed)
		require.Equal(t, station, auth.ConsumedBy, "consumed exactly once, by the winner")
	})
}

// A RACER THAT LOST THE COMMIT ITSELF - checkBindings passed on an empty table, the winner
// landed in between - takes the `!won` replay the same way, in both stores. memStore.Admit
// answers from its `a.Consumed` check and PGStore.Admit from `consumed` under FOR UPDATE; both
// must say "lost", not "failed".
func TestParityALoserThatLostTheCommitIsAnsweredFromTheWinnersRecord(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store, winner *Registry, now time.Time) {
		inter := &interposingStore{Store: s}
		loser := loserOver(inter, now)

		inter.beforeAdmit = func() {
			_, err := winner.Admit(goodProof())
			require.NoError(t, err)
		}

		got, err := loser.Admit(goodProof())
		require.NoError(t, err)
		require.Equal(t, station, got.StationID)
		require.Equal(t, authorID, got.AuthID)
		require.Equal(t, int64(1), got.Epoch)
	})
}

// A STORE REFUSAL IN THE COMMIT REACHES THE CALLER AS A REFUSAL. The racer here holds a
// DIFFERENT invitation for a different Station naming the SAME assertion key; checkBindings
// found the key free, the racer took it in between, and the store refuses the commit - the
// memory store under its mutex, Postgres through the partial unique index. Registry.Admit must
// pass that through as ErrRejected rather than dress it as an outage, or the handler tells the
// caller "try again in a moment" about something that will never change.
// TestParityConcurrentAttachmentsCannotShareAnAssertionKey only reaches this branch when the
// scheduler lets both racers clear checkBindings first.
func TestParityAStoreRefusalInTheCommitIsARefusalNotAnOutage(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store, _ *Registry, now time.Time) {
		inter := &interposingStore{Store: s}
		loser := loserOver(inter, now)

		rival := withSecret(Authorization{
			ID: "auth-2", Network: net, StationID: "st-2", Owner: owner,
			Origin:       Origin{Kind: OriginJoined, TowerID: tower},
			AssertionKey: keyA, SessionKey: "K2",
			IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
		})
		require.NoError(t, s.PutAuthorization(rival))
		inter.beforeAdmit = func() {
			ok, err := s.Admit(rival.ID, Attachment{
				StationID: "st-2", Owner: owner, AssertionKey: keyA, SessionKey: "K2",
				Origin: rival.Origin, Epoch: 1, State: StateQuarantine, AttachedAt: now,
				AuthID: rival.ID,
			})
			require.NoError(t, err)
			require.True(t, ok)
		}

		_, err := loser.Admit(goodProof())
		require.ErrorIs(t, err, ErrRejected, "the store's permanent answer must arrive as a refusal")
		require.NotErrorIs(t, err, ErrUnavailable,
			"reporting it as an outage invites a retry loop against something that will never change")

		// The refusal cost the loser nothing: no row, and the invitation is still spendable.
		_, ok, err := s.ByStation(station)
		require.NoError(t, err)
		require.False(t, ok, "a refused commit must leave no Station behind")
		auth, ok, err := s.Authorization(authorID)
		require.NoError(t, err)
		require.True(t, ok)
		require.False(t, auth.Consumed, "a refused attachment must not spend the invitation")
	})
}

// A LOSER WHOSE RE-READ FAILS IS TOLD THE STORE IS UNAVAILABLE, not that its keys were wrong.
// The `!won` path re-reads the invitation to find the winner; if the store cannot answer that
// read, the caller is owed the same "try again" every other store blink produces.
func TestParityALoserWhoseReReadFailsReportsAnOutage(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store, winner *Registry, now time.Time) {
		rs := &refusingStore{Store: s}
		inter := &interposingStore{Store: rs}
		loser := loserOver(inter, now)

		inter.beforeAdmit = func() {
			_, err := winner.Admit(goodProof())
			require.NoError(t, err)
			rs.fail = "authorization"
		}

		_, err := loser.Admit(goodProof())
		require.ErrorIs(t, err, ErrUnavailable)
		require.NotErrorIs(t, err, ErrRejected)
	})
}

// A LOSER WHOSE INVITATION WAS REAPED BEFORE ITS RE-READ gets a refusal, not an outage. The
// store answered "spent" and then the row was gone: there is nothing to replay and nothing to
// retry against, and the sentence is the one for an invitation that does not exist.
func TestParityALoserWhoseInvitationWasReapedIsRefusedNotStranded(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store, winner *Registry, now time.Time) {
		inter := &interposingStore{Store: s}
		loser := loserOver(inter, now)

		inter.beforeAdmit = func() {
			_, err := winner.Admit(goodProof())
			require.NoError(t, err)
			// The consumed row, past expiry and past the retry horizon, is reaped.
			n, err := s.Reap(now.Add(2*time.Hour), 0)
			require.NoError(t, err)
			require.Equal(t, int64(1), n)
		}

		_, err := loser.Admit(goodProof())
		require.ErrorIs(t, err, ErrRejected)
		require.NotErrorIs(t, err, ErrUnavailable)
		require.Contains(t, err.Error(), "no such invitation")
	})
}

// THE STORE CONTRACT ITSELF: Admit on an invitation that is already spent returns false with NO
// error, in both stores. The Store interface promises exactly this ("false with no error when
// the authorization was already consumed - the caller then decides whether this is an
// idempotent retry or divergent reuse"), and Registry.Admit's `!won` branch is written against
// it. Until now nothing drove the promise except a racer that happened to lose.
func TestParityAdmitOnASpentInvitationReportsALostRaceWithoutError(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store, r *Registry, now time.Time) {
		first, err := r.Admit(goodProof())
		require.NoError(t, err)

		won, err := s.Admit(authorID, first)
		require.NoError(t, err, "a spent invitation is a lost race, not a store failure")
		require.False(t, won)

		got, ok, err := s.ByStation(station)
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, int64(1), got.Epoch, "the losing write changed nothing")
		require.Equal(t, authorID, got.AuthID)
	})
}
