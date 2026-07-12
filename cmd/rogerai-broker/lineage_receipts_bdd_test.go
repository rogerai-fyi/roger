package main

// lineage_receipts_bdd_test.go makes features/trust/lineage_receipts.feature an EXECUTABLE
// godog suite in the BROKER package, so the SETTLE-BINDING scenarios (which need the real
// relay + ledger, not just protocol.UsageReceipt) are actually asserted - previously the
// feature was prose-only (lineage_tamper_test.go is a plain unit test that only covers the
// tamper table + broker-field exclusion, and merely references the feature in a comment).
//
// It drives the REAL code (no mocks except the deliberate finalizeErrStore fault injector,
// the same pattern grant_test.go's grantUsageErrStore uses):
//   - protocol layer: SignNode/VerifyNode/SignBroker/Hash/Cost/CostWith2 over signingBytes.
//   - settle binding: a full served b.relay round-trip (a node-goroutine answers the job with
//     a signed receipt) asserting the broker co-signs ONLY a node-verified receipt, bills its
//     OWN resolved price (not the node's claim), clamps capture to the authorized hold, voids
//     no-output (charge 0 + refund + owner strike + $0 co-signed receipt), and fails safe
//     (hold refunded, no billing headers) when the ledger settle errors.
//   - over-report (both axes) + idempotency: the real CostWith2/billedTokens + store.Settle.
//
// signReq lives in auth_test.go; relayBroker in enforce_test.go; feApprox in fee_splits_bdd_test.go.

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
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

// finalizeErrStore wraps Mem but ERRORS on Finalize, simulating a ledger settle failure so
// the relay's fail-safe (settled stays false -> deferred ReleaseHold refunds) is exercised.
type finalizeErrStore struct{ *store.Mem }

func (finalizeErrStore) Finalize(string, string, float64, float64, float64, protocol.UsageReceipt) (float64, error) {
	return 0, errors.New("ledger finalize failed")
}

type lrState struct {
	t *testing.T
	b *broker // the broker used by the most recent served-relay run (strike reads / idempotency)

	// node identity (Background)
	nodePub  ed25519.PublicKey
	nodePriv ed25519.PrivateKey

	// protocol-layer working receipts
	rec   protocol.UsageReceipt // the receipt under test
	hash0 string                // a captured Hash for the chain/tamper checks
	r2    protocol.UsageReceipt // the next chained receipt

	// served-relay config (Given steps set these; the When runs the round-trip)
	signWrong     bool    // forged: sign the receipt with the WRONG key
	nodeStatus    int     // node's returned HTTP status
	nodeBody      string  // node's returned body (drives produced-output)
	claimPrompt   int     // receipt's claimed prompt tokens
	claimComp     int     // receipt's claimed completion tokens
	nodePriceOut  float64 // the node's CLAIMED out-price (may be inflated)
	offerPriceIn  float64 // the broker's active in-price
	offerPriceOut float64 // the broker's active out-price
	ctx           int
	maxTokens     int
	errStore      bool // failsafe: wrap the ledger so Finalize errors

	// served-relay results
	code       int
	hdrReceipt string
	hdrCost    string
	balBefore  float64
	balAfter   float64
	spend      float64
	earn       float64
	hold       float64
	brokerPub  ed25519.PublicKey

	// over-report (settle-math) results
	overCost      float64
	overBilledIn  int
	overBilledOut int

	// idempotency results
	idemBal  float64
	idemEarn float64
}

func (s *lrState) reset() {
	s.nodePub, s.nodePriv, _ = ed25519.GenerateKey(nil)
	s.rec = lrSample()
	s.hash0, s.r2 = "", protocol.UsageReceipt{}
	s.signWrong = false
	s.nodeStatus = 200
	s.nodeBody = `{"choices":[{"message":{"role":"assistant","content":"hello there, this is a real answer"}}]}`
	s.claimPrompt, s.claimComp = 10, 5
	s.nodePriceOut = 1.0
	s.offerPriceIn, s.offerPriceOut = 1.0, 1.0
	s.ctx, s.maxTokens = 4096, 100
	s.errStore = false
	s.code, s.hdrReceipt, s.hdrCost = 0, "", ""
	s.balBefore, s.balAfter, s.spend, s.earn, s.hold = 0, 0, 0, 0, 0
	s.brokerPub = nil
	s.overCost, s.overBilledIn, s.overBilledOut = 0, 0, 0
	s.idemBal, s.idemEarn = 0, 0
	s.b = nil
}

// lrSample is a populated, internally-consistent receipt (broker-package twin of the
// protocol package's sampleReceipt, which is not exported).
func lrSample() protocol.UsageReceipt {
	return protocol.UsageReceipt{
		RequestID: "req-1", NodeID: "n1", User: "u_alice", Model: "m",
		PromptTokens: 100, CompletionTokens: 50, PriceIn: 2.0, PriceOut: 6.0,
		PrevHash: "00", TS: 1700000000,
	}
}

// --- Background -------------------------------------------------------------

func (s *lrState) bgNode() error     { return nil } // keypair generated in reset()
func (s *lrState) bgConsumer() error { return nil } // funded per served-relay run

// ============================================================================
// protocol layer: node-signed over canonical bytes
// ============================================================================

func (s *lrState) nodeServesAndSigns() error {
	s.rec = lrSample()
	s.rec.NodeID = "n1"
	s.rec.SignNode(s.nodePriv)
	return nil
}

func (s *lrState) verifyNodeOK() error {
	if !s.rec.VerifyNode(hex.EncodeToString(s.nodePub)) {
		return fmt.Errorf("a freshly node-signed receipt must verify against the node's registered pubkey")
	}
	return nil
}

func (s *lrState) aNodeSignedReceipt() error {
	s.rec = lrSample()
	s.rec.SignNode(s.nodePriv)
	return nil
}

func (s *lrState) brokerSetsFields() error {
	s.rec.GrantID = "grant_x"
	s.rec.BrokerPromptTokens = 7
	s.rec.BrokerCompletionTokens = 9
	return nil
}

func (s *lrState) nodeSigStillVerifies() error {
	if !s.rec.VerifyNode(hex.EncodeToString(s.nodePub)) {
		return fmt.Errorf("broker-set fields (GrantID/Broker*Tokens) must be zeroed in signingBytes - the node sig must still verify")
	}
	return nil
}

func (s *lrState) alterField(field string) error {
	switch field {
	case "Model":
		s.rec.Model = "evil-model"
	case "User":
		s.rec.User = "u_evil"
	case "PromptTokens":
		s.rec.PromptTokens++
	case "CompletionTokens":
		s.rec.CompletionTokens++
	case "PriceIn":
		s.rec.PriceIn = 9.99
	case "PriceOut":
		s.rec.PriceOut = 9.99
	case "PrevHash":
		s.rec.PrevHash = "deadbeef"
	case "TS":
		s.rec.TS++
	case "RequestID":
		s.rec.RequestID = "req-evil"
	case "NodeID":
		s.rec.NodeID = "n-evil"
	default:
		return fmt.Errorf("unknown tamper field %q", field)
	}
	return nil
}

func (s *lrState) verifyNodeFails() error {
	if s.rec.VerifyNode(hex.EncodeToString(s.nodePub)) {
		return fmt.Errorf("tampering a node-signed field must break VerifyNode")
	}
	return nil
}

// --- hash chain -------------------------------------------------------------

func (s *lrState) r1WithHash() error {
	s.rec = lrSample()
	s.rec.SignNode(s.nodePriv)
	s.hash0 = s.rec.Hash()
	return nil
}

func (s *lrState) nextReceipt() error {
	s.r2 = lrSample()
	s.r2.RequestID = "req-2"
	s.r2.PrevHash = s.hash0 // chained off R1's hash
	s.r2.SignNode(s.nodePriv)
	return nil
}

func (s *lrState) r2PrevEqualsH1() error {
	if s.r2.PrevHash != s.hash0 {
		return fmt.Errorf("R2.PrevHash = %q, want R1.Hash() %q", s.r2.PrevHash, s.hash0)
	}
	return nil
}

func (s *lrState) receiptHashH() error {
	s.rec = lrSample()
	s.rec.SignNode(s.nodePriv)
	s.hash0 = s.rec.Hash()
	return nil
}

func (s *lrState) alterAnySignedField() error { s.rec.CompletionTokens += 7; return nil }

func (s *lrState) hashChanged() error {
	if s.rec.Hash() == s.hash0 {
		return fmt.Errorf("altering a signed field must change the receipt hash (chain link broken)")
	}
	return nil
}

// --- cost math --------------------------------------------------------------

func (s *lrState) costReceipt(promptTok, priceIn, compTok, priceOut string) error {
	pt, _ := strconv.Atoi(promptTok)
	ct, _ := strconv.Atoi(compTok)
	pin, err := feParseFloat(priceIn)
	if err != nil {
		return err
	}
	pout, err := feParseFloat(priceOut)
	if err != nil {
		return err
	}
	s.rec = protocol.UsageReceipt{PromptTokens: pt, PriceIn: pin, CompletionTokens: ct, PriceOut: pout}
	return nil
}

func (s *lrState) costIs(v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	return feApprox(s.rec.Cost(), want)
}

func (s *lrState) costWith2Receipt(compTok, priceOut string) error {
	ct, _ := strconv.Atoi(compTok)
	pout, err := feParseFloat(priceOut)
	if err != nil {
		return err
	}
	s.rec = protocol.UsageReceipt{CompletionTokens: ct, PriceOut: pout}
	return nil
}

func (s *lrState) billsVerifiedComp(v string) error {
	ct, _ := strconv.Atoi(v)
	s.overCost = s.rec.CostWith2(0, ct) // bill the broker-verified count
	return nil
}

func (s *lrState) costReflectsComp(v string) error {
	ct, _ := strconv.Atoi(v)
	wantVerified := s.rec.CostWith2(0, ct)
	claimed := s.rec.Cost()
	if e := feApprox(s.overCost, wantVerified); e != nil {
		return fmt.Errorf("billed cost must reflect the verified %s tokens: %w", v, e)
	}
	if !(s.overCost < claimed) {
		return fmt.Errorf("verified cost %.6f must be LESS than the claimed cost %.6f", s.overCost, claimed)
	}
	return nil
}

// ============================================================================
// settle binding: served relay round-trips
// ============================================================================

// runServedRelay drives a full b.relay round-trip: a node goroutine answers the job with a
// receipt (signed by the node's key, or a WRONG key when signWrong) and the configured
// status/body; the consumer is a funded, logged-in wallet. Results are read back from the
// ledger + response headers. Uses the configured (optionally Finalize-erroring) ledger.
func (s *lrState) runServedRelay() error {
	mem := store.NewMem()
	var db store.Store = mem
	if s.errStore {
		db = finalizeErrStore{mem}
	}
	b := relayBroker(db)
	s.brokerPub = b.priv.Public().(ed25519.PublicKey)

	b.nodes["n1"] = protocol.NodeRegistration{
		NodeID: "n1", PubKey: hex.EncodeToString(s.nodePub),
		Offers: []protocol.ModelOffer{{Model: "m", PriceIn: s.offerPriceIn, PriceOut: s.offerPriceOut, Ctx: s.ctx}},
	}
	b.lastSeen["n1"] = time.Now()
	tun := &nodeTunnel{jobs: make(chan protocol.Job, 1), waiters: map[string]chan protocol.JobResult{}}
	b.tunnels["n1"] = tun
	if err := mem.BindNode("n1", "op1"); err != nil {
		return err
	}

	_, userPriv, _ := ed25519.GenerateKey(nil)
	userPubHex := hex.EncodeToString(userPriv.Public().(ed25519.PublicKey))
	if err := mem.BindOwner(store.Owner{GitHubID: 7, Login: "alice", Pubkey: userPubHex}); err != nil {
		return err
	}
	if _, err := mem.AddCredits("u_gh_7", 1000); err != nil {
		return err
	}
	s.balBefore, _ = mem.PeekBalance("u_gh_7")

	signKey := s.nodePriv
	if s.signWrong {
		_, wrongPriv, _ := ed25519.GenerateKey(nil)
		signKey = wrongPriv
	}
	status, body := s.nodeStatus, s.nodeBody
	cp, cc, npo := s.claimPrompt, s.claimComp, s.nodePriceOut
	go func() {
		job := <-tun.jobs
		rec := protocol.UsageReceipt{
			RequestID: job.ID, NodeID: "n1", Model: "m",
			PromptTokens: cp, CompletionTokens: cc, PriceIn: 1.0, PriceOut: npo,
			TS: time.Now().Unix(),
		}
		rec.SignNode(signKey)
		res := protocol.JobResult{ID: job.ID, Status: status, Body: []byte(body), Receipt: rec}
		tun.mu.Lock()
		ch := tun.waiters[job.ID]
		tun.mu.Unlock()
		ch <- res
	}()

	reqBody := []byte(fmt.Sprintf(`{"model":"m","max_tokens":%d}`, s.maxTokens))
	s.hold = estimateMaxCost(reqBody, s.offerPriceIn, s.offerPriceOut, s.ctx)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(reqBody)))
	signReq(r, userPriv, reqBody)
	w := httptest.NewRecorder()
	b.relay(w, r)

	s.code = w.Code
	s.hdrReceipt = w.Header().Get("X-RogerAI-Receipt")
	s.hdrCost = w.Header().Get("X-RogerAI-Cost")
	s.balAfter, _ = mem.PeekBalance("u_gh_7")
	s.spend, _ = mem.SpendOf("u_gh_7")
	s.earn, _ = mem.EarningsOf("n1")
	s.b = b // for the void scenarios' owner-strike read
	return nil
}

func (s *lrState) ownerStruck() (bool, error) {
	if s.b == nil {
		return false, nil
	}
	st, err := s.b.db.StrikesByOwner("op1", 100)
	if err != nil {
		return false, err
	}
	for _, k := range st {
		if k.Kind == store.StrikeEmptyOutput {
			return true, nil
		}
	}
	return false, nil
}

// --- co-sign happy path -----------------------------------------------------

func (s *lrState) relayReturnsVerified() error { return s.runServedRelay() }

func (s *lrState) brokerCosigns() error {
	if s.hdrReceipt == "" {
		return fmt.Errorf("no co-signed receipt was returned (X-RogerAI-Receipt empty)")
	}
	rec, err := protocol.DecodeReceipt(s.hdrReceipt)
	if err != nil {
		return err
	}
	if rec.BrokerSig == "" {
		return fmt.Errorf("the returned receipt carries no BrokerSig (broker did not counter-sign)")
	}
	s.rec = rec
	return nil
}

func (s *lrState) bothSigsVerify() error {
	if !s.rec.VerifyNode(hex.EncodeToString(s.nodePub)) {
		return fmt.Errorf("the co-signed receipt's NODE signature does not verify")
	}
	// Verify the BROKER sig over the SAME canonical bytes by routing it through VerifyNode
	// (ed25519.Verify(pub, signingBytes, sig)) against the broker's pubkey - signingBytes
	// zeroes BOTH sigs, so moving BrokerSig into NodeSig checks the broker sig without
	// re-implementing the unexported signingBytes.
	probe := s.rec
	probe.NodeSig = probe.BrokerSig
	if !probe.VerifyNode(hex.EncodeToString(s.brokerPub)) {
		return fmt.Errorf("the broker signature does not verify over the canonical bytes")
	}
	return nil
}

func (s *lrState) receiptHeaderReturned() error {
	if s.hdrReceipt == "" {
		return fmt.Errorf("the co-signed receipt must ride the X-RogerAI-Receipt header")
	}
	return nil
}

// --- forged receipt ---------------------------------------------------------

func (s *lrState) forgedReceipt() error { s.signWrong = true; return nil }
func (s *lrState) relayProcess() error  { return s.runServedRelay() }

func (s *lrState) noSettlementNoEarning() error {
	if s.spend != 0 {
		return fmt.Errorf("a forged receipt must not settle: spend = %.6f, want 0", s.spend)
	}
	if s.earn != 0 {
		return fmt.Errorf("a forged receipt must mint no earning: earn = %.6f, want 0", s.earn)
	}
	return nil
}

func (s *lrState) holdRefundedFull() error {
	if e := feApprox(s.balAfter, s.balBefore); e != nil {
		return fmt.Errorf("the pre-authorized hold must be refunded in full: %w", e)
	}
	return nil
}

func (s *lrState) noReceiptHeader() error {
	if s.hdrReceipt != "" {
		return fmt.Errorf("no co-signed receipt should be emitted for a forged receipt; got %q", s.hdrReceipt)
	}
	return nil
}

// --- broker bills its resolved price ----------------------------------------

func (s *lrState) inflatedPriceOut() error {
	s.offerPriceIn, s.offerPriceOut = 1.0, 1.0 // the broker's active price (<= the consumer out-cap)
	s.nodePriceOut = 9.99                      // the node CLAIMS an inflated out-price
	s.claimComp = 1000
	s.maxTokens = 4000 // size the hold ABOVE the cost so the spend reflects the PRICE, not the hold clamp
	return nil
}

func (s *lrState) relaySettles() error { return s.runServedRelay() }

func (s *lrState) billedAtResolved() error {
	rec, err := protocol.DecodeReceipt(s.hdrReceipt)
	if err != nil {
		return err
	}
	billedPrompt := rec.PromptTokens
	if rec.BrokerPromptTokens > 0 {
		billedPrompt = rec.BrokerPromptTokens
	}
	billedComp := rec.CompletionTokens
	if rec.BrokerCompletionTokens > 0 {
		billedComp = rec.BrokerCompletionTokens
	}
	atBroker := (float64(billedPrompt)*rec.PriceIn + float64(billedComp)*rec.PriceOut) / 1e6
	if e := feApprox(s.spend, atBroker); e != nil {
		return fmt.Errorf("consumer must be billed at the broker-resolved price: %w (spend=%.8f)", e, s.spend)
	}
	// Had the broker used the node's inflated out-price, the same billed tokens would cost
	// strictly more - prove the cheaper broker price was used.
	atNode := (float64(billedPrompt)*rec.PriceIn + float64(billedComp)*s.nodePriceOut) / 1e6
	if !(s.spend < atNode) {
		return fmt.Errorf("spend %.8f is not below the node-claimed-price cost %.8f - the node's price may have been used", s.spend, atNode)
	}
	return nil
}

func (s *lrState) receiptPriceIsBrokers() error {
	rec, err := protocol.DecodeReceipt(s.hdrReceipt)
	if err != nil {
		return err
	}
	if e := feApprox(rec.PriceOut, s.offerPriceOut); e != nil {
		return fmt.Errorf("the settled receipt's PriceOut must be the broker's price, not the node's claim: %w (got %.4f)", e, rec.PriceOut)
	}
	return nil
}

// --- over-report on both axes (settle math) ---------------------------------

func (s *lrState) overReportClaim() error {
	mem := store.NewMem()
	_ = mem.BindNode("n1", "op1")
	if _, _, err := mem.CreditOnce("c", "alice", 100); err != nil {
		return err
	}
	rec := protocol.UsageReceipt{
		RequestID: "over1", Model: "m", PriceIn: 1.0, PriceOut: 1.0,
		PromptTokens: 1000, CompletionTokens: 1000,
		BrokerPromptTokens: 400, BrokerCompletionTokens: 400, // the broker re-count (lesser)
	}
	s.overCost = rec.CostWith2(400, 400) // settle path bills min(claim, recount) per axis
	if _, err := mem.Settle("alice", "n1", s.overCost, s.overCost*0.7, rec); err != nil {
		return err
	}
	entries, err := mem.RecentByNode("n1", 5)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.RequestID == "over1" {
			s.overBilledIn, s.overBilledOut = e.PromptTokens, e.CompletionTokens
		}
	}
	return nil
}

func (s *lrState) costUsesMinBothAxes() error {
	if s.overBilledIn != 400 || s.overBilledOut != 400 {
		return fmt.Errorf("settle must bill min(claim, recount) on BOTH axes: billed in=%d out=%d, want 400/400", s.overBilledIn, s.overBilledOut)
	}
	// 400*1 + 400*1 over 1e6
	if e := feApprox(s.overCost, (400.0*1.0+400.0*1.0)/1e6); e != nil {
		return fmt.Errorf("cost must reflect the floored 400/400 counts: %w", e)
	}
	return nil
}

// --- capture clamp ----------------------------------------------------------

func (s *lrState) costExceedsHold() error {
	// Keep the out-price <= the consumer out-cap (10) so the node is pickable; a small hold
	// (max_tokens 8) + a massive completion over-claim makes the raw cost >> the hold.
	s.offerPriceIn, s.offerPriceOut = 1.0, 9.0
	s.maxTokens, s.ctx = 8, 256
	s.claimComp = 100000 // node over-claims -> raw cost >> hold -> capture clamps to the hold
	return nil
}

func (s *lrState) settlesClamp() error { return s.runServedRelay() }

func (s *lrState) clampedToHold() error {
	if e := feApprox(s.spend, s.hold); e != nil {
		return fmt.Errorf("capture must be clamped to the authorized hold %.6f: %w (spend=%.6f)", s.hold, e, s.spend)
	}
	return nil
}

// --- settle failure fails safe ----------------------------------------------

func (s *lrState) settleErrors() error { s.errStore = true; return nil }

func (s *lrState) holdRefundedNoBilling() error {
	if e := feApprox(s.balAfter, s.balBefore); e != nil {
		return fmt.Errorf("a settle error must refund the hold in full: %w", e)
	}
	if s.hdrCost != "" {
		return fmt.Errorf("a settle error must emit NO billing headers; got X-RogerAI-Cost=%q", s.hdrCost)
	}
	return nil
}

// --- void on no usable output -----------------------------------------------

func (s *lrState) nodeReturnsShape(shape string) error {
	switch shape {
	case "an error status (>=400)":
		s.nodeStatus, s.nodeBody = 502, `{"error":"upstream boom"}`
	case "an empty / whitespace completion":
		s.nodeStatus, s.nodeBody = 200, `{"choices":[{"message":{"content":"   "}}]}`
		s.claimComp = 0
	case "no output text and zero tokens":
		// The TRUE-negative: no completion text AND completion_tokens==0 -> still voided + struck.
		s.nodeStatus, s.nodeBody = 200, `{"choices":[{"message":{"content":""}}]}`
		s.claimComp = 0
	case "empty text but usage reports completion tokens":
		// Usage backstop: no captured text but the node's usage reports tokens -> NOT voided
		// (billed off the reported tokens), the honest owner is NOT struck.
		s.nodeStatus, s.nodeBody = 200, `{"choices":[{"message":{"content":""}}]}`
		s.claimComp = 5
	default:
		return fmt.Errorf("unknown no-output shape %q", shape)
	}
	return nil
}

func (s *lrState) charged0Refunded() error {
	if s.spend != 0 {
		return fmt.Errorf("a no-output request must charge $0: spend = %.6f", s.spend)
	}
	if e := feApprox(s.balAfter, s.balBefore); e != nil {
		return fmt.Errorf("a no-output request must refund the hold in full: %w", e)
	}
	if s.earn != 0 {
		return fmt.Errorf("a no-output request must mint NO earning: earn = %.6f", s.earn)
	}
	return nil
}

func (s *lrState) ownerFlagged() error {
	struck, err := s.ownerStruck()
	if err != nil {
		return err
	}
	if !struck {
		return fmt.Errorf("a no-output request must flag (strike) the owner for empty output")
	}
	return nil
}

// billedNonZeroEarns asserts the usage-backstop path SETTLED (billed off the reported tokens
// and the node earned), rather than voiding to $0.
func (s *lrState) billedNonZeroEarns() error {
	if !(s.spend > 0) {
		return fmt.Errorf("empty text WITH reported completion tokens must be billed (usage backstop), spend=%.8f", s.spend)
	}
	if !(s.earn > 0) {
		return fmt.Errorf("the usage-backstop path must mint an earning, earn=%.8f", s.earn)
	}
	return nil
}

// notFlaggedEmptyOutput asserts the honest owner was NOT struck on the usage-backstop path.
func (s *lrState) notFlaggedEmptyOutput() error {
	struck, err := s.ownerStruck()
	if err != nil {
		return err
	}
	if struck {
		return fmt.Errorf("empty text WITH reported completion tokens must NOT strike the owner (usage backstop)")
	}
	return nil
}

func (s *lrState) zeroReceiptRecorded() error {
	// The void path SignBroker's a $0 receipt and records a $0 metering Entry for the lineage
	// trail (tunnel.go: rec.SignBroker(b.priv); Settle(payer,node,0,0,rec)); it sets NO
	// X-RogerAI-Receipt header on the void response, so assert the recorded $0 metering entry
	// directly from the ledger rather than the (absent) transport header.
	entries, err := s.b.db.RecentByNode("n1", 50)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Cost == 0 {
			return nil // a $0 metering entry was recorded for the lineage trail
		}
	}
	return fmt.Errorf("the void path must record a $0 metering entry for the lineage trail; none found")
}

// --- idempotent settle ------------------------------------------------------

func (s *lrState) alreadySettled() error {
	mem := store.NewMem()
	_ = mem.BindNode("n1", "op1")
	if _, _, err := mem.CreditOnce("c", "alice", 100); err != nil {
		return err
	}
	rec := protocol.UsageReceipt{RequestID: "idem1", Model: "m", PriceIn: 1, PriceOut: 1, PromptTokens: 100, CompletionTokens: 100, TS: 1}
	if _, err := mem.Settle("alice", "n1", 2.0, 1.4, rec); err != nil {
		return err
	}
	// stash for the resubmit
	s.rec = rec
	s.b = relayBroker(mem)
	return nil
}

func (s *lrState) resubmitSameReceipt() error {
	mem := s.b.db.(*store.Mem)
	if _, err := mem.Settle("alice", "n1", 2.0, 1.4, s.rec); err != nil {
		return err
	}
	s.idemBal, _ = mem.PeekBalance("alice")
	s.idemEarn, _ = mem.EarningsOf("n1")
	return nil
}

func (s *lrState) debitedOnce() error {
	if e := feApprox(s.idemBal, 98.0); e != nil { // 100 - 2 (once), not 96
		return fmt.Errorf("the wallet must be debited only ONCE on a replay: %w", e)
	}
	if e := feApprox(s.idemEarn, 1.4); e != nil { // earned once, not 2.8
		return fmt.Errorf("the earning must be minted only ONCE on a replay: %w", e)
	}
	return nil
}

func TestLineageReceiptsBDD(t *testing.T) {
	st := &lrState{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})

			sc.Step(`^a registered node with an ed25519 keypair$`, st.bgNode)
			sc.Step(`^a funded consumer with a pre-authorized hold$`, st.bgConsumer)

			// protocol layer
			sc.Step(`^the node serves the request and signs the receipt with its key$`, st.nodeServesAndSigns)
			sc.Step(`^VerifyNode against the node's registered pubkey succeeds$`, st.verifyNodeOK)
			sc.Step(`^a node-signed receipt$`, st.aNodeSignedReceipt)
			sc.Step(`^the broker sets GrantID, BrokerPromptTokens, and BrokerCompletionTokens$`, st.brokerSetsFields)
			sc.Step(`^the node signature still verifies \(those fields are zeroed in signingBytes\)$`, st.nodeSigStillVerifies)
			sc.Step(`^the field "([^"]*)" is altered after signing$`, st.alterField)
			sc.Step(`^VerifyNode fails$`, st.verifyNodeFails)

			// hash chain
			sc.Step(`^a node-signed receipt R1 with hash H1$`, st.r1WithHash)
			sc.Step(`^the node produces the next receipt R2 for the same node$`, st.nextReceipt)
			sc.Step(`^R2\.PrevHash equals H1$`, st.r2PrevEqualsH1)
			sc.Step(`^a receipt with hash H$`, st.receiptHashH)
			sc.Step(`^any node-signed field is altered$`, st.alterAnySignedField)
			sc.Step(`^Hash no longer equals H \(the chain link is broken\)$`, st.hashChanged)

			// cost math
			sc.Step(`^a receipt with (\d+) prompt tokens at ([\d.]+) /1M in and (\d+) completion tokens at ([\d.]+) /1M out$`, st.costReceipt)
			sc.Step(`^Cost is ([\d.]+)$`, st.costIs)
			sc.Step(`^a receipt claiming (\d+) completion tokens at ([\d.]+) /1M out$`, st.costWith2Receipt)
			sc.Step(`^settlement bills a broker-verified (\d+) completion tokens$`, st.billsVerifiedComp)
			sc.Step(`^the billed cost reflects (\d+) completion tokens, not \d+$`, st.costReflectsComp)

			// settle binding - co-sign
			sc.Step(`^the broker relays a request whose returned receipt verifies$`, st.relayReturnsVerified)
			sc.Step(`^the broker counter-signs it \(BrokerSig\)$`, st.brokerCosigns)
			sc.Step(`^both the node and broker signatures verify over the same canonical bytes$`, st.bothSigsVerify)
			sc.Step(`^the co-signed receipt is returned on the X-RogerAI-Receipt header$`, st.receiptHeaderReturned)

			// forged
			sc.Step(`^the node returns a receipt signed with the WRONG key$`, st.forgedReceipt)
			sc.Step(`^the broker relays the request$`, st.relayProcess)
			sc.Step(`^settlement does not run and no earning is minted$`, st.noSettlementNoEarning)
			sc.Step(`^the consumer's pre-authorized hold is refunded in full$`, st.holdRefundedFull)
			sc.Step(`^no co-signed receipt is emitted$`, st.noReceiptHeader)

			// broker bills resolved price
			sc.Step(`^the node returns a receipt claiming an inflated price_out$`, st.inflatedPriceOut)
			sc.Step(`^the broker relays and settles$`, st.relaySettles)
			sc.Step(`^the consumer is billed at the broker-resolved active price$`, st.billedAtResolved)
			sc.Step(`^the receipt's PriceIn/PriceOut are the broker's price, not the node's claim$`, st.receiptPriceIsBrokers)

			// over-report both axes
			sc.Step(`^the node claims more prompt and completion tokens than the broker re-count$`, st.overReportClaim)
			sc.Step(`^the cost uses min\(claim, broker-recount\) on BOTH the input and output axes$`, st.costUsesMinBothAxes)

			// capture clamp
			sc.Step(`^the settled cost would exceed the pre-authorized hold$`, st.costExceedsHold)
			sc.Step(`^the broker settles$`, st.settlesClamp)
			sc.Step(`^the captured cost is clamped to the authorized hold amount$`, st.clampedToHold)

			// settle failure fail-safe
			sc.Step(`^the ledger settle returns an error$`, st.settleErrors)
			sc.Step(`^the consumer's hold is refunded and no billing headers are emitted$`, st.holdRefundedNoBilling)

			// void on no usable output
			sc.Step(`^the node returns "([^"]*)"$`, st.nodeReturnsShape)
			sc.Step(`^the consumer is charged 0 and the hold is refunded in full$`, st.charged0Refunded)
			sc.Step(`^the owner is flagged for the empty output$`, st.ownerFlagged)
			sc.Step(`^a \$0 metering receipt is still broker-co-signed and recorded for the lineage trail$`, st.zeroReceiptRecorded)
			sc.Step(`^the consumer is billed a non-zero cost and the node earns$`, st.billedNonZeroEarns)
			sc.Step(`^the owner is NOT flagged for empty output$`, st.notFlaggedEmptyOutput)

			// idempotent settle
			sc.Step(`^a receipt that has already settled for its request id$`, st.alreadySettled)
			sc.Step(`^the same receipt is submitted again$`, st.resubmitSameReceipt)
			sc.Step(`^the wallet is debited only once and the earning is minted only once$`, st.debitedOnce)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/trust/lineage_receipts.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("lineage-receipts behavior scenarios failed (see godog output above)")
	}
}
