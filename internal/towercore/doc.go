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
//
// THE DIRECTION OF TRUST, which every package here is written around: a Tower TRANSPORTS,
// it does not DECIDE. Nothing in towercore may take a Tower's word for eligibility, price,
// capacity, or identity. Where a package looks like it is trusting the Tower, it is
// comparing the Tower's claim against something Core recorded for itself.
package towercore
