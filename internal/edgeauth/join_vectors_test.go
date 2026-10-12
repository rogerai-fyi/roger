package edgeauth

// Golden vectors for the bearer join proof (features/join/invite.feature, "roger edge invite needs
// no joiner key first"). testdata/join_proof_vectors.json was computed independently of this
// package (Python hashlib, see testdata/gen_join_proof_vectors.py) and is shared with the phone:
// both implementations must reproduce every proof byte for byte, and the join domain must never
// verify as the root-proof domain or the reverse.

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/joincode"
)

type joinVector struct {
	Name      string `json:"name"`
	CodeTyped string `json:"code_typed"`
	Code      string `json:"code"`
	RootFP    string `json:"root_fp"`
	UserKey   string `json:"user_key"`
	NodeID    string `json:"node_id"`
	NodeKey   string `json:"node_key"`
	Nonce     string `json:"nonce"`
	Key       string `json:"key"`
	JoinProof string `json:"join_proof"`
	// Verifies is false for a row that must NOT verify in the join domain (a proof made for the
	// root-proof domain over the same inputs).
	Verifies bool `json:"verifies"`
	// Domain is the domain the row's proof was made in; the join domain unless stated.
	Domain string `json:"domain,omitempty"`
}

type joinVectors struct {
	Domain     string       `json:"domain"`
	RootDomain string       `json:"root_domain"`
	Iterations int          `json:"iterations"`
	Proofs     []joinVector `json:"proofs"`
}

func loadJoinVectors(t *testing.T) joinVectors {
	t.Helper()
	raw, err := os.ReadFile("testdata/join_proof_vectors.json")
	require.NoError(t, err)
	var v joinVectors
	require.NoError(t, json.Unmarshal(raw, &v))
	require.NotEmpty(t, v.Proofs)
	return v
}

func joinRequest(g joinVector) Request {
	return Request{UserKey: g.UserKey, NodeID: g.NodeID, NodeKey: g.NodeKey, Nonce: g.Nonce}
}

func TestJoinProofParametersAreTheRuledOnes(t *testing.T) {
	v := loadJoinVectors(t)
	require.Equal(t, v.Domain, joincode.EdgeJoin.Name)
	require.Equal(t, v.RootDomain, joincode.Edge.Name)
	require.NotEqual(t, joincode.EdgeJoin.Name, joincode.Edge.Name, "the two domains must differ")
	require.Equal(t, v.Iterations, joincode.EdgeJoin.Iterations)
	require.Equal(t, joincode.Edge.Iterations, joincode.EdgeJoin.Iterations, "the join proof keeps the Edge stretch")
	require.Equal(t, joincode.Edge.CodeLen, joincode.EdgeJoin.CodeLen, "the join proof keeps the Edge code format")
	require.True(t, joincode.EdgeJoin.SaltWithNonce, "the join stretch is salted with the request nonce")
}

// Every row made in the join domain reproduces byte for byte through JoinProof and verifies the way
// the authority checks it (bearerMatch's joincode.EdgeJoin.Check over the same fields).
func TestGoldenJoinProofs(t *testing.T) {
	for _, g := range loadJoinVectors(t).Proofs {
		if !g.Verifies {
			continue
		}
		t.Run(g.Name, func(t *testing.T) {
			canon, err := CanonicalCode(g.CodeTyped)
			require.NoError(t, err)
			require.Equal(t, g.Code, canon)
			r := joinRequest(g)
			require.Equal(t, g.JoinProof, JoinProof(g.Code, g.RootFP, r), "JoinProof must reproduce the vector")
			require.NoError(t, joincode.EdgeJoin.Check(g.CodeTyped, g.JoinProof, g.Nonce, joinFields(g.RootFP, r)...))
		})
	}
}

// A root fingerprint given with its "sha256:" prefix proves the same bytes as the bare hex: the
// invite's fp carries the prefix, the authority's descriptor does not, and both sides must agree.
func TestJoinProofStripsTheFingerprintPrefix(t *testing.T) {
	g := loadJoinVectors(t).Proofs[0]
	require.True(t, g.Verifies)
	r := joinRequest(g)
	require.Equal(t, g.JoinProof, JoinProof(g.Code, "sha256:"+g.RootFP, r))
}

// The rows that must not verify: a proof made in the root-proof domain over the join fields, and
// the join domain's own proofs checked against the root-proof domain. Neither direction may pass.
func TestJoinAndRootProofsNeverCrossVerify(t *testing.T) {
	v := loadJoinVectors(t)
	for _, g := range v.Proofs {
		r := joinRequest(g)
		fields := joinFields(g.RootFP, r)
		t.Run(g.Name, func(t *testing.T) {
			if g.Verifies {
				// A genuine join proof checked as if it were a root proof fails.
				require.ErrorIs(t, joincode.Edge.Check(g.CodeTyped, g.JoinProof, g.Nonce, fields...), joincode.ErrProofMismatch)
				return
			}
			require.Equal(t, v.RootDomain, g.Domain, "a non-verifying row is a root-domain proof over the join fields")
			// The vector was computed in the root domain: our root domain reproduces it, so the row
			// is a real proof, not a typo.
			require.Equal(t, g.JoinProof, joincode.Edge.Proof(g.Code, g.Nonce, fields...))
			// And the join domain refuses it.
			require.ErrorIs(t, joincode.EdgeJoin.Check(g.CodeTyped, g.JoinProof, g.Nonce, fields...), joincode.ErrProofMismatch)
		})
	}
}

// Every field the join proof is bound to: change any one and the proof fails, in both the
// producer and the checker.
func TestGoldenJoinProofsFailWhenAnyFieldChanges(t *testing.T) {
	flip := func(s string) string {
		b := []byte(s)
		if b[len(b)-1] == '0' {
			b[len(b)-1] = '1'
		} else {
			b[len(b)-1] = '0'
		}
		return string(b)
	}
	g := loadJoinVectors(t).Proofs[0]
	require.True(t, g.Verifies)
	cases := map[string]joinVector{}
	c := g
	c.Code = flip(g.Code)
	c.CodeTyped = c.Code
	cases["code"] = c
	c = g
	c.RootFP = flip(g.RootFP)
	cases["root"] = c
	c = g
	c.UserKey = flip(g.UserKey)
	cases["user key"] = c
	c = g
	c.NodeID = flip(g.NodeID)
	cases["node id"] = c
	c = g
	c.NodeKey = flip(g.NodeKey)
	cases["node key"] = c
	c = g
	c.Nonce = flip(g.Nonce)
	cases["nonce (replay on another request)"] = c
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r := joinRequest(c)
			require.NotEqual(t, g.JoinProof, JoinProof(c.Code, c.RootFP, r))
			require.ErrorIs(t, joincode.EdgeJoin.Check(c.CodeTyped, g.JoinProof, c.Nonce, joinFields(c.RootFP, r)...), joincode.ErrProofMismatch)
		})
	}
}
