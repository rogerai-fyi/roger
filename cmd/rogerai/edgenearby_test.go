package main

// Units behind features/edge/empty_edge_simple.feature §7 that the scenarios reach only on their
// happy path: who --for may name, the TUI's invite hook with no Edge or no listening authority, and
// the nearby list with nothing pinging. A real authority on disk; no mocks.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/edgeauth"
	"rogerai.fm/roger/v6/internal/edgeauth/enrollhttp"
	"rogerai.fm/roger/v6/internal/joininvite"
	"rogerai.fm/roger/v6/internal/look"
	"rogerai.fm/roger/v6/internal/store"
)

func nearbyNode(t *testing.T, name, pin string) store.EdgeNode {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return store.EdgeNode{ID: edgeauth.NodeID(pub), Name: name, Kind: "mobile", Pin: pin,
		Presence: string(edge.PresenceCandidate), LastSeen: time.Now().Unix()}
}

func seeNearby(t *testing.T, nodes ...store.EdgeNode) {
	t.Helper()
	st, err := loadEdgeState()
	require.NoError(t, err)
	st.candidates = append(st.candidates, nodes...)
	require.NoError(t, st.save())
}

func TestEdgeInviteTarget(t *testing.T) {
	useTempConfig(t)
	a, b := nearbyNode(t, "iPhone", ""), nearbyNode(t, "iPhone", "")
	desk := nearbyNode(t, "deskside", strings.Repeat("ab", 32))
	seeNearby(t, a, b, desk)

	// Two devices with one name: the name is refused as ambiguous, never resolved to the first.
	_, err := edgeInviteTarget("iPhone")
	require.Error(t, err)
	// Each is still named exactly by its id.
	got, err := edgeInviteTarget(a.ID)
	require.NoError(t, err)
	require.Equal(t, a.ID, got.ID)
	got, err = edgeInviteTarget(b.ID)
	require.NoError(t, err)
	require.Equal(t, b.ID, got.ID)
	// A unique name resolves.
	got, err = edgeInviteTarget("deskside")
	require.NoError(t, err)
	require.Equal(t, desk.ID, got.ID)
	// A device never seen is refused with where to look.
	_, err = edgeInviteTarget("n_nothere")
	require.ErrorContains(t, err, "roger edge nearby")
}

func TestEdgeInviteForNode(t *testing.T) {
	useTempConfig(t)
	phone := nearbyNode(t, "gentle-ibex-14", "")

	// No Edge here: i says to start one first.
	_, _, err := edgeInviteForNode(phone)
	require.EqualError(t, err, look.NearbyNoEdgeYet)

	// An authority that has never answered on the network: i says so rather than mint a line with
	// no address in it.
	local, err := edgeauth.Designate(edgeAuthDir(), "shed")
	require.NoError(t, err)
	lan := edgeLANIPs
	t.Cleanup(func() { edgeLANIPs = lan })
	edgeLANIPs = func() []string { return []string{"192.168.1.5"} }
	_, _, err = edgeInviteForNode(phone)
	require.EqualError(t, err, look.NearbyNotServing)

	// Serving: a bound invite, and a claim-only phone's claim granted with it.
	edgeSaveAuthorityPort(8791)
	line, until, err := edgeInviteForNode(phone)
	require.NoError(t, err)
	require.WithinDuration(t, time.Now().Add(edgeauth.CodeLifetime), until, time.Minute)
	in, err := joininvite.Parse(line)
	require.NoError(t, err)
	require.Equal(t, phone.ID, in.For)
	require.Equal(t, []string{"192.168.1.5:8791"}, in.Addrs)
	local, _, err = edgeauth.OpenLocal(edgeAuthDir())
	require.NoError(t, err)
	require.True(t, local.ClaimGranted(phone.ID))

	// A roger device (it serves a certificate) gets the bound invite and NO claim grant: a grant that
	// outlives a forget would let a removed node re-claim (the serving path never grants one).
	desk := nearbyNode(t, "deskside", strings.Repeat("ab", 32))
	line, _, err = edgeInviteForNode(desk)
	require.NoError(t, err)
	in, err = joininvite.Parse(line)
	require.NoError(t, err)
	require.Equal(t, desk.ID, in.For)
	require.False(t, local.ClaimGranted(desk.ID))
}

func TestEdgeNearbyNoneAndUsage(t *testing.T) {
	useTempConfig(t)
	out, err := captureEdgeStdout(func() error { return dispatch(loadConfig(), []string{"edge", "nearby"}) })
	require.NoError(t, err)
	require.Equal(t, look.NearbyCLINone+"\n", out)
	// nearby takes no argument; approve takes exactly one.
	_, err = captureEdgeStdout(func() error { return dispatch(loadConfig(), []string{"edge", "nearby", "x"}) })
	require.Error(t, err)
	_, err = captureEdgeStdout(func() error { return dispatch(loadConfig(), []string{"edge", "approve"}) })
	require.Error(t, err)
	// approve of a device never seen fails and says where to look.
	_, err = captureEdgeStdout(func() error { return dispatch(loadConfig(), []string{"edge", "approve", "n_nothere"}) })
	require.ErrorContains(t, err, "roger edge scan")
	// invite --for with no Edge here is the same refusal invite always gave.
	_, err = captureEdgeStdout(func() error {
		return dispatch(loadConfig(), []string{"edge", "invite", "--for", "n_nothere"})
	})
	require.ErrorContains(t, err, "not an Edge authority")
}

// A device presenting an invite minted for another one is told so, in the words the screen uses,
// through the real HTTP enroll path (the 403 flattens the error; the classifier restores it).
func TestEnrollWithAnInviteForAnotherDevice(t *testing.T) {
	useTempConfig(t)
	local, err := edgeauth.Designate(t.TempDir(), "shed")
	require.NoError(t, err)
	srv := httptest.NewServer(enrollhttp.Handler(local))
	t.Cleanup(srv.Close)
	code, err := local.MintBearerCodeFor(nearbyNode(t, "gentle-ibex-14", "").ID)
	require.NoError(t, err)

	other, _, _ := ed25519.GenerateKey(rand.Reader)
	_, user, _ := ed25519.GenerateKey(rand.Reader)
	req := edgeauth.NewRequest(edgeauth.LocalAccount, "impostor", "mobile", other, time.Now())
	req.Sign(user)
	req.JoinProof = edgeauth.JoinProof(code, local.Descriptor().Fingerprint, req)
	_, err = enrollhttp.Enroll(context.Background(), srv.URL, req)
	require.Error(t, err)
	err = edgeClassifyEnrollErr(err)
	require.ErrorIs(t, err, edgeauth.ErrInviteForOther)
	require.Equal(t, look.JoinSaysForOther, err.Error())
}
