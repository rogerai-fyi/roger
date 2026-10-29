package joincode

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The Edge domain must reproduce the approved Edge golden vectors byte for byte: moving the
// construction here may not change a single proof the phone already checks.
func TestEdgeDomainReproducesTheEdgeGoldenVectors(t *testing.T) {
	raw, err := os.ReadFile("../edgeauth/testdata/root_proof_vectors.json")
	require.NoError(t, err)
	var v struct {
		Proofs []struct {
			Code   string `json:"code"`
			RootFP string `json:"root_fp"`
			Accnt  string `json:"account"`
			NodeID string `json:"node_id"`
			Nonce  string `json:"nonce"`
			Proof  string `json:"proof"`
		} `json:"proofs"`
	}
	require.NoError(t, json.Unmarshal(raw, &v))
	require.NotEmpty(t, v.Proofs)
	for _, p := range v.Proofs {
		require.Equal(t, p.Proof, Edge.Proof(p.Code, p.Nonce, p.RootFP, p.Accnt, p.NodeID), "vector %+v", p)
	}
}

func TestTowerCodesCarryAtLeast128Bits(t *testing.T) {
	require.GreaterOrEqual(t, Tower.CodeLen*5, 128)
	c := Tower.NewCode()
	require.Len(t, c, Tower.CodeLen)
	got, err := Tower.Canonical(strings.ToLower(Tower.Format(c)))
	require.NoError(t, err)
	require.Equal(t, c, got)
	require.NotEqual(t, c, Tower.NewCode())
}

func TestEdgeCodesKeepTheTenSymbolFormat(t *testing.T) {
	require.Equal(t, 10, Edge.CodeLen)
	require.Len(t, Edge.NewCode(), 10)
	require.Equal(t, "ABCDE-FGHJK", Edge.Format("ABCDEFGHJK"))
}

// A Tower stores only a verifier; the proof a client sends checks against it.
func TestTowerProofChecksAgainstTheStoredVerifier(t *testing.T) {
	code := Tower.NewCode()
	ver := Tower.Verifier(code)
	require.NotContains(t, string(ver), code)
	proof := Tower.Proof(code, "n1", "client-pk", "tower-fp")
	require.NoError(t, Tower.CheckWithVerifier(ver, proof, "n1", "client-pk", "tower-fp"))
	// Any field changed fails: the proof binds the code to this client, this Tower, this request.
	require.ErrorIs(t, Tower.CheckWithVerifier(ver, proof, "n2", "client-pk", "tower-fp"), ErrProofMismatch)
	require.ErrorIs(t, Tower.CheckWithVerifier(ver, proof, "n1", "other-pk", "tower-fp"), ErrProofMismatch)
	require.ErrorIs(t, Tower.CheckWithVerifier(ver, proof, "n1", "client-pk", "other-fp"), ErrProofMismatch)
	require.ErrorIs(t, Tower.CheckWithVerifier(Tower.Verifier(Tower.NewCode()), proof, "n1", "client-pk", "tower-fp"), ErrProofMismatch)
}

// A proof made in one domain never verifies in the other (adversarial.feature: cross-domain).
func TestAProofNeverCrossesDomains(t *testing.T) {
	code := "ABCDEFGHJK"
	edge := Edge.Proof(code, "n", "a", "b", "c")
	require.ErrorIs(t, Edge.Check(code, edge, "n", "a", "b", "c"), nil)
	towerCode := Tower.NewCode()
	tp := Tower.Proof(towerCode, "n", "a", "b")
	require.ErrorIs(t, Edge.Check(code, tp, "n", "a", "b", "c"), ErrProofMismatch)
	require.ErrorIs(t, Tower.CheckWithVerifier(Tower.Verifier(towerCode), Edge.Proof(towerCode, "n", "a", "b"), "n", "a", "b"), ErrProofMismatch)
}

func TestCheckNamesWhatWentWrong(t *testing.T) {
	require.ErrorIs(t, Edge.Check("ABCDEFGHJK", "", "n"), ErrNoProof)
	require.ErrorIs(t, Edge.Check("ABCDEFGHJK", "v9:00", "n"), ErrProofVersion)
	require.ErrorIs(t, Edge.Check("not a code", "v1:00", "n"), ErrBadCode)
}

func TestFormatAndVerifierEdges(t *testing.T) {
	c := Tower.NewCode()
	require.Equal(t, c, Tower.Format(c), "a Tower code is not grouped")
	require.Equal(t, "ABC", Edge.Format("ABC"), "a wrong-length code is shown as given")
	require.Panics(t, func() { Edge.Verifier("ABCDEFGHJK") })
	require.ErrorIs(t, Edge.CheckWithVerifier([]byte("x"), "v1:00", "n"), ErrProofMismatch)
	require.ErrorIs(t, Tower.CheckWithVerifier(Tower.Verifier(c), "v1:zz", "n"), ErrProofMismatch)
	_, err := Edge.Canonical("ABCDEFGHJ!")
	require.ErrorIs(t, err, ErrBadCode)
	got, err := Edge.Canonical(" abcde-fghjk ")
	require.NoError(t, err)
	require.Equal(t, "ABCDEFGHJK", got)
	got, err = Edge.Canonical("0OIL1-23456")
	require.NoError(t, err)
	require.Equal(t, "00111"+"23456", got)
}
