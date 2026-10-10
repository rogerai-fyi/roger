package serve

// Contract: features/tower/standalone_consumer_plane.feature
//
// The binary's setup decisions - flags, bind posture, standalone-only mode, and the lock -
// all fail fast with a precise message, and a prepared plane actually serves over a real
// loopback socket. The dependency-graph guarantee is proven separately in structural_test.go.

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"rogerai.fm/roger/v6/internal/towerpin"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"net"
	"rogerai.fm/roger/v6/internal/tower"
)

func standaloneDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "tw")
	_, err := tower.Init(dir, tower.ModeStandalone)
	require.NoError(t, err)
	return dir
}

func TestPrepareRejectsBadSetup(t *testing.T) {
	// --dir required.
	_, err := Prepare([]string{}, io.Discard)
	require.ErrorContains(t, err, "--dir is required")

	// Bind posture: a public address without acknowledgement is refused before the dir is touched.
	_, err = Prepare([]string{"--dir", standaloneDir(t), "--bind", "0.0.0.0:8787"}, io.Discard)
	require.ErrorContains(t, err, "ALL interfaces")

	// A JOINED directory is refused: this Core-free binary cannot serve Core-admitted clients.
	joined := filepath.Join(t.TempDir(), "jtw")
	_, jerr := tower.Init(joined, tower.ModeJoined)
	require.NoError(t, jerr)
	_, err = Prepare([]string{"--dir", joined, "--bind", "127.0.0.1:0"}, io.Discard)
	require.Error(t, err, "a joined directory cannot host the standalone consumer plane")
}

func TestPreparedPlaneServesOverALoopbackSocket(t *testing.T) {
	p, err := Prepare([]string{"--dir", standaloneDir(t), "--bind", "127.0.0.1:0"}, io.Discard)
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() { done <- p.Serve() }()
	t.Cleanup(func() {
		_ = p.srv.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	})

	// The plane is listening: an unauthenticated /discover gets the uniform 401 over the wire.
	url := "http://" + p.ln.Addr().String() + "/discover"
	var resp *http.Response
	require.Eventually(t, func() bool {
		r, e := http.Get(url)
		if e != nil {
			return false
		}
		resp = r
		return true
	}, 2*time.Second, 20*time.Millisecond)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "an unsigned request is refused by the live plane")
}

func TestPrepareCoversItsFailurePaths(t *testing.T) {
	// A flag parse error.
	_, err := Prepare([]string{"--bogus-flag"}, io.Discard)
	require.Error(t, err)

	// An uninitialized data directory: Open fails (bind resolved fine first).
	_, err = Prepare([]string{"--dir", t.TempDir(), "--bind", "127.0.0.1:0"}, io.Discard)
	require.Error(t, err, "an uninitialized directory cannot be opened as a Tower")

	// The directory is already locked by another process (here, this test).
	dir := standaloneDir(t)
	st, err := tower.Open(dir)
	require.NoError(t, err)
	release, err := st.Lock()
	require.NoError(t, err)
	defer func() { _ = release() }()
	_, err = Prepare([]string{"--dir", dir, "--bind", "127.0.0.1:0"}, io.Discard)
	require.ErrorContains(t, err, "already owns")

	// The bind address is already in use: net.Listen fails and the lock is released.
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer taken.Close()
	dir2 := standaloneDir(t)
	_, err = Prepare([]string{"--dir", dir2, "--bind", taken.Addr().String()}, io.Discard)
	require.ErrorContains(t, err, "cannot listen")
	// The lock was released on the listen failure, so a second prepare on the same dir succeeds.
	p, err := Prepare([]string{"--dir", dir2, "--bind", "127.0.0.1:0"}, io.Discard)
	require.NoError(t, err, "a failed listen must have released the directory lock")
	_ = p.release()
	_ = p.ln.Close()
}

// An empty --bind is the plane's default (localplane.DefaultBind, loopback): the posture check
// passes, so the next thing to fail on an uninitialized directory is opening it, never the bind.
func TestPrepareEmptyBindIsTheLoopbackDefault(t *testing.T) {
	_, err := Prepare([]string{"--dir", t.TempDir(), "--bind", ""}, io.Discard)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "--bind", "an empty bind is the loopback default, not a posture refusal")
}

// Run installs the runtime guard BEFORE anything else, even when setup then fails.
func TestRunInstallsTheGuardFirst(t *testing.T) {
	installed := false
	old := installGuard
	installGuard = func(io.Writer) func() { installed = true; return func() {} }
	defer func() { installGuard = old }()
	err := Run([]string{}, io.Discard)
	require.ErrorContains(t, err, "--dir is required")
	require.True(t, installed, "the guard goes in before setup")
}

// features/join/tower_identity.feature: the Tower serves TLS with a certificate self-signed by its
// own tls.key on the same port, prints the fingerprint it serves under, and a client pinned to that
// fingerprint reaches every route over TLS.
func TestServeAnswersTLSUnderTheTowersOwnKey(t *testing.T) {
	dir := standaloneDir(t)
	var out strings.Builder
	p, err := Prepare([]string{"--dir", dir, "--bind", "127.0.0.1:0"}, &out)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- p.Serve() }()
	t.Cleanup(func() { _ = p.srv.Close(); <-done })
	st, err := tower.Open(dir)
	require.NoError(t, err)
	fp, err := st.JoinFingerprint()
	require.NoError(t, err)
	require.Contains(t, out.String(), "sha256:"+fp, "serve prints the fingerprint it serves under")

	resp, err := towerpin.Client(fp, 5*time.Second).Get("https://" + p.ln.Addr().String() + "/discover")
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "the plane answers over TLS")
	require.NotNil(t, resp.TLS)
}

// A directory whose TLS key is unreadable does not serve at all: never plain-only by accident.
func TestServeRefusesWithoutItsTLSKey(t *testing.T) {
	dir := standaloneDir(t)
	require.NoError(t, os.Remove(filepath.Join(dir, "tls.key")))
	_, err := Prepare([]string{"--dir", dir, "--bind", "127.0.0.1:0"}, io.Discard)
	require.Error(t, err)
}
