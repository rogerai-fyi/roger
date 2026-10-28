//go:build unix

package toweradmin

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v7/internal/tower"
)

// Live E2E 2026-10-28: every administration command checks Running first, which connects and hangs
// up without a request, and serve logged each one as "refused an unreadable request". A connection
// that sends nothing is a probe and is not logged; anything that is not a request still is.
func TestAnEmptyConnectionIsASilentProbe(t *testing.T) {
	for _, tc := range []struct {
		name, send string
		logged     bool
	}{
		{name: "Running's probe: connect, send nothing, hang up", send: "", logged: false},
		{name: "only whitespace, then hang up", send: " \n\t", logged: false},
		{name: "not json", send: "not json", logged: true},
		{name: "a stray closing brace", send: "}\n", logged: true},
		{name: "a request cut off midway", send: `{"op":`, logged: true},
		{name: "a request cut off inside a string", send: `{"op":"fingerpr`, logged: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := shortDir(t)
			st, err := tower.Init(dir, tower.ModeStandalone)
			require.NoError(t, err)
			ln, err := Listen(dir)
			require.NoError(t, err)
			t.Cleanup(func() { _ = ln.Close(); Remove(dir) })
			var mu sync.Mutex
			var logged []string
			done := make(chan struct{})
			go func() {
				defer close(done)
				c, err := ln.Accept()
				if err != nil {
					return
				}
				handle(c, st, nil, func(f string, a ...any) { mu.Lock(); logged = append(logged, f); mu.Unlock() })
			}()
			conn, err := net.Dial("unix", Path(dir))
			require.NoError(t, err)
			if tc.send != "" {
				_, err = conn.Write([]byte(tc.send))
				require.NoError(t, err)
			}
			require.NoError(t, conn.(*net.UnixConn).CloseWrite())
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("the connection was never handled")
			}
			_ = conn.Close()
			mu.Lock()
			defer mu.Unlock()
			if tc.logged {
				require.Equal(t, []string{"admin socket: refused an unreadable request"}, logged)
			} else {
				require.Empty(t, logged, "a probe is not a refused request")
			}
		})
	}
}

// Running itself makes no log line on the serving side.
func TestRunningLogsNothing(t *testing.T) {
	dir := shortDir(t)
	st, err := tower.Init(dir, tower.ModeStandalone)
	require.NoError(t, err)
	ln, err := Listen(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close(); Remove(dir) })
	var mu sync.Mutex
	var logged []string
	go func() {
		_ = Serve(ln, st, func(f string, a ...any) { mu.Lock(); logged = append(logged, f); mu.Unlock() })
	}()
	for i := 0; i < 5; i++ {
		require.True(t, Running(dir))
	}
	_, err = Client{Dir: dir}.JoinFingerprint()
	require.NoError(t, err, "a real request after the probes is answered: the probes ended cleanly")
	mu.Lock()
	defer mu.Unlock()
	require.Empty(t, logged)
}

// Live E2E 2026-10-28: a data directory whose admin.sock path is longer than this system allows
// for a socket failed with "bind: invalid argument". The error now says why and what to do.
func TestASocketPathTooLongSaysSo(t *testing.T) {
	limit := len(syscall.RawSockaddrUnix{}.Path) - 1 // room for the terminating NUL
	base, err := os.MkdirTemp("", "ta")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	for _, tc := range []struct {
		name   string
		extra  int // bytes the socket path is over (+) or under (-) the limit
		tooLon bool
	}{
		{name: "well under the limit", extra: -20, tooLon: false},
		{name: "exactly at the limit", extra: 0, tooLon: false},
		{name: "one byte over", extra: 1, tooLon: true},
		{name: "far over", extra: 60, tooLon: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// dir + "/" + SocketName must be limit+extra bytes long.
			want := limit + tc.extra - len(SocketName) - 1 - len(base) - 1
			require.Positive(t, want, "the temp dir is too long for this test")
			dir := filepath.Join(base, strings.Repeat("d", want))
			require.NoError(t, os.MkdirAll(dir, 0o700))
			require.Len(t, Path(dir), limit+tc.extra)
			ln, err := Listen(dir)
			if !tc.tooLon {
				require.NoError(t, err)
				_ = ln.Close()
				Remove(dir)
				return
			}
			require.Error(t, err)
			require.Contains(t, err.Error(), "too long")
			require.Contains(t, err.Error(), "use a shorter --dir")
			require.Contains(t, err.Error(), Path(dir))
			_, serr := os.Lstat(Path(dir))
			require.True(t, os.IsNotExist(serr), "nothing is left behind")
		})
	}
}
