package edge

import (
	"crypto/ed25519"
	"fmt"
	"sort"
	"strings"

	"rogerai.fm/roger/v6/internal/edgeauth"
	"rogerai.fm/roger/v6/internal/store"
)

// Capability is the vocabulary the rest of the system routes and reasons on. A node
// declares capabilities; the fleet verifies them where they are verifiable.
type Capability string

const (
	// Serve hosts a model and can take relayed inference. A Station is a node with
	// this capability - not the definition of a node.
	Serve Capability = "serve"
	// Classify runs a task model locally against a fixed label set.
	Classify Capability = "classify"
	// Sense produces readings on a schedule or on demand.
	Sense Capability = "sense"
	// Actuate accepts commands that CHANGE THE WORLD. It is declared and
	// owner-confirmed, and is the one capability that is never inferred from
	// behavior: the cost of guessing wrong is not a bad answer, it is a moving
	// machine.
	Actuate Capability = "actuate"
	// Relay forwards for nodes that cannot be reached directly.
	Relay Capability = "relay"
	// Operate runs an agent that can act on other nodes.
	Operate Capability = "operate"
)

// Capabilities lists the vocabulary in the design's order.
func Capabilities() []Capability {
	return []Capability{Serve, Classify, Sense, Actuate, Relay, Operate}
}

// Valid reports whether c is a capability at all. An unknown string is never stored:
// a fleet that accepts capabilities it cannot reason about cannot route on them.
func (c Capability) Valid() bool {
	for _, k := range Capabilities() {
		if k == c {
			return true
		}
	}
	return false
}

// VerificationMethod is how this capability is verified, in the words of the spec's
// table. It is prose on purpose: it is what a fleet view shows an owner next to a
// CLAIMED mark, so they can see WHY a claim has not become a verification.
func (c Capability) VerificationMethod() string {
	switch c {
	case Serve:
		return "the existing serving probe"
	case Classify:
		return "a canary sample with a known label"
	case Sense:
		return "a read that returns a well-formed sample"
	case Actuate:
		return "declared only, owner-confirmed, never probed"
	case Relay:
		return "reachability observed from a second node"
	case Operate:
		return "declared, and gated by grant at use time"
	}
	return ""
}

// CapState is how far a declared capability has got.
type CapState string

const (
	// Claimed: the node says so and nothing has confirmed it. Nothing routes on it.
	Claimed CapState = "CLAIMED"
	// Verified: observation (or, for actuate, the owner) has confirmed it.
	Verified CapState = "VERIFIED"
	// PendingConfirmation: actuate, declared but not yet confirmed by the owner.
	PendingConfirmation CapState = "PENDING CONFIRMATION"
)

// Kind is what the thing IS, and it bounds what may be asked of it.
type Kind string

const (
	Host   Kind = "host"   // Linux, macOS, anything with a real TCP stack
	Board  Kind = "board"  // a microcontroller, kilobytes of RAM
	Mobile Kind = "mobile" // a phone or tablet
)

// Kinds lists the kinds a node may be.
func Kinds() []Kind { return []Kind{Host, Board, Mobile} }

// Valid reports whether k is a kind at all.
func (k Kind) Valid() bool {
	for _, x := range Kinds() {
		if x == k {
			return true
		}
	}
	return false
}

// Encoding is how this kind carries the (one) message schema: hosts and mobiles can
// afford JSON over the existing tunnel; a board carries the same schema in a compact
// binary framing sized for kilobytes of RAM. One schema, two encodings.
func (k Kind) Encoding() string {
	switch k {
	case Host, Mobile:
		return "json"
	case Board:
		return "compact"
	}
	return ""
}

// MayDeclare bounds a kind's capabilities. A board has no room to serve a model, no
// business relaying for others and no agent runtime; a mobile is too intermittent to
// be somebody else's relay.
func (k Kind) MayDeclare(c Capability) bool {
	if !c.Valid() {
		return false
	}
	switch k {
	case Host:
		return true
	case Board:
		return c == Classify || c == Sense || c == Actuate
	case Mobile:
		return c != Relay
	}
	return false
}

// NodeID derives a node's stable identity from the public half of the keypair the node
// generated for itself: two nodes cannot share an id without sharing a key.
//
// The derivation itself lives in internal/edgeauth, because the issuing authority has
// to make the same check without linking this package's LAN face. One derivation, two
// callers: see edgeauth.NodeID for why it is shaped the way it is.
func NodeID(pub ed25519.PublicKey) string { return edgeauth.NodeID(pub) }

// MaxNameLen bounds an owner-chosen name. A name is typed into a command line and drawn
// in a topology box at 80 columns; 64 is generous for both.
const MaxNameLen = 64

// ValidName reports whether a name is usable in a command line and in a graph.
//
// The rule is an allow-list, not a deny-list of the traversal spellings we thought of:
// a leading alphanumeric, then alphanumerics and `.`, `-`, `_`, with `..` refused
// outright. That accepts "bench-pi", "pi5-cabinet-2", "a" and "UPPER", and refuses the
// empty name, anything with whitespace in it, "../etc/passwd", and anything past the
// length bound.
func ValidName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("a node needs a name")
	case len(name) > MaxNameLen:
		return fmt.Errorf("a node name is at most %d characters", MaxNameLen)
	case strings.Contains(name, ".."):
		return fmt.Errorf("%q is not a usable node name", name)
	}
	for i := 0; i < len(name); i++ {
		ch := name[i]
		alnum := (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9')
		if i == 0 {
			if !alnum {
				return fmt.Errorf("a node name starts with a letter or a digit, not %q", string(ch))
			}
			continue
		}
		if !alnum && ch != '-' && ch != '_' && ch != '.' {
			return fmt.Errorf("%q is not usable in a node name", string(ch))
		}
	}
	return nil
}

// NewNode builds the record a node presents at enrollment: its id derived from its own
// public key, the owner's name for it, its kind, and the capabilities it DECLARES.
//
// Every declared capability starts unverified. `actuate` starts PENDING CONFIRMATION
// rather than CLAIMED, because for actuate there is no probe that could ever promote
// it - only the owner can.
func NewNode(pub ed25519.PublicKey, name string, kind Kind, caps ...Capability) store.EdgeNode {
	n := store.EdgeNode{ID: NodeID(pub), Name: name, Kind: string(kind)}
	for _, c := range caps {
		n.Caps = append(n.Caps, store.EdgeCap{Name: string(c), State: string(initialState(c))})
	}
	return n
}

// initialState is where a freshly declared capability starts.
func initialState(c Capability) CapState {
	if c == Actuate {
		return PendingConfirmation
	}
	return Claimed
}

// Routable reports whether the fleet may route work of kind c to this node. Only a
// VERIFIED capability routes: a claim the fleet could not confirm buys nothing, which
// is the whole difference between a declaration and a capability.
func Routable(n store.EdgeNode, c Capability) bool {
	for _, d := range n.Caps {
		if d.Name == string(c) {
			return d.State == string(Verified)
		}
	}
	return false
}

// Declares reports whether the node declares c at all, at any state.
func Declares(n store.EdgeNode, c Capability) bool {
	for _, d := range n.Caps {
		if d.Name == string(c) {
			return true
		}
	}
	return false
}

// transportRank orders transports by PREFERENCE, not by the order discovery happened to
// find them: a LAN-direct link is lower latency and works with no internet at all, so it
// is always read first and a relay path second.
func transportRank(kind string) int {
	switch kind {
	case "lan":
		return 0
	case "relay":
		return 1
	}
	return 2
}

// sortTransports puts the preference order on a record. Stable, so two LAN addresses
// keep the order they were learned in.
func sortTransports(ts []store.EdgeTransport) {
	sort.SliceStable(ts, func(i, j int) bool {
		return transportRank(ts[i].Kind) < transportRank(ts[j].Kind)
	})
}

// Presence is what the fleet has most recently OBSERVED about a node. It is not a
// property of the node: it is what we know, and a node we have stopped hearing from is
// drawn dark rather than dropped, because a fleet that silently loses members hides the
// problem it should be showing.
type Presence string

const (
	// PresenceVerified: seen, and its certificate matched the pin.
	PresenceVerified Presence = "VERIFIED"
	// PresenceDark: enrolled, but not heard from within the liveness window. KEPT.
	PresenceDark Presence = "DARK"
	// PresenceReenroll: still a member of record, but the Edge's authority changed
	// under it, so the certificate it holds is no longer one this Edge can verify. It
	// is KEPT and shown with the reason: an authority migration that silently emptied
	// the fleet would hide the very thing the owner has to act on.
	PresenceReenroll Presence = "NEEDS RE-ENROLLMENT"
	// PresenceCandidate: seen on the LAN, not enrolled. Not a member: no capabilities,
	// no traffic, until the owner adopts it.
	PresenceCandidate Presence = "CANDIDATE"
)
