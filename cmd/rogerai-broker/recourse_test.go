package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/store"
)

// unhealthyStore wraps a Mem store but reports a store-backend failure from Healthy, so
// the /ready test can simulate the DB being down without a real Postgres.
type unhealthyStore struct {
	store.Store
}

func (u unhealthyStore) Healthy() error { return errors.New("connection refused") }

// newRecourseBroker wires a broker with a bound operator (pubkey = hex of priv's public
// key), one owned node, and an admin key set, so the owner-authed + admin-authed
// recourse endpoints can be exercised on both auth paths.
func newRecourseBroker(t *testing.T) (*broker, store.Store, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(nil)
	pubHex := hex.EncodeToString(pub)
	_, bpriv, _ := ed25519.GenerateKey(nil)
	db := store.NewMem()
	_ = db.BindOwner(store.Owner{GitHubID: 7, Login: "octocat", Pubkey: pubHex})
	_ = db.BindNode("n", pubHex)
	b := &broker{
		priv: bpriv, db: db, seedFunds: 0, conn: loadConnect(), pubOfUser: map[string]string{},
		bannedOwners: map[string]bool{}, strikeWarnAt: 3, strikeBanAt: 5,
		adminKey: "admin-secret-key", recountHoldDays: 7,
	}
	b.bill.creditUSD = 1
	return b, db, priv
}

// TestOwnerStrikesAuthAndScope locks operator recourse part (a): the strikes endpoint is
// owner-authed (anonymous 401), serves both auth paths (web session + signed CLI),
// returns the CALLER's own strikes, and is structurally cross-account safe (the lookup
// key is the authenticated pubkey, so a different caller never sees octocat's strikes).
func TestOwnerStrikesAuthAndScope(t *testing.T) {
	b, db, priv := newRecourseBroker(t)
	pubHex := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	// Accrue two strikes against the operator account.
	_, _ = db.OwnerStrike(pubHex, store.StrikeRecountDiscrepancy, `{"axis":"output"}`, "s1")
	_, _ = db.OwnerStrike(pubHex, store.StrikeEmptyOutput, `{"axis":"output"}`, "s2")

	// Anonymous -> 401.
	w := httptest.NewRecorder()
	b.ownerStrikes(w, httptest.NewRequest(http.MethodGet, "/owner/strikes", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("anon /owner/strikes = %d, want 401", w.Code)
	}

	// Both auth paths see the caller's own 2 strikes.
	for _, tc := range []struct {
		name string
		req  *http.Request
	}{
		{"session", sessionReq(b, http.MethodGet, "/owner/strikes", "octocat", 7)},
		{"signed", signedReq(http.MethodGet, "/owner/strikes", nil, priv)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			b.ownerStrikes(w, tc.req)
			if w.Code != http.StatusOK {
				t.Fatalf("%s = %d, want 200 (%s)", tc.name, w.Code, w.Body.String())
			}
			var resp struct {
				Strikes []store.Strike `json:"strikes"`
				Count   int            `json:"count"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}
			if resp.Count != 2 || len(resp.Strikes) != 2 {
				t.Errorf("%s strikes = %d/%d, want 2/2", tc.name, resp.Count, len(resp.Strikes))
			}
		})
	}

	// Cross-account: a DIFFERENT signed caller (mallory) bound to her own pubkey, with
	// NO strikes, must see 0 - she can never read octocat's strikes (the key is HER pubkey).
	mpub, mpriv, _ := ed25519.GenerateKey(nil)
	_ = db.BindOwner(store.Owner{GitHubID: 9, Login: "mallory", Pubkey: hex.EncodeToString(mpub)})
	w = httptest.NewRecorder()
	b.ownerStrikes(w, signedReq(http.MethodGet, "/owner/strikes", nil, mpriv))
	if w.Code != http.StatusOK {
		t.Fatalf("mallory = %d, want 200", w.Code)
	}
	var mr struct {
		Count int `json:"count"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &mr)
	if mr.Count != 0 {
		t.Errorf("cross-account leak: mallory saw %d strikes, want 0", mr.Count)
	}
}

// TestAdminUnholdPromotesFrozenLots locks operator recourse part (b): an admin-authed
// unhold clears a frozen operator's account hold + forgives strikes, and the previously
// frozen lots promote to payable again. Also locks the admin gate (no/bad key -> 403).
func TestAdminUnholdPromotesFrozenLots(t *testing.T) {
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "0")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	b, db, priv := newRecourseBroker(t)
	pubHex := hex.EncodeToString(priv.Public().(ed25519.PublicKey))

	// Operator earns a lot (immediately releasable), then gets frozen: a strike + account
	// hold (the strike path also bans at >=banAt; here 1 strike just holds + records).
	_, _ = db.AddCredits("u_gh_7", 30)
	_, _ = db.Settle("u_gh_7", "n", 30, 30, rec("rq1"))
	_, _ = db.OwnerStrike(pubHex, store.StrikeRecountDiscrepancy, `{"axis":"output"}`, "s1")
	_ = db.SetAccountRecountHold(pubHex, true)
	// While held, the lot must NOT be payable.
	if split, _ := db.EarningSplitOf(pubHex, time.Now()); split.Payable != 0 {
		t.Fatalf("held operator payable = %v, want 0 (frozen)", split.Payable)
	}

	// Admin gate: missing key -> 403.
	body, _ := json.Marshal(adminUnholdRequest{AccountID: pubHex, Forgive: true})
	r := httptest.NewRequest(http.MethodPost, "/admin/unhold", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	b.adminUnhold(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("unhold without admin key = %d, want 403", w.Code)
	}

	// Admin gate: wrong key -> 403.
	r = httptest.NewRequest(http.MethodPost, "/admin/unhold", strings.NewReader(string(body)))
	r.Header.Set("X-Roger-Admin", "nope")
	w = httptest.NewRecorder()
	b.adminUnhold(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("unhold with bad key = %d, want 403", w.Code)
	}

	// Correct admin key -> clears the hold + forgives strikes.
	r = httptest.NewRequest(http.MethodPost, "/admin/unhold", strings.NewReader(string(body)))
	r.Header.Set("X-Roger-Admin", "admin-secret-key")
	w = httptest.NewRecorder()
	b.adminUnhold(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("unhold with admin key = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	// The frozen lot now promotes to payable.
	if split, _ := db.EarningSplitOf(pubHex, time.Now()); split.Payable != 30 {
		t.Errorf("after unhold payable = %v, want 30 (lot promoted)", split.Payable)
	}
	// Strikes forgiven.
	if rem, _ := db.StrikesByOwner(pubHex, 0); len(rem) != 0 {
		t.Errorf("after forgive strikes = %d, want 0", len(rem))
	}
}

// TestRecountHoldSweepAutoExpires locks operator recourse part (c): a hold older than
// the window auto-clears (via the same ExpireRecountHolds the sweep calls) so an
// honest operator's frozen lots promote again with NO admin action.
func TestRecountHoldSweepAutoExpires(t *testing.T) {
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "0")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	b, db, priv := newRecourseBroker(t)
	pubHex := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	_, _ = db.AddCredits("u_gh_7", 30)
	_, _ = db.Settle("u_gh_7", "n", 30, 30, rec("rq1"))
	_ = db.SetAccountRecountHold(pubHex, true)
	if split, _ := db.EarningSplitOf(pubHex, time.Now()); split.Payable != 0 {
		t.Fatalf("held payable = %v, want 0", split.Payable)
	}
	// Expiry with a FUTURE cutoff (= "everything older than now+window") clears the hold,
	// exactly what recountHoldSweep does each tick with cutoff = now - window.
	n, err := db.ExpireRecountHolds(time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expired %d holds, want 1", n)
	}
	if split, _ := db.EarningSplitOf(pubHex, time.Now()); split.Payable != 30 {
		t.Errorf("after auto-expiry payable = %v, want 30", split.Payable)
	}
	_ = b // broker carries recountHoldDays=7; the sweep wiring is covered by build/vet
}

// TestReversalRecordedAndRetried locks the silent-money-leak guard: a FAILED Stripe
// reversal is recorded as a durable open intent (not dropped), and the retry sweep's
// re-attempt succeeds and marks it done so it drops from the open set.
func TestReversalRecordedAndRetried(t *testing.T) {
	b, db, _ := newRecourseBroker(t)

	// First attempt FAILS: reversePaidLots must record the intent and NOT drop it.
	fail := true
	b.conn.reverseTransfer = func(transferID string, cents int64, idem string) (string, error) {
		if fail {
			return "", errors.New("stripe 500")
		}
		return "trr_ok", nil
	}
	b.reversePaidLots("dp1", []store.Reversal{
		{DisputeID: "dp1", LotID: 42, AccountID: "pk1", TransferID: "tr_1", Amount: 9},
	})
	open, _ := db.OpenPendingReversals(0)
	if len(open) != 1 {
		t.Fatalf("after failed reversal open = %d, want 1 (recorded, not dropped)", len(open))
	}
	if open[0].Attempts != 1 {
		t.Errorf("attempts = %d, want 1", open[0].Attempts)
	}

	// Now Stripe recovers; one retry sweep pass (inlined) reverses + marks it done.
	fail = false
	for _, pr := range open {
		revID, rerr := b.payoutTransferReversal(pr.TransferID, pr.Amount, pr.Key)
		if rerr != nil {
			t.Fatalf("retry should succeed, got %v", rerr)
		}
		if revID == "" {
			t.Fatal("retry returned empty reversal id")
		}
		_ = db.MarkReversalAttempt(pr.Key, true, "", b.reversalMaxAttempts(), time.Now())
	}
	open, _ = db.OpenPendingReversals(0)
	if len(open) != 0 {
		t.Errorf("after recovered retry open = %d, want 0 (done)", len(open))
	}
}

// TestReadyReports503WhenDBDown locks readiness: /ready returns 200 when the store is
// healthy and 503 (with a JSON status) when the store backend is down. /health stays a
// cheap static "ok" regardless.
func TestReadyReports503WhenDBDown(t *testing.T) {
	b, _, _ := newRecourseBroker(t)

	// Healthy store -> 200 ready:true.
	w := httptest.NewRecorder()
	b.ready(w, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("ready (healthy) = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	var ok struct {
		Ready bool `json:"ready"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &ok)
	if !ok.Ready {
		t.Error("ready:true expected when store healthy")
	}

	// DB down -> 503 ready:false.
	b.db = unhealthyStore{b.db}
	w = httptest.NewRecorder()
	b.ready(w, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("ready (db down) = %d, want 503", w.Code)
	}
	var bad struct {
		Ready bool   `json:"ready"`
		DB    string `json:"db"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &bad)
	if bad.Ready || bad.DB != "down" {
		t.Errorf("ready (db down) body = %+v, want ready:false db:down", bad)
	}
}

// TestAdminSurfaceClosedWithoutKey locks the closed-by-default admin gate: with no admin
// key configured, EVERY admin request is rejected (even with a header).
func TestAdminSurfaceClosedWithoutKey(t *testing.T) {
	b, _, _ := newRecourseBroker(t)
	b.adminKey = "" // ephemeral / unset broker key
	body, _ := json.Marshal(adminUnholdRequest{AccountID: "pk1"})
	r := httptest.NewRequest(http.MethodPost, "/admin/unhold", strings.NewReader(string(body)))
	r.Header.Set("X-Roger-Admin", "anything")
	w := httptest.NewRecorder()
	b.adminUnhold(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("admin op with no key configured = %d, want 403 (closed by default)", w.Code)
	}
}
