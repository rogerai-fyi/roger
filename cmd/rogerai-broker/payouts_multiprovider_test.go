package main

// payouts_multiprovider_test.go locks the account-model widening the founder asked for:
// a Tower operator (or any provider) must reach the SAME earnings + cash-out surface no
// matter WHICH login they signed up with - GitHub, Apple, or first-party email - because
// the logins were consolidated behind one website. Before the fix the payout/earnings
// surface gated on GitHubID != 0, so an Apple/email operator could serve traffic and earn
// but never see or withdraw a cent.
//
// Every provider is resolved by its own UNIQUE key (GitHub id / Apple sub / verified
// email), so this widening does NOT weaken features/security/apple_session_isolation:
// TestAppleSessionCannotReadGitHubAccount and friends still pass, and the cross-provider
// no-leak cases below are asserted directly.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"rogerai.fm/roger/v5/internal/store"
)

// earningsFor drives the REAL GET /payouts/earnings handler with the given session cookie
// and returns the decoded body + HTTP status.
func earningsFor(t *testing.T, b *broker, cookie string) (map[string]any, int) {
	t.Helper()
	w := httptest.NewRecorder()
	b.payoutsEarnings(w, withSession(httptest.NewRequest(http.MethodGet, "/payouts/earnings", nil), cookie))
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return out, w.Code
}

// TestAppleOperatorSeesEarnings: an operator who signed up with Apple (githubID==0, a
// stable Apple sub, an Apple wallet) reads their own tower-relay earnings.
func TestAppleOperatorSeesEarnings(t *testing.T) {
	mem := store.NewMem()
	b := relayBroker(mem)
	const pub = "applepub01"
	if err := mem.BindOwner(store.Owner{AppleSub: "apple-sub-777", Login: "op@privaterelay.appleid.com", Pubkey: pub, ConnectStatus: "active"}); err != nil {
		t.Fatal(err)
	}
	// A tower-relay earning lot for this owner (keyed by pubkey), tagged as tower.
	if err := mem.AddOperatorLot("tower:tw1", pub, "req-apple-1", 4.0, time.Now()); err != nil {
		t.Fatal(err)
	}
	cookie := b.signSessionFull("op@privaterelay.appleid.com", 0, "u_apple_777", "apple-sub-777", time.Now().Add(time.Hour).Unix())
	out, code := earningsFor(t, b, cookie)
	if code != http.StatusOK {
		t.Fatalf("Apple operator got %d reading own earnings, want 200: %v", code, out)
	}
	if tr, _ := out["tower_relay"].(float64); tr <= 0 {
		t.Fatalf("Apple operator's tower_relay earnings not surfaced: %v", out)
	}
}

// TestEmailOperatorSeesEarnings: an operator who signed up with a first-party verified
// email (githubID==0, no Apple sub, login == the proven address) reads their earnings.
func TestEmailOperatorSeesEarnings(t *testing.T) {
	mem := store.NewMem()
	b := relayBroker(mem)
	const pub = "emailpub01"
	const addr = "operator@example.test"
	if err := mem.BindOwner(store.Owner{Login: addr, Email: addr, EmailVerifiedAt: time.Now().Unix(), Pubkey: pub, ConnectStatus: "active"}); err != nil {
		t.Fatal(err)
	}
	if err := mem.AddOperatorLot("node-a", pub, "req-email-1", 6.0, time.Now()); err != nil {
		t.Fatal(err)
	}
	cookie := b.signSessionFull(addr, 0, walletForEmail(addr), "", time.Now().Add(time.Hour).Unix())
	out, code := earningsFor(t, b, cookie)
	if code != http.StatusOK {
		t.Fatalf("email operator got %d reading own earnings, want 200: %v", code, out)
	}
	if s, _ := out["serving"].(float64); s <= 0 {
		t.Fatalf("email operator's serving earnings not surfaced: %v", out)
	}
}

// TestEmailSessionNeedsVerifiedAddress: an email session whose address is only recorded on
// a profile (never PROVEN via a code) resolves NO owner - profile text is not identity, so
// it must not reach earnings. Fail-closed: 403, not a leak.
func TestEmailSessionNeedsVerifiedAddress(t *testing.T) {
	mem := store.NewMem()
	b := relayBroker(mem)
	const addr = "unverified@example.test"
	// EmailVerifiedAt == 0: the address is unproven.
	if err := mem.BindOwner(store.Owner{Login: addr, Email: addr, Pubkey: "unvpub", ConnectStatus: "active"}); err != nil {
		t.Fatal(err)
	}
	_ = mem.AddOperatorLot("node-a", "unvpub", "r", 5.0, time.Now())
	cookie := b.signSessionFull(addr, 0, walletForEmail(addr), "", time.Now().Add(time.Hour).Unix())
	_, code := earningsFor(t, b, cookie)
	if code != http.StatusForbidden {
		t.Fatalf("unverified-email session got %d, want 403 (unproven address is not identity)", code)
	}
}

// TestAppleSessionCannotReadGitHubEarnings: the isolation invariant under the widened
// resolver. A GitHub owner earns; an Apple session whose sub is bound to a DIFFERENT
// (Apple) owner must see its OWN (zero) earnings, never the GitHub owner's - even though
// both share the literal login "apple". Resolution is by Apple sub, not login.
func TestAppleSessionCannotReadGitHubEarnings(t *testing.T) {
	mem := store.NewMem()
	b := relayBroker(mem)
	// The high-value collision target: a GitHub owner whose login is literally "apple".
	if err := mem.BindOwner(store.Owner{GitHubID: 501, Login: "apple", Pubkey: "ghapplepub", ConnectStatus: "active"}); err != nil {
		t.Fatal(err)
	}
	if err := mem.AddOperatorLot("node-gh", "ghapplepub", "gh-req", 100.0, time.Now()); err != nil {
		t.Fatal(err)
	}
	// A DISTINCT Apple account that also happens to carry login "apple".
	if err := mem.BindOwner(store.Owner{AppleSub: "apple-sub-xyz", Login: "apple", Pubkey: "realapplepub", ConnectStatus: "active"}); err != nil {
		t.Fatal(err)
	}
	cookie := b.signSessionFull("apple", 0, "u_apple_xyz", "apple-sub-xyz", time.Now().Add(time.Hour).Unix())
	out, code := earningsFor(t, b, cookie)
	if code != http.StatusOK {
		t.Fatalf("Apple operator got %d, want 200: %v", code, out)
	}
	// Its own account has no lots -> zero. A leak would show the GitHub owner's 100.
	for _, k := range []string{"payable", "held", "serving", "tower_relay"} {
		if v, _ := out[k].(float64); v > 0 {
			t.Fatalf("Apple session saw the GitHub owner's %s=%v — cross-provider earnings leak", k, v)
		}
	}
}
