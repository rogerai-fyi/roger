package main

// The shared device-login store, exercised against a real Redis-protocol server.
//
// internal/deviceauth proves the Store CONTRACT against its in-process implementation;
// this proves the shared one actually satisfies it, including the parts that only exist
// here - the CAS script, the server-side expiry, and the two-instance case that is the
// whole reason the seam exists.

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/deviceauth"
)

func newSharedDeviceStore(t *testing.T) (deviceauth.Store, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	vs, err := newValkeyStore("redis://" + mr.Addr())
	require.NoError(t, err)
	t.Cleanup(func() { _ = vs.Close() })
	ds := newValkeyDeviceStore(vs)
	require.NotNil(t, ds, "a wired shared store must yield a device store")
	return ds, mr
}

func sharedRecord(now time.Time) deviceauth.Record {
	return deviceauth.Record{
		DevHash:   "aa11",
		UserHash:  "bb22",
		BoundKey:  "pubkey-A",
		Status:    deviceauth.StatusPending,
		Requested: now,
		Expires:   now.Add(10 * time.Minute),
		Interval:  5 * time.Second,
	}
}

func TestSharedDeviceStoreRoundTrip(t *testing.T) {
	s, _ := newSharedDeviceStore(t)
	rec := sharedRecord(time.Now())
	require.NoError(t, s.Create(rec))

	byDev, ok, err := s.ByDevice(rec.DevHash)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "pubkey-A", byDev.BoundKey)
	require.Equal(t, deviceauth.StatusPending, byDev.Status)

	byUser, ok, err := s.ByUser(rec.UserHash)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, rec.DevHash, byUser.DevHash)
}

func TestSharedDeviceStoreMissIsNotAnError(t *testing.T) {
	s, _ := newSharedDeviceStore(t)
	_, ok, err := s.ByDevice("nope")
	require.NoError(t, err)
	require.False(t, ok)

	_, ok, err = s.ByUser("nope")
	require.NoError(t, err)
	require.False(t, ok)
}

func TestSharedDeviceStoreCASRejectsAStaleRevision(t *testing.T) {
	// This is the property that makes "a code is consumed once across the deployment"
	// true: two instances acting on the same read, exactly one wins.
	s, _ := newSharedDeviceStore(t)
	rec := sharedRecord(time.Now())
	require.NoError(t, s.Create(rec))

	read, _, err := s.ByDevice(rec.DevHash)
	require.NoError(t, err)
	stale := read

	read.Status = deviceauth.StatusApproved
	read.Account = "acct-1"
	won, err := s.CAS(read)
	require.NoError(t, err)
	require.True(t, won)

	stale.Status = deviceauth.StatusDenied
	won, err = s.CAS(stale)
	require.NoError(t, err)
	require.False(t, won, "a write against a superseded revision is refused")

	after, _, err := s.ByDevice(rec.DevHash)
	require.NoError(t, err)
	require.Equal(t, deviceauth.StatusApproved, after.Status)
	require.Equal(t, "acct-1", after.Account)
}

func TestSharedDeviceStoreCreateDoesNotResetALoginUnderWay(t *testing.T) {
	s, _ := newSharedDeviceStore(t)
	rec := sharedRecord(time.Now())
	require.NoError(t, s.Create(rec))

	read, _, err := s.ByDevice(rec.DevHash)
	require.NoError(t, err)
	read.Status = deviceauth.StatusApproved
	won, err := s.CAS(read)
	require.NoError(t, err)
	require.True(t, won)

	// A replayed Create must not roll the login back to pending.
	require.NoError(t, s.Create(rec))
	after, _, err := s.ByDevice(rec.DevHash)
	require.NoError(t, err)
	require.Equal(t, deviceauth.StatusApproved, after.Status)
}

func TestSharedDeviceStoreCASOnAnAbsentRecordReportsNoWrite(t *testing.T) {
	s, _ := newSharedDeviceStore(t)
	rec := sharedRecord(time.Now())
	require.NoError(t, s.Create(rec))
	read, _, err := s.ByDevice(rec.DevHash)
	require.NoError(t, err)

	require.NoError(t, s.Delete(rec.DevHash))

	won, err := s.CAS(read)
	require.NoError(t, err)
	require.False(t, won)

	_, ok, err := s.ByUser(rec.UserHash)
	require.NoError(t, err)
	require.False(t, ok, "Delete removes the user index too")
}

func TestSharedDeviceStoreBudgetIsPerSubmitter(t *testing.T) {
	s, _ := newSharedDeviceStore(t)
	for i := 1; i <= 3; i++ {
		n, err := s.Penalize("attacker", time.Minute)
		require.NoError(t, err)
		require.Equal(t, i, n)
	}
	spent, err := s.Budget("attacker")
	require.NoError(t, err)
	require.Equal(t, 3, spent)

	other, err := s.Budget("innocent")
	require.NoError(t, err)
	require.Zero(t, other)
}

func TestSharedDeviceStoreExpiresRecordsServerSide(t *testing.T) {
	// Expiry is armed as a TTL on the key itself, so a record cannot outlive the login it
	// describes even if no reaper ever runs.
	s, mr := newSharedDeviceStore(t)
	rec := sharedRecord(time.Now())
	rec.Expires = time.Now().Add(time.Minute)
	require.NoError(t, s.Create(rec))

	mr.FastForward(2 * time.Minute)

	_, ok, err := s.ByDevice(rec.DevHash)
	require.NoError(t, err)
	require.False(t, ok)

	_, ok, err = s.ByUser(rec.UserHash)
	require.NoError(t, err)
	require.False(t, ok)
}

func TestSharedDeviceStoreReportsAnOutageRatherThanAMiss(t *testing.T) {
	// The distinction the whole design rests on: a store that cannot answer must not look
	// like a code that does not exist, or a legitimate CLI is told its good code is bad.
	s, mr := newSharedDeviceStore(t)
	rec := sharedRecord(time.Now())
	require.NoError(t, s.Create(rec))

	mr.Close() // the backend goes away

	_, ok, err := s.ByDevice(rec.DevHash)
	require.Error(t, err, "an unreachable store is an error, never a clean miss")
	require.False(t, ok)

	require.Error(t, s.Create(sharedRecord(time.Now())))
}

func TestTwoBrokerInstancesCompleteOneDeviceLogin(t *testing.T) {
	// The case that is simply broken without a shared store: the CLI polls one instance
	// while the human approves on another, so the approval lands in one process's map and
	// the poll reads another's. Here both flows are backed by the same server.
	s, _ := newSharedDeviceStore(t)
	instanceA := deviceauth.NewWithStore(deviceauth.Config{}, s)
	instanceB := deviceauth.NewWithStore(deviceauth.Config{}, s)

	pending, err := instanceA.Start("pubkey-A")
	require.NoError(t, err)

	info, ok := instanceB.Describe(pending.UserCode, "acct-1")
	require.True(t, ok, "the approval screen finds a login the other instance issued")
	require.Equal(t, pending.UserCode, info.UserCode)

	require.NoError(t, instanceB.Approve(pending.UserCode, "acct-1"))

	res, err := instanceA.Poll(pending.DeviceCode, "pubkey-A")
	require.NoError(t, err)
	require.Equal(t, deviceauth.StatusApproved, res.Status)
	require.Equal(t, "acct-1", res.Account)
	require.Equal(t, "pubkey-A", res.BoundKey, "the key bound is the key recorded at issue")

	// And the code is spent: the second instance cannot redeem it again.
	_, err = instanceB.Poll(pending.DeviceCode, "pubkey-A")
	require.Error(t, err)
}

func TestTheGuessingBudgetIsSharedAcrossInstances(t *testing.T) {
	s, _ := newSharedDeviceStore(t)
	instanceA := deviceauth.NewWithStore(deviceauth.Config{MaxWrongCodes: 4}, s)
	instanceB := deviceauth.NewWithStore(deviceauth.Config{MaxWrongCodes: 4}, s)

	for i := 0; i < 2; i++ {
		require.Error(t, instanceA.Approve("NOTACODE", "attacker"))
		require.Error(t, instanceB.Approve("NOTACODE", "attacker"))
	}

	pending, err := instanceA.Start("pubkey-A")
	require.NoError(t, err)
	require.Error(t, instanceB.Approve(pending.UserCode, "attacker"),
		"spreading guesses across instances does not multiply the allowance")
	require.NoError(t, instanceA.Approve(pending.UserCode, "innocent"),
		"and one submitter's spent budget never locks another out")
}

func TestNoSharedStoreKeepsTheInProcessDefault(t *testing.T) {
	// A single-instance deployment must keep working with no configuration change and no
	// new dependency.
	require.Nil(t, newValkeyDeviceStore(nil))
	require.Nil(t, newValkeyDeviceStore(&memStore{}))
	require.NotNil(t, newDeviceFlowWithStore(nil), "a nil store still yields a usable flow")
}
