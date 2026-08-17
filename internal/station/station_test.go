package station

// station_test.go is the spec for the Station's half of the joined network.
//
// A Station is the machine that actually serves work behind a Tower. It holds TWO keys and
// the Tower holds NEITHER: the assertion key signs its offers, and the secure-session key
// terminates its end of the inner channel. That separation is the whole reason a joined
// Tower can be untrusted - if the Tower could sign for a Station, "signed by the Station"
// would mean "signed by whoever is relaying", and every guarantee downstream of it would be
// worth nothing.
//
// Until this package existed there was no Station-side software at all: no way to generate
// those keys, and no way to produce a signed offer. A joined Tower therefore pushed a valid
// inventory of ZERO leaves, forever. Core's whole leaf-verification path - nineteen rejection
// rows, price bands, quarantine, origin fencing - had nothing to verify.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInitMintsTwoDifferentKeysAndKeepsThemPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "st")
	s, err := Init(dir)
	require.NoError(t, err)

	require.NotEmpty(t, s.StationID)
	require.Contains(t, s.StationID, "st-")
	require.NotEmpty(t, s.AssertionPub())
	require.NotEmpty(t, s.SessionPub())

	// TWO KEYS, NOT ONE USED TWICE. attach refuses an invitation naming the same key for
	// both, and it is right to: one key doing both jobs means compromising the channel
	// hands over the ability to sign offers as well.
	require.NotEqual(t, s.AssertionPub(), s.SessionPub())

	// Private material is 0600 and never in the readable state file.
	for _, name := range []string{assertionKeyFile, sessionKeyFile} {
		info, serr := os.Stat(filepath.Join(dir, name))
		require.NoError(t, serr)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm(), name)
	}
	raw, err := os.ReadFile(filepath.Join(dir, stateFile))
	require.NoError(t, err)
	require.NotContains(t, string(raw), "PRIVATE")
}

// A data directory is initialized ONCE. Re-initializing over a live Station would mint new
// keys, and the attachment Core recorded names the OLD ones - the Station would be
// cryptographically unable to prove it is itself, with no way back.
func TestInitRefusesToOverwriteAnExistingStation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "st")
	_, err := Init(dir)
	require.NoError(t, err)
	_, err = Init(dir)
	require.Error(t, err)
	require.Contains(t, err.Error(), "already")
}

func mustStation(t *testing.T) *Station {
	t.Helper()
	s, err := Init(filepath.Join(t.TempDir(), "st"))
	require.NoError(t, err)
	return s
}

func offerID(t *testing.T, raw []byte) string {
	t.Helper()
	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	return got["offer_id"].(string)
}

func TestInitReportsADirectoryItCannotCreate(t *testing.T) {
	parent := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(parent, "file"), []byte("x"), 0o600))
	// A path under a regular file can never be a directory.
	_, err := Init(filepath.Join(parent, "file", "st"))
	require.Error(t, err)
}

func TestInitRefusesADirectoryItCannotWriteTo(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the permission bits this test relies on")
	}
	dir := filepath.Join(t.TempDir(), "st")
	require.NoError(t, os.MkdirAll(dir, 0o500)) // exists, not writable
	_, err := Init(dir)
	require.Error(t, err)
}
