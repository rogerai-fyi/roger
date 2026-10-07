package tower

import (
	"fmt"
	"net"

	"rogerai.fm/roger/v6/internal/netpriv"
)

// Egress control is what makes standalone isolation a PROOF rather than an omission.
//
// It is not enough that a standalone Tower contains no code to dial RogerAI. Anything
// this process connects to - a local database, a cache, an attached service - is a
// potential path off the machine, and a caller-supplied URL would turn the Tower into an
// open egress proxy. So every outbound destination is checked against a declared private
// allowlist, and caller-supplied targets are not fetched at all in v1.
//
// Two deliberate strictnesses:
//
//   - A destination must be a literal IP. A hostname is refused rather than resolved,
//     because resolving it IS the DNS lookup the Phase 1 gate says must not happen, and
//     because a name that resolves to an allowed address once can resolve to a forbidden
//     one on the next connection (DNS rebinding).
//   - Declaring an allowlist REPLACES the default rather than extending it, so an
//     operator who declares a range cannot accidentally keep a broader one they forgot.

// EgressGuard decides whether this Tower may connect to a destination.
type EgressGuard struct {
	allowed []*net.IPNet
}

// NewEgressGuard builds a guard. A nil or empty allowlist uses the private defaults.
func NewEgressGuard(allowed []*net.IPNet) *EgressGuard {
	if len(allowed) == 0 {
		// The default is internal/netpriv's home set: loopback, RFC 1918 and ULA. Notably absent
		// is link-local - it carries cloud instance-metadata endpoints, the classic pivot for
		// turning a relay into a credential thief.
		return &EgressGuard{allowed: netpriv.HomeCIDRs()}
	}
	return &EgressGuard{allowed: allowed}
}

// Allow reports whether a host:port destination is inside the declared private
// allowlist. Everything else - including every RogerAI address - is refused.
func (g *EgressGuard) Allow(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("destination %q is not a valid host:port", addr)
	}
	if port == "" {
		return fmt.Errorf("destination %q has no port", addr)
	}
	if _, perr := net.LookupPort("tcp", port); perr != nil {
		return fmt.Errorf("destination %q has an invalid port", addr)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		// Refused WITHOUT resolving: see the package comment.
		return fmt.Errorf("destination %q must be a literal IP inside the declared private allowlist; hostnames are not resolved", addr)
	}
	for _, n := range g.allowed {
		if n.Contains(ip) {
			return nil
		}
	}
	return fmt.Errorf("destination %s is outside this Tower's declared private allowlist", addr)
}

// AllowRequestTarget always refuses. A standalone Tower does not fetch a target chosen
// by a client or Station in v1 - with the caller choosing the destination, no allowlist
// can keep it from being an egress proxy.
func (g *EgressGuard) AllowRequestTarget(target string) error {
	return fmt.Errorf("this Tower does not fetch request-supplied targets (%q): v1 has no such route", target)
}
