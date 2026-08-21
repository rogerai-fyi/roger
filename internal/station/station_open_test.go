package station

// station_open_test.go is the spec for LOADING a Station that already exists.
//
// Init is a mint, and it refuses a directory that already holds a Station for a good reason:
// re-minting would issue keys that no recorded attachment names. But until Open existed,
// minting was the only thing this package could do, so every caller that needed "this host's
// Station" on a second run got that refusal instead of the identity - and the one caller that
// treats attaching as best-effort turned it into permanent, silent absence from the relay
// fabric after the first run of `roger share` on a machine.

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/protocol"
)

// The property the relay join depends on: the same directory answers with the same identity
// every time, keys included, however many times a process asks.
func TestInitOrOpenReturnsTheSameIdentityEveryTime(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "st")

	first, err := InitOrOpen(dir)
	require.NoError(t, err)

	for i := 0; i < 3; i++ {
		again, oerr := InitOrOpen(dir)
		require.NoError(t, oerr, "run %d: a host that has attached before must be able to attach again", i+2)
		require.Equal(t, first.StationID, again.StationID)
		require.Equal(t, first.Assertion, again.Assertion)
		require.Equal(t, first.Session, again.Session)
		// The PRIVATE halves too - the public record is only worth anything if the loaded
		// Station can still sign and decrypt as itself.
		require.Equal(t, first.AssertionPub(), again.AssertionPub())
		require.Equal(t, first.SessionPriv(), again.SessionPriv())
		require.Equal(t, dir, again.Dir())
	}
}

// Open is the load half on its own: it refuses a directory that holds nothing rather than
// inventing an identity for it.
func TestOpenRefusesADirectoryWithNoStation(t *testing.T) {
	_, err := Open(filepath.Join(t.TempDir(), "st"))
	require.Error(t, err)
	require.True(t, os.IsNotExist(err), "a missing Station reads as missing: %v", err)
}

// THE LOUD-FAILURE CONTRACT. Every one of these directories is broken in a different way, and
// none of them may be answered by minting a fresh identity: a second Station beside a first
// one that attachments still name is unrecoverable, where an error message is not.
func TestOpenRefusesAPartialDirectoryRatherThanMintingANewIdentity(t *testing.T) {
	cases := map[string]func(t *testing.T, dir string){
		"the assertion key is gone": func(t *testing.T, dir string) {
			require.NoError(t, os.Remove(filepath.Join(dir, assertionKeyFile)))
		},
		"the session key is gone": func(t *testing.T, dir string) {
			require.NoError(t, os.Remove(filepath.Join(dir, sessionKeyFile)))
		},
		"the assertion key is truncated": func(t *testing.T, dir string) {
			require.NoError(t, os.WriteFile(filepath.Join(dir, assertionKeyFile), []byte("abcd"), 0o600))
		},
		"the session key is not hex": func(t *testing.T, dir string) {
			require.NoError(t, os.WriteFile(filepath.Join(dir, sessionKeyFile), []byte("not a key"), 0o600))
		},
		"the state file is not JSON": func(t *testing.T, dir string) {
			require.NoError(t, os.WriteFile(filepath.Join(dir, stateFile), []byte("{"), 0o600))
		},
		"the state file names no station": func(t *testing.T, dir string) {
			require.NoError(t, os.WriteFile(filepath.Join(dir, stateFile), []byte(`{"station_id":""}`), 0o600))
		},
		// The subtle one: every file is present and well-formed, but they are from two
		// different Stations - a half-finished copy between machines, or a restore of one
		// file and not the others. What this Station would sign with is not what anybody
		// recorded about it.
		"the recorded keys are somebody else's": func(t *testing.T, dir string) {
			other, err := Init(filepath.Join(t.TempDir(), "other"))
			require.NoError(t, err)
			raw, err := os.ReadFile(filepath.Join(other.Dir(), assertionKeyFile))
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(dir, assertionKeyFile), raw, 0o600))
		},
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "st")
			minted, err := Init(dir)
			require.NoError(t, err)
			breakIt(t, dir)

			_, err = Open(dir)
			require.Error(t, err, "a broken Station directory must be reported, not papered over")

			// And InitOrOpen must report it too - never fall through to Init.
			got, err := InitOrOpen(dir)
			require.Error(t, err)
			require.Nil(t, got)

			// Nothing was re-minted behind the error. Where the state file survived the
			// breakage it must still name the Station the operator has an attachment for;
			// where the breakage WAS the state file, it must not have been replaced by a
			// working record of some new identity.
			raw, rerr := os.ReadFile(filepath.Join(dir, stateFile))
			require.NoError(t, rerr)
			var onDisk struct {
				StationID string `json:"station_id"`
			}
			if json.Unmarshal(raw, &onDisk) == nil && onDisk.StationID != "" {
				require.Equal(t, minted.StationID, onDisk.StationID, "a new identity was minted over a broken one")
			}
		})
	}
}

// A loaded Station is a WORKING Station, not just a record of one: the keys it read off disk
// are the keys it signs and decrypts with.
func TestOpenLoadsKeysThatStillWork(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "st")
	minted, err := Init(dir)
	require.NoError(t, err)

	loaded, err := Open(dir)
	require.NoError(t, err)
	require.Equal(t, hex.EncodeToString(loaded.AssertionPub()), loaded.Assertion)
	require.Equal(t, hex.EncodeToString(loaded.SessionPub()), loaded.Session)
	require.Equal(t, minted.SessionPub(), loaded.SessionPub())
}

// THE STATION ID IS THE ASSERTION KEY'S, and a directory minted before that rule is restamped
// rather than refused.
//
// A random id is unguessable, which reads as the safer choice and is not: it is also
// unreclaimable. Core's reaper DELETES a terminal attachment thirty days after a revoke, and the
// id it frees has been public the whole time - it was the leftmost label of this Station's relay
// DNS name and the relay_name in every authorize answer it served. Anybody could then bind that
// name to a Station of their own, and this directory, which keeps its id forever with no re-mint
// path, would meet "this Station ID is already bound to another assertion key" on every
// re-attach from then on. Deriving the id from the key makes it claimable only by the machine
// that holds the key. See protocol.DeriveStationID.
func TestAFreshStationsIdIsDerivedFromItsAssertionKey(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "st")
	s, err := Init(dir)
	require.NoError(t, err)
	require.Equal(t, protocol.DeriveStationID(s.AssertionPub()), s.StationID)

	// Written down, not merely returned: the next process reads it from the file.
	again, err := Open(dir)
	require.NoError(t, err)
	require.Equal(t, s.StationID, again.StationID)

	// Two Stations are two identities, which is the other half of "the id is the key": nothing
	// about the derivation makes distinct keys share a name.
	other, err := Init(filepath.Join(t.TempDir(), "st"))
	require.NoError(t, err)
	require.NotEqual(t, s.StationID, other.StationID)
}

// A LEGACY DIRECTORY IS REPAIRED AND SAYS SO, which is the migration in one test.
//
// Open REFUSES a state file whose recorded public key disagrees with the key file, because that
// has lost information and nobody can say which half is the real Station. An id that is not the
// one its key derives has lost nothing at all - there is exactly one possible answer and it is
// computable from a key that is right here - so refusing would take a working provider off the
// network over a value we can recompute, with no remedy but deleting the identity. That is the
// precise outcome the derivation exists to prevent, so this repairs, and warns.
func TestOpenRestampsAStationIdMintedBeforeDerivation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "st")
	s, err := Init(dir)
	require.NoError(t, err)
	want := s.StationID

	// The shape the OLD Init wrote: "st-" + 12 random bytes of hex.
	const legacy = "st-0f1e2d3c4b5a69788796a5b4"
	raw, err := os.ReadFile(filepath.Join(dir, stateFile))
	require.NoError(t, err)
	var onDisk map[string]any
	require.NoError(t, json.Unmarshal(raw, &onDisk))
	onDisk["station_id"] = legacy
	rewritten, err := json.Marshal(onDisk)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, stateFile), rewritten, 0o600))

	reopened, err := Open(dir)
	require.NoError(t, err, "a legacy id must not be a refusal - there is no remedy but deletion")
	require.Equal(t, want, reopened.StationID)
	require.NotEmpty(t, reopened.Warnings)
	require.Contains(t, reopened.Warnings[len(reopened.Warnings)-1], legacy,
		"the warning names the id that was replaced, so an operator can match it to their records")

	// AND THE REPAIR IS DURABLE AND QUIET THE SECOND TIME. A restamp that only ever lived in
	// memory would warn on every start and re-do the work forever.
	third, err := Open(dir)
	require.NoError(t, err)
	require.Equal(t, want, third.StationID)
	require.Empty(t, third.Warnings, "a repaired directory has nothing left to report")
}
