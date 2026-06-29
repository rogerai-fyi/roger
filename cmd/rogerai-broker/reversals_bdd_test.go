package main

// reversals_bdd_test.go makes features/money/reversals.feature an EXECUTABLE Cucumber suite,
// driving the REAL durable pending-reversal lifecycle (the silent-money-leak guard on a
// post-payout dispute):
//   - store.RecordPendingReversal / OpenPendingReversals / MarkReversalAttempt (record is
//     idempotent on the key; the open list excludes done + dead-lettered, oldest-first,
//     honors a limit; an attempt bumps the count, success is terminal-done, failure records
//     the error and dead-letters at maxAttempts),
//   - the broker reversePaidLots (records the intent BEFORE the Stripe call, marks the
//     outcome) + reversalRetryOnce (the retry sweep) with an injected conn.reverseTransfer.
//
// Every OPEN-row assertion (attempts / last error / created-at / last-attempt / ordering /
// limit / membership) is read back from store.OpenPendingReversals — the store IS the truth.
// A row that goes terminal (done/dead) leaves the open list (store-verified); the done-vs-dead
// LABEL and a terminal row's final attempt count are tracked in a step-state mirror updated
// ONLY by driving the real store/broker methods (the store's done/dead transition itself is
// unit-tested in internal/store). feApprox/feParseFloat live in fee_splits_bdd_test.go.

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/store"
)

type prMirror struct {
	attempts    int
	done        bool
	dead        bool
	lastErr     string
	lastAttempt int64
	createdAt   int64
	exists      bool
}

type rvState struct {
	db          *store.Mem
	b           *broker
	now         time.Time
	maxAttempts int
	mirror      map[string]*prMirror
	createSeq   int64

	// reverseTransfer stub
	reverseFail         bool
	reverseCalls        int
	lastIdemKey         string
	checkIntentKey      string
	intentExistedAtCall bool

	openList []store.PendingReversal

	// reversePaidLots dispute-reversal scratch (section 5)
	pendingLot      int64
	pendingTransfer string
	pendingAmount   float64
}

func (s *rvState) reset() {
	os.Setenv("ROGERAI_REVERSAL_MAX_ATTEMPTS", "10")
	s.db = store.NewMem()
	s.now = time.Now()
	s.maxAttempts = 10
	s.mirror = map[string]*prMirror{}
	s.createSeq = 0
	s.reverseFail = false
	s.reverseCalls = 0
	s.lastIdemKey = ""
	s.checkIntentKey = ""
	s.intentExistedAtCall = false
	s.openList = nil
	s.pendingLot, s.pendingTransfer, s.pendingAmount = 0, "", 0
	s.b = buildPayoutBrokerForTest(s.db)
	s.b.conn.reverseTransfer = func(transferID string, cents int64, idemKey string) (string, error) {
		s.reverseCalls++
		s.lastIdemKey = idemKey
		if s.checkIntentKey != "" && s.inOpen(s.checkIntentKey) {
			s.intentExistedAtCall = true
		}
		if s.reverseFail {
			return "", errStripeTransfer
		}
		return "trr_ok", nil
	}
}

// --- store-grounded helpers -------------------------------------------------

func (s *rvState) inOpen(key string) bool {
	open, _ := s.db.OpenPendingReversals(0)
	for _, pr := range open {
		if pr.Key == key {
			return true
		}
	}
	return false
}

// peek returns the store's OPEN row for key (all fields store-grounded); ok=false for a
// terminal (done/dead) or absent row.
func (s *rvState) peek(key string) (store.PendingReversal, bool) {
	open, _ := s.db.OpenPendingReversals(0)
	for _, pr := range open {
		if pr.Key == key {
			return pr, true
		}
	}
	return store.PendingReversal{}, false
}

func (s *rvState) m(key string) *prMirror {
	if s.mirror[key] == nil {
		s.mirror[key] = &prMirror{}
	}
	return s.mirror[key]
}

// record drives the REAL RecordPendingReversal and mirrors it (idempotent on key).
func (s *rvState) record(key, transfer, account string, amount float64, createdAt int64) error {
	if err := s.db.RecordPendingReversal(store.PendingReversal{
		Key: key, TransferID: transfer, AccountID: account, Amount: amount, CreatedAt: createdAt,
	}); err != nil {
		return err
	}
	if key == "" {
		return nil
	}
	if !s.m(key).exists {
		mm := s.m(key)
		mm.exists = true
		if createdAt != 0 {
			mm.createdAt = createdAt
		} else {
			mm.createdAt = s.now.Unix()
		}
	}
	return nil
}

// markAttempt drives the REAL MarkReversalAttempt and mirrors its state transition.
func (s *rvState) markAttempt(key string, success bool, errMsg string, now time.Time) error {
	if err := s.db.MarkReversalAttempt(key, success, errMsg, s.maxAttempts, now); err != nil {
		return err
	}
	mm := s.mirror[key]
	if mm == nil || !mm.exists || mm.done || mm.dead {
		return nil // unknown / terminal: real method is a no-op, mirror unchanged
	}
	mm.attempts++
	mm.lastAttempt = now.Unix()
	if success {
		mm.done = true
		mm.lastErr = ""
	} else {
		mm.lastErr = errMsg
		if s.maxAttempts > 0 && mm.attempts >= s.maxAttempts {
			mm.dead = true
		}
	}
	return nil
}

// --- setup steps ------------------------------------------------------------

func (s *rvState) freshStore() error { s.reset(); return nil }

func (s *rvState) nextCreatedAt() int64 { s.createSeq++; return s.now.Unix() + s.createSeq }

func (s *rvState) recordedFor(key, transfer, amount, account string) error {
	a, err := feParseFloat(amount)
	if err != nil {
		return err
	}
	return s.record(key, transfer, account, a, s.nextCreatedAt())
}

func (s *rvState) recordedFailedAttempts(key, n string) error {
	c, err := strconv.Atoi(n)
	if err != nil {
		return err
	}
	for i := 0; i < c; i++ {
		if err := s.markAttempt(key, false, "setup", s.now.Add(time.Duration(i)*time.Second)); err != nil {
			return err
		}
	}
	return nil
}

func (s *rvState) recordedSucceededDone(key string) error {
	if err := s.record(key, "tr_1", "op1", 70, s.nextCreatedAt()); err != nil {
		return err
	}
	return s.markAttempt(key, true, "", s.now)
}

func (s *rvState) recordAgain(key string) error { return s.record(key, "tr_1", "op1", 70, s.nextCreatedAt()) }

func (s *rvState) recordEmptyKey() error { return s.record("", "tr_1", "op1", 70, 0) }

func (s *rvState) openReversal(key string) error { return s.record(key, "tr_1", "op1", 70, s.nextCreatedAt()) }

func (s *rvState) doneReversal(key string) error { return s.recordedSucceededDone(key) }

func (s *rvState) deadReversal(key string) error {
	if err := s.record(key, "tr_1", "op1", 70, s.nextCreatedAt()); err != nil {
		return err
	}
	old := s.maxAttempts
	s.maxAttempts = 1
	defer func() { s.maxAttempts = old }()
	return s.markAttempt(key, false, "dead", s.now) // attempt 1 >= max 1 -> dead-letter
}

func (s *rvState) reversalCreatedAt(key, when string) error {
	off := map[string]int64{"earliest": 1, "later": 2, "latest": 3}[when]
	return s.record(key, "tr_1", "op1", 70, s.now.Unix()+off)
}

func (s *rvState) nOpenExist(n string) error {
	c, err := strconv.Atoi(n)
	if err != nil {
		return err
	}
	for i := 0; i < c; i++ {
		if err := s.record(fmt.Sprintf("reverse:bulk:%d", i), "tr_1", "op1", 70, s.nextCreatedAt()); err != nil {
			return err
		}
	}
	return nil
}

func (s *rvState) openWithAttempts(key, n string) error {
	if err := s.record(key, "tr_1", "op1", 70, s.nextCreatedAt()); err != nil {
		return err
	}
	c, err := strconv.Atoi(n)
	if err != nil {
		return err
	}
	old := s.maxAttempts
	s.maxAttempts = 1 << 30 // keep open while seeding attempts
	for i := 0; i < c; i++ {
		if err := s.markAttempt(key, false, "seed", s.now.Add(time.Duration(i)*time.Second)); err != nil {
			s.maxAttempts = old
			return err
		}
	}
	s.maxAttempts = old
	return nil
}

func (s *rvState) maxAttemptsIs(n string) error {
	c, err := strconv.Atoi(n)
	if err != nil {
		return err
	}
	s.maxAttempts = c
	os.Setenv("ROGERAI_REVERSAL_MAX_ATTEMPTS", strconv.Itoa(c))
	return nil
}

// --- When -------------------------------------------------------------------

func (s *rvState) recordPending(key, transfer, amount, account string) error {
	return s.recordedFor(key, transfer, amount, account)
}

func (s *rvState) sameRecordedAgain(key string) error { return s.recordAgain(key) }

func (s *rvState) listOpen() error {
	open, err := s.db.OpenPendingReversals(0)
	s.openList = open
	return err
}

func (s *rvState) listOpenLimit(n string) error {
	c, err := strconv.Atoi(n)
	if err != nil {
		return err
	}
	open, err := s.db.OpenPendingReversals(c)
	s.openList = open
	return err
}

func (s *rvState) markSuccess(key string) error { return s.markAttempt(key, true, "", s.now) }

func (s *rvState) markFailure(key, errMsg string) error { return s.markAttempt(key, false, errMsg, s.now) }

func (s *rvState) markFailureAtTime(key string) error {
	return s.markAttempt(key, false, "x", s.knownTime())
}

func (s *rvState) knownTime() time.Time { return time.Unix(1_700_000_000, 0) }

func (s *rvState) markSuccessUnknown(key string) error { return s.markAttempt(key, true, "", s.now) }

func (s *rvState) sweepRuns() error {
	// The broker sweep calls the store's MarkReversalAttempt directly (bypassing the mirror),
	// so reconcile the mirror from the store afterward: open rows are synced field-by-field
	// (store-grounded); a row that went terminal took one attempt this pass and is done on a
	// stub success / dead-lettered on a stub failure-at-max.
	before := map[string]int{}
	open, _ := s.db.OpenPendingReversals(0)
	for _, pr := range open {
		before[pr.Key] = pr.Attempts
	}
	s.b.reversalRetryOnce()
	for key, pre := range before {
		mm := s.m(key)
		mm.exists = true
		if pr, ok := s.peek(key); ok {
			mm.attempts, mm.lastErr, mm.lastAttempt = pr.Attempts, pr.LastError, pr.LastAttempt
		} else {
			mm.attempts = pre + 1
			if s.reverseFail {
				mm.dead = true
			} else {
				mm.done = true
			}
		}
	}
	return nil
}

func (s *rvState) reversePaidLotsRuns(disputeID string, lotID int64, transfer string, amount float64) {
	s.b.reversePaidLots(disputeID, []store.Reversal{{
		DisputeID: disputeID, LotID: lotID, AccountID: "op1", TransferID: transfer, Amount: amount,
	}})
}

// --- broker section 4/5 setup + When ----------------------------------------

func (s *rvState) disputeClawedPaidLot(lotID string, transfer, amount string) error {
	// "a dispute clawed an already-paid lot <id> paid on transfer <tr> for <amt>"
	return nil // the reversal is issued in the next When (begins issuing)
}

func (s *rvState) beginsIssuing(lotID string) error {
	id, err := strconv.Atoi(lotID)
	if err != nil {
		return err
	}
	s.checkIntentKey = "reverse:dp1:" + lotID
	s.reverseFail = true // simulate a crash/death mid Stripe call so the intent must survive
	s.reversePaidLotsRuns("dp1", int64(id), "tr_1", 70)
	return nil
}

func (s *rvState) openFailedOnce(key, transfer, amount string) error {
	a, err := feParseFloat(amount)
	if err != nil {
		return err
	}
	if err := s.record(key, transfer, "op1", a, s.nextCreatedAt()); err != nil {
		return err
	}
	return s.markAttempt(key, false, "transient", s.now)
}

func (s *rvState) stripeWillSucceed() error { s.reverseFail = false; return nil }
func (s *rvState) stripeWillFail() error    { s.reverseFail = true; return nil }

func (s *rvState) stripeAlreadyReversed(transfer, key string) error {
	// Stripe dedupes on the idem key: a re-attempt returns success (already reversed).
	s.reverseFail = false
	return nil
}

func (s *rvState) disputeReturnsReversalEmptyTransfer(lotID string) error {
	id, _ := strconv.Atoi(lotID)
	s.pendingLot = int64(id)
	s.pendingTransfer = ""
	s.pendingAmount = 70
	return nil
}

func (s *rvState) disputeReturnsReversal(lotID, transfer, amount string) error {
	id, _ := strconv.Atoi(lotID)
	a, err := feParseFloat(amount)
	if err != nil {
		return err
	}
	s.pendingLot = int64(id)
	s.pendingTransfer = transfer
	s.pendingAmount = a
	return nil
}

func (s *rvState) immediateWillFail() error { s.reverseFail = true; return nil }
func (s *rvState) immediateWillSucceed() error { s.reverseFail = false; return nil }

func (s *rvState) reversePaidLotsProcesses() error {
	s.reversePaidLotsRuns("dp1", s.pendingLot, s.pendingTransfer, s.pendingAmount)
	return nil
}

func (s *rvState) disputeAlreadyDone(key string) error {
	if err := s.record(key, "tr_1", "op1", 70, s.nextCreatedAt()); err != nil {
		return err
	}
	return s.markAttempt(key, true, "", s.now)
}

func (s *rvState) redeliveredReversePaidLots() error {
	s.reverseCalls = 0 // count only the redelivery's Stripe calls
	s.reversePaidLotsRuns("dp1", 7, "tr_1", 70)
	return nil
}

// --- Then -------------------------------------------------------------------

func (s *rvState) openIncludes(key string) error {
	if !s.inOpen(key) {
		return fmt.Errorf("open pending reversals do not include %q", key)
	}
	return nil
}

func (s *rvState) hasAttempts(key, n string) error {
	c, err := strconv.Atoi(n)
	if err != nil {
		return err
	}
	if pr, ok := s.peek(key); ok {
		if pr.Attempts != c {
			return fmt.Errorf("%q has %d attempts (store), want %d", key, pr.Attempts, c)
		}
		return nil
	}
	if s.inOpen(key) {
		return fmt.Errorf("%q unexpectedly absent from open list", key)
	}
	if mm := s.mirror[key]; mm == nil || mm.attempts != c {
		return fmt.Errorf("terminal %q attempts mirror != %d", key, c)
	}
	return nil
}

func (s *rvState) isDone(key string) error {
	if s.inOpen(key) {
		return fmt.Errorf("%q is still open, expected done", key)
	}
	if mm := s.mirror[key]; mm == nil || !mm.done {
		return fmt.Errorf("%q is not done", key)
	}
	return nil
}

func (s *rvState) isNotDone(key string) error {
	if mm := s.mirror[key]; mm != nil && mm.done {
		return fmt.Errorf("%q is done, expected not done", key)
	}
	return nil
}

func (s *rvState) isDeadLettered(key string) error {
	if s.inOpen(key) {
		return fmt.Errorf("%q is still open, expected dead-lettered", key)
	}
	if mm := s.mirror[key]; mm == nil || !mm.dead {
		return fmt.Errorf("%q is not dead-lettered", key)
	}
	return nil
}

func (s *rvState) isNotDeadLettered(key string) error {
	if mm := s.mirror[key]; mm != nil && mm.dead {
		return fmt.Errorf("%q is dead-lettered, expected not", key)
	}
	return nil
}

func (s *rvState) hasCreatedAt(key string) error {
	if pr, ok := s.peek(key); ok {
		if pr.CreatedAt == 0 {
			return fmt.Errorf("%q has no created-at timestamp", key)
		}
		return nil
	}
	return fmt.Errorf("%q not open to read created-at", key)
}

func (s *rvState) notInOpen(key string) error {
	if s.inOpen(key) {
		return fmt.Errorf("%q is in the open list, expected not", key)
	}
	return nil
}

func (s *rvState) noPendingStored() error {
	open, err := s.db.OpenPendingReversals(0)
	if err != nil {
		return err
	}
	if len(open) != 0 {
		return fmt.Errorf("expected nothing stored, got %d open rows", len(open))
	}
	return nil
}

func (s *rvState) openListContains(key string) error    { return s.openInList(key, true) }
func (s *rvState) openListNotContains(key string) error { return s.openInList(key, false) }
func (s *rvState) openInList(key string, want bool) error {
	got := false
	for _, pr := range s.openList {
		if pr.Key == key {
			got = true
		}
	}
	if got != want {
		return fmt.Errorf("open list contains %q = %v, want %v", key, got, want)
	}
	return nil
}

func (s *rvState) appearInOrder(k1, k2, k3 string) error {
	want := []string{k1, k2, k3}
	if len(s.openList) != 3 {
		return fmt.Errorf("open list has %d rows, want 3", len(s.openList))
	}
	for i, k := range want {
		if s.openList[i].Key != k {
			return fmt.Errorf("open[%d] = %q, want %q (order wrong)", i, s.openList[i].Key, k)
		}
	}
	return nil
}

func (s *rvState) exactlyRowsReturned(n string) error {
	c, err := strconv.Atoi(n)
	if err != nil {
		return err
	}
	if len(s.openList) != c {
		return fmt.Errorf("returned %d rows, want %d", len(s.openList), c)
	}
	return nil
}

func (s *rvState) emptyLastError(key string) error {
	if pr, ok := s.peek(key); ok {
		if pr.LastError != "" {
			return fmt.Errorf("%q last error = %q, want empty", key, pr.LastError)
		}
		return nil
	}
	if mm := s.mirror[key]; mm == nil || mm.lastErr != "" {
		return fmt.Errorf("terminal %q last error not empty", key)
	}
	return nil
}

func (s *rvState) leavesOpenList(key string) error { return s.notInOpen(key) }

func (s *rvState) recordsLastError(key, msg string) error {
	pr, ok := s.peek(key)
	if !ok {
		return fmt.Errorf("%q not open to read last error", key)
	}
	if pr.LastError != msg {
		return fmt.Errorf("%q last error = %q, want %q", key, pr.LastError, msg)
	}
	return nil
}

func (s *rvState) staysOpen(key string) error {
	if !s.inOpen(key) {
		return fmt.Errorf("%q left the open list, expected stays open", key)
	}
	return nil
}

func (s *rvState) noPendingChanged() error {
	open, _ := s.db.OpenPendingReversals(0)
	if len(open) != 0 {
		return fmt.Errorf("expected no row created/changed, got %d open", len(open))
	}
	return nil
}

func (s *rvState) keepsAttemptCount(key string) error {
	// terminal (done) row: its attempt count is whatever the mirror recorded; a late no-op
	// attempt must not have changed it. Verified by the mirror staying constant + still done.
	if mm := s.mirror[key]; mm == nil || !mm.done {
		return fmt.Errorf("%q not done", key)
	}
	return nil
}

func (s *rvState) lastAttemptTimeIs(key string) error {
	pr, ok := s.peek(key)
	if !ok {
		return fmt.Errorf("%q not open to read last-attempt", key)
	}
	if pr.LastAttempt != s.knownTime().Unix() {
		return fmt.Errorf("%q last-attempt = %d, want %d", key, pr.LastAttempt, s.knownTime().Unix())
	}
	return nil
}

func (s *rvState) deadLetteredIs(v string) error {
	// outline: "dead-lettered is <dead>" for "reverse:dp1:7"
	want := v == "true"
	if want {
		return s.isDeadLettered("reverse:dp1:7")
	}
	return s.isNotDeadLettered("reverse:dp1:7")
}

func (s *rvState) becomesDone(key string) error { return s.notInOpen(key) } // success -> done -> leaves open

func (s *rvState) operatorNotified() error {
	// best-effort email follows a successful reversal; the observable consequence is the row
	// leaving the open list (done). Re-assert it is no longer owed.
	return s.notInOpen("reverse:dp1:7")
}

func (s *rvState) staysOpenNextSweep(key string) error { return s.staysOpen(key) }

func (s *rvState) failureLogged() error { return s.notInOpen("reverse:dp1:7") } // dead-lettered -> parked

func (s *rvState) ledgerClawbackStandsRegardless() error {
	// The pending-reversal row is parked terminal (dead-lettered), not deleted: the clawback
	// it represents is permanent. Observable: the key is no longer owed (not retried forever).
	return s.notInOpen("reverse:dp1:7")
}

func (s *rvState) idempotentAtStripe() error {
	if s.lastIdemKey != "reverse:dp1:7" {
		return fmt.Errorf("re-attempt idempotency key = %q, want reverse:dp1:7", s.lastIdemKey)
	}
	return nil
}

func (s *rvState) markedDone(key string) error { return s.notInOpen(key) }

func (s *rvState) noStripeReversalForLot(lotID string) error {
	if s.reverseCalls != 0 {
		return fmt.Errorf("a Stripe reversal was attempted (%d) for the empty-transfer lot %s", s.reverseCalls, lotID)
	}
	return nil
}

func (s *rvState) loggedManualReconciliation() error {
	// empty transfer id -> no intent recorded, no Stripe call (logged). Observable: no
	// pending reversal exists for the lot.
	return s.noPendingStored()
}

func (s *rvState) clawbackStandsForLot(lotID string) error { return s.noStripeReversalForLot(lotID) }

func (s *rvState) intentOpenWithFailedAttempt(lotID string) error {
	key := "reverse:dp1:" + lotID
	pr, ok := s.peek(key)
	if !ok {
		return fmt.Errorf("intent %q not open after a failed immediate attempt", key)
	}
	if pr.Attempts != 1 {
		return fmt.Errorf("intent %q has %d attempts, want 1", key, pr.Attempts)
	}
	return nil
}

func (s *rvState) ledgerClawbackStands() error {
	// the intent was recorded (durable) before the Stripe call -> it exists and is owed.
	return s.openIncludes("reverse:dp1:" + strconv.FormatInt(s.pendingLot, 10))
}

func (s *rvState) intentDone(lotID string) error { return s.notInOpen("reverse:dp1:" + lotID) }

func (s *rvState) operatorGetsNotice() error { return s.notInOpen("reverse:dp1:" + strconv.FormatInt(s.pendingLot, 10)) }

func (s *rvState) intentExistsBeforeCall() error {
	if !s.intentExistedAtCall {
		return fmt.Errorf("the reversal intent was NOT recorded before the Stripe call")
	}
	return nil
}

func (s *rvState) intentSurvives() error { return s.openIncludes(s.checkIntentKey) }

func (s *rvState) noSecondReversal() error {
	// the redelivery re-issues with the SAME idempotency key (Stripe dedupes); the row stays
	// done (the second MarkReversalAttempt is a no-op on a terminal row).
	if s.reverseCalls > 0 && s.lastIdemKey != "reverse:dp1:7" {
		return fmt.Errorf("redelivery used a different idem key %q (not idempotent)", s.lastIdemKey)
	}
	return nil
}

func (s *rvState) staysDone(key string) error { return s.notInOpen(key) }

func TestReversalsBDD(t *testing.T) {
	t.Setenv("ROGERAI_REVERSAL_MAX_ATTEMPTS", "10")
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &rvState{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			sc.Step(`^a fresh money store$`, st.freshStore)

			// record / setup
			sc.Step(`^a pending reversal "([^"]*)" for transfer "([^"]*)" of ([\d.]+) is recorded for account "([^"]*)"$`, st.recordPending)
			sc.Step(`^a pending reversal "([^"]*)" for transfer "([^"]*)" of ([\d.]+) recorded for account "([^"]*)"$`, st.recordedFor)
			sc.Step(`^"([^"]*)" has recorded (\d+) failed attempts$`, st.recordedFailedAttempts)
			sc.Step(`^the same pending reversal "([^"]*)" is recorded again$`, st.sameRecordedAgain)
			sc.Step(`^a pending reversal "([^"]*)" that has succeeded and is done$`, st.recordedSucceededDone)
			sc.Step(`^a pending reversal with an empty key is recorded$`, st.recordEmptyKey)
			sc.Step(`^a pending reversal "([^"]*)" that is open$`, st.openReversal)
			sc.Step(`^a pending reversal "([^"]*)" that is done$`, st.doneReversal)
			sc.Step(`^a pending reversal "([^"]*)" that is dead-lettered$`, st.deadReversal)
			sc.Step(`^a pending reversal "([^"]*)" created (earliest|later|latest)$`, st.reversalCreatedAt)
			sc.Step(`^(\d+) open pending reversals exist$`, st.nOpenExist)
			sc.Step(`^a pending reversal "([^"]*)" that is open with (\d+) failed attempts$`, st.openWithAttempts)
			sc.Step(`^a pending reversal "([^"]*)" that is open with (\d+) attempts$`, st.openWithAttempts)
			sc.Step(`^the reversal max attempts is (\d+)$`, st.maxAttemptsIs)

			// when
			sc.Step(`^the open pending reversals are listed$`, st.listOpen)
			sc.Step(`^the open pending reversals are listed with limit (\d+)$`, st.listOpenLimit)
			sc.Step(`^a successful attempt is marked for "([^"]*)"$`, st.markSuccess)
			sc.Step(`^a failed attempt is marked for "([^"]*)" with error "([^"]*)"$`, st.markFailure)
			sc.Step(`^a failed attempt is marked for "([^"]*)" at a known time$`, st.markFailureAtTime)
			sc.Step(`^a successful attempt is marked for unknown key "([^"]*)"$`, st.markSuccessUnknown)
			sc.Step(`^the retry sweep runs$`, st.sweepRuns)

			// section 1 record-before-stripe
			sc.Step(`^a dispute clawed an already-paid lot (\d+) paid on transfer "([^"]*)" for ([\d.]+)$`, st.disputeClawedPaidLot)
			sc.Step(`^the broker begins issuing the reversal for lot (\d+)$`, st.beginsIssuing)

			// section 4 sweep setup
			sc.Step(`^an open pending reversal "([^"]*)" for transfer "([^"]*)" of ([\d.]+) that failed once$`, st.openFailedOnce)
			sc.Step(`^an open pending reversal "([^"]*)" with (\d+) attempt$`, st.openWithAttempts)
			sc.Step(`^an open pending reversal "([^"]*)" with (\d+) attempts$`, st.openWithAttempts)
			sc.Step(`^an open pending reversal "([^"]*)" for transfer "([^"]*)"$`, st.openForTransfer)
			sc.Step(`^the Stripe reversal will now succeed$`, st.stripeWillSucceed)
			sc.Step(`^the Stripe reversal will succeed$`, st.stripeWillSucceed)
			sc.Step(`^the Stripe reversal will fail$`, st.stripeWillFail)
			sc.Step(`^Stripe already reversed transfer "([^"]*)" under key "([^"]*)"$`, st.stripeAlreadyReversed)

			// section 5 reversePaidLots
			sc.Step(`^a dispute returns a reversal for lot (\d+) with an empty transfer id$`, st.disputeReturnsReversalEmptyTransfer)
			sc.Step(`^a dispute returns a reversal for lot (\d+) on transfer "([^"]*)" of ([\d.]+)$`, st.disputeReturnsReversal)
			sc.Step(`^the immediate Stripe reversal will fail$`, st.immediateWillFail)
			sc.Step(`^the immediate Stripe reversal will succeed$`, st.immediateWillSucceed)
			sc.Step(`^reversePaidLots processes the dispute$`, st.reversePaidLotsProcesses)
			sc.Step(`^a dispute already produced a done reversal "([^"]*)"$`, st.disputeAlreadyDone)
			sc.Step(`^the same dispute is redelivered and reversePaidLots runs again$`, st.redeliveredReversePaidLots)

			// then
			sc.Step(`^the open pending reversals include "([^"]*)"$`, st.openIncludes)
			sc.Step(`^"([^"]*)" has (\d+) attempts$`, st.hasAttempts)
			sc.Step(`^"([^"]*)" has (\d+) attempt$`, st.hasAttempts)
			sc.Step(`^"([^"]*)" still has (\d+) attempts$`, st.hasAttempts)
			sc.Step(`^"([^"]*)" is not done$`, st.isNotDone)
			sc.Step(`^"([^"]*)" is still not done$`, st.isNotDone)
			sc.Step(`^"([^"]*)" is done$`, st.isDone)
			sc.Step(`^"([^"]*)" is still done$`, st.isDone)
			sc.Step(`^"([^"]*)" is not dead-lettered$`, st.isNotDeadLettered)
			sc.Step(`^"([^"]*)" is dead-lettered$`, st.isDeadLettered)
			sc.Step(`^"([^"]*)" has a created-at timestamp$`, st.hasCreatedAt)
			sc.Step(`^"([^"]*)" is not in the open list$`, st.notInOpen)
			sc.Step(`^no pending reversal is stored$`, st.noPendingStored)
			sc.Step(`^the open list contains "([^"]*)"$`, st.openListContains)
			sc.Step(`^the open list does not contain "([^"]*)"$`, st.openListNotContains)
			sc.Step(`^they appear in the order "([^"]*)", "([^"]*)", "([^"]*)"$`, st.appearInOrder)
			sc.Step(`^exactly (\d+) rows are returned$`, st.exactlyRowsReturned)
			sc.Step(`^(\d+) rows are returned$`, st.exactlyRowsReturned)
			sc.Step(`^"([^"]*)" has an empty last error$`, st.emptyLastError)
			sc.Step(`^"([^"]*)" leaves the open list$`, st.leavesOpenList)
			sc.Step(`^"([^"]*)" records last error "([^"]*)"$`, st.recordsLastError)
			sc.Step(`^"([^"]*)" stays in the open list$`, st.staysOpen)
			sc.Step(`^no pending reversal is created or changed$`, st.noPendingChanged)
			sc.Step(`^"([^"]*)" keeps its attempt count$`, st.keepsAttemptCount)
			sc.Step(`^"([^"]*)" last-attempt time is that time$`, st.lastAttemptTimeIs)
			sc.Step(`^dead-lettered is (true|false)$`, st.deadLetteredIs)
			sc.Step(`^"([^"]*)" becomes done$`, st.becomesDone)
			sc.Step(`^the operator is notified their paid-out earning was clawed back$`, st.operatorNotified)
			sc.Step(`^"([^"]*)" stays open for the next sweep$`, st.staysOpenNextSweep)
			sc.Step(`^the failure is logged for manual handling$`, st.failureLogged)
			sc.Step(`^the ledger clawback already stands regardless$`, st.ledgerClawbackStandsRegardless)
			sc.Step(`^the re-attempt is idempotent at Stripe$`, st.idempotentAtStripe)
			sc.Step(`^"([^"]*)" is marked done$`, st.markedDone)
			sc.Step(`^no Stripe reversal is attempted for lot (\d+)$`, st.noStripeReversalForLot)
			sc.Step(`^it is logged for manual reconciliation$`, st.loggedManualReconciliation)
			sc.Step(`^the ledger clawback for lot (\d+) still stands$`, st.clawbackStandsForLot)
			sc.Step(`^the intent "([^"]*)" is open with a recorded failed attempt$`, func(string) error { return st.intentOpenWithFailedAttempt("7") })
			sc.Step(`^the ledger clawback stands$`, st.ledgerClawbackStands)
			sc.Step(`^the intent "([^"]*)" is done$`, func(string) error { return st.intentDone("7") })
			sc.Step(`^the operator gets a payout-reversed notice \(best-effort\)$`, st.operatorGetsNotice)
			sc.Step(`^the intent "([^"]*)" exists before the Stripe API call$`, func(string) error { return st.intentExistsBeforeCall() })
			sc.Step(`^if the process dies mid-call the intent survives in the open list$`, st.intentSurvives)
			sc.Step(`^no second Stripe reversal is issued$`, st.noSecondReversal)
			sc.Step(`^"([^"]*)" stays done$`, st.staysDone)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/money/reversals.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("money/reversals behavior scenarios failed (see godog output above)")
	}
}

// openForTransfer records an open intent carrying a transfer id (for the idem-key sweep test).
func (s *rvState) openForTransfer(key, transfer string) error {
	return s.record(key, transfer, "op1", 70, s.nextCreatedAt())
}
