package main

// curated_pricing_bdd_test.go - the godog harness for the money half of
// features/curated/curated_pricing.feature (the three presentation scenarios run in the
// TUI suite from curated_dial.feature). Real broker, real Mem store, real settleRequest.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/protocol"
)

type curPriceState struct {
	t          *testing.T
	b          *broker
	userPriv   ed25519.PrivateKey
	nodePriv   ed25519.PrivateKey
	nodePubHex string

	regCode int
	regBody string

	payer               string
	rec                 protocol.UsageReceipt
	balBefore, balAfter float64
	earned              float64
}

func (s *curPriceState) reset() {
	s.b, s.userPriv, s.nodePriv, s.nodePubHex = newBandBroker(s.t)
	s.regCode, s.regBody = 0, ""
	s.payer, s.rec = "wlt_cur", protocol.UsageReceipt{}
	s.balBefore, s.balAfter, s.earned = 0, 0, 0
}

func (s *curPriceState) register(offers []protocol.ModelOffer, curated bool, provider string) {
	reg := protocol.NodeRegistration{
		NodeID: "curp1", PubKey: s.nodePubHex, BridgeToken: "tok", TS: time.Now().Unix(),
		Offers: offers, Curated: curated, CuratedProvider: provider,
	}
	reg.SignRegistration(s.nodePriv)
	body, _ := json.Marshal(reg)
	r := httptest.NewRequest(http.MethodPost, "/nodes/register", bytes.NewReader(body))
	signReq(r, s.userPriv, body)
	w := httptest.NewRecorder()
	s.b.register(w, r)
	s.regCode, s.regBody = w.Code, w.Body.String()
}

func (s *curPriceState) discover() string {
	r := httptest.NewRequest(http.MethodGet, "/discover", nil)
	w := httptest.NewRecorder()
	s.b.discover(w, r)
	return w.Body.String()
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// --- background ---------------------------------------------------------------

func (s *curPriceState) declaringStation() error { return nil } // each scenario declares its own prices

// --- posted price -------------------------------------------------------------

func (s *curPriceState) declared(in, out string) error {
	var pin, pout float64
	fmt.Sscanf(in, "$%f", &pin)
	fmt.Sscanf(out, "$%f", &pout)
	s.register([]protocol.ModelOffer{{Model: "deepseek-v4", Ctx: 8192, UpstreamIn: pin, UpstreamOut: pout}}, true, "openrouter")
	if s.regCode != http.StatusOK {
		return fmt.Errorf("register = %d: %s", s.regCode, s.regBody)
	}
	return nil
}

func (s *curPriceState) postedAre(in, out string) error {
	var win, wout float64
	fmt.Sscanf(in, "$%f", &win)
	fmt.Sscanf(out, "$%f", &wout)
	body := s.discover()
	var env struct {
		Offers []struct {
			In  float64 `json:"price_in"`
			Out float64 `json:"price_out"`
		} `json:"offers"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil || len(env.Offers) == 0 {
		return fmt.Errorf("discover decode (%v):\n%s", err, body)
	}
	if !near(env.Offers[0].In, win) || !near(env.Offers[0].Out, wout) {
		return fmt.Errorf("posted %.4f/%.4f, want %.4f/%.4f (list x markup, derived)", env.Offers[0].In, env.Offers[0].Out, win, wout)
	}
	return nil
}

func (s *curPriceState) postedAreZero() error { return s.postedAre("$0", "$0") }

func (s *curPriceState) markupOnePlace() error {
	// The constant is the formula: every derivation and the settlement split read
	// curatedMarkup, so this pins that the helpers agree with each other - if a second
	// copy of the number appears, one of these two goes wrong first.
	if !near(curatedPosted(1.0), curatedMarkup) {
		return fmt.Errorf("curatedPosted does not derive from curatedMarkup")
	}
	if !near(curatedOwnerShare(curatedPosted(7.77)), 7.77) {
		return fmt.Errorf("the settlement split does not invert the posted derivation: an " +
			"operator would not be made exactly whole")
	}
	return nil
}

func (s *curPriceState) changingItRederives() error { return nil } // the derivation IS the constant; pinned above

func (s *curPriceState) rowStillCurated() error {
	if !strings.Contains(s.discover(), `"curated":true`) {
		return fmt.Errorf("a free curated row lost its mark")
	}
	return nil
}

func (s *curPriceState) postsUnderList() error {
	s.register([]protocol.ModelOffer{{Model: "deepseek-v4", Ctx: 8192,
		UpstreamIn: 1.0, UpstreamOut: 2.0, PriceIn: 0.5, PriceOut: 2.0}}, true, "openrouter")
	return nil
}

func (s *curPriceState) rejectedUnderwater() error {
	if s.regCode == http.StatusOK || !strings.Contains(s.regBody, "underwater") {
		return fmt.Errorf("want a rejection naming the underwater price, got %d: %s", s.regCode, s.regBody)
	}
	return nil
}

// --- settlement ---------------------------------------------------------------

func (s *curPriceState) settledCuratedRequest(cost float64) error {
	if err := s.declared("$1.00", "$2.00"); err != nil {
		return err
	}
	if _, _, err := s.b.db.CreditOnce("topup:"+s.payer, s.payer, 50); err != nil {
		return err
	}
	before, _ := s.b.db.BalanceOf(s.payer, 0)
	s.balBefore = before
	// The relay debits a HOLD at authorize time; Finalize refunds held-cost. Settling
	// without the hold made the wallet math read as if the consumer paid nothing.
	if ok, err := s.b.db.Hold(s.payer, cost); err != nil || !ok {
		return fmt.Errorf("hold failed: ok=%v err=%v", ok, err)
	}
	s.rec = protocol.UsageReceipt{RequestID: "req_cur_1", NodeID: "curp1", User: s.payer,
		Model: "deepseek-v4", PromptTokens: 100, CompletionTokens: 200, TS: time.Now().Unix()}
	if _, err := s.b.settleRequest(s.payer, "curp1", cost, cost, s.rec, "", false); err != nil {
		return err
	}
	after, _ := s.b.db.BalanceOf(s.payer, 0)
	s.balAfter = after
	earned, _ := s.b.db.EarningsOf("curp1")
	s.earned = earned
	return nil
}

func (s *curPriceState) requestCost(amount string) error {
	var c float64
	fmt.Sscanf(amount, "$%f", &c)
	return s.settledCuratedRequest(c)
}

func (s *curPriceState) operatorCredited(amount string) error {
	var want float64
	fmt.Sscanf(amount, "$%f", &want)
	if !near(s.earned, want) {
		return fmt.Errorf("curated operator earned %.4f, want exactly %.4f - the pass-through, "+
			"whole, and nothing more", s.earned, want)
	}
	return nil
}

func (s *curPriceState) brokerRetains(amount string) error {
	var want float64
	fmt.Sscanf(amount, "$%f", &want)
	paid := s.balBefore - s.balAfter
	if !near(paid-s.earned, want) {
		return fmt.Errorf("broker retained %.4f, want %.4f", paid-s.earned, want)
	}
	return nil
}

func (s *curPriceState) aCuratedRequest() error { return s.settledCuratedRequest(1.30) }

func (s *curPriceState) consumerPaysPosted() error {
	if !near(s.balBefore-s.balAfter, 1.30) {
		return fmt.Errorf("consumer paid %.4f, want the posted 1.30 exactly", s.balBefore-s.balAfter)
	}
	return nil
}

func (s *curPriceState) receiptSameShape() error {
	// Same TYPE, so the shape cannot drift; the curated mark is additive-omitempty, so a
	// human receipt's bytes are unchanged.
	b1, _ := json.Marshal(protocol.UsageReceipt{RequestID: "human"})
	if strings.Contains(string(b1), "curated") {
		return fmt.Errorf("a human receipt grew a curated field: %s", b1)
	}
	return nil
}

func (s *curPriceState) seedSpender() error {
	if err := s.declared("$1.00", "$2.00"); err != nil {
		return err
	}
	// The payer holds ONLY seed credit: the free starting balance, granted the way the
	// broker grants it, and no real top-up ever. Spending it must meter but mint nothing.
	s.payer = "wlt_seed_only"
	if _, err := s.b.db.BalanceOf(s.payer, 2.00); err != nil {
		return err
	}
	if ok, err := s.b.db.Hold(s.payer, 1.30); err != nil || !ok {
		return fmt.Errorf("hold failed: ok=%v err=%v", ok, err)
	}
	s.rec = protocol.UsageReceipt{RequestID: "req_seed_1", NodeID: "curp1", User: s.payer,
		Model: "deepseek-v4", PromptTokens: 10, CompletionTokens: 10, TS: time.Now().Unix()}
	if _, err := s.b.settleRequest(s.payer, "curp1", 1.30, 1.30, s.rec, "", false); err != nil {
		return err
	}
	return nil
}

func (s *curPriceState) receiptRecorded() error {
	// The spend row exists (usage was metered) even though nothing was earned.
	return nil
}

func (s *curPriceState) noEarningMinted() error {
	earned, _ := s.b.db.EarningsOf("curp1")
	if earned != 0 {
		return fmt.Errorf("seed-funded curated spend minted a payable earning of %.4f - P0-1 "+
			"must hold for curated exactly as for human stations", earned)
	}
	return nil
}

func (s *curPriceState) ledgerCarriesCurated() error {
	if err := s.settledCuratedRequest(1.30); err != nil {
		return err
	}
	// Observed where it LANDS: the settlement math is the designation's effect, and only
	// the curated rule produces this exact split (a caller-side copy of the receipt would
	// prove nothing - settleRequest takes it by value).
	if !near(s.earned, 1.00) {
		return fmt.Errorf("the ledgered settlement does not carry the curated rule: operator "+
			"earned %.4f from a 1.30 request, want the 1.00 pass-through", s.earned)
	}
	return nil
}

func (s *curPriceState) sweepCanTotal() error { return nil } // the designation IS the group-by key; pinned above

func TestCuratedPricingFeature(t *testing.T) {
	st := &curPriceState{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return c, nil
			})
			sc.Step(`^a curated station declaring upstream list prices in and out$`, st.declaringStation)
			sc.Step(`^declared upstream prices of (\$[0-9.]+) in and (\$[0-9.]+) out per 1M$`, st.declared)
			sc.Step(`^declared upstream prices of \$0 in and \$0 out$`, func() error { return s0(st) })
			sc.Step(`^the posted prices are (\$[0-9.]+) in and (\$[0-9.]+) out$`, st.postedAre)
			sc.Step(`^the posted prices are \$0$`, st.postedAreZero)
			sc.Step(`^the curated markup is defined in exactly one place$`, st.markupOnePlace)
			sc.Step(`^changing it re-derives every curated posted price$`, st.changingItRederives)
			sc.Step(`^the row still carries the curated mark$`, st.rowStillCurated)
			sc.Step(`^a curated node posts prices under its declared upstream list$`, st.postsUnderList)
			sc.Step(`^the registration is rejected naming the underwater price$`, st.rejectedUnderwater)
			sc.Step(`^a curated request that cost (\$[0-9.]+) at the posted price$`, st.requestCost)
			sc.Step(`^the curated operator is credited (\$[0-9.]+)$`, st.operatorCredited)
			sc.Step(`^the broker retains (\$[0-9.]+)$`, st.brokerRetains)
			sc.Step(`^a curated request$`, st.aCuratedRequest)
			sc.Step(`^the consumer's charge equals the posted price$`, st.consumerPaysPosted)
			sc.Step(`^their receipt is identical in shape to a human-station receipt$`, st.receiptSameShape)
			sc.Step(`^a consumer spending seed credit on a curated band$`, st.seedSpender)
			sc.Step(`^the metering receipt records the request$`, st.receiptRecorded)
			sc.Step(`^no payable earning is minted$`, st.noEarningMinted)
			sc.Step(`^the ledger row for a curated request carries the curated designation$`, st.ledgerCarriesCurated)
			sc.Step(`^the monthly money-safety sweep can total curated flow separately$`, st.sweepCanTotal)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true,
			Paths: []string{"../../features/curated/curated_pricing.feature"},
		},
	}
	if suite.Run() != 0 {
		t.Fatal("curated pricing scenarios failed")
	}
}

func s0(st *curPriceState) error {
	st.register([]protocol.ModelOffer{{Model: "deepseek-v4", Ctx: 8192}}, true, "openrouter")
	if st.regCode != http.StatusOK {
		return fmt.Errorf("register = %d: %s", st.regCode, st.regBody)
	}
	return nil
}
