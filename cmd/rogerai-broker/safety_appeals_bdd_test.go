package main

// safety_appeals_bdd_test.go makes features/safety/appeals.feature EXECUTABLE, driving the
// REAL recourse surface (recourse.go): an operator reads their OWN strikes + node-ban status
// (ownerStrikes, owner-scoped to the authenticated pubkey), files a SELF-SERVE appeal
// (ownerAppeal POST -> AddAppeal, visible as pending on GET), the founder reviews the open
// queue (adminAppeals, admin-gated), a reviewed unhold clears a recount hold (adminUnhold ->
// SetAccountRecountHold(false)), forgive ALSO wipes strikes + lifts the ban (ForgiveOwner +
// the in-memory owner-ban cache), a hold auto-expires past the window (recountHoldSweepOnce
// -> ExpireRecountHolds), and every admin control is founder-gated (requireAdmin -> 403).
// Reuses newRecourseBroker + the sessionReq/sessionPost auth helpers; reads store state and
// real handler responses - no mocks.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/store"
)

type apState struct {
	t     *testing.T
	b     *broker
	db    store.Store
	priv  ed25519.PrivateKey
	acct  string // the authenticated owner's pubkey (the account key for strikes/holds/appeals)
	login string
	gid   int64

	resp map[string]any // last decoded handler JSON
	code int            // last handler status
}

func (s *apState) reset() {
	s.b, s.db, s.priv = newRecourseBroker(s.t)
	s.b.banned = map[string]bool{} // newRecourseBroker leaves it nil; banNode writes to it
	s.acct = hex.EncodeToString(s.priv.Public().(ed25519.PublicKey))
	s.login, s.gid = "octocat", 7
	s.resp, s.code = nil, 0
}

func (s *apState) call(h http.HandlerFunc, r *http.Request) {
	w := httptest.NewRecorder()
	h(w, r)
	s.code = w.Code
	s.resp = map[string]any{}
	_ = json.Unmarshal(w.Body.Bytes(), &s.resp)
}

func (s *apState) adminGET(path string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.Header.Set("X-Roger-Admin", "admin-secret-key")
	return r
}

func (s *apState) adminPOST(path, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(body)))
	r.Header.Set("X-Roger-Admin", "admin-secret-key")
	return r
}

func (s *apState) expireCount(window time.Duration) int {
	n, _ := s.db.ExpireRecountHolds(time.Now().Add(window))
	return n
}

// --- Scenario 1: operator sees own strikes + status ------------------------

func (s *apState) operatorHasStrikesAndNodeBan(_ string) error {
	_, _ = s.db.OwnerStrike(s.acct, store.StrikeRecountDiscrepancy, `{"axis":"output"}`, "s1")
	_, _ = s.db.OwnerStrike(s.acct, store.StrikeEmptyOutput, `{"axis":"output"}`, "s2")
	s.b.banNode("n", "report abuse") // node "n" is bound to this owner in newRecourseBroker
	return nil
}

func (s *apState) readsOwnStrikes() error {
	s.call(s.b.ownerStrikes, sessionReq(s.b, http.MethodGet, "/owner/strikes", s.login, s.gid))
	return nil
}

func (s *apState) seesCountEvidenceNodeBan() error {
	if s.code != http.StatusOK {
		return fmt.Errorf("ownerStrikes = %d, want 200", s.code)
	}
	if c, _ := s.resp["count"].(float64); c < 1 {
		return fmt.Errorf("strike count = %v, want >= 1 (the operator must see their strikes)", s.resp["count"])
	}
	if strikes, _ := s.resp["strikes"].([]any); len(strikes) == 0 {
		return fmt.Errorf("strikes (with evidence) must be surfaced to the operator")
	}
	nb, _ := s.resp["node_bans"].(map[string]any)
	if _, ok := nb["n"]; !ok {
		return fmt.Errorf("node-ban status for the owner's node must be visible, got %v", s.resp["node_bans"])
	}
	return nil
}

// --- Scenario 2: self-serve appeal -----------------------------------------

func (s *apState) bannedOrStruck(_ string) error {
	_, _ = s.db.OwnerStrike(s.acct, store.StrikeRecountDiscrepancy, `{"axis":"output"}`, "s1")
	return nil
}

func (s *apState) filesAppeal() error {
	s.call(s.b.ownerAppeal, sessionPost(s.b, http.MethodPost, "/owner/appeal", s.login, s.gid, `{"reason":"this looks like a false positive"}`))
	if s.code != http.StatusOK {
		return fmt.Errorf("file appeal = %d, want 200 (%v)", s.code, s.resp)
	}
	return nil
}

func (s *apState) appealRecordedPending() error {
	if ok, _ := s.resp["ok"].(bool); !ok {
		return fmt.Errorf("appeal POST should report ok=true")
	}
	if st, _ := s.resp["state"].(string); st != store.AppealOpen {
		return fmt.Errorf("new appeal state = %q, want %q (pending)", s.resp["state"], store.AppealOpen)
	}
	// GET the caller's own appeals: the filed one is visible.
	s.call(s.b.ownerAppeal, sessionReq(s.b, http.MethodGet, "/owner/appeal", s.login, s.gid))
	if c, _ := s.resp["count"].(float64); c < 1 {
		return fmt.Errorf("the operator must see their pending appeal on GET, count=%v", s.resp["count"])
	}
	return nil
}

// --- Scenario 3: founder review queue --------------------------------------

func (s *apState) appealsPending() error {
	_, err := s.db.AddAppeal(store.Appeal{AccountID: s.acct, Reason: "please review"})
	return err
}

func (s *apState) founderReadsQueue() error {
	s.call(s.b.adminAppeals, s.adminGET("/admin/appeals"))
	return nil
}

func (s *apState) openAppealsListed() error {
	if s.code != http.StatusOK {
		return fmt.Errorf("adminAppeals = %d, want 200", s.code)
	}
	if c, _ := s.resp["count"].(float64); c < 1 {
		return fmt.Errorf("the admin queue must list the pending appeal(s), count=%v", s.resp["count"])
	}
	return nil
}

// --- Scenario 4: reviewed unhold clears a recount hold ---------------------

func (s *apState) accountUnderHold(_ string) error { return s.db.SetAccountRecountHold(s.acct, true) }

func (s *apState) founderUnholds(_ string) error {
	s.call(s.b.adminUnhold, s.adminPOST("/admin/unhold", `{"account_id":"`+s.acct+`"}`))
	if s.code != http.StatusOK {
		return fmt.Errorf("adminUnhold = %d, want 200 (%v)", s.code, s.resp)
	}
	return nil
}

func (s *apState) holdClearedLotsPromote() error {
	if n := s.expireCount(8 * 24 * time.Hour); n != 0 {
		return fmt.Errorf("the hold should already be cleared by the unhold; a sweep still expired %d", n)
	}
	return nil
}

// --- Scenario 5: forgive clears hold + wipes strikes + lifts ban -----------

func (s *apState) bannedAndUnderHold(_ string) error {
	_, _ = s.db.OwnerStrike(s.acct, store.StrikeRecountDiscrepancy, `{"axis":"output"}`, "s1")
	s.b.banOwner(s.acct, "abuse", "{}") // durable ban + in-memory bannedOwners cache
	return s.db.SetAccountRecountHold(s.acct, true)
}

func (s *apState) founderUnholdsWithForgive() error {
	s.call(s.b.adminUnhold, s.adminPOST("/admin/unhold", `{"account_id":"`+s.acct+`","forgive":true}`))
	if s.code != http.StatusOK {
		return fmt.Errorf("adminUnhold forgive = %d, want 200 (%v)", s.code, s.resp)
	}
	return nil
}

func (s *apState) fullReinstatement() error {
	if strikes, _ := s.db.StrikesByOwner(s.acct, 0); len(strikes) != 0 {
		return fmt.Errorf("forgive must wipe strikes, %d remain", len(strikes))
	}
	if banned, _, _ := s.db.IsOwnerBanned(s.acct); banned {
		return fmt.Errorf("forgive must lift the durable ban")
	}
	if s.b.isOwnerBanned(s.acct) {
		return fmt.Errorf("forgive must refresh the in-memory owner-ban cache (still banned in memory)")
	}
	if n := s.expireCount(8 * 24 * time.Hour); n != 0 {
		return fmt.Errorf("forgive must clear the hold; a sweep still expired %d", n)
	}
	return nil
}

// --- Scenario 6: hold auto-expires past the window -------------------------

func (s *apState) accountPlacedUnderHold() error { return s.db.SetAccountRecountHold(s.acct, true) }

func (s *apState) windowPasses() error {
	// recountHoldDays is 7; a sweep with a cutoff past the window auto-expires the hold.
	s.b.recountHoldSweepOnce(time.Now().Add(8 * 24 * time.Hour))
	return nil
}

func (s *apState) sweepExpiresHold() error {
	if n := s.expireCount(8 * 24 * time.Hour); n != 0 {
		return fmt.Errorf("the periodic sweep should have auto-expired the hold; %d still pending", n)
	}
	return nil
}

// --- Scenario 7: admin controls founder-gated ------------------------------

func (s *apState) nonAdminCaller() error { return nil }

func (s *apState) hitsAdminControls() error {
	wA := httptest.NewRecorder()
	s.b.adminAppeals(wA, httptest.NewRequest(http.MethodGet, "/admin/appeals", nil)) // no admin header
	wU := httptest.NewRecorder()
	s.b.adminUnhold(wU, httptest.NewRequest(http.MethodPost, "/admin/unhold", bytes.NewReader([]byte(`{"account_id":"`+s.acct+`"}`))))
	s.code = wA.Code
	if wU.Code > s.code {
		s.code = wU.Code
	}
	s.resp = map[string]any{"appeals_code": wA.Code, "unhold_code": wU.Code}
	return nil
}

func (s *apState) refusedSuperAdminOnly() error {
	ac, _ := s.resp["appeals_code"].(int)
	uc, _ := s.resp["unhold_code"].(int)
	if ac != http.StatusForbidden || uc != http.StatusForbidden {
		return fmt.Errorf("non-admin must be refused 403 on both controls; got appeals=%d unhold=%d", ac, uc)
	}
	return nil
}

func TestSafetyAppealsBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &apState{t: t}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			sc.Step(`^operator "([^"]*)" has strikes and a node ban$`, st.operatorHasStrikesAndNodeBan)
			sc.Step(`^they read their own strikes$`, st.readsOwnStrikes)
			sc.Step(`^they see the strike count, evidence, and node-ban status \(transparency\)$`, st.seesCountEvidenceNodeBan)
			sc.Step(`^"([^"]*)" is banned \(or struck\)$`, st.bannedOrStruck)
			sc.Step(`^they file an appeal with a reason$`, st.filesAppeal)
			sc.Step(`^the appeal is recorded and visible to the operator as pending$`, st.appealRecordedPending)
			sc.Step(`^one or more appeals are pending$`, st.appealsPending)
			sc.Step(`^the founder reads the admin appeals queue$`, st.founderReadsQueue)
			sc.Step(`^the open appeals are listed for review \(admin-gated\)$`, st.openAppealsListed)
			sc.Step(`^account "([^"]*)" is under a recount hold$`, st.accountUnderHold)
			sc.Step(`^the founder issues an admin unhold for "([^"]*)"$`, st.founderUnholds)
			sc.Step(`^the hold is cleared and the operator's earning lots promote on the next sweep$`, st.holdClearedLotsPromote)
			sc.Step(`^"([^"]*)" is banned and under a hold$`, st.bannedAndUnderHold)
			sc.Step(`^the founder unholds with forgive$`, st.founderUnholdsWithForgive)
			sc.Step(`^the hold clears, the strikes are wiped, and the ban is lifted \(full reinstatement\)$`, st.fullReinstatement)
			sc.Step(`^an account placed under a recount hold$`, st.accountPlacedUnderHold)
			sc.Step(`^more than recountHoldDays pass$`, st.windowPasses)
			sc.Step(`^the periodic sweep expires the hold \(a transient hold never traps earnings forever\)$`, st.sweepExpiresHold)
			sc.Step(`^a non-admin caller$`, st.nonAdminCaller)
			sc.Step(`^they hit the admin appeals queue or the unhold control$`, st.hitsAdminControls)
			sc.Step(`^they are refused \(these are super-admin-only, like the rest of /admin\)$`, st.refusedSuperAdminOnly)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/safety/appeals.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("safety/appeals behavior scenarios failed (see godog output above)")
	}
}
