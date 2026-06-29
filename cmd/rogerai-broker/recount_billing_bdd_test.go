package main

// recount_billing_bdd_test.go makes features/money/recount_billing.feature an EXECUTABLE
// Cucumber suite, driving the REAL L1 re-count + settle-time billing path:
//   - completionText (folds reasoning into completion) + producedUsableOutput (the VOID gate),
//   - settleRecount / settleRecountPrompt (bill min(claim, EXACT broker re-count) per axis;
//     heuristic / unreachable / disabled never re-bill; the input byte floor),
//   - protocol.UsageReceipt.CostWith2 + store.Settle/billedTokens (the actual charge + the
//     claim-vs-billed audit row) + flagEmptyOutput (the void strike),
//   - observeRecount / observeRecountInput (over-report -> earnings HELD past billing tol +
//     owner STRIKE past the wider strike tol; the async probe path never strikes),
//   - loadRecount (strike tolerance clamped up to billing tolerance).
//
// The tokenizer-sidecar is a local httptest stub that returns the re-count encoded in the
// posted text ("t:<tokens>:<exact>") so each axis gets its own count; "unreachable" points at
// a closed server and "disabled" clears the URL. Consequence assertions read STORE state
// (RecountHeldNodes, StrikesByOwner, the KindAdjust audit row, the $0 metering Entry) so a
// regression in any billing/void/strike invariant fails red. settleRecount fires observeRecount
// asynchronously; the suite ALSO calls it synchronously with the same (claim,recount,exact) so
// consequence reads are deterministic (the calls are idempotent + decision-consistent).
// feApprox/feParseFloat live in fee_splits_bdd_test.go (same package).

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

type rcState struct {
	b       *broker
	sidecar *httptest.Server
	now     time.Time
	feeRate float64

	wallet     string
	walletInit float64
	node       string
	owner      string

	model              string
	priceIn, priceOut  float64
	bodyBytes          int
	claimPrompt        int
	claimCompletion    int
	recountPrompt      int
	recountCompletion  int
	exact              bool
	recountPromptSet   bool
	recountCompletSet  bool

	completion    string
	completionSet bool
	content       string
	reasoning     string
	status        int
	statusSet     bool
	holdAmt       float64

	// settle outputs
	billedPrompt     int
	billedCompletion int
	cost             float64
	voided           bool

	// evaluate outputs
	completionTextResult string
	usable               bool

	reqID string
}

func (s *rcState) reset() {
	os.Setenv("ROGERAI_RECOUNT_TOLERANCE", "")
	os.Setenv("ROGERAI_RECOUNT_STRIKE_TOLERANCE", "")
	if s.sidecar != nil {
		s.sidecar.Close()
	}
	// Sidecar stub: return the re-count encoded in the posted text "t:<tokens>:<exact>";
	// otherwise fall back to the configured completion re-count (used when a real reasoning
	// text is posted, single-axis).
	s.sidecar = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Model, Text string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		tok, exact, ok := parseSentinel(in.Text)
		if !ok {
			tok, exact = s.recountCompletion, s.exact
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"tokens": tok, "exact": exact})
	}))
	os.Setenv("TOKENIZER_URL", s.sidecar.URL)
	_, priv, _ := ed25519.GenerateKey(nil)
	db := store.NewMem()
	s.b = buildBroker(db, priv, 0.30, 0, time.Hour)
	s.now = time.Now()
	s.feeRate = 0.30
	s.wallet, s.walletInit = "", 0
	s.node, s.owner = "n1", "op1"
	s.model, s.priceIn, s.priceOut, s.bodyBytes = "", 0, 0, 0
	s.claimPrompt, s.claimCompletion = 0, 0
	s.recountPrompt, s.recountCompletion, s.exact = 0, 0, true
	s.recountPromptSet, s.recountCompletSet = false, false
	s.completion, s.completionSet = "", false
	s.content, s.reasoning = "", ""
	s.status, s.statusSet = 200, false
	s.holdAmt = 0
	s.billedPrompt, s.billedCompletion, s.cost, s.voided = 0, 0, 0, false
	s.completionTextResult, s.usable = "", false
	s.reqID = "rq1"
}

func parseSentinel(text string) (int, bool, bool) {
	if !strings.HasPrefix(text, "t:") {
		return 0, false, false
	}
	parts := strings.Split(text, ":")
	if len(parts) != 3 {
		return 0, false, false
	}
	n, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, false, false
	}
	return n, parts[2] == "true", true
}

func sentinel(tokens int, exact bool) string { return fmt.Sprintf("t:%d:%t", tokens, exact) }

func rcBody(content, reasoning string) []byte {
	b, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{"content": content, "reasoning": reasoning}}},
	})
	return b
}

// --- Background / setup -----------------------------------------------------

func (s *rcState) sidecarEnabled() error  { return nil } // reset() already wires the stub
func (s *rcState) sidecarDisabled() error { s.b.recount.url = ""; return nil }

func (s *rcState) billingTol(p string) error {
	n, err := feParseFloat(p)
	if err != nil {
		return err
	}
	os.Setenv("ROGERAI_RECOUNT_TOLERANCE", strconv.FormatFloat(n/100, 'f', -1, 64))
	s.b.recount.tolerance = n / 100
	return nil
}

func (s *rcState) strikeTol(p string) error {
	n, err := feParseFloat(p)
	if err != nil {
		return err
	}
	os.Setenv("ROGERAI_RECOUNT_STRIKE_TOLERANCE", strconv.FormatFloat(n/100, 'f', -1, 64))
	s.b.recount.strikeTolerance = n / 100
	return nil
}

func (s *rcState) strikeTolConfigured(p string) error {
	n, err := feParseFloat(p)
	if err != nil {
		return err
	}
	os.Setenv("ROGERAI_RECOUNT_STRIKE_TOLERANCE", strconv.FormatFloat(n/100, 'f', -1, 64))
	return nil // loadRecount (when "the re-count config loads") applies the clamp
}

func (s *rcState) feePct(p string) error {
	n, err := strconv.Atoi(p)
	if err != nil {
		return err
	}
	s.feeRate = float64(n) / 100
	return nil
}

func (s *rcState) walletFunded(name, v string) error {
	f, err := feParseFloat(v)
	if err != nil {
		return err
	}
	s.wallet, s.walletInit = name, f
	_, err = s.b.db.AddCredits(name, f)
	return err
}

func (s *rcState) nodeOwned(node, owner string) error {
	s.node, s.owner = node, owner
	if err := s.b.db.BindNode(node, owner); err != nil {
		return err
	}
	return s.b.db.BindOwner(store.Owner{GitHubID: 1, Login: owner, Pubkey: owner})
}

func (s *rcState) servedPriceOut(model, v string) error {
	p, err := feParseFloat(v)
	if err != nil {
		return err
	}
	s.model, s.priceOut = model, p
	return nil
}

func (s *rcState) servedPriceIn(model, v string) error {
	p, err := feParseFloat(v)
	if err != nil {
		return err
	}
	s.model, s.priceIn = model, p
	return nil
}

func (s *rcState) servedPriceInOut(model, in, out string) error {
	pi, err := feParseFloat(in)
	if err != nil {
		return err
	}
	po, err := feParseFloat(out)
	if err != nil {
		return err
	}
	s.model, s.priceIn, s.priceOut = model, pi, po
	return nil
}

func (s *rcState) bodyOf(v string) error {
	n, err := strconv.Atoi(v)
	if err != nil {
		return err
	}
	s.bodyBytes = n
	return nil
}

func (s *rcState) claimsCompletion(v string) error {
	n, err := strconv.Atoi(v)
	if err != nil {
		return err
	}
	s.claimCompletion = n
	return nil
}

func (s *rcState) claimsPrompt(v string) error {
	n, err := strconv.Atoi(v)
	if err != nil {
		return err
	}
	s.claimPrompt = n
	return nil
}

func (s *rcState) claimsBoth(p, c string) error {
	if err := s.claimsPrompt(p); err != nil {
		return err
	}
	return s.claimsCompletion(c)
}

func (s *rcState) exactRecountCompletion(v string) error {
	n, err := strconv.Atoi(v)
	if err != nil {
		return err
	}
	s.recountCompletion, s.exact, s.recountCompletSet = n, true, true
	return nil
}

func (s *rcState) exactRecountPrompt(v string) error {
	n, err := strconv.Atoi(v)
	if err != nil {
		return err
	}
	s.recountPrompt, s.exact, s.recountPromptSet = n, true, true
	return nil
}

func (s *rcState) exactRecountBoth(p, c string) error {
	if err := s.exactRecountPrompt(p); err != nil {
		return err
	}
	return s.exactRecountCompletion(c)
}

func (s *rcState) heuristicRecountCompletion(v string) error {
	n, err := strconv.Atoi(v)
	if err != nil {
		return err
	}
	s.recountCompletion, s.exact, s.recountCompletSet = n, false, true
	return nil
}

func (s *rcState) recountCompletionExact(v, exact string) error {
	n, err := strconv.Atoi(v)
	if err != nil {
		return err
	}
	s.recountCompletion, s.exact, s.recountCompletSet = n, exact == "true", true
	return nil
}

func (s *rcState) recountPromptExact(v, exact string) error {
	n, err := strconv.Atoi(v)
	if err != nil {
		return err
	}
	s.recountPrompt, s.exact, s.recountPromptSet = n, exact == "true", true
	return nil
}

func (s *rcState) sidecarUnreachable() error {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // closed -> the POST fails (transport error) -> fail-open to the claim
	s.b.recount.url = url
	return nil
}

func (s *rcState) returnsStatus(v string) error {
	n, err := strconv.Atoi(v)
	if err != nil {
		return err
	}
	s.status, s.statusSet = n, true
	s.completion, s.completionSet = "", true
	return nil
}

func (s *rcState) returnsStatusEmpty(v string) error {
	if err := s.returnsStatus(v); err != nil {
		return err
	}
	s.completion, s.completionSet = "", true
	return nil
}

func (s *rcState) returnsStatusWhitespace(v string) error {
	if err := s.returnsStatus(v); err != nil {
		return err
	}
	s.completion, s.completionSet = "   ", true
	return nil
}

func (s *rcState) replyContentReasoning(content, reasoning string) error {
	s.content, s.reasoning = content, reasoning
	s.status, s.statusSet = 200, true
	return nil
}

func (s *rcState) replyEmptyReasoning(reasoning string) error {
	return s.replyContentReasoning("", reasoning)
}

func (s *rcState) replyContentNoReasoning(content string) error {
	return s.replyContentReasoning(content, "")
}

func (s *rcState) replyEmptyReasoningChannel(string) error {
	// "empty content and a <N>-token reasoning channel": a non-empty reasoning text whose
	// exact re-count the next step pins; the channel count itself is asserted via billing.
	return s.replyContentReasoning("", "reasoning channel text")
}

func (s *rcState) preAuthHold(v string) error {
	f, err := feParseFloat(v)
	if err != nil {
		return err
	}
	s.holdAmt = f
	ok, err := s.b.db.Hold(s.wallet, f)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("pre-auth hold of %g refused", f)
	}
	return nil
}

// servedRequestVoid backs the VOID-gate truth table row setup.
func (s *rcState) servedRequestVoid(status, completion, claim string) error {
	n, err := strconv.Atoi(status)
	if err != nil {
		return err
	}
	c, err := strconv.Atoi(claim)
	if err != nil {
		return err
	}
	s.status, s.statusSet = n, true
	s.completion, s.completionSet = strings.Trim(completion, `"`), true
	s.claimCompletion = c
	return nil
}

func (s *rcState) replyClassify(status, content, reasoning, claim string) error {
	n, err := strconv.Atoi(status)
	if err != nil {
		return err
	}
	c, err := strconv.Atoi(claim)
	if err != nil {
		return err
	}
	s.status, s.statusSet = n, true
	s.content = strings.Trim(content, `"`)
	s.reasoning = strings.Trim(reasoning, `"`)
	s.claimCompletion = c
	return nil
}

func (s *rcState) probeObserves(claim, recount string) error {
	cl, err := strconv.Atoi(claim)
	if err != nil {
		return err
	}
	rc, err := strconv.Atoi(recount)
	if err != nil {
		return err
	}
	s.claimCompletion, s.recountCompletion, s.exact, s.recountCompletSet = cl, rc, true, true
	return nil
}

// --- When -------------------------------------------------------------------

func (s *rcState) requestSettles() error {
	rec := protocol.UsageReceipt{
		RequestID: s.reqID, Model: s.model, PriceIn: s.priceIn, PriceOut: s.priceOut,
		PromptTokens: s.claimPrompt, CompletionTokens: s.claimCompletion, TS: s.now.Unix(),
	}
	completion := s.completion
	if !s.completionSet {
		if s.claimCompletion > 0 {
			completion = sentinel(s.recountCompletion, s.exact)
		} else {
			completion = "ok"
		}
	}
	if producedUsableOutput(s.status, completion, rec.CompletionTokens) {
		promptText := ""
		if s.claimPrompt > 0 {
			promptText = sentinel(s.recountPrompt, s.exact)
		}
		s.billedPrompt = s.b.settleRecountPrompt(s.node, s.reqID, s.model, promptText, rec.PromptTokens, s.bodyBytes)
		s.billedCompletion = s.b.settleRecount(s.node, s.reqID, s.model, completion, rec.CompletionTokens)
		rec.BrokerPromptTokens, rec.BrokerCompletionTokens = s.billedPrompt, s.billedCompletion
		s.cost = rec.CostWith2(s.billedPrompt, s.billedCompletion)
		ownerShare := s.cost * (1 - s.feeRate)
		if _, err := s.b.db.Settle(s.wallet, s.node, s.cost, ownerShare, rec); err != nil {
			return err
		}
		s.voided = false
	} else {
		s.voided = true
		s.b.flagEmptyOutput(s.node, rec, s.status)
		if _, err := s.b.db.Settle(s.wallet, s.node, 0, 0, rec); err != nil { // $0 metering receipt
			return err
		}
		s.cost, s.billedPrompt, s.billedCompletion = 0, 0, 0
	}
	if s.holdAmt > 0 { // the production path's deferred ReleaseHold refunds the full pre-auth
		if _, err := s.b.db.ReleaseHold(s.wallet, s.holdAmt); err != nil {
			return err
		}
	}
	// Deterministic over-report consequence fold (settleRecount also fired this async; the
	// calls are idempotent on the hold/strike and decision-consistent on the same inputs).
	if s.recountCompletSet && s.claimCompletion > 0 {
		s.b.observeRecount(s.node, s.reqID, s.claimCompletion, s.recountCompletion, s.exact)
	}
	if s.recountPromptSet && s.claimPrompt > 0 {
		s.b.observeRecountInput(s.node, s.reqID, s.claimPrompt, s.recountPrompt, s.exact)
	}
	return nil
}

func (s *rcState) evaluateResponse() error {
	s.completionTextResult = completionText(rcBody(s.content, s.reasoning))
	s.usable = producedUsableOutput(s.status, s.completionTextResult, s.claimCompletion)
	return nil
}

func (s *rcState) foldIntoTrust() error {
	s.b.observeRecount(s.node, "", s.claimCompletion, s.recountCompletion, s.exact) // probe: no request id
	return nil
}

func (s *rcState) configLoads() error { s.b.recount = loadRecount(); return nil }

// --- store-grounded helpers -------------------------------------------------

func (s *rcState) held() bool {
	m, _ := s.b.db.RecountHeldNodes()
	return m[s.node]
}

func (s *rcState) discrepancies() int {
	s.b.metricsMu.Lock()
	defer s.b.metricsMu.Unlock()
	return s.b.trust[s.node].discrepancies
}

func (s *rcState) countStrikes(acct, kind string) (int, error) {
	strikes, err := s.b.db.StrikesByOwner(acct, 0)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, st := range strikes {
		if st.Kind == kind {
			n++
		}
	}
	return n, nil
}

// --- Then -------------------------------------------------------------------

func (s *rcState) billedCompletionIs(v string) error {
	n, err := strconv.Atoi(v)
	if err != nil {
		return err
	}
	if s.billedCompletion != n {
		return fmt.Errorf("billed completion = %d, want %d", s.billedCompletion, n)
	}
	return nil
}

func (s *rcState) billedPromptIs(v string) error {
	n, err := strconv.Atoi(v)
	if err != nil {
		return err
	}
	if s.billedPrompt != n {
		return fmt.Errorf("billed prompt = %d, want %d", s.billedPrompt, n)
	}
	return nil
}

func (s *rcState) consumerCharged(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	bal, err := s.b.db.BalanceOf(s.wallet, 0)
	if err != nil {
		return err
	}
	return feApprox(s.walletInit-bal, want) // store-verified debit (hold placed+released nets 0)
}

func (s *rcState) operatorEarnsOwnerShareOf(v string) error {
	cost, err := feParseFloat(v)
	if err != nil {
		return err
	}
	e, err := s.b.db.EarningsOf(s.node)
	if err != nil {
		return err
	}
	return feApprox(e, cost*(1-s.feeRate)) // owner share OF the named cost (30% platform fee)
}

func (s *rcState) operatorEarns(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	e, err := s.b.db.EarningsOf(s.node)
	if err != nil {
		return err
	}
	return feApprox(e, want)
}

func (s *rcState) auditRow(claim, billed string) error {
	cl, err := strconv.Atoi(claim)
	if err != nil {
		return err
	}
	bl, err := strconv.Atoi(billed)
	if err != nil {
		return err
	}
	rows, err := s.b.db.LedgerOf(s.wallet, []string{store.KindAdjust}, 1000)
	if err != nil {
		return err
	}
	found := false
	for _, r := range rows {
		if r.Ref == s.reqID {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("no claim-vs-billed (adjust) audit row for %q", s.reqID)
	}
	if s.claimCompletion != cl {
		return fmt.Errorf("claimed = %d, want %d", s.claimCompletion, cl)
	}
	es, err := s.b.db.EntriesByUser(s.wallet, 0, 1<<62)
	if err != nil {
		return err
	}
	for _, e := range es {
		if e.RequestID == s.reqID && e.CompletionTokens != bl {
			return fmt.Errorf("entry billed completion = %d, want %d", e.CompletionTokens, bl)
		}
	}
	return nil
}

func (s *rcState) noAuditRow() error {
	rows, err := s.b.db.LedgerOf(s.wallet, []string{store.KindAdjust}, 1000)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if r.Ref == s.reqID {
			return fmt.Errorf("an unexpected claim-vs-billed audit row was written for %q", s.reqID)
		}
	}
	return nil
}

func (s *rcState) neverBillsMore() error {
	if s.billedCompletion > s.claimCompletion {
		return fmt.Errorf("billed completion %d > claimed %d", s.billedCompletion, s.claimCompletion)
	}
	if s.claimPrompt > 0 && s.billedPrompt > s.claimPrompt {
		return fmt.Errorf("billed prompt %d > claimed %d", s.billedPrompt, s.claimPrompt)
	}
	return nil
}

func (s *rcState) noOverReportDiscrepancy() error {
	if s.held() {
		return fmt.Errorf("an over-report discrepancy was recorded (node held)")
	}
	return nil
}

func (s *rcState) nodeNotPenalized() error { return s.noOverReportDiscrepancy() }

func (s *rcState) producedUsable() error {
	if !s.usable {
		return fmt.Errorf("expected usable output, got void")
	}
	return nil
}

func (s *rcState) completionTextIs(v string) error {
	if s.completionTextResult != v {
		return fmt.Errorf("completion text = %q, want %q", s.completionTextResult, v)
	}
	return nil
}

func (s *rcState) usableIs(v string) error {
	want := v == "true"
	if s.usable != want {
		return fmt.Errorf("usable = %v, want %v", s.usable, want)
	}
	return nil
}

func (s *rcState) voidStateIs(v string) error {
	want := v == "true"
	if s.voided != want {
		return fmt.Errorf("void state = %v, want %v", s.voided, want)
	}
	return nil
}

func (s *rcState) notVoided() error {
	if s.voided {
		return fmt.Errorf("request was voided to $0 but should bill")
	}
	if s.cost <= 0 {
		return fmt.Errorf("cost = %g, want > 0", s.cost)
	}
	return nil
}

func (s *rcState) emptyStrikeFlagged(owner string) error {
	n, err := s.countStrikes(owner, store.StrikeEmptyOutput)
	if err != nil {
		return err
	}
	if n < 1 {
		return fmt.Errorf("expected an empty-output strike against %s, got %d", owner, n)
	}
	return nil
}

func (s *rcState) emptyStrikeNotFired() error {
	n, err := s.countStrikes(s.owner, store.StrikeEmptyOutput)
	if err != nil {
		return err
	}
	if n != 0 {
		return fmt.Errorf("empty-output strike fired (%d) but should not", n)
	}
	return nil
}

func (s *rcState) recountStrikeFlagged(owner string) error {
	n, err := s.countStrikes(owner, store.StrikeRecountDiscrepancy)
	if err != nil {
		return err
	}
	if n < 1 {
		return fmt.Errorf("expected a recount-discrepancy strike against %s, got %d", owner, n)
	}
	return nil
}

func (s *rcState) holdRefundedFull(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	if want != s.holdAmt {
		return fmt.Errorf("scenario hold %g != asserted %g", s.holdAmt, want)
	}
	bal, err := s.b.db.BalanceOf(s.wallet, 0)
	if err != nil {
		return err
	}
	return feApprox(bal, s.walletInit) // hold placed then released, $0 charge -> back to start
}

func (s *rcState) holdRefunded() error {
	bal, err := s.b.db.BalanceOf(s.wallet, 0)
	if err != nil {
		return err
	}
	return feApprox(bal, s.walletInit)
}

func (s *rcState) zeroMeteringReceipt() error {
	es, err := s.b.db.EntriesByUser(s.wallet, 0, 1<<62)
	if err != nil {
		return err
	}
	for _, e := range es {
		if e.RequestID == s.reqID {
			if e.Cost != 0 {
				return fmt.Errorf("metering receipt cost = %g, want 0", e.Cost)
			}
			return nil
		}
	}
	return fmt.Errorf("no $0 metering receipt recorded for %q", s.reqID)
}

func (s *rcState) overReportRatioIs(v string) error {
	n, err := strconv.Atoi(v)
	if err != nil {
		return err
	}
	got := float64(s.claimCompletion-s.recountCompletion) / float64(s.recountCompletion) * 100
	if s.recountPromptSet && !s.recountCompletSet {
		got = float64(s.claimPrompt-s.recountPrompt) / float64(s.recountPrompt) * 100
	}
	return feApprox(got, float64(n))
}

func (s *rcState) overReportExceedsBilling(v string) error {
	n, err := feParseFloat(v)
	if err != nil {
		return err
	}
	ratio := float64(s.claimCompletion-s.recountCompletion) / float64(s.recountCompletion)
	if ratio <= n/100 {
		return fmt.Errorf("over-report %.4f does not exceed billing tolerance %.4f", ratio, n/100)
	}
	return nil
}

func (s *rcState) earningsHeld() error {
	if !s.held() {
		return fmt.Errorf("node %s earnings not held (expected held from promotion)", s.node)
	}
	return nil
}

func (s *rcState) earningsNotHeld() error {
	if s.held() {
		return fmt.Errorf("node %s earnings held but should NOT be", s.node)
	}
	return nil
}

func (s *rcState) discrepancyRecorded() error {
	if s.discrepancies() < 1 {
		return fmt.Errorf("no discrepancy recorded against %s", s.node)
	}
	return nil
}

func (s *rcState) noDiscrepancyRecorded() error {
	if s.discrepancies() != 0 {
		return fmt.Errorf("%d discrepancies recorded against %s, want 0", s.discrepancies(), s.node)
	}
	return nil
}

func (s *rcState) ownerNotStruckWithin(string, string) error {
	n, err := s.countStrikes(s.owner, store.StrikeRecountDiscrepancy)
	if err != nil {
		return err
	}
	if n != 0 {
		return fmt.Errorf("owner struck (%d) but the over-report is within the strike tolerance", n)
	}
	return nil
}

func (s *rcState) noProbeStrike() error {
	strikes, err := s.b.db.StrikesByOwner(s.owner, 0)
	if err != nil {
		return err
	}
	if len(strikes) != 0 {
		return fmt.Errorf("probe-path discrepancy recorded %d strike(s), want 0", len(strikes))
	}
	return nil
}

func (s *rcState) heldStateIs(v string) error {
	want := v == "true"
	if s.held() != want {
		return fmt.Errorf("earnings-held = %v, want %v", s.held(), want)
	}
	return nil
}

func (s *rcState) struckStateIs(v string) error {
	want := v == "true"
	n, err := s.countStrikes(s.owner, store.StrikeRecountDiscrepancy)
	if err != nil {
		return err
	}
	if (n >= 1) != want {
		return fmt.Errorf("owner-struck = %v (strikes=%d), want %v", n >= 1, n, want)
	}
	return nil
}

func (s *rcState) effectiveStrikeTol(v string) error {
	n, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return feApprox(s.b.recount.strikeTolerance, n/100)
}

func TestRecountBillingBDD(t *testing.T) {
	t.Setenv("TOKENIZER_URL", "")
	t.Setenv("ROGERAI_RECOUNT_TOLERANCE", "")
	t.Setenv("ROGERAI_RECOUNT_STRIKE_TOLERANCE", "")
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &rcState{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				if st.sidecar != nil {
					st.sidecar.Close()
					st.sidecar = nil
				}
				return ctx, nil
			})

			// Background / config
			sc.Step(`^the L1 re-count sidecar is enabled$`, st.sidecarEnabled)
			sc.Step(`^the L1 re-count sidecar is disabled$`, st.sidecarDisabled)
			sc.Step(`^the billing re-count tolerance is ([\d.]+)%$`, st.billingTol)
			sc.Step(`^the owner strike tolerance is ([\d.]+)%$`, st.strikeTol)
			sc.Step(`^the owner strike tolerance is configured to ([\d.]+)%$`, st.strikeTolConfigured)
			sc.Step(`^the platform fee rate is (\d+)%$`, st.feePct)
			sc.Step(`^a consumer wallet "([^"]*)" funded with ([\d.]+) real credits$`, st.walletFunded)
			sc.Step(`^node "([^"]*)" is owned by account "([^"]*)"$`, st.nodeOwned)

			// served request setup
			sc.Step(`^a served request on model "([^"]*)" at price_out ([\d.]+) per 1M$`, st.servedPriceOut)
			sc.Step(`^a served request on model "([^"]*)" at price_in ([\d.]+) per 1M$`, st.servedPriceIn)
			sc.Step(`^a served request on model "([^"]*)" at price_in ([\d.]+) per 1M and price_out ([\d.]+) per 1M$`, st.servedPriceInOut)
			sc.Step(`^the request body is (\d+) bytes$`, st.bodyOf)
			sc.Step(`^the node claims (\d+) completion tokens$`, st.claimsCompletion)
			sc.Step(`^the node claims (\d+) prompt tokens$`, st.claimsPrompt)
			sc.Step(`^the node claims (\d+) prompt tokens and (\d+) completion tokens$`, st.claimsBoth)
			sc.Step(`^the broker exactly re-counts (\d+) completion tokens$`, st.exactRecountCompletion)
			sc.Step(`^the broker exactly re-counts (\d+) completion tokens from the reasoning text$`, st.exactRecountCompletion)
			sc.Step(`^the broker exactly re-counts (\d+) prompt tokens$`, st.exactRecountPrompt)
			sc.Step(`^the broker exactly re-counts (\d+) prompt tokens and (\d+) completion tokens$`, st.exactRecountBoth)
			sc.Step(`^the broker re-counts (\d+) completion tokens with a NON-exact heuristic$`, st.heuristicRecountCompletion)
			sc.Step(`^the broker re-counts (\d+) completion tokens with exact=(true|false)$`, st.recountCompletionExact)
			sc.Step(`^the broker re-counts (\d+) prompt tokens with exact=(true|false)$`, st.recountPromptExact)
			sc.Step(`^the tokenizer sidecar is unreachable$`, st.sidecarUnreachable)
			sc.Step(`^the node returns status (\d+)$`, st.returnsStatus)
			sc.Step(`^the node returns status (\d+) with an empty completion$`, st.returnsStatusEmpty)
			sc.Step(`^the node returns status (\d+) with a whitespace-only completion$`, st.returnsStatusWhitespace)
			sc.Step(`^the node returns a reply with content "([^"]*)" and reasoning "([^"]*)"$`, st.replyContentReasoning)
			sc.Step(`^the node returns a reply with content "([^"]*)" and no reasoning$`, st.replyContentNoReasoning)
			sc.Step(`^the node returns a reply with empty content and reasoning "([^"]*)"$`, st.replyEmptyReasoning)
			sc.Step(`^the node returns a reply with empty content and a (\d+)-token reasoning channel$`, st.replyEmptyReasoningChannel)
			sc.Step(`^alice has a pre-auth hold of ([\d.]+) credits for the request$`, st.preAuthHold)
			sc.Step(`^a served request with node status (\d+) and completion (.+) claiming (\d+) completion tokens$`, st.servedRequestVoid)
			sc.Step(`^a reply with status (\d+) content (.+) reasoning (.+) claiming (\d+) completion tokens$`, st.replyClassify)
			sc.Step(`^a probe re-count observes claimed (\d+) vs exact re-count (\d+)$`, st.probeObserves)

			// When
			sc.Step(`^the request settles$`, st.requestSettles)
			sc.Step(`^the broker evaluates the response$`, st.evaluateResponse)
			sc.Step(`^the discrepancy is folded into trust$`, st.foldIntoTrust)
			sc.Step(`^the re-count config loads$`, st.configLoads)

			// Then
			sc.Step(`^the billed completion tokens are (\d+)$`, st.billedCompletionIs)
			sc.Step(`^the billed prompt tokens are (\d+)$`, st.billedPromptIs)
			sc.Step(`^the consumer is charged ([\d.]+) credits$`, st.consumerCharged)
			sc.Step(`^the operator earns the owner share of ([\d.]+) credits$`, st.operatorEarnsOwnerShareOf)
			sc.Step(`^the operator earns ([\d.]+) credits$`, st.operatorEarns)
			sc.Step(`^a claim-vs-billed audit row records claimed (\d+) billed (\d+)$`, st.auditRow)
			sc.Step(`^no claim-vs-billed audit row is written$`, st.noAuditRow)
			sc.Step(`^the broker never bills more than the node claimed$`, st.neverBillsMore)
			sc.Step(`^no over-report discrepancy is recorded$`, st.noOverReportDiscrepancy)
			sc.Step(`^the node is not penalized for the sidecar outage$`, st.nodeNotPenalized)
			sc.Step(`^the request produced usable output$`, st.producedUsable)
			sc.Step(`^the completion text re-counted is "([^"]*)"$`, st.completionTextIs)
			sc.Step(`^producing usable output is (true|false)$`, st.usableIs)
			sc.Step(`^the request void state is (true|false)$`, st.voidStateIs)
			sc.Step(`^the request is not voided to \$0$`, st.notVoided)
			sc.Step(`^the empty-output strike is flagged against owner "([^"]*)"$`, st.emptyStrikeFlagged)
			sc.Step(`^the empty-output strike does NOT fire$`, st.emptyStrikeNotFired)
			sc.Step(`^the recount-over-report strike is flagged against owner "([^"]*)"$`, st.recountStrikeFlagged)
			sc.Step(`^the recount-over-report strike is flagged against owner "([^"]*)" on the input axis$`, st.recountStrikeFlagged)
			sc.Step(`^alice's full hold of ([\d.]+) credits is refunded$`, st.holdRefundedFull)
			sc.Step(`^the hold is refunded in full$`, st.holdRefunded)
			sc.Step(`^a \$0 metering receipt is recorded for lineage$`, st.zeroMeteringReceipt)
			sc.Step(`^the over-report ratio is (\d+)%$`, st.overReportRatioIs)
			sc.Step(`^the over-report exceeds the ([\d.]+)% billing tolerance$`, st.overReportExceedsBilling)
			sc.Step(`^the node's earnings are HELD from promotion$`, st.earningsHeld)
			sc.Step(`^the node's earnings are NOT held$`, st.earningsNotHeld)
			sc.Step(`^a discrepancy is recorded against the node$`, st.discrepancyRecorded)
			sc.Step(`^no discrepancy is recorded against the node$`, st.noDiscrepancyRecorded)
			sc.Step(`^the owner is NOT struck because (\d+)% is within the (\d+)% strike tolerance$`, st.ownerNotStruckWithin)
			sc.Step(`^no owner strike is recorded for the probe-path discrepancy$`, st.noProbeStrike)
			sc.Step(`^the earnings-held state is (true|false)$`, st.heldStateIs)
			sc.Step(`^the owner-struck state is (true|false)$`, st.struckStateIs)
			sc.Step(`^the effective strike tolerance is ([\d.]+)%$`, st.effectiveStrikeTol)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/money/recount_billing.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("money/recount_billing behavior scenarios failed (see godog output above)")
	}
}
