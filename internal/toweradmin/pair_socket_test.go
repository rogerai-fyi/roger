package toweradmin_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/localplane"
	"rogerai.fm/roger/v6/internal/tower"
	"rogerai.fm/roger/v6/internal/toweradmin"
)

func pairDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "tp")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return filepath.Join(d, "lab")
}

// The owner's pairing commands travel the socket to the serving plane (features/join/approve.feature).
func TestPairingReachesTheServingPlane(t *testing.T) {
	dir := pairDir(t)
	st, err := tower.Init(dir, tower.ModeStandalone)
	require.NoError(t, err)
	ln, err := toweradmin.Listen(dir)
	require.NoError(t, err)
	go func() { _ = toweradmin.ServeWith(ln, st, localplane.New(st), func(string, ...any) {}) }()
	t.Cleanup(func() { _ = ln.Close(); toweradmin.Remove(dir) })

	c := toweradmin.Client{Dir: dir}
	reqs, err := c.PairRequests()
	require.NoError(t, err)
	require.Empty(t, reqs, "no one is asking yet")
	require.Error(t, c.ApprovePair("nope"), "the plane refuses an unknown request")
	require.Error(t, c.RefusePair("nope"), "the plane refuses an unknown request")
}

// A Tower serving without a plane takes no join requests, and says so.
func TestPairingWithoutAPlaneIsRefused(t *testing.T) {
	dir := pairDir(t)
	st, err := tower.Init(dir, tower.ModeStandalone)
	require.NoError(t, err)
	ln, err := toweradmin.Listen(dir)
	require.NoError(t, err)
	go func() { _ = toweradmin.Serve(ln, st, func(string, ...any) {}) }()
	t.Cleanup(func() { _ = ln.Close(); toweradmin.Remove(dir) })

	_, err = toweradmin.Client{Dir: dir}.PairRequests()
	require.ErrorContains(t, err, "not taking join requests")
}

// Do runs one request directly against a Tower's state, as the socket does.
func TestDoRunsARequestDirectly(t *testing.T) {
	st, err := tower.Init(pairDir(t), tower.ModeStandalone)
	require.NoError(t, err)
	fp, err := st.JoinFingerprint()
	require.NoError(t, err)
	require.Equal(t, fp, toweradmin.Do(st, toweradmin.Request{Op: "fingerprint"}).Fingerprint)
	require.Contains(t, toweradmin.Do(st, toweradmin.Request{Op: "nope"}).Error, "unknown administration request")
	require.Contains(t, toweradmin.Do(st, toweradmin.Request{Op: "approve", ID: "x"}).Error, "not taking join requests")
}
