package joinsas

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

// The word list is the fixed BIP-39 English list, byte for byte, so every platform shows the same
// words for the same pairing.
func TestTheWordListIsFixed(t *testing.T) {
	raw, err := os.ReadFile("words.txt")
	require.NoError(t, err)
	sum := sha256.Sum256(raw)
	require.Equal(t, wordsSHA256, hex.EncodeToString(sum[:]))
	require.Len(t, Words(), 2048)
}

func sample() Transcript {
	return Transcript{
		ClientKey: "aa", TowerKey: "bb", ClientEph: "cc", TowerEph: "dd",
		ClientNonce: "01", TowerNonce: "02",
	}
}

func TestThePhraseIsThreeWordsAndTwoDigits(t *testing.T) {
	p := Phrase(sample())
	require.Regexp(t, regexp.MustCompile(`^[a-z]+-[a-z]+-[a-z]+-[0-9]{2}$`), p)
	require.Equal(t, p, Phrase(sample()), "the same transcript gives the same phrase on both screens")
}

// A party in the middle swaps a key: the two ends' transcripts differ, and so do their phrases.
func TestAnyChangedKeyOrNonceChangesThePhrase(t *testing.T) {
	base := Phrase(sample())
	for name, mut := range map[string]func(*Transcript){
		"tower key":    func(x *Transcript) { x.TowerKey = "b0" },
		"client key":   func(x *Transcript) { x.ClientKey = "a0" },
		"client eph":   func(x *Transcript) { x.ClientEph = "c0" },
		"tower eph":    func(x *Transcript) { x.TowerEph = "d0" },
		"client nonce": func(x *Transcript) { x.ClientNonce = "00" },
		"tower nonce":  func(x *Transcript) { x.TowerNonce = "00" },
	} {
		x := sample()
		mut(&x)
		require.NotEqual(t, base, Phrase(x), "changing the %s must change the phrase", name)
	}
}

// The client commits to its nonce before it sees the Tower's; a reveal that does not match the
// commitment is refused.
func TestTheCommitmentBindsTheNonce(t *testing.T) {
	n := NewNonce()
	c := Commit(n)
	require.NoError(t, CheckCommit(c, n))
	require.ErrorIs(t, CheckCommit(c, NewNonce()), ErrCommitMismatch)
	require.ErrorIs(t, CheckCommit("zz", n), ErrCommitMismatch)
	require.NotEqual(t, NewNonce(), NewNonce())
}

// Field boundaries are unambiguous: moving bytes between fields changes the phrase.
func TestFieldsCannotBeShiftedIntoEachOther(t *testing.T) {
	a := Transcript{ClientKey: "ab", TowerKey: "c"}
	b := Transcript{ClientKey: "a", TowerKey: "bc"}
	require.NotEqual(t, Phrase(a), Phrase(b))
}
