package protocol

// attachproof_test.go proves the two things the attach proof has to be: bound to ONE request,
// and impossible to confuse with either of the other two byte-spaces the same Ed25519 key signs
// in (protocol.CanonicalRequest, and towerobj's receipts).
//
// The domain-separation half is tested STRUCTURALLY rather than by trying a handful of
// collisions, because a handful of collisions is what an attacker looks for and a test can only
// ever fail to find one. What is asserted instead are the two invariants the argument in
// attachproof.go rests on - a disjoint prefix, and the absence of a byte every CanonicalRequest
// must contain - each of which holds for ALL inputs rather than for the ones somebody thought of.

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
)

func testProof() AttachProof {
	return AttachProof{
		Network:      "roger-public",
		CallerPubkey: "aa" + strings.Repeat("bb", 31),
		TS:           1755000000,
		StationID:    "st-0123456789abcdef",
		AssertionKey: strings.Repeat("cc", 32),
		SessionKey:   strings.Repeat("dd", 32),
		Body:         []byte(`{"model":"m"}`),
	}
}

// The verifier takes its public key from the CLAIM, not from the signer - which is the entire
// property. A signature by any other key is refused however well-formed it is.
func TestAnAttachProofVerifiesOnlyUnderTheKeyItNames(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	mustBe(t, err == nil, "keygen")
	other, otherPriv, err := ed25519.GenerateKey(rand.Reader)
	mustBe(t, err == nil, "keygen")

	p := testProof()
	p.AssertionKey = hexEncode(pub)
	mustBe(t, p.Verify(p.Sign(priv)), "the named key's own signature verifies")
	mustBe(t, !p.Verify(p.Sign(otherPriv)), "a signature by a key the proof does not name is refused")

	// And naming the other key does not rescue the first signature either: claim and proof move
	// together or not at all.
	q := p
	q.AssertionKey = hexEncode(other)
	mustBe(t, !q.Verify(p.Sign(priv)), "swapping the claim invalidates the proof")
	mustBe(t, q.Verify(q.Sign(otherPriv)), "and the matching pair still works")
}

// EVERY FIELD IS LOAD-BEARING. A field that is in the struct but not in the bytes is the exact
// shape of defect this proof exists to close one layer up - a check that exists and covers less
// than a reader thinks. Each mutation below must invalidate a signature made before it.
func TestEveryFieldOfAnAttachProofIsSigned(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	mustBe(t, err == nil, "keygen")
	base := testProof()
	base.AssertionKey = hexEncode(pub)
	sig := base.Sign(priv)
	mustBe(t, base.Verify(sig), "the unmutated proof verifies")

	mutations := map[string]func(p *AttachProof){
		"network":       func(p *AttachProof) { p.Network = "roger-standalone" },
		"caller pubkey": func(p *AttachProof) { p.CallerPubkey = strings.Repeat("ee", 32) },
		"timestamp":     func(p *AttachProof) { p.TS++ },
		"station id":    func(p *AttachProof) { p.StationID = "st-deadbeef" },
		"session key":   func(p *AttachProof) { p.SessionKey = strings.Repeat("ff", 32) },
		"body":          func(p *AttachProof) { p.Body = []byte(`{"model":"m2"}`) },
	}
	for name, mutate := range mutations {
		p := base
		mutate(&p)
		if p.Verify(sig) {
			t.Fatalf("%s is not covered by the signature: a proof survived changing it", name)
		}
	}
}

// SEPARATION FROM protocol.CanonicalRequest, BY SHAPE.
//
// CanonicalRequest is method + "\n" + path + "\n" + ts + "\n" + digest, so every string it can
// ever produce contains at least three line feeds. An attach statement contains none: its
// separator is NUL and every field is drawn from an alphabet that cannot carry one. A byte
// string with no LF is therefore not in the image of CanonicalRequest for ANY input - which is
// what makes the separation independent of what the method happens to be, and it matters
// because the door signature (internal/towerhub) already puts a label where a method belongs.
func TestAnAttachStatementIsNotACanonicalRequestForAnyInput(t *testing.T) {
	p := testProof()
	if strings.ContainsRune(string(p.statement()), '\n') {
		t.Fatal("an attach statement must contain no line feed - that absence IS the separation " +
			"from protocol.CanonicalRequest, which always contains at least three")
	}
	// The other half of the same claim, stated against the real function rather than assumed.
	for _, m := range []string{"GET", "POST", "roger-hub-door-v1 GET", "", "rogerai-station-attach-proof-v1"} {
		if n := strings.Count(CanonicalRequest(m, "/poll?nonce=x", 1755000000, nil), "\n"); n < 3 {
			t.Fatalf("CanonicalRequest(%q) produced %d line feeds, not the >=3 the argument needs", m, n)
		}
	}
	// So a signature over a hub request can never verify as an attach proof, and the reverse.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	mustBe(t, err == nil, "keygen")
	p.AssertionKey = hexEncode(pub)
	reqSig := hexEncode(ed25519.Sign(priv, []byte(CanonicalRequest("POST", "/tower/edge/attach", p.TS, p.Body))))
	mustBe(t, !p.Verify(reqSig), "a hub-request signature is not an attach proof")
	_, ok := VerifyRequest(hexEncode(pub), p.Sign(priv), p.TS, "POST", "/tower/edge/attach", p.Body)
	mustBe(t, !ok, "an attach proof is not a hub-request signature")
}

// SEPARATION FROM towerobj, BY PREFIX. Both spaces open with a fixed, NUL-terminated tag; the
// two tags differ before any variable byte and neither is a prefix of the other, so no
// combination of network, object type and version can bridge them. Spelled out here rather than
// imported, because importing towerobj into the SDK package to assert a constant would create a
// dependency for the sake of a string.
func TestAnAttachStatementCannotBeReadAsASignedObject(t *testing.T) {
	const towerobjDomain = "rogerobj-v1\x00"
	stmt := string(testProof().statement())
	if !strings.HasPrefix(stmt, attachProofDomain) {
		t.Fatal("the statement must open with its own domain tag")
	}
	if strings.HasPrefix(stmt, towerobjDomain) {
		t.Fatal("an attach statement must not open with towerobj's domain")
	}
	if strings.HasPrefix(attachProofDomain, towerobjDomain) || strings.HasPrefix(towerobjDomain, attachProofDomain) {
		t.Fatal("neither domain tag may be a prefix of the other, or a truncated reading could bridge them")
	}
}

func mustBe(t *testing.T, cond bool, what string) {
	t.Helper()
	if !cond {
		t.Fatalf("expected: %s", what)
	}
}

func hexEncode(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&0x0f])
	}
	return string(out)
}

// THE SEPARATION SURVIVES A FIELD THAT CARRIES A SEPARATOR, and this is the invariant that was
// silently doing the work while a different one was written down.
//
// The argument above says an attach statement contains no line feed, so it cannot be in the
// image of CanonicalRequest. That is true only if every caller validates every field, and one
// did not: cmd/rogerai-broker/toweredgeattach.go validated the TRIMMED Station id and put the
// RAW one in the statement, so "\n\n\nst-x\n" reached these bytes and the alphabet was not
// closed. The separation held anyway, by a property nobody had claimed: this statement always
// ends "...\x00<assertion>\x00<session>\x00<digest>", so whatever follows its LAST line feed
// carries at least one NUL, while CanonicalRequest always ends with a BARE HEX digest after its
// last line feed and hex can never contain a NUL.
//
// So this is the assertion that must hold for ANY input, including the ones a caller should
// never send. It also fails the moment somebody reorders the fields and the digest stops being
// last, which is the drift it exists to catch.
func TestAnAttachStatementStaysOutOfCanonicalRequestEvenWithSeparatorsInItsFields(t *testing.T) {
	hostile := AttachProof{
		Network:      "roger\npublic",
		CallerPubkey: "aa\nbb",
		TS:           1755000000,
		StationID:    "\n\n\nst-advlf\n",
		AssertionKey: "cc\ndd",
		SessionKey:   "ee\nff",
		Body:         []byte("{\n}\n"),
	}
	stmt := string(hostile.statement())
	tail := stmt[strings.LastIndex(stmt, "\n")+1:]
	if !strings.ContainsRune(tail, 0) {
		t.Fatalf("nothing after this statement's last line feed carries a NUL (%q), so it could be "+
			"read as a CanonicalRequest's trailing digest - the digest is no longer last", tail)
	}
	// And the other half, against the real function: what follows CanonicalRequest's last line
	// feed is bare hex, which can never carry a NUL.
	for _, m := range []string{"GET", "POST", "roger-hub-door-v1 GET", ""} {
		cr := CanonicalRequest(m, "/poll?nonce=\x00x", 1755000000, []byte("\x00"))
		crTail := cr[strings.LastIndex(cr, "\n")+1:]
		if strings.ContainsRune(crTail, 0) {
			t.Fatalf("CanonicalRequest(%q) ends %q, which carries a NUL - the tail argument no "+
				"longer separates the two spaces", m, crTail)
		}
	}
	// The two together: a signature over one is never valid over the other, even here.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	mustBe(t, err == nil, "keygen")
	hostile.AssertionKey = hexEncode(pub)
	reqSig := hexEncode(ed25519.Sign(priv, []byte(CanonicalRequest("POST", "/tower/edge/attach", hostile.TS, hostile.Body))))
	mustBe(t, !hostile.Verify(reqSig), "a hub-request signature verified as an attach proof")
}
