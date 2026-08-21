package protocol

// stationid.go mints a Station's IDENTITY FROM ITS ASSERTION KEY, so that naming an identity
// and proving you hold it are the same act.
//
// # THE HOLE THIS CLOSES, WHICH THE POSSESSION PROOF LEFT OPEN
//
// protocol.AttachProof binds the Station id into the statement the assertion key signs, and a
// reviewer asked the obvious follow-up: what does that prove about the id? Nothing. The proof
// is signed by the CLAIMANT's own assertion key, so "I claim somebody else's Station id with
// keys that are genuinely mine" was a perfectly valid proof. `POST /tower/edge/attach` minted
// whatever `station_id` the body named, and the only thing standing between an attacker and
// another operator's identity was a row in the store - `checkBindings` refusing an id that is
// already taken.
//
// THAT ROW IS NOT FOREVER, which is the part that made this reachable rather than theoretical.
// Terminal attachments are DELETED (attach.Store.ReapTerminal, thirty days after a revoke; a
// dormant Station reaches terminal after a hundred and eighty more). Once the row is gone the
// id is free - and it was never secret: it is the `relay_name` in every `/tower/edge/authorize`
// answer that Station ever served, it is the leftmost label of its relay DNS name, and it is in
// the placement logs. So the sequence "revoke, wait out the reaper, take the name" handed an
// attacker a permanent, self-renewing denial: the rightful machine keeps the id on disk forever
// with no re-mint path, so its own re-attach meets "this Station ID is already bound to another
// assertion key" on every backoff, and the only recoveries are deleting the Station directory
// (which destroys the identity and its earnings lineage) or a human at Core.
//
// # WHY DERIVATION RATHER THAN AN OWNERSHIP LOOKUP
//
// The other candidate fix was to refuse an operator-supplied `station_id` that has no prior row
// owned by this account. It cannot work, and the reason is the same reaper: after the reap
// THERE IS NO ROW to look up, for the attacker or for the rightful owner, so the lookup either
// refuses everybody (which denies the owner their own return - the very outcome being
// prevented) or refuses nobody (which is today). Making it work would need a permanent tombstone
// of every Station id ever issued, which is an unbounded table whose growth is exactly what
// ReapTerminal exists to prevent.
//
// Derivation needs no lookup at all. The id IS the key, hashed: to attach as st-<h> you must
// present the assertion key whose digest is h, and AttachProof already makes you prove you hold
// its private half. A reaped id is therefore reclaimable only by the machine that always held
// it, which is both the security property and the operability one. There is no state to
// consult, nothing to migrate a table for, and no window between the reap and the return.
//
// # WHY THE MIGRATION COST IS ZERO, WHICH IS NOT AN ASSUMPTION
//
// This changes the identity a node presents, so it would be a wire change with a transition to
// design if self-attach had shipped. It has not: `internal/agent/tower.go`, this whole package's
// caller in `internal/station`, and `cmd/rogerai-broker/toweredgeattach.go` are all ABSENT from
// tag v5.7.1, the newest tag in the tree. There is no deployed node holding a random Station id,
// so this is a hard cutover for the same reason the possession proof was one, and it is refused
// loudly rather than accepted-if-it-looks-close for the same reason too. `station.Open` repairs
// a directory minted before this rule and says so in a warning; Core refuses an id that is not
// the one its key mints, rather than silently binding a different id than the caller named -
// which would reintroduce, at the identity layer, the exact "the value signed is not the value
// bound" defect the same review found in the Station-id trim.
//
// # WHY A TRUNCATED HASH IS ENOUGH
//
// The attack that matters is SECOND PREIMAGE: an attacker wants one specific victim's id, so
// they must find an Ed25519 keypair whose public half digests to that exact 96-bit prefix,
// which is 2^96 keygens. Collisions between two honest Stations are the birthday bound
// (2^48 keys before a pair is likely), and a fleet of even a hundred million Stations sits
// around 10^-14 - and a collision costs an honest operator a refusal, not a compromise, because
// whoever attaches second is refused rather than merged. Twelve bytes is also exactly the width
// the random minter used, so the relay DNS names, log lines and column widths are unchanged.

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
)

// stationIDDomain tags this digest so it is not the sha256 of an assertion key that anything
// else in the tree computes, now or later. Fixed length, NUL-terminated, same discipline as
// attachProofDomain: a bare hash of a public key is the kind of value two subsystems arrive at
// independently and then discover they have to keep equal forever.
const stationIDDomain = "rogerai-station-id-v1\x00"

// DeriveStationID is the ONE definition of the Station id an assertion key mints.
//
// It is in package protocol because both halves of the wire need the same answer: the node
// stamps it into its persistent identity at `station.Init`, and Core recomputes it from the key
// in the attach body and refuses anything else. A second copy of this function is how the two
// ends drift into disagreeing about who somebody is.
//
// The result is always in attach.ValidStationID's alphabet ("st-" + lowercase hex), which is
// what keeps it safe as the leftmost label of a relay DNS name and what keeps the AttachProof
// statement free of separators. That is asserted rather than assumed - see the tests.
func DeriveStationID(assertionKey ed25519.PublicKey) string {
	sum := sha256.Sum256(append([]byte(stationIDDomain), assertionKey...))
	// Twelve bytes, the same width the random minter used - see the note above for why the
	// truncation is not the weak part.
	return "st-" + hex.EncodeToString(sum[:12])
}
