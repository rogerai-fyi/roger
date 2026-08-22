// Package towerjoin holds the JOINED-mode account flow for roger-tower: signing in and
// registering a Tower with the public RogerAI network.
//
// It lives outside internal/tower deliberately. Signing in needs the network, and
// internal/tower is covered by a gate test that fails if any file there gains the ability
// to reach it. Keeping every outbound call on this side of the boundary is what lets the
// standalone core stay provably egress-free, and it means a standalone operator links no
// network code at all into the path they run.
//
// FOUNDER RULING 2026-08-02: the account line is drawn at "does this Tower carry other
// people's traffic?", not at money. Standalone never needs an account. Joined always
// does, because availability cannot be forced cryptographically - the defences are health
// scoring, probation and revocation, all per-identity, and if identities are free then
// revocation is a speed bump rather than a penalty.
package towerjoin

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"rogerai.fm/roger/v6/internal/tower"
)

const accountFile = "account.json"

// Account is the operator's RogerAI identity as this machine holds it.
type Account struct {
	Login string `json:"login"`
	// Token is the bearer credential. It is never rendered: see String.
	Token string `json:"token"`
}

// SignedIn reports whether this account is usable.
func (a Account) SignedIn() bool { return a.Login != "" }

// String describes the account for a human. It names WHO, never the credential - so a
// status line, a log, or a support paste can never carry the token.
func (a Account) String() string {
	if !a.SignedIn() {
		return "not signed in"
	}
	return fmt.Sprintf("signed in as @%s", a.Login)
}

// SaveAccount persists the credential owner-only.
func SaveAccount(dir string, a Account) error {
	b, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, accountFile), b, 0o600)
}

// LoadAccount reads the stored credential. Anything unreadable reads as SIGNED OUT
// rather than as an error: a half-written or corrupt credential must never be treated as
// a usable account.
func LoadAccount(dir string) (Account, bool) {
	b, err := os.ReadFile(filepath.Join(dir, accountFile))
	if err != nil {
		return Account{}, false
	}
	var a Account
	if err := json.Unmarshal(b, &a); err != nil {
		return Account{}, false
	}
	return a, a.SignedIn()
}

// SignOut removes the stored credential and nothing else. The Tower's own identity key
// and data directory are untouched, and a Tower that is already registered keeps serving
// until its lease expires or Roger Core revokes it - signing out on one machine is not a
// way to silently withdraw a Tower from the network.
func SignOut(dir string) error {
	err := os.Remove(filepath.Join(dir, accountFile))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// enrollFn is the network half, swappable for tests. Production points at enroll, which
// currently reports that Phase 2 has not shipped.
var enrollFn = enroll

func setEnrollForTest(f func(*tower.State, Account) error) func() {
	prev := enrollFn
	enrollFn = f
	return func() { enrollFn = prev }
}

// Register submits this Tower for admission to the public network.
//
// The two refusals below happen BEFORE any network call, so a mis-invoked registration
// cannot leave partial state anywhere - locally or at Roger Core.
func Register(st *tower.State, a Account) error {
	if st.Mode != tower.ModeJoined {
		return errors.New(
			"this Tower is standalone and cannot join the public network: standalone is a separate local network with its own trust root, " +
				"so joining means initializing a new data directory with --mode joined (nothing is copied automatically)")
	}
	if !a.SignedIn() {
		return errors.New(
			"registering a Tower requires a RogerAI account - sign in first with `roger-tower login`. " +
				"A joined Tower relays other people's traffic, so it must stay accountable - standalone mode needs no account at all")
	}
	return enrollFn(st, a)
}
