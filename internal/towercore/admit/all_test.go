package admit

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The approval queue's order is a promise to the admin: waiting Towers first, newest
// enrollment first within a group, and stable - a queue that reshuffles under the cursor
// invites approving the wrong row.
func TestAllOrdersTheQueueForTheApprover(t *testing.T) {
	r := New(Config{TokenTTL: time.Hour, LeaseTTL: time.Hour, MaxTowersPerOwner: 10})

	enroll := func(owner string) Tower {
		tok, err := r.IssueToken(owner)
		require.NoError(t, err)
		tw, err := r.Enroll(tok, "key-"+owner)
		require.NoError(t, err)
		return tw
	}

	oldest := enroll("owner-1")
	require.NoError(t, r.Transition(oldest.ID, StateActive))
	revoked := enroll("owner-2")
	require.NoError(t, r.Transition(revoked.ID, StateRevoked))
	waiting := enroll("owner-3")

	got := r.All()
	require.Len(t, got, 3)
	require.Equal(t, waiting.ID, got[0].ID, "the Tower awaiting a decision leads")
	require.Equal(t, oldest.ID, got[1].ID, "live states next")
	require.Equal(t, revoked.ID, got[2].ID, "terminal states last")

	// Stable across reads.
	again := r.All()
	for i := range got {
		require.Equal(t, got[i].ID, again[i].ID)
	}
}

func TestWaitingRankCoversEveryState(t *testing.T) {
	require.Equal(t, 0, waitingRank(StateQuarantine))
	require.Equal(t, 0, waitingRank(StatePending))
	require.Equal(t, 1, waitingRank(StateActive))
	require.Equal(t, 1, waitingRank(StateDraining))
	require.Equal(t, 1, waitingRank(StateSuspended))
	require.Equal(t, 2, waitingRank(StateRevoked))
	require.Equal(t, 2, waitingRank(StateExpired))
	require.Equal(t, 2, waitingRank(State("unknown")), "an unknown state must not jump the queue")
}

// A store that cannot answer yields an empty queue, never a partial one presented as whole.
func TestAllOnAFailingStoreAnswersEmpty(t *testing.T) {
	r := NewWithStore(Config{}, allFailingStore{})
	require.Nil(t, r.All())
}

// allFailingStore fails exactly the read All depends on; embedding the interface keeps
// this fake honest as the interface grows.
type allFailingStore struct{ Store }

func (allFailingStore) AllTowers() ([]Tower, error) { return nil, ErrUnavailable }

// Token reads without consuming, and an outage answers as an outage - never as "no such
// token", which would burn a legitimate enrollment.
func TestTokenReadsWithoutConsumingAndFailsHonestly(t *testing.T) {
	r := New(Config{TokenTTL: time.Hour, LeaseTTL: time.Hour, MaxTowersPerOwner: 3})
	id, err := r.IssueToken("owner-a")
	require.NoError(t, err)

	tok, ok, err := r.Token(id)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "owner-a", tok.Owner)

	// Reading did not consume it.
	_, stillThere, err := r.Token(id)
	require.NoError(t, err)
	require.True(t, stillThere)

	// A store outage is ErrUnavailable, wrapped or bare.
	broken := NewWithStore(Config{}, tokenReadFailingStore{})
	_, _, err = broken.Token("any")
	require.ErrorIs(t, err, ErrUnavailable)
	plain := NewWithStore(Config{}, tokenReadPlainErrStore{})
	_, _, err = plain.Token("any")
	require.ErrorIs(t, err, ErrUnavailable, "a plain store error is still reported as unavailability")
}

type tokenReadFailingStore struct{ Store }

func (tokenReadFailingStore) GetToken(string) (Token, bool, error) {
	return Token{}, false, ErrUnavailable
}

type tokenReadPlainErrStore struct{ Store }

func (tokenReadPlainErrStore) GetToken(string) (Token, bool, error) {
	return Token{}, false, errPlainOutage
}

var errPlainOutage = errString("the database fell over")

type errString string

func (e errString) Error() string { return string(e) }
