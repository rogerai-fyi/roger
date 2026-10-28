package joinsas

// Golden vectors for the matching phrase (features/join/approve.feature §2). testdata/sas_vectors.json
// was generated INDEPENDENTLY in Python (hashlib, testdata/gen_sas_vectors.py, which regenerates it byte
// for byte) from the written derivation, not from this package,
// so a phone or any other implementation can check itself offline against the same file, and a
// change to the domain string, the length prefix, the bit slicing or the word list fails here byte
// for byte.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

type sasVectors struct {
	Domain      string `json:"domain"`
	WordsSHA256 string `json:"words_sha256"`
	WordsCount  int    `json:"words_count"`
	Vectors     []struct {
		Name       string `json:"name"`
		Transcript struct {
			ClientKey   string `json:"client_key"`
			TowerKey    string `json:"tower_key"`
			ClientEph   string `json:"client_eph"`
			TowerEph    string `json:"tower_eph"`
			ClientNonce string `json:"client_nonce"`
			TowerNonce  string `json:"tower_nonce"`
		} `json:"transcript"`
		Phrase       string `json:"phrase"`
		ClientCommit string `json:"client_commit"`
	} `json:"vectors"`
	WrongDomain struct {
		Domain string `json:"domain"`
		Vector string `json:"vector"`
		Phrase string `json:"phrase"`
	} `json:"wrong_domain_phrase"`
}

func loadVectors(t *testing.T) sasVectors {
	t.Helper()
	raw, err := os.ReadFile("testdata/sas_vectors.json")
	require.NoError(t, err)
	var v sasVectors
	require.NoError(t, json.Unmarshal(raw, &v))
	require.NotEmpty(t, v.Vectors)
	return v
}

func TestThePhraseMatchesTheIndependentVectors(t *testing.T) {
	v := loadVectors(t)
	require.Equal(t, Domain, v.Domain, "the domain string the vectors were made with")
	raw, err := os.ReadFile("words.txt")
	require.NoError(t, err)
	sum := sha256.Sum256(raw)
	require.Equal(t, v.WordsSHA256, hex.EncodeToString(sum[:]), "the vectors were made with this exact word list")
	require.Equal(t, v.WordsCount, len(Words()))
	for _, c := range v.Vectors {
		tr := Transcript{ClientKey: c.Transcript.ClientKey, TowerKey: c.Transcript.TowerKey,
			ClientEph: c.Transcript.ClientEph, TowerEph: c.Transcript.TowerEph,
			ClientNonce: c.Transcript.ClientNonce, TowerNonce: c.Transcript.TowerNonce}
		require.Equal(t, c.Phrase, Phrase(tr), "phrase for %q", c.Name)
		require.Equal(t, c.ClientCommit, Commit(c.Transcript.ClientNonce), "commitment for %q", c.Name)
		require.NoError(t, CheckCommit(c.ClientCommit, c.Transcript.ClientNonce))
	}
}

// Mutation check on the domain: the vectors also record what the realistic phrase WOULD be under a
// different domain string. It must not be what this package produces, so a domain change cannot pass
// by accident, and the recorded value shows the derivation is otherwise identical.
func TestADifferentDomainGivesADifferentPhrase(t *testing.T) {
	v := loadVectors(t)
	require.NotEqual(t, Domain, v.WrongDomain.Domain)
	for _, c := range v.Vectors {
		if c.Name != v.WrongDomain.Vector {
			continue
		}
		tr := Transcript{ClientKey: c.Transcript.ClientKey, TowerKey: c.Transcript.TowerKey,
			ClientEph: c.Transcript.ClientEph, TowerEph: c.Transcript.TowerEph,
			ClientNonce: c.Transcript.ClientNonce, TowerNonce: c.Transcript.TowerNonce}
		require.NotEqual(t, v.WrongDomain.Phrase, Phrase(tr), "the phrase under domain %q must differ", v.WrongDomain.Domain)
		require.NotEmpty(t, v.WrongDomain.Phrase)
		return
	}
	t.Fatalf("the wrong-domain vector %q is not among the vectors", v.WrongDomain.Vector)
}
