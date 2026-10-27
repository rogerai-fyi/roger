package main

// The surfaces of features/edge/enrollment_root_trust.feature on the authority and the joiner's
// wizard seam: adopt and readmit mint a code and say where to enter it, an adopting device whose
// code expired gets a fresh one by adopting it again, and the TUI's join holds the authority's
// answer until the code verifies. REAL authorities, REAL HTTP, the REAL host closures.

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/edgeauth"
	"rogerai.fm/roger/v7/internal/tui"
)

func aPhoneID(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return edgeauth.NodeID(pub)
}

func TestAdoptingByClaimMintsACodeAndTheLineSaysWhereToEnterIt(t *testing.T) {
	useTempConfig(t)
	_, err := edgeauth.Designate(edgeAuthDir(), "shed")
	require.NoError(t, err)
	id := aPhoneID(t)
	require.NoError(t, edgeAdoptByClaim(id, "pocket"))
	local, _, err := edgeauth.OpenLocal(edgeAuthDir())
	require.NoError(t, err)
	code, _, ok := local.NodeCode(id)
	require.True(t, ok, "adopting mints a code for that device")

	line := edgeNodeCodeLine(id, "pocket")
	require.Contains(t, line, "pocket")
	require.Contains(t, line, edgeauth.FormatCode(code))
	require.Contains(t, line, "15 minutes")
	require.Contains(t, line, "before root proofs", "the mixed-version warning (D9)")
}

func TestReadmittingMintsAFreshCodeAndSaysAHeldRootNeedsNone(t *testing.T) {
	useTempConfig(t)
	_, err := edgeauth.Designate(edgeAuthDir(), "shed")
	require.NoError(t, err)
	id := aPhoneID(t)
	require.NoError(t, edgeReAdmitDeliberate(id, "attic"))
	local, _, err := edgeauth.OpenLocal(edgeAuthDir())
	require.NoError(t, err)
	code, _, ok := local.NodeCode(id)
	require.True(t, ok)
	line := edgeReadmitCodeLine(id, "attic")
	require.Contains(t, line, edgeauth.FormatCode(code))
	require.Contains(t, line, "still holds this Edge's root")
}

func TestAdoptingAnAdoptingDeviceAgainShowsAFreshCode(t *testing.T) {
	useTempConfig(t)
	_, err := edgeauth.Designate(edgeAuthDir(), "shed")
	require.NoError(t, err)
	id := aPhoneID(t)
	require.NoError(t, edgeAdoptByClaim(id, "pocket"))
	local, _, _ := edgeauth.OpenLocal(edgeAuthDir())
	first, _, _ := local.NodeCode(id)

	msg, err := edgeAdoptWant("pocket")
	require.NoError(t, err)
	second, _, ok := local.NodeCode(id)
	require.True(t, ok)
	require.NotEqual(t, first, second, "a fresh code, and the old one is void")
	require.Contains(t, msg, edgeauth.FormatCode(second))
}

func TestTheAdoptCodeHookReadsTheLiveCode(t *testing.T) {
	useTempConfig(t)
	_, err := edgeauth.Designate(edgeAuthDir(), "shed")
	require.NoError(t, err)
	id := aPhoneID(t)
	_, _, ok := edgeAdoptCode(id)
	require.False(t, ok)
	require.NoError(t, edgeAdoptByClaim(id, "pocket"))
	code, until, ok := edgeAdoptCode(id)
	require.True(t, ok)
	require.Len(t, strings.ReplaceAll(code, "-", ""), edgeauth.CodeLen)
	require.WithinDuration(t, time.Now().Add(edgeauth.CodeLifetime), until, time.Minute)
}

func TestTheWizardsJoinHoldsTheAnswerUntilTheCodeVerifies(t *testing.T) {
	shed := newRootedAuthority(t, "shed")
	code := shed.allow(t, aJoiner(t))
	h := edgeSetupHooks(loadConfig())

	err := h.Join("bench", shed.url)
	var need *tui.NeedsCodeError
	require.True(t, errors.As(err, &need), "the join is held for the code, got %v", err)
	require.Equal(t, edgeauth.ShortFingerprint(shed.fingerprint()), need.Root)
	keptNothing(t)

	done, err := h.JoinCode("ZZZZZ-ZZZZZ")
	require.False(t, done, "a mistyped code is typed again")
	require.ErrorContains(t, err, "4 attempts left")
	keptNothing(t)

	done, err = h.JoinCode(code)
	require.True(t, done)
	require.NoError(t, err)
	require.Equal(t, shed.fingerprint(), heldRootFP(t))

	done, err = h.JoinCode(code)
	require.True(t, done)
	require.Error(t, err, "nothing is held any more")
}

func TestTheWizardsJoinRefusesAnImpostorAndKeepsNothing(t *testing.T) {
	shed := newRootedAuthority(t, "shed")
	impostor := newRootedAuthority(t, "shed")
	key := aJoiner(t)
	code := shed.allow(t, key)
	impostor.allow(t, key)
	h := edgeSetupHooks(loadConfig())
	require.Error(t, h.Join("bench", impostor.url))
	for i := 0; i < edgeauth.CodeAttempts-1; i++ {
		done, err := h.JoinCode(code)
		require.False(t, done)
		require.ErrorContains(t, err, "does not match the Edge that answered")
		if i == 0 {
			require.ErrorContains(t, err, "is not the Edge that showed you this code", "said once, on the first")
		}
	}
	done, err := h.JoinCode(code)
	require.True(t, done)
	require.ErrorContains(t, err, "impersonating")
	keptNothing(t)
}

func TestTheSetupStateNamesTheRootForThePin(t *testing.T) {
	useTempConfig(t)
	h := edgeSetupHooks(loadConfig())
	require.NoError(t, h.NewEdge("shed"))
	require.Equal(t, edgeDescriptor().Fingerprint, edgeSetupState().RootFingerprint)
}

func TestTheConsoleJoinKeepsTheRootOnlyWithTheCode(t *testing.T) {
	shed := newRootedAuthority(t, "shed")
	code := shed.allow(t, aJoiner(t))
	cfg := loadConfig()
	require.Error(t, edgeConsoleJoin(cfg, "bench", shed.url, "not-a-code"))
	err := edgeConsoleJoin(cfg, "bench", shed.url, "ZZZZZ-ZZZZZ")
	require.ErrorContains(t, err, "does not match the Edge that answered")
	keptNothing(t)
	require.Error(t, edgeConsoleJoin(cfg, "bad name!!", shed.url, code))
	require.NoError(t, edgeConsoleJoin(cfg, "bench", shed.url, shed.allow(t, edgeUserKey())))
	require.Equal(t, shed.fingerprint(), heldRootFP(t))
}
