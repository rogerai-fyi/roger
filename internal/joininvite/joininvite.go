// Package joininvite is the one invite every local join uses (features/join/invite.feature):
//
//	roger://join?kind=<tower-client|tower-station|edge>&at=<host:port>[&at=...]&fp=sha256:<hex>&code=<code>[&for=<name>]
//
// It carries the addresses to try in order, the FULL identity fingerprint (a Tower's TLS key, or
// an Edge root), the one-time code, and optionally the one machine name it is for. Edge invites
// from before this change ("rogerai-edge://join?at=...&root=...&code=...") are still read, as
// kind edge.
//
// It imports nothing from roger beyond internal/joincode, so the Core-free serve path may use it.
package joininvite

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"

	"rogerai.fm/roger/v6/internal/joincode"
)

// Kind is what an invite lets its holder do.
type Kind string

const (
	TowerClient  Kind = "tower-client"
	TowerStation Kind = "tower-station"
	Edge         Kind = "edge"
)

// Scheme is the invite's URL scheme; legacyEdgeScheme is the Edge-only scheme it replaces.
const (
	Scheme           = "roger"
	legacyEdgeScheme = "rogerai-edge"
)

// ErrBadInvite is anything that is not a usable invite.
var ErrBadInvite = errors.New("that is not a usable invite")

// Invite is one decoded invite. FP is the fingerprint as 64 lowercase hex; Code is canonical.
type Invite struct {
	Kind  Kind
	Addrs []string
	FP    string
	Code  string
	For   string
}

// Domain is the join-code domain this kind uses.
func (k Kind) Domain() joincode.Domain {
	if k == Edge {
		return joincode.Edge
	}
	return joincode.Tower
}

// IsTower reports whether the invite joins a Tower (as a client or a Station).
func (k Kind) IsTower() bool { return k == TowerClient || k == TowerStation }

// String encodes the invite in the one scheme.
func (in Invite) String() string {
	v := url.Values{}
	for _, a := range in.Addrs {
		v.Add("at", a)
	}
	v.Set("fp", "sha256:"+in.FP)
	if in.Code != "" {
		v.Set("code", in.Code)
	}
	if in.For != "" {
		v.Set("for", in.For)
	}
	// kind goes first, and only there, so an invite reads as what it is (Encode sorts the rest).
	return Scheme + "://join?kind=" + url.QueryEscape(string(in.Kind)) + "&" + v.Encode()
}

// Parse reads an invite in the one scheme, or a legacy Edge invite, and validates every field.
func Parse(s string) (Invite, error) {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || u.Host != "join" {
		return Invite{}, fmt.Errorf("%w: an invite starts %s://join?", ErrBadInvite, Scheme)
	}
	q := u.Query()
	var in Invite
	switch u.Scheme {
	case Scheme:
		in.Kind = Kind(q.Get("kind"))
		if in.Kind != TowerClient && in.Kind != TowerStation && in.Kind != Edge {
			return Invite{}, fmt.Errorf("%w: it names an unknown kind %q", ErrBadInvite, q.Get("kind"))
		}
		in.FP, err = parseFP(q.Get("fp"))
	case legacyEdgeScheme:
		in.Kind = Edge
		in.FP, err = parseFP(q.Get("root"))
	default:
		return Invite{}, fmt.Errorf("%w: an invite starts %s://join?", ErrBadInvite, Scheme)
	}
	if err != nil {
		return Invite{}, err
	}
	in.Addrs = q["at"]
	if len(in.Addrs) == 0 {
		return Invite{}, fmt.Errorf("%w: it names no address", ErrBadInvite)
	}
	for _, a := range in.Addrs {
		if _, _, err := net.SplitHostPort(a); err != nil {
			return Invite{}, fmt.Errorf("%w: %q is not a host:port", ErrBadInvite, a)
		}
	}
	if c := q.Get("code"); c != "" {
		if in.Code, err = in.Kind.Domain().Canonical(c); err != nil {
			return Invite{}, fmt.Errorf("%w: %v", ErrBadInvite, err)
		}
	} else if in.Kind.IsTower() {
		return Invite{}, fmt.Errorf("%w: a Tower invite carries a code", ErrBadInvite)
	}
	in.For = q.Get("for")
	return in, nil
}

// parseFP reads a full sha256 fingerprint, with or without its "sha256:" prefix, grouped with
// colons or not. A prefix of a fingerprint is refused: a short one can be matched on purpose.
func parseFP(s string) (string, error) {
	s = strings.TrimSpace(s)
	if alg, rest, ok := strings.Cut(s, ":"); ok && len(alg) < 8 {
		if !strings.EqualFold(alg, "sha256") {
			return "", fmt.Errorf("%w: its fingerprint uses an unsupported hash %q", ErrBadInvite, alg)
		}
		s = rest
	}
	s = strings.ToLower(strings.ReplaceAll(s, ":", ""))
	if len(s) != 64 || strings.Trim(s, "0123456789abcdef") != "" {
		return "", fmt.Errorf("%w: it does not carry a full fingerprint (64 hex characters)", ErrBadInvite)
	}
	return s, nil
}
