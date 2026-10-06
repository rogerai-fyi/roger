package edgeauth

// Contract: features/edge/enrollment_root_trust.feature (sections 2, 3, 6, 8 and the golden
// vectors in section 14). The vectors in testdata/root_proof_vectors.json were computed
// independently of this package (Python hashlib) and are shared with the phone.

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type proofVector struct {
	CodeTyped string `json:"code_typed"`
	Code      string `json:"code"`
	RootFP    string `json:"root_fp"`
	Account   string `json:"account"`
	NodeID    string `json:"node_id"`
	Nonce     string `json:"nonce"`
	Key       string `json:"key"`
	Proof     string `json:"proof"`
}

type vectors struct {
	Domain     string        `json:"domain"`
	Iterations int           `json:"iterations"`
	Proofs     []proofVector `json:"proofs"`
	Codes      []struct {
		Typed     string `json:"typed"`
		Canonical string `json:"canonical"`
		Error     string `json:"error"`
	} `json:"codes"`
	Pins []struct {
		Typed string `json:"typed"`
		Pin   string `json:"pin"`
		Error string `json:"error"`
	} `json:"pins"`
}

func loadVectors(t *testing.T) vectors {
	t.Helper()
	raw, err := os.ReadFile("testdata/root_proof_vectors.json")
	require.NoError(t, err)
	var v vectors
	require.NoError(t, json.Unmarshal(raw, &v))
	require.NotEmpty(t, v.Proofs)
	return v
}

func TestTheProofParametersAreTheRuledOnes(t *testing.T) {
	v := loadVectors(t)
	require.Equal(t, v.Domain, ProofDomain)
	require.Equal(t, 200000, ProofIterations, "D3: 200,000 PBKDF2 iterations")
	require.Equal(t, v.Iterations, ProofIterations)
	require.Equal(t, 10, CodeLen, "D2: 10 symbols")
	require.Equal(t, "15m0s", CodeLifetime.String(), "D4: 15 minutes")
	require.Equal(t, 5, CodeAttempts, "D5: 5 wrong entries")
}

func TestGoldenRootProofs(t *testing.T) {
	for _, g := range loadVectors(t).Proofs {
		t.Run(g.Code, func(t *testing.T) {
			canon, err := CanonicalCode(g.CodeTyped)
			require.NoError(t, err)
			require.Equal(t, g.Code, canon)
			require.Equal(t, g.Proof, RootProof(g.Code, g.RootFP, g.Account, g.NodeID, g.Nonce))
			require.NoError(t, VerifyRootProof(g.CodeTyped, g.Proof, g.RootFP, g.Account, g.NodeID, g.Nonce))
		})
	}
}

// "a vector with any one field changed fails in both" - every field the proof is bound to.
func TestGoldenRootProofsFailWhenAnyFieldChanges(t *testing.T) {
	flip := func(s string) string {
		if s == "" {
			return "x"
		}
		b := []byte(s)
		if b[len(b)-1] == '0' {
			b[len(b)-1] = '1'
		} else {
			b[len(b)-1] = '0'
		}
		return string(b)
	}
	g := loadVectors(t).Proofs[0]
	cases := map[string]proofVector{}
	c := g
	c.Code = flip(g.Code)
	cases["code"] = c
	c = g
	c.RootFP = flip(g.RootFP)
	cases["root"] = c
	c = g
	c.Account = flip(g.Account)
	cases["account"] = c
	c = g
	c.NodeID = flip(g.NodeID)
	cases["node id"] = c
	c = g
	c.Nonce = flip(g.Nonce)
	cases["nonce"] = c
	c = g
	c.Proof = flip(g.Proof)
	cases["proof"] = c
	c = g
	c.Proof = "v2:" + strings.TrimPrefix(g.Proof, "v1:")
	cases["proof version"] = c
	for name, m := range cases {
		t.Run(name, func(t *testing.T) {
			err := VerifyRootProof(m.Code, m.Proof, m.RootFP, m.Account, m.NodeID, m.Nonce)
			require.Error(t, err)
		})
	}
}

func TestVerifyRootProofNamesWhatWentWrong(t *testing.T) {
	g := loadVectors(t).Proofs[0]
	err := VerifyRootProof(g.Code, "", g.RootFP, g.Account, g.NodeID, g.Nonce)
	require.True(t, errors.Is(err, ErrNoRootProof), "an answer with no proof: %v", err)

	err = VerifyRootProof(g.Code, "v9:"+strings.TrimPrefix(g.Proof, "v1:"), g.RootFP, g.Account, g.NodeID, g.Nonce)
	require.True(t, errors.Is(err, ErrProofVersion), "an unknown proof version: %v", err)

	err = VerifyRootProof("ZZZZZZZZZZ", g.Proof, g.RootFP, g.Account, g.NodeID, g.Nonce)
	require.True(t, errors.Is(err, ErrRootProofMismatch), "a wrong code: %v", err)

	err = VerifyRootProof("nope", g.Proof, g.RootFP, g.Account, g.NodeID, g.Nonce)
	require.True(t, errors.Is(err, ErrBadCode), "a malformed code: %v", err)

	err = VerifyRootProof(g.Code, "v1:zz", g.RootFP, g.Account, g.NodeID, g.Nonce)
	require.True(t, errors.Is(err, ErrRootProofMismatch), "an unreadable proof is a mismatch: %v", err)
}

func TestCanonicalCodeVectors(t *testing.T) {
	for _, c := range loadVectors(t).Codes {
		t.Run(c.Typed, func(t *testing.T) {
			got, err := CanonicalCode(c.Typed)
			if c.Error != "" {
				require.Error(t, err)
				require.True(t, errors.Is(err, ErrBadCode))
				require.Contains(t, err.Error(), map[string]string{
					"length": "10 symbols", "symbol": "not a code symbol",
				}[c.Error])
				return
			}
			require.NoError(t, err)
			require.Equal(t, c.Canonical, got)
		})
	}
}

func TestParsePinVectors(t *testing.T) {
	for _, p := range loadVectors(t).Pins {
		t.Run(p.Typed, func(t *testing.T) {
			got, err := ParsePin(p.Typed)
			if p.Error != "" {
				require.Error(t, err)
				require.True(t, errors.Is(err, ErrBadPin))
				require.Contains(t, err.Error(), map[string]string{
					"short":  "too short to be a pin",
					"length": "not a full fingerprint",
					"hex":    "not a fingerprint",
					"hash":   "unsupported hash",
				}[p.Error])
				return
			}
			require.NoError(t, err)
			require.Equal(t, p.Pin, got)
		})
	}
}

func TestNewCodeIsUniformAndUnique(t *testing.T) {
	const n = 10000
	seen := make(map[string]bool, n)
	var counts [CodeLen][32]int
	for i := 0; i < n; i++ {
		c := NewCode()
		require.Len(t, c, CodeLen)
		canon, err := CanonicalCode(c)
		require.NoError(t, err)
		require.Equal(t, c, canon)
		require.False(t, seen[c], "a repeated code after %d draws", i)
		seen[c] = true
		for pos, r := range c {
			counts[pos][strings.IndexRune(codeAlphabet, r)]++
		}
	}
	// Each symbol is expected n/32 = 312.5 times per position; a uniform draw stays well inside
	// +/- 35% at this n (about 7 standard deviations), a biased one does not.
	for pos := range counts {
		for sym, got := range counts[pos] {
			require.InDelta(t, n/32, got, n/32*0.35, "position %d symbol %c", pos, codeAlphabet[sym])
		}
	}
}

func TestFormatCodeGroupsInFives(t *testing.T) {
	require.Equal(t, "7KQ2M-9XH4P", FormatCode("7KQ2M9XH4P"))
	require.Equal(t, "short", FormatCode("short"), "a value that is not a code is shown as it is")
}

func TestShortFingerprintIsTheReadAloudForm(t *testing.T) {
	fp := strings.Repeat("ab", 32)
	require.Equal(t, "ABAB-ABAB-ABAB-ABAB", ShortFingerprint(fp))
	require.Equal(t, "", ShortFingerprint("abc"))
}
