package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ROTATE-IN-PLACE and FORGET. Before these, a band's code could only be changed by revoking
// it and going private again - which mints a DIFFERENT band (new id, new dial, and a quota
// slot re-taken after the old one was surrendered) - and the revoked row it left behind
// could never be removed by anyone.

// THE LOAD-BEARING LOCK. bands[id] is what the dashboard reads; byHash is what RESOLVE
// reads. A rotation that updated the row and left the old hash in the index would leave the
// OLD CODE STILL WORKING while telling the operator it had been replaced - a security
// promise silently not kept, which is worse than not shipping rotation at all.
func TestRotateBurnsTheOldCodeAndArmsTheNew(t *testing.T) {
	eachBandStore(t, func(t *testing.T, s Store) {
		b := bandFixture(t, s)

		updated, ok, err := s.RotateBandCode(b.ID, b.Owner, "hash_rotated", "147.520 MHz · ••••-••••")
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, "hash_rotated", updated.CodeHash)

		// The OLD code must no longer resolve. This is the whole point of the operation.
		_, found, err := s.BandByCodeHash("hash_1")
		require.NoError(t, err)
		require.False(t, found, "the OLD code still resolves - the rotation did not burn it")

		// The NEW code must resolve to the SAME band.
		got, found, err := s.BandByCodeHash("hash_rotated")
		require.NoError(t, err)
		require.True(t, found, "the new code does not resolve")
		require.Equal(t, b.ID, got.ID, "rotation must keep the band's identity")
	})
}

// Everything except the key survives: that is what "in place" means, and it is the reason
// to rotate rather than revoke and re-mint.
func TestRotateKeepsIdentityBindingAndSlot(t *testing.T) {
	eachBandStore(t, func(t *testing.T, s Store) {
		b := bandFixture(t, s)
		label := "family"
		_, _, err := s.UpdateBand(b.ID, b.Owner, BandPatch{Label: &label})
		require.NoError(t, err)

		before, err := s.CountActiveBands(b.Owner, time.Unix(2000, 0))
		require.NoError(t, err)

		updated, ok, err := s.RotateBandCode(b.ID, b.Owner, "hash_rotated", "147.520 MHz · ••••-••••")
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, b.ID, updated.ID, "the band id must survive a rotation")
		require.Equal(t, b.NodeID, updated.NodeID, "the node binding must survive a rotation")
		require.Equal(t, "family", updated.Label, "the label must survive a rotation")

		after, err := s.CountActiveBands(b.Owner, time.Unix(2000, 0))
		require.NoError(t, err)
		require.Equal(t, before, after, "a rotation must not consume or free a quota slot")

		// And the node lookup still finds it, so a re-register still binds to this band.
		got, found, err := s.BandByNode(b.NodeID)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, b.ID, got.ID)
	})
}

// Owner scoping: another owner's band answers exactly like one that does not exist, so this
// can never be used to burn someone else's code or to enumerate band ids.
func TestRotateIsOwnerScoped(t *testing.T) {
	eachBandStore(t, func(t *testing.T, s Store) {
		b := bandFixture(t, s)
		_, ok, err := s.RotateBandCode(b.ID, "someone_else", "hash_evil", "1 MHz · ••••-••••")
		require.NoError(t, err)
		require.False(t, ok, "another owner rotated a band that is not theirs")

		// The original code must be untouched.
		got, found, err := s.BandByCodeHash("hash_1")
		require.NoError(t, err)
		require.True(t, found, "a refused rotation still burnt the owner's code")
		require.Equal(t, b.ID, got.ID)
	})
}

// Revoke is FINAL and surrendered the quota slot. Rotating a revoked band would resurrect a
// burnt band under a working code and hand back a slot the owner gave up.
func TestRotateRefusesARevokedBand(t *testing.T) {
	eachBandStore(t, func(t *testing.T, s Store) {
		b := bandFixture(t, s)
		revoked, err := s.SetBandRevoked(b.ID, b.Owner, true)
		require.NoError(t, err)
		require.True(t, revoked)

		_, ok, err := s.RotateBandCode(b.ID, b.Owner, "hash_zombie", "147.520 MHz · ••••-••••")
		require.NoError(t, err)
		require.False(t, ok, "a revoked band was rotated back to life")

		_, found, err := s.BandByCodeHash("hash_zombie")
		require.NoError(t, err)
		require.False(t, found, "a revoked band's rotation armed a working code")
	})
}

// FORGET deletes a revoked row for good - the only way to clear the dead history that
// otherwise accumulates around a live band forever.
func TestForgetRemovesARevokedRow(t *testing.T) {
	eachBandStore(t, func(t *testing.T, s Store) {
		b := bandFixture(t, s)
		_, err := s.SetBandRevoked(b.ID, b.Owner, true)
		require.NoError(t, err)

		ok, err := s.ForgetBand(b.ID, b.Owner)
		require.NoError(t, err)
		require.True(t, ok)

		list, err := s.BandsByOwner(b.Owner)
		require.NoError(t, err)
		require.Empty(t, list, "the forgotten row is still in the owner's list")
	})
}

// NEGATIVE HALF, and the one that matters: a LIVE band must never be deletable. Dropping a
// live row removes its code from the resolve index while every consumer holding that code
// carries on believing it works, and frees a quota slot with no confirm anywhere.
func TestForgetRefusesALiveBand(t *testing.T) {
	eachBandStore(t, func(t *testing.T, s Store) {
		b := bandFixture(t, s)
		ok, err := s.ForgetBand(b.ID, b.Owner)
		require.NoError(t, err)
		require.False(t, ok, "a LIVE band was deleted - its consumers are stranded with no revoke")

		got, found, err := s.BandByCodeHash("hash_1")
		require.NoError(t, err)
		require.True(t, found, "a refused forget still dropped the code from the resolve index")
		require.Equal(t, b.ID, got.ID)
	})
}

func TestForgetIsOwnerScoped(t *testing.T) {
	eachBandStore(t, func(t *testing.T, s Store) {
		b := bandFixture(t, s)
		_, err := s.SetBandRevoked(b.ID, b.Owner, true)
		require.NoError(t, err)

		ok, err := s.ForgetBand(b.ID, "someone_else")
		require.NoError(t, err)
		require.False(t, ok, "another owner deleted a band that is not theirs")

		list, err := s.BandsByOwner(b.Owner)
		require.NoError(t, err)
		require.Len(t, list, 1, "a refused forget still removed the row")
	})
}
