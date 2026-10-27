package edgeauth_test

// Golden vectors for checks_root_sig (checksroot.go): computed independently of Go
// (testdata/gen_checks_root_vectors.py, pure-Python RFC 8032) and shared with the phone, which must
// reproduce every signed byte and signature exactly as this does.

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/edgeauth"
)

func TestChecksRootGoldenVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/checks_root_vectors.json")
	require.NoError(t, err)
	var file struct {
		Domain  string `json:"domain"`
		Vectors []struct {
			Name      string `json:"name"`
			Request   string `json:"request"`
			Seed      string `json:"seed"`
			PublicKey string `json:"public_key"`
			Account   string `json:"account"`
			NodeID    string `json:"node_id"`
			NodeKey   string `json:"node_key"`
			JoinName  string `json:"join_name"`
			Kind      string `json:"kind"`
			Nonce     string `json:"nonce"`
			TS        int64  `json:"ts"`
			SignedHex string `json:"signed_hex"`
			Sig       string `json:"checks_root_sig"`
		} `json:"vectors"`
	}
	require.NoError(t, json.Unmarshal(raw, &file))
	require.Equal(t, "rogerai-checks-root/v1", file.Domain)
	require.Len(t, file.Vectors, 5)
	for _, v := range file.Vectors {
		seed, err := hex.DecodeString(v.Seed)
		require.NoError(t, err)
		key := ed25519.NewKeyFromSeed(seed)
		require.Equal(t, v.PublicKey, hex.EncodeToString(key.Public().(ed25519.PublicKey)), v.Name)
		nodePub, err := hex.DecodeString(v.NodeKey)
		require.NoError(t, err)
		require.Equal(t, v.NodeID, edgeauth.NodeID(nodePub), v.Name)
		switch v.Request {
		case "enroll":
			r := edgeauth.Request{Account: v.Account, NodeID: v.NodeID, NodeKey: v.NodeKey, Name: v.JoinName,
				Kind: v.Kind, Nonce: v.Nonce, TS: v.TS, UserKey: v.PublicKey}
			require.Equal(t, v.SignedHex, hex.EncodeToString(r.ChecksRootBytes()), v.Name)
			r.SignChecksRoot(key)
			require.Equal(t, v.Sig, r.ChecksRootSig, v.Name)
			require.True(t, r.ChecksRootProven(), v.Name)
		case "claim":
			c := edgeauth.ClaimRequest{NodeID: v.NodeID, NodeKey: v.NodeKey, Name: v.JoinName, Kind: v.Kind, Nonce: v.Nonce, TS: v.TS}
			require.Equal(t, v.SignedHex, hex.EncodeToString(c.ChecksRootBytes()), v.Name)
			c.SignChecksRoot(key)
			require.Equal(t, v.Sig, c.ChecksRootSig, v.Name)
			require.True(t, c.ChecksRootProven(), v.Name)
		default:
			t.Fatalf("%s: unknown request %q", v.Name, v.Request)
		}
	}
}
