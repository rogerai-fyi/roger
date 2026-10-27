package main

// coldbranches_test.go pins the branches of this command that were deterministic and never
// driven: the hub's refusals and warnings at start-up, the spool and latch on a disk that
// misbehaves, and the flag mistakes `serve` reports before it touches a directory. None of
// them depends on timing; each is asserted as the sentence or the state an operator sees.

import (
	"bytes"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// --- the hub's start-up refusals and warnings ----------------------------------------------

// A TLS posture with nothing behind it is refused before the hub fetches anything: serving
// plaintext under a pin Core is about to publish is the trap this check exists to remove.
func TestTheHubRefusesATLSPostureWithNoCertificate(t *testing.T) {
	st := servingTower(t)
	stop := make(chan struct{})
	defer close(stop)
	_, err := runHubInBackground(st, hubOptions{Addr: "127.0.0.1:0", TLS: true}, &bytes.Buffer{}, stop)
	require.ErrorContains(t, err, "refusing to serve plaintext under a TLS posture")
}

// An address that cannot be bound is the hub's error, named with the address.
func TestTheHubNamesAnAddressItCannotBind(t *testing.T) {
	core := newCoreStub(t)
	core.answerDispatchKey(t)
	core.answerHubNodes(`{"nodes":[]}`)
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = taken.Close() })

	st := servingTower(t)
	stop := make(chan struct{})
	defer close(stop)
	_, err = runHubInBackground(st, hubOptions{Addr: taken.Addr().String()}, &bytes.Buffer{}, stop)
	require.ErrorContains(t, err, "cannot listen on "+taken.Addr().String())
}

// A DISK THAT WILL NOT TAKE THE SPOOL OR THE LATCH DEGRADES LOUDLY, NOT SILENTLY. Both
// directories are blocked by a plain file, so neither can be created: the hub still serves,
// and the operator is told exactly what they have lost - receipts that survive only in memory,
// and a bearer latch that reopens on every restart.
func TestAHubWithoutItsSpoolOrLatchServesAndSaysSo(t *testing.T) {
	core := newCoreStub(t)
	core.answerDispatchKey(t)
	core.answerHubNodes(`{"nodes":[]}`)
	st := servingTower(t)
	for _, name := range []string{spoolDirName, signedLatchDirName} {
		require.NoError(t, os.WriteFile(filepath.Join(st.Dir(), name), []byte("in the way"), 0o600))
	}

	out := &syncBuffer{}
	stop := make(chan struct{})
	wait, err := runHubInBackground(st, hubOptions{Addr: "127.0.0.1:0"}, out, stop)
	require.NoError(t, err, "a hub without its spool or latch still serves")
	require.Eventually(t, func() bool { return strings.Contains(out.String(), "hub: listening on ") },
		5*time.Second, 10*time.Millisecond)
	close(stop)
	wait()
	require.Contains(t, out.String(), "settle spool unavailable")
	require.Contains(t, out.String(), "receipts queued for Core survive only in memory")
	require.Contains(t, out.String(), "signed-station latch unavailable")
	require.Contains(t, out.String(), "work again after every restart")
}

// --- the spool and the latch on a disk that misbehaves -----------------------------------

// The spool reports a write it could not make, loads nothing from a directory that is gone,
// and skips what it cannot read - a stray subdirectory and a dangling symlink - without
// losing the entries beside them.
func TestTheSpoolSkipsWhatItCannotReadAndReportsWhatItCannotWrite(t *testing.T) {
	base := t.TempDir()
	spool, err := newSettleSpool(base)
	require.NoError(t, err)
	live := pendingSettle{stationID: "st-1", attemptID: "att-live", receipt: []byte("r"),
		deadline: time.Now().Add(time.Hour)}
	require.NoError(t, spool.put(live))
	require.NoError(t, os.Mkdir(filepath.Join(spool.dir, "a-subdirectory"), 0o700))
	require.NoError(t, os.Symlink(filepath.Join(base, "nowhere"), filepath.Join(spool.dir, "dangling.json")))

	got := spool.load(time.Now())
	require.Len(t, got, 1, "the live entry survives its unreadable neighbours")
	require.Equal(t, "att-live", got[0].attemptID)

	require.NoError(t, os.RemoveAll(spool.dir))
	require.Error(t, spool.put(live), "a write into a directory that is gone is reported, not swallowed")
	require.Empty(t, spool.load(time.Now()), "a directory that is gone holds nothing")
}

// A latch directory that cannot be created is the caller's to decide about, and a latch entry
// that cannot be read is skipped rather than failing every other Station's latch.
func TestTheLatchReportsABlockedDirectoryAndSkipsAnUnreadableEntry(t *testing.T) {
	base := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(base, signedLatchDirName), []byte("in the way"), 0o600))
	_, err := newSignedLatch(base, &bytes.Buffer{})
	require.Error(t, err)

	base = t.TempDir()
	l, err := newSignedLatch(base, &bytes.Buffer{})
	require.NoError(t, err)
	require.NoError(t, l.Add("st-1"))
	require.NoError(t, os.Symlink(filepath.Join(base, "nowhere"), filepath.Join(l.dir, "dangling")))
	got, err := l.Load()
	require.NoError(t, err)
	require.Equal(t, []string{"st-1"}, got)
}

// --- `serve`'s flag mistakes, reported as flag mistakes ------------------------------------

// Half a TLS key pair, TLS with no hub to protect, and an advertised data plane with no hub
// serving it are each refused before any directory is touched - as the flag mistake they are.
func TestServeRefusesHalfConfiguredDataPlanesBeforeTouchingTheDirectory(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--hub-tls-cert", "c.pem"}, "must be given together"},
		{[]string{"--hub-tls"}, "no hub listener to protect"},
		{[]string{"--relay-public", "127.0.0.1:8444"}, "no --hub is serving one"},
	} {
		var out bytes.Buffer
		err := cmdServe(c.args, &out)
		require.ErrorContains(t, err, c.want, "%v", c.args)
	}
}

// --- `init` and `revoke` on directories that are not there -------------------------------

// A data directory that cannot be created, and one that is not a Tower, are each the error
// the filesystem or the store gives - not a success line over nothing.
func TestInitAndRevokeReportADirectoryThatIsNotThere(t *testing.T) {
	file := filepath.Join(t.TempDir(), "a-file")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o600))
	var out bytes.Buffer
	require.Error(t, run([]string{"init", "--dir", filepath.Join(file, "tw"), "--mode", "joined"}, &out))
	_, err := runCLI(t, "revoke", "--dir", filepath.Join(t.TempDir(), "missing"), "--yes")
	require.Error(t, err)
}

// A revoke Core refuses reaches the operator as Core's sentence; nothing local changes.
func TestARevokeCoreRefusesIsReportedAsCoresSentence(t *testing.T) {
	core := newCoreStub(t)
	dir := joinedRegisteredTower(t)
	core.reply["/tower/self/lifecycle"] = func(w http.ResponseWriter, _ int) bool {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":{"message":"this Tower still holds a live link"}}`))
		return true
	}
	_, err := runCLI(t, "revoke", "--dir", dir, "--yes")
	require.ErrorContains(t, err, "live link")
}

// Earnings Core reports without a unit are shown in credits, the ledger's own unit.
func TestEarningsWithoutAUnitAreShownInCredits(t *testing.T) {
	core := newCoreStub(t)
	core.reply["/tower/earnings/owed"] = func(w http.ResponseWriter, _ int) bool {
		_, _ = w.Write([]byte(`{"owner":"ab12","held":0,"payable":1.5,"paid":0}`))
		return true
	}
	out, err := runCLI(t, "earnings")
	require.NoError(t, err)
	require.Contains(t, out, "(credits)")
}
