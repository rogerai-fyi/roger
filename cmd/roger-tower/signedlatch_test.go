package main

// signedlatch_test.go is the spec for the durable half of the bearer-kill latch, which had NO
// tests at all: Add and path measured 0.0%, Load 38.5%. That distribution is exactly backwards
// from the risk. The in-memory latch is tested in towerhub; what this file adds is the property
// the whole mechanism was built for - the latch SURVIVING a restart - and the only way any test
// exercised it was as an incidental fixture. The write path that closes a stolen bearer's
// window for good had never executed.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The property the file exists for: a latch set before a "restart" (a second instance on the
// same directory - which is what a redeploy is to this code) is still set after it.
func TestTheLatchSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	first, err := newSignedLatch(dir, &bytes.Buffer{})
	require.NoError(t, err)
	require.NoError(t, first.Add("st-1"))
	require.NoError(t, first.Add("st-2"))

	second, err := newSignedLatch(dir, &bytes.Buffer{})
	require.NoError(t, err)
	got, err := second.Load()
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"st-1", "st-2"}, got,
		"a latch that does not survive a restart re-opens a stolen bearer on every redeploy")
}

func TestAddingTheSameStationTwiceRecordsItOnce(t *testing.T) {
	l, err := newSignedLatch(t.TempDir(), &bytes.Buffer{})
	require.NoError(t, err)
	require.NoError(t, l.Add("st-1"))
	require.NoError(t, l.Add("st-1"))
	got, err := l.Load()
	require.NoError(t, err)
	require.Equal(t, []string{"st-1"}, got)
}

// The filename is a hash of the id, and this is why: an id is attacker-influenced text, and a
// file-per-entry store whose names were the ids themselves would hand path traversal to
// whoever names a Station. The latch must record the id faithfully AND keep every file inside
// its own directory, whatever the id is spelled like.
func TestAHostileStationIdCannotEscapeTheDirectory(t *testing.T) {
	base := t.TempDir()
	l, err := newSignedLatch(base, &bytes.Buffer{})
	require.NoError(t, err)

	hostile := []string{"../../etc/cron.d/x", "st/../../x", "..", ".", "st\x00null", strings.Repeat("A", 4096)}
	for _, id := range hostile {
		require.NoError(t, l.Add(id), "id %q", id)
	}
	got, err := l.Load()
	require.NoError(t, err)
	require.ElementsMatch(t, hostile, got, "the id must be stored faithfully, however hostile")

	// Nothing may exist outside the latch directory. Walk the whole TempDir: the only
	// regular files anywhere under it live in signed-stations/.
	require.NoError(t, filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
		require.NoError(t, err)
		if !info.IsDir() {
			require.Equal(t, signedLatchDirName, filepath.Base(filepath.Dir(path)),
				"file %q escaped the latch directory", path)
		}
		return nil
	}))
}

// One corrupt entry must not take down the load: failing the whole Load would re-open the
// bearer for EVERY station on the tower, which is the direction that costs operators their
// queues. The empty file is the corruption a died-mid-write process actually leaves.
func TestOneCorruptEntryDoesNotReopenEveryOtherStation(t *testing.T) {
	dir := t.TempDir()
	l, err := newSignedLatch(dir, &bytes.Buffer{})
	require.NoError(t, err)
	require.NoError(t, l.Add("st-good"))

	latchDir := filepath.Join(dir, signedLatchDirName)
	require.NoError(t, os.WriteFile(filepath.Join(latchDir, "empty"), nil, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(latchDir, "blank"), []byte("  \n"), 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(latchDir, "a-directory"), 0o700))

	got, err := l.Load()
	require.NoError(t, err)
	require.Equal(t, []string{"st-good"}, got)
}

// A latch that cannot write must say so - once - and return the error. The warning names the
// consequence (the bearer comes back on restart) because that is what the operator has to
// act on; a line per poll would bury it.
func TestAFailingDiskWarnsOnceAndReturnsTheError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores permission bits, so the failure cannot be provoked")
	}
	dir := t.TempDir()
	var out bytes.Buffer
	l, err := newSignedLatch(dir, &out)
	require.NoError(t, err)
	require.NoError(t, os.Chmod(filepath.Join(dir, signedLatchDirName), 0o500))
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, signedLatchDirName), 0o700) })

	require.Error(t, l.Add("st-1"))
	require.Error(t, l.Add("st-2"))
	require.Equal(t, 1, strings.Count(out.String(), "WARNING"),
		"the operator needs to know once; a warning per request buries it")
	require.Contains(t, out.String(), "after this tower restarts",
		"the warning must name the consequence, not just the syscall")
}

// The nil-pointer-in-an-interface conversion. A nil *signedLatch stored directly into the
// interface is a NON-nil interface holding a nil pointer, and every call on the serving path
// would panic. latchStore is two lines that exist to make that unrepresentable - worth a test
// precisely because it reads as noise.
func TestANilLatchBecomesANilInterface(t *testing.T) {
	require.Nil(t, latchStore(nil))
	l, err := newSignedLatch(t.TempDir(), &bytes.Buffer{})
	require.NoError(t, err)
	require.NotNil(t, latchStore(l))
}

// An unreadable directory fails Load rather than answering "nobody signs": err is the one
// answer that does not silently re-admit every bearer.
func TestAnUnreadableDirectoryIsAnErrorNotAnEmptyAnswer(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores permission bits")
	}
	dir := t.TempDir()
	l, err := newSignedLatch(dir, &bytes.Buffer{})
	require.NoError(t, err)
	require.NoError(t, l.Add("st-1"))
	require.NoError(t, os.Chmod(filepath.Join(dir, signedLatchDirName), 0o000))
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, signedLatchDirName), 0o700) })

	_, err = l.Load()
	require.Error(t, err)
}
