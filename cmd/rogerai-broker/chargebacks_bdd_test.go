package main

// chargebacks_bdd_test.go makes features/money/chargebacks.feature an EXECUTABLE Cucumber
// suite. The step defs drive the REAL dispute clawback: store.ChargebackLineage (lineage-
// attributed claw, newest-first recency, fee-aware consumer-cost cap, pro-rata on the
// overshoot lot, paid-lot Stripe reversal, platform-loss remainder, dispute-id idempotency)
// AND the broker dispute webhook (charge.dispute.created) for the wallet-resolution +
// negative-amount guards. Every assertion is read back from the STORE (ChargebackResult,
// EarningSplitOf, the append-only ledger rows: chargeback / adjustment / payout_reversed /
// platform_loss / earn) so a regression in any of the P0-3/P0-4 invariants (no over-claw,
// no collateral claw, cross-consumer isolation, conservation, idempotency) fails red.
//
// Lots are built via the real Settle path (which also writes the entries the wallet-recency
// claw walks). Setup is materialized lazily at the first dispute so a "paid" lot can be paid
// out BEFORE a sibling "held" lot is settled (RequestPayout pays ALL currently-payable lots,
// so the only way to leave one held is to settle it after the payout). Recency order is
// driven by the receipt TS (earlier < normal < later bands). feApprox/feParseFloat live in
// fee_splits_bdd_test.go (same package).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// cbReq is one settled request staged for materialization.
type cbReq struct {
	wallet, requestID, node string
	cost, ownerShare        float64
	seedFunded              bool
	state                   string // "held" (default) | "payable" | "paid"
	transferID              string // paid lots: the Stripe transfer ("" = legacy no-transfer)
	ts                      int64  // receipt TS — drives newest-first recency
}

type cbDisputeArgs struct {
	wallet, requestID string
	amount            float64
}

type cbState struct {
	db      *store.Mem
	feeRate float64

	credits     map[string]float64 // wallet -> real credits
	seedCredits map[string]float64 // wallet -> FREE seed credits
	nodeOwner   map[string]string  // node -> owner account
	reqs        []*cbReq
	reqByID     map[string]*cbReq
	grossExpect map[string]float64 // requestID -> asserted lot gross (verified post-materialize)

	earlierN, normalN, laterN int

	materialized     bool
	balBeforeDispute map[string]float64

	// dispute bookkeeping
	results      map[string]store.ChargebackResult
	disputeArgs  map[string]cbDisputeArgs
	lastDispute  string
	lastResult   store.ChargebackResult
	totalClawed  float64
	balBeforeRed float64

	// broker webhook
	bk          *broker
	webhookCode int
}

func (s *cbState) reset() {
	s.db = store.NewMem()
	s.feeRate = 0.30
	s.credits = map[string]float64{}
	s.seedCredits = map[string]float64{}
	s.nodeOwner = map[string]string{}
	s.reqs = nil
	s.reqByID = map[string]*cbReq{}
	s.grossExpect = map[string]float64{}
	s.earlierN, s.normalN, s.laterN = 0, 0, 0
	s.materialized = false
	s.balBeforeDispute = map[string]float64{}
	s.results = map[string]store.ChargebackResult{}
	s.disputeArgs = map[string]cbDisputeArgs{}
	s.lastDispute = ""
	s.lastResult = store.ChargebackResult{}
	s.totalClawed = 0
	s.balBeforeRed = 0
	s.bk = nil
	s.webhookCode = 0
}

// --- Background / setup -----------------------------------------------------

func (s *cbState) freshStore() error { s.reset(); return nil }

func (s *cbState) feePct(p string) error {
	n, err := strconv.Atoi(p)
	if err != nil {
		return err
	}
	s.feeRate = float64(n) / 100
	return nil
}

func (s *cbState) walletReal(name, v string) error {
	f, err := feParseFloat(v)
	if err != nil {
		return err
	}
	s.credits[name] += f
	return nil
}

func (s *cbState) walletSeed(name, v string) error {
	f, err := feParseFloat(v)
	if err != nil {
		return err
	}
	s.seedCredits[name] += f
	return nil
}

func (s *cbState) nodeOwned(node, owner string) error {
	s.nodeOwner[node] = owner
	return nil
}

func (s *cbState) nextTS(kind string) int64 {
	switch kind {
	case "earlier":
		s.earlierN++
		return 1000 + int64(s.earlierN)
	case "later":
		s.laterN++
		return 9000 + int64(s.laterN)
	default:
		s.normalN++
		return 5000 + int64(s.normalN)
	}
}

// addReq stages one settled request. ownerShare<0 means "derive from the fee rate".
func (s *cbState) addReq(wallet, reqID, node string, cost, ownerShare float64, kind string, seed bool) {
	if ownerShare < 0 {
		ownerShare = cost * (1 - s.feeRate)
	}
	r := &cbReq{
		wallet: wallet, requestID: reqID, node: node, cost: cost, ownerShare: ownerShare,
		seedFunded: seed, state: "held", ts: s.nextTS(kind),
	}
	s.reqs = append(s.reqs, r)
	s.reqByID[reqID] = r
}

func (s *cbState) settledReqNoShare(wallet, reqID, cost, node string) error {
	c, err := feParseFloat(cost)
	if err != nil {
		return err
	}
	s.addReq(wallet, reqID, node, c, -1, "", false)
	return nil
}

func (s *cbState) settledReqShare(wallet, reqID, cost, node, share string) error {
	c, err := feParseFloat(cost)
	if err != nil {
		return err
	}
	g, err := feParseFloat(share)
	if err != nil {
		return err
	}
	s.addReq(wallet, reqID, node, c, g, "", false)
	return nil
}

func (s *cbState) laterReqNoShare(wallet, reqID, cost, node string) error {
	c, err := feParseFloat(cost)
	if err != nil {
		return err
	}
	s.addReq(wallet, reqID, node, c, -1, "later", false)
	return nil
}

func (s *cbState) laterReqShare(wallet, reqID, cost, node, share string) error {
	c, err := feParseFloat(cost)
	if err != nil {
		return err
	}
	g, err := feParseFloat(share)
	if err != nil {
		return err
	}
	s.addReq(wallet, reqID, node, c, g, "later", false)
	return nil
}

func (s *cbState) earlierReqShare(wallet, reqID, cost, node, share string) error {
	c, err := feParseFloat(cost)
	if err != nil {
		return err
	}
	g, err := feParseFloat(share)
	if err != nil {
		return err
	}
	s.addReq(wallet, reqID, node, c, g, "earlier", false)
	return nil
}

func (s *cbState) singleReqShare(wallet, reqID, cost, node, share string) error {
	return s.settledReqShare(wallet, reqID, cost, node, share)
}

func (s *cbState) unrelatedReqShare(wallet, reqID, cost, node, share string) error {
	return s.settledReqShare(wallet, reqID, cost, node, share)
}

func (s *cbState) seedFundedReq(wallet, reqID, cost, node string) error {
	c, err := feParseFloat(cost)
	if err != nil {
		return err
	}
	s.addReq(wallet, reqID, node, c, -1, "", true)
	return nil
}

func (s *cbState) nSettledReqs(wallet, n, cost, node, share string) error {
	count, err := strconv.Atoi(n)
	if err != nil {
		return err
	}
	c, err := feParseFloat(cost)
	if err != nil {
		return err
	}
	g, err := feParseFloat(share)
	if err != nil {
		return err
	}
	for i := 0; i < count; i++ {
		s.addReq(wallet, fmt.Sprintf("%s_n%d", wallet, len(s.reqs)), node, c, g, "", false)
	}
	return nil
}

func (s *cbState) lotsTotaling(wallet, total, n, node string) error {
	op, err := feParseFloat(total)
	if err != nil {
		return err
	}
	count, err := strconv.Atoi(n)
	if err != nil {
		return err
	}
	if count == 0 {
		return nil // wallet has credits but no lots
	}
	perGross := op / float64(count)
	perCost := perGross / (1 - s.feeRate)
	for i := 0; i < count; i++ {
		s.addReq(wallet, fmt.Sprintf("%s_cons%d", wallet, len(s.reqs)), node, perCost, perGross, "", false)
	}
	return nil
}

func (s *cbState) orderedReqs(wallet, list, cost, share, node string) error {
	c, err := feParseFloat(cost)
	if err != nil {
		return err
	}
	g, err := feParseFloat(share)
	if err != nil {
		return err
	}
	for _, q := range strings.Split(list, ",") {
		id := strings.Trim(strings.TrimSpace(q), `"`)
		if id == "" {
			continue
		}
		s.addReq(wallet, id, node, c, g, "", false)
	}
	return nil
}

func (s *cbState) noSettledReqs(wallet string) error { return nil }

func (s *cbState) lotHasGross(reqID, v string) error {
	g, err := feParseFloat(v)
	if err != nil {
		return err
	}
	s.grossExpect[reqID] = g // verified against the store's KindEarn row post-materialize
	return nil
}

func (s *cbState) lotHeld(reqID string) error {
	if r := s.reqByID[reqID]; r != nil {
		r.state = "held"
	}
	return nil
}

func (s *cbState) lotPayable(reqID string) error {
	if r := s.reqByID[reqID]; r != nil {
		r.state = "payable"
	}
	return nil
}

func (s *cbState) lotPaidTransfer(reqID, transfer string) error {
	r := s.reqByID[reqID]
	if r == nil {
		return fmt.Errorf("unknown request %q for paid-lot setup", reqID)
	}
	r.state = "paid"
	r.transferID = transfer
	return nil
}

func (s *cbState) lotPaidNoTransfer(reqID string) error {
	r := s.reqByID[reqID]
	if r == nil {
		return fmt.Errorf("unknown request %q for paid-lot setup", reqID)
	}
	r.state = "paid"
	r.transferID = ""
	return nil
}

func (s *cbState) opPaidLot(op, reqID, transfer string) error {
	return s.lotPaidTransfer(reqID, transfer)
}

// --- materialization --------------------------------------------------------

func (s *cbState) settleReq(r *cbReq) error {
	rec := protocol.UsageReceipt{RequestID: r.requestID, TS: r.ts}
	_, err := s.db.Settle(r.wallet, r.node, r.cost, r.ownerShare, rec)
	return err
}

func (s *cbState) walletSet() map[string]bool {
	set := map[string]bool{}
	for w := range s.credits {
		set[w] = true
	}
	for w := range s.seedCredits {
		set[w] = true
	}
	for _, r := range s.reqs {
		set[r.wallet] = true
	}
	return set
}

func (s *cbState) materialize() error {
	if s.materialized {
		return nil
	}
	s.materialized = true

	for w, amt := range s.credits {
		if _, _, err := s.db.CreditOnce("real:"+w, w, amt); err != nil {
			return err
		}
	}
	for w, amt := range s.seedCredits {
		if _, _, err := s.db.SeedOnce(w, amt); err != nil {
			return err
		}
	}
	for node, owner := range s.nodeOwner {
		if err := s.db.BindNode(node, owner); err != nil {
			return err
		}
	}

	ordered := append([]*cbReq(nil), s.reqs...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].ts < ordered[j].ts })

	future := time.Now().Add(200 * 24 * time.Hour)

	// Paid lots first: settle + pay out individually so each carries its own transfer id and
	// later "held"/"payable" lots (settled below) are never swept into the payout.
	for _, r := range ordered {
		if r.state != "paid" {
			continue
		}
		if err := s.settleReq(r); err != nil {
			return err
		}
		owner := s.nodeOwner[r.node]
		pay, ok, _, err := s.db.RequestPayout(owner, future, 0)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("paid-lot %q: payout not created (lot not payable)", r.requestID)
		}
		if err := s.db.SettlePayout(pay.ID, r.transferID); err != nil {
			return err
		}
	}

	// Held / payable lots.
	for _, r := range ordered {
		if r.state == "paid" {
			continue
		}
		if err := s.settleReq(r); err != nil {
			return err
		}
	}

	// Promote the lots that must be PAYABLE (no scenario mixes payable + held on one owner).
	promoted := map[string]bool{}
	for _, r := range ordered {
		if r.state != "payable" {
			continue
		}
		owner := s.nodeOwner[r.node]
		if promoted[owner] {
			continue
		}
		promoted[owner] = true
		if _, err := s.db.EarningSplitOf(owner, future); err != nil { // promoteLocked: held -> payable
			return err
		}
	}

	// Verify staged lot-gross expectations against the store's KindEarn rows.
	for reqID, want := range s.grossExpect {
		r := s.reqByID[reqID]
		if r == nil {
			return fmt.Errorf("gross expectation for unknown request %q", reqID)
		}
		sum, err := s.ledgerSum(s.nodeOwner[r.node], []string{store.KindEarn}, reqID)
		if err != nil {
			return err
		}
		if e := feApprox(sum, want); e != nil {
			return fmt.Errorf("lot %q gross: %w", reqID, e)
		}
	}

	for w := range s.walletSet() {
		bal, err := s.db.BalanceOf(w, 0)
		if err != nil {
			return err
		}
		s.balBeforeDispute[w] = bal
	}
	return nil
}

// --- store helpers ----------------------------------------------------------

func (s *cbState) ledgerSum(holder string, kinds []string, ref string) (float64, error) {
	rows, err := s.db.LedgerOf(holder, kinds, 1_000_000)
	if err != nil {
		return 0, err
	}
	var sum float64
	for _, r := range rows {
		if ref != "" && r.Ref != ref {
			continue
		}
		sum += r.Amount
	}
	return sum, nil
}

func (s *cbState) operators() []string {
	seen := map[string]bool{}
	var out []string
	for _, op := range s.nodeOwner {
		if !seen[op] {
			seen[op] = true
			out = append(out, op)
		}
	}
	return out
}

// clawRowsForReq returns the claw/reverse ledger rows that name requestID (idem key
// "claw:<dispute>:<req>" / "reverse:<dispute>:<req>"), across every known operator.
func (s *cbState) clawRowsForReq(reqID string) ([]store.LedgerRow, error) {
	var out []store.LedgerRow
	for _, op := range s.operators() {
		rows, err := s.db.LedgerOf(op, []string{store.KindAdjustment, store.KindPayoutReversed}, 1_000_000)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			if strings.HasSuffix(r.IdemKey, ":"+reqID) {
				out = append(out, r)
			}
		}
	}
	return out, nil
}

// clawedReqsForDispute returns the set of requestIDs whose lot a given dispute clawed/reversed.
func (s *cbState) clawedReqsForDispute(dispute string) (map[string]bool, error) {
	set := map[string]bool{}
	for _, op := range s.operators() {
		rows, err := s.db.LedgerOf(op, []string{store.KindAdjustment, store.KindPayoutReversed}, 1_000_000)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			if r.Ref != dispute {
				continue
			}
			if i := strings.LastIndex(r.IdemKey, ":"); i >= 0 {
				set[r.IdemKey[i+1:]] = true
			}
		}
	}
	return set, nil
}

func (s *cbState) clawedInPlaceFromOp(op, dispute string) (float64, error) {
	sum, err := s.ledgerSum(op, []string{store.KindAdjustment}, dispute)
	return -sum, err
}

func (s *cbState) opRemaining(op string) (float64, error) {
	sp, err := s.db.EarningSplitOf(op, time.Now())
	if err != nil {
		return 0, err
	}
	return sp.Held + sp.Payable + sp.Reserved, nil
}

func (s *cbState) eligibleLots(wallet string) int {
	n := 0
	for _, r := range s.reqs {
		if r.wallet == wallet && !r.seedFunded && r.ownerShare > 0 {
			n++
		}
	}
	return n
}

// --- dispute When steps -----------------------------------------------------

func (s *cbState) openDispute(dispute, wallet, requestID string, amount float64) error {
	if err := s.materialize(); err != nil {
		return err
	}
	res, err := s.db.ChargebackLineage(dispute, wallet, requestID, amount, time.Now())
	if err != nil {
		return err
	}
	s.results[dispute] = res
	s.disputeArgs[dispute] = cbDisputeArgs{wallet: wallet, requestID: requestID, amount: amount}
	s.lastDispute = dispute
	s.lastResult = res
	if !res.AlreadyHandled {
		s.totalClawed += res.Clawed
	}
	return nil
}

func (s *cbState) recencyDispute(dispute, amount, wallet string) error {
	a, err := feParseFloat(amount)
	if err != nil {
		return err
	}
	return s.openDispute(dispute, wallet, "", a)
}

func (s *cbState) explicitMostRecent(dispute, amount, wallet, reqID string) error {
	a, err := feParseFloat(amount)
	if err != nil {
		return err
	}
	return s.openDispute(dispute, wallet, reqID, a)
}

func (s *cbState) explicitRequest(dispute, amount, wallet, reqID string) error {
	a, err := feParseFloat(amount)
	if err != nil {
		return err
	}
	return s.openDispute(dispute, wallet, reqID, a)
}

func (s *cbState) deliveredAgain(dispute string) error {
	args, ok := s.disputeArgs[dispute]
	if !ok {
		return fmt.Errorf("redelivery of unknown dispute %q", dispute)
	}
	s.balBeforeRed, _ = s.db.BalanceOf(args.wallet, 0)
	res, err := s.db.ChargebackLineage(dispute, args.wallet, args.requestID, args.amount, time.Now())
	if err != nil {
		return err
	}
	s.results[dispute] = res
	s.lastResult = res
	if !res.AlreadyHandled {
		s.totalClawed += res.Clawed
	}
	return nil
}

// --- webhook When steps -----------------------------------------------------

func (s *cbState) ensureBroker() {
	if s.bk == nil {
		s.bk = &broker{db: s.db, bill: loadBilling()}
	}
}

func (s *cbState) postDispute(id, paymentIntent, charge, metaUser string, amountCents int) error {
	s.ensureBroker()
	obj := map[string]any{"id": id, "amount": amountCents}
	if paymentIntent != "" {
		obj["payment_intent"] = paymentIntent
	}
	if charge != "" {
		obj["charge"] = charge
	}
	if metaUser != "" {
		obj["metadata"] = map[string]any{"user": metaUser}
	}
	payload, _ := json.Marshal(map[string]any{
		"type": "charge.dispute.created",
		"data": map[string]any{"object": obj},
	})
	r := httptest.NewRequest(http.MethodPost, "/billing/webhook", bytes.NewReader(payload))
	r.Header.Set("Stripe-Signature", stripeSig(payload, "whsec_test", time.Now().Unix()))
	w := httptest.NewRecorder()
	s.bk.webhook(w, r)
	s.webhookCode = w.Code
	s.lastDispute = id
	return nil
}

func (s *cbState) disputeArrivesUnresolvable(dispute, amount string) error {
	if err := s.materialize(); err != nil {
		return err
	}
	a, err := feParseFloat(amount)
	if err != nil {
		return err
	}
	s.disputeArgs[dispute] = cbDisputeArgs{amount: a}
	return nil
}

func (s *cbState) webhookProcesses(dispute string) error {
	args := s.disputeArgs[dispute]
	return s.postDispute(dispute, "", "", "", int(args.amount*100))
}

func (s *cbState) webhookReceivesNeg(dispute, amount, wallet string) error {
	if err := s.materialize(); err != nil {
		return err
	}
	a, err := feParseFloat(amount)
	if err != nil {
		return err
	}
	return s.postDispute(dispute, "", "", wallet, int(a*100))
}

// --- Then steps -------------------------------------------------------------

func (s *cbState) clawedFromOp(v, op string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	if e := feApprox(s.lastResult.Clawed, want); e != nil {
		return fmt.Errorf("ChargebackResult.Clawed: %w", e)
	}
	got, err := s.clawedInPlaceFromOp(op, s.lastDispute)
	if err != nil {
		return err
	}
	if e := feApprox(got, want); e != nil {
		return fmt.Errorf("operator %s adjustment-ledger claw total: %w", op, e)
	}
	return nil
}

func (s *cbState) platformLossIs(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	if e := feApprox(s.lastResult.PlatformLoss, want); e != nil {
		return fmt.Errorf("ChargebackResult.PlatformLoss: %w", e)
	}
	sum, err := s.ledgerSum("platform", []string{store.KindPlatformLoss}, s.lastDispute)
	if err != nil {
		return err
	}
	if want == 0 {
		if sum != 0 {
			return fmt.Errorf("expected no platform_loss row, got %g", -sum)
		}
		return nil
	}
	return feApprox(-sum, want)
}

func (s *cbState) lotIntact(reqID string) error {
	rows, err := s.clawRowsForReq(reqID)
	if err != nil {
		return err
	}
	if len(rows) != 0 {
		return fmt.Errorf("lot %q was clawed (%d claw/reverse ledger rows) but should be intact", reqID, len(rows))
	}
	return nil
}

func (s *cbState) opRetains(op, v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	got, err := s.opRemaining(op)
	if err != nil {
		return err
	}
	return feApprox(got, want)
}

func (s *cbState) opRetainsAcross(op, v, n string) error {
	if err := s.opRetains(op, v); err != nil {
		return err
	}
	want, err := strconv.Atoi(n)
	if err != nil {
		return err
	}
	clawed, err := s.clawedReqsForDispute(s.lastDispute)
	if err != nil {
		return err
	}
	eligible := s.eligibleLots(s.disputeArgs[s.lastDispute].wallet)
	if got := eligible - len(clawed); got != want {
		return fmt.Errorf("untouched lots = %d, want %d (eligible %d, clawed %d)", got, want, eligible, len(clawed))
	}
	return nil
}

func (s *cbState) conservation(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	var rev float64
	for _, r := range s.lastResult.Reversals {
		rev += r.Amount
	}
	return feApprox(s.lastResult.Clawed+rev+s.lastResult.PlatformLoss, want)
}

func (s *cbState) balanceReducedBy(wallet, v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	bal, err := s.db.BalanceOf(wallet, 0)
	if err != nil {
		return err
	}
	return feApprox(s.balBeforeDispute[wallet]-bal, want)
}

func (s *cbState) balanceUnchanged(wallet string) error {
	bal, err := s.db.BalanceOf(wallet, 0)
	if err != nil {
		return err
	}
	return feApprox(bal, s.balBeforeDispute[wallet])
}

func (s *cbState) exactlyOneLotClawed() error {
	clawed, err := s.clawedReqsForDispute(s.lastDispute)
	if err != nil {
		return err
	}
	if len(clawed) != 1 {
		return fmt.Errorf("expected exactly one lot clawed by %q, got %d", s.lastDispute, len(clawed))
	}
	return nil
}

func (s *cbState) lotClawed(reqID string) error {
	rows, err := s.clawRowsForReq(reqID)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return fmt.Errorf("lot %q has no claw/reverse ledger row but should be clawed", reqID)
	}
	return nil
}

func (s *cbState) opNotClawed(op string) error {
	sum, err := s.ledgerSum(op, []string{store.KindAdjustment, store.KindPayoutReversed}, "")
	if err != nil {
		return err
	}
	if sum != 0 {
		return fmt.Errorf("operator %s was clawed %g but should be untouched", op, -sum)
	}
	for _, rv := range s.lastResult.Reversals {
		if rv.AccountID == op {
			return fmt.Errorf("operator %s has a reversal but should be untouched", op)
		}
	}
	return nil
}

func (s *cbState) nLotsIntact(n string) error {
	want, err := strconv.Atoi(n)
	if err != nil {
		return err
	}
	clawed, err := s.clawedReqsForDispute(s.lastDispute)
	if err != nil {
		return err
	}
	eligible := s.eligibleLots(s.disputeArgs[s.lastDispute].wallet)
	if got := eligible - len(clawed); got != want {
		return fmt.Errorf("untouched lots = %d, want %d (eligible %d, clawed %d)", got, want, eligible, len(clawed))
	}
	return nil
}

func (s *cbState) clawedInPlace(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return feApprox(s.lastResult.Clawed, want)
}

func (s *cbState) noReversal() error {
	if len(s.lastResult.Reversals) != 0 {
		return fmt.Errorf("expected no Stripe transfer reversal, got %d", len(s.lastResult.Reversals))
	}
	return nil
}

func (s *cbState) adjustmentRow(v string) error {
	want, err := feParseFloat(v) // signed, e.g. -70.00
	if err != nil {
		return err
	}
	for _, op := range s.operators() {
		rows, err := s.db.LedgerOf(op, []string{store.KindAdjustment}, 1_000_000)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if r.Ref == s.lastDispute && feApprox(r.Amount, want) == nil {
				return nil
			}
		}
	}
	return fmt.Errorf("no operator adjustment ledger row of %g for dispute %q", want, s.lastDispute)
}

func (s *cbState) oneReversal(transfer, v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	if len(s.lastResult.Reversals) != 1 {
		return fmt.Errorf("expected exactly 1 reversal, got %d", len(s.lastResult.Reversals))
	}
	rv := s.lastResult.Reversals[0]
	if rv.TransferID != transfer {
		return fmt.Errorf("reversal transfer = %q, want %q", rv.TransferID, transfer)
	}
	return feApprox(rv.Amount, want)
}

func (s *cbState) payoutReversedRow(v, op string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	sum, err := s.ledgerSum(op, []string{store.KindPayoutReversed}, s.lastDispute)
	if err != nil {
		return err
	}
	return feApprox(sum, want)
}

func (s *cbState) reversalCarries(dispute, op string) error {
	if len(s.lastResult.Reversals) == 0 {
		return fmt.Errorf("no reversal to inspect")
	}
	rv := s.lastResult.Reversals[0]
	if rv.DisputeID != dispute {
		return fmt.Errorf("reversal dispute = %q, want %q", rv.DisputeID, dispute)
	}
	if rv.LotID == 0 {
		return fmt.Errorf("reversal carries no lot id")
	}
	if rv.AccountID != op {
		return fmt.Errorf("reversal account = %q, want %q", rv.AccountID, op)
	}
	return nil
}

func (s *cbState) lotClawedInPlaceFor(reqID, v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	r := s.reqByID[reqID]
	if r == nil {
		return fmt.Errorf("unknown request %q", reqID)
	}
	rows, err := s.db.LedgerOf(s.nodeOwner[r.node], []string{store.KindAdjustment}, 1_000_000)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if strings.HasSuffix(row.IdemKey, ":"+reqID) {
			return feApprox(-row.Amount, want)
		}
	}
	return fmt.Errorf("lot %q has no in-place claw (adjustment) row", reqID)
}

func (s *cbState) clawedPlusReversed(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	var rev float64
	for _, r := range s.lastResult.Reversals {
		rev += r.Amount
	}
	return feApprox(s.lastResult.Clawed+rev, want)
}

func (s *cbState) reversalEmptyTransfer() error {
	if len(s.lastResult.Reversals) == 0 {
		return fmt.Errorf("no reversal returned")
	}
	if t := s.lastResult.Reversals[0].TransferID; t != "" {
		return fmt.Errorf("reversal transfer id = %q, want empty", t)
	}
	return nil
}

func (s *cbState) brokerSkipsReversal() error {
	// The broker's reversePaidLots skips the Stripe API call (logging for manual
	// reconciliation) exactly when the reversal has no transfer id; assert that condition.
	return s.reversalEmptyTransfer()
}

func (s *cbState) ledgerClawbackStands() error {
	if len(s.lastResult.Reversals) == 0 {
		return fmt.Errorf("no reversal returned")
	}
	op := s.lastResult.Reversals[0].AccountID
	sum, err := s.ledgerSum(op, []string{store.KindPayoutReversed}, s.lastDispute)
	if err != nil {
		return err
	}
	if sum >= 0 {
		return fmt.Errorf("no payout_reversed ledger clawback recorded for %s", op)
	}
	return nil
}

func (s *cbState) nothingClawed() error {
	if s.lastResult.Clawed != 0 || len(s.lastResult.Reversals) != 0 {
		return fmt.Errorf("expected nothing clawed, got clawed=%g reversals=%d", s.lastResult.Clawed, len(s.lastResult.Reversals))
	}
	clawed, err := s.clawedReqsForDispute(s.lastDispute)
	if err != nil {
		return err
	}
	if len(clawed) != 0 {
		return fmt.Errorf("expected no clawed lots, got %d", len(clawed))
	}
	return nil
}

func (s *cbState) platformLossRow(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	sum, err := s.ledgerSum("platform", []string{store.KindPlatformLoss}, s.lastDispute)
	if err != nil {
		return err
	}
	return feApprox(sum, want)
}

func (s *cbState) redeliveryAlreadyHandled() error {
	if !s.lastResult.AlreadyHandled {
		return fmt.Errorf("redelivery not marked already-handled")
	}
	return nil
}

func (s *cbState) redeliveryClaws(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return feApprox(s.lastResult.Clawed, want)
}

func (s *cbState) redeliveryNoLoss() error {
	if s.lastResult.PlatformLoss != 0 {
		return fmt.Errorf("redelivery booked platform loss %g", s.lastResult.PlatformLoss)
	}
	return nil
}

func (s *cbState) balanceUnchangedByRedelivery(string) error {
	bal, err := s.db.BalanceOf(s.disputeArgs[s.lastDispute].wallet, 0)
	if err != nil {
		return err
	}
	return feApprox(bal, s.balBeforeRed)
}

func (s *cbState) lotClawedOnce(reqID string) error {
	rows, err := s.clawRowsForReq(reqID)
	if err != nil {
		return err
	}
	if len(rows) != 1 {
		return fmt.Errorf("lot %q clawed %d times, want exactly once", reqID, len(rows))
	}
	return nil
}

func (s *cbState) noSecondChargeback(dispute string) error {
	args := s.disputeArgs[dispute]
	rows, err := s.db.LedgerOf(args.wallet, []string{store.KindChargeback}, 1_000_000)
	if err != nil {
		return err
	}
	n := 0
	for _, r := range rows {
		if r.Ref == dispute {
			n++
		}
	}
	if n != 1 {
		return fmt.Errorf("dispute %q wrote %d chargeback rows, want exactly 1", dispute, n)
	}
	return nil
}

func (s *cbState) totalClawedIs(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return feApprox(s.totalClawed, want)
}

func (s *cbState) lotClawedBy(reqID, dispute string) error {
	rows, err := s.clawRowsForReq(reqID)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if r.Ref == dispute {
			return nil
		}
	}
	return fmt.Errorf("lot %q not clawed by dispute %q", reqID, dispute)
}

func (s *cbState) noLotClawedTwice() error {
	count := map[string]int{}
	for _, op := range s.operators() {
		rows, err := s.db.LedgerOf(op, []string{store.KindAdjustment, store.KindPayoutReversed}, 1_000_000)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if i := strings.LastIndex(r.IdemKey, ":"); i >= 0 {
				count[r.IdemKey[i+1:]]++
			}
		}
	}
	for req, n := range count {
		if n > 1 {
			return fmt.Errorf("lot %q clawed %d times", req, n)
		}
	}
	return nil
}

func (s *cbState) disputeClawsFromOp(dispute, v, op string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	res := s.results[dispute]
	if e := feApprox(res.Clawed, want); e != nil {
		return fmt.Errorf("dispute %q Clawed: %w", dispute, e)
	}
	got, err := s.clawedInPlaceFromOp(op, dispute)
	if err != nil {
		return err
	}
	return feApprox(got, want)
}

func (s *cbState) disputeClawsAny(dispute, v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	res := s.results[dispute]
	if e := feApprox(res.Clawed, want); e != nil {
		return fmt.Errorf("dispute %q Clawed: %w", dispute, e)
	}
	if want == 0 && len(res.Reversals) != 0 {
		return fmt.Errorf("dispute %q has %d reversals, want 0", dispute, len(res.Reversals))
	}
	return nil
}

func (s *cbState) disputeBooksLoss(dispute, v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	res := s.results[dispute]
	if e := feApprox(res.PlatformLoss, want); e != nil {
		return fmt.Errorf("dispute %q PlatformLoss: %w", dispute, e)
	}
	sum, err := s.ledgerSum("platform", []string{store.KindPlatformLoss}, dispute)
	if err != nil {
		return err
	}
	return feApprox(-sum, want)
}

func (s *cbState) noOtherOpClawed(op string) error {
	for _, o := range s.operators() {
		if o == op {
			continue
		}
		sum, err := s.ledgerSum(o, []string{store.KindAdjustment, store.KindPayoutReversed}, "")
		if err != nil {
			return err
		}
		if sum != 0 {
			return fmt.Errorf("operator %s clawed %g but only %s should be", o, -sum, op)
		}
	}
	return nil
}

func (s *cbState) recoveredNotExceed(op, v string) error {
	max, err := feParseFloat(v)
	if err != nil {
		return err
	}
	var rev float64
	for _, r := range s.lastResult.Reversals {
		rev += r.Amount
	}
	recovered := s.lastResult.Clawed + rev
	if recovered > max+1e-9 {
		return fmt.Errorf("recovered %g from %s exceeds the disputed cap %g (over-claw)", recovered, op, max)
	}
	return nil
}

func (s *cbState) noClawbackPerformed() error {
	for _, op := range s.operators() {
		sum, err := s.ledgerSum(op, []string{store.KindAdjustment, store.KindPayoutReversed}, "")
		if err != nil {
			return err
		}
		if sum != 0 {
			return fmt.Errorf("operator %s was clawed despite an unresolvable dispute", op)
		}
	}
	return nil
}

func (s *cbState) noPlatformLossBooked() error {
	rows, err := s.db.LedgerOf("platform", []string{store.KindPlatformLoss}, 1_000_000)
	if err != nil {
		return err
	}
	if len(rows) != 0 {
		return fmt.Errorf("expected no platform_loss rows, got %d", len(rows))
	}
	return nil
}

func (s *cbState) webhookAcks() error {
	if s.webhookCode != http.StatusOK {
		return fmt.Errorf("webhook returned %d, want 200", s.webhookCode)
	}
	return nil
}

func (s *cbState) noClawbackFor(dispute string) error {
	if s.webhookCode != http.StatusOK {
		return fmt.Errorf("webhook returned %d, want 200", s.webhookCode)
	}
	// No chargeback ledger row for this dispute, no operator clawed, and alice's balance and
	// staged lot intact (the broker's amount>0 guard must keep the store untouched).
	rows, err := s.db.LedgerOf("alice", []string{store.KindChargeback}, 1_000_000)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if r.Ref == dispute {
			return fmt.Errorf("a chargeback row was written for the negative dispute %q", dispute)
		}
	}
	if bal, _ := s.db.BalanceOf("alice", 0); feApprox(bal, s.balBeforeDispute["alice"]) != nil {
		return fmt.Errorf("negative dispute changed alice balance %g -> %g", s.balBeforeDispute["alice"], bal)
	}
	return s.noClawbackPerformed()
}

func TestChargebacksBDD(t *testing.T) {
	t.Setenv("STRIPE_SECRET_KEY", "sk_test_dummy")
	t.Setenv("STRIPE_WEBHOOK_SECRET", "whsec_test")
	t.Setenv("ROGERAI_CREDIT_USD", "1")
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &cbState{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			// Background / setup
			sc.Step(`^a fresh money store$`, st.freshStore)
			sc.Step(`^the platform fee rate is (\d+)%$`, st.feePct)
			sc.Step(`^wallet "([^"]*)" has ([\d.]+) in real credits$`, st.walletReal)
			sc.Step(`^wallet "([^"]*)" has ([\d.]+) in FREE seed credits$`, st.walletSeed)
			sc.Step(`^node "([^"]*)" is owned by account "([^"]*)"$`, st.nodeOwned)
			sc.Step(`^(\w+) has a settled request "([^"]*)" of cost ([\d.]+) on node "([^"]*)"$`, st.settledReqNoShare)
			sc.Step(`^(\w+) has a settled request "([^"]*)" of cost ([\d.]+) on node "([^"]*)" with owner share ([\d.]+)$`, st.settledReqShare)
			sc.Step(`^(\w+) has a later settled request "([^"]*)" of cost ([\d.]+) on node "([^"]*)"$`, st.laterReqNoShare)
			sc.Step(`^(\w+) has a later settled request "([^"]*)" of cost ([\d.]+) on node "([^"]*)" with owner share ([\d.]+)$`, st.laterReqShare)
			sc.Step(`^(\w+) has an earlier settled request "([^"]*)" of cost ([\d.]+) on node "([^"]*)" with owner share ([\d.]+)$`, st.earlierReqShare)
			sc.Step(`^(\w+) has a single settled request "([^"]*)" of cost ([\d.]+) on node "([^"]*)" with owner share ([\d.]+)$`, st.singleReqShare)
			sc.Step(`^an unrelated consumer "([^"]*)" has a settled request "([^"]*)" of cost ([\d.]+) on node "([^"]*)" with owner share ([\d.]+)$`, st.unrelatedReqShare)
			sc.Step(`^(\w+) has a settled request "([^"]*)" of cost ([\d.]+) on node "([^"]*)" funded entirely by seed credits$`, st.seedFundedReq)
			sc.Step(`^(\w+) has (\d+) settled requests of cost ([\d.]+) each on node "([^"]*)" with owner share ([\d.]+) each$`, st.nSettledReqs)
			sc.Step(`^(\w+) has lots totaling ([\d.]+) in operator gross across (\d+) lots of equal cost on node "([^"]*)"$`, st.lotsTotaling)
			sc.Step(`^(\w+) has settled requests in this order oldest-first: (.+) each cost ([\d.]+) owner share ([\d.]+) on node "([^"]*)"$`, st.orderedReqs)
			sc.Step(`^(\w+) has no settled requests$`, st.noSettledReqs)
			sc.Step(`^the lot for "([^"]*)" has gross ([\d.]+)$`, st.lotHasGross)
			sc.Step(`^the lot for "([^"]*)" is held$`, st.lotHeld)
			sc.Step(`^the lot for "([^"]*)" is payable$`, st.lotPayable)
			sc.Step(`^the lot for "([^"]*)" was paid out on Stripe transfer "([^"]*)"$`, st.lotPaidTransfer)
			sc.Step(`^the lot for "([^"]*)" was paid out with no recorded Stripe transfer id$`, st.lotPaidNoTransfer)
			sc.Step(`^operator "([^"]*)" has requested and settled a payout that paid the lot for "([^"]*)" on transfer "([^"]*)"$`, st.opPaidLot)
			sc.Step(`^a dispute "([^"]*)" of ([\d.]+) arrives with no stored charge mapping and no metadata wallet$`, st.disputeArrivesUnresolvable)

			// When
			sc.Step(`^a wallet-recency dispute "([^"]*)" of ([\d.]+) is opened on (\w+)$`, st.recencyDispute)
			sc.Step(`^a dispute "([^"]*)" of ([\d.]+) is opened on (\w+)'s most recent charge for "([^"]*)"$`, st.explicitMostRecent)
			sc.Step(`^a dispute "([^"]*)" of ([\d.]+) is opened on (\w+)'s request "([^"]*)"$`, st.explicitRequest)
			sc.Step(`^the same dispute "([^"]*)" is delivered again$`, st.deliveredAgain)
			sc.Step(`^the dispute webhook processes "([^"]*)"$`, st.webhookProcesses)
			sc.Step(`^the dispute webhook receives "([^"]*)" with amount (-?[\d.]+) for (\w+)$`, st.webhookReceivesNeg)

			// Then
			sc.Step(`^exactly ([\d.]+) is clawed from operator "([^"]*)"$`, st.clawedFromOp)
			sc.Step(`^([\d.]+) is clawed from operator "([^"]*)"$`, st.clawedFromOp)
			sc.Step(`^the platform loss is ([\d.]+)$`, st.platformLossIs)
			sc.Step(`^the lot for "([^"]*)" is still intact and not clawed$`, st.lotIntact)
			sc.Step(`^operator "([^"]*)" retains ([\d.]+) in lots$`, st.opRetains)
			sc.Step(`^operator "([^"]*)" retains ([\d.]+) across (\d+) untouched lots$`, st.opRetainsAcross)
			sc.Step(`^clawed plus reversed plus platform loss equals ([\d.]+)$`, st.conservation)
			sc.Step(`^(\w+)'s balance is reduced by exactly ([\d.]+)$`, st.balanceReducedBy)
			sc.Step(`^exactly one lot is clawed$`, st.exactlyOneLotClawed)
			sc.Step(`^the newest lot "([^"]*)" is clawed$`, st.lotClawed)
			sc.Step(`^(\w+)'s balance is unchanged$`, st.balanceUnchanged)
			sc.Step(`^operator "([^"]*)" is not clawed at all$`, st.opNotClawed)
			sc.Step(`^(\d+) lots are still intact and not clawed$`, st.nLotsIntact)
			sc.Step(`^the lot for "([^"]*)" is marked clawed$`, st.lotClawed)
			sc.Step(`^([\d.]+) is counted as clawed-in-place$`, st.clawedInPlace)
			sc.Step(`^no Stripe transfer reversal is requested$`, st.noReversal)
			sc.Step(`^an operator adjustment ledger row of (-?[\d.]+) is written for the dispute$`, st.adjustmentRow)
			sc.Step(`^one Stripe transfer reversal is returned for transfer "([^"]*)" of amount ([\d.]+)$`, st.oneReversal)
			sc.Step(`^a payout_reversed ledger row of (-?[\d.]+) is written for operator "([^"]*)"$`, st.payoutReversedRow)
			sc.Step(`^the reversal carries dispute "([^"]*)", the lot id, and operator account "([^"]*)"$`, st.reversalCarries)
			sc.Step(`^the lot for "([^"]*)" is clawed in place for ([\d.]+)$`, st.lotClawedInPlaceFor)
			sc.Step(`^clawed-in-place plus reversed equals ([\d.]+)$`, st.clawedPlusReversed)
			sc.Step(`^the returned reversal has an empty transfer id$`, st.reversalEmptyTransfer)
			sc.Step(`^the broker skips the Stripe reversal and logs it for manual reconciliation$`, st.brokerSkipsReversal)
			sc.Step(`^the ledger clawback still stands$`, st.ledgerClawbackStands)
			sc.Step(`^the lot for "([^"]*)" is clawed$`, st.lotClawed)
			sc.Step(`^nothing is clawed from any operator$`, st.nothingClawed)
			sc.Step(`^a platform_loss ledger row of (-?[\d.]+) is written for the dispute$`, st.platformLossRow)
			sc.Step(`^the redelivery reports already-handled$`, st.redeliveryAlreadyHandled)
			sc.Step(`^the redelivery claws back ([\d.]+)$`, st.redeliveryClaws)
			sc.Step(`^the redelivery books no additional platform loss$`, st.redeliveryNoLoss)
			sc.Step(`^(\w+)'s balance is unchanged by the redelivery$`, st.balanceUnchangedByRedelivery)
			sc.Step(`^the lot for "([^"]*)" is clawed exactly once$`, st.lotClawedOnce)
			sc.Step(`^no second chargeback ledger row is written for "([^"]*)"$`, st.noSecondChargeback)
			sc.Step(`^the total clawed across both deliveries is ([\d.]+)$`, st.totalClawedIs)
			sc.Step(`^the lot for "([^"]*)" is clawed by "([^"]*)"$`, st.lotClawedBy)
			sc.Step(`^no lot is clawed twice$`, st.noLotClawedTwice)
			sc.Step(`^the total clawed across both disputes is ([\d.]+)$`, st.totalClawedIs)
			sc.Step(`^"([^"]*)" claws ([\d.]+) from operator "([^"]*)"$`, st.disputeClawsFromOp)
			sc.Step(`^"([^"]*)" claws ([\d.]+) from any operator$`, st.disputeClawsAny)
			sc.Step(`^"([^"]*)" books a platform loss of ([\d.]+)$`, st.disputeBooksLoss)
			sc.Step(`^no operator other than "([^"]*)" is clawed$`, st.noOtherOpClawed)
			sc.Step(`^the amount recovered from operator "([^"]*)" must not exceed ([\d.]+)$`, st.recoveredNotExceed)
			sc.Step(`^no clawback is performed$`, st.noClawbackPerformed)
			sc.Step(`^no platform loss is booked$`, st.noPlatformLossBooked)
			sc.Step(`^the webhook still acknowledges receipt$`, st.webhookAcks)
			sc.Step(`^no clawback is performed for "([^"]*)"$`, st.noClawbackFor)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/money/chargebacks.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("money/chargebacks behavior scenarios failed (see godog output above)")
	}
}
