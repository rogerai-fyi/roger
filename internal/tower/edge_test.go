package tower

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Failure and boundary paths. These are the ones that matter when something has already
// gone wrong: a corrupted directory must not be adopted as valid, and a half-written init
// must not leave key material behind.

func TestOpenRejectsCorruptState(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, stateFile), []byte("{not json"), 0o600))
	_, err := Open(dir)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unreadable")
}

func TestOpenRejectsAnUnsupportedRecordedMode(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, stateFile),
		[]byte(`{"mode":"hybrid","tower_id":"abc"}`), 0o600))
	_, err := Open(dir)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported mode")
}

// A directory that cannot be created must fail rather than silently serving from
// nowhere. Using a path under a regular file guarantees the failure.
func TestInitFailsWhenTheDirectoryCannotBeCreated(t *testing.T) {
	base := t.TempDir()
	blocker := filepath.Join(base, "afile")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))

	_, err := Init(filepath.Join(blocker, "tower"), ModeStandalone)
	require.Error(t, err)
}

// A failed init leaves nothing: no key material for a later run to adopt as real.
func TestCleanupRemovesAHalfWrittenDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "t")
	require.NoError(t, os.MkdirAll(dir, dirPerm))
	require.NoError(t, os.WriteFile(filepath.Join(dir, identityKey), []byte("key"), keyPerm))

	err := cleanupOnError(dir, os.ErrPermission)
	require.ErrorIs(t, err, os.ErrPermission, "the original error is preserved")
	_, statErr := os.Stat(dir)
	require.True(t, os.IsNotExist(statErr), "a failed init must leave no directory behind")
}

func TestStandaloneInitWritesAnOfflineRoot(t *testing.T) {
	dir := t.TempDir()
	_, err := Init(dir, ModeStandalone)
	require.NoError(t, err)
	fi, err := os.Stat(filepath.Join(dir, offlineRoot))
	require.NoError(t, err, "a standalone Tower is the root of its own network")
	require.Equal(t, os.FileMode(keyPerm), fi.Mode().Perm())
}

func TestJoinedInitWritesNoOfflineRoot(t *testing.T) {
	dir := t.TempDir()
	_, err := Init(dir, ModeJoined)
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(dir, offlineRoot))
	require.True(t, os.IsNotExist(err), "a joined Tower mints no trust root: Roger Core is the authority")
}

// --- redacted printing ----------------------------------------------------

func TestPrintRedactedShowsStandaloneAndStoragePaths(t *testing.T) {
	y := minimalStandalone + `
identity:
  dir: /var/lib/roger-tower/identity
standalone:
  offlineRootFile: /etc/tower/root.key
  trustPublicationFile: /etc/tower/trust.json
  settlementSignerFile: /etc/tower/settle.key
storage:
  urlFile: /run/secrets/db-url
`
	c, err := ParseConfig([]byte(y))
	require.NoError(t, err)
	out := c.PrintRedacted()

	for _, want := range []string{
		"/var/lib/roger-tower/identity",
		"/etc/tower/root.key",
		"/etc/tower/trust.json",
		"/etc/tower/settle.key",
		"/run/secrets/db-url",
	} {
		require.Contains(t, out, want, "the secret PATH is shown so an operator can see what is configured")
	}
	require.Equal(t, 4, strings.Count(out, "contents not read"),
		"every secret file is marked unread")
}

func TestPrintRedactedShowsJoinedCertificatePath(t *testing.T) {
	c, err := ParseConfig([]byte(minimalJoined + "  certificateFile: /etc/tower/tls.crt\n"))
	require.NoError(t, err)
	require.Contains(t, c.PrintRedacted(), "/etc/tower/tls.crt")
}

// --- doctor's unhappy paths ------------------------------------------------

func TestDoctorFlagsAJoinedConfigWithNoAuthority(t *testing.T) {
	// Bypass ParseConfig deliberately: doctor must not assume validation ran.
	c := &Config{Mode: ModeJoined}
	c.applyDefaults()
	rep := Doctor(c)
	require.False(t, rep.OK)
	require.Contains(t, strings.Join(rep.Problems, " "), "no authority")
	require.Contains(t, rep.String(), "doctor: NOT OK")
}

// Standalone reachability is structurally impossible, not merely validated away: even a
// Config hand-built with joined fields set reports no public authority and no
// advertisement, because both accessors are gated on mode.
func TestStandaloneCannotReportReachabilityEvenIfFieldsAreSet(t *testing.T) {
	c := &Config{Mode: ModeStandalone, PublicAdvertisement: true, Joined: &JoinedConfig{Authority: "https://x"}}
	c.applyDefaults()
	require.Empty(t, c.PublicAuthority())
	require.False(t, c.AdvertisesPublicly())

	rep := Doctor(c)
	require.False(t, rep.ReachesPublicNetwork, "a standalone Tower can never report public reachability")
	require.True(t, rep.OK)
}

func TestIsLoopbackCoversTheAcceptedForms(t *testing.T) {
	for _, ok := range []string{"127.0.0.1:1", "127.0.0.53:9", "[::1]:80", "localhost:7070"} {
		require.True(t, isLoopback(ok), "%s is loopback", ok)
	}
	for _, notOK := range []string{"0.0.0.0:1", "10.0.0.5:1", "[::]:80", "example.com:443"} {
		require.False(t, isLoopback(notOK), "%s is not loopback", notOK)
	}
}

// --- IO failure paths ------------------------------------------------------
//
// These matter because the failure mode is silent otherwise: a Tower that cannot write
// its lock or its state must refuse to run, not run without them.

func TestLockFailsWhenTheDirectoryIsNotWritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	dir := t.TempDir()
	_, err := Init(dir, ModeStandalone)
	require.NoError(t, err)
	st, err := Open(dir)
	require.NoError(t, err)

	require.NoError(t, os.Chmod(dir, 0o500)) // read+execute, no write
	t.Cleanup(func() { _ = os.Chmod(dir, dirPerm) })

	_, err = st.Lock()
	require.Error(t, err, "a Tower that cannot take its lock must refuse to run")
}

func TestInitFailsWhenKeysCannotBeWritten(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	parent := t.TempDir()
	require.NoError(t, os.Chmod(parent, 0o500))
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })

	_, err := Init(filepath.Join(parent, "t"), ModeStandalone)
	require.Error(t, err, "init must fail rather than produce a Tower with no identity")
}

func TestWriteFailsOnAReadOnlyDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	dir := t.TempDir()
	st := &State{Mode: ModeStandalone, TowerID: "abc", dir: dir}
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, dirPerm) })

	require.Error(t, st.write())
}

func TestRandomHexProducesDistinctValuesOfTheRightLength(t *testing.T) {
	a, err := randomHex(localIDBytes)
	require.NoError(t, err)
	b, err := randomHex(localIDBytes)
	require.NoError(t, err)
	require.Len(t, a, localIDBytes*2)
	require.NotEqual(t, a, b)
}
