package main

// relay_spend_bdd_test.go makes features/relay/spend.feature EXECUTABLE under godog, driving
// the REAL priced relay money path end-to-end (no mocks): the spend is keyed off the SIGNED
// identity (never an X-Roger-User header), a hold pre-authorizes before dispatch (no
// overdraft), a served request settles (debit consumer + mint operator share + co-signed
// receipt), a request that never finds a station is not charged, and moderation gates the
// whole path BEFORE any node is paid.
//
// Spec correction (code is source of truth): the SIGNATURE-not-header scenario said the debit
// lands on "UserIDFromPubkey(signer)". That is the LOGGED-OUT form, but an anonymous keypair
// is rejected (401) on a PAID model, so a priced spend that actually settles is by a
// logged-in caller whose payer is the unified "u_gh_<id>" wallet (one wallet per account).
// The invariant the scenario protects - the SIGNATURE, never any X-Roger-User header, picks
// the payer - is asserted exactly: the signer's wallet is debited, the header value untouched.
//
// signReq lives in auth_test.go; relayBroker in enforce_test.go; feApprox in fee_splits_bdd_test.go.

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

type spState struct {
	// served-relay config
	unsigned  bool   // omit the request signature
	noStation bool   // request a model NO node offers
	flagMod   bool   // configure a flagging moderation backend
	xUser     string // an X-Roger-User header value (a victim id), if any
	model     string // requested model

	// results
	code      int
	spend     float64
	earn      float64
	hdrRcpt   string
	balBefore float64
	balAfter  float64
	victimBal float64

	// pure-hold scenario
	holdMem *store.Mem
	holdOK1 bool
	holdOK2 bool
	holdBal float64
}

func (s *spState) reset() {
	s.unsigned, s.noStation, s.flagMod = false, false, false
	s.xUser, s.model = "", "m"
	s.code, s.spend, s.earn, s.hdrRcpt = 0, 0, 0, ""
	s.balBefore, s.balAfter, s.victimBal = 0, 0, 0
	s.holdOK1, s.holdOK2, s.holdBal = false, false, 0
}

// run drives one priced relay round-trip with the configured node/consumer/moderation.
func (s *spState) run() error {
	mem := store.NewMem()
	b := relayBroker(mem)
	if s.flagMod {
		flag := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"results":[{"flagged":true}]}`))
		}))
		defer flag.Close()
		b.mod = moderation{provider: "url", url: flag.URL, client: flag.Client()}
	}

	nodePub, nodePriv, _ := ed25519.GenerateKey(nil)
	if !s.noStation {
		b.nodes["n1"] = protocol.NodeRegistration{
			NodeID: "n1", PubKey: hex.EncodeToString(nodePub),
			Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 1.0, PriceOut: 1.0, Ctx: 4096}},
		}
		b.lastSeen["n1"] = time.Now()
		tun := &nodeTunnel{jobs: make(chan protocol.Job, 1), waiters: map[string]chan protocol.JobResult{}}
		b.tunnels["n1"] = tun
		if err := mem.BindNode("n1", "op1"); err != nil {
			return err
		}
		go func() {
			job, ok := <-tun.jobs
			if !ok {
				return
			}
			rec := protocol.UsageReceipt{
				RequestID: job.ID, NodeID: "n1", Model: "m",
				PromptTokens: 12, CompletionTokens: 40, PriceIn: 1.0, PriceOut: 1.0, TS: time.Now().Unix(),
			}
			rec.SignNode(nodePriv)
			body := []byte(`{"choices":[{"message":{"role":"assistant","content":"a genuine answer for the consumer"}}]}`)
			res := protocol.JobResult{ID: job.ID, Status: 200, Body: body, Receipt: rec}
			tun.mu.Lock()
			ch := tun.waiters[job.ID]
			tun.mu.Unlock()
			if ch != nil {
				ch <- res
			}
		}()
	}

	_, userPriv, _ := ed25519.GenerateKey(nil)
	userPubHex := hex.EncodeToString(userPriv.Public().(ed25519.PublicKey))
	if err := mem.BindOwner(store.Owner{GitHubID: 7, Login: "alice", Pubkey: userPubHex}); err != nil {
		return err
	}
	if _, err := mem.AddCredits("u_gh_7", 100); err != nil {
		return err
	}
	s.balBefore, _ = mem.PeekBalance("u_gh_7")

	// Carry a real prompt so the moderation screen has text to evaluate (screen("") is a
	// no-op pass); harmless for the non-moderated scenarios.
	reqBody := []byte(fmt.Sprintf(`{"model":%q,"max_tokens":64,"messages":[{"role":"user","content":"please answer this"}]}`, s.model))
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(reqBody)))
	if !s.unsigned {
		signReq(r, userPriv, reqBody)
	}
	if s.xUser != "" {
		r.Header.Set("X-Roger-User", s.xUser)
	}
	w := httptest.NewRecorder()
	b.relay(w, r)

	s.code = w.Code
	s.hdrRcpt = w.Header().Get("X-RogerAI-Receipt")
	s.balAfter, _ = mem.PeekBalance("u_gh_7")
	s.spend, _ = mem.SpendOf("u_gh_7")
	s.earn, _ = mem.EarningsOf("n1")
	if s.xUser != "" {
		s.victimBal, _ = mem.PeekBalance(s.xUser)
	}
	return nil
}

// --- scenario 1: signature, not header, picks the payer ---------------------

func (s *spState) consumerSignsPriced() error { s.xUser = "u_victim"; return nil }
func (s *spState) relaySettles() error        { return s.run() }

func (s *spState) debitOnSigner() error {
	if !(s.spend > 0) {
		return fmt.Errorf("the signer's wallet must be debited for a served priced request; spend=%.6f (code=%d)", s.spend, s.code)
	}
	if s.victimBal != 0 {
		return fmt.Errorf("the X-Roger-User header value must NEVER be charged; victim balance=%.6f", s.victimBal)
	}
	return nil
}

// --- scenario 2: unsigned spend is rejected, victim untouched ---------------

func (s *spState) unsignedWithVictimHeader() error {
	s.unsigned = true
	s.xUser = "u_victim"
	return nil
}
func (s *spState) brokerProcesses() error { return s.run() }

func (s *spState) rejectedVictimUntouched() error {
	if s.code != http.StatusUnauthorized {
		return fmt.Errorf("an unsigned spend must be rejected (401, a spend always requires a signature); got %d", s.code)
	}
	if s.victimBal != 0 {
		return fmt.Errorf("the victim's wallet must be untouched; balance=%.6f", s.victimBal)
	}
	if s.spend != 0 {
		return fmt.Errorf("an unsigned, rejected request must spend nothing; spend=%.6f", s.spend)
	}
	return nil
}

// --- scenario 3: hold pre-authorizes, no overdraft (store-level) ------------

func (s *spState) walletBalance(v string) error {
	s.holdMem = store.NewMem()
	amt, err := feParseFloat(strings.TrimPrefix(v, "$"))
	if err != nil {
		return err
	}
	if _, _, err := s.holdMem.CreditOnce("c", "w", amt); err != nil {
		return err
	}
	return nil
}

func (s *spState) reserveAmount(v string) error {
	amt, err := feParseFloat(strings.TrimPrefix(v, "$"))
	if err != nil {
		return err
	}
	s.holdOK1, _ = s.holdMem.Hold("w", amt)
	s.holdBal, _ = s.holdMem.PeekBalance("w")
	// A SECOND reservation that would overdraft the remaining balance must be refused.
	s.holdOK2, _ = s.holdMem.Hold("w", 0.80)
	bal2, _ := s.holdMem.PeekBalance("w")
	s.holdBal = bal2
	return nil
}

func (s *spState) holdReservesNoNegative() error {
	if !s.holdOK1 {
		return fmt.Errorf("the first hold ($0.40 of $1.00) must succeed")
	}
	if s.holdOK2 {
		return fmt.Errorf("a hold that would overdraft ($0.80 of the remaining $0.60) must be REFUSED (no overdraft)")
	}
	if s.holdBal < 0 {
		return fmt.Errorf("the wallet must never go negative through the hold path; balance=%.6f", s.holdBal)
	}
	return nil
}

// --- scenario 4: a served request settles -----------------------------------

func (s *spState) servedSuccessfully() error { return nil }
func (s *spState) settlesCostShare() error   { return s.run() }

func (s *spState) debitMintReceipt() error {
	if !(s.spend > 0) {
		return fmt.Errorf("a served request must debit the consumer; spend=%.6f (code=%d)", s.spend, s.code)
	}
	wantEarn := s.spend * (1 - 0.30) // relayBroker fee is 30%
	if e := feApprox(s.earn, wantEarn); e != nil {
		return fmt.Errorf("the operator must earn the owner share: %w", e)
	}
	if s.hdrRcpt == "" {
		return fmt.Errorf("a served request must record + return a co-signed receipt (X-RogerAI-Receipt)")
	}
	return nil
}

// --- scenario 5: a request that never served is not charged -----------------

func (s *spState) noStationOrFailed() error { s.noStation = true; s.model = "nomodel"; return nil }
func (s *spState) relayReturnsError() error { return s.run() }

func (s *spState) holdReleasedNoSpend() error {
	if s.code == http.StatusOK {
		return fmt.Errorf("a request with no station must NOT succeed; got 200")
	}
	if e := feApprox(s.balAfter, s.balBefore); e != nil {
		return fmt.Errorf("a request that never served must release the hold (balance unchanged): %w", e)
	}
	if s.spend != 0 {
		return fmt.Errorf("a request that never served must record no spend; spend=%.6f", s.spend)
	}
	return nil
}

// --- scenario 6: moderation gates before any node is paid --------------------

func (s *spState) moderationFlagged() error { s.flagMod = true; return nil }
func (s *spState) relayRuns() error         { return s.run() }

func (s *spState) rejectedBeforeDispatchNoCharge() error {
	if s.code != http.StatusUnavailableForLegalReasons {
		return fmt.Errorf("a flagged prompt must be rejected (451) before dispatch; got %d", s.code)
	}
	if s.spend != 0 {
		return fmt.Errorf("a moderation-rejected request must charge nothing; spend=%.6f", s.spend)
	}
	if e := feApprox(s.balAfter, s.balBefore); e != nil {
		return fmt.Errorf("a moderation-rejected request must place no captured hold: %w", e)
	}
	if s.earn != 0 {
		return fmt.Errorf("no node may be paid for a moderation-rejected request; earn=%.6f", s.earn)
	}
	return nil
}

func TestRelaySpendBDD(t *testing.T) {
	st := &spState{}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			sc.Step(`^a consumer signs a priced relay request with their key$`, st.consumerSignsPriced)
			sc.Step(`^the relay settles$`, st.relaySettles)
			sc.Step(`^the debit lands on UserIDFromPubkey\(signer\), regardless of any X-Roger-User header$`, st.debitOnSigner)

			sc.Step(`^a request that sets X-Roger-User to a victim's id but carries NO valid signature$`, st.unsignedWithVictimHeader)
			sc.Step(`^the broker processes the relay$`, st.brokerProcesses)
			sc.Step(`^it is rejected \(a spend ALWAYS requires a signature\) and the victim's wallet is untouched$`, st.rejectedVictimUntouched)

			sc.Step(`^a wallet with balance \$([\d.]+)$`, st.walletBalance)
			sc.Step(`^a relay needs to reserve \$([\d.]+)$`, st.reserveAmount)
			sc.Step(`^a hold reserves it before dispatch, and the wallet can never go negative through this path$`, st.holdReservesNoNegative)

			sc.Step(`^a held request that the node serves successfully$`, st.servedSuccessfully)
			sc.Step(`^it settles for cost C with owner share S$`, st.settlesCostShare)
			sc.Step(`^the consumer wallet is debited C, the operator earns S, and a receipt is recorded$`, st.debitMintReceipt)

			sc.Step(`^a hold was placed but dispatch found no station / failed before serving$`, st.noStationOrFailed)
			sc.Step(`^the relay returns its error$`, st.relayReturnsError)
			sc.Step(`^the hold is released and no spend is recorded \(you pay only for served tokens\)$`, st.holdReleasedNoSpend)

			sc.Step(`^moderation is required and a prompt is flagged$`, st.moderationFlagged)
			sc.Step(`^the relay runs$`, st.relayRuns)
			sc.Step(`^it is rejected before dispatch \(no hold settles, no node serves, no charge\)$`, st.rejectedBeforeDispatchNoCharge)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/relay/spend.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("relay spend behavior scenarios failed (see godog output above)")
	}
}
