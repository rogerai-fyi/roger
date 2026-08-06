package towerobj

// Canonical bytes and signatures for every Tower-network application object, per
// features/tower/receipt_v2.feature ("Signed-object v1 has one canonical JSON and
// signature suite").
//
// This is the foundation the inventory, grants, leases and receipts all sit on, so it is
// built first. The property that matters is not "it round-trips" but "two independent
// implementations produce the SAME BYTES" - a signature is only checkable if both sides
// agree, byte for byte, on what was signed. Every rule below exists to remove one way for
// two encoders to disagree.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func newKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return pub, priv
}

// --- canonical form --------------------------------------------------------

func TestMembersAreOrderedRegardlessOfInputOrder(t *testing.T) {
	// The whole point: semantically identical input, identical bytes out.
	a, err := Canonical([]byte(`{"b":"2","a":"1","c":"3"}`))
	require.NoError(t, err)
	b, err := Canonical([]byte(`{"c":"3","a":"1","b":"2"}`))
	require.NoError(t, err)
	require.Equal(t, string(a), string(b))
	require.Equal(t, `{"a":"1","b":"2","c":"3"}`, string(a))
}

func TestOrderingIsByUTF16CodeUnitAsJCSRequires(t *testing.T) {
	// JCS orders on UTF-16 code units, which is NOT the same as Go's byte-wise string
	// order once you leave the BMP. Getting this wrong produces bytes that agree with
	// every ASCII test and disagree with a conforming implementation on real data.
	out, err := Canonical([]byte(`{"é":"e","z":"z","😀":"emoji","a":"a"}`))
	require.NoError(t, err)
	// a < z < é(U+00E9) < 😀(surrogate pair D83D DE00)
	require.Equal(t, `{"a":"a","z":"z","é":"e","😀":"emoji"}`, string(out))
}

func TestWhitespaceIsRemoved(t *testing.T) {
	out, err := Canonical([]byte("{\n  \"a\" : \"1\" ,\n  \"b\" : [ \"x\" , \"y\" ]\n}"))
	require.NoError(t, err)
	require.Equal(t, `{"a":"1","b":["x","y"]}`, string(out))
}

func TestArrayOrderIsPreserved(t *testing.T) {
	// Members are sorted; ARRAY elements are not - their order is meaning.
	out, err := Canonical([]byte(`{"xs":["c","a","b"]}`))
	require.NoError(t, err)
	require.Equal(t, `{"xs":["c","a","b"]}`, string(out))
}

func TestNestedObjectsAreCanonicalisedToo(t *testing.T) {
	out, err := Canonical([]byte(`{"outer":{"b":"2","a":"1"},"arr":[{"z":"1","y":"2"}]}`))
	require.NoError(t, err)
	require.Equal(t, `{"arr":[{"y":"2","z":"1"}],"outer":{"a":"1","b":"2"}}`, string(out))
}

// --- what strict parsing must refuse ---------------------------------------

func TestStrictParsingRefusesWhatWouldMakeTwoEncodersDisagree(t *testing.T) {
	for name, in := range map[string]string{
		// Which duplicate wins is implementation-defined, so a signature over one is not a
		// signature over the other.
		"a duplicate member": `{"a":"1","a":"2"}`,
		"trailing bytes":     `{"a":"1"} {"b":"2"}`,
		"trailing garbage":   `{"a":"1"}xx`,
		// Absence is omission. Explicit null is a second way to say the same thing.
		"explicit null":    `{"a":null}`,
		"null nested":      `{"a":{"b":null}}`,
		"null in an array": `{"a":[null]}`,
		// Every integer is a bounded base-10 STRING. Allowing JSON numbers would drag in
		// float formatting - the one part of JCS implementations reliably disagree on.
		"a JSON number":     `{"n":1}`,
		"a float":           `{"n":1.5}`,
		"a big number":      `{"n":10000000000000000000000}`,
		"exponent notation": `{"n":1e3}`,
		"a number nested":   `{"a":{"n":1}}`,
		"a number in array": `{"a":[1]}`,
		"not an object":     `["a"]`,
		"a bare string":     `"a"`,
		"empty input":       ``,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Canonical([]byte(in))
			require.Error(t, err, "%s must be refused", name)
		})
	}
}

func TestInvalidUTF8IsRefused(t *testing.T) {
	_, err := Canonical([]byte{'{', '"', 'a', '"', ':', '"', 0xff, 0xfe, '"', '}'})
	require.Error(t, err)
}

func TestStringsOutsideNFCAreRefused(t *testing.T) {
	// "é" as e + combining acute is a DIFFERENT byte sequence from the composed form, and
	// normalising silently would change what a signature covers. Refusing means the sender
	// must decide, not us.
	decomposed := "é"
	_, err := Canonical([]byte(`{"a":"` + decomposed + `"}`))
	require.Error(t, err, "a decomposed string must be refused rather than normalised")

	composed := "é"
	_, err = Canonical([]byte(`{"a":"` + composed + `"}`))
	require.NoError(t, err, "the composed form is fine")

	_, err = Canonical([]byte(`{"` + decomposed + `":"x"}`))
	require.Error(t, err, "member names are held to it too")
}

func TestBooleansAndEmptyContainersSurvive(t *testing.T) {
	// Not everything is refused - the rules target ambiguity, not expressiveness.
	out, err := Canonical([]byte(`{"t":true,"f":false,"o":{},"a":[]}`))
	require.NoError(t, err)
	require.Equal(t, `{"a":[],"f":false,"o":{},"t":true}`, string(out))
}

func TestStringEscapingHasOneShape(t *testing.T) {
	// JCS escapes the minimum: the two mandatory characters and the C0 controls, using
	// the short forms where they exist. Anything else - including non-ASCII - is emitted
	// literally, so an encoder that escapes more produces different bytes for the same text.
	in := "{\"a\":\"q\\\"b\\\\s\\n\\t\u00e9\U0001F600\"}"
	out, err := Canonical([]byte(in))
	require.NoError(t, err)
	require.Equal(t, "{\"a\":\"q\\\"b\\\\s\\n\\t\u00e9\U0001F600\"}", string(out))

	// A raw control character in the input is not valid JSON at all.
	_, err = Canonical([]byte("{\"a\":\"x\ny\"}"))
	require.Error(t, err)
}

// --- signing ---------------------------------------------------------------

const (
	net  = "roger-public"
	typ  = "TowerInventoryV1"
	vers = 1
)

func TestSignAndVerifyRoundTrip(t *testing.T) {
	pub, priv := newKey(t)
	obj := []byte(`{"revision":"41","tower_id":"tw-1"}`)

	signed, err := Sign(priv, net, typ, vers, obj, "sig")
	require.NoError(t, err)
	require.NoError(t, Verify(pub, net, typ, vers, signed, "sig"))
}

func TestTheSignatureCoversTheContent(t *testing.T) {
	pub, priv := newKey(t)
	signed, err := Sign(priv, net, typ, vers, []byte(`{"revision":"41"}`), "sig")
	require.NoError(t, err)

	tampered := strings.Replace(string(signed), `"41"`, `"42"`, 1)
	require.Error(t, Verify(pub, net, typ, vers, []byte(tampered), "sig"),
		"changing a signed field must break the signature")
}

func TestASignatureIsBoundToItsDomain(t *testing.T) {
	// The domain prefix is what stops a signature being lifted between networks, object
	// types, or versions. Same bytes, same key, different meaning - and it must not verify.
	pub, priv := newKey(t)
	obj := []byte(`{"revision":"41"}`)
	signed, err := Sign(priv, net, typ, vers, obj, "sig")
	require.NoError(t, err)

	require.Error(t, Verify(pub, "local-9f", typ, vers, signed, "sig"),
		"another network must not accept it")
	require.Error(t, Verify(pub, net, "TowerGrantV1", vers, signed, "sig"),
		"another object type must not accept it")
	require.Error(t, Verify(pub, net, typ, 2, signed, "sig"),
		"another object version must not accept it")
}

func TestAnotherKeyDoesNotVerify(t *testing.T) {
	_, priv := newKey(t)
	other, _ := newKey(t)
	signed, err := Sign(priv, net, typ, vers, []byte(`{"a":"1"}`), "sig")
	require.NoError(t, err)
	require.Error(t, Verify(other, net, typ, vers, signed, "sig"))
}

func TestSigningOmitsOnlyItsOwnSignatureMember(t *testing.T) {
	// An object carrying somebody else's signature must have THAT signature covered by
	// this one - otherwise a relay could strip or swap a Station's signature and the
	// Tower's signature would still verify.
	pub, priv := newKey(t)
	obj := []byte(`{"station_sig":"AAAA","revision":"41"}`)
	signed, err := Sign(priv, net, typ, vers, obj, "tower_sig")
	require.NoError(t, err)
	require.NoError(t, Verify(pub, net, typ, vers, signed, "tower_sig"))

	swapped := strings.Replace(string(signed), `"AAAA"`, `"BBBB"`, 1)
	require.Error(t, Verify(pub, net, typ, vers, []byte(swapped), "tower_sig"),
		"another party's signature is part of what this one covers")
}

func TestVerifyRefusesAnObjectWithNoSignature(t *testing.T) {
	pub, _ := newKey(t)
	require.Error(t, Verify(pub, net, typ, vers, []byte(`{"a":"1"}`), "sig"))
}

func TestVerifyRefusesAMalformedSignature(t *testing.T) {
	pub, _ := newKey(t)
	for _, bad := range []string{`""`, `"!!!not base64url!!!"`, `"AAAA"`, `{}`} {
		require.Error(t, Verify(pub, net, typ, vers,
			[]byte(`{"a":"1","sig":`+bad+`}`), "sig"))
	}
}

func TestSignatureIsUnpaddedBase64URL(t *testing.T) {
	_, priv := newKey(t)
	signed, err := Sign(priv, net, typ, vers, []byte(`{"a":"1"}`), "sig")
	require.NoError(t, err)
	sig := extract(t, signed, "sig")
	require.NotContains(t, sig, "=", "unpadded")
	require.NotContains(t, sig, "+", "base64URL alphabet, not standard")
	require.NotContains(t, sig, "/", "base64URL alphabet, not standard")
}

func TestSigningAnObjectThatAlreadyCarriesThatMemberReplacesIt(t *testing.T) {
	// Re-signing must not leave a stale signature behind, and must not sign over its own
	// previous one.
	pub, priv := newKey(t)
	first, err := Sign(priv, net, typ, vers, []byte(`{"a":"1"}`), "sig")
	require.NoError(t, err)
	second, err := Sign(priv, net, typ, vers, first, "sig")
	require.NoError(t, err)
	require.Equal(t, string(first), string(second), "signing is deterministic and idempotent")
	require.NoError(t, Verify(pub, net, typ, vers, second, "sig"))
}

// --- complete-object hash --------------------------------------------------

func TestTheCompleteHashIncludesTheSignature(t *testing.T) {
	// The signing bytes omit the signature; the OBJECT hash includes it. That is what lets
	// a later object bind "this exact signed thing" rather than "something that says the
	// same".
	_, priv := newKey(t)
	obj := []byte(`{"a":"1"}`)
	signed, err := Sign(priv, net, typ, vers, obj, "sig")
	require.NoError(t, err)

	unsignedHash, err := Hash(obj)
	require.NoError(t, err)
	signedHash, err := Hash(signed)
	require.NoError(t, err)
	require.NotEqual(t, unsignedHash, signedHash)
}

func TestTheHashIsStableAcrossInputOrder(t *testing.T) {
	a, err := Hash([]byte(`{"b":"2","a":"1"}`))
	require.NoError(t, err)
	b, err := Hash([]byte(`{"a":"1","b":"2"}`))
	require.NoError(t, err)
	require.Equal(t, a, b)
}

func TestTheHashIsUnpaddedBase64URL(t *testing.T) {
	h, err := Hash([]byte(`{"a":"1"}`))
	require.NoError(t, err)
	require.NotContains(t, h, "=")
	require.Len(t, h, 43, "sha256 as unpadded base64url")
}

// --- bounded integers ------------------------------------------------------

func TestIntegerStringsHaveOneAcceptedShape(t *testing.T) {
	for name, in := range map[string]string{
		"a leading zero": "01",
		"a plus sign":    "+1",
		"negative zero":  "-0",
		"whitespace":     " 1",
		"empty":          "",
		"not a number":   "x",
		"a float":        "1.0",
		"exponent":       "1e3",
		"beyond int64":   "9223372036854775808",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseInt(in)
			require.Error(t, err, "%q must be refused", in)
		})
	}
	for _, ok := range []string{"0", "1", "-1", "41", "9223372036854775807"} {
		v, err := ParseInt(ok)
		require.NoError(t, err, ok)
		require.Equal(t, ok, FormatInt(v), "and it round-trips to one shape")
	}
}

func extract(t *testing.T, obj []byte, member string) string {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(obj, &m))
	s, ok := m[member].(string)
	require.True(t, ok, "%s must be a string", member)
	return s
}

// --- the remaining edges ----------------------------------------------------

func TestTrailingWhitespaceIsFineButTrailingContentIsNot(t *testing.T) {
	// A trailing newline is how files end; a trailing VALUE is a second document, and
	// which one was signed would be a matter of opinion.
	out, err := Canonical([]byte("{\"a\":\"1\"}\n  \t\r\n"))
	require.NoError(t, err)
	require.Equal(t, `{"a":"1"}`, string(out))

	_, err = Canonical([]byte("{\"a\":\"1\"}\n{\"b\":\"2\"}"))
	require.Error(t, err)
}

func TestControlCharactersAreEscapedInTheirShortForm(t *testing.T) {
	// \b and \f have short forms; other C0 controls take \u00xx. An encoder that picks a
	// different spelling produces different bytes for the same text.
	in := "{\"a\":\"\\b\\f\\u0001\\u001f\"}"
	out, err := Canonical([]byte(in))
	require.NoError(t, err)
	require.Equal(t, "{\"a\":\"\\b\\f\\u0001\\u001f\"}", string(out))
}

func TestOrderingFallsBackToLengthOnASharedPrefix(t *testing.T) {
	out, err := Canonical([]byte(`{"ab":"2","a":"1","abc":"3"}`))
	require.NoError(t, err)
	require.Equal(t, `{"a":"1","ab":"2","abc":"3"}`, string(out))
}

func TestSignRefusesInputItCannotCanonicalise(t *testing.T) {
	// A signature over bytes we could not agree on is worse than no signature.
	_, priv := newKey(t)
	for _, bad := range []string{`{"n":1}`, `{"a":null}`, `["x"]`, ``, `{"a":"1","a":"2"}`} {
		_, err := Sign(priv, net, typ, vers, []byte(bad), "sig")
		require.Error(t, err, "%q", bad)
	}
}

func TestVerifyRefusesInputItCannotCanonicalise(t *testing.T) {
	pub, _ := newKey(t)
	require.Error(t, Verify(pub, net, typ, vers, []byte(`{"n":1,"sig":"AAAA"}`), "sig"))
	require.Error(t, Verify(pub, net, typ, vers, []byte(`not json`), "sig"))
}

func TestVerifyRefusesAKeyOfTheWrongSize(t *testing.T) {
	_, priv := newKey(t)
	signed, err := Sign(priv, net, typ, vers, []byte(`{"a":"1"}`), "sig")
	require.NoError(t, err)
	require.Error(t, Verify(ed25519.PublicKey("short"), net, typ, vers, signed, "sig"))
}

func TestVerifyRefusesANonStringSignatureMember(t *testing.T) {
	pub, _ := newKey(t)
	require.Error(t, Verify(pub, net, typ, vers, []byte(`{"a":"1","sig":true}`), "sig"))
}

func TestHashRefusesInputItCannotCanonicalise(t *testing.T) {
	_, err := Hash([]byte(`{"n":1}`))
	require.Error(t, err)
}

func TestEmptyObjectsAndDeepNestingCanonicalise(t *testing.T) {
	out, err := Canonical([]byte(`{}`))
	require.NoError(t, err)
	require.Equal(t, `{}`, string(out))

	out, err = Canonical([]byte(`{"a":{"b":{"c":[{"e":"1","d":"2"}]}}}`))
	require.NoError(t, err)
	require.Equal(t, `{"a":{"b":{"c":[{"d":"2","e":"1"}]}}}`, string(out))
}

func TestADuplicateNestedInsideAnArrayIsCaught(t *testing.T) {
	// The scanner has to track object frames through arrays too, or a duplicate hides one
	// level down.
	_, err := Canonical([]byte(`{"xs":[{"a":"1","a":"2"}]}`))
	require.Error(t, err)

	// ...and a value that merely EQUALS a key elsewhere is not a duplicate. This is the
	// false positive the first version of the scanner produced.
	out, err := Canonical([]byte(`{"a":"a","xs":["a","a"],"b":{"a":"a"}}`))
	require.NoError(t, err)
	require.Equal(t, `{"a":"a","b":{"a":"a"},"xs":["a","a"]}`, string(out))
}
