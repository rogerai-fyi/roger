package main

// accountkey.go answers one question with one answer: WHICH KEY DOES THIS ACCOUNT'S MONEY
// LIVE UNDER?
//
// An account may hold several owner rows - one per device key a person signed in with. The
// wallet side has always been canonical (accountWalletForOwner: u_gh_/u_apple_/u_email_), but
// the EARNING side keyed on whichever device pubkey happened to be present when a lot was
// minted, and read on whichever device pubkey happened to be signing when the operator looked.
// For a one-device account those are the same key and nothing ever went wrong. For an operator
// with a laptop and a server they are different keys, and an audit found the consequences:
// lots minted under one, a cash-out looking under the other and finding nothing, and - because
// the underlying lookups had no ORDER BY - the possibility of lots scattering across both with
// no way to gather them.
//
// The canonical key is the account's EARLIEST owner row (the store now orders on that). Every
// device of one account resolves to it, so mint and read agree by construction.

import "rogerai.fm/roger/v5/internal/store"

// accountOwnerOf resolves any of an account's device rows to its canonical one. Falls back to
// the row it was given - a device key bound to no shared identity IS its own account.
//
// It swallows store errors on purpose, and the purpose is narrow: on the MINT path a lookup
// that failed must not stop an operator being paid, and the fallback keys the lot under a row
// that really is theirs. A caller for whom "I could not tell" and "they are unrelated" are
// different answers must use accountOwnerOfChecked instead - self-dealing is exactly such a
// caller, because there the fallback silently means "not the same account", which means pay.
func (b *broker) accountOwnerOf(o store.Owner) store.Owner {
	c, _ := b.accountOwnerOfChecked(o)
	return c
}

// accountOwnerOfChecked is accountOwnerOf with the store errors KEPT rather than dropped. It
// still tries every linkage - one unreachable index must not hide a link another would have
// found - and returns the first error it met alongside whatever it managed to resolve. So a
// non-nil error means "this answer may be incomplete", never "this answer is wrong".
func (b *broker) accountOwnerOfChecked(o store.Owner) (store.Owner, error) {
	if o.Pubkey == "" || o.Anonymized {
		return o, nil
	}
	var firstErr error
	keep := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	// Ordered by how strongly the identity binds an account together: a provider subject is
	// unforgeable and permanent, a verified email is proven, a login is neither (it can be
	// renamed, and a rename must not silently re-key an operator's earnings).
	if o.AppleSub != "" {
		c, found, err := b.db.OwnerByAppleSub(o.AppleSub)
		keep(err)
		if err == nil && found && c.Pubkey != "" {
			return c, firstErr
		}
	}
	if o.GitHubID != 0 && o.Login != "" {
		c, found, err := b.db.OwnerByLogin(o.Login)
		keep(err)
		if err == nil && found && c.Pubkey != "" && c.GitHubID == o.GitHubID {
			return c, firstErr
		}
	}
	if o.EmailVerifiedAt != 0 && o.Email != "" {
		c, found, err := b.db.OwnerByVerifiedEmail(o.Email)
		keep(err)
		if err == nil && found && c.Pubkey != "" {
			return c, firstErr
		}
	}
	return o, firstErr
}

// accountKeyOf is the pubkey an account's earning lots are minted under and read back from.
func (b *broker) accountKeyOf(o store.Owner) string { return b.accountOwnerOf(o).Pubkey }

// accountKeyOfPubkey resolves a raw device pubkey to its account's canonical key. Returns the
// input unchanged when it belongs to no known owner, so a caller never loses a key it had.
func (b *broker) accountKeyOfPubkey(pubkey string) string {
	if pubkey == "" {
		return pubkey
	}
	o, found, err := b.db.OwnerByPubkey(pubkey)
	if err != nil || !found {
		return pubkey
	}
	return b.accountKeyOf(o)
}

// nodeRegisteredTo reports whether nodeID names a live broker registration whose pubkey is
// exactly pubkey. It is the check behind the station<->node join (M0 of
// docs/relay-selection-design.md).
//
// The join exists so edge placement can score a station on what the probes measured. That
// makes a node id worth stealing: a fresh station naming a well-probed node would inherit
// its reliability, and inherit the traffic that reputation attracts. So the claim is only
// accepted from the machine it is about - the same key that registered the node must be the
// key signing the attach.
//
// Registration itself is TOFU-bound (a node id belongs to the first pubkey that claims it,
// and later registrations must use the same key), so equality here is a real identity check
// rather than a name comparison.
func (b *broker) nodeRegisteredTo(nodeID, pubkey string) bool {
	if nodeID == "" || pubkey == "" {
		return false
	}
	b.mu.Lock()
	reg, ok := b.nodes[nodeID]
	b.mu.Unlock()
	return ok && reg.PubKey == pubkey
}
