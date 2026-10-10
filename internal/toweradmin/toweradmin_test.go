package toweradmin

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/tower"
)

// shortDir is a data directory with a path short enough for a unix socket.
func shortDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "ta")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return filepath.Join(d, "lab")
}

func serving(t *testing.T) (*tower.State, string, *[]string) {
	t.Helper()
	dir := shortDir(t)
	st, err := tower.Init(dir, tower.ModeStandalone)
	require.NoError(t, err)
	ln, err := Listen(dir)
	require.NoError(t, err)
	var logged []string
	go func() { _ = Serve(ln, st, func(f string, a ...any) { logged = append(logged, f) }) }()
	t.Cleanup(func() { _ = ln.Close(); Remove(dir) })
	return st, dir, &logged
}

func TestTheSocketIsOwnerOnlyAndAnswers(t *testing.T) {
	st, dir, _ := serving(t)
	fi, err := os.Stat(Path(dir))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	require.True(t, Running(dir))

	c := Client{Dir: dir}
	fp, err := c.JoinFingerprint()
	require.NoError(t, err)
	want, _ := st.JoinFingerprint()
	require.Equal(t, want, fp)

	inv, code, err := c.CreateJoinInvitation(tower.JoinClient, "laptop-x", nil, time.Minute)
	require.NoError(t, err)
	require.NotEmpty(t, code)
	require.Len(t, c.LiveInvitations(), 1)
	require.Len(t, st.LiveInvitations(), 1, "the operation ran on the serving process's state")
	require.NoError(t, c.VoidInvitation(inv.ID))
	require.Empty(t, c.LiveInvitations())
	require.Error(t, c.VoidInvitation(inv.ID), "an error comes back in words")

	_, err = c.ConsumeInvitation("nope", "nope", "u_x")
	require.Error(t, err)
	oinv, ocode, err := c.CreateInvitation("u_x", time.Minute, 3)
	require.NoError(t, err)
	cred, err := c.ConsumeInvitation(oinv.ID, ocode, "u_x")
	require.NoError(t, err)
	require.Equal(t, tower.RoleLocalOperator, cred.Role)
	s, err := c.AttachStation("st-1", strings.Repeat("ab", 8), []string{"m"})
	require.NoError(t, err)
	require.Equal(t, "st-1", s.ID)
	_, err = c.AttachCuratedStation("st-2", strings.Repeat("cd", 8), []string{"m"}, "")
	require.Error(t, err, "a curated attach with no provider is refused, as directly")
}

// Anything other than a known request is refused and logged, and the socket stays up.
func TestAnUnknownRequestIsRefusedAndTheSocketStaysUp(t *testing.T) {
	_, dir, logged := serving(t)
	_, err := Client{Dir: dir}.call(Request{Op: "drop-tables"})
	require.ErrorContains(t, err, "unknown administration request")
	conn, err := net.Dial("unix", Path(dir))
	require.NoError(t, err)
	_, _ = conn.Write([]byte("not json"))
	_ = conn.Close()
	time.Sleep(50 * time.Millisecond)
	require.True(t, Running(dir), "the socket stays up")
	require.GreaterOrEqual(t, len(*logged), 2)
}

// A socket left by a crashed serve is replaced; a live one is not taken over.
func TestAStaleSocketIsReplaced(t *testing.T) {
	dir := shortDir(t)
	_, err := tower.Init(dir, tower.ModeStandalone)
	require.NoError(t, err)
	stale, err := net.Listen("unix", Path(dir))
	require.NoError(t, err)
	stale.(*net.UnixListener).SetUnlinkOnClose(false)
	require.NoError(t, stale.Close()) // crashed: the file remains, nothing listens
	_, err = os.Stat(Path(dir))
	require.NoError(t, err)
	ln, err := Listen(dir)
	require.NoError(t, err, "a stale socket does not block the next start")
	_, err = Listen(dir)
	require.ErrorContains(t, err, "already answers", "a live socket is not taken over")
	require.NoError(t, ln.Close())
}

func TestAnotherUserIsRefused(t *testing.T) {
	_, dir, logged := serving(t)
	prev := peerUID
	peerUID = func() int { return os.Getuid() + 1 }
	t.Cleanup(func() { peerUID = prev })
	_, err := Client{Dir: dir}.JoinFingerprint()
	require.Error(t, err, "a peer that is not the serving user gets no answer")
	require.Contains(t, strings.Join(*logged, "\n"), "refused a connection")
}

// With no serve, nothing answers: Running is false and a call says so; a directory that does not
// exist has no socket to open.
func TestNoServeNoAnswer(t *testing.T) {
	dir := shortDir(t)
	require.False(t, Running(dir))
	_, err := Client{Dir: dir}.JoinFingerprint()
	require.Error(t, err)
	require.Nil(t, Client{Dir: dir}.LiveInvitations())
	_, err = Listen(filepath.Join(dir, "missing", "deeper"))
	require.Error(t, err)
}

// A request that is not JSON gets a refusal in words, not a dropped connection.
func TestAnUnreadableRequestIsAnsweredWithARefusal(t *testing.T) {
	_, dir, _ := serving(t)
	conn, err := net.Dial("unix", Path(dir))
	require.NoError(t, err)
	defer conn.Close()
	_, _ = conn.Write([]byte("}\n"))
	buf := make([]byte, 256)
	n, _ := conn.Read(buf)
	require.Contains(t, string(buf[:n]), "not an administration request")
}

func TestRevokeClientThroughTheSocket(t *testing.T) {
	_, dir, _ := serving(t)
	_, err := Client{Dir: dir}.RevokeClientNamed("nobody")
	require.ErrorContains(t, err, "no admitted client")
}
