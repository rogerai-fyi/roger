// Package joinsas is the matching phrase of a request-and-approve join (features/join/approve.feature):
// a short authentication string both screens show, derived from a transcript that covers both
// parties' long-term keys (the client's signing key and the key the Tower or Edge presented on
// this connection), both ephemeral X25519 keys and both fresh nonces. The client commits to its
// nonce before it sees the other side's, so a party in the middle cannot steer the phrase: it gets
// one guess per pairing at about 40 bits.
//
// The words are the fixed BIP-39 English list (words.txt, from python-mnemonic, MIT), the same on
// every platform. This package imports nothing from the rest of roger.
package joinsas

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Domain separates the phrase and the commitment from every other hash of these fields.
const Domain = "join-sas/v1"

const wordsSHA256 = "2f5eed53a4727b4bf8880d8f3f199efc90e58503646d9ff8eff3a2ed3b24dbda"

//go:embed words.txt
var wordsRaw string

var words = strings.Fields(wordsRaw)

// Words is the fixed list the phrase draws from.
func Words() []string { return words }

// ErrCommitMismatch is a revealed nonce that is not the one committed to.
var ErrCommitMismatch = errors.New("the revealed nonce does not match its commitment")

// Transcript is everything the phrase covers, each field hex.
type Transcript struct {
	ClientKey   string // the client's long-term signing key
	TowerKey    string // the key the Tower (or the Edge's root) presented on this connection
	ClientEph   string // the client's ephemeral X25519 public key
	TowerEph    string // the Tower's ephemeral X25519 public key
	ClientNonce string // revealed after the Tower's nonce was sent
	TowerNonce  string
}

// NewNonce is a fresh 32-byte nonce, hex.
func NewNonce() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("joinsas: no secure randomness: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// Commit is the client's commitment to its nonce, sent before it sees the other side's nonce.
func Commit(nonce string) string {
	sum := sha256.Sum256([]byte(Domain + "/commit\n" + nonce))
	return hex.EncodeToString(sum[:])
}

// CheckCommit verifies a revealed nonce against its commitment.
func CheckCommit(commit, nonce string) error {
	if subtle.ConstantTimeCompare([]byte(commit), []byte(Commit(nonce))) != 1 {
		return ErrCommitMismatch
	}
	return nil
}

// Phrase is the matching phrase: three words and two digits, joined by hyphens.
func Phrase(t Transcript) string {
	h := sha256.New()
	for _, f := range []string{Domain, t.ClientKey, t.TowerKey, t.ClientEph, t.TowerEph, t.ClientNonce, t.TowerNonce} {
		// Length-prefixed, so no field's bytes can be shifted into its neighbour.
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(f)))
		h.Write(n[:])
		h.Write([]byte(f))
	}
	sum := h.Sum(nil)
	v := binary.BigEndian.Uint64(sum[:8])
	w1, w2, w3 := v>>53&2047, v>>42&2047, v>>31&2047
	digits := (v & (1<<31 - 1)) % 100
	return fmt.Sprintf("%s-%s-%s-%02d", words[w1], words[w2], words[w3], digits)
}
