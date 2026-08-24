package main

// Contract: features/tower/standalone_consumer_plane.feature
//
// The binary's setup decisions - flags, bind posture, standalone-only mode, and the lock -
// all fail fast with a precise message, and a prepared plane actually serves over a real
// loopback socket. The dependency-graph guarantee is proven separately in structural_test.go.

import (
	"bytes"
	"io"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"net"
	"os"
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
	_, err := prepare([]string{}, io.Discard)
	require.ErrorContains(t, err, "--dir is required")

	// Bind posture: a public address without acknowledgement is refused before the dir is touched.
	_, err = prepare([]string{"--dir", standaloneDir(t), "--bind", "0.0.0.0:8787"}, io.Discard)
	require.ErrorContains(t, err, "ALL interfaces")

	// A JOINED directory is refused: this Core-free binary cannot serve Core-admitted clients.
	joined := filepath.Join(t.TempDir(), "jtw")
	_, jerr := tower.Init(joined, tower.ModeJoined)
	require.NoError(t, jerr)
	_, err = prepare([]string{"--dir", joined, "--bind", "127.0.0.1:0"}, io.Discard)
	require.Error(t, err, "a joined directory cannot host the standalone consumer plane")
}

func TestPreparedPlaneServesOverALoopbackSocket(t *testing.T) {
	p, err := prepare([]string{"--dir", standaloneDir(t), "--bind", "127.0.0.1:0"}, io.Discard)
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() { done <- p.serve() }()
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

func TestRunReturnsPrepareErrors(t *testing.T) {
	// run surfaces a setup error rather than starting to serve.
	err := run([]string{}, io.Discard)
	require.ErrorContains(t, err, "--dir is required")
}

func TestPrepareCoversItsFailurePaths(t *testing.T) {
	// A flag parse error.
	_, err := prepare([]string{"--bogus-flag"}, io.Discard)
	require.Error(t, err)

	// An uninitialized data directory: Open fails (bind resolved fine first).
	_, err = prepare([]string{"--dir", t.TempDir(), "--bind", "127.0.0.1:0"}, io.Discard)
	require.Error(t, err, "an uninitialized directory cannot be opened as a Tower")

	// The directory is already locked by another process (here, this test).
	dir := standaloneDir(t)
	st, err := tower.Open(dir)
	require.NoError(t, err)
	release, err := st.Lock()
	require.NoError(t, err)
	defer func() { _ = release() }()
	_, err = prepare([]string{"--dir", dir, "--bind", "127.0.0.1:0"}, io.Discard)
	require.ErrorContains(t, err, "already owns")

	// The bind address is already in use: net.Listen fails and the lock is released.
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer taken.Close()
	dir2 := standaloneDir(t)
	_, err = prepare([]string{"--dir", dir2, "--bind", taken.Addr().String()}, io.Discard)
	require.ErrorContains(t, err, "cannot listen")
	// The lock was released on the listen failure, so a second prepare on the same dir succeeds.
	p, err := prepare([]string{"--dir", dir2, "--bind", "127.0.0.1:0"}, io.Discard)
	require.NoError(t, err, "a failed listen must have released the directory lock")
	_ = p.release()
	_ = p.ln.Close()
}

func TestMainExitsNonZeroOnSetupError(t *testing.T) {
	oldArgs, oldExit := os.Args, osExit
	defer func() { os.Args, osExit = oldArgs, oldExit }()
	code := -1
	osExit = func(c int) { code = c }
	os.Args = []string{"roger-tower-local"} // no --dir
	main()
	require.Equal(t, 1, code, "main exits non-zero when setup fails")
}

func TestVersionCommandPrintsTheBuild(t *testing.T) {
	for _, arg := range []string{"version", "--version"} {
		var out bytes.Buffer
		require.NoError(t, run([]string{arg}, &out))
		require.Equal(t, version+"\n", out.String())
	}
}
