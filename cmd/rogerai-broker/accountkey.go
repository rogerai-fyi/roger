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
func (b *broker) accountOwnerOf(o store.Owner) store.Owner {
	if o.Pubkey == "" || o.Anonymized {
		return o
	}
	// Ordered by how strongly the identity binds an account together: a provider subject is
	// unforgeable and permanent, a verified email is proven, a login is neither (it can be
	// renamed, and a rename must not silently re-key an operator's earnings).
	if o.AppleSub != "" {
		if c, found, err := b.db.OwnerByAppleSub(o.AppleSub); err == nil && found && c.Pubkey != "" {
			return c
		}
	}
	if o.GitHubID != 0 && o.Login != "" {
		if c, found, err := b.db.OwnerByLogin(o.Login); err == nil && found && c.Pubkey != "" &&
			c.GitHubID == o.GitHubID {
			return c
		}
	}
	if o.EmailVerifiedAt != 0 && o.Email != "" {
		if c, found, err := b.db.OwnerByVerifiedEmail(o.Email); err == nil && found && c.Pubkey != "" {
			return c
		}
	}
	return o
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
