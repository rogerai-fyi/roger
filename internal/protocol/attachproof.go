package protocol

// attachproof.go is the proof a Station gives that the assertion key it is handing Core is
// ACTUALLY ITS OWN.
//
// # THE HOLE, WHICH WAS OPEN FOR THE WHOLE LIFE OF SELF-ATTACH
//
// `POST /tower/edge/attach` takes `assertion_key` and `session_key` out of the request body.
// The request is signed - but with the caller's ACCOUNT key, which proves who is asking and
// says nothing whatever about whether the keys they are handing over are theirs. So anyone who
// learned a Station's assertion PUBLIC key could bind it to a Station of their own.
//
// "Learned" was never a barrier. On an unpinned hub link that key is in the clear in the
// X-Roger-Pubkey header of every poll, which is one every twenty-five seconds for the life of
// the process, so every party on that path already has it. And the window is not only "before
// its owner first attaches": the live/held uniqueness indexes are partial and terminal states
// release their keys deliberately, so revocation and the dormant-then-retired path reopen it on
// a key that is by then public.
//
// The damage is denial rather than theft - a squatter holds no private half, so the Station it
// squatted can never serve, sign a receipt or be paid - but the denial is severe and
// self-renewing: the squat makes the rightful owner's own attach fail on key uniqueness, their
// node re-attaches on its designed backoff, and every retry is refused for as long as the squat
// stands. See docs/relay-selection-design.md 5.6.
//
// # WHAT THE SIGNATURE COVERS, AND WHY EACH FIELD IS IN IT
//
// A signature over the public key alone would have been worthless: it is a token, liftable off
// the wire once and replayable forever by anybody, and it would have been one more check that
// exists and proves nothing. This binds the proof to ONE attach request:
//
//   - the CALLER KEY - the X-Roger-Pubkey whose signature authenticates the request carrying
//     this proof. It is what makes the proof non-transferable: a captured proof can only be
//     presented by a party that can also produce a request signature under that same key, which
//     is the holder of that account's private half and nobody else. Without it, an attacker
//     could lift a victim's proof off the wire and re-present it under their own account, which
//     is precisely the squat.
//   - the TIMESTAMP, which is the X-Roger-TS of that same request. It is not a second clock: the
//     request signature is verified against SigMaxSkew already, so a proof naming that timestamp
//     inherits exactly the freshness the request has, and one function bounds both.
//   - the STATION ID and BOTH KEYS, so the statement says in full what is being claimed rather
//     than deferring all of it to a digest. A reader of a log line can see what was proved.
//   - the BODY DIGEST, so the proof covers every other term of the attach as well - the node id,
//     the model, the prices - and cannot be moved onto a request that differs in any byte.
//   - the NETWORK, because the public network and a standalone one are different trust roots
//     and material issued under one carries no authority under the other.
//
// The session key gets no signature of its own and cannot: it is X25519, a key-agreement key
// that cannot sign at all. Including it here is the assertion key VOUCHING for it - "this
// session key belongs to the same Station as this assertion key" - which is a weaker statement
// than possession and is not the same thing. The residual is written out in
// docs/relay-selection-design.md 5.6.
//
// # DOMAIN SEPARATION, WHICH IS LOAD-BEARING RATHER THAN TIDY
//
// The assertion key already signs in two other byte-spaces, and this is a THIRD use of one key:
//
//	1. protocol.CanonicalRequest - hub polls, /complete, /audit/*, and the door signature.
//	2. towerobj signing bytes    - receipts and signed transcripts.
//
// A confusion between any two of the three would let a captured object be presented as another:
// a receipt replayed as an attach, or an attach proof replayed as a poll. Two independent
// arguments hold here, and they are independent on purpose, because a separation resting on one
// property is one refactor away from resting on none.
//
//   - BY PREFIX. Every string in space 2 begins "rogerobj-v1\x00"; every string in this space
//     begins attachProofDomain. Both prefixes are fixed, both are terminated by a NUL that no
//     variable field can contain, and they differ at their sixth byte ("rogero" against
//     "rogera"), so neither is a prefix of the other and no combination of network, object type
//     or version can bridge them.
//   - BY SHAPE, which is what covers space 1, whose strings carry no domain tag at all.
//     CanonicalRequest is method + "\n" + path + "\n" + ts + "\n" + digest, so EVERY string in
//     it contains at least three line feeds. This statement contains NONE: its separator is NUL,
//     its fields are hex, a decimal integer, a network name and a Station ID (st-[a-z0-9]+ - the
//     name-injection gate in attach/stationid.go is what makes that a closed alphabet), and not
//     one of those can carry a line feed. A byte string with no LF is not in the image of
//     CanonicalRequest for ANY input, so the separation does not depend on what the method
//     happens to be - which matters, because the door signature already puts a label where
//     CanonicalRequest expects a method and the next scheme may put something else there.
//
// Neither argument depends on the other, and the second holds even if somebody later adds a
// third tagged space that collides with the first.
//
// It also cannot be produced as a SIDE EFFECT of ordinary operation, which is the half that is
// easy to forget: a node signs hub requests through Station.SignRequest and receipts through
// towerobj, and both of those paths hash their input into one of the two spaces above before
// the key ever sees it. There is no call anywhere that hands the assertion key caller-chosen
// bytes, so no honest operation can be steered into emitting a valid attach proof.

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
)

// HeaderAttachProof carries the attach proof: a hex Ed25519 signature by the ASSERTION KEY
// NAMED IN THE BODY over the statement below.
//
// It rides in a header rather than in the body for a reason that is structural rather than
// stylistic: the proof covers a digest of the body, so a proof carried inside the body would
// have to cover itself. The request signature is in headers for the same reason.
const HeaderAttachProof = "X-Roger-Attach-Proof"

// attachProofDomain tags this byte-space. Fixed length, NUL-terminated, and deliberately not a
// prefix of "rogerobj-v1\x00" nor it of this - see the domain-separation note above. Bump the
// version suffix if a field is ever added or reordered: an old verifier must fail to verify a
// new statement rather than verify a truncated reading of one.
const attachProofDomain = "rogerai-station-attach-proof-v1\x00"

// AttachProof is one self-attach's possession statement, on both sides of the wire.
//
// It is ONE type with a signer and a verifier on it rather than two canonicalizers, for the
// same reason towerhub's hubEpochStatement is one function for the hub and the node: a second
// copy of a canonical form is how a signing scheme grows a hole, and the copy always drifts in
// the direction that accepts more.
type AttachProof struct {
	// Network is the trust root this attachment belongs to (link.PublicNetwork today).
	Network string
	// CallerPubkey is the hex Ed25519 key in X-Roger-Pubkey - the ACCOUNT key whose signature
	// authenticates the attach request this proof rides on. Binding it is what stops the proof
	// being lifted and re-presented by somebody else.
	CallerPubkey string
	// TS is the X-Roger-TS of that same request, so the proof is fresh exactly as long as the
	// request is and no separate skew window has to be reasoned about.
	TS int64
	// StationID is the identity being claimed, exactly as the body spells it - empty when the
	// node is letting Core mint one, which binds nothing an attacker gets to choose either,
	// since Core mints it.
	StationID string
	// AssertionKey is the hex Ed25519 key being claimed. It is also the key that must have
	// produced the signature: the claim and the proof are about the same key by construction.
	AssertionKey string
	// SessionKey is the hex X25519 secure-session key presented alongside. Vouched for, not
	// proved - it cannot sign.
	SessionKey string
	// Body is the exact request body, so the proof covers the whole offer and not merely the
	// fields named above.
	Body []byte
}

// statement is the exact bytes signed and verified. NUL-separated so it shares no shape with
// protocol.CanonicalRequest; domain-tagged so it shares no prefix with towerobj.
func (p AttachProof) statement() []byte {
	sum := sha256.Sum256(p.Body)
	var b strings.Builder
	b.WriteString(attachProofDomain)
	b.WriteString(p.Network)
	b.WriteByte(0)
	b.WriteString(p.CallerPubkey)
	b.WriteByte(0)
	b.WriteString(strconv.FormatInt(p.TS, 10))
	b.WriteByte(0)
	b.WriteString(p.StationID)
	b.WriteByte(0)
	b.WriteString(p.AssertionKey)
	b.WriteByte(0)
	b.WriteString(p.SessionKey)
	b.WriteByte(0)
	b.WriteString(hex.EncodeToString(sum[:]))
	return []byte(b.String())
}

// Sign produces the hex signature for HeaderAttachProof. priv MUST be the private half of
// p.AssertionKey; a caller that signs with anything else produces a proof its own verifier
// refuses, which is the failure mode to want.
func (p AttachProof) Sign(priv ed25519.PrivateKey) string {
	return hex.EncodeToString(ed25519.Sign(priv, p.statement()))
}

// Verify reports whether sigHex is a signature over this statement BY THE KEY p NAMES. That is
// the whole property: the key the caller is asking Core to bind is the key that had to sign.
//
// It answers false rather than an error on every failure, deliberately. Which of "no header",
// "not hex", "wrong length" and "does not verify" refused a caller is a probing oracle and is
// worth nothing to an honest one, whose answer is the same in all four cases: sign it with the
// key you are claiming.
func (p AttachProof) Verify(sigHex string) bool {
	pub, err := hex.DecodeString(p.AssertionKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return false
	}
	sig, err := hex.DecodeString(sigHex)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(pub), p.statement(), sig)
}
