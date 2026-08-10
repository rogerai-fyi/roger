package stationattach

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The branches the happy path never reaches: a store that cannot answer, and the checks that
// only fire when an earlier one passes.
//
// The store-failure branches all decide ONE thing: whether an operator is told "your keys
// are wrong" or "we cannot answer right now". Getting that backwards sends somebody to
// support with the wrong problem, and in the attachment path it would send them to
// regenerate keys that were never the issue.

// refusingStore delegates to a real store but can be armed to fail one named operation, so
// each unavailable branch is reachable on its own rather than in a heap.
type refusingStore struct {
	Store
	fail string
}

var errBoom = errors.New("the store fell over")

func (r *refusingStore) Authorization(id string) (Authorization, bool, error) {
	if r.fail == "authorization" {
		return Authorization{}, false, errBoom
	}
	return r.Store.Authorization(id)
}

func (r *refusingStore) Admit(authID string, at Attachment) (bool, error) {
	if r.fail == "admit" {
		return false, errBoom
	}
	return r.Store.Admit(authID, at)
}

func (r *refusingStore) ByStation(id string) (Attachment, bool, error) {
	if r.fail == "bystation" {
		return Attachment{}, false, errBoom
	}
	return r.Store.ByStation(id)
}

func (r *refusingStore) ByAssertionKey(k string) (Attachment, bool, error) {
	if r.fail == "byassertion" {
		return Attachment{}, false, errBoom
	}
	return r.Store.ByAssertionKey(k)
}

func (r *refusingStore) BySessionKey(k string) (Attachment, bool, error) {
	if r.fail == "bysession" {
		return Attachment{}, false, errBoom
	}
	return r.Store.BySessionKey(k)
}

func (r *refusingStore) SetState(id, state string) (bool, error) {
	if r.fail == "setstate" {
		return false, errBoom
	}
	return r.Store.SetState(id, state)
}

func armed(t *testing.T, op string) (*Registry, *refusingStore) {
	t.Helper()
	now := time.Unix(1_700_000_000, 0).UTC()
	base := NewMemStore()
	require.NoError(t, base.PutAuthorization(withSecret(Authorization{
		ID: authorID, Network: net, StationID: station, Owner: owner,
		Origin: Origin{Kind: OriginJoined, TowerID: tower}, AssertionKey: keyA, SessionKey: keyK,
		IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
	})))
	rs := &refusingStore{Store: base, fail: op}
	return New(Config{Network: net, Now: func() time.Time { return now }}, rs), rs
}

func TestAStoreThatCannotAnswerIsNotARefusal(t *testing.T) {
	for _, op := range []string{"authorization", "admit", "bystation", "byassertion", "bysession"} {
		t.Run(op, func(t *testing.T) {
			r, _ := armed(t, op)
			_, err := r.Admit(goodProof())
			require.ErrorIs(t, err, ErrUnavailable,
				"a store outage must report an outage, never that the attachment was refused")
			require.NotErrorIs(t, err, ErrRejected,
				"telling an operator their keys are wrong because the database blinked sends them "+
					"to regenerate keys that were fine")
		})
	}
}

func TestTheReadPathsAlsoReportAnOutage(t *testing.T) {
	r, rs := armed(t, "")
	_, err := r.Admit(goodProof())
	require.NoError(t, err)

	rs.fail = "bystation"
	_, _, err = r.Station(station)
	require.ErrorIs(t, err, ErrUnavailable)
	_, err = r.Promote(station)
	require.ErrorIs(t, err, ErrUnavailable)

	rs.fail = "setstate"
	_, err = r.Revoke(station)
	require.ErrorIs(t, err, ErrUnavailable)

	// Promote reads first, then writes: arm only the write so the second branch is reached.
	rs.fail = ""
	_, err = r.Admit(goodProof())
	require.NoError(t, err)
	rs.fail = "setstate"
	_, err = r.Promote(station)
	require.ErrorIs(t, err, ErrUnavailable)
}

// A losing racer whose authorization vanished under it - the store said "already consumed"
// and then could not produce the record. It must not be told its keys were wrong.
func TestALostRaceWithAnUnreadableWinnerReportsAnOutage(t *testing.T) {
	r, rs := armed(t, "")
	_, err := r.Admit(goodProof())
	require.NoError(t, err)

	// Second call takes the replay path; make the record unreadable.
	rs.fail = "bystation"
	_, err = r.Admit(goodProof())
	require.ErrorIs(t, err, ErrUnavailable)
}

// A consumed authorization whose Station record is gone entirely is not an outage - it is an
// invitation that cannot be replayed, and reuse is refused.
func TestAConsumedAuthorizationWithNoRecordIsRefused(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	s := NewMemStore()
	require.NoError(t, s.PutAuthorization(withSecret(Authorization{
		ID: authorID, Network: net, StationID: station, Owner: owner,
		Origin: Origin{Kind: OriginJoined, TowerID: tower}, AssertionKey: keyA, SessionKey: keyK,
		IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
		Consumed: true, ConsumedBy: "st-vanished",
	})))
	r := New(Config{Network: net, Now: func() time.Time { return now }}, s)

	_, err := r.Admit(goodProof())
	require.ErrorIs(t, err, ErrRejected)
	require.Contains(t, err.Error(), "already been used")
}

// An invitation issued in the future is a forged or badly-clocked one, and it is refused
// before anything is consumed.
func TestAnInvitationFromTheFutureIsRefused(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	s := NewMemStore()
	require.NoError(t, s.PutAuthorization(withSecret(Authorization{
		ID: authorID, Network: net, StationID: station, Owner: owner,
		Origin: Origin{Kind: OriginJoined, TowerID: tower}, AssertionKey: keyA, SessionKey: keyK,
		IssuedAt: now.Add(10 * time.Minute), ExpiresAt: now.Add(time.Hour),
	})))
	r := New(Config{Network: net, Now: func() time.Time { return now }}, s)

	_, err := r.Admit(goodProof())
	require.ErrorIs(t, err, ErrRejected)
	require.Contains(t, err.Error(), "not valid yet")
}

// The proof's origin is well-formed but the stored invitation's is not. Both are checked:
// trusting the proof alone would let a malformed invitation through on the strength of a
// tidy request.
func TestAMalformedInvitationOriginIsRefusedEvenWithATidyProof(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	s := NewMemStore()
	require.NoError(t, s.PutAuthorization(withSecret(Authorization{
		ID: authorID, Network: net, StationID: station, Owner: owner,
		Origin: Origin{Kind: "sideways", TowerID: tower}, AssertionKey: keyA, SessionKey: keyK,
		IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
	})))
	r := New(Config{Network: net, Now: func() time.Time { return now }}, s)

	_, err := r.Admit(goodProof())
	require.ErrorIs(t, err, ErrRejected)
	require.Contains(t, err.Error(), "unknown origin kind")
}

// Same Station, same origin kind, a DIFFERENT assertion key. Not a kind change and not a
// retry: it is a second signer claiming an existing identity.
func TestRebindingAStationToAnotherAssertionKeyIsRefused(t *testing.T) {
	r, s, now := fixture(t)
	_, err := r.Admit(goodProof())
	require.NoError(t, err)

	require.NoError(t, s.PutAuthorization(withSecret(Authorization{
		ID: "auth-2", Network: net, StationID: station, Owner: owner,
		Origin: Origin{Kind: OriginJoined, TowerID: tower}, AssertionKey: "A_new", SessionKey: "K_new",
		IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
	})))
	p := goodProof()
	p.AuthID, p.AssertionKey, p.SessionKey = "auth-2", "A_new", "K_new"

	_, err = r.Admit(p)
	require.ErrorIs(t, err, ErrRejected)
	require.Contains(t, err.Error(), "already bound to another assertion key")
}

func TestSetStateOnAnUnknownStationReportsNoChange(t *testing.T) {
	s := NewMemStore()
	moved, err := s.SetState("st-nobody", StateRevoked)
	require.NoError(t, err)
	require.False(t, moved)
}
