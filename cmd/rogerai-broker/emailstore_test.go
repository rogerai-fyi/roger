package main

// The shared store behind first-party sign-in.
//
// Email login shipped wired to the IN-PROCESS store, which carries exactly the defect that
// had just been fixed for device login and is worse here because it is user-facing on
// every sign-in:
//
//  1. A restart drops every outstanding code. The person types a code that really was
//     mailed to them and is told it is not valid.
//  2. Behind more than one instance, /auth/email/start lands on A and /auth/email/verify
//     on B, so the code is never found and first-party sign-in CANNOT COMPLETE.
//  3. The rate limits are per-instance, so the per-address limit that exists to stop our
//     mailer flooding somebody's inbox is silently multiplied by the instance count.

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/emailauth"
)

func newSharedEmailStore(t *testing.T) (emailauth.Store, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	vs, err := newValkeyStore("redis://" + mr.Addr())
	require.NoError(t, err)
	t.Cleanup(func() { _ = vs.Close() })
	s := newValkeyEmailStore(vs)
	require.NotNil(t, s, "a wired shared store must yield an email store")
	return s, mr
}

func TestSharedEmailStoreRoundTrip(t *testing.T) {
	s, _ := newSharedEmailStore(t)
	now := time.Now()
	rec := emailauth.Record{
		AddrHash: "addr-1", CodeHash: "code-1",
		Issued: now, Expires: now.Add(10 * time.Minute),
	}
	require.NoError(t, s.Put(rec))

	got, ok, err := s.ByAddress("addr-1")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "code-1", got.CodeHash)
	require.NotZero(t, got.Rev)
}

func TestSharedEmailStoreMissIsNotAnError(t *testing.T) {
	s, _ := newSharedEmailStore(t)
	_, ok, err := s.ByAddress("nobody")
	require.NoError(t, err)
	require.False(t, ok)
}

func TestSharedEmailStoreSpendsACodeExactlyOnce(t *testing.T) {
	// Two instances verifying the same correct code at the same moment must accept one.
	s, _ := newSharedEmailStore(t)
	now := time.Now()
	require.NoError(t, s.Put(emailauth.Record{
		AddrHash: "addr-1", CodeHash: "code-1", Issued: now, Expires: now.Add(time.Minute),
	}))

	read, ok, err := s.ByAddress("addr-1")
	require.NoError(t, err)
	require.True(t, ok)

	won, err := s.Consume(read)
	require.NoError(t, err)
	require.True(t, won)

	won, err = s.Consume(read)
	require.NoError(t, err)
	require.False(t, won, "the second instance loses, and its caller refuses")
}

func TestSharedEmailStoreReplacingACodeRetiresTheOldOne(t *testing.T) {
	s, _ := newSharedEmailStore(t)
	now := time.Now()
	require.NoError(t, s.Put(emailauth.Record{
		AddrHash: "addr-1", CodeHash: "old", Issued: now, Expires: now.Add(time.Minute),
	}))
	require.NoError(t, s.Put(emailauth.Record{
		AddrHash: "addr-1", CodeHash: "new", Issued: now, Expires: now.Add(time.Minute),
	}))

	got, _, err := s.ByAddress("addr-1")
	require.NoError(t, err)
	require.Equal(t, "new", got.CodeHash, "a person who asks twice must not leave a spare credential")
	require.Zero(t, got.Attempts, "and the fresh code carries a fresh budget")
}

func TestSharedEmailStoreCountsGuessesPerAddress(t *testing.T) {
	s, _ := newSharedEmailStore(t)
	now := time.Now()
	require.NoError(t, s.Put(emailauth.Record{
		AddrHash: "addr-1", CodeHash: "code-1", Issued: now, Expires: now.Add(time.Minute),
	}))

	for i := 1; i <= 3; i++ {
		n, err := s.Penalize("addr-1", time.Minute)
		require.NoError(t, err)
		require.Equal(t, i, n)
	}
	got, _, err := s.ByAddress("addr-1")
	require.NoError(t, err)
	require.Equal(t, 3, got.Attempts, "the budget rides with the record, so a restart cannot refill it")
}

func TestSharedEmailRateLimitsAreOneBudgetAcrossInstances(t *testing.T) {
	// The point of moving these into the shared store: two instances must not each grant a
	// full allowance, or the mail-flood control is exactly twice as weak as configured.
	s, _ := newSharedEmailStore(t)
	now := time.Now()

	for i := 0; i < 3; i++ {
		ok, err := s.AllowRequest("addr-1", "src-1", 3, 100, time.Hour, now)
		require.NoError(t, err)
		require.True(t, ok)
	}
	ok, err := s.AllowRequest("addr-1", "src-1", 3, 100, time.Hour, now)
	require.NoError(t, err)
	require.False(t, ok, "the per-address budget is spent, whichever instance asks")

	// A DIFFERENT address is still allowed: the budget just spent was the address's, not
	// the sender's. (The source budget is given room here so this asserts the property it
	// names - src-1 has already been charged three times above.)
	ok, err = s.AllowRequest("addr-2", "src-1", 3, 100, time.Hour, now)
	require.NoError(t, err)
	require.True(t, ok)
}

func TestSharedEmailSubmitLimitIsShared(t *testing.T) {
	s, _ := newSharedEmailStore(t)
	now := time.Now()
	for i := 0; i < 2; i++ {
		ok, err := s.AllowSubmit("src-1", 2, time.Hour, now)
		require.NoError(t, err)
		require.True(t, ok)
	}
	ok, err := s.AllowSubmit("src-1", 2, time.Hour, now)
	require.NoError(t, err)
	require.False(t, ok)
}

func TestSharedEmailStoreExpiresCodesServerSide(t *testing.T) {
	s, mr := newSharedEmailStore(t)
	now := time.Now()
	require.NoError(t, s.Put(emailauth.Record{
		AddrHash: "addr-1", CodeHash: "code-1", Issued: now, Expires: now.Add(time.Minute),
	}))

	mr.FastForward(2 * time.Minute)

	_, ok, err := s.ByAddress("addr-1")
	require.NoError(t, err)
	require.False(t, ok, "a code cannot outlive the window it was issued for")
}

func TestSharedEmailStoreReportsAnOutageRatherThanAMiss(t *testing.T) {
	// The distinction the whole design rests on: a store that cannot answer must not look
	// like a code that does not exist, or a person is told their good code is wrong.
	s, mr := newSharedEmailStore(t)
	now := time.Now()
	require.NoError(t, s.Put(emailauth.Record{
		AddrHash: "addr-1", CodeHash: "code-1", Issued: now, Expires: now.Add(time.Minute),
	}))

	mr.Close()

	_, ok, err := s.ByAddress("addr-1")
	require.Error(t, err)
	require.False(t, ok)
	require.ErrorIs(t, err, emailauth.ErrUnavailable)

	require.ErrorIs(t, s.Put(emailauth.Record{AddrHash: "addr-2"}), emailauth.ErrUnavailable)
}

func TestTwoInstancesCompleteOneEmailSignIn(t *testing.T) {
	// The case that is simply broken without this: the code is requested on one instance
	// and typed back on another.
	s, _ := newSharedEmailStore(t)
	instanceA := emailauth.NewWithStore(emailauth.Config{}, s)
	instanceB := emailauth.NewWithStore(emailauth.Config{}, s)

	code, err := instanceA.Request("someone@rogerai.fm", "src-1")
	require.NoError(t, err)

	addr, err := instanceB.Submit("someone@rogerai.fm", code, "src-1")
	require.NoError(t, err, "the verify must find a code the other instance issued")
	require.Equal(t, "someone@rogerai.fm", addr)

	// And it is spent everywhere, not just on B.
	_, err = instanceA.Submit("someone@rogerai.fm", code, "src-1")
	require.Error(t, err)
}

func TestNoSharedStoreKeepsEmailSignInInProcess(t *testing.T) {
	require.Nil(t, newValkeyEmailStore(nil))
	require.Nil(t, newValkeyEmailStore(&memStore{}))
}

// --- the outage branches, and the edges the happy path skips ---------------

func TestSharedEmailStoreReportsAnOutageOnEveryPath(t *testing.T) {
	// Each of these decides whether a person is told "your code is wrong" or "we cannot
	// answer right now". Getting any one of them backwards sends somebody to support with
	// the wrong problem.
	s, mr := newSharedEmailStore(t)
	now := time.Now()
	rec := emailauth.Record{
		AddrHash: "addr-1", CodeHash: "code-1", Issued: now, Expires: now.Add(time.Minute),
	}
	require.NoError(t, s.Put(rec))
	read, _, err := s.ByAddress("addr-1")
	require.NoError(t, err)

	mr.Close()

	_, err = s.Consume(read)
	require.ErrorIs(t, err, emailauth.ErrUnavailable)

	_, err = s.Penalize("addr-1", time.Minute)
	require.ErrorIs(t, err, emailauth.ErrUnavailable)

	_, err = s.AllowRequest("addr-1", "src-1", 5, 5, time.Hour, now)
	require.ErrorIs(t, err, emailauth.ErrUnavailable)

	_, err = s.AllowSubmit("src-1", 5, time.Hour, now)
	require.ErrorIs(t, err, emailauth.ErrUnavailable)
}

func TestPenalizingAnAddressWithNoOutstandingCodeIsHarmless(t *testing.T) {
	// A guess against an address that has no code has nothing to charge on the record. The
	// per-source budget above it is what makes that guess costly.
	s, _ := newSharedEmailStore(t)
	n, err := s.Penalize("addr-nothing", time.Minute)
	require.NoError(t, err)
	require.Zero(t, n)
}

func TestReapIsAServerSideNoOp(t *testing.T) {
	// Every key carries its own expiry, so there is nothing to scan for - and a SCAN across
	// a shared instance is exactly what this layer forbids.
	s, _ := newSharedEmailStore(t)
	require.NoError(t, s.Reap(time.Now()))
}

func TestAnAlreadyExpiredRecordStillLands(t *testing.T) {
	// A record whose deadline has already passed must not be written with a zero or
	// negative TTL, which some servers read as "no expiry" - that would make it permanent.
	require.Positive(t, emailTTLFor(emailauth.Record{Expires: time.Now().Add(-time.Hour)}))
	require.Positive(t, emailTTLFor(emailauth.Record{Expires: time.Now().Add(time.Hour)}))
}

func TestAFreshCodeGetsAFreshGuessingBudget(t *testing.T) {
	// Found by the pre-push audit. Penalize increments an `attempts` hash field for
	// atomicity; the replace script rewrote only `rec` and left that field behind, so a
	// newly mailed code inherited the previous code's spent budget. Someone guessing at
	// your address could have stopped you signing in at all - and the in-process store
	// resets on Put, so the two disagreed.
	s, _ := newSharedEmailStore(t)
	now := time.Now()
	require.NoError(t, s.Put(emailauth.Record{
		AddrHash: "addr-1", CodeHash: "old", Issued: now, Expires: now.Add(time.Minute),
	}))
	for i := 0; i < 5; i++ {
		_, err := s.Penalize("addr-1", time.Minute)
		require.NoError(t, err)
	}
	spent, _, err := s.ByAddress("addr-1")
	require.NoError(t, err)
	require.Equal(t, 5, spent.Attempts)

	// A new code is a new start.
	require.NoError(t, s.Put(emailauth.Record{
		AddrHash: "addr-1", CodeHash: "new", Issued: now, Expires: now.Add(time.Minute),
	}))
	fresh, _, err := s.ByAddress("addr-1")
	require.NoError(t, err)
	require.Zero(t, fresh.Attempts, "a fresh code carries a fresh budget")

	n, err := s.Penalize("addr-1", time.Minute)
	require.NoError(t, err)
	require.Equal(t, 1, n, "and counting restarts from one, not from six")
}
