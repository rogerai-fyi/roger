// Package towercore is a grouping directory, not a package with code in it. It exists so
// the Tower network's CORE side is one thing you can look at, rather than eight
// similarly-named packages scattered through internal/.
//
// # THE BOUNDARY THIS LAYOUT ENCODES
//
// The Tower network has two sides, and they are not allowed to be the same code:
//
//	internal/towercore/...   ROGER CORE. Runs in the broker. Holds the CA, the registries,
//	                         and every decision a Tower is not trusted to make.
//	internal/tower/          THE TOWER ITSELF, standalone. Enforced to make NO outbound
//	                         network call at all - internal/tower/egress_test.go
//	                         (TestStandaloneHasNoOutboundNetworkCallAtAll) fails if any file
//	                         in it gains the ability. A standalone Tower is a private network
//	                         with its own trust root and must never phone home.
//	internal/towerstore/     The Tower's durable store, OUTSIDE internal/tower precisely
//	                         because a database driver dials and would trip that gate.
//	internal/towerjoin/      The Tower's network half - enrolling with Core. Outside for the
//	                         same reason.
//	internal/towerobj/       The wire format BOTH sides share. It is deliberately not under
//	                         towercore: a format only Core can produce is not a format, and
//	                         putting it here would invite Core-only assumptions into it.
//
// If you are tempted to move internal/tower under this directory, read that egress test
// first. The split looks like inconsistency and is actually the invariant.
//
// WHAT EACH CORE PACKAGE DOES, in the order a Tower meets them:
//
//	cert     the certificate authority. Issues the short-lived certificate a Tower holds,
//	         under a root whose custody is a three-way ladder (injected / persisted /
//	         generated-once-and-loudly).
//	admit    the admission registry. One-time enrollment tokens, the atomic admission
//	         bundle, leases, lifecycle state, and revocation.
//	enroll   the enrollment handshake itself: challenge, proof, CSR, and the idempotency
//	         that makes a lost response survivable.
//	attach   Station attachment. Binds a Station ID to its two independent keys under
//	         exactly one origin. Everything downstream verifies against what is recorded
//	         here, so an empty attach registry means an inert network.
//	link     the session a registered Tower holds open: version and capability negotiation,
//	         liveness, and the head a reconnect quotes.
//	inv      signed, revisioned inventories with hash-chained deltas. Decides which leaves
//	         are routable, and refuses everything it cannot verify itself.
//	head     the durable chain position, so ANY instance can answer a reconnect and see a
//	         replay or a fork rather than guessing.
//	policy   the seam through which inv asks Core the questions it may not answer alone -
//	         bans, owners, revoked keys, allowed models, price bands. Fails closed.
//	envelope what makes the content a Tower relays opaque TO it: a request sealed to the
//	         Station's secure-session key and a result sealed back to Core's, so a relay
//	         carries ciphertext and digests and reads neither end.
//	attempt  the single authoritative state of one public attempt and its signed, chained
//	         history. Two objects: the Core-private AttemptEventV1 carrying the hold and the
//	         funding reservation, and the disclosure-safe AttemptIssueCommitmentV1 that is
//	         the only one a Tower or Station ever sees. Money is decided from this.
//	fleet    the fleet-wide view of which Stations are routable, so a broker that is not
//	         holding a Tower's link can still route to it. A read model over inv, never
//	         authority: every dispatch re-checks the attachment before issuing anything.
//	dispatch one unit of work handed to one Station and one verifiable answer back: a
//	         one-use grant Core signs, and a receipt the Station signs over the exact bytes
//	         it returned. Carries no money - Tower-backed work is uncompensated in v1.
//	reputation what became of each edge attempt, per Tower, over a moving window:
//	         corroborated, uncorroborated, disputed, canary pass/fail, audit mismatch. It
//	         records facts and computes rates; the decision to flag or suspend is a policy
//	         read over them, kept separate so a threshold can move without touching evidence,
//	         and so no rate ever reverses a settlement.
//
// # WHY THE IN-MEMORY STORES LOOK LIKE DEAD CODE AND ARE NOT
//
// The in-memory store constructors and the CA's NewAuthority/ExportRoot have no callers
// inside the broker binary. loadTowerSubsystem returns nil without a database, so the broker
// never builds a memory store; and the CA is loaded through LoadOrCreate, never minted
// directly. A dead-code sweep will find all of them, every time.
//
// (If you are running such a sweep: exclude comment lines. Naming these symbols in this very
// paragraph was enough to make one report them as having a production caller, which is a
// cheerful reminder that a check you cannot see the workings of is a check you do not have.)
//
// They are the REFERENCE IMPLEMENTATION each durable store is held against. The parity
// suites run one scenario against both and require the same answer, and the two are written
// deliberately differently - a held mutex against a locked row and a CAS, a Go comparison
// against a conditional upsert, a scan against a partial unique index - so agreement between
// them is a real result rather than a tautology. That has already paid for itself: the band
// occupancy bug in internal/store shipped precisely because a covered memory store sat beside
// an uncovered durable one, and this module has since caught mem/PG divergence on
// re-attachment, on live-key uniqueness, on duplicate ids and on a cap's expiry boundary.
//
// Deleting them would delete the only thing that can tell you the durable store is wrong.
//
// cert.NewAuthority and cert.ExportRoot are the same shape for a different reason: they are
// the OFFLINE ROOT CEREMONY. Minting a Tower CA root and exporting both halves as PEM is how
// an operator produces the key material for ROGERAI_TOWER_CA_{KEY,CERT}_PEM before the first
// broker ever starts - which is exactly how this deployment's root was made. The broker does
// not call them because by the time it runs, the ceremony is over.
//
// # THE DIRECTION OF TRUST, which every package here is written around: a Tower TRANSPORTS,
// it does not DECIDE. Nothing in towercore may take a Tower's word for eligibility, price,
// capacity, or identity. Where a package looks like it is trusting the Tower, it is
// comparing the Tower's claim against something Core recorded for itself.
package towercore
