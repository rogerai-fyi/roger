package station

// station_integrity_test.go is the spec for what Open is allowed to accept.
//
// Open's doc comment promises "a state file whose recorded public halves do not match the private
// keys beside it" fails loudly. It did not - it could not - because of how Go's ed25519 works.

import (
	"crypto/ed25519"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// A CORRUPT SEED PASSED THE CROSS-CHECK AND THEN SIGNED UNVERIFIABLE RECEIPTS.
//
// ed25519.PrivateKey.Public() returns the private key's trailing 32 bytes VERBATIM - it does not
// re-derive from the seed. So comparing hex(AssertionPub()) against the state file proved only
// that two files agreed about the tail, and said nothing about the half that actually signs. A
// station whose seed was corrupted by a truncated write or a partial restore loaded clean, served
// real work, and produced receipts that verify under no key anybody recorded.
func TestOpenRefusesAKeyWhoseSeedDoesNotMatchItsPublicHalf(t *testing.T) {
	dir := t.TempDir()
	s, err := Init(dir)
	require.NoError(t, err)
	recorded := s.AssertionPub()

	// Corrupt the SEED half only, exactly as a bad write would: the trailing public half and the
	// state file still agree with each other, which is all the old check ever asked.
	path := filepath.Join(dir, assertionKeyFile)
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	key, err := hex.DecodeString(string(raw))
	require.NoError(t, err)
	key[0] ^= 0xff
	require.NoError(t, os.WriteFile(path, []byte(hex.EncodeToString(key)), 0o600))

	// The property that made this dangerous, stated so the test explains itself: the corrupt key
	// still "agrees" with the recording, and still signs.
	corrupt := ed25519.PrivateKey(key)
	require.Equal(t, []byte(recorded), []byte(corrupt.Public().(ed25519.PublicKey)),
		"the cross-check Open used to make cannot see this corruption")
	require.False(t, ed25519.Verify(recorded, []byte("a receipt"), ed25519.Sign(corrupt, []byte("a receipt"))),
		"and the signatures it produces verify under nothing")

	_, oerr := Open(dir)
	require.Error(t, oerr, "a station with a corrupt seed loaded without complaint")
	require.Contains(t, oerr.Error(), "internally inconsistent")
}

// The honest directory still opens, and still signs verifiably. A check that refuses everything
// is not a check.
func TestOpenAcceptsAnIntactStation(t *testing.T) {
	dir := t.TempDir()
	minted, err := Init(dir)
	require.NoError(t, err)
	loaded, err := Open(dir)
	require.NoError(t, err)
	require.Equal(t, minted.StationID, loaded.StationID)
	msg := []byte("a receipt")
	require.True(t, ed25519.Verify(loaded.AssertionPub(), msg, ed25519.Sign(loaded.assertionPriv, msg)))
	require.Empty(t, loaded.Warnings, "a directory Init made needs no repair")
}

// A world-readable station directory loaded without a word. Init writes 0700/0600, but MkdirAll
// does not change an existing directory's mode, and a restore or a copy between machines carries
// whatever mode it came with. These keys sign the receipts an operator is paid against.
func TestOpenTightensAndReportsLoosePermissions(t *testing.T) {
	dir := t.TempDir()
	_, err := Init(dir)
	require.NoError(t, err)
	require.NoError(t, os.Chmod(dir, 0o755))
	require.NoError(t, os.Chmod(filepath.Join(dir, assertionKeyFile), 0o644))

	s, err := Open(dir)
	require.NoError(t, err, "a permissive mode is repaired, not refused - refusing takes a working provider off the network")
	require.NotEmpty(t, s.Warnings, "a world-readable Station key was loaded silently")

	// Repaired on disk, so the next run is clean and the operator is told once rather than every
	// restart.
	di, err := os.Stat(dir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), di.Mode().Perm())
	ki, err := os.Stat(filepath.Join(dir, assertionKeyFile))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), ki.Mode().Perm())

	again, err := Open(dir)
	require.NoError(t, err)
	require.Empty(t, again.Warnings)
}
