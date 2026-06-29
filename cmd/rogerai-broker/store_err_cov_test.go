package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/store"
)

var errBoom = errors.New("store backend down")

// failStore wraps a real store and forces selected read/write methods to error, so the
// handler "store error" 500 branches can be exercised without a real broken backend. Every
// non-overridden method delegates to the embedded store (so auth/owner lookups still work).
type failStore struct {
	store.Store
	failGrants      bool
	failBands       bool
	failRemaskBands bool
	failSetCap      bool
	failStrikes     bool
	failPending     bool
	failAppeals     bool
	failNodes       bool
	failAddAppeal   bool
	failDelete      bool
	failSettlePay   bool
	failNodeHold    bool
	failAcctHold    bool
	failForgive     bool
	failWalletCh    bool
	failCreditOn    bool

	// safety / strike surfaces (report.go + strikes.go error branches)
	failAddReport    bool
	failPreserveCSAM bool
	failBanNode      bool
	failUnbanNode    bool
	failExpireBans   bool
	failBannedNodes  bool
	failOwnerStrike  bool
	failStrikeStats  bool
	failBanOwner     bool
	failBannedOwners bool
}

func (f *failStore) AddReport(r store.Report) (int64, error) {
	if f.failAddReport {
		return 0, errBoom
	}
	return f.Store.AddReport(r)
}
func (f *failStore) PreserveCSAM(inc store.CSAMIncident) (int64, error) {
	if f.failPreserveCSAM {
		return 0, errBoom
	}
	return f.Store.PreserveCSAM(inc)
}
func (f *failStore) BanNode(nodeID, reason string) error {
	if f.failBanNode {
		return errBoom
	}
	return f.Store.BanNode(nodeID, reason)
}
func (f *failStore) UnbanNode(nodeID string) error {
	if f.failUnbanNode {
		return errBoom
	}
	return f.Store.UnbanNode(nodeID)
}
func (f *failStore) ExpireNodeBans(olderThan time.Time) ([]string, error) {
	if f.failExpireBans {
		return nil, errBoom
	}
	return f.Store.ExpireNodeBans(olderThan)
}
func (f *failStore) BannedNodes() (map[string]string, error) {
	if f.failBannedNodes {
		return nil, errBoom
	}
	return f.Store.BannedNodes()
}
func (f *failStore) OwnerStrike(accountID, kind, evidenceJSON, idemKey string) (int, error) {
	if f.failOwnerStrike {
		return 0, errBoom
	}
	return f.Store.OwnerStrike(accountID, kind, evidenceJSON, idemKey)
}
func (f *failStore) OwnerStrikeStats(accountID string, since int64) (int, int, error) {
	if f.failStrikeStats {
		return 0, 0, errBoom
	}
	return f.Store.OwnerStrikeStats(accountID, since)
}
func (f *failStore) BanOwner(accountID, reason, evidenceJSON string) error {
	if f.failBanOwner {
		return errBoom
	}
	return f.Store.BanOwner(accountID, reason, evidenceJSON)
}
func (f *failStore) BannedOwners() (map[string]string, error) {
	if f.failBannedOwners {
		return nil, errBoom
	}
	return f.Store.BannedOwners()
}

func (f *failStore) WalletByCharge(ref string) (string, float64, bool, error) {
	if f.failWalletCh {
		return "", 0, false, errBoom
	}
	return f.Store.WalletByCharge(ref)
}
func (f *failStore) CreditOnce(key, user string, amount float64) (bool, float64, error) {
	if f.failCreditOn {
		return false, 0, errBoom
	}
	return f.Store.CreditOnce(key, user, amount)
}

func (f *failStore) SettlePayout(id int64, tr string) error {
	if f.failSettlePay {
		return errBoom
	}
	return f.Store.SettlePayout(id, tr)
}
func (f *failStore) SetNodeRecountHold(node string, held bool) error {
	if f.failNodeHold {
		return errBoom
	}
	return f.Store.SetNodeRecountHold(node, held)
}
func (f *failStore) SetAccountRecountHold(acct string, held bool) error {
	if f.failAcctHold {
		return errBoom
	}
	return f.Store.SetAccountRecountHold(acct, held)
}
func (f *failStore) ForgiveOwner(acct string) (int, error) {
	if f.failForgive {
		return 0, errBoom
	}
	return f.Store.ForgiveOwner(acct)
}

func (f *failStore) GrantsByOwner(o string) ([]store.Grant, error) {
	if f.failGrants {
		return nil, errBoom
	}
	return f.Store.GrantsByOwner(o)
}
func (f *failStore) BandsByOwner(o string) ([]store.Band, error) {
	if f.failBands {
		return nil, errBoom
	}
	return f.Store.BandsByOwner(o)
}
func (f *failStore) RemaskBandDisplays() (int, error) {
	if f.failRemaskBands {
		return 0, errBoom
	}
	return f.Store.RemaskBandDisplays()
}
func (f *failStore) SetMonthlyCap(h string, c float64) error {
	if f.failSetCap {
		return errBoom
	}
	return f.Store.SetMonthlyCap(h, c)
}
func (f *failStore) StrikesByOwner(a string, n int) ([]store.Strike, error) {
	if f.failStrikes {
		return nil, errBoom
	}
	return f.Store.StrikesByOwner(a, n)
}
func (f *failStore) PendingAppeals(n int) ([]store.Appeal, error) {
	if f.failPending {
		return nil, errBoom
	}
	return f.Store.PendingAppeals(n)
}
func (f *failStore) AppealsByOwner(a string, n int) ([]store.Appeal, error) {
	if f.failAppeals {
		return nil, errBoom
	}
	return f.Store.AppealsByOwner(a, n)
}
func (f *failStore) NodesOfAccount(a string) ([]string, error) {
	if f.failNodes {
		return nil, errBoom
	}
	return f.Store.NodesOfAccount(a)
}
func (f *failStore) AddAppeal(a store.Appeal) (int64, error) {
	if f.failAddAppeal {
		return 0, errBoom
	}
	return f.Store.AddAppeal(a)
}
func (f *failStore) DeleteAccount(login string) (bool, error) {
	if f.failDelete {
		return false, errBoom
	}
	return f.Store.DeleteAccount(login)
}

// brokerWithFailStore returns a brokerWithOwner whose db is wrapped in a failStore the
// caller can toggle, plus the owner.
func brokerWithFailStore(t *testing.T) (*broker, *failStore, store.Owner) {
	t.Helper()
	b, o := brokerWithOwner(t)
	fs := &failStore{Store: b.db}
	b.db = fs
	b.adminKey = "admin-k"
	return b, fs, o
}

// TestStoreErrorBranches drives the store-error 500 path of every handler that wraps a
// store read/write in an explicit error check, confirming each fails closed with a 500
// rather than serving partial/empty data.
func TestStoreErrorBranches(t *testing.T) {
	t.Run("grantList", func(t *testing.T) {
		b, fs, _ := brokerWithFailStore(t)
		fs.failGrants = true
		w := httptest.NewRecorder()
		b.grants(w, sessionReq(b, http.MethodGet, "/grants", "octocat", 7))
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("grantList store-err = %d, want 500", w.Code)
		}
	})

	t.Run("bands", func(t *testing.T) {
		b, fs, o := brokerWithFailStore(t)
		fs.failBands = true
		w := httptest.NewRecorder()
		b.bands(w, ownerReq(http.MethodGet, "/bands", o.Pubkey))
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("bands store-err = %d, want 500", w.Code)
		}
	})

	t.Run("accountLimit", func(t *testing.T) {
		b, fs, _ := brokerWithFailStore(t)
		fs.failSetCap = true
		w := httptest.NewRecorder()
		b.accountLimit(w, sessionPost(b, http.MethodPatch, "/account/limit", "octocat", 7, `{"monthly_cap":5}`))
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("accountLimit store-err = %d, want 500", w.Code)
		}
	})

	t.Run("ownerStrikes", func(t *testing.T) {
		b, fs, _ := brokerWithFailStore(t)
		fs.failStrikes = true
		w := httptest.NewRecorder()
		b.ownerStrikes(w, sessionReq(b, http.MethodGet, "/owner/strikes", "octocat", 7))
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("ownerStrikes store-err = %d, want 500", w.Code)
		}
	})

	t.Run("adminAppeals", func(t *testing.T) {
		b, fs, _ := brokerWithFailStore(t)
		fs.failPending = true
		r := httptest.NewRequest(http.MethodGet, "/admin/appeals", nil)
		r.Header.Set("X-Roger-Admin", "admin-k")
		w := httptest.NewRecorder()
		b.adminAppeals(w, r)
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("adminAppeals store-err = %d, want 500", w.Code)
		}
	})

	t.Run("ownerAppealGet", func(t *testing.T) {
		b, fs, _ := brokerWithFailStore(t)
		fs.failAppeals = true
		w := httptest.NewRecorder()
		b.ownerAppeal(w, sessionReq(b, http.MethodGet, "/owner/appeal", "octocat", 7))
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("ownerAppeal GET store-err = %d, want 500", w.Code)
		}
	})

	t.Run("ownerAppealNodesErr", func(t *testing.T) {
		b, fs, _ := brokerWithFailStore(t)
		fs.failNodes = true
		w := httptest.NewRecorder()
		b.ownerAppeal(w, sessionPost(b, http.MethodPost, "/owner/appeal", "octocat", 7, `{"node_id":"n","reason":"x"}`))
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("ownerAppeal NodesOfAccount store-err = %d, want 500", w.Code)
		}
	})

	t.Run("ownerAppealAddErr", func(t *testing.T) {
		b, fs, _ := brokerWithFailStore(t)
		fs.failAddAppeal = true
		w := httptest.NewRecorder()
		b.ownerAppeal(w, sessionPost(b, http.MethodPost, "/owner/appeal", "octocat", 7, `{"reason":"no node so AddAppeal is reached"}`))
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("ownerAppeal AddAppeal store-err = %d, want 500", w.Code)
		}
	})

	t.Run("accountDelete", func(t *testing.T) {
		b, fs, _ := brokerWithFailStore(t)
		fs.failDelete = true // clean account (no balance/earnings) so DeleteAccount is reached
		w := httptest.NewRecorder()
		b.accountDelete(w, sessionReq(b, http.MethodPost, "/account/delete", "octocat", 7))
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("accountDelete store-err = %d, want 500", w.Code)
		}
	})

	t.Run("adminUnholdNode", func(t *testing.T) {
		b, fs, _ := brokerWithFailStore(t)
		fs.failNodeHold = true
		r := httptest.NewRequest(http.MethodPost, "/admin/unhold", strings.NewReader(`{"node":"n"}`))
		r.Header.Set("X-Roger-Admin", "admin-k")
		w := httptest.NewRecorder()
		b.adminUnhold(w, r)
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("adminUnhold node store-err = %d, want 500", w.Code)
		}
	})

	t.Run("adminUnholdAccount", func(t *testing.T) {
		b, fs, _ := brokerWithFailStore(t)
		fs.failAcctHold = true
		r := httptest.NewRequest(http.MethodPost, "/admin/unhold", strings.NewReader(`{"account_id":"pk1"}`))
		r.Header.Set("X-Roger-Admin", "admin-k")
		w := httptest.NewRecorder()
		b.adminUnhold(w, r)
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("adminUnhold account store-err = %d, want 500", w.Code)
		}
	})

	t.Run("adminUnholdForgive", func(t *testing.T) {
		b, fs, _ := brokerWithFailStore(t)
		fs.failForgive = true
		r := httptest.NewRequest(http.MethodPost, "/admin/unhold", strings.NewReader(`{"account_id":"pk1","forgive":true}`))
		r.Header.Set("X-Roger-Admin", "admin-k")
		w := httptest.NewRecorder()
		b.adminUnhold(w, r)
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("adminUnhold forgive store-err = %d, want 500", w.Code)
		}
	})
}
