package protocol

// stationid_test.go pins the three properties a derived Station id has to have: it is a function
// of the key and nothing else, it is in the alphabet a relay DNS name and an attach proof both
// require, and it is domain-separated from a bare hash of the same key.

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"testing"
)

// The alphabet attach.ValidStationID enforces, restated here rather than imported: the towercore
// package must not become a dependency of the SDK package for the sake of one regexp, and the
// pairing is asserted from the other side too (cmd/rogerai-broker's identity tests attach with
// derived ids, and the handler runs the real gate on them).
var derivedShape = regexp.MustCompile(`^st-[a-z0-9]{1,64}$`)

func TestADerivedStationIdIsAFunctionOfTheKeyAlone(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	mustBe(t, err == nil, "keygen")
	first := DeriveStationID(pub)
	mustBe(t, first == DeriveStationID(pub), "the same key must always mint the same id")

	other, _, err := ed25519.GenerateKey(rand.Reader)
	mustBe(t, err == nil, "keygen")
	mustBe(t, DeriveStationID(other) != first, "two keys must not mint one identity")

	// ONE FLIPPED BIT IS A DIFFERENT STATION. Not a property of the hash so much as a property of
	// the whole key being hashed - a truncation that dropped a byte of input would pass every
	// other assertion here.
	near := append(ed25519.PublicKey(nil), pub...)
	near[len(near)-1] ^= 0x01
	mustBe(t, DeriveStationID(near) != first, "a key differing in one bit minted the same id")
	near = append(ed25519.PublicKey(nil), pub...)
	near[0] ^= 0x01
	mustBe(t, DeriveStationID(near) != first, "a key differing in its FIRST bit minted the same id")
}

// THE ALPHABET IS CLOSED, which is what makes a derived id safe as the leftmost label of a relay
// DNS name (attach/stationid.go's name-injection gate) and what keeps the AttachProof statement
// free of separators.
func TestADerivedStationIdIsAlwaysAValidStationId(t *testing.T) {
	for i := 0; i < 200; i++ {
		pub, _, err := ed25519.GenerateKey(rand.Reader)
		mustBe(t, err == nil, "keygen")
		id := DeriveStationID(pub)
		if !derivedShape.MatchString(id) {
			t.Fatalf("derived %q, which is not a valid Station id", id)
		}
		if strings.ContainsAny(id, "\n\x00.*") {
			t.Fatalf("derived %q, which carries a byte a DNS name or a signing statement treats "+
				"specially", id)
		}
	}
}

// AND IT IS NOT THE BARE HASH OF THE KEY. The tag is cheap insurance: a plain sha256 of a public
// key is a value two subsystems arrive at independently and then have to keep equal forever.
func TestADerivedStationIdIsDomainTagged(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	mustBe(t, err == nil, "keygen")
	bare := sha256.Sum256(pub)
	mustBe(t, DeriveStationID(pub) != "st-"+hex.EncodeToString(bare[:12]),
		"the derivation must be domain-tagged, not a bare hash of the key")
	mustBe(t, strings.HasSuffix(stationIDDomain, "\x00"),
		"the tag must be NUL-terminated so no variable byte can extend it")
}
