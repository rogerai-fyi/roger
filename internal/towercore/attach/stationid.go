package attach

// stationid.go is the one definition of what a Station ID may be.
//
// # WHY THIS EXISTS, AND WHY IT IS HERE
//
// A Station ID is operator-supplied at invitation time, and it flows - unmodified - into the
// DNS name of the Station's edge TLS certificate (st-<id>.relay.<domain>). A security review
// found that an operator could invite a Station named "*" and be issued a wildcard
// certificate covering every other Station's relay name, held under the operator's own key -
// which is a total break of the edge path's confidentiality, since the whole model rests on
// the private key for a relay name living only on that one Station.
//
// So a Station ID is constrained to exactly the shape Core itself mints: "st-" followed by
// lowercase hex. That is narrow on purpose. It cannot contain a dot (which would inject extra
// DNS labels), a wildcard, whitespace, or anything a certificate name parser might treat
// specially - and validating it HERE, in the package every attachment goes through, means no
// caller can forget to.

import "regexp"

// stationIDPattern is a superset of the shape Core mints (newStationID: "st-"+randomHex) and
// the ONLY shape an operator may supply: "st-" then lowercase alphanumeric. The exact
// character set is the point, not the length - it can carry no dot (extra DNS label), no
// wildcard, no whitespace, no uppercase, nothing a DNS name or certificate SAN parser treats
// specially. Core's own ids (st-<hex>) are a subset, so nothing Core mints is ever refused.
var stationIDPattern = regexp.MustCompile(`^st-[a-z0-9]{1,64}$`)

// ValidStationID reports whether id is a well-formed Station ID.
//
// The point is not the length; it is that the character set can never carry a name-injection
// payload. A one-character suffix is allowed - a short id is harmless - but an empty one is
// not, because "st-.relay" would be a certificate name with an empty leftmost label.
func ValidStationID(id string) bool {
	return stationIDPattern.MatchString(id)
}
