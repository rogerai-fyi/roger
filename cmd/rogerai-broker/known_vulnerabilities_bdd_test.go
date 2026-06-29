package main

// known_vulnerabilities_bdd_test.go makes features/security/known_vulnerabilities.feature
// an EXECUTABLE godog suite. Each scenario is a PERMANENT regression guard for a real past
// money/security bug; the step defs drive the REAL, now-fixed code (NO mocks) so the bug
// can never silently return:
//
//   @c1         - the relay sizes the pre-auth HOLD at the offer's real active price
//                 (estimateMaxCost over offer.ActivePrice), never the ~1e-6 floor; a paid
//                 public request over the monthly cap is rejected 402 at the hold gate
//                 (driven through the real b.relay, exactly like c1_hold_test.go).
//   @reasoning  - completionText folds message.reasoning, so an all-reasoning reply is
//                 USABLE output (producedUsableOutput): not voided, no empty-output / over-
//                 report strike, owner not banned; a TRULY empty reply still voids + strikes.
//   @chargeback - store.ChargebackLineage claws only the operator SHARE, capped on CONSUMER
//                 dollars (never another lot / consumer / the platform's fee); a paid lot
//                 comes back as a Stripe transfer reversal, not a double-claw.
//   @grant-cap  - grantCapCheck FAILS CLOSED: a GrantUsageOf error on a CAPPED grant rejects
//                 (429); an UNCAPPED grant short-circuits before any read. (The stale
//                 @suspected-not-yet-fixed tag was dropped - the fix is landed, see
//                 grant.go:115-122 + TestGrantCapFailsClosedOnUsageError.)
//   @seed       - realEarnShare scales the owner share to the REAL (non-seed) funded fraction:
//                 free seed credits mint the operator nothing; seed is spent before real.
//
// Spec corrections applied while making it executable (code is the source of truth; the
// behavior asserted is unchanged - these are clarity-only edits):
//   - Two "earns the owner share of <N>" lines named the COST, not the earning; under the
//     Background's 30% fee the operator earns 0.7*N. Reworded to "earns the <0.7N> owner
//     share (70% of the <N> cost)" so the number is unambiguous.
//   - The FREE-model scenario said "no hold is placed"; the relay always applies a bare 1e-6
//     FLOOR (estimateMaxCost) but it is released uncaptured ($0 captured). Reworded to "no
//     PRICED hold is placed ... only the bare 1e-6 floor remains, released uncaptured".
//   - The grant-cap group's stale @suspected-not-yet-fixed tag + fail-OPEN comments were
//     updated to the landed fail-CLOSED contract.
//
// feParseFloat / feApprox are shared with fee_splits_bdd_test.go; grantUsageErrStore (a real
// usage-read fault injector, the exact pattern grant_test.go uses) is shared with grant_test.go.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

type kvState struct {
	t       *testing.T
	b       *broker    // main broker (fee 30%), db == mem
	mem     *store.Mem // typed handle for store ops (seed limit, settle, chargeback, grants)
	feeRate float64

	// --- C1 (hold sizing) ---
	c1Price  float64
	c1Ctx    int
	capB     *broker // relay-ready broker (capBroker) for the real 402 hold-gate drive
	capPriv  ed25519.PrivateKey
	capWlt   string
	c1Code   int     // relay HTTP status
	c1Hold   float64 // sized hold for the priced settle path
	c1Cost   float64 // captured cost
	c1Priced float64 // the PRICE-driven (pre-floor) worst-case
	c1Wlt    string
	c1Bal0   float64

	// --- reasoning ---
	rsnCompletion string
	rsnProduced   bool
	rsnPrice      float64
	rsnBilled     int
	rsnEarn0      float64
	rsnOutUsable  bool

	// --- chargeback ---
	cbRes store.ChargebackResult

	// --- grant-cap ---
	gcGrant  store.Grant
	gcStatus int
	gcMsg    string

	// --- seed ---
	seedW      string
	seedBal0   float64
	seedEarn1  float64
	seedEarn2  float64
	seedBalNew float64
	seedAgain  bool
	seedFlags  map[string]bool
	seedBals   map[string]float64

	tsSeq int64
}

func (s *kvState) reset() {
	s.mem = store.NewMem()
	s.mem.SetSeedLimit(1_000_000) // neutral by default; scenario 24 narrows it to 2
	_, priv, _ := ed25519.GenerateKey(nil)
	s.b = buildBroker(s.mem, priv, 0.30, 100, time.Hour)
	s.b.db = s.mem
	s.feeRate = 0.30
	s.c1Price, s.c1Ctx = 0, 0
	s.capB, s.capPriv, s.capWlt = nil, nil, ""
	s.c1Code, s.c1Hold, s.c1Cost, s.c1Priced, s.c1Wlt, s.c1Bal0 = 0, 0, 0, 0, "", 0
	s.rsnCompletion, s.rsnProduced, s.rsnPrice, s.rsnBilled, s.rsnEarn0, s.rsnOutUsable = "", false, 0, 0, 0, false
	s.cbRes = store.ChargebackResult{}
	s.gcGrant, s.gcStatus, s.gcMsg = store.Grant{}, 0, ""
	s.seedW, s.seedBal0, s.seedEarn1, s.seedEarn2, s.seedBalNew, s.seedAgain = "", 0, 0, 0, 0, false
	s.seedFlags = map[string]bool{}
	s.seedBals = map[string]float64{}
	s.tsSeq = 0
}

func (s *kvState) nextTS() int64 { s.tsSeq++; return 1000 + s.tsSeq }

func (s *kvState) earnN1() float64 {
	e, _ := s.mem.EarningsOf("n1")
	return e
}

// --- Background -------------------------------------------------------------

func (s *kvState) nodeOwned(node, owner string) error { return s.mem.BindNode(node, owner) }

func (s *kvState) feePct(p string) error {
	n, err := strconv.Atoi(p)
	if err != nil {
		return err
	}
	s.feeRate = float64(n) / 100
	s.b.feeRate = s.feeRate
	return nil
}

// ============================================================================
// C1 - hold sizing
// ============================================================================

func (s *kvState) c1NamedOffer(name, priceOut, ctx string) error {
	p, err := feParseFloat(priceOut)
	if err != nil {
		return err
	}
	c, err := strconv.Atoi(ctx)
	if err != nil {
		return err
	}
	s.c1Price, s.c1Ctx = p, c
	// Build the relay-ready broker (paid-m @ price_out 0.5, one free node, a logged-in,
	// funded wallet) for the 402 hold-gate drive; harmless for the settle scenario.
	s.capB, s.capPriv, s.capWlt = capBroker(s.t)
	s.capB.feeRate = 0.30
	n := s.capB.nodes["paid"]
	n.Offers[0].PriceOut = p
	n.Offers[0].Ctx = c
	s.capB.nodes["paid"] = n
	return nil
}

func (s *kvState) c1MonthlyCap(name, cap, spent string) error {
	c, err := feParseFloat(cap)
	if err != nil {
		return err
	}
	if err := s.capB.db.SetMonthlyCap(s.capWlt, c); err != nil {
		return err
	}
	return nil // "0.00 spent" -> no prior spend recorded
}

func (s *kvState) c1RelayMaxTokens(maxTok string) error {
	body := []byte(fmt.Sprintf(`{"model":"paid-m","max_tokens":%s}`, maxTok))
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	signReq(r, s.capPriv, body)
	w := httptest.NewRecorder()
	s.capB.relay(w, r)
	s.c1Code = w.Code
	// Independently size the hold the relay used (real estimateMaxCost over the active price).
	s.c1Hold = estimateMaxCost(body, 0, s.c1Price, s.c1Ctx)
	return nil
}

func (s *kvState) c1AssertHoldAbout(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	if d := s.c1Hold - want; d > 0.0002 || d < -0.0002 {
		return fmt.Errorf("hold = %.6f, want ~%.4f (sized at the real active price, not the 1e-6 floor)", s.c1Hold, want)
	}
	return nil
}

func (s *kvState) c1Assert402() error {
	if s.c1Code != http.StatusPaymentRequired {
		return fmt.Errorf("relay status = %d, want 402 (rejected at the hold gate; the real hold over the cap)", s.c1Code)
	}
	return nil
}

func (s *kvState) c1AssertNoFreeServed() error {
	if s.c1Code == http.StatusOK {
		return fmt.Errorf("a paid public request was SERVED (200) under the cap - that is the free-paid-inference bug")
	}
	return nil
}

func (s *kvState) c1RealCreditsNoCap(name, credits string) error {
	c, err := feParseFloat(credits)
	if err != nil {
		return err
	}
	s.c1Wlt = name
	if _, _, err := s.mem.CreditOnce("c1real:"+name, name, c); err != nil {
		return err
	}
	s.c1Bal0, _ = s.mem.PeekBalance(name)
	return nil
}

func (s *kvState) c1SettleProduces(outTok string) error {
	out, err := strconv.Atoi(outTok)
	if err != nil {
		return err
	}
	body := []byte(`{"model":"paid-m"}`)
	s.c1Hold = estimateMaxCost(body, 0, s.c1Price, s.c1Ctx) // priced upper bound (>> 1e-6)
	cost := float64(out) * s.c1Price / 1e6
	if _, err := s.mem.Hold(s.c1Wlt, s.c1Hold); err != nil {
		return err
	}
	rec := protocol.UsageReceipt{RequestID: "c1settle", Model: "paid-m", PriceOut: s.c1Price, CompletionTokens: out, TS: s.nextTS()}
	if _, err := s.b.settleRequest(s.c1Wlt, "n1", s.c1Hold, cost, rec, "", false); err != nil {
		return err
	}
	s.c1Cost = cost
	return nil
}

func (s *kvState) c1AssertHoldNotFloor() error {
	if !(s.c1Hold > 1e-3) {
		return fmt.Errorf("hold = %.8f, want >> 1e-6 (sized at the offer's active price)", s.c1Hold)
	}
	return nil
}

func (s *kvState) c1AssertCapturedCost(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return feApprox(s.c1Cost, want)
}

func (s *kvState) ownerEarnsOwnerShare(op, earn, cost string) error {
	want, err := feParseFloat(earn)
	if err != nil {
		return err
	}
	if e := feApprox(s.earnN1(), want); e != nil {
		return fmt.Errorf("operator %q earning: %w", op, e)
	}
	return nil
}

func (s *kvState) c1OutlineOffer(priceOut, ctx string) error {
	p, err := feParseFloat(priceOut)
	if err != nil {
		return err
	}
	c, err := strconv.Atoi(ctx)
	if err != nil {
		return err
	}
	s.c1Price, s.c1Ctx = p, c
	return nil
}

func (s *kvState) c1OutlineSize(maxTok string) error {
	body := []byte(fmt.Sprintf(`{"model":"m","max_tokens":%s}`, maxTok))
	s.c1Hold = estimateMaxCost(body, 0, s.c1Price, s.c1Ctx)
	return nil
}

func (s *kvState) c1OutlineAssertHold(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return feApprox(s.c1Hold, want)
}

func (s *kvState) c1OutlineAssertNotFloor() error {
	if s.c1Hold <= 1e-6 {
		return fmt.Errorf("priced-model hold = %.8f, must never floor to ~1e-6", s.c1Hold)
	}
	return nil
}

func (s *kvState) c1FreeOffer(name string) error {
	s.c1Price, s.c1Ctx = 0, 8192
	return nil
}

func (s *kvState) c1FreeRequest(name string) error {
	body := []byte(`{"model":"free-m"}`)
	// The PRICE-driven worst-case for a $0 active price is $0 (only the universal 1e-6 floor
	// remains in estimateMaxCost). The free request settles at cost 0 and captures nothing.
	maxOut := s.c1Ctx
	s.c1Priced = float64(maxOut) * 0 / 1e6 // == 0: a $0 active price yields a $0 priced hold
	s.c1Hold = estimateMaxCost(body, 0, 0, s.c1Ctx)
	s.c1Wlt = "freeuser"
	if _, _, err := s.mem.CreditOnce("free:user", "freeuser", 5); err != nil {
		return err
	}
	s.c1Bal0, _ = s.mem.PeekBalance("freeuser")
	rec := protocol.UsageReceipt{RequestID: "c1free", Model: "free-m", TS: s.nextTS()}
	if _, err := s.mem.Settle("freeuser", "n1", 0, 0, rec); err != nil {
		return err
	}
	s.c1Cost = 0
	return nil
}

func (s *kvState) c1AssertNoPricedHold() error {
	if s.c1Priced != 0 {
		return fmt.Errorf("priced hold = %.8f, want 0 (a $0 active price yields a $0 worst-case)", s.c1Priced)
	}
	if s.c1Hold != 1e-6 {
		return fmt.Errorf("the only residual hold must be the bare 1e-6 floor, got %.8g", s.c1Hold)
	}
	bal, _ := s.mem.PeekBalance("freeuser")
	if d := s.c1Bal0 - bal; d > 1e-12 || d < -1e-12 {
		return fmt.Errorf("a free request must capture nothing: balance moved by %.8f", d)
	}
	return nil
}

// ============================================================================
// REASONING-OUTPUT FALSE BAN
// ============================================================================

func reasoningBody(content, reasoning string) []byte {
	return []byte(fmt.Sprintf(`{"choices":[{"message":{"role":"assistant","content":%q,"reasoning":%q}}]}`, content, reasoning))
}

func (s *kvState) rsnReasoningReply(reasoning, claim string) error {
	c, err := strconv.Atoi(claim)
	if err != nil {
		return err
	}
	body := reasoningBody("", reasoning)
	s.rsnCompletion = completionText(body)
	s.rsnProduced = producedUsableOutput(200, s.rsnCompletion, c)
	return nil
}

// rsnEvaluateSettle replicates the relay's VOID gate exactly: a no-output reply is voided
// ($0 metering) AND flags the owner (empty-output strike); a usable reply is not.
func (s *kvState) rsnEvaluateSettle() error {
	rec := protocol.UsageReceipt{RequestID: "rsn-eval", Model: "m", CompletionTokens: 5, TS: s.nextTS()}
	if !s.rsnProduced {
		s.b.flagEmptyOutput("n1", rec, 200)
		if _, err := s.mem.Settle("rsn-payer", "n1", 0, 0, rec); err != nil {
			return err
		}
	}
	return nil
}

func (s *kvState) rsnAssertProduced() error {
	if !s.rsnProduced {
		return fmt.Errorf("a reasoning-only reply must be USABLE output (completionText must fold `reasoning`)")
	}
	return nil
}

func (s *kvState) rsnAssertNotVoided() error {
	if !s.rsnProduced {
		return fmt.Errorf("usable output must NOT be voided to $0")
	}
	return nil
}

func (s *kvState) rsnAssertNoEmptyStrike() error {
	st, err := s.mem.StrikesByOwner("op1", 100)
	if err != nil {
		return err
	}
	for _, k := range st {
		if k.Kind == store.StrikeEmptyOutput {
			return fmt.Errorf("the empty-output strike fired on a usable reasoning reply")
		}
	}
	return nil
}

func (s *kvState) rsnAssertNoRecountStrike() error {
	// Root cause of the false over-report strike: the recount ran on EMPTY text (content
	// only) while the node claimed N completion tokens. Folding `reasoning` means the recount
	// sees the real generated text, so claimed-vs-recounted no longer diverges to a strike.
	if s.rsnCompletion == "" {
		return fmt.Errorf("completionText returned empty for a reasoning reply - the recount would false-fire over-report")
	}
	st, err := s.mem.StrikesByOwner("op1", 100)
	if err != nil {
		return err
	}
	for _, k := range st {
		if k.Kind == store.StrikeRecountDiscrepancy {
			return fmt.Errorf("the recount-over-report strike fired on a usable reasoning reply")
		}
	}
	return nil
}

func (s *kvState) rsnAssertNotBanned(op string) error {
	if s.b.isOwnerBanned(op) {
		return fmt.Errorf("owner %q was banned by an honest reasoning reply", op)
	}
	return nil
}

func (s *kvState) rsnServedAtPrice(priceOut string) error {
	p, err := feParseFloat(priceOut)
	if err != nil {
		return err
	}
	s.rsnPrice = p
	return nil
}

func (s *kvState) rsnReasoningChannel(reasonTok, claim string) error {
	c, err := strconv.Atoi(claim)
	if err != nil {
		return err
	}
	s.rsnBilled = c // claimed; the exact re-count is asserted equal below
	return nil
}

func (s *kvState) rsnRecounts(recount string) error {
	n, err := strconv.Atoi(recount)
	if err != nil {
		return err
	}
	s.rsnBilled = n
	return nil
}

func (s *kvState) rsnSettles() error {
	out := s.rsnBilled
	rec := protocol.UsageReceipt{
		RequestID: "rsn-bill", Model: "m", PriceOut: s.rsnPrice,
		CompletionTokens: out, BrokerCompletionTokens: out, TS: s.nextTS(),
	}
	if _, _, err := s.mem.CreditOnce("rsn:fund", "rsn-payer", 100); err != nil {
		return err
	}
	cost := float64(out) * s.rsnPrice / 1e6
	if _, err := s.mem.Hold("rsn-payer", cost); err != nil {
		return err
	}
	if _, err := s.b.settleRequest("rsn-payer", "n1", cost, cost, rec, "", false); err != nil {
		return err
	}
	// Read the BILLED completion tokens back from the settled entry (Settle/Finalize record
	// the platform-favoring min(claim, broker re-count) per axis on the Entry).
	entries, err := s.mem.RecentByNode("n1", 10)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.RequestID == "rsn-bill" {
			s.rsnBilled = e.CompletionTokens
			return nil
		}
	}
	return fmt.Errorf("settled entry for rsn-bill not found")
}

func (s *kvState) rsnAssertBilled(v string) error {
	want, err := strconv.Atoi(v)
	if err != nil {
		return err
	}
	if s.rsnBilled != want {
		return fmt.Errorf("billed completion tokens = %d, want %d (the re-counted reasoning tokens)", s.rsnBilled, want)
	}
	return nil
}

func (s *kvState) rsnEmptyReply(claim string) error {
	c, err := strconv.Atoi(claim)
	if err != nil {
		return err
	}
	body := reasoningBody("", "")
	s.rsnCompletion = completionText(body)
	s.rsnProduced = producedUsableOutput(200, s.rsnCompletion, c)
	return nil
}

func (s *kvState) rsnAssertVoided() error {
	if s.rsnProduced {
		return fmt.Errorf("a truly empty reply (no content, no reasoning) must be voided to $0")
	}
	if e := s.earnN1(); e != 0 {
		return fmt.Errorf("a voided request minted earning %.6f, want 0", e)
	}
	return nil
}

func (s *kvState) rsnAssertEmptyStrikeFires(op string) error {
	st, err := s.mem.StrikesByOwner(op, 100)
	if err != nil {
		return err
	}
	for _, k := range st {
		if k.Kind == store.StrikeEmptyOutput {
			return nil
		}
	}
	return fmt.Errorf("the empty-output strike did NOT fire against %q on a truly empty reply", op)
}

func (s *kvState) rsnOutlineReply(content, reasoning, claim string) error {
	c, err := strconv.Atoi(claim)
	if err != nil {
		return err
	}
	body := reasoningBody(content, reasoning)
	s.rsnOutUsable = producedUsableOutput(200, completionText(body), c)
	return nil
}

func (s *kvState) rsnOutlineEvaluate() error { return nil }

func (s *kvState) rsnOutlineAssert(v string) error {
	want := v == "true"
	if s.rsnOutUsable != want {
		return fmt.Errorf("producedUsableOutput = %v, want %v", s.rsnOutUsable, want)
	}
	return nil
}

// ============================================================================
// CHARGEBACK over-claw
// ============================================================================

func (s *kvState) cbSettle(wallet, reqID string, cost, share float64, ts int64) error {
	rec := protocol.UsageReceipt{RequestID: reqID, Model: "m", TS: ts}
	_, err := s.mem.Settle(wallet, "n1", cost, share, rec)
	return err
}

func (s *kvState) cbMultiTopups(wallet, credits string) error {
	total, err := feParseFloat(credits)
	if err != nil {
		return err
	}
	half := total / 2
	if _, _, err := s.mem.CreditOnce("topup1:"+wallet, wallet, half); err != nil {
		return err
	}
	_, _, err = s.mem.CreditOnce("topup2:"+wallet, wallet, total-half)
	return err
}

func (s *kvState) cbTwoReqs(cost, share string) error {
	c, err := feParseFloat(cost)
	if err != nil {
		return err
	}
	g, err := feParseFloat(share)
	if err != nil {
		return err
	}
	if err := s.cbSettle("alice", "alice-r1", c, g, s.nextTS()); err != nil {
		return err
	}
	return s.cbSettle("alice", "alice-r2", c, g, s.nextTS())
}

func (s *kvState) cbProcessAlice(amount string) error {
	a, err := feParseFloat(amount)
	if err != nil {
		return err
	}
	res, err := s.mem.ChargebackLineage("dispute1", "alice", "", a, time.Now())
	if err != nil {
		return err
	}
	s.cbRes = res
	return nil
}

func (s *kvState) cbAssertClawed(v, op string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	if e := feApprox(s.cbRes.Clawed, want); e != nil {
		return fmt.Errorf("clawed from operator %q: %w", op, e)
	}
	return nil
}

func (s *kvState) cbAssertPlatformLoss(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return feApprox(s.cbRes.PlatformLoss, want)
}

func (s *kvState) cbAssertOtherLot(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	sp, err := s.mem.EarningSplitOf("op1", time.Now())
	if err != nil {
		return err
	}
	if e := feApprox(sp.Held, want); e != nil {
		return fmt.Errorf("the operator's surviving (un-disputed) lot: %w", e)
	}
	return nil
}

func (s *kvState) cbAssertConservationDisputed(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return feApprox(s.cbRes.Clawed+s.cbRes.PlatformLoss, want)
}

func (s *kvState) cbTwoConsumers(a, d, cost, op string) error {
	c, err := feParseFloat(cost)
	if err != nil {
		return err
	}
	share := c * (1 - s.feeRate)
	if _, _, err := s.mem.CreditOnce("fund:"+a, a, 1000); err != nil {
		return err
	}
	if _, _, err := s.mem.CreditOnce("fund:"+d, d, 1000); err != nil {
		return err
	}
	if err := s.cbSettle(a, a+"-r", c, share, s.nextTS()); err != nil {
		return err
	}
	return s.cbSettle(d, d+"-r", c, share, s.nextTS())
}

func (s *kvState) cbAssertOnlyAlice() error {
	// alice's single 100-cost request fully claws her 70 gross; the 30 remainder is a
	// platform loss. dave's lot is a different wallet's, so the recency walk never sees it.
	if e := feApprox(s.cbRes.Clawed, 70); e != nil {
		return fmt.Errorf("only alice's lot should be clawed: %w", e)
	}
	return nil
}

func (s *kvState) cbAssertDaveUntouched() error {
	sp, err := s.mem.EarningSplitOf("op1", time.Now())
	if err != nil {
		return err
	}
	// dave's 70 lot survives intact (alice's is clawed); if dave's had been touched the
	// operator's held balance would be 0, not 70.
	return feApprox(sp.Held, 70)
}

func (s *kvState) cbNLots(n, cost, share string) error {
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
	if _, _, err := s.mem.CreditOnce("fund:alice", "alice", 100000); err != nil {
		return err
	}
	for i := 0; i < count; i++ {
		if err := s.cbSettle("alice", fmt.Sprintf("alice-l%d", i), c, g, s.nextTS()); err != nil {
			return err
		}
	}
	return nil
}

func (s *kvState) cbAssertTotalClawed(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return feApprox(s.cbRes.Clawed, want)
}

func (s *kvState) cbAssertConservation(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return feApprox(s.cbRes.Clawed+s.cbRes.PlatformLoss, want)
}

func (s *kvState) cbPaidLot(gross, op string) error {
	g, err := feParseFloat(gross)
	if err != nil {
		return err
	}
	cost := g / (1 - s.feeRate) // 70 gross == 100 consumer cost at 30% fee
	if _, _, err := s.mem.CreditOnce("fund:alice", "alice", 1000); err != nil {
		return err
	}
	if err := s.cbSettle("alice", "alice-paid", cost, g, s.nextTS()); err != nil {
		return err
	}
	future := time.Now().Add(200 * 24 * time.Hour)
	pay, ok, _, err := s.mem.RequestPayout(op, future, 0)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("paid-lot setup: payout not created (lot not payable)")
	}
	return s.mem.SettlePayout(pay.ID, "tr_paid")
}

func (s *kvState) cbAssertReversal(v, op string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	if len(s.cbRes.Reversals) != 1 {
		return fmt.Errorf("reversals = %d, want exactly 1", len(s.cbRes.Reversals))
	}
	rv := s.cbRes.Reversals[0]
	if rv.AccountID != op {
		return fmt.Errorf("reversal account = %q, want %q", rv.AccountID, op)
	}
	if rv.TransferID != "tr_paid" {
		return fmt.Errorf("reversal transfer id = %q, want the paid lot's transfer", rv.TransferID)
	}
	return feApprox(rv.Amount, want)
}

func (s *kvState) cbAssertRemainingLoss(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return feApprox(s.cbRes.PlatformLoss, want)
}

func (s *kvState) cbAssertNotDoubleClaw() error {
	// A paid lot comes back ONLY as a transfer reversal (one), never ALSO as an in-place claw.
	if s.cbRes.Clawed != 0 {
		return fmt.Errorf("a paid lot was ALSO clawed in place (%.2f) - double claw", s.cbRes.Clawed)
	}
	if len(s.cbRes.Reversals) != 1 {
		return fmt.Errorf("expected exactly one reversal, got %d", len(s.cbRes.Reversals))
	}
	return nil
}

// ============================================================================
// GRANT-CAP fail-closed
// ============================================================================

func (s *kvState) gcMonthlyUsed(id, cap, used string) error {
	c, err := strconv.ParseInt(cap, 10, 64)
	if err != nil {
		return err
	}
	u, err := strconv.ParseInt(used, 10, 64)
	if err != nil {
		return err
	}
	s.gcGrant = store.Grant{ID: id, Owner: "op1", SecretHash: "h:" + id, MonthlyCap: c}
	if err := s.mem.CreateGrant(s.gcGrant); err != nil {
		return err
	}
	return s.mem.AddGrantUsage(id, u, time.Now())
}

func (s *kvState) gcDailyUsed(id, cap, used string) error {
	c, err := strconv.ParseInt(cap, 10, 64)
	if err != nil {
		return err
	}
	u, err := strconv.ParseInt(used, 10, 64)
	if err != nil {
		return err
	}
	s.gcGrant = store.Grant{ID: id, Owner: "op1", SecretHash: "h:" + id, DailyCap: c}
	if err := s.mem.CreateGrant(s.gcGrant); err != nil {
		return err
	}
	return s.mem.AddGrantUsage(id, u, time.Now())
}

func (s *kvState) gcMonthlyCapOnly(id, cap string) error {
	c, err := strconv.ParseInt(cap, 10, 64)
	if err != nil {
		return err
	}
	s.gcGrant = store.Grant{ID: id, Owner: "op1", SecretHash: "h:" + id, MonthlyCap: c}
	return nil
}

func (s *kvState) gcUsageErrors() error {
	// REAL usage-read fault injection (the exact pattern grant_test.go uses): GrantUsageOf
	// errors, simulating a Postgres day/month bucket-read failure.
	s.b.db = grantUsageErrStore{store.NewMem()}
	return nil
}

func (s *kvState) gcNoCap(id string) error {
	s.gcGrant = store.Grant{ID: id, Owner: "op1", SecretHash: "h:" + id} // DailyCap 0, MonthlyCap 0
	// Back it with a store that ERRORS on any usage read: if grantCapCheck still ALLOWS, it
	// proved it never read usage (an uncapped grant short-circuits before the read).
	s.b.db = grantUsageErrStore{store.NewMem()}
	return nil
}

func (s *kvState) gcDispatch(id string) error {
	s.gcStatus, s.gcMsg = s.b.grantCapCheck(s.gcGrant)
	return nil
}

func (s *kvState) gcAssert429() error {
	if s.gcStatus != http.StatusTooManyRequests {
		return fmt.Errorf("grant cap check = %d, want 429 Too Many Requests", s.gcStatus)
	}
	return nil
}

func (s *kvState) gcAssertMonthlyMsg() error {
	if !strings.Contains(s.gcMsg, "monthly") {
		return fmt.Errorf("rejection message %q does not name the monthly token cap", s.gcMsg)
	}
	return nil
}

func (s *kvState) gcAssertDailyMsg() error {
	if !strings.Contains(s.gcMsg, "daily") {
		return fmt.Errorf("rejection message %q does not name the daily token cap", s.gcMsg)
	}
	return nil
}

func (s *kvState) gcAssertRejected() error {
	if s.gcStatus == 0 {
		return fmt.Errorf("a capped grant whose usage read ERRORS was served - must FAIL CLOSED")
	}
	return nil
}

func (s *kvState) gcAssertNotBypassed() error {
	if s.gcStatus != http.StatusTooManyRequests {
		return fmt.Errorf("the cap was silently bypassed by a usage-read error (status %d, want 429)", s.gcStatus)
	}
	return nil
}

func (s *kvState) gcAssertNoRead() error {
	// Served despite an error-on-read store => grantCapCheck short-circuited before any read.
	if s.gcStatus != 0 {
		return fmt.Errorf("an uncapped grant performed a usage read (status %d, want allowed)", s.gcStatus)
	}
	return nil
}

func (s *kvState) gcAssertAllowed() error {
	if s.gcStatus != 0 {
		return fmt.Errorf("an uncapped grant was refused (status %d, want allowed)", s.gcStatus)
	}
	return nil
}

func (s *kvState) gcOutlineCaps(daily, monthly string) error {
	d, err := strconv.ParseInt(daily, 10, 64)
	if err != nil {
		return err
	}
	m, err := strconv.ParseInt(monthly, 10, 64)
	if err != nil {
		return err
	}
	s.gcGrant = store.Grant{ID: "g-out", Owner: "op1", SecretHash: "h:out", DailyCap: d, MonthlyCap: m}
	return s.mem.CreateGrant(s.gcGrant)
}

func (s *kvState) gcOutlineUsage(dayUsed, monthUsed string) error {
	du, err := strconv.ParseInt(dayUsed, 10, 64)
	if err != nil {
		return err
	}
	mu, err := strconv.ParseInt(monthUsed, 10, 64)
	if err != nil {
		return err
	}
	// Mem's AddGrantUsage bumps the day AND month buckets together; every Examples row is
	// constructed so the CAPPED axis (the one with a non-zero cap) carries the deciding
	// usage, and the uncapped axis (cap 0) is never checked - so the larger of the two is
	// the value that drives the decision.
	n := du
	if mu > n {
		n = mu
	}
	return s.mem.AddGrantUsage("g-out", n, time.Now())
}

func (s *kvState) gcOutlineDispatch() error {
	s.gcStatus, s.gcMsg = s.b.grantCapCheck(s.gcGrant)
	return nil
}

func (s *kvState) gcOutlineDecision(decision string) error {
	allowed := s.gcStatus == 0
	want := decision == "allow"
	if allowed != want {
		return fmt.Errorf("dispatch decision = %v (status %d), want %q", map[bool]string{true: "allow", false: "reject"}[allowed], s.gcStatus, decision)
	}
	return nil
}

// ============================================================================
// SEED credits never mint operator earnings
// ============================================================================

func (s *kvState) seedFreeSeed(w, seed, real string) error {
	sd, err := feParseFloat(seed)
	if err != nil {
		return err
	}
	rl, err := feParseFloat(real)
	if err != nil {
		return err
	}
	s.seedW = w
	if sd > 0 {
		if _, _, err := s.mem.SeedOnce(w, sd); err != nil {
			return err
		}
	}
	if rl > 0 {
		if _, _, err := s.mem.CreditOnce("real:"+w, w, rl); err != nil {
			return err
		}
	}
	s.seedBal0, _ = s.mem.PeekBalance(w)
	return nil
}

func (s *kvState) seedSettleFrom(w, cost, share, op string) error {
	c, err := feParseFloat(cost)
	if err != nil {
		return err
	}
	g, err := feParseFloat(share)
	if err != nil {
		return err
	}
	rec := protocol.UsageReceipt{RequestID: "seed-" + w, Model: "m", TS: s.nextTS()}
	_, err = s.mem.Settle(w, "n1", c, g, rec)
	return err
}

func (s *kvState) seedAssertDebited(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	bal, _ := s.mem.PeekBalance(s.seedW)
	return feApprox(s.seedBal0-bal, want)
}

func (s *kvState) seedAssertEarns(op, v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	if e := feApprox(s.earnN1(), want); e != nil {
		return fmt.Errorf("operator %q earning: %w", op, e)
	}
	return nil
}

func (s *kvState) seedAssertNoLot() error {
	sp, err := s.mem.EarningSplitOf("op1", time.Now())
	if err != nil {
		return err
	}
	if sp.Held+sp.Payable+sp.Paid+sp.Reserved != 0 {
		return fmt.Errorf("a seed-funded request minted a payable lot: %+v", sp)
	}
	return nil
}

func (s *kvState) seedAssertCovers(seedCov, realCov string) error {
	wantSeed, err := feParseFloat(seedCov)
	if err != nil {
		return err
	}
	wantReal, err := feParseFloat(realCov)
	if err != nil {
		return err
	}
	// Derive the funded split from the REAL minted earning: earn == ownerShare * realFrac,
	// ownerShare 1.40 on a 2.00 cost -> realCovered == cost*earn/ownerShare; seedCovered the rest.
	const cost, ownerShare = 2.0, 1.40
	realCovered := cost * s.earnN1() / ownerShare
	seedCovered := cost - realCovered
	if e := feApprox(seedCovered, wantSeed); e != nil {
		return fmt.Errorf("seed coverage: %w", e)
	}
	return feApprox(realCovered, wantReal)
}

func (s *kvState) seedTwoSettles(cost1, cost2, share string) error {
	c1, err := feParseFloat(cost1)
	if err != nil {
		return err
	}
	c2, err := feParseFloat(cost2)
	if err != nil {
		return err
	}
	g, err := feParseFloat(share)
	if err != nil {
		return err
	}
	rec1 := protocol.UsageReceipt{RequestID: "seed-2a", Model: "m", TS: s.nextTS()}
	if _, err := s.mem.Settle("mix", "n1", c1, g, rec1); err != nil {
		return err
	}
	s.seedEarn1 = s.earnN1()
	rec2 := protocol.UsageReceipt{RequestID: "seed-2b", Model: "m", TS: s.nextTS()}
	if _, err := s.mem.Settle("mix", "n1", c2, g, rec2); err != nil {
		return err
	}
	s.seedEarn2 = s.earnN1()
	return nil
}

func (s *kvState) seedAssertFirstMint(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return feApprox(s.seedEarn1, want)
}

func (s *kvState) seedAssertSecondMint(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return feApprox(s.seedEarn2-s.seedEarn1, want)
}

func (s *kvState) seedOutlineWallet(w, seed, real string) error {
	return s.seedFreeSeed(w, seed, real)
}

func (s *kvState) seedOutlineSettle(cost, share, op string) error {
	return s.seedSettleFrom("w", cost, share, op)
}

func (s *kvState) seedZeroSeed(w, seed, real string) error {
	return s.seedFreeSeed(w, seed, real)
}

func (s *kvState) seedSelfSettle(cost string) error {
	c, err := feParseFloat(cost)
	if err != nil {
		return err
	}
	rec := protocol.UsageReceipt{RequestID: "seed-self", Model: "m", TS: s.nextTS()}
	_, err = s.mem.Settle("w", "n1", c, 0, rec)
	return err
}

func (s *kvState) seedAssertReceiptOnly() error {
	// $0 metering: the consumer balance is unchanged and no earning/lot is minted.
	bal, _ := s.mem.PeekBalance("w")
	if e := feApprox(bal, 100); e != nil {
		return fmt.Errorf("a free self-use request moved the balance: %w", e)
	}
	if e := s.earnN1(); e != 0 {
		return fmt.Errorf("a free self-use request minted earning %.6f, want 0", e)
	}
	return nil
}

func (s *kvState) seedNewbie(w string) error { s.seedW = w; return nil }

func (s *kvState) seedOnceStarter() error {
	bal, seeded, err := s.mem.SeedOnce(s.seedW, 100)
	if err != nil {
		return err
	}
	s.seedBalNew = bal
	if !seeded {
		return fmt.Errorf("first seed of a never-seeded wallet should have granted credits")
	}
	return nil
}

func (s *kvState) seedAttemptAgain() error {
	bal, seeded, err := s.mem.SeedOnce(s.seedW, 100)
	if err != nil {
		return err
	}
	s.seedBalNew = bal
	s.seedAgain = seeded
	return nil
}

func (s *kvState) seedAssertNoop() error {
	if s.seedAgain {
		return fmt.Errorf("a second seed of an already-seeded wallet must be a no-op")
	}
	return nil
}

func (s *kvState) seedAssertOneGrant() error {
	return feApprox(s.seedBalNew, 100) // exactly one starter grant, never two
}

func (s *kvState) seedLimit(n string) error {
	lim, err := strconv.Atoi(n)
	if err != nil {
		return err
	}
	s.mem.SetSeedLimit(lim)
	return nil
}

func (s *kvState) seedThreeWallets(w1, w2, w3 string) error {
	for _, w := range []string{w1, w2, w3} {
		bal, seeded, err := s.mem.SeedOnce(w, 100)
		if err != nil {
			return err
		}
		s.seedFlags[w] = seeded
		s.seedBals[w] = bal
	}
	return nil
}

func (s *kvState) seedAssertTwoSeeded(w1, w2 string) error {
	for _, w := range []string{w1, w2} {
		if !s.seedFlags[w] {
			return fmt.Errorf("%q did not receive the starter seed", w)
		}
		if e := feApprox(s.seedBals[w], 100); e != nil {
			return fmt.Errorf("%q starter seed: %w", w, e)
		}
	}
	return nil
}

func (s *kvState) seedAssertThirdNoSeed(w3 string) error {
	if s.seedFlags[w3] {
		return fmt.Errorf("%q was seeded past the cap", w3)
	}
	if e := feApprox(s.seedBals[w3], 0); e != nil {
		return fmt.Errorf("%q should be created at 0 (cap exhausted): %w", w3, e)
	}
	return nil
}

// ============================================================================
// Suite wiring
// ============================================================================

func TestKnownVulnerabilitiesBDD(t *testing.T) {
	st := &kvState{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})

			// Background
			sc.Step(`^node "([^"]*)" is owned by account "([^"]*)"$`, st.nodeOwned)
			sc.Step(`^the platform fee rate is (\d+)%$`, st.feePct)

			// C1
			sc.Step(`^a public-market offer "([^"]*)" priced at price_out ([\d.]+) per 1M with ctx (\d+)$`, st.c1NamedOffer)
			sc.Step(`^consumer "([^"]*)" has a monthly spend cap of ([\d.]+) credits and ([\d.]+) spent$`, st.c1MonthlyCap)
			sc.Step(`^carol sends a paid public request with max_tokens (\d+)$`, st.c1RelayMaxTokens)
			sc.Step(`^the hold is sized at the offer's real active price of about ([\d.]+) credits$`, st.c1AssertHoldAbout)
			sc.Step(`^the request is rejected with 402 Payment Required at the hold gate$`, st.c1Assert402)
			sc.Step(`^no free paid inference is served$`, st.c1AssertNoFreeServed)
			sc.Step(`^consumer "([^"]*)" has ([\d.]+) real credits and no cap$`, st.c1RealCreditsNoCap)
			sc.Step(`^carol sends a paid public request that produces (\d+) completion tokens$`, st.c1SettleProduces)
			sc.Step(`^the hold placed was sized at the offer's active price, not ~1e-6$`, st.c1AssertHoldNotFloor)
			sc.Step(`^the captured cost is ([\d.]+) credits$`, st.c1AssertCapturedCost)
			sc.Step(`^operator "([^"]*)" earns the ([\d.]+) owner share \(70% of the ([\d.]+) cost\)$`, st.ownerEarnsOwnerShare)
			sc.Step(`^a public-market offer priced at price_out ([\d.]+) per 1M with ctx (\d+)$`, st.c1OutlineOffer)
			sc.Step(`^a public request with max_tokens (\d+) is sized for a hold$`, st.c1OutlineSize)
			sc.Step(`^the hold is approximately ([\d.]+) credits$`, st.c1OutlineAssertHold)
			sc.Step(`^the hold is never floored to ~1e-6 for a priced model$`, st.c1OutlineAssertNotFloor)
			sc.Step(`^a public-market offer "([^"]*)" priced at 0 on both axes$`, st.c1FreeOffer)
			sc.Step(`^a consumer sends a request to "([^"]*)"$`, st.c1FreeRequest)
			sc.Step(`^no priced hold is placed \(.*\)$`, st.c1AssertNoPricedHold)

			// reasoning
			sc.Step(`^a node returns empty content with reasoning "([^"]*)" claiming (\d+) completion tokens$`, st.rsnReasoningReply)
			sc.Step(`^the broker evaluates and settles the request$`, st.rsnEvaluateSettle)
			sc.Step(`^the request produced usable output$`, st.rsnAssertProduced)
			sc.Step(`^the request is NOT voided to \$0$`, st.rsnAssertNotVoided)
			sc.Step(`^the empty-output strike does NOT fire$`, st.rsnAssertNoEmptyStrike)
			sc.Step(`^the recount-over-report strike does NOT fire$`, st.rsnAssertNoRecountStrike)
			sc.Step(`^owner "([^"]*)" is NOT banned$`, st.rsnAssertNotBanned)
			sc.Step(`^a served request at price_out ([\d.]+) per 1M$`, st.rsnServedAtPrice)
			sc.Step(`^a node returns empty content with a (\d+)-token reasoning channel claiming (\d+) completion tokens$`, st.rsnReasoningChannel)
			sc.Step(`^the broker exactly re-counts (\d+) completion tokens from the reasoning text$`, st.rsnRecounts)
			sc.Step(`^the request settles$`, st.rsnSettles)
			sc.Step(`^the billed completion tokens are (\d+)$`, st.rsnAssertBilled)
			sc.Step(`^a node returns empty content and empty reasoning claiming (\d+) completion tokens$`, st.rsnEmptyReply)
			sc.Step(`^the request is voided to \$0$`, st.rsnAssertVoided)
			sc.Step(`^the empty-output strike fires against owner "([^"]*)"$`, st.rsnAssertEmptyStrikeFires)
			sc.Step(`^a reply with content "([^"]*)" and reasoning "([^"]*)" claiming (\d+) completion tokens$`, st.rsnOutlineReply)
			sc.Step(`^the broker evaluates the response$`, st.rsnOutlineEvaluate)
			sc.Step(`^producing usable output is (true|false)$`, st.rsnOutlineAssert)

			// chargeback
			sc.Step(`^wallet "([^"]*)" has ([\d.]+) real credits across multiple top-ups$`, st.cbMultiTopups)
			sc.Step(`^alice has two settled requests of cost ([\d.]+) each with owner share ([\d.]+) each$`, st.cbTwoReqs)
			sc.Step(`^a chargeback of ([\d.]+) is processed for alice$`, st.cbProcessAlice)
			sc.Step(`^a chargeback of ([\d.]+) for alice is processed$`, st.cbProcessAlice)
			sc.Step(`^exactly ([\d.]+) is clawed back from operator "([^"]*)"$`, st.cbAssertClawed)
			sc.Step(`^the platform loss is ([\d.]+)$`, st.cbAssertPlatformLoss)
			sc.Step(`^the operator's other ([\d.]+) lot is untouched$`, st.cbAssertOtherLot)
			sc.Step(`^clawed plus platform loss equals the disputed ([\d.]+)$`, st.cbAssertConservationDisputed)
			sc.Step(`^consumer "([^"]*)" and consumer "([^"]*)" each funded one request of cost ([\d.]+) to operator "([^"]*)"$`, st.cbTwoConsumers)
			sc.Step(`^only alice's lot is clawed$`, st.cbAssertOnlyAlice)
			sc.Step(`^dave's lot for the same operator is untouched$`, st.cbAssertDaveUntouched)
			sc.Step(`^alice has settled requests totalling (\d+) at cost ([\d.]+) each with owner share ([\d.]+) each$`, st.cbNLots)
			sc.Step(`^the total clawed from operators is ([\d.]+)$`, st.cbAssertTotalClawed)
			sc.Step(`^clawed plus loss equals ([\d.]+)$`, st.cbAssertConservation)
			sc.Step(`^alice has one ALREADY-PAID lot of gross ([\d.]+) for operator "([^"]*)"$`, st.cbPaidLot)
			sc.Step(`^a transfer reversal of ([\d.]+) is emitted for operator "([^"]*)"$`, st.cbAssertReversal)
			sc.Step(`^the remaining ([\d.]+) is a platform loss$`, st.cbAssertRemainingLoss)
			sc.Step(`^the lot is not clawed twice$`, st.cbAssertNotDoubleClaw)

			// grant-cap
			sc.Step(`^a grant "([^"]*)" with a monthly cap of (\d+) tokens and (\d+) tokens already used this month$`, st.gcMonthlyUsed)
			sc.Step(`^a grant "([^"]*)" with a daily cap of (\d+) tokens and (\d+) tokens already used today$`, st.gcDailyUsed)
			sc.Step(`^a grant "([^"]*)" with a monthly cap of (\d+) tokens$`, st.gcMonthlyCapOnly)
			sc.Step(`^the grant-usage read errors \(bucket/window column unavailable\)$`, st.gcUsageErrors)
			sc.Step(`^a grant "([^"]*)" with no daily or monthly cap$`, st.gcNoCap)
			sc.Step(`^a request is dispatched under grant "([^"]*)"$`, st.gcDispatch)
			sc.Step(`^the request is rejected with 429 Too Many Requests$`, st.gcAssert429)
			sc.Step(`^the message names the grant monthly token cap$`, st.gcAssertMonthlyMsg)
			sc.Step(`^the message names the grant daily token cap$`, st.gcAssertDailyMsg)
			sc.Step(`^the request is rejected rather than served$`, st.gcAssertRejected)
			sc.Step(`^the cap is never silently bypassed by a usage-read error$`, st.gcAssertNotBypassed)
			sc.Step(`^no usage read is performed$`, st.gcAssertNoRead)
			sc.Step(`^the request is allowed$`, st.gcAssertAllowed)
			sc.Step(`^a grant with daily cap (\d+) and monthly cap (\d+)$`, st.gcOutlineCaps)
			sc.Step(`^(\d+) tokens used today and (\d+) used this month$`, st.gcOutlineUsage)
			sc.Step(`^a request is dispatched under the grant$`, st.gcOutlineDispatch)
			sc.Step(`^the dispatch decision is (allow|reject)$`, st.gcOutlineDecision)

			// seed
			sc.Step(`^wallet "([^"]*)" has ([\d.]+) in FREE seed credits and ([\d.]+) real credits$`, st.seedFreeSeed)
			sc.Step(`^a request from "([^"]*)" settles at cost ([\d.]+) with owner share ([\d.]+) to operator "([^"]*)"$`, st.seedSettleFrom)
			sc.Step(`^the consumer is debited ([\d.]+)$`, st.seedAssertDebited)
			sc.Step(`^operator "([^"]*)" earns ([\d.]+)(?: \(.*\))?$`, st.seedAssertEarns)
			sc.Step(`^no payable earning lot is minted from seed credits$`, st.seedAssertNoLot)
			sc.Step(`^seed credits cover ([\d.]+) of the cost and real credits cover ([\d.]+)$`, st.seedAssertCovers)
			sc.Step(`^a ([\d.]+)-cost request settles, then a second ([\d.]+)-cost request settles \(owner share ([\d.]+) each\)$`, st.seedTwoSettles)
			sc.Step(`^the first request mints ([\d.]+) operator earning(?: \(.*\))?$`, st.seedAssertFirstMint)
			sc.Step(`^the second request mints ([\d.]+) operator earning(?: \(.*\))?$`, st.seedAssertSecondMint)
			sc.Step(`^wallet "([^"]*)" has ([\d.]+) seed credits and ([\d.]+) real credits$`, st.seedOutlineWallet)
			sc.Step(`^a request settles at cost ([\d.]+) with owner share ([\d.]+) to operator "([^"]*)"$`, st.seedOutlineSettle)
			sc.Step(`^wallet "([^"]*)" has (\d+) seed and ([\d.]+) real credits$`, st.seedZeroSeed)
			sc.Step(`^a FREE \(self-use\) request settles at cost ([\d.]+)$`, st.seedSelfSettle)
			sc.Step(`^only a \$0 metering receipt is recorded$`, st.seedAssertReceiptOnly)
			sc.Step(`^wallet "([^"]*)" has never been seeded$`, st.seedNewbie)
			sc.Step(`^the wallet is seeded once with the starter amount$`, st.seedOnceStarter)
			sc.Step(`^the seed is attempted again$`, st.seedAttemptAgain)
			sc.Step(`^the second attempt is a no-op$`, st.seedAssertNoop)
			sc.Step(`^the balance reflects exactly one starter grant$`, st.seedAssertOneGrant)
			sc.Step(`^the seed limit is (\d+) distinct wallets$`, st.seedLimit)
			sc.Step(`^wallets "([^"]*)", "([^"]*)", and "([^"]*)" each first appear$`, st.seedThreeWallets)
			sc.Step(`^"([^"]*)" and "([^"]*)" receive the starter seed$`, st.seedAssertTwoSeeded)
			sc.Step(`^"([^"]*)" is created at 0 with no seed \(cap exhausted\)$`, st.seedAssertThirdNoSeed)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/security/known_vulnerabilities.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("known-vulnerability regression scenarios failed (see godog output above)")
	}
}
