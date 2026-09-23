package main

// The TUI onboarding SEAM (edgesetup.go): the closures the wizard drives through Hooks. The
// @tui suite runs the wizard over a FAKE backend, so these tests exercise the REAL one - the
// same edgeDesignateCore / edgeEnrollCore the CLI runs - and prove the not-allowed refusal is
// mapped to tui.ErrNotAllowedYet (the one reclassification the wizard depends on).

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/edgeauth"
	"rogerai.fm/roger/v6/internal/tui"
)

func TestEdgeSetupHooksNewEdge(t *testing.T) {
	useTempConfig(t)
	h := edgeSetupHooks(loadConfig())

	require.NotNil(t, h.DefaultName)
	require.NotEmpty(t, h.DefaultName())
	require.NotEmpty(t, h.UserKeyHex())

	// Before: nothing set up.
	require.False(t, edgeSetupState().Enrolled)
	require.False(t, edgeSetupState().AuthorityHere)

	// NewEdge designates this machine and enrolls it, with no network.
	require.NoError(t, h.NewEdge("shed"))

	st := edgeSetupState()
	require.True(t, st.Enrolled, "this machine should be enrolled after NewEdge")
	require.True(t, st.AuthorityHere, "this machine should root the Edge after NewEdge")
}

func TestEdgeSetupHooksNewEdgeRejectsBadName(t *testing.T) {
	useTempConfig(t)
	h := edgeSetupHooks(loadConfig())
	require.Error(t, h.NewEdge("not a valid name!!"))
	require.False(t, edgeSetupState().Enrolled)
}

func TestEdgeSetupHooksJoinNotAllowed(t *testing.T) {
	useTempConfig(t)
	// An authority that refuses the way an un-allowed enroll is refused: 403 with the exact
	// reason the enrollment path maps back to ErrUnknownMachine.
	authority := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, edgeauth.ErrUnknownMachine.Error(), http.StatusForbidden)
	}))
	defer authority.Close()

	h := edgeSetupHooks(loadConfig())
	err := h.Join("bench", authority.URL)
	require.Error(t, err)
	require.True(t, errors.Is(err, tui.ErrNotAllowedYet),
		"a not-allowed join must map to tui.ErrNotAllowedYet, got %v", err)
	require.False(t, edgeSetupState().Enrolled, "a refused join must not enroll the machine")
}

func TestEdgeSetupHooksJoinRejectsBadName(t *testing.T) {
	useTempConfig(t)
	h := edgeSetupHooks(loadConfig())
	require.Error(t, h.Join("bad name!!", "http://127.0.0.1:1"))
}

func TestEdgeSetupHooksJoinOtherError(t *testing.T) {
	useTempConfig(t)
	h := edgeSetupHooks(loadConfig())
	// A dead address: the enroll fails to reach the authority. That is NOT "not allowed",
	// so it must come back as the raw error, never reclassified to the allow step.
	err := h.Join("bench", "http://127.0.0.1:1")
	require.Error(t, err)
	require.False(t, errors.Is(err, tui.ErrNotAllowedYet),
		"an unreachable authority must not be mistaken for not-allowed")
}

func TestCmdEdgeSetupJoinNeedsAddress(t *testing.T) {
	useTempConfig(t)
	prev := edgeIsInteractive
	edgeIsInteractive = func() bool { return true }
	t.Cleanup(func() { edgeIsInteractive = prev })

	edgeStdin = strings.NewReader("j\nbench\n\n") // join, name, then a BLANK address
	_, err := captureEdgeStdout(func() error { return cmdEdgeSetup(loadConfig(), nil) })
	require.Error(t, err, "a join with no address must be refused")
}

func TestCmdEdgeSetupJoinUnreachable(t *testing.T) {
	useTempConfig(t)
	prev := edgeIsInteractive
	edgeIsInteractive = func() bool { return true }
	t.Cleanup(func() { edgeIsInteractive = prev })

	edgeStdin = strings.NewReader("j\nbench\nhttp://127.0.0.1:1\n")
	_, err := captureEdgeStdout(func() error { return cmdEdgeSetup(loadConfig(), nil) })
	require.Error(t, err, "an unreachable authority must surface an error, not a clean exit")
	require.Contains(t, err.Error(), "could not reach the authority at http://127.0.0.1:1",
		"the owner should see the address, not a raw dial error")
	require.False(t, edgeSetupState().Enrolled)
}

func TestEdgeSetupJoinErrorFraming(t *testing.T) {
	// nil passes through.
	require.NoError(t, edgeSetupJoinError("http://x", nil))
	// A not-allowed error passes through unchanged (the caller maps it to the allow step).
	na := edgeauth.ErrUnknownMachine
	require.ErrorIs(t, edgeSetupJoinError("http://x", na), edgeauth.ErrUnknownMachine)
	// A real refusal is left in the authority's OWN words, not reframed as unreachable.
	refused := errors.New("the authority refused: signature does not verify")
	require.Equal(t, refused, edgeSetupJoinError("http://x", refused))
	// A transport failure is reframed with the address.
	dial := errors.New("dial tcp 192.168.1.10:8791: connect: connection refused")
	got := edgeSetupJoinError("http://192.168.1.10:8791", dial)
	require.Contains(t, got.Error(), "could not reach the authority at http://192.168.1.10:8791")
}

func TestCmdEdgeSetupNewEndToEnd(t *testing.T) {
	useTempConfig(t)
	prev := edgeIsInteractive
	edgeIsInteractive = func() bool { return true }
	t.Cleanup(func() { edgeIsInteractive = prev })

	edgeStdin = strings.NewReader("n\nshed\n")
	out, err := captureEdgeStdout(func() error { return cmdEdgeSetup(loadConfig(), nil) })
	require.NoError(t, err)
	require.Contains(t, out, "roger edge enroll")
	st := edgeSetupState()
	require.True(t, st.Enrolled)
	require.True(t, st.AuthorityHere)
}

func TestEdgeSetupEnrollHint(t *testing.T) {
	// Known authority address: the hint carries it verbatim so a copy-paste joins.
	got := edgeSetupEnrollHintFrom(edge.SelfStatus{AuthorityAddr: "http://192.168.1.20:8791"})
	require.Contains(t, got, "http://192.168.1.20:8791")
	require.Contains(t, got, "roger edge enroll")
	// Unknown address: a clear placeholder, never a blank or a wrong address.
	got = edgeSetupEnrollHintFrom(edge.SelfStatus{})
	require.Contains(t, got, "<this machine's LAN address>")
}

func TestEdgeSetupHooksEnrollSelf(t *testing.T) {
	useTempConfig(t)
	// Designate first (authority here, not yet enrolled), then finish via EnrollSelf.
	_, err := edgeDesignateCore(edgeAuthDir(), "shed")
	require.NoError(t, err)
	require.True(t, edgeSetupState().AuthorityHere)
	require.False(t, edgeSetupState().Enrolled)

	h := edgeSetupHooks(loadConfig())
	require.NoError(t, h.EnrollSelf())
	require.True(t, edgeSetupState().Enrolled, "EnrollSelf should finish the missed enroll")
}
