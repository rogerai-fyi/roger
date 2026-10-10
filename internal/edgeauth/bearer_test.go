package edgeauth_test

// "roger edge invite needs no joiner key first" (features/join/invite.feature, ruling 2026-10-10
// superseding enrollment D15): a bearer code admits the key that proves it holds the code, once.
// A real local authority on disk; real signed requests.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edgeauth"
)

// bearerReq is a signed request from a key the authority never allowed, carrying the join proof
// for code against this authority's root.
func bearerReq(t *testing.T, local *edgeauth.Local, code string) edgeauth.Request {
	t.Helper()
	_, user := keypair(t)
	nodePub, _ := keypair(t)
	req := enrollReq(user, nodePub, time.Now())
	req.JoinProof = edgeauth.JoinProof(code, local.Descriptor().Fingerprint, req)
	return req
}

func TestABearerCodeAdmitsTheKeyThatProvesIt(t *testing.T) {
	local, err := edgeauth.Designate(t.TempDir(), "shed")
	require.NoError(t, err)
	code, err := local.MintBearerCode()
	require.NoError(t, err)

	req := bearerReq(t, local, code)
	resp, err := local.Issue(req)
	require.NoError(t, err, "a key-less invite admits the key that proves the code")
	requireProof(t, code, resp, req.Nonce)
	allowed, err := local.Allowed()
	require.NoError(t, err)
	require.Contains(t, allowed, req.UserKey, "the joiner's key is on the allow-list afterwards")
}

func TestABearerCodeIsSingleUse(t *testing.T) {
	local, err := edgeauth.Designate(t.TempDir(), "shed")
	require.NoError(t, err)
	code, err := local.MintBearerCode()
	require.NoError(t, err)
	_, err = local.Issue(bearerReq(t, local, code))
	require.NoError(t, err)

	_, err = local.Issue(bearerReq(t, local, code))
	require.ErrorIs(t, err, edgeauth.ErrUnknownMachine, "a spent invite admits nobody else")
}

func TestAWrongBearerProofIsTheSameRefusal(t *testing.T) {
	local, err := edgeauth.Designate(t.TempDir(), "shed")
	require.NoError(t, err)
	code, err := local.MintBearerCode()
	require.NoError(t, err)
	other, err := local.MintBearerCode()
	require.NoError(t, err)
	require.NotEqual(t, code, other)

	wrong := edgeauth.NewCode()
	_, err = local.Issue(bearerReq(t, local, wrong))
	require.ErrorIs(t, err, edgeauth.ErrUnknownMachine)
	// Either live invite still works after one wrong guess.
	_, err = local.Issue(bearerReq(t, local, code))
	require.NoError(t, err)
}

func TestABearerProofIsBoundToItsRequest(t *testing.T) {
	local, err := edgeauth.Designate(t.TempDir(), "shed")
	require.NoError(t, err)
	code, err := local.MintBearerCode()
	require.NoError(t, err)
	stolen := bearerReq(t, local, code)
	// The proof lifted onto another key's request does not verify.
	_, user := keypair(t)
	nodePub, _ := keypair(t)
	req := enrollReq(user, nodePub, time.Now())
	req.JoinProof = stolen.JoinProof
	_, err = local.Issue(req)
	require.ErrorIs(t, err, edgeauth.ErrUnknownMachine)
	// And a proof for another authority's root does not verify here.
	foreign := bearerReq(t, local, code)
	foreign.JoinProof = edgeauth.JoinProof(code, "00"+local.Descriptor().Fingerprint[2:], foreign)
	_, err = local.Issue(foreign)
	require.ErrorIs(t, err, edgeauth.ErrUnknownMachine)
}

func TestBearerGuessesAreBudgeted(t *testing.T) {
	local, err := edgeauth.Designate(t.TempDir(), "shed")
	require.NoError(t, err)
	code, err := local.MintBearerCode()
	require.NoError(t, err)
	for i := 0; i < edgeauth.BearerGuessBudget; i++ {
		_, err = local.Issue(bearerReq(t, local, edgeauth.NewCode()))
		require.ErrorIs(t, err, edgeauth.ErrUnknownMachine)
	}
	_, err = local.Issue(bearerReq(t, local, code))
	require.ErrorIs(t, err, edgeauth.ErrUnknownMachine, "after the guess budget the invite is void")
}

func TestAnExpiredBearerCodeAdmitsNobody(t *testing.T) {
	local, err := edgeauth.Designate(t.TempDir(), "shed")
	require.NoError(t, err)
	code, err := local.MintBearerCode()
	require.NoError(t, err)
	later := time.Now().Add(edgeauth.CodeLifetime + time.Minute)
	local.SetNow(func() time.Time { return later })
	_, user := keypair(t)
	nodePub, _ := keypair(t)
	req := enrollReq(user, nodePub, later)
	req.JoinProof = edgeauth.JoinProof(code, local.Descriptor().Fingerprint, req)
	_, err = local.Issue(req)
	require.ErrorIs(t, err, edgeauth.ErrUnknownMachine)
}
