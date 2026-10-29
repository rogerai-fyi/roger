package edgeauth_test

// "an invite bound to one device is refused for another" (features/edge/empty_edge_simple.feature
// §7, founder ruling 2026-10-12): an invite minted FOR a node id (the id a nearby device advertised,
// derived from its node key) admits that node only. A different node presenting the same code is
// refused, the code is NOT spent, and the refusal is not a wrong guess against the invite. A real
// local authority on disk; real signed requests.

import (
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/edgeauth"
)

// boundReq is a signed request from a key the authority never allowed, presenting the given node
// key and the join proof for code against this authority's root.
func boundReq(t *testing.T, local *edgeauth.Local, code string, nodePub ed25519.PublicKey) edgeauth.Request {
	t.Helper()
	_, user := keypair(t)
	req := enrollReq(user, nodePub, time.Now())
	req.JoinProof = edgeauth.JoinProof(code, local.Descriptor().Fingerprint, req)
	return req
}

func TestABoundBearerCodeAdmitsOnlyTheNodeItIsFor(t *testing.T) {
	local, err := edgeauth.Designate(t.TempDir(), "shed")
	require.NoError(t, err)
	aPub, _ := keypair(t)
	bPub, _ := keypair(t)
	code, err := local.MintBearerCodeFor(edgeauth.NodeID(aPub))
	require.NoError(t, err)

	// B holds the code (it read the QR over A's shoulder) and proves it correctly: refused by name,
	// and the invite stays live for A.
	for i := 0; i < edgeauth.BearerGuessBudget+2; i++ {
		_, err = local.Issue(boundReq(t, local, code, bPub))
		require.ErrorIs(t, err, edgeauth.ErrInviteForOther, "attempt %d: another node id must be refused", i)
	}
	allowed, err := local.Allowed()
	require.NoError(t, err)
	require.Empty(t, allowed, "a refused device's key is not admitted")

	req := boundReq(t, local, code, aPub)
	resp, err := local.Issue(req)
	require.NoError(t, err, "the device the invite is for still joins with it: the refusals spent nothing")
	require.Equal(t, edgeauth.NodeID(aPub), resp.NodeID)
	requireProof(t, code, resp, req.Nonce)

	_, err = local.Issue(boundReq(t, local, code, aPub))
	require.ErrorIs(t, err, edgeauth.ErrUnknownMachine, "once used, the bound invite admits nobody, A included")
}

func TestAnUnboundBearerCodeStillAdmitsAnyNode(t *testing.T) {
	local, err := edgeauth.Designate(t.TempDir(), "shed")
	require.NoError(t, err)
	code, err := local.MintBearerCodeFor("")
	require.NoError(t, err)
	anyPub, _ := keypair(t)
	_, err = local.Issue(boundReq(t, local, code, anyPub))
	require.NoError(t, err, "an invite for nobody in particular is the plain roger edge invite")
}

// A wrong proof for a bound code is still a wrong guess: the budget protects the code either way.
func TestAWrongProofAgainstABoundCodeIsBudgeted(t *testing.T) {
	local, err := edgeauth.Designate(t.TempDir(), "shed")
	require.NoError(t, err)
	aPub, _ := keypair(t)
	code, err := local.MintBearerCodeFor(edgeauth.NodeID(aPub))
	require.NoError(t, err)
	for i := 0; i < edgeauth.BearerGuessBudget; i++ {
		_, err = local.Issue(boundReq(t, local, "wrong-"+code, aPub))
		require.ErrorIs(t, err, edgeauth.ErrUnknownMachine)
	}
	_, err = local.Issue(boundReq(t, local, code, aPub))
	require.ErrorIs(t, err, edgeauth.ErrUnknownMachine, "the budget voided the code")
}

// The node id a request names is proved against its node key BEFORE any invite is consulted: a
// request whose key does not derive the id it claims is refused as a key mismatch, matches no invite
// and spends nothing, so it can neither borrow a bound invite's node id nor wear down its budget.
func TestABoundBearerCodeIsNotConsultedForAnUnprovedNodeID(t *testing.T) {
	local, err := edgeauth.Designate(t.TempDir(), "shed")
	require.NoError(t, err)
	aPub, _ := keypair(t)
	bPub, _ := keypair(t)
	code, err := local.MintBearerCodeFor(edgeauth.NodeID(aPub))
	require.NoError(t, err)
	claimsA := func(proofCode string) edgeauth.Request {
		_, user := keypair(t)
		req := edgeauth.NewRequest(edgeauth.LocalAccount, "workshop", "host", bPub, time.Now())
		req.NodeID = edgeauth.NodeID(aPub) // B's key, A's id
		req.Sign(user)
		req.JoinProof = edgeauth.JoinProof(proofCode, local.Descriptor().Fingerprint, req)
		return req
	}
	_, err = local.Issue(claimsA(code))
	require.ErrorIs(t, err, edgeauth.ErrKeyMismatch, "the right code does not lend B the id it is bound to")
	for i := 0; i < edgeauth.BearerGuessBudget; i++ {
		_, err = local.Issue(claimsA("wrong-" + code))
		require.ErrorIs(t, err, edgeauth.ErrKeyMismatch, "attempt %d", i)
	}
	allowed, err := local.Allowed()
	require.NoError(t, err)
	require.Empty(t, allowed)
	_, err = local.Issue(boundReq(t, local, code, aPub))
	require.NoError(t, err, "requests that never proved their node id spent none of the invite's budget")
}
