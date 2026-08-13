package comp

// policy.go parses the one policy number the whole program turns on: the share rate, in parts
// per million, as it appears on the wire.
//
// Contract: features/tower/operator_revenue_share.feature ("Compensation rate wire validation
// covers the complete canonical boundary").

import (
	"errors"
	"strconv"
)

// ParsePPM validates a rate_ppm exactly as it must appear in signed policy bytes: a CANONICAL
// non-negative integer STRING in [0, 1000000]. The strictness is the point - a money rate read
// loosely is a rate an attacker can smuggle a second meaning into - so this rejects everything
// the spec's boundary table rejects and accepts only what it accepts:
//
//	accepted: "0", "1", "100000", "1000000" (and every canonical integer between)
//	rejected: "-1", "1000001" (out of range); "1.0", "1e6" (not an integer); "01", "+1" (not
//	          canonical); a JSON number 100000 or null or a missing field (not a string); and
//	          "9223372036854775808" / "18446744073709551616" (rejected BEFORE bounded conversion,
//	          i.e. on the canonical-form check, never by overflowing a parse).
//
// The input is the raw JSON string value (the decoded contents of "rate_ppm":"..."), so a caller
// that received a JSON number, null, or absence has nothing to pass here and rejects upstream.
func ParsePPM(s string) (uint32, error) {
	if !canonicalUint(s) {
		return 0, errors.New("comp: rate_ppm is not a canonical non-negative integer string")
	}
	// Canonical and within the digit budget below; ParseUint cannot fail here, but the range
	// check is what enforces the [0, 1000000] policy bound.
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil || n > PPMScale {
		return 0, errors.New("comp: rate_ppm out of range [0, 1000000]")
	}
	return uint32(n), nil
}

// canonicalUint reports whether s is the canonical decimal form of a non-negative integer: one or
// more digits, no sign, no leading zero unless the value is exactly "0", no point, no exponent,
// no whitespace. A short digit cap keeps a huge-but-canonical string from reaching ParseUint at
// all, so "9223372036854775808" is rejected on FORM (too many meaningful digits for the bound),
// matching "rejected before bounded conversion".
func canonicalUint(s string) bool {
	// 1000000 is 7 digits; any longer string cannot be in range and is refused HERE, on form,
	// before any numeric conversion - which is how "9223372036854775808" and
	// "18446744073709551616" are "rejected before bounded conversion".
	if s == "" || len(s) > 7 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	if len(s) > 1 && s[0] == '0' { // no leading zero except the single-digit "0"
		return false
	}
	return true
}
