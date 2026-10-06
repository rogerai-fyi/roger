package main

// THE FIRST ENROLLMENT VERIFIES THE ROOT (features/edge/enrollment_root_trust.feature).
//
// A machine that already holds its Edge's root checks every answer against it. A machine that
// holds none - a first join against a local authority, or a deliberate move to a new root - keeps
// the root it is handed only once the owner vouches for it: with the one-time enrollment code the
// authority's allow/adopt/readmit printed (the answer's root proof must verify with it), or with
// the root's full fingerprint (--root, or an invite). There is no third way: no flag, setting or
// variable keeps an unproven root (ruling D7).
//
// HOLD, THEN VERIFY (ruling D14). The joiner never sends anything derived from the code, so it may
// ask for the code AFTER the authority answers: the answer is held, unwritten, and a mistyped code
// is simply typed again against it, up to edgeauth.CodeAttempts times and for no longer than
// edgeauth.CodeLifetime (rulings D4, D5).

import (
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"rogerai.fm/roger/v6/internal/edgeauth"
)

// edgeNow is the joiner's clock. A seam so the spec can say "the owner typed it sixteen minutes
// later"; production is the wall clock.
var edgeNow = time.Now

// edgeJoinTrust is what the owner gave this join to vouch for the root.
type edgeJoinTrust struct {
	Code        string // canonical enrollment code, or ""
	Pin         string // the root's full fingerprint (64 hex), or ""
	ReplaceRoot bool   // the owner means to move this machine to a different root (ruling D11)
	Yes         bool   // a root change is confirmed without a prompt
	// Ask reads one answer from the owner; nil when there is no one to ask.
	Ask func(prompt string) string
	// Hold asks for the answer to be held and returned (errEdgeNeedsCode) instead of refused, for a
	// caller that asks for the code on its own screen (the TUI wizard).
	Hold bool
}

// errEdgeNeedsCode is a join whose answer is held, waiting for the code the authority showed.
var errEdgeNeedsCode = errors.New("the authority answered; enter the enrollment code it shows to finish joining")

// edgeHeldJoin is an authority's answer that has passed every check but the owner's: nothing of it
// has been written.
type edgeHeldJoin struct {
	id       *edgeauth.Identity
	resp     edgeauth.Response
	nonce    string
	where    string // the address that answered, for the messages
	received time.Time
	attempts int
	finish   func() (edgeEnrollResult, error)
	verified bool
}

// rootShort is the read-aloud fingerprint of the root this answer offers.
func (h *edgeHeldJoin) rootShort() string { return edgeauth.ShortFingerprint(h.fp()) }

func (h *edgeHeldJoin) fp() string { return edgeauth.RootFingerprint(h.id.Root) }

// check tries one code against the held answer. A nil error means the root is proven. done reports
// that the held answer is spent - proven, or past saving - so the caller must not ask again.
func (h *edgeHeldJoin) check(typed string) (done bool, err error) {
	if edgeNow().Sub(h.received) > edgeauth.CodeLifetime {
		return true, fmt.Errorf("that answer arrived more than %d minutes ago, so it is no longer trusted: run the join again.\n"+
			"  nothing was kept", int(edgeauth.CodeLifetime.Minutes()))
	}
	if h.attempts >= edgeauth.CodeAttempts {
		return true, edgeCapReached()
	}
	err = edgeauth.VerifyRootProof(typed, h.resp.RootProof, h.fp(), h.resp.Account, h.resp.NodeID, h.nonce)
	switch {
	case err == nil:
		h.verified = true
		return true, nil
	case errors.Is(err, edgeauth.ErrBadCode):
		return false, usagef("%v", err) // not a code at all: not a guess at the proof
	case errors.Is(err, edgeauth.ErrNoRootProof):
		return true, edgeNoProof(h)
	case errors.Is(err, edgeauth.ErrProofVersion):
		return true, fmt.Errorf("%w.\n  the authority is newer than this roger: update roger here, or join with --root and the full\n"+
			"  fingerprint `roger edge authority` prints there. nothing was kept", err)
	}
	h.attempts++
	if h.attempts >= edgeauth.CodeAttempts {
		return true, edgeCapReached()
	}
	return false, fmt.Errorf("%w at %s (it offered root %s; %d attempts left).\n"+
		"  check the code; if it is right, the device that answered at that address is not the Edge that showed you this code.\n"+
		"  to start over, mint a fresh one with allow or adopt on the authority",
		edgeauth.ErrRootProofMismatch, h.where, h.rootShort(), edgeauth.CodeAttempts-h.attempts)
}

func edgeCapReached() error {
	return fmt.Errorf("the code did not match %d times: a device on your network may be impersonating the authority.\n"+
		"  nothing was kept; mint a fresh code with allow or adopt on the authority and join again", edgeauth.CodeAttempts)
}

// edgeNoProof says which "no proof" it was: an authority that does not prove roots at all, or one
// that held no live code for this machine.
func edgeNoProof(h *edgeHeldJoin) error {
	if h.resp.ProofStatus == edgeauth.ProofNone {
		return fmt.Errorf("%w: the Edge at %s holds no live code for this machine - it was already used or has expired\n"+
			"  (a code lasts %d minutes, for one join). mint a fresh one with allow or adopt on the authority. nothing was kept",
			edgeauth.ErrNoRootProof, h.where, int(edgeauth.CodeLifetime.Minutes()))
	}
	return fmt.Errorf("%w: the authority at %s does not prove its root (it runs a roger from before root proofs),\n"+
		"  so the code cannot be checked. update roger on it, or join with --root and the full fingerprint\n"+
		"  `roger edge authority` prints there (it offered root %s). nothing was kept",
		edgeauth.ErrNoRootProof, h.where, h.rootShort())
}

// edgeCheckPin refuses a root that does not hash to the owner's pin.
func edgeCheckPin(pin string, root *x509.Certificate, where string) error {
	got := edgeauth.RootFingerprint(root)
	if got == pin {
		return nil
	}
	return fmt.Errorf("%w (at %s):\n  expected sha256:%s\n  offered  sha256:%s\n  nothing was kept",
		edgeauth.ErrPinMismatch, where, pin, got)
}

// edgeAskForCode reads codes from the owner until one proves the held answer, the cap is reached,
// or the answer is too old.
func edgeAskForCode(h *edgeHeldJoin, ask func(string) string) error {
	fmt.Printf("the authority at %s answered, offering root %s.\n", h.where, h.rootShort())
	for {
		done, err := h.check(ask("enter the enrollment code shown on the authority: "))
		if done {
			return err
		}
		if err != nil {
			fmt.Println(err)
		}
	}
}

// edgeReadLine reads one line from edgeStdin, a byte at a time so that a later y/N read by
// edgeConfirmed sees exactly what follows it.
func edgeReadLine() string {
	b := make([]byte, 1)
	var line []byte
	for {
		n, err := edgeStdin.Read(b)
		if n > 0 {
			if b[0] == '\n' {
				break
			}
			line = append(line, b[0])
		}
		if err != nil || len(line) > 256 {
			break
		}
	}
	return strings.TrimSpace(string(line))
}

// edgeBrokerAuthenticated reports whether Core can be reached over a channel that authenticates
// it: https, or plain http to this machine's own loopback (development and the test harness).
// A plain-http broker anywhere else would put the Core path back where the LAN path was (D10).
func edgeBrokerAuthenticated(broker string) bool {
	u, err := url.Parse(broker)
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return true
	case "http":
		h := u.Hostname()
		if strings.EqualFold(h, "localhost") {
			return true
		}
		ip := net.ParseIP(h)
		return ip != nil && ip.IsLoopback()
	}
	return false
}

// edgeInviteScheme is the scheme of a pasteable invite: the authority's address, its root's full
// fingerprint, and the one-time code, in one string.
const edgeInviteScheme = "rogerai-edge"

// edgeInvite renders an invite.
func edgeInvite(at, fingerprint, code string) string {
	v := url.Values{}
	v.Set("at", at)
	v.Set("root", fingerprint)
	if code != "" {
		v.Set("code", strings.ReplaceAll(code, "-", ""))
	}
	return edgeInviteScheme + "://join?" + v.Encode()
}

// edgeParseInvite reads an invite back into an address, a pin and a code.
func edgeParseInvite(s string) (endpoint, pin, code string, err error) {
	u, perr := url.Parse(strings.TrimSpace(s))
	if perr != nil || u.Scheme != edgeInviteScheme || u.Host != "join" {
		return "", "", "", usagef("that is not an Edge invite (it starts %s://join?)", edgeInviteScheme)
	}
	q := u.Query()
	at := q.Get("at")
	if _, _, serr := net.SplitHostPort(at); serr != nil {
		return "", "", "", usagef("that invite names no authority address")
	}
	if pin, err = edgeauth.ParsePin(q.Get("root")); err != nil {
		return "", "", "", usagef("that invite's root fingerprint is unusable: %v", err)
	}
	if c := q.Get("code"); c != "" {
		if code, err = edgeauth.CanonicalCode(c); err != nil {
			return "", "", "", usagef("%v", err)
		}
	}
	return "http://" + at, pin, code, nil
}

// edgeTrustFromArgs reads --code, --root, --invite, --replace-root and --yes. Every malformed value
// is refused here, before anything is sent.
func edgeTrustFromArgs(argv edgeArgv, endpoint string) (string, edgeJoinTrust, error) {
	var tr edgeJoinTrust
	if inv, ok := argv.flags["invite"]; ok {
		if endpoint != "" {
			return "", tr, usagef("an invite already names the authority's address; drop --authority")
		}
		ep, pin, code, err := edgeParseInvite(inv)
		if err != nil {
			return "", tr, err
		}
		endpoint, tr.Pin, tr.Code = ep, pin, code
	}
	if c, ok := argv.flags["code"]; ok {
		code, err := edgeauth.CanonicalCode(c)
		if err != nil {
			return "", tr, usagef("%v", err)
		}
		tr.Code = code
	}
	if r, ok := argv.flags["root"]; ok {
		pin, err := edgeauth.ParsePin(r)
		if err != nil {
			return "", tr, usagef("%v", err)
		}
		tr.Pin = pin
	}
	tr.ReplaceRoot, tr.Yes = argv.has("replace-root"), argv.has("yes")
	return endpoint, tr, nil
}
