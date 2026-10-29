package edgeauth

// code.go is how a machine joining an Edge for the first time knows the root it was handed is
// the owner's (features/edge/enrollment_root_trust.feature).
//
// The owner's allow, adopt or readmit on the authority mints a one-time ENROLLMENT CODE, shown
// on the authority's own screen. The authority's answer to that join carries a ROOT PROOF: an
// HMAC, keyed by the code stretched with PBKDF2 and salted with the request's own nonce, over the
// root's fingerprint, the account, the node id and that nonce. The joiner recomputes it from the
// code the owner read off the authority. The joiner never SENDS anything derived from the code,
// so there is nothing to guess at the authority, and a device that relays one exchange faces an
// offline search the stretch prices out of the code's 15-minute life.
//
// A full SHA-256 fingerprint of the root (a PIN) is the secret-free alternative: no other root
// hashes to it, so it needs nothing from the authority at all.
//
// The derivation is pinned by testdata/root_proof_vectors.json, which the phone shares.

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"rogerai.fm/roger/v7/internal/joincode"
)

const (
	// ProofDomain separates this derivation from everything else that hashes these fields.
	ProofDomain = "rogerai-edge-root-proof/v1" // joincode.Edge.Name
	// ProofVersion prefixes a proof on the wire, so a joiner can refuse one it does not know.
	ProofVersion = "v1"
	// ProofIterations is the PBKDF2 work factor (ruling D3).
	ProofIterations = 200000
	// CodeLen is the code's length in Crockford symbols: 50 bits (ruling D2).
	CodeLen = 10
	// CodeLifetime is how long a minted code stays live, and how long a joiner keeps an answer
	// it has not yet verified (ruling D4).
	CodeLifetime = 15 * time.Minute
	// CodeAttempts is how many codes a joiner tries against held answers before it discards them
	// (ruling D5).
	CodeAttempts = 5
)

var (
	// ErrBadCode is input that is not an enrollment code.
	ErrBadCode = errors.New("that is not an enrollment code")
	// ErrBadPin is input that is not a full root fingerprint.
	ErrBadPin = errors.New("that is not a root fingerprint")
	// ErrNoRootProof is an answer that carried no proof for the code the joiner holds.
	ErrNoRootProof = errors.New("the answer carried no proof for the enrollment code")
	// ErrProofVersion is a proof in a version this joiner does not implement.
	ErrProofVersion = errors.New("the answer's root proof is in a version this roger does not know")
	// ErrRootProofMismatch is a proof that does not verify: the device that answered is not the
	// Edge that showed the owner this code (or the code was mistyped).
	ErrRootProofMismatch = errors.New("the enrollment code does not match the Edge that answered")
	// ErrPinMismatch is a root that does not hash to the pin the owner gave.
	ErrPinMismatch = errors.New("the root that answered does not match the fingerprint you gave")
)

// NewCode draws a fresh code from the operating system's secure random source, in canonical form
// (internal/joincode, the Edge domain).
func NewCode() string { return joincode.Edge.NewCode() }

// FormatCode shows a canonical code the way the owner reads it: two groups of five.
func FormatCode(code string) string { return joincode.Edge.Format(code) }

// CanonicalCode reads a code however it was transcribed: case-folded, with Crockford's
// confusables (I and L read as 1, O as 0), hyphens and spaces dropped. Anything else is refused,
// and so is anything that is not exactly CodeLen symbols.
func CanonicalCode(typed string) (string, error) {
	c, err := joincode.Edge.Canonical(typed)
	if err != nil {
		return "", fmt.Errorf("%w: a code is %d symbols, shown XXXXX-XXXXX (%v)", ErrBadCode, CodeLen, err)
	}
	return c, nil
}

// RootProof is what an authority attaches to its answer: proof that whoever minted code also
// holds this root, for this account, this node and this request. code must be canonical.
func RootProof(code, rootFP, account, nodeID, nonce string) string {
	return joincode.Edge.Proof(code, nonce, rootFP, account, nodeID)
}

// VerifyRootProof checks an answer's proof against the code the owner typed (any transcription
// CanonicalCode accepts). Each refusal names what went wrong; none of them is a reason to keep
// anything.
func VerifyRootProof(typedCode, proof, rootFP, account, nodeID, nonce string) error {
	code, err := CanonicalCode(typedCode)
	if err != nil {
		return err
	}
	switch err := joincode.Edge.Check(code, proof, nonce, rootFP, account, nodeID); {
	case err == nil:
		return nil
	case errors.Is(err, joincode.ErrNoProof):
		return ErrNoRootProof
	case errors.Is(err, joincode.ErrProofVersion):
		return ErrProofVersion
	default:
		return ErrRootProofMismatch
	}
}

// ParsePin reads a full root fingerprint however it was pasted - with or without "sha256:", in
// either case, grouped with colons or spaces - and returns it as 64 lowercase hex. A prefix of a
// fingerprint is refused: a short fingerprint can be matched by a root made for the purpose.
func ParsePin(typed string) (string, error) {
	s := strings.TrimSpace(typed)
	if alg, rest, ok := strings.Cut(s, ":"); ok && !isHex(alg) {
		if !strings.EqualFold(alg, "sha256") {
			return "", fmt.Errorf("%w: %s is an unsupported hash; give the sha256 fingerprint", ErrBadPin, alg)
		}
		s = rest
	}
	s = strings.ToLower(strings.NewReplacer(":", "", " ", "").Replace(s))
	if !isHex(s) {
		return "", fmt.Errorf("%w: it is not a fingerprint (64 hex characters)", ErrBadPin)
	}
	switch {
	case len(s) < 32:
		return "", fmt.Errorf("%w: it is too short to be a pin; give the full fingerprint", ErrBadPin)
	case len(s) != 64:
		return "", fmt.Errorf("%w: it is not a full fingerprint (64 hex characters)", ErrBadPin)
	}
	return s, nil
}

func isHex(s string) bool {
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return false
		}
	}
	return true
}

// ShortFingerprint is the read-aloud form of a root fingerprint: the first 16 hex characters in
// four groups. It is reassurance, never the trust decision.
func ShortFingerprint(fp string) string {
	if len(fp) < 16 {
		return ""
	}
	u := strings.ToUpper(fp[:16])
	return u[:4] + "-" + u[4:8] + "-" + u[8:12] + "-" + u[12:16]
}

// ErrRootUnproven is a first answer the joiner was given nothing to vouch for: no code and no pin.
var ErrRootUnproven = errors.New("the root of the Edge you are joining has to be confirmed (a code or its full fingerprint) before it is kept")

// VerifyAnswer is the whole rule a joiner applies to an authority's answer before keeping
// anything (features/edge/enrollment_root_trust.feature). The answer must pass CheckResponse. A
// root the joiner already holds (want.Root) decides on its own. Otherwise the owner's pin and/or
// code must vouch for the root it offers - every one given must agree - and with neither, nothing
// is kept. nonce is the one the joiner's own request carried.
func VerifyAnswer(resp Response, want Expect, nonce, code, pin string) (*Identity, error) {
	id, err := CheckResponse(resp, want)
	if err != nil {
		return nil, err
	}
	if want.Root != nil {
		return id, nil
	}
	if code == "" && pin == "" {
		return nil, ErrRootUnproven
	}
	fp := RootFingerprint(id.Root)
	if pin != "" && fp != pin {
		return nil, ErrPinMismatch
	}
	if code != "" {
		if err := VerifyRootProof(code, resp.RootProof, fp, resp.Account, resp.NodeID, nonce); err != nil {
			return nil, err
		}
	}
	return id, nil
}
