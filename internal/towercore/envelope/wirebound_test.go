package envelope

import (
	"testing"
)

// THE WIRE-ATTESTATION INVARIANT (P8): a marshaled sealed envelope is always at least as
// large as the plaintext it carries. Settlement evidence (the tower's wire counts) and the
// audit's tower-attribution rest on this being physically true - if envelope encoding ever
// gained compression or a compact binary framing that broke it, every honest settlement
// could be falsely disputed and honest towers falsely convicted. This test is the tripwire.
func TestSealedNeverSmallerThanPlaintext(t *testing.T) {
	pub, _, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	for _, size := range []int{0, 1, 16, 1024, 1 << 20} {
		plain := make([]byte, size)
		for i := range plain {
			plain[i] = byte(i) // arbitrary compressible content
		}
		sealed, err := SealTo(pub, plain, "att-x")
		if err != nil {
			t.Fatal(err)
		}
		raw, err := sealed.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		if len(raw) < len(plain) {
			t.Fatalf("sealed envelope (%d bytes) smaller than its plaintext (%d bytes) - the wire-attestation invariant is broken", len(raw), len(plain))
		}
	}
}
