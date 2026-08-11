// Package towerobj is the canonical encoding and signature suite every Tower-network
// application object shares - inventories, grants, leases, assertions, receipts.
//
// The requirement from the spec is not that an object round-trips, it is that TWO
// INDEPENDENT IMPLEMENTATIONS PRODUCE THE SAME BYTES. A signature is only checkable if both
// sides agree, byte for byte, on what was signed, so every rule here exists to remove one
// way for two encoders to disagree. Where a choice exists, this refuses the input rather
// than picking for the sender - normalising silently would change what a signature covers.
//
// THE RULES, and what each one is for:
//
//   - RFC 8785 JCS member ordering, on UTF-16 code units. Not Go's byte order: the two
//     agree on ASCII and diverge outside the BMP, which is the kind of bug that passes
//     every test and fails on real data.
//   - No duplicate members. Which one wins is implementation-defined, so a signature over
//     one is not a signature over the other.
//   - No explicit null. Absence is omission; two ways to say the same thing is one way to
//     disagree.
//   - NO JSON NUMBERS AT ALL. The spec requires every security, sequence, time, count,
//     rate and money integer to be a bounded base-10 string. Refusing numbers outright
//     also removes the hardest part of JCS - ECMAScript float formatting - from the
//     signing path entirely, and float formatting is exactly where implementations differ.
//   - Strings and member names must be NFC. The composed and decomposed forms are
//     different bytes for the same text.
//   - Signing bytes prepend network, object type and object version, and omit ONLY that
//     object's own signature member. The prefix stops a signature being lifted between
//     networks, types or versions; omitting only its own member means another party's
//     signature is part of what this one covers, so a relay cannot strip or swap it.
//
// No third-party JCS library. This is the signing path, the rules are pinned precisely
// enough to implement directly, and a dependency here would be supply-chain surface on the
// one thing every other guarantee rests on.
package towerobj

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// b64 is the one encoding used for every digest, public key and signature.
var b64 = base64.RawURLEncoding

// Canonical parses strictly and re-emits the object in canonical form.
func Canonical(raw []byte) ([]byte, error) {
	v, err := parseStrict(raw)
	if err != nil {
		return nil, err
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("a signed object must be a JSON object")
	}
	var b strings.Builder
	if err := writeValue(&b, obj); err != nil {
		return nil, err
	}
	return []byte(b.String()), nil
}

// CanonicalList canonicalizes an ORDERED LIST of strings.
//
// The spec derives several stable identities from "strict JCS [tag, network, id, revision]" -
// a JSON ARRAY, not an object - and Canonical above deliberately refuses anything that is not
// an object, because a signed object is always one. This is the same canonical writer applied
// to the other shape, so there stays exactly ONE implementation of what canonical means.
//
// Two implementations of that would be two implementations of every identity derived from it,
// and a disagreement about an identity is a disagreement about which attempt is which.
func CanonicalList(items []string) ([]byte, error) {
	vals := make([]any, 0, len(items))
	for _, it := range items {
		if err := checkString(it); err != nil {
			return nil, err
		}
		vals = append(vals, it)
	}
	var b strings.Builder
	if err := writeValue(&b, vals); err != nil {
		return nil, err
	}
	return []byte(b.String()), nil
}

// HashList is the digest of a canonical list, which is how a deterministic identity is
// derived from its parts.
func HashList(items []string) (string, error) {
	c, err := CanonicalList(items)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(c)
	return b64.EncodeToString(sum[:]), nil
}

// parseStrict decodes with every ambiguity refused.
func parseStrict(raw []byte) (any, error) {
	if len(raw) == 0 {
		return nil, errors.New("empty input")
	}
	if !utf8.Valid(raw) {
		return nil, errors.New("input is not valid UTF-8")
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	// UseNumber keeps numbers unparsed so they can be refused by TYPE rather than by
	// value - a float that happens to be integral is still a number.
	dec.UseNumber()

	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("not decodable JSON: %w", err)
	}
	// Anything after the first value is a second document, and which one was signed would
	// be a matter of opinion.
	if dec.More() {
		return nil, errors.New("trailing bytes after the object")
	}
	var rest [1]byte
	if n, _ := dec.Buffered().Read(rest[:]); n > 0 && !isSpace(rest[0]) {
		return nil, errors.New("trailing bytes after the object")
	}
	if err := check(v); err != nil {
		return nil, err
	}
	// encoding/json silently keeps the LAST duplicate, so duplicates have to be found in
	// the raw token stream rather than in the decoded value.
	if err := rejectDuplicates(raw); err != nil {
		return nil, err
	}
	return v, nil
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

// check walks the decoded value and refuses everything the format does not admit.
func check(v any) error {
	switch t := v.(type) {
	case nil:
		return errors.New("explicit null is not allowed: absence is omission")
	case json.Number:
		return fmt.Errorf("JSON number %s is not allowed: integers are base-10 strings", t.String())
	case string:
		return checkString(t)
	case bool:
		return nil
	case []any:
		for _, e := range t {
			if err := check(e); err != nil {
				return err
			}
		}
		return nil
	case map[string]any:
		for k, e := range t {
			if err := checkString(k); err != nil {
				return fmt.Errorf("member name %q: %w", k, err)
			}
			if err := check(e); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unsupported value of type %T", v)
	}
}

func checkString(s string) error {
	if !utf8.ValidString(s) {
		return errors.New("string is not valid UTF-8")
	}
	if !norm.NFC.IsNormalString(s) {
		return errors.New("string is not in Unicode NFC")
	}
	return nil
}

// frame tracks one open container while scanning for duplicate member names.
type frame struct {
	isObject  bool
	expectKey bool
	seen      map[string]bool
}

// rejectDuplicates re-scans the raw tokens, because encoding/json silently keeps the last
// duplicate and cannot report that it saw one.
//
// It tracks key/value alternation explicitly. Using Token() plus More() to guess which
// strings are keys does NOT work: More() is true for values as well, so a VALUE equal to
// an earlier key reads as a duplicate and a perfectly good object is refused.
func rejectDuplicates(raw []byte) error {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	var stack []*frame

	// valueDone is called after each complete value; inside an object it flips the
	// expectation back to a key.
	valueDone := func() {
		if n := len(stack); n > 0 && stack[n-1].isObject {
			stack[n-1].expectKey = true
		}
	}

	for {
		tok, err := dec.Token()
		if err != nil {
			break // structure was already validated by the decode above
		}
		if d, ok := tok.(json.Delim); ok {
			switch d {
			case '{':
				stack = append(stack, &frame{isObject: true, expectKey: true, seen: map[string]bool{}})
			case '[':
				stack = append(stack, &frame{})
			case '}', ']':
				if len(stack) > 0 {
					stack = stack[:len(stack)-1]
				}
				valueDone() // the container itself was a value in its parent
			}
			continue
		}
		// A scalar. In an object it is either the member name or its value.
		if n := len(stack); n > 0 && stack[n-1].isObject && stack[n-1].expectKey {
			name, _ := tok.(string)
			if stack[n-1].seen[name] {
				return fmt.Errorf("duplicate member %q", name)
			}
			stack[n-1].seen[name] = true
			stack[n-1].expectKey = false
			continue
		}
		valueDone()
	}
	return nil
}

// writeValue emits canonical bytes.
func writeValue(b *strings.Builder, v any) error {
	switch t := v.(type) {
	case string:
		writeString(b, t)
	case bool:
		if t {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case []any:
		b.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := writeValue(b, e); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return lessUTF16(keys[i], keys[j]) })
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			writeString(b, k)
			b.WriteByte(':')
			if err := writeValue(b, t[k]); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	default:
		return fmt.Errorf("unsupported value of type %T", v)
	}
	return nil
}

// lessUTF16 orders two strings by their UTF-16 code units, which is what JCS specifies.
func lessUTF16(a, b string) bool {
	ua, ub := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < len(ua) && i < len(ub); i++ {
		if ua[i] != ub[i] {
			return ua[i] < ub[i]
		}
	}
	return len(ua) < len(ub)
}

// writeString emits a JCS string: the two mandatory escapes, the C0 controls in their
// short form where one exists, and everything else literal.
func writeString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(b, `\u%04x`, r)
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
}

// signingBytes are what a signature actually covers: the domain, then the canonical object
// with only its own signature member removed.
func signingBytes(network, objType string, version int, obj map[string]any, sigMember string) ([]byte, error) {
	stripped := make(map[string]any, len(obj))
	for k, v := range obj {
		if k == sigMember {
			continue
		}
		stripped[k] = v
	}
	var b strings.Builder
	// The domain is length-prefixed by its separators so no combination of network, type
	// and version can be confused with another - "ab"+"c" must not equal "a"+"bc".
	b.WriteString("rogerobj-v1\x00")
	b.WriteString(network)
	b.WriteString("\x00")
	b.WriteString(objType)
	b.WriteString("\x00")
	b.WriteString(strconv.Itoa(version))
	b.WriteString("\x00")
	if err := writeValue(&b, stripped); err != nil {
		return nil, err
	}
	return []byte(b.String()), nil
}

// Sign returns the object in canonical form carrying its signature.
func Sign(priv ed25519.PrivateKey, network, objType string, version int, raw []byte, sigMember string) ([]byte, error) {
	v, err := parseStrict(raw)
	if err != nil {
		return nil, err
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("a signed object must be a JSON object")
	}
	msg, err := signingBytes(network, objType, version, obj, sigMember)
	if err != nil {
		return nil, err
	}
	obj[sigMember] = b64.EncodeToString(ed25519.Sign(priv, msg))

	var b strings.Builder
	if err := writeValue(&b, obj); err != nil {
		return nil, err
	}
	return []byte(b.String()), nil
}

// Verify checks the signature in sigMember against the rest of the object.
func Verify(pub ed25519.PublicKey, network, objType string, version int, raw []byte, sigMember string) error {
	v, err := parseStrict(raw)
	if err != nil {
		return err
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return errors.New("a signed object must be a JSON object")
	}
	sigStr, ok := obj[sigMember].(string)
	if !ok || sigStr == "" {
		return fmt.Errorf("object carries no %s", sigMember)
	}
	sig, err := b64.DecodeString(sigStr)
	if err != nil {
		return fmt.Errorf("%s is not unpadded base64url: %w", sigMember, err)
	}
	if len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("%s is not an Ed25519 signature", sigMember)
	}
	msg, err := signingBytes(network, objType, version, obj, sigMember)
	if err != nil {
		return err
	}
	if len(pub) != ed25519.PublicKeySize || !ed25519.Verify(pub, msg, sig) {
		return errors.New("signature does not verify")
	}
	return nil
}

// Hash is the COMPLETE-object digest: canonical bytes including the signature member.
// Signing omits the signature; hashing includes it, which is what lets a later object bind
// "this exact signed thing" rather than "something that says the same".
func Hash(raw []byte) (string, error) {
	c, err := Canonical(raw)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(c)
	return b64.EncodeToString(sum[:]), nil
}

// ParseInt reads a bounded canonical base-10 integer string. One shape only: no leading
// zero, no plus, no negative zero, no whitespace, nothing beyond int64. Two encoders that
// both accept "01" and "1" do not agree on what was signed.
func ParseInt(s string) (int64, error) {
	if s == "" {
		return 0, errors.New("empty integer")
	}
	body := strings.TrimPrefix(s, "-")
	if body == "" || (len(body) > 1 && body[0] == '0') {
		return 0, fmt.Errorf("%q is not a canonical integer", s)
	}
	if s == "-0" {
		return 0, errors.New(`"-0" is not a canonical integer`)
	}
	for i := 0; i < len(body); i++ {
		if body[i] < '0' || body[i] > '9' {
			return 0, fmt.Errorf("%q is not a canonical integer", s)
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is out of range: %w", s, err)
	}
	return n, nil
}

// FormatInt is the inverse: the one accepted shape.
func FormatInt(n int64) string { return strconv.FormatInt(n, 10) }
